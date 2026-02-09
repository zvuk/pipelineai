package executor

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zvuk/pipelineai/internal/runtime/llm"
)

type fakeClientSeq struct {
	responses []llm.ChatCompletionResponse
	requests  []llm.ChatCompletionRequest
}

func (f *fakeClientSeq) CreateChatCompletion(_ context.Context, req llm.ChatCompletionRequest) (llm.ChatCompletionResponse, error) {
	f.requests = append(f.requests, req)
	if len(f.responses) == 0 {
		return llm.ChatCompletionResponse{}, nil
	}
	r := f.responses[0]
	f.responses = f.responses[1:]
	return r, nil
}

func TestRunLLMStep_FunctionCallEcho(t *testing.T) {
	artifactDir := t.TempDir()

	cfg := testLLMConfig(t, "produce_manifest", " Ты ассистент ", "Сделай что-нибудь")

	first := llm.ChatCompletionResponse{
		ID:    "r1",
		Model: "gpt-test",
		Choices: []llm.ChatCompletionChoice{{
			Index: 0,
			Message: llm.Message{
				Role:    llm.RoleAssistant,
				Content: "",
				ToolCalls: []llm.ToolCall{{
					ID:   "tool_1",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "demo",
						Arguments: `{"echo":"pong"}`,
					},
				}},
			},
			FinishReason: "tool_calls",
		}},
	}
	second := llm.ChatCompletionResponse{
		ID:    "r2",
		Model: "gpt-test",
		Choices: []llm.ChatCompletionChoice{{
			Index: 0,
			Message: llm.Message{
				Role:    llm.RoleAssistant,
				Content: "final",
			},
			FinishReason: "stop",
		}},
	}

	fake := &fakeClientSeq{responses: []llm.ChatCompletionResponse{first, second}}
	exec := newTestExecutor(t, cfg, fake, artifactDir)

	resp, path, err := exec.RunLLMStep(context.Background(), "produce_manifest", nil)
	if err != nil {
		t.Fatalf("step execution failed: %v", err)
	}

	if resp.ID != "r2" {
		t.Fatalf("expected final response id r2, got %s", resp.ID)
	}
	if len(fake.requests) != 2 {
		t.Fatalf("expected two requests, got %d", len(fake.requests))
	}
	// Во втором запросе должен появиться tool message с указанием tool_call_id
	lastReq := fake.requests[1]
	if len(lastReq.Messages) < 3 {
		t.Fatalf("expected at least 3 messages, got %d", len(lastReq.Messages))
	}
	toolMsg := lastReq.Messages[len(lastReq.Messages)-1]
	if toolMsg.Role != llm.RoleTool || toolMsg.ToolCallID != "tool_1" {
		t.Fatalf("expected tool message with id tool_1, got role=%s id=%s", toolMsg.Role, toolMsg.ToolCallID)
	}
	if !strings.Contains(path, filepath.Join("llm", "produce_manifest")) {
		t.Fatalf("unexpected artifact path: %s", path)
	}
}
