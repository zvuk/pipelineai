package projectconfig

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zvuk/pipelineai/pkg/dsl"
)

func TestPrepareLoadsLocalOverrideCopiesResourcesAndRendersBlocks(t *testing.T) {
	tmp := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir tmp: %v", err)
	}
	defer func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Errorf("restore wd: %v", err)
		}
	}()

	if err := os.MkdirAll(filepath.Join(tmp, "repo-rules"), 0o755); err != nil {
		t.Fatalf("mkdir repo rules: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "repo-rules", "playwright.md"), []byte("PLAYWRIGHT RULE\n"), 0o644); err != nil {
		t.Fatalf("write playwright rule: %v", err)
	}

	override := `
version: 1
instruction_blocks:
  - id: review_rules
    mode: append
    items:
      - path: '{{ index .project.resources "qa_rules" }}/playwright.md'
        label: Playwright QA rules
        when:
          any_glob:
            - "**/*.spec.ts"
resource_copy:
  - id: qa_rules
    source:
      repo: target
      path: repo-rules
    destination: .pai/rules
settings:
  ai_review_mode: reviewer
`
	if err := os.WriteFile(filepath.Join(tmp, ".pai-config.yaml"), []byte(strings.TrimSpace(override)+"\n"), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}

	cfg := &dsl.Config{
		Version: 1,
		Agent: dsl.Agent{
			Name:        "test",
			Model:       "gpt-test",
			ArtifactDir: ".agent/artifacts",
		},
		ProjectConfig: &dsl.ProjectConfig{
			Enabled: true,
			InstructionBlocks: []dsl.InstructionBlock{
				{
					ID:      "review_rules",
					Title:   mustTemplate(t, "Review rules"),
					Content: mustTemplate(t, "Read these files before analysis."),
					Items: []dsl.InstructionItem{
						{Path: mustTemplate(t, "ci/rules/general.md"), Label: mustTemplate(t, "General rules"), Required: true},
						{Path: mustTemplate(t, "ci/rules/go.md"), Label: mustTemplate(t, "Go rules"), When: &dsl.InstructionWhen{AnyExt: []string{".go"}}},
					},
				},
			},
			Settings: map[string]string{
				"ai_review_mode": "commenter",
			},
		},
	}

	if err := Prepare(context.Background(), cfg, nil); err != nil {
		t.Fatalf("prepare project config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".pai", "rules", "playwright.md")); err != nil {
		t.Fatalf("copied playwright rule not found: %v", err)
	}

	templateCtx := map[string]any{
		"matrix": map[string]any{
			"file_path": "tests/login.spec.ts",
		},
	}
	templateCtx["project"] = StaticTemplateContext(cfg.ProjectConfig)
	staticProjectCtx := templateCtx["project"].(map[string]any)
	settings := staticProjectCtx["settings"].(map[string]string)
	if got := settings["ai_review_mode"]; got != "reviewer" {
		t.Fatalf("expected overridden review mode setting, got: %s", got)
	}

	projectCtx, err := TemplateContext(cfg.ProjectConfig, templateCtx)
	if err != nil {
		t.Fatalf("render template context: %v", err)
	}
	blocks := projectCtx["instruction_blocks"].(map[string]string)
	text := blocks["review_rules"]
	if !strings.Contains(text, "Review rules") {
		t.Fatalf("expected block title, got: %s", text)
	}
	if !strings.Contains(text, "General rules") {
		t.Fatalf("expected base general rule item, got: %s", text)
	}
	if !strings.Contains(text, "Playwright QA rules") {
		t.Fatalf("expected override playwright rule item, got: %s", text)
	}
	if strings.Contains(text, "Go rules") {
		t.Fatalf("did not expect Go rules for spec.ts file, got: %s", text)
	}
}

func mustTemplate(t *testing.T, raw string) dsl.TemplateString {
	t.Helper()
	tpl, err := dsl.NewTemplateString(raw)
	if err != nil {
		t.Fatalf("template %q: %v", raw, err)
	}
	return tpl
}
