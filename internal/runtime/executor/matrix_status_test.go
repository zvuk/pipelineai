package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zvuk/pipelineai/internal/runtime/llm"
	"github.com/zvuk/pipelineai/pkg/dsl"
)

func TestRunMatrixStep_AllowFailureWritesDegradedStatus(t *testing.T) {
	artifactDir := t.TempDir()
	manifestPath := filepath.Join(t.TempDir(), "manifest.yaml")
	manifest := "items:\n  - id: unit-1\n"
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	cfg := &dsl.Config{
		Version: 1,
		Agent: dsl.Agent{
			Name:        "pipelineai",
			Model:       "gpt-test",
			ArtifactDir: artifactDir,
			OpenAI:      dsl.AgentOpenAI{BaseURL: "http://localhost", APIKeyEnv: "LLM_API_KEY"},
		},
		Steps: []dsl.Step{
			{
				ID:   "review_per_file",
				Type: "matrix",
				Matrix: &dsl.StepMatrix{
					FromYAML: mustTemplate(t, manifestPath),
					Select:   "items",
					ItemID:   mustTemplate(t, "{{ .item.id }}"),
				},
				Run: &dsl.StepMatrixRun{Step: "review_file"},
			},
			{
				ID:           "review_file",
				Type:         "llm",
				Template:     true,
				AllowFailure: true,
				Retries:      1,
				LLM: &dsl.StepLLM{
					SystemPrompt: mustTemplate(t, "system"),
					UserPrompt:   mustTemplate(t, "user"),
				},
			},
		},
	}

	exec := newTestExecutor(t, cfg, &fakeClient{failErr: errors.New("boom")}, artifactDir)
	if err := exec.RunMatrixStep(context.Background(), "review_per_file", 1); err != nil {
		t.Fatalf("matrix step must not fail when child allow_failure=true, got %v", err)
	}

	statusPath := filepath.Join(artifactDir, "items", "unit-1", "status.json")
	data, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("failed to read status artifact: %v", err)
	}

	var status map[string]any
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatalf("failed to decode status artifact: %v", err)
	}
	if status["status"] != "degraded" {
		t.Fatalf("expected degraded status, got %#v", status["status"])
	}
	if status["ok"] != false {
		t.Fatalf("expected ok=false, got %#v", status["ok"])
	}
	if status["degraded"] != true {
		t.Fatalf("expected degraded=true, got %#v", status["degraded"])
	}
	if status["error"] == "" {
		t.Fatal("expected error message in status artifact")
	}
}

type fakeClientRetry struct {
	attempt int
}

func (f *fakeClientRetry) CreateChatCompletion(_ context.Context, _ llm.ChatCompletionRequest) (llm.ChatCompletionResponse, error) {
	f.attempt++
	if f.attempt == 1 {
		return llm.ChatCompletionResponse{}, errors.New("transient failure")
	}
	return llm.ChatCompletionResponse{
		ID:    "ok",
		Model: "gpt-test",
		Choices: []llm.ChatCompletionChoice{{
			Index: 0,
			Message: llm.Message{
				Role:    llm.RoleAssistant,
				Content: "SKIP",
			},
			FinishReason: "stop",
		}},
	}, nil
}

func TestRunMatrixStep_ClearsRecoveredErrorAfterSuccessfulRetry(t *testing.T) {
	artifactDir := t.TempDir()
	manifestPath := filepath.Join(t.TempDir(), "manifest.yaml")
	manifest := "items:\n  - id: unit-1\n"
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	cfg := &dsl.Config{
		Version: 1,
		Agent: dsl.Agent{
			Name:        "pipelineai",
			Model:       "gpt-test",
			ArtifactDir: artifactDir,
			OpenAI:      dsl.AgentOpenAI{BaseURL: "http://localhost", APIKeyEnv: "LLM_API_KEY"},
		},
		Steps: []dsl.Step{
			{
				ID:   "review_per_file",
				Type: "matrix",
				Matrix: &dsl.StepMatrix{
					FromYAML: mustTemplate(t, manifestPath),
					Select:   "items",
					ItemID:   mustTemplate(t, "{{ .item.id }}"),
				},
				Run: &dsl.StepMatrixRun{Step: "review_file"},
			},
			{
				ID:       "review_file",
				Type:     "llm",
				Template: true,
				Retries:  2,
				LLM: &dsl.StepLLM{
					SystemPrompt: mustTemplate(t, "system"),
					UserPrompt:   mustTemplate(t, "user"),
				},
			},
		},
	}

	exec := newTestExecutor(t, cfg, &fakeClientRetry{}, artifactDir)
	if err := exec.RunMatrixStep(context.Background(), "review_per_file", 1); err != nil {
		t.Fatalf("matrix step must succeed after retry, got %v", err)
	}

	statusPath := filepath.Join(artifactDir, "items", "unit-1", "status.json")
	data, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("failed to read status artifact: %v", err)
	}

	var status map[string]any
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatalf("failed to decode status artifact: %v", err)
	}
	if status["status"] != "ok" {
		t.Fatalf("expected ok status, got %#v", status["status"])
	}
	if status["ok"] != true {
		t.Fatalf("expected ok=true, got %#v", status["ok"])
	}
	if status["degraded"] != false {
		t.Fatalf("expected degraded=false, got %#v", status["degraded"])
	}
	if got := status["error"]; got != "" {
		t.Fatalf("expected recovered status to clear error, got %#v", got)
	}
}
