package executor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

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
	}
	if extra != nil {
		for k, v := range extra {
			tctx[k] = v
		}
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
			return fmt.Errorf("executor: failed to render env %s for step %s: %v", k, stepID, err)
		}
		env = append(env, fmt.Sprintf("%s=%s", k, rv))
	}
	cmd.Env = env
	return nil
}

// runBashStep запускает скрипт через bash -lc и возвращает stdout/stderr.
func runBashStep(ctx context.Context, run, dir string, timeout time.Duration, stepType string, stepID string, envVars map[string]string, tctx map[string]any) (string, string, error) {
	shCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(shCtx, "bash", "-lc", run)
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
		return stdout.String(), stderr.String(), fmt.Errorf("executor: %s step failed: %v, stderr: %s", stepType, err, stderr.String())
	}

	return stdout.String(), stderr.String(), nil
}
