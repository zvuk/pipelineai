package dsl

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write temporary config: %v", err)
	}
	return path
}

func TestLoadFile_Success(t *testing.T) {
	t.Setenv("TEST_LLM_BASE_URL", "http://localhost:1234/v1")
	t.Setenv("TEST_LLM_API_KEY_ENV", "LLM_API_KEY")

	cfgPath := writeTempConfig(t, `
version: 1
agent:
  name: "pipelineai"
  model: "openai/gpt-oss-20b"
  artifact_dir: ".agent/artifacts"
  budget_mode: "continue_with_compaction"
  model_context_window: 131072
  tool_output_warn_percent: 10
  tool_output_hard_cap_percent: 25
  auto_compact_percent: 85
  compact_target_percent: 60
  response_reserve_tokens: 4096
  tool_result_mode: "persist_on_overflow"
  tool_result_preview_tokens: 768
  shell_capture_max_bytes: 262144
  disable_inline_tool_call_fallback: true
  tokenizer_cache_dir: ".agent/cache/tokenizers"
  reasoning: true
  openai:
    base_url: '{{ env "TEST_LLM_BASE_URL" }}'
    api_key_env: '{{ env "TEST_LLM_API_KEY_ENV" }}'
steps:
  - id: produce_manifest
    type: llm
    system_prompt: "{{ trim \"  Ты ассистент  \" }}"
    user_prompt: "Сформируй manifest"
`)

	cfg, err := LoadFile(cfgPath)
	if err != nil {
		t.Fatalf("expected successful load, got error: %v", err)
	}

	if cfg.Agent.OpenAI.BaseURL != "http://localhost:1234/v1" {
		t.Fatalf("base_url resolved incorrectly: %s", cfg.Agent.OpenAI.BaseURL)
	}
	if cfg.Agent.OpenAI.APIKeyEnv != "LLM_API_KEY" {
		t.Fatalf("api_key_env resolved incorrectly: %s", cfg.Agent.OpenAI.APIKeyEnv)
	}
	if cfg.Agent.ModelContextWindow == nil || *cfg.Agent.ModelContextWindow != 131072 {
		t.Fatalf("model_context_window resolved incorrectly: %#v", cfg.Agent.ModelContextWindow)
	}
	if cfg.Agent.BudgetMode != "continue_with_compaction" {
		t.Fatalf("budget_mode resolved incorrectly: %q", cfg.Agent.BudgetMode)
	}
	if cfg.Agent.ToolOutputWarnPercent == nil || *cfg.Agent.ToolOutputWarnPercent != 10 {
		t.Fatalf("tool_output_warn_percent resolved incorrectly: %#v", cfg.Agent.ToolOutputWarnPercent)
	}
	if cfg.Agent.ToolOutputHardCapPercent == nil || *cfg.Agent.ToolOutputHardCapPercent != 25 {
		t.Fatalf("tool_output_hard_cap_percent resolved incorrectly: %#v", cfg.Agent.ToolOutputHardCapPercent)
	}
	if cfg.Agent.AutoCompactPercent == nil || *cfg.Agent.AutoCompactPercent != 85 {
		t.Fatalf("auto_compact_percent resolved incorrectly: %#v", cfg.Agent.AutoCompactPercent)
	}
	if cfg.Agent.CompactTargetPercent == nil || *cfg.Agent.CompactTargetPercent != 60 {
		t.Fatalf("compact_target_percent resolved incorrectly: %#v", cfg.Agent.CompactTargetPercent)
	}
	if cfg.Agent.ResponseReserveTokens == nil || *cfg.Agent.ResponseReserveTokens != 4096 {
		t.Fatalf("response_reserve_tokens resolved incorrectly: %#v", cfg.Agent.ResponseReserveTokens)
	}
	if cfg.Agent.ToolResultMode != "persist_on_overflow" {
		t.Fatalf("tool_result_mode resolved incorrectly: %q", cfg.Agent.ToolResultMode)
	}
	if cfg.Agent.ToolResultPreviewTokens == nil || *cfg.Agent.ToolResultPreviewTokens != 768 {
		t.Fatalf("tool_result_preview_tokens resolved incorrectly: %#v", cfg.Agent.ToolResultPreviewTokens)
	}
	if cfg.Agent.ShellCaptureMaxBytes == nil || *cfg.Agent.ShellCaptureMaxBytes != 262144 {
		t.Fatalf("shell_capture_max_bytes resolved incorrectly: %#v", cfg.Agent.ShellCaptureMaxBytes)
	}
	if !cfg.Agent.DisableInlineToolCallFallback {
		t.Fatal("expected disable_inline_tool_call_fallback=true to be preserved")
	}
	if cfg.Agent.TokenizerCacheDir != ".agent/cache/tokenizers" {
		t.Fatalf("tokenizer_cache_dir resolved incorrectly: %q", cfg.Agent.TokenizerCacheDir)
	}
	if !cfg.Agent.Reasoning {
		t.Fatal("expected reasoning=true to be preserved")
	}
	if len(cfg.Steps) != 1 {
		t.Fatalf("expected one step, got %d", len(cfg.Steps))
	}
	step := cfg.Steps[0]
	if step.LLM == nil {
		t.Fatalf("llm step not recognized")
	}
	if got := step.LLM.SystemPrompt.String(); got != "{{ trim \"  Ты ассистент  \" }}" {
		t.Fatalf("expected original system_prompt template, got %q", got)
	}
}

func TestLoadFile_InvalidConfig(t *testing.T) {
	cfgPath := writeTempConfig(t, `
version: 2
agent:
  name: ""
  model: ""
  artifact_dir: ""
  openai:
    base_url: ""
    api_key_env: ""
steps: []
`)

	_, err := LoadFile(cfgPath)
	if err == nil {
		t.Fatal("expected validation error but got nil")
	}
}

func TestValidate_DuplicateSteps(t *testing.T) {
	cfg := &Config{
		Version: 1,
		Agent: Agent{
			Name:        "a",
			Model:       "m",
			ArtifactDir: ".agent",
			OpenAI: AgentOpenAI{
				BaseURL:   "http://localhost",
				APIKeyEnv: "KEY",
			},
		},
		Steps: []Step{
			{ID: "one", Type: "llm", LLM: &StepLLM{SystemPrompt: TemplateString{raw: "a"}, UserPrompt: TemplateString{raw: "b"}}},
			{ID: "one", Type: "llm", LLM: &StepLLM{SystemPrompt: TemplateString{raw: "a"}, UserPrompt: TemplateString{raw: "b"}}},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for duplicate step")
	}
}

func TestValidate_InvalidRuntimePolicies(t *testing.T) {
	cfg := &Config{
		Version: 1,
		Agent: Agent{
			Name:                    "a",
			Model:                   "m",
			ArtifactDir:             ".agent",
			BudgetMode:              "broken",
			ToolResultMode:          "invalid",
			ToolResultPreviewTokens: intPtrValue(0),
			ShellCaptureMaxBytes:    intPtrValue(-1),
			OpenAI: AgentOpenAI{
				BaseURL:   "http://localhost",
				APIKeyEnv: "KEY",
			},
		},
		Steps: []Step{
			{
				ID:   "one",
				Type: "llm",
				LLM: &StepLLM{
					SystemPrompt:            TemplateString{raw: "a"},
					UserPrompt:              TemplateString{raw: "b"},
					BudgetMode:              "nope",
					ToolResultMode:          "bad",
					ToolResultPreviewTokens: intPtrValue(0),
					ShellCaptureMaxBytes:    intPtrValue(0),
				},
			},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for invalid runtime policy settings")
	}
}

func intPtrValue(v int) *int {
	return &v
}
