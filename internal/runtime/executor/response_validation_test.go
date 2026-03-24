package executor

import (
	"context"
	"strings"
	"testing"

	"github.com/zvuk/pipelineai/internal/runtime/llm"
)

func TestRunLLMStep_ReviewFileValidatorRejectsHallucinatedPatchApplied(t *testing.T) {
	cfg := testToolConfig(t, "Проведи review", 10, 95, 1000)
	cfg.Steps[0].LLM.ResponseValidator = "review_file"
	client := &fakeClientSeq{responses: []llm.ChatCompletionResponse{
		finalTextResponse("Patch Applied"),
	}}
	exec := newTokenTestExecutor(t, cfg, client)

	_, _, err := exec.RunLLMStep(context.Background(), "tool_step", map[string]any{
		"matrix": map[string]any{
			"file_path":      "internal/app/cast/cast.go",
			"file_paths_csv": "internal/app/cast/cast.go",
		},
	})
	if err == nil {
		t.Fatal("expected validator error for hallucinated patch response")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "validator rejected") {
		t.Fatalf("unexpected validator error: %v", err)
	}
}

func TestValidateReviewFileResponseAcceptsSuccessfulInlineNote(t *testing.T) {
	resp := finalTextResponse("Создан 1 inline-комментарий")
	messages := []llm.Message{
		{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{{
				ID:   "tool_1",
				Type: "function",
				Function: llm.FunctionCall{
					Name:      "gitlab_create_inline_draft_note",
					Arguments: `{"file_path":"internal/app/cast/cast.go","line":12,"body":"⚠️ пример"}`,
				},
			}},
		},
		{
			Role:       llm.RoleTool,
			ToolCallID: "tool_1",
			Content:    `{"tool":"gitlab_create_inline_draft_note","ok":true,"stdout":"","stderr":"","exit_code":0,"elapsed_ms":1}`,
		},
	}
	err := validateReviewFileResponse(resp, messages, map[string]any{
		"matrix": map[string]any{
			"file_path":      "internal/app/cast/cast.go",
			"file_paths_csv": "internal/app/cast/cast.go",
		},
	})
	if err != nil {
		t.Fatalf("expected validator to accept successful inline note, got %v", err)
	}
}
