package executor

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/zvuk/pipelineai/internal/runtime/llm"
	"github.com/zvuk/pipelineai/internal/runtime/tokens"
	"github.com/zvuk/pipelineai/pkg/dsl"
)

type fakeTokenCounter struct {
	profile tokens.ModelProfile
}

func (f fakeTokenCounter) ResolveModel(model string, contextWindowOverride *int) tokens.ModelProfile {
	profile := f.profile
	profile.RequestedModel = model
	if contextWindowOverride != nil && *contextWindowOverride > 0 {
		profile.ContextWindow = *contextWindowOverride
	}
	return profile
}

func (f fakeTokenCounter) CountText(_ string, _ *int, text string) tokens.Estimate {
	return tokens.Estimate{Tokens: approxFakeTokens(len(text)), Exact: true, Strategy: "fake"}
}

func (f fakeTokenCounter) EstimateMessage(_ string, _ *int, msg llm.Message) tokens.Estimate {
	data, _ := json.Marshal(msg)
	return tokens.Estimate{Tokens: approxFakeTokens(len(data)), Exact: true, Strategy: "fake"}
}

func (f fakeTokenCounter) EstimateMessages(_ string, _ *int, messages []llm.Message) tokens.Estimate {
	data, _ := json.Marshal(messages)
	return tokens.Estimate{Tokens: approxFakeTokens(len(data)), Exact: true, Strategy: "fake"}
}

func (f fakeTokenCounter) EstimateRequest(_ string, _ *int, req llm.ChatCompletionRequest) tokens.Estimate {
	data, _ := json.Marshal(req)
	return tokens.Estimate{Tokens: approxFakeTokens(len(data)), Exact: true, Strategy: "fake"}
}

type callDrivenClient struct {
	t        *testing.T
	requests []llm.ChatCompletionRequest
	fn       func(call int, req llm.ChatCompletionRequest) (llm.ChatCompletionResponse, error)
}

func (c *callDrivenClient) CreateChatCompletion(_ context.Context, req llm.ChatCompletionRequest) (llm.ChatCompletionResponse, error) {
	c.requests = append(c.requests, req)
	return c.fn(len(c.requests), req)
}

func testToolConfig(t *testing.T, userPrompt string, toolWarnPercent, autoCompactPercent, contextWindow int) *dsl.Config {
	t.Helper()
	cfg := &dsl.Config{
		Version: 1,
		Agent: dsl.Agent{
			Name:        "pipelineai",
			Model:       "cloudru/gpt-oss-120b",
			ArtifactDir: ".agent/artifacts",
			OpenAI: dsl.AgentOpenAI{
				BaseURL:   "http://localhost",
				APIKeyEnv: "LLM_API_KEY",
			},
			ModelContextWindow:    intPtr(contextWindow),
			ToolOutputWarnPercent: intPtr(toolWarnPercent),
			AutoCompactPercent:    intPtr(autoCompactPercent),
		},
		Steps: []dsl.Step{{
			ID:   "tool_step",
			Type: "llm",
			LLM: &dsl.StepLLM{
				SystemPrompt: mustTemplate(t, "Ты тестовый ассистент"),
				UserPrompt:   mustTemplate(t, userPrompt),
				ToolsAllowed: []string{"shell"},
			},
		}},
	}
	return cfg
}

func newTokenTestExecutor(t *testing.T, cfg *dsl.Config, client LLMClient) *Executor {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	exec, err := New(cfg, client, t.TempDir(), logger)
	if err != nil {
		t.Fatalf("failed to create executor: %v", err)
	}
	exec.tokenizer = fakeTokenCounter{
		profile: tokens.ModelProfile{
			RequestedModel:  cfg.Agent.Model,
			NormalizedModel: "gpt-oss-120b",
			DisplayName:     "gpt-oss-120b",
			ContextWindow:   *cfg.Agent.ModelContextWindow,
		},
	}
	return exec
}

func TestRunLLMStep_LargeToolResultWarnsFirstTime(t *testing.T) {
	cfg := testToolConfig(t, "Нужен большой вывод", 10, 95, 1000)
	client := &fakeClientSeq{responses: []llm.ChatCompletionResponse{
		toolCallResponse(`{"command":["bash","-lc","head -c 400 /dev/zero | tr '\\0' x"]}`),
		finalTextResponse("done"),
	}}
	exec := newTokenTestExecutor(t, cfg, client)

	if _, _, err := exec.RunLLMStep(context.Background(), "tool_step", nil); err != nil {
		t.Fatalf("step execution failed: %v", err)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(client.requests))
	}

	toolMsg := client.requests[1].Messages[len(client.requests[1].Messages)-1]
	if !strings.Contains(toolMsg.Content, `"suppressed":true`) {
		t.Fatalf("expected suppressed tool payload, got %s", toolMsg.Content)
	}
	if !strings.Contains(toolMsg.Content, "force_full_output=true") {
		t.Fatalf("expected retry hint in warning payload, got %s", toolMsg.Content)
	}
	if strings.Contains(toolMsg.Content, `"stdout":"xxxxxxxx`) {
		t.Fatalf("expected large stdout to be omitted from warning payload, got %s", toolMsg.Content)
	}
}

func TestRunLLMStep_LargeToolResultReturnsFullPayloadOnRepeat(t *testing.T) {
	cfg := testToolConfig(t, "Нужен повтор", 10, 95, 1000)
	client := &fakeClientSeq{responses: []llm.ChatCompletionResponse{
		toolCallResponse(`{"command":["bash","-lc","head -c 420 /dev/zero | tr '\\0' y"]}`),
		toolCallResponse(`{"command":["bash","-lc","head -c 420 /dev/zero | tr '\\0' y"]}`),
		finalTextResponse("done"),
	}}
	exec := newTokenTestExecutor(t, cfg, client)

	if _, _, err := exec.RunLLMStep(context.Background(), "tool_step", nil); err != nil {
		t.Fatalf("step execution failed: %v", err)
	}
	if len(client.requests) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(client.requests))
	}

	firstToolMsg := client.requests[1].Messages[len(client.requests[1].Messages)-1]
	secondToolMsg := client.requests[2].Messages[len(client.requests[2].Messages)-1]
	if !strings.Contains(firstToolMsg.Content, `"suppressed":true`) {
		t.Fatalf("expected first tool result to be suppressed, got %s", firstToolMsg.Content)
	}
	if strings.Contains(secondToolMsg.Content, `"suppressed":true`) {
		t.Fatalf("expected repeated tool call to return full payload, got %s", secondToolMsg.Content)
	}
	if !strings.Contains(secondToolMsg.Content, `"stdout":"yyyy`) {
		t.Fatalf("expected repeated tool call to include stdout, got %s", secondToolMsg.Content)
	}
}

func TestRunLLMStep_ForceFullOutputBypassesWarning(t *testing.T) {
	cfg := testToolConfig(t, "Нужен форс", 10, 95, 1000)
	client := &fakeClientSeq{responses: []llm.ChatCompletionResponse{
		toolCallResponse(`{"command":["bash","-lc","head -c 160 /dev/zero | tr '\\0' z"],"force_full_output":true}`),
		finalTextResponse("done"),
	}}
	exec := newTokenTestExecutor(t, cfg, client)

	if _, _, err := exec.RunLLMStep(context.Background(), "tool_step", nil); err != nil {
		t.Fatalf("step execution failed: %v", err)
	}
	toolMsg := client.requests[1].Messages[len(client.requests[1].Messages)-1]
	if strings.Contains(toolMsg.Content, `"suppressed":true`) {
		t.Fatalf("expected force_full_output to bypass suppression, got %s", toolMsg.Content)
	}
	if !strings.Contains(toolMsg.Content, `"stdout":"zzzz`) {
		t.Fatalf("expected full stdout in force_full_output response, got %s", toolMsg.Content)
	}
}

func TestRunLLMStep_AutoCompactionPreservesCriticalFacts(t *testing.T) {
	cfg := testToolConfig(t, "CRITICAL_FACT=42\nСохрани это в памяти до конца шага.", 90, 85, 800)
	client := &callDrivenClient{
		t: t,
		fn: func(call int, req llm.ChatCompletionRequest) (llm.ChatCompletionResponse, error) {
			switch call {
			case 1:
				return toolCallResponseWithUsage(`{"command":["bash","-lc","echo -n one"]}`, 600), nil
			case 2:
				return toolCallResponseWithUsage(`{"command":["bash","-lc","echo -n two"]}`, 690), nil
			case 3:
				if len(req.Messages) < 2 || req.Messages[0].Content != compactPrompt {
					t.Fatalf("expected compaction request on call 3, got %#v", req.Messages)
				}
				if !strings.Contains(req.Messages[1].Content, "<SUMMARIZATION_PROMPT>") {
					t.Fatalf("expected gpt-oss compaction prompt wrapper, got %s", req.Messages[1].Content)
				}
				if !strings.Contains(req.Messages[1].Content, "<COMPACTION_HISTORY>") {
					t.Fatalf("expected gpt-oss compaction history wrapper, got %s", req.Messages[1].Content)
				}
				return finalTextResponseWithUsage("Current progress: shell calls already executed.\nImportant context: CRITICAL_FACT=42.\nNext steps: continue the task using the preserved fact.", 22), nil
			case 4:
				contents := make([]string, 0, len(req.Messages))
				for _, msg := range req.Messages {
					contents = append(contents, msg.Content)
				}
				payload := strings.Join(contents, "\n\n")
				if !strings.Contains(payload, compactSummaryPrefix) {
					t.Fatalf("expected compact summary prefix in post-compaction request, got %s", payload)
				}
				if !strings.Contains(payload, "<COMPACTION_SUMMARY>") {
					t.Fatalf("expected wrapped compaction summary in post-compaction request, got %s", payload)
				}
				if !strings.Contains(payload, "CRITICAL_FACT=42") {
					t.Fatalf("expected critical fact to survive compaction, got %s", payload)
				}
				return finalTextResponse("preserved"), nil
			default:
				t.Fatalf("unexpected request #%d", call)
				return llm.ChatCompletionResponse{}, nil
			}
		},
	}
	exec := newTokenTestExecutor(t, cfg, client)

	resp, _, err := exec.RunLLMStep(context.Background(), "tool_step", nil)
	if err != nil {
		t.Fatalf("step execution failed: %v", err)
	}
	if len(resp.Choices) == 0 || strings.TrimSpace(resp.Choices[0].Message.Content) != "preserved" {
		t.Fatalf("expected preserved final response, got %#v", resp.Choices)
	}
	if len(client.requests) != 4 {
		t.Fatalf("expected 4 requests including compaction, got %d", len(client.requests))
	}
}

func toolCallResponse(args string) llm.ChatCompletionResponse {
	return toolCallResponseWithUsage(args, 25)
}

func toolCallResponseWithUsage(args string, promptTokens int) llm.ChatCompletionResponse {
	return llm.ChatCompletionResponse{
		ID:    "tool",
		Model: "gpt-test",
		Choices: []llm.ChatCompletionChoice{{
			Index: 0,
			Message: llm.Message{
				Role: llm.RoleAssistant,
				ToolCalls: []llm.ToolCall{{
					ID:   "tool_1",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "shell",
						Arguments: args,
					},
				}},
			},
			FinishReason: "tool_calls",
		}},
		Usage: llm.Usage{
			PromptTokens:     promptTokens,
			CompletionTokens: 5,
			TotalTokens:      promptTokens + 5,
		},
	}
}

func finalTextResponse(text string) llm.ChatCompletionResponse {
	return finalTextResponseWithUsage(text, 20)
}

func finalTextResponseWithUsage(text string, promptTokens int) llm.ChatCompletionResponse {
	return llm.ChatCompletionResponse{
		ID:    "final",
		Model: "gpt-test",
		Choices: []llm.ChatCompletionChoice{{
			Index: 0,
			Message: llm.Message{
				Role:    llm.RoleAssistant,
				Content: text,
			},
			FinishReason: "stop",
		}},
		Usage: llm.Usage{
			PromptTokens:     promptTokens,
			CompletionTokens: 5,
			TotalTokens:      promptTokens + 5,
		},
	}
}

func intPtr(v int) *int {
	return &v
}

func approxFakeTokens(size int) int {
	if size <= 0 {
		return 0
	}
	return (size + 3) / 4
}
