package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zvuk/pipelineai/pkg/dsl"
)

func TestRunShellStepFailureIncludesUsefulDiagnostics(t *testing.T) {
	artifactDir := t.TempDir()
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
				ID:   "broken_shell",
				Type: "shell",
				Shell: &dsl.StepShell{
					Run: mustTemplate(t, "set -euo pipefail\necho before\nfalse\necho after\n"),
				},
			},
		},
	}
	exec := newTestExecutor(t, cfg, &fakeClient{}, artifactDir)

	_, err := exec.RunShellStep(context.Background(), "broken_shell", nil)
	if err == nil {
		t.Fatal("expected shell step error")
	}
	var bashErr *BashStepError
	if !errors.As(err, &bashErr) {
		t.Fatalf("expected BashStepError, got %T: %v", err, err)
	}
	if bashErr.ExitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", bashErr.ExitCode)
	}
	if bashErr.Line != 3 {
		t.Fatalf("expected failing user script line 3, got %d", bashErr.Line)
	}
	if !strings.Contains(bashErr.Command, "false") {
		t.Fatalf("expected failing command in diagnostics, got %q", bashErr.Command)
	}
	if strings.TrimSpace(bashErr.ArtifactPath) == "" {
		t.Fatalf("expected failure artifact path")
	}
	if _, statErr := os.Stat(filepath.Join(bashErr.ArtifactPath, "stderr.txt")); statErr != nil {
		t.Fatalf("expected stderr artifact: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(bashErr.ArtifactPath, "stdout.txt")); statErr != nil {
		t.Fatalf("expected stdout artifact: %v", statErr)
	}
}
