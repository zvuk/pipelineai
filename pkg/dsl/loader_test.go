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
