package applypatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zvuk/pipelineai/internal/tools/approval"
)

func writeFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func TestAddFile(t *testing.T) {
	dir := t.TempDir()
	patch := "*** Begin Patch\n*** Add File: hello.txt\n+Hello\n*** End Patch\n"
	res, err := Exec(Args{Patch: patch, Workdir: dir}, nil)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if len(res.Added) != 1 {
		t.Fatalf("expected 1 added, got %v", res.Added)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "hello.txt"))
	if strings.TrimSpace(string(data)) != "Hello" {
		t.Fatalf("unexpected content: %q", string(data))
	}
}

func TestDeleteFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "bye.txt", "bye\n")
	patch := "*** Begin Patch\n*** Delete File: bye.txt\n*** End Patch\n"
	res, err := Exec(Args{Patch: patch, Workdir: dir}, nil)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if len(res.Deleted) != 1 {
		t.Fatalf("expected deleted, got %v", res.Deleted)
	}
	if _, err := os.Stat(filepath.Join(dir, "bye.txt")); !os.IsNotExist(err) {
		t.Fatalf("file must be deleted")
	}
}

func TestUpdateFileSimple(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "a\nb\n")
	patch := "*** Begin Patch\n*** Update File: a.txt\n@@\n a\n-b\n+B\n*** End Patch\n"
	_, err := Exec(Args{Patch: patch, Workdir: dir}, nil)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(got) != "a\nB\n" {
		t.Fatalf("unexpected content: %q", string(got))
	}
}

func TestMoveTo(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "old.txt", "line\n")
	patch := "*** Begin Patch\n*** Update File: old.txt\n*** Move to: new/new.txt\n@@\n line\n+added\n*** End Patch\n"
	_, err := Exec(Args{Patch: patch, Workdir: dir}, nil)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.txt")); err == nil {
		t.Fatalf("old file should be removed after move")
	}
	got, _ := os.ReadFile(filepath.Join(dir, "new/new.txt"))
	if string(got) != "line\nadded\n" {
		t.Fatalf("unexpected content: %q", string(got))
	}
}

func TestDryRun(t *testing.T) {
	dir := t.TempDir()
	patch := "*** Begin Patch\n*** Add File: x.txt\n+X\n*** End Patch\n"
	res, err := Exec(Args{Patch: patch, Workdir: dir, DryRun: true}, nil)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if len(res.Added) != 1 {
		t.Fatalf("expected dry added, got %v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.txt")); !os.IsNotExist(err) {
		t.Fatalf("file should not exist after dry-run")
	}
}

func TestApproverDenyCreate(t *testing.T) {
	dir := t.TempDir()
	patch := "*** Begin Patch\n*** Add File: docs/readme.md\n+hello\n*** End Patch\n"
	ap := &approval.ApplyPatchApprover{Rules: []approval.ApplyRule{{GlobPatterns: []string{"**/docs/**"}, AllowCreate: boolPtr(false)}}}
	if _, err := Exec(Args{Patch: patch, Workdir: dir}, ap); err == nil {
		t.Fatalf("expected approver deny error")
	}
}

func boolPtr(b bool) *bool { return &b }
