package shell

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestExec_BoundedCapturePersistsOverflow(t *testing.T) {
	res, err := Exec(context.Background(), Args{
		Command:         []string{"bash", "-lc", "head -c 4096 /dev/zero | tr '\\0' x"},
		Timeout:         5 * time.Second,
		MaxCaptureBytes: 256,
		PersistOverflow: true,
	}, nil)
	if err != nil {
		t.Fatalf("shell exec failed: %v", err)
	}
	if !res.StdoutTruncated {
		t.Fatal("expected stdout preview to be truncated")
	}
	if res.StdoutBytes <= 256 {
		t.Fatalf("expected full stdout bytes to exceed preview budget, got %d", res.StdoutBytes)
	}
	if strings.TrimSpace(res.StdoutCapturePath) == "" {
		t.Fatal("expected overflow capture path to be created")
	}
	if _, err := os.Stat(res.StdoutCapturePath); err != nil {
		t.Fatalf("expected capture file to exist: %v", err)
	}
	_ = os.Remove(res.StdoutCapturePath)
}

func TestExec_BoundedCaptureWithoutPersistenceDoesNotCreateSpillFile(t *testing.T) {
	res, err := Exec(context.Background(), Args{
		Command:         []string{"bash", "-lc", "head -c 4096 /dev/zero | tr '\\0' y"},
		Timeout:         5 * time.Second,
		MaxCaptureBytes: 256,
	}, nil)
	if err != nil {
		t.Fatalf("shell exec failed: %v", err)
	}
	if !res.StdoutTruncated {
		t.Fatal("expected stdout preview to be truncated")
	}
	if res.StdoutCapturePath != "" {
		t.Fatalf("expected no capture path without persistence, got %q", res.StdoutCapturePath)
	}
}
