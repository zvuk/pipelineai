package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/zvuk/pipelineai/pkg/dsl"
)

// RunPlanStep выполняет шаг type=plan.
// По механике выполнения он эквивалентен shell-шагу, но семантически предназначен
// для подготовки стратегии/манифестов последующих этапов сценария.
func (e *Executor) RunPlanStep(ctx context.Context, stepID string, extra map[string]any) (string, error) {
	step, ok := e.getStep(stepID)
	if !ok {
		return "", fmt.Errorf("executor: step %s not found", stepID)
	}
	if step.Type != "plan" || step.Plan == nil {
		return "", fmt.Errorf("executor: step %s is not of type plan", stepID)
	}

	// Рендерим inputs
	inputs, err := e.renderInputs(step, extra)
	if err != nil {
		return "", err
	}

	// Контекст для шаблонов step.plan.*
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

	run, err := step.Plan.Run.Execute(tctx)
	if err != nil {
		return "", fmt.Errorf("executor: failed to render plan.run for step %s: %w", stepID, err)
	}
	e.log.Debug("plan step script", "step", stepID, "run", crop(run, 300))

	dir, err := step.Plan.Dir.Execute(tctx)
	if err != nil {
		return "", fmt.Errorf("executor: failed to render plan.dir for step %s: %w", stepID, err)
	}

	timeout := 5 * time.Minute
	if step.Plan.Timeout != nil {
		timeout = step.Plan.Timeout.Duration
	}

	planCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(planCtx, "bash", "-lc", run)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = strings.TrimSpace(dir)
	}

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Переменные окружения шага
	if len(step.Env) > 0 {
		env := os.Environ()
		for k, v := range step.Env {
			rv, rerr := dsl.RenderStringWithDefaults(v, tctx)
			if rerr != nil {
				return "", fmt.Errorf("executor: failed to render env %s for step %s: %v", k, stepID, rerr)
			}
			env = append(env, fmt.Sprintf("%s=%s", k, rv))
		}
		cmd.Env = env
	}

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("executor: plan step failed: %v, stderr: %s", err, stderr.String())
	}

	// Outputs для plan шага обрабатываются так же, как для shell.
	if err := e.processShellOutputs(step, stdout.String(), stderr.String(), inputs, extra); err != nil {
		return "", err
	}

	e.log.Debug("plan step outputs",
		"step", stepID,
		"stdout", crop(stdout.String(), 150),
		"stderr", crop(stderr.String(), 150),
	)
	e.log.Info("plan step done", "step", stepID)

	return "", nil
}
