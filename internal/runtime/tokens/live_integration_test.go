//go:build tokenizers_hf

package tokens

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zvuk/pipelineai/internal/runtime/llm"
	"github.com/zvuk/pipelineai/pkg/config"
	"github.com/zvuk/pipelineai/pkg/dsl"
)

func TestLiveTokenizerAndProviderUsage(t *testing.T) {
	if os.Getenv("PAI_RUN_LIVE_TESTS") != "1" {
		t.Skip("set PAI_RUN_LIVE_TESTS=1 to run live integration tests")
	}

	repoRoot := findRepoRoot(t)
	if err := config.LoadEnvFileIfExists(filepath.Join(repoRoot, ".env"), false); err != nil {
		t.Fatalf("failed to load .env: %v", err)
	}

	settings, err := config.Load()
	if err != nil {
		t.Fatalf("failed to load settings: %v", err)
	}

	cfg := &dsl.Config{
		Agent: dsl.Agent{
			Model:             settings.LLMModel,
			TokenizerCacheDir: t.TempDir(),
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := NewManager(cfg, logger)

	textEstimate := manager.CountText(settings.LLMModel, nil, "Reply with exactly OK.")
	if textEstimate.Tokens <= 0 {
		t.Fatalf("expected positive token count, got %#v", textEstimate)
	}
	if !textEstimate.Exact {
		t.Fatalf("expected exact tokenizer backend for model %q, got %#v", settings.LLMModel, textEstimate)
	}

	client, err := llm.NewClient(llm.ModelConfig{
		BaseURL:        settings.LLMBaseURL,
		APIKey:         settings.LLMAPIKey,
		Model:          settings.LLMModel,
		RequestTimeout: minDuration(settings.LLMRequestTimeout, 30*time.Second),
	}, logger)
	if err != nil {
		t.Fatalf("failed to create live client: %v", err)
	}

	req := llm.ChatCompletionRequest{
		Model: settings.LLMModel,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "Reply with exactly OK."},
			{Role: llm.RoleUser, Content: "Say OK."},
		},
	}

	resp, err := client.CreateChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("live completion failed: %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatalf("expected at least one choice from live model")
	}
	if resp.Usage.PromptTokens <= 0 {
		t.Fatalf("expected provider prompt_tokens > 0, got %#v", resp.Usage)
	}

	reqEstimate := manager.EstimateRequest(settings.LLMModel, nil, req)
	if reqEstimate.Tokens <= 0 {
		t.Fatalf("expected positive request estimate, got %#v", reqEstimate)
	}
	if diff := abs(reqEstimate.Tokens - resp.Usage.PromptTokens); diff > resp.Usage.PromptTokens {
		t.Fatalf("request estimate drift is too high: estimate=%d actual=%d", reqEstimate.Tokens, resp.Usage.PromptTokens)
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("failed to locate repo root from %s", wd)
		}
		dir = parent
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func minDuration(a, b time.Duration) time.Duration {
	if a <= 0 {
		return b
	}
	if a < b {
		return a
	}
	return b
}
