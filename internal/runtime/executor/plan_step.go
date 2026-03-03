package executor

import (
	"context"
	"fmt"
	"strings"
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
	tctx := e.renderStepContext(step, inputs, extra)

	engine := strings.ToLower(strings.TrimSpace(step.Plan.Engine))
	if engine == "partition" {
		return e.runPlanPartitionStep(step, stepID, inputs, extra, tctx)
	}
	if engine != "" && engine != "shell" {
		return "", fmt.Errorf("executor: unsupported plan engine for step %s: %s", stepID, step.Plan.Engine)
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

	timeout := stepTimeoutOrDefault(step.Plan.Timeout)
	stdout, stderr, err := runBashStep(ctx, run, dir, timeout, "plan", stepID, step.Env, tctx)
	if err != nil {
		return "", err
	}

	// Outputs для plan шага обрабатываются так же, как для shell.
	if err := e.processShellOutputs(step, stdout, stderr, inputs, extra); err != nil {
		return "", err
	}

	e.log.Debug("plan step outputs",
		"step", stepID,
		"stdout", crop(stdout, 150),
		"stderr", crop(stderr, 150),
	)
	e.log.Info("plan step done", "step", stepID)

	return "", nil
}
