package executor

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// annotateBashStepError сохраняет полные stdout/stderr и добавляет путь в ошибку.
func (e *Executor) annotateBashStepError(stepType string, stepID string, err error) error {
	if err == nil {
		return nil
	}
	var bashErr *BashStepError
	if !errors.As(err, &bashErr) {
		return err
	}
	artifactPath, writeErr := e.writeBashFailureArtifact(stepType, stepID, bashErr)
	if writeErr == nil {
		bashErr.ArtifactPath = artifactPath
	} else {
		e.log.Warn("failed to write shell failure artifact",
			slog.String("step", stepID),
			slog.String("type", stepType),
			slog.String("error", writeErr.Error()),
		)
	}
	attrs := []any{
		slog.String("step", stepID),
		slog.String("type", stepType),
		slog.Int("exit_code", bashErr.ExitCode),
	}
	if bashErr.Line > 0 {
		attrs = append(attrs, slog.Int("line", bashErr.Line))
	}
	if strings.TrimSpace(bashErr.Command) != "" {
		attrs = append(attrs, slog.String("command", crop(bashErr.Command, 220)))
	}
	if strings.TrimSpace(bashErr.ArtifactPath) != "" {
		attrs = append(attrs, slog.String("artifact_path", bashErr.ArtifactPath))
	}
	if stderrTail := strings.TrimSpace(tailLines(bashErr.Stderr, 30)); stderrTail != "" {
		attrs = append(attrs, slog.String("stderr_tail", stderrTail))
	}
	if stdoutTail := strings.TrimSpace(tailLines(bashErr.Stdout, 30)); stdoutTail != "" {
		attrs = append(attrs, slog.String("stdout_tail", stdoutTail))
	}
	e.log.Error("shell step failed", attrs...)
	return bashErr
}

func (e *Executor) writeBashFailureArtifact(stepType string, stepID string, bashErr *BashStepError) (string, error) {
	if bashErr == nil {
		return "", fmt.Errorf("bash error is nil")
	}
	root := strings.TrimSpace(e.artifacts.Root())
	if root == "" {
		return "", fmt.Errorf("artifact root is empty")
	}
	dir := bashFailureArtifactDir(root, stepID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create shell failure artifact dir %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stdout.txt"), []byte(bashErr.Stdout), 0o600); err != nil {
		return "", fmt.Errorf("failed to write stdout artifact: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stderr.txt"), []byte(bashErr.Stderr), 0o600); err != nil {
		return "", fmt.Errorf("failed to write stderr artifact: %w", err)
	}
	meta := map[string]any{
		"step":        stepID,
		"type":        stepType,
		"exit_code":   bashErr.ExitCode,
		"line":        bashErr.Line,
		"command":     bashErr.Command,
		"timed_out":   bashErr.TimedOut,
		"created_at":  time.Now().UTC().Format(time.RFC3339Nano),
		"stdout_tail": tailLines(bashErr.Stdout, 80),
		"stderr_tail": tailLines(bashErr.Stderr, 80),
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal shell failure metadata: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o600); err != nil {
		return "", fmt.Errorf("failed to write shell failure metadata: %w", err)
	}
	return dir, nil
}
