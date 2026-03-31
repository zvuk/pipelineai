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
			Content:    `{"tool":"gitlab_create_inline_draft_note","ok":true,"stdout":"{\"status\":\"ok\",\"created\":true}","stderr":"","exit_code":0,"elapsed_ms":1}`,
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

func TestValidateReviewFileResponseRejectsDuplicateInlineAnchor(t *testing.T) {
	resp := finalTextResponse("Созданы inline-комментарии")
	messages := []llm.Message{
		{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{
				{
					ID:   "tool_1",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "gitlab_create_inline_draft_note",
						Arguments: `{"file_path":"deployments/docker-compose.yml","line":90,"body":"⚠️ первый"}`,
					},
				},
				{
					ID:   "tool_2",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "gitlab_create_inline_draft_note",
						Arguments: `{"file_path":"deployments/docker-compose.yml","line":90,"body":"⚠️ второй"}`,
					},
				},
			},
		},
		{
			Role:       llm.RoleTool,
			ToolCallID: "tool_1",
			Content:    `{"tool":"gitlab_create_inline_draft_note","ok":true,"stdout":"{\"status\":\"ok\",\"created\":true}","stderr":"","exit_code":0,"elapsed_ms":1}`,
		},
		{
			Role:       llm.RoleTool,
			ToolCallID: "tool_2",
			Content:    `{"tool":"gitlab_create_inline_draft_note","ok":true,"stdout":"{\"status\":\"ok\",\"created\":true}","stderr":"","exit_code":0,"elapsed_ms":1}`,
		},
	}

	err := validateReviewFileResponse(resp, messages, map[string]any{
		"matrix": map[string]any{
			"file_path":      "deployments/docker-compose.yml",
			"file_paths_csv": "deployments/docker-compose.yml",
		},
	})
	if err == nil {
		t.Fatal("expected validator to reject duplicate inline note anchor")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "duplicate inline note") {
		t.Fatalf("unexpected validator error: %v", err)
	}
}

func TestValidateReviewFileResponseAcceptsDeduplicatedInlineNote(t *testing.T) {
	resp := finalTextResponse("Комментарий уже существует в текущем прогоне")
	messages := []llm.Message{
		{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{{
				ID:   "tool_1",
				Type: "function",
				Function: llm.FunctionCall{
					Name:      "gitlab_create_inline_draft_note",
					Arguments: `{"file_path":"deployments/docker-compose.yml","line":90,"body":"⚠️ пример"}`,
				},
			}},
		},
		{
			Role:       llm.RoleTool,
			ToolCallID: "tool_1",
			Content:    `{"tool":"gitlab_create_inline_draft_note","ok":true,"stdout":"{\"status\":\"ok\",\"created\":false,\"deduplicated\":true}","stderr":"","exit_code":0,"elapsed_ms":1}`,
		},
	}

	err := validateReviewFileResponse(resp, messages, map[string]any{
		"matrix": map[string]any{
			"file_path":      "deployments/docker-compose.yml",
			"file_paths_csv": "deployments/docker-compose.yml",
		},
	})
	if err != nil {
		t.Fatalf("expected validator to accept deduplicated inline note, got %v", err)
	}
}
