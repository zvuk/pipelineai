package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/zvuk/pipelineai/internal/runtime/llm"
	"github.com/zvuk/pipelineai/internal/runtime/tokens"
	"github.com/zvuk/pipelineai/internal/tools/registry"
	"github.com/zvuk/pipelineai/pkg/dsl"
)

const (
	compactPrompt              = "Вы выполняете КОМПАКТНУЮ ПЕРЕДАЧУ РАБОЧЕГО КОНТЕКСТА для следующей LLM, которая продолжит эту же задачу.\n\nПодготовьте короткую, но операционную сводку. Обязательно сохраните:\n- текущий прогресс и уже принятые решения;\n- факты, ограничения, предпочтения пользователя и важные договорённости;\n- что уже проверено, какие гипотезы подтверждены или отброшены;\n- какие действия или шаги ещё остались;\n- все важные `capture_ref` / пути к сохранённым большим результатам инструментов и как их читать узко.\n\nНе пересказывайте диалог подряд. Дайте рабочую память, с которой следующая модель сможет продолжить задачу без повторного блуждания.\n"
	compactSummaryPrefix       = "Ниже приведена рабочая сводка от предыдущей модели, которая уже выполняла эту задачу. Используйте её как оперативную память: продолжайте уже начатую работу, не повторяйте широкие поиски и учитывайте сохранённые `capture_ref`, ограничения и незавершённые шаги."
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

	fitLimit := requestFitLimit(tracker.ContextWindow(), metrics.ResponseReserveTokens)
	targetLimit := metrics.CompactTargetTokens
	if targetLimit <= 0 || targetLimit > fitLimit {
		targetLimit = fitLimit
	}

	for attempt := 0; attempt < 3; attempt++ {
		metrics.syncTracker(tracker)
		estimate := tracker.EstimatedNextPromptTokens()
		if estimate <= fitLimit && estimate < metrics.AutoCompactThreshold {
			return nil
		}

		compacted, changed, usage, err := e.compactMessages(ctx, *req, tracker.profile, targetLimit, fitLimit)
		if err != nil {
			metrics.recordBudgetExceeded(err.Error())
			return err
		}
		if !changed {
			if estimate > fitLimit {
				err := fmt.Errorf(
					"executor: estimated prompt %d tokens exceeds fit limit %d and there is not enough history left to compact safely",
					estimate,
					fitLimit,
				)
				metrics.recordBudgetExceeded(err.Error())
				return err
			}
			return nil
		}

		req.Messages = compacted
		tracker.ResetToRequest(*req)
		metrics.Compactions++
		metrics.Requests++
		metrics.recordUsage(usage)
		metrics.syncTracker(tracker)
		e.log.InfoContext(ctx, "context compacted",
			slog.String("step", step.ID),
			slog.Int("estimated_tokens_after_compaction", tracker.EstimatedNextPromptTokens()),
		)

		if tracker.EstimatedNextPromptTokens() <= fitLimit && tracker.EstimatedNextPromptTokens() <= targetLimit {
			return nil
		}
	}

	metrics.syncTracker(tracker)
	if tracker.EstimatedNextPromptTokens() > fitLimit {
		err := fmt.Errorf(
			"executor: prompt still exceeds fit limit after compaction: estimated %d tokens, fit limit %d, context window %d",
			tracker.EstimatedNextPromptTokens(),
			fitLimit,
			tracker.ContextWindow(),
		)
		metrics.recordBudgetExceeded(err.Error())
		return err
	}
	return nil
}

func (e *Executor) compactMessages(
	ctx context.Context,
	req llm.ChatCompletionRequest,
	profile tokens.ModelProfile,
	targetLimit int,
	fitLimit int,
) ([]llm.Message, bool, llm.Usage, error) {
	if len(req.Messages) == 0 {
		return nil, false, llm.Usage{}, nil
	}

	head, compactable, tail := splitMessagesForCompaction(req.Messages)
	if len(compactable) == 0 {
		compacted, changed := fitCompactedConversation(e.tokenizer, profile, head, nil, tail, targetLimit, fitLimit, req.Model)
		return compacted, changed, llm.Usage{}, nil
	}

	blocks := renderCompactionBlocks(compactable)
	if len(blocks) == 0 {
		return req.Messages, false, llm.Usage{}, nil
	}

	inputBudget := fitLimit / 2
	if inputBudget <= 0 {
		inputBudget = tokens.DefaultFallbackContextWindow / 2
	}
	blocks = trimCompactionBlocks(e.tokenizer, profile, blocks, inputBudget)
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

	summaryMsg := llm.Message{
		Role:    llm.RoleUser,
		Content: buildCompactionSummaryContent(req.Model, summary),
	}
	compacted, changed := fitCompactedConversation(e.tokenizer, profile, head, &summaryMsg, tail, targetLimit, fitLimit, req.Model)
	return compacted, changed, resp.Usage, nil
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
	for i := len(rest) - 1; i >= 0; i-- {
		if rest[i].Role == llm.RoleUser {
			if i > 0 {
				tailStart = i
			}
			break
		}
	}
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

func fitCompactedConversation(
	counter tokens.Counter,
	profile tokens.ModelProfile,
	head []llm.Message,
	summary *llm.Message,
	tail []llm.Message,
	targetLimit int,
	fitLimit int,
	model string,
) ([]llm.Message, bool) {
	out := make([]llm.Message, 0, len(head)+1+len(tail))
	out = append(out, head...)
	if summary != nil {
		out = append(out, *summary)
	}

	baseEstimate := counter.EstimateMessages(profile.RequestedModel, &profile.ContextWindow, out)
	if targetLimit <= 0 || targetLimit > fitLimit {
		targetLimit = fitLimit
	}
	if fitLimit > 0 && baseEstimate.Tokens > fitLimit {
		if summary != nil {
			trimmed, ok := shrinkMessageForBudget(counter, profile, *summary, fitLimit-counter.EstimateMessages(profile.RequestedModel, &profile.ContextWindow, head).Tokens, model)
			if ok {
				out = append(append([]llm.Message(nil), head...), trimmed)
				baseEstimate = counter.EstimateMessages(profile.RequestedModel, &profile.ContextWindow, out)
			}
		}
	}

	selectedTail := fitTailMessages(counter, profile, out, tail, targetLimit, fitLimit, model)
	changed := len(selectedTail) != len(tail) || summary != nil
	out = append(out, selectedTail...)
	return out, changed
}

func fitTailMessages(
	counter tokens.Counter,
	profile tokens.ModelProfile,
	base []llm.Message,
	tail []llm.Message,
	targetLimit int,
	fitLimit int,
	model string,
) []llm.Message {
	if len(tail) == 0 {
		return nil
	}
	if targetLimit <= 0 || targetLimit > fitLimit {
		targetLimit = fitLimit
	}

	selected := make([]llm.Message, 0, len(tail))
	baseTokens := counter.EstimateMessages(profile.RequestedModel, &profile.ContextWindow, base).Tokens
	currentTokens := baseTokens
	for i := len(tail) - 1; i >= 0; i-- {
		msg := tail[i]
		estimate := counter.EstimateMessage(profile.RequestedModel, &profile.ContextWindow, msg)
		if currentTokens+estimate.Tokens <= targetLimit {
			selected = append([]llm.Message{msg}, selected...)
			currentTokens += estimate.Tokens
			continue
		}

		budget := targetLimit - currentTokens
		trimmed, ok := shrinkMessageForBudget(counter, profile, msg, budget, model)
		if !ok {
			continue
		}
		trimmedEstimate := counter.EstimateMessage(profile.RequestedModel, &profile.ContextWindow, trimmed)
		if currentTokens+trimmedEstimate.Tokens > fitLimit {
			continue
		}
		selected = append([]llm.Message{trimmed}, selected...)
		currentTokens += trimmedEstimate.Tokens
	}
	return selected
}

func shrinkMessageForBudget(
	counter tokens.Counter,
	profile tokens.ModelProfile,
	msg llm.Message,
	budget int,
	model string,
) (llm.Message, bool) {
	if budget <= 0 {
		return llm.Message{}, false
	}
	if msg.Role == llm.RoleTool {
		if shrunk, ok := shrinkToolMessageForBudget(counter, profile, msg, budget); ok {
			return shrunk, true
		}
	}

	content := strings.TrimSpace(msg.Content)
	if content == "" {
		return llm.Message{}, false
	}
	trimmed := msg
	prefix := "Context trimmed to fit the remaining token budget.\n\n"
	candidates := []int{1200, 800, 400, 200}
	for _, maxChars := range candidates {
		trimmed.Content = prefix + crop(content, maxChars)
		estimate := counter.EstimateMessage(profile.RequestedModel, &profile.ContextWindow, trimmed)
		if estimate.Tokens <= budget {
			return trimmed, true
		}
	}

	trimmed.Content = prefix + crop(content, 80)
	if estimate := counter.EstimateMessage(profile.RequestedModel, &profile.ContextWindow, trimmed); estimate.Tokens <= budget {
		return trimmed, true
	}
	return llm.Message{}, false
}

func shrinkToolMessageForBudget(
	counter tokens.Counter,
	profile tokens.ModelProfile,
	msg llm.Message,
	budget int,
) (llm.Message, bool) {
	var out registry.ExecResult
	if err := json.Unmarshal([]byte(msg.Content), &out); err != nil {
		return llm.Message{}, false
	}

	preview := buildToolResultPreview(out, 800)
	if preview == "" {
		preview = crop(strings.TrimSpace(msg.Content), 400)
	}

	compacted := registry.ExecResult{
		Tool:             out.Tool,
		Ok:               out.Ok,
		ExitCode:         out.ExitCode,
		Summary:          out.Summary,
		Added:            out.Added,
		Modified:         out.Modified,
		Deleted:          out.Deleted,
		ElapsedMs:        out.ElapsedMs,
		NewWorkdir:       out.NewWorkdir,
		ToolError:        out.ToolError,
		Warning:          "Tool message was compacted to keep the dialog within the context budget.",
		Suppressed:       true,
		HardSuppressed:   true,
		Preview:          preview,
		CaptureRef:       out.CaptureRef,
		ArtifactPath:     out.ArtifactPath,
		CaptureKind:      out.CaptureKind,
		CapturePersisted: out.CapturePersisted,
		SuggestedReads:   out.SuggestedReads,
		EstimatedTokens:  out.EstimatedTokens,
		ThresholdTokens:  out.ThresholdTokens,
	}

	for _, maxChars := range []int{800, 400, 200, 120, 80} {
		compacted.Preview = crop(preview, maxChars)
		data, err := json.Marshal(compacted)
		if err != nil {
			return llm.Message{}, false
		}
		trimmed := msg
		trimmed.Content = string(data)
		estimate := counter.EstimateMessage(profile.RequestedModel, &profile.ContextWindow, trimmed)
		if estimate.Tokens <= budget {
			return trimmed, true
		}
	}
	return llm.Message{}, false
}

func mustJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}
