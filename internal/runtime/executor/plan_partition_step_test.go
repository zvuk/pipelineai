package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zvuk/pipelineai/pkg/dsl"
)

func TestRunPlanStepPartitionEngine(t *testing.T) {
	tmp := t.TempDir()
	artifactDir := filepath.Join(tmp, ".agent", "artifacts")
	planDir := filepath.Join(artifactDir, "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir plan dir: %v", err)
	}

	candidatesPath := filepath.Join(planDir, "candidates.json")
	manifestJSONPath := filepath.Join(planDir, "file-manifest.json")
	manifestYAMLPath := filepath.Join(planDir, "file-manifest.yaml")
	resourcesDir := filepath.Join(planDir, "review-resources")

	basePromptPath := filepath.Join(tmp, "base", "file-review.md")
	baseRulesDir := filepath.Join(tmp, "base", "rules")
	overrideConfigPath := filepath.Join(tmp, "overrides", "ai-review-overrides.yml")
	goPromptOverlayPath := filepath.Join(tmp, "overrides", "overlays", "go-prompt.md")
	goRuleOverlayPath := filepath.Join(tmp, "overrides", "overlays", "go-rule.md")

	if err := os.MkdirAll(filepath.Dir(basePromptPath), 0o755); err != nil {
		t.Fatalf("mkdir base prompt dir: %v", err)
	}
	if err := os.MkdirAll(baseRulesDir, 0o755); err != nil {
		t.Fatalf("mkdir base rules dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(overrideConfigPath), 0o755); err != nil {
		t.Fatalf("mkdir override dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(goPromptOverlayPath), 0o755); err != nil {
		t.Fatalf("mkdir overlays dir: %v", err)
	}

	if err := os.WriteFile(basePromptPath, []byte("BASE PROMPT\n"), 0o644); err != nil {
		t.Fatalf("write base prompt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(baseRulesDir, "general.md"), []byte("BASE RULE\n"), 0o644); err != nil {
		t.Fatalf("write base rule: %v", err)
	}
	if err := os.WriteFile(goPromptOverlayPath, []byte("GO PROMPT OVERLAY\n"), 0o644); err != nil {
		t.Fatalf("write go prompt overlay: %v", err)
	}
	if err := os.WriteFile(goRuleOverlayPath, []byte("GO RULE OVERLAY\n"), 0o644); err != nil {
		t.Fatalf("write go rule overlay: %v", err)
	}

	overrideYAML := `
version: 1
default_profile: default
profiles:
  default:
    prompt_overlays:
      file_review:
        - path: overlays/go-prompt.md
          mode: append
          when:
            any_glob:
              - "**/*.go"
    rule_overlays:
      - target: general.md
        path: overlays/go-rule.md
        mode: append
        when:
          any_glob:
            - "**/*.go"
`
	if err := os.WriteFile(overrideConfigPath, []byte(strings.TrimSpace(overrideYAML)+"\n"), 0o644); err != nil {
		t.Fatalf("write override config: %v", err)
	}

	candidatesJSON := `
{
  "items": [
    {"file_path":"internal/auth/service.go","item_hash":"aaa111","item_weight":40},
    {"file_path":"docs/api/readme.md","item_hash":"bbb222","item_weight":10},
    {"file_path":"docs/api/changelog.md","item_hash":"ccc333","item_weight":12}
  ]
}
`
	if err := os.WriteFile(candidatesPath, []byte(strings.TrimSpace(candidatesJSON)+"\n"), 0o644); err != nil {
		t.Fatalf("write candidates: %v", err)
	}

	cfg := &dsl.Config{
		Version: 1,
		Agent: dsl.Agent{
			Name:        "partition-test",
			Model:       "gpt-test",
			ArtifactDir: artifactDir,
			OpenAI:      dsl.AgentOpenAI{BaseURL: "http://localhost", APIKeyEnv: "LLM_API_KEY"},
		},
		Steps: []dsl.Step{
			{
				ID:   "build_units",
				Type: "plan",
				Plan: &dsl.StepPlan{
					Engine: "partition",
					Partition: &dsl.StepPlanPartition{
						SourcePath:         mustTemplate(t, candidatesPath),
						Select:             "items",
						ManifestJSONPath:   mustTemplate(t, manifestJSONPath),
						ManifestYAMLPath:   mustTemplate(t, manifestYAMLPath),
						UnitResourcesDir:   mustTemplate(t, resourcesDir),
						BasePromptPath:     mustTemplate(t, basePromptPath),
						BaseRulesDir:       mustTemplate(t, baseRulesDir),
						OverrideConfigPath: mustTemplate(t, overrideConfigPath),
						OverrideProfile:    mustTemplate(t, "default"),
						SwitchToBucketsAt:  mustTemplate(t, "2"),
						BucketMaxItems:     mustTemplate(t, "2"),
						BucketMaxWeight:    mustTemplate(t, "100"),
						PriorityWeight:     mustTemplate(t, "220"),
						PriorityAnyGlob:    []string{"**/auth/**"},
						NonPriorityAnyExt:  []string{".md"},
					},
				},
				Outputs: []dsl.StepOutput{
					{
						ID:   "manifest_json",
						Type: "file",
						From: "path",
						Path: mustTemplate(t, manifestJSONPath),
					},
				},
			},
		},
	}

	exec := newTestExecutor(t, cfg, &fakeClient{}, artifactDir)
	if _, err := exec.RunPlanStep(context.Background(), "build_units", nil); err != nil {
		t.Fatalf("run partition step: %v", err)
	}

	raw, err := os.ReadFile(manifestJSONPath)
	if err != nil {
		t.Fatalf("read manifest json: %v", err)
	}

	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	rawItems, ok := manifest["items"].([]any)
	if !ok {
		t.Fatalf("manifest.items must be array, got %T", manifest["items"])
	}
	if len(rawItems) != 2 {
		t.Fatalf("expected 2 units, got %d", len(rawItems))
	}

	var goUnit map[string]any
	var groupUnit map[string]any
	for _, it := range rawItems {
		obj, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("item must be object, got %T", it)
		}
		primary := strings.TrimSpace(asString(obj["primary_file_path"]))
		if strings.HasSuffix(primary, ".go") {
			goUnit = obj
		}
		if strings.TrimSpace(asString(obj["unit_type"])) == "group" {
			groupUnit = obj
		}
	}
	if goUnit == nil {
		t.Fatal("go unit not found")
	}
	if groupUnit == nil {
		t.Fatal("group unit not found")
	}
	if testAsInt(groupUnit["file_count"]) != 2 {
		t.Fatalf("expected grouped low-risk unit with file_count=2, got %v", groupUnit["file_count"])
	}

	goPromptPath := strings.TrimSpace(asString(goUnit["prompt_file_path"]))
	goRulesDir := strings.TrimSpace(asString(goUnit["rules_dir"]))
	if goPromptPath == "" || goRulesDir == "" {
		t.Fatalf("go unit must contain prompt_file_path and rules_dir, got prompt=%q rules=%q", goPromptPath, goRulesDir)
	}

	goPromptRaw, err := os.ReadFile(goPromptPath)
	if err != nil {
		t.Fatalf("read go unit prompt: %v", err)
	}
	if !strings.Contains(string(goPromptRaw), "GO PROMPT OVERLAY") {
		t.Fatalf("expected GO PROMPT OVERLAY in unit prompt, got: %s", string(goPromptRaw))
	}

	goRuleRaw, err := os.ReadFile(filepath.Join(goRulesDir, "general.md"))
	if err != nil {
		t.Fatalf("read go unit rule: %v", err)
	}
	if !strings.Contains(string(goRuleRaw), "GO RULE OVERLAY") {
		t.Fatalf("expected GO RULE OVERLAY in unit rule, got: %s", string(goRuleRaw))
	}
}

func testAsInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case float64:
		return int(t)
	default:
		return 0
	}
}
