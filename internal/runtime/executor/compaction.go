package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/zvuk/pipelineai/internal/runtime/llm"
	"github.com/zvuk/pipelineai/internal/runtime/tokens"
	"github.com/zvuk/pipelineai/pkg/dsl"
)

const (
	compactPrompt              = "Вы выполняете КОМПАКТИЗАЦИЮ КОНТЕКСТНОЙ КОНТРОЛЬНОЙ ТОЧКИ. Подготовьте сводку-передачу для другой LLM, которая продолжит задачу.\n\nВключите:\n- Текущий прогресс и ключевые принятые решения\n- Важный контекст, ограничения и предпочтения пользователя\n- Что осталось сделать дальше (понятные следующие шаги)\n- Любые критически важные данные, примеры или ссылки, нужные для продолжения\n\nПишите кратко, структурированно и так, чтобы следующая LLM могла бесшовно продолжить работу.\n"
	compactSummaryPrefix       = "Другая языковая модель уже начала решать эту задачу и подготовила сводку по проделанной работе. Вам также доступно состояние инструментов, которые она использовала. Используйте эту информацию, чтобы продолжить уже сделанное и не дублировать работу. Ниже приведена сводка от предыдущей модели; опирайтесь на неё в дальнейшем анализе:"
	compactionPromptTag        = "SUMMARIZATION_PROMPT"
	compactionHistoryTag       = "COMPACTION_HISTORY"
	compactionSummaryTag       = "COMPACTION_SUMMARY"
	compactionHistoryLeadIn    = "Сожми следующую историю диалога для продолжения работы в следующем ходе."
	compactionSummarySeparator = "\n"
)

func (e *Executor) maybeCompactContext(
	ctx context.Context,
	req *llm.ChatCompletionRequest,
	step *dsl.Step,
	tracker *promptTokenTracker,
	metrics *stepTokenMetrics,
) error {
	if req == nil || tracker == nil || metrics == nil {
		return nil
	}
	if metrics.AutoCompactThreshold <= 0 || tracker.ContextWindow() <= 0 {
		metrics.syncTracker(tracker)
		return nil
	}

	for attempt := 0; attempt < 3; attempt++ {
		metrics.syncTracker(tracker)
		if tracker.EstimatedNextPromptTokens() < metrics.AutoCompactThreshold {
			return nil
		}

		compacted, changed, usage, err := e.compactMessages(ctx, *req, tracker.profile)
		if err != nil {
			return err
		}
		if !changed {
			if tracker.EstimatedNextPromptTokens() >= tracker.ContextWindow() {
				return fmt.Errorf(
					"executor: estimated prompt %d tokens exceeds model context window %d and there is not enough old history to compact",
					tracker.EstimatedNextPromptTokens(),
					tracker.ContextWindow(),
				)
			}
			return nil
		}

		req.Messages = compacted
		tracker.ResetToRequest(*req)
		metrics.Compactions++
		metrics.recordResponse(llm.ChatCompletionResponse{Usage: usage})
		e.log.InfoContext(ctx, "context compacted",
			slog.String("step", step.ID),
			slog.Int("estimated_tokens_after_compaction", tracker.EstimatedNextPromptTokens()),
		)
	}

	return nil
}

func (e *Executor) compactMessages(
	ctx context.Context,
	req llm.ChatCompletionRequest,
	profile tokens.ModelProfile,
) ([]llm.Message, bool, llm.Usage, error) {
	head, compactable, tail := splitMessagesForCompaction(req.Messages)
	if len(compactable) == 0 {
		return req.Messages, false, llm.Usage{}, nil
	}

	blocks := renderCompactionBlocks(compactable)
	if len(blocks) == 0 {
		return req.Messages, false, llm.Usage{}, nil
	}
	budget := profile.ContextWindow / 2
	if budget <= 0 {
		budget = tokens.DefaultFallbackContextWindow / 2
	}
	blocks = trimCompactionBlocks(e.tokenizer, profile, blocks, budget)
	if len(blocks) == 0 {
		return req.Messages, false, llm.Usage{}, nil
	}

	compactReq := llm.ChatCompletionRequest{
		Model: req.Model,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: compactPrompt},
			{
				Role:    llm.RoleUser,
				Content: buildCompactionRequestContent(req.Model, blocks),
			},
		},
	}

	maxTokens := 2048
	compactReq.MaxTokens = &maxTokens

	resp, err := e.client.CreateChatCompletion(ctx, compactReq)
	if err != nil {
		return nil, false, llm.Usage{}, fmt.Errorf("executor: compact request failed: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, false, resp.Usage, fmt.Errorf("executor: compact request returned no choices")
	}

	summary := strings.TrimSpace(resp.Choices[0].Message.Content)
	if summary == "" {
		return nil, false, resp.Usage, fmt.Errorf("executor: compact request returned an empty summary")
	}

	compacted := make([]llm.Message, 0, len(head)+1+len(tail))
	compacted = append(compacted, head...)
	compacted = append(compacted, llm.Message{
		Role:    llm.RoleUser,
		Content: buildCompactionSummaryContent(req.Model, summary),
	})
	compacted = append(compacted, tail...)
	return compacted, true, resp.Usage, nil
}

func buildCompactionRequestContent(model string, blocks []string) string {
	history := strings.TrimSpace(strings.Join(blocks, "\n\n"))
	if !isGPTOSSModel(model) {
		return compactionHistoryLeadIn + "\n\n" + history
	}

	var b strings.Builder
	b.WriteString(xmlBlock(compactionPromptTag, compactionHistoryLeadIn))
	if history != "" {
		b.WriteString("\n\n")
		b.WriteString(xmlBlock(compactionHistoryTag, history))
	}
	return b.String()
}

func buildCompactionSummaryContent(model string, summary string) string {
	body := compactSummaryPrefix + compactionSummarySeparator + strings.TrimSpace(summary)
	if !isGPTOSSModel(model) {
		return body
	}
	return xmlBlock(compactionSummaryTag, body)
}

func xmlBlock(tag string, content string) string {
	tag = strings.TrimSpace(tag)
	content = strings.TrimSpace(content)
	if tag == "" {
		return content
	}
	if content == "" {
		return "<" + tag + "></" + tag + ">"
	}

	var b strings.Builder
	b.WriteString("<")
	b.WriteString(tag)
	b.WriteString(">\n")
	b.WriteString(content)
	b.WriteString("\n</")
	b.WriteString(tag)
	b.WriteString(">")
	return b.String()
}

func splitMessagesForCompaction(messages []llm.Message) (head []llm.Message, compactable []llm.Message, tail []llm.Message) {
	if len(messages) == 0 {
		return nil, nil, nil
	}

	split := 0
	for split < len(messages) && messages[split].Role == llm.RoleSystem {
		split++
	}
	head = append(head, messages[:split]...)
	rest := messages[split:]
	if len(rest) <= 4 {
		return head, nil, rest
	}

	tailStart := len(rest) - 4
	if rest[tailStart].Role == llm.RoleTool && tailStart > 0 {
		tailStart--
	}
	if tailStart > 0 && rest[tailStart-1].Role == llm.RoleAssistant && len(rest[tailStart-1].ToolCalls) > 0 {
		tailStart--
	}
	if tailStart <= 0 {
		return head, nil, rest
	}

	compactable = append(compactable, rest[:tailStart]...)
	tail = append(tail, rest[tailStart:]...)
	return head, compactable, tail
}

func renderCompactionBlocks(messages []llm.Message) []string {
	out := make([]string, 0, len(messages))
	for i, msg := range messages {
		var b strings.Builder
		fmt.Fprintf(&b, "## Message %d\nrole: %s\n", i+1, msg.Role)
		if strings.TrimSpace(msg.Name) != "" {
			fmt.Fprintf(&b, "name: %s\n", strings.TrimSpace(msg.Name))
		}
		if strings.TrimSpace(msg.ToolCallID) != "" {
			fmt.Fprintf(&b, "tool_call_id: %s\n", strings.TrimSpace(msg.ToolCallID))
		}
		if len(msg.ToolCalls) > 0 {
			fmt.Fprintf(&b, "tool_calls: %s\n", crop(mustJSON(msg.ToolCalls), 2000))
		}
		if msg.FunctionCall != nil {
			fmt.Fprintf(&b, "function_call: %s\n", crop(mustJSON(msg.FunctionCall), 2000))
		}
		if txt := strings.TrimSpace(msg.Reasoning); txt != "" {
			fmt.Fprintf(&b, "reasoning:\n%s\n", txt)
		}
		if txt := strings.TrimSpace(msg.Content); txt != "" {
			fmt.Fprintf(&b, "content:\n%s\n", txt)
		}
		rendered := strings.TrimSpace(b.String())
		if rendered != "" {
			out = append(out, rendered)
		}
	}
	return out
}

func trimCompactionBlocks(counter tokens.Counter, profile tokens.ModelProfile, blocks []string, budget int) []string {
	if len(blocks) == 0 {
		return nil
	}

	out := append([]string(nil), blocks...)
	for len(out) > 0 {
		text := strings.Join(out, "\n\n")
		estimate := counter.CountText(profile.RequestedModel, &profile.ContextWindow, text)
		if estimate.Tokens <= budget {
			return out
		}
		out = out[1:]
	}
	return nil
}

func mustJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}
