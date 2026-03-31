package artifacts

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewManagerAndWriteLLMResponse(t *testing.T) {
	dir := t.TempDir()
	manager, err := NewManager(dir)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	payload := map[string]any{"hello": "world", "meta": map[string]any{"hash": "same"}}
	path, err := manager.WriteLLMResponse("demo", payload)
	if err != nil {
		t.Fatalf("failed to write artifact: %v", err)
	}

	if !strings.Contains(path, filepath.Join("llm", "demo")) || !strings.HasSuffix(strings.ToLower(path), ".json") {
		t.Fatalf("unexpected artifact path: %s", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read artifact: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("artifact must not be empty")
	}
}

func TestNewManagerEmptyRoot(t *testing.T) {
	if _, err := NewManager(""); err == nil {
		t.Fatal("expected an error for empty root directory")
	}
}

func TestWriteLLMResponse_Dedup(t *testing.T) {
	dir := t.TempDir()
	manager, err := NewManager(dir)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	payload := map[string]any{"data": 1, "meta": map[string]any{"hash": "abc"}}
	p1, err := manager.WriteLLMResponse("item", payload)
	if err != nil {
		t.Fatalf("failed to write artifact: %v", err)
	}
	// Вторая запись с тем же хэшем должна вернуть тот же путь
	p2, err := manager.WriteLLMResponse("item", payload)
	if err != nil {
		t.Fatalf("failed to write artifact: %v", err)
	}
	if p1 != p2 {
		t.Fatalf("expected dedup to return same path, got %s and %s", p1, p2)
	}
	// Проверим, что в директории только один файл
	stepDir := filepath.Join(dir, "llm", "item")
	entries, err := os.ReadDir(stepDir)
	if err != nil {
		t.Fatalf("failed to read dir: %v", err)
	}
	files := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			files++
		}
	}
	if files != 1 {
		t.Fatalf("expected 1 file after dedup, got %d", files)
	}
}

func TestWriteLLMResponse_Concurrent(t *testing.T) {
	dir := t.TempDir()
	manager, err := NewManager(dir)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	const workers = 32
	errCh := make(chan error, workers)
	done := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			payload := map[string]any{
				"worker": idx,
				"meta": map[string]any{
					"hash": "hash-" + strconv.Itoa(idx),
				},
			}
			if _, err := manager.WriteLLMResponse("review_file", payload); err != nil {
				errCh <- err
			}
		}(i)
	}

	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent WriteLLMResponse calls must finish without hanging")
	}
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent WriteLLMResponse returned error: %v", err)
		}
	}

	stepDir := filepath.Join(dir, "llm", "review_file")
	entries, err := os.ReadDir(stepDir)
	if err != nil {
		t.Fatalf("failed to read dir: %v", err)
	}

	files := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			files++
		}
	}
	if files != workers {
		t.Fatalf("expected %d files after concurrent writes, got %d", workers, files)
	}
}
