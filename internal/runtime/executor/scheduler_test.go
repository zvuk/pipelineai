package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/zvuk/pipelineai/pkg/dsl"
)

func TestRunWithNeeds_WaitsForAllFanInDependencies(t *testing.T) {
	cfg := testSchedulerConfig(t)
	exec := newTestExecutor(t, cfg, &fakeClient{}, cfg.Agent.ArtifactDir)

	if err := exec.RunWithNeeds(context.Background(), "finalize", 4); err != nil {
		t.Fatalf("run with needs failed: %v", err)
	}

	assertSchedulerArtifacts(t, cfg)
}

func TestRunAll_WaitsForAllFanInDependencies(t *testing.T) {
	cfg := testSchedulerConfig(t)
	exec := newTestExecutor(t, cfg, &fakeClient{}, cfg.Agent.ArtifactDir)

	if err := exec.RunAll(context.Background(), 4); err != nil {
		t.Fatalf("run all failed: %v", err)
	}

	assertSchedulerArtifacts(t, cfg)
}

func testSchedulerConfig(t *testing.T) *dsl.Config {
	t.Helper()

	tmp := t.TempDir()
	aPath := filepath.Join(tmp, "a.done")
	bPath := filepath.Join(tmp, "b.done")
	cPath := filepath.Join(tmp, "c.done")
	summaryPath := filepath.Join(tmp, "summary.done")

	return &dsl.Config{
		Version: 1,
		Agent: dsl.Agent{
			Name:        "scheduler-test",
			Model:       "gpt-test",
			ArtifactDir: filepath.Join(tmp, ".agent", "artifacts"),
			OpenAI:      dsl.AgentOpenAI{BaseURL: "http://localhost", APIKeyEnv: "LLM_API_KEY"},
		},
		Steps: []dsl.Step{
			{
				ID:   "prepare_source",
				Type: "shell",
				Shell: &dsl.StepShell{
					Run: mustTemplate(t, "set -euo pipefail\nprintf 'ok' > "+shellQuote(aPath)+"\n"),
				},
			},
			{
				ID:    "prepare_index",
				Type:  "shell",
				Needs: []string{"prepare_source"},
				Shell: &dsl.StepShell{
					Run: mustTemplate(t, "set -euo pipefail\ntest -f "+shellQuote(aPath)+"\nprintf 'ok' > "+shellQuote(bPath)+"\n"),
				},
			},
			{
				ID:    "process_batch",
				Type:  "shell",
				Needs: []string{"prepare_index"},
				Shell: &dsl.StepShell{
					Run: mustTemplate(t, "set -euo pipefail\ntest -f "+shellQuote(bPath)+"\nsleep 0.2\nprintf 'ok' > "+shellQuote(cPath)+"\n"),
				},
			},
			{
				ID:    "fan_in_gate",
				Type:  "shell",
				Needs: []string{"prepare_source", "prepare_index", "process_batch"},
				Shell: &dsl.StepShell{
					Run: mustTemplate(t, "set -euo pipefail\ntest -f "+shellQuote(aPath)+"\ntest -f "+shellQuote(bPath)+"\ntest -f "+shellQuote(cPath)+"\n"),
				},
			},
			{
				ID:    "finalize",
				Type:  "shell",
				Needs: []string{"fan_in_gate"},
				Shell: &dsl.StepShell{
					Run: mustTemplate(t, "set -euo pipefail\nprintf 'ok' > "+shellQuote(summaryPath)+"\n"),
				},
			},
		},
	}
}

func assertSchedulerArtifacts(t *testing.T, cfg *dsl.Config) {
	t.Helper()

	root := filepath.Dir(filepath.Dir(cfg.Agent.ArtifactDir))
	files := []string{
		filepath.Join(root, "a.done"),
		filepath.Join(root, "b.done"),
		filepath.Join(root, "c.done"),
		filepath.Join(root, "summary.done"),
	}
	for _, p := range files {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected file %s to exist: %v", p, err)
		}
	}
}

func shellQuote(s string) string {
	return "'" + s + "'"
}
