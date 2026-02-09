package executor

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zvuk/pipelineai/internal/runtime/llm"
	"github.com/zvuk/pipelineai/pkg/dsl"
)

type fakeClient struct {
	resp      llm.ChatCompletionResponse
	lastReq   llm.ChatCompletionRequest
	callCount int
	failErr   error
}

func (f *fakeClient) CreateChatCompletion(_ context.Context, req llm.ChatCompletionRequest) (llm.ChatCompletionResponse, error) {
	f.callCount++
	f.lastReq = req
	if f.failErr != nil {
		return llm.ChatCompletionResponse{}, f.failErr
	}
	return f.resp, nil
}

func mustTemplate(t *testing.T, text string) dsl.TemplateString {
	t.Helper()
	ts, err := dsl.NewTemplateString(text)
	if err != nil {
		t.Fatalf("template error: %v", err)
	}
	return ts
}

func testLLMConfig(t *testing.T, stepID, systemPrompt, userPrompt string) *dsl.Config {
	t.Helper()
	return &dsl.Config{
		Version: 1,
		Agent: dsl.Agent{
			Name:        "pipelineai",
			Model:       "gpt-test",
			ArtifactDir: ".agent/artifacts",
			OpenAI:      dsl.AgentOpenAI{BaseURL: "http://localhost", APIKeyEnv: "LLM_API_KEY"},
		},
		Steps: []dsl.Step{{
			ID:   stepID,
			Type: "llm",
			LLM: &dsl.StepLLM{
				SystemPrompt: mustTemplate(t, systemPrompt),
				UserPrompt:   mustTemplate(t, userPrompt),
			},
		}},
	}
}

func newTestExecutor(t *testing.T, cfg *dsl.Config, client LLMClient, artifactDir string) *Executor {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	exec, err := New(cfg, client, artifactDir, logger)
	if err != nil {
		t.Fatalf("failed to create executor: %v", err)
	}
	return exec
}

func TestRunLLMStepSuccess(t *testing.T) {
	artifactDir := t.TempDir()

	cfg := testLLMConfig(t, "produce_manifest", "  Ты ассистент PipelineAI  ", "Собери manifest")

	fake := &fakeClient{resp: llm.ChatCompletionResponse{ID: "resp-1", Model: "gpt-test"}}
	exec := newTestExecutor(t, cfg, fake, artifactDir)

	resp, path, err := exec.RunLLMStep(context.Background(), "produce_manifest", nil)
	if err != nil {
		t.Fatalf("step execution failed: %v", err)
	}

	if resp.ID != "resp-1" {
		t.Fatalf("unexpected response id: %s", resp.ID)
	}
	if fake.callCount != 1 {
		t.Fatalf("client must be called exactly once, got %d calls", fake.callCount)
	}
	if len(fake.lastReq.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(fake.lastReq.Messages))
	}
	if got := fake.lastReq.Messages[0].Content; got != "Ты ассистент PipelineAI" {
		t.Fatalf("system prompt should be trimmed, got %q", got)
	}

	if !strings.Contains(path, filepath.Join("llm", "produce_manifest")) || !strings.HasSuffix(strings.ToLower(path), ".json") {
		t.Fatalf("unexpected artifact path: %s", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("artifact was not created: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("failed to parse artifact json: %v", err)
	}
	if payload["response"] == nil {
		t.Fatal("response field is missing in artifact")
	}
	if payload["system"] == nil || payload["prompt"] == nil {
		t.Fatal("system/prompt fields are missing in artifact")
	}
	if meta, ok := payload["meta"].(map[string]any); !ok || meta["hash"] == nil {
		t.Fatal("meta.hash is missing in artifact")
	}
	if msgs, ok := payload["messages"].([]any); !ok || len(msgs) < 2 {
		t.Fatal("messages array missing or too short")
	}
}

func TestRunLLMStepUnknown(t *testing.T) {
	cfg := &dsl.Config{Version: 1, Agent: dsl.Agent{Name: "a", Model: "m", ArtifactDir: ".", OpenAI: dsl.AgentOpenAI{BaseURL: "b", APIKeyEnv: "k"}}, Steps: nil}
	fake := &fakeClient{}
	exec := newTestExecutor(t, cfg, fake, t.TempDir())

	if _, _, err := exec.RunLLMStep(context.Background(), "missing", nil); err == nil {
		t.Fatal("expected an error for missing step")
	}
}
