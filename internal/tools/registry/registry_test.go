package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zvuk/pipelineai/internal/runtime/llm"
	"github.com/zvuk/pipelineai/internal/tools/approval"
	"github.com/zvuk/pipelineai/pkg/dsl"
)

func mustTpl(t *testing.T, s string) dsl.TemplateString {
	t.Helper()
	ts, err := dsl.NewTemplateString(s)
	if err != nil {
		t.Fatalf("template error: %v", err)
	}
	return ts
}

func TestExecCall_ShellAllowed(t *testing.T) {
	reg := New(&dsl.Config{})
	ctx := context.Background()
	allowed := []string{"shell"}
	tc := llm.ToolCall{Type: "function", Function: llm.FunctionCall{Name: "shell", Arguments: `{"command":["bash","-lc","echo -n hi"]}`}}
	out := reg.ExecCall(ctx, tc, allowed, "", 3*time.Second, nil, nil)
	if !out.Ok {
		t.Fatalf("shell exec failed: %s", out.ToolError)
	}
	if out.Stdout != "hi" {
		t.Fatalf("unexpected stdout: %q", out.Stdout)
	}
}

func TestExecCall_Disallowed(t *testing.T) {
	reg := New(&dsl.Config{})
	ctx := context.Background()
	allowed := []string{"shell"}
	tc := llm.ToolCall{Type: "function", Function: llm.FunctionCall{Name: "apply_patch", Arguments: `{"input":"*** Begin Patch\n*** Add File: x.txt\n+ok\n*** End Patch\n"}`}}
	out := reg.ExecCall(ctx, tc, allowed, "", 2*time.Second, nil, nil)
	if out.Ok || out.ToolError == "" {
		t.Fatalf("expected disallowed tool error, got: ok=%v err=%q", out.Ok, out.ToolError)
	}
}

func TestExecCall_ApplyPatch_CreateFile(t *testing.T) {
	dir := t.TempDir()
	reg := New(&dsl.Config{})
	ctx := context.Background()
	allowed := []string{"apply_patch"}
	patch := "*** Begin Patch\n*** Add File: demo.txt\n+hello\n*** End Patch\n"
	a := map[string]any{"input": patch, "workdir": dir}
	ab, _ := json.Marshal(a)
	tc := llm.ToolCall{Type: "function", Function: llm.FunctionCall{Name: "apply_patch", Arguments: string(ab)}}
	// Используем пустой approver (nil) — разрешено всё
	out := reg.ExecCall(ctx, tc, allowed, dir, 2*time.Second, nil, &approval.ApplyPatchApprover{})
	if !out.Ok {
		t.Fatalf("apply_patch failed: %s", out.ToolError)
	}
	p := filepath.Join(dir, "demo.txt")
	if !fileExists(p) {
		t.Fatalf("expected file to be created: %s", p)
	}
}

func fileExists(path string) bool {
	if fi, err := os.Stat(path); err == nil {
		return !fi.IsDir()
	}
	return false
}

func TestExecCall_UserFunction_HttpRequest(t *testing.T) {
	// Локальный HTTP сервер
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("pong"))
	}))
	defer srv.Close()

	fn := dsl.Function{
		Name:        "http_request",
		Description: "HTTP request via curl",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":    map[string]any{"type": "string"},
				"method": map[string]any{"type": "string"},
			},
			"required": []any{"url", "method"},
		},
		Implementation: dsl.FunctionImplementation{Type: "shell", ShellTemplate: mustTpl(t, `curl -sS -X "{{ .params.method }}" "{{ .params.url }}"`)},
	}
	cfg := &dsl.Config{Functions: []dsl.Function{fn}}
	reg := New(cfg)
	ctx := context.Background()
	allowed := []string{"http_request"}
	args := `{"url":"` + srv.URL + `","method":"GET"}`
	tc := llm.ToolCall{Type: "function", Function: llm.FunctionCall{Name: "http_request", Arguments: args}}
	out := reg.ExecCall(ctx, tc, allowed, "", 4*time.Second, nil, nil)
	if !out.Ok {
		t.Fatalf("http_request failed: %s", out.ToolError)
	}
	if out.Stdout != "pong" {
		t.Fatalf("unexpected http response: %q", out.Stdout)
	}
}
