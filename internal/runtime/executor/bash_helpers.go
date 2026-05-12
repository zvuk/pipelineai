package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zvuk/pipelineai/internal/runtime/projectconfig"
	"github.com/zvuk/pipelineai/pkg/dsl"
)

// renderStepContext формирует общий шаблонный контекст шага.
func (e *Executor) renderStepContext(step dsl.Step, inputs map[string]ioValue, extra map[string]any) map[string]any {
	tctx := map[string]any{
		"agent":    e.templateAgent(),
		"step":     templateStep(step),
		"defaults": e.templateDefaults(),
		"inputs":   inputsToTemplate(inputs),
		"outputs":  e.outputsContext(),
		"project":  projectconfig.StaticTemplateContext(e.cfg.ProjectConfig),
	}
	for k, v := range extra {
		tctx[k] = v
	}
	return tctx
}

func stepTimeoutOrDefault(v *dsl.Duration) time.Duration {
	timeout := 5 * time.Minute
	if v != nil {
		timeout = v.Duration
	}
	return timeout
}

// applyStepEnv рендерит step.env и добавляет его к окружению процесса.
func applyStepEnv(cmd *exec.Cmd, envVars map[string]string, tctx map[string]any, stepID string) error {
	if len(envVars) == 0 {
		return nil
	}
	env := os.Environ()
	for k, v := range envVars {
		rv, err := dsl.RenderStringWithDefaults(v, tctx)
		if err != nil {
			return fmt.Errorf("executor: failed to render env %s for step %s: %w", k, stepID, err)
		}
		env = append(env, fmt.Sprintf("%s=%s", k, rv))
	}
	cmd.Env = env
	return nil
}

// runBashStep запускает скрипт через bash с ERR-trap и возвращает stdout/stderr.
func runBashStep(ctx context.Context, run, dir string, timeout time.Duration, stepType string, stepID string, envVars map[string]string, tctx map[string]any) (string, string, error) {
	shCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	scriptPath, err := writeTrackedBashScript(run)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = os.Remove(scriptPath) }()
	trapPath := scriptPath + ".err"
	defer func() { _ = os.Remove(trapPath) }()

	cmd := exec.CommandContext(shCtx, "bash", scriptPath)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = strings.TrimSpace(dir)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := applyStepEnv(cmd, envVars, tctx, stepID); err != nil {
		return "", "", err
	}
	if err := cmd.Run(); err != nil {
		detail := readBashTrapDetail(trapPath)
		exitCode := extractExitCode(err)
		if detail.ExitCode != 0 {
			exitCode = detail.ExitCode
		}
		timedOut := errors.Is(shCtx.Err(), context.DeadlineExceeded)
		if timedOut {
			exitCode = 124
		}
		return stdout.String(), stderr.String(), &BashStepError{
			StepType: stepType,
			StepID:   stepID,
			Err:      err,
			ExitCode: exitCode,
			Line:     detail.Line,
			Command:  detail.Command,
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			TimedOut: timedOut,
		}
	}

	return stdout.String(), stderr.String(), nil
}

// BashStepError хранит диагностические детали падения shell/plan шага.
type BashStepError struct {
	StepType     string
	StepID       string
	Err          error
	ExitCode     int
	Line         int
	Command      string
	Stdout       string
	Stderr       string
	TimedOut     bool
	ArtifactPath string
}

func (e *BashStepError) Error() string {
	if e == nil {
		return ""
	}
	reason := "failed"
	if e.TimedOut {
		reason = "timed out"
	}
	parts := []string{
		fmt.Sprintf("executor: %s step %s %s", e.StepType, e.StepID, reason),
		fmt.Sprintf("exit_code=%d", e.ExitCode),
	}
	if e.Line > 0 {
		parts = append(parts, fmt.Sprintf("line=%d", e.Line))
	}
	if strings.TrimSpace(e.Command) != "" {
		parts = append(parts, fmt.Sprintf("command=%q", cropLocal(e.Command, 180)))
	}
	if tail := strings.TrimSpace(tailLines(e.Stderr, 40)); tail != "" {
		parts = append(parts, "stderr_tail="+strconv.Quote(tail))
	}
	if tail := strings.TrimSpace(tailLines(e.Stdout, 40)); tail != "" {
		parts = append(parts, "stdout_tail="+strconv.Quote(tail))
	}
	if strings.TrimSpace(e.ArtifactPath) != "" {
		parts = append(parts, "artifact_path="+e.ArtifactPath)
	}
	return strings.Join(parts, ", ")
}

func writeTrackedBashScript(run string) (string, error) {
	file, err := os.CreateTemp("", "pipelineai-step-*.sh")
	if err != nil {
		return "", fmt.Errorf("executor: failed to create shell script temp file: %w", err)
	}
	defer file.Close()
	trapPath := file.Name() + ".err"
	wrapper := []string{
		"__pai_err_file=" + bashShellQuote(trapPath),
		`trap '__pai_code=$?; __pai_line=$((LINENO - 2)); printf "%s\t%s\t%s\n" "$__pai_code" "$__pai_line" "$BASH_COMMAND" > "$__pai_err_file"; exit "$__pai_code"' ERR`,
		run,
	}
	if _, err := file.WriteString(strings.Join(wrapper, "\n")); err != nil {
		return "", fmt.Errorf("executor: failed to write shell script temp file: %w", err)
	}
	if err := file.Chmod(0o700); err != nil {
		return "", fmt.Errorf("executor: failed to chmod shell script temp file: %w", err)
	}
	return file.Name(), nil
}

type bashTrapDetail struct {
	ExitCode int
	Line     int
	Command  string
}

func readBashTrapDetail(path string) bashTrapDetail {
	data, err := os.ReadFile(path)
	if err != nil {
		return bashTrapDetail{}
	}
	parts := strings.SplitN(strings.TrimSpace(string(data)), "\t", 3)
	if len(parts) < 2 {
		return bashTrapDetail{}
	}
	exitCode, _ := strconv.Atoi(parts[0])
	line, _ := strconv.Atoi(parts[1])
	command := ""
	if len(parts) == 3 {
		command = strings.TrimSpace(parts[2])
	}
	if line < 0 {
		line = 0
	}
	return bashTrapDetail{ExitCode: exitCode, Line: line, Command: command}
}

func extractExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func bashShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func tailLines(s string, maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= maxLines {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-maxLines:], "\n")
}

func cropLocal(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}

func bashFailureArtifactDir(root string, stepID string) string {
	return filepath.Join(root, "shell", stepID)
}
