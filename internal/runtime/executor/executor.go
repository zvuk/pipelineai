package executor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/zvuk/pipelineai/internal/runtime/llm"
	"github.com/zvuk/pipelineai/internal/tools/registry"
	"github.com/zvuk/pipelineai/pkg/artifacts"
	"github.com/zvuk/pipelineai/pkg/dsl"
)

// LLMClient описывает минимальные требования к клиенту LLM для исполнителя.
type LLMClient interface {
	CreateChatCompletion(ctx context.Context, req llm.ChatCompletionRequest) (llm.ChatCompletionResponse, error)
}

// Executor отвечает за выполнение шагов конфигурации.
type Executor struct {
	cfg       *dsl.Config
	client    LLMClient
	artifacts *artifacts.Manager
	log       *slog.Logger
	stepByID  map[string]dsl.Step
	muSteps   sync.RWMutex
	tools     *registry.Registry
	// produced — накопленные выходы шагов для подстановки в шаблоны последующих шагов
	produced map[string]map[string]ioValue
	muProd   sync.RWMutex
}

// runStepWithPolicy выполняет шаг с учётом политики retries/allow_failure.
// Retries применяется ко всем типам шагов; allow_failure позволяет не заваливать сценарий
// после исчерпания попыток.
func (e *Executor) runStepWithPolicy(ctx context.Context, step dsl.Step, stepID string, parallel int, name string) error {
	attempts := step.Retries
	if attempts <= 0 {
		attempts = 1
	}

	var lastErr error

	for attempt := 1; attempt <= attempts; attempt++ {
		stepTO := e.stepTimeoutFor(step)
		stepCtx, cancel := context.WithTimeout(ctx, stepTO)
		defer cancel()

		e.log.Info("step attempt start",
			slog.String("step", stepID),
			slog.String("type", step.Type),
			slog.String("name", name),
			slog.Int("attempt", attempt),
			slog.Int("retries", attempts),
		)

		err := e.runSingleStep(stepCtx, step, stepID, parallel, name)
		if err == nil {
			e.log.Info("step end",
				slog.String("step", stepID),
				slog.String("type", step.Type),
				slog.String("name", name),
			)
			return nil
		}

		lastErr = err
		e.log.Warn("step attempt failed",
			slog.String("step", stepID),
			slog.String("type", step.Type),
			slog.String("name", name),
			slog.Int("attempt", attempt),
			slog.Int("retries", attempts),
			slog.String("error", err.Error()),
		)

		// Если остались попытки — сделаем краткий бэкофф
		if attempt < attempts {
			backoff := time.Duration(500*attempt) * time.Millisecond
			if backoff > 3*time.Second {
				backoff = 3 * time.Second
			}
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	// Все попытки исчерпаны
	if step.AllowFailure {
		e.log.Error("step failed after retries, allowed to fail",
			slog.String("step", stepID),
			slog.String("type", step.Type),
			slog.String("name", name),
			slog.Int("retries", attempts),
			slog.String("error", lastErr.Error()),
		)
		// Сценарий продолжается
		return nil
	}

	return lastErr
}

// runSingleStep выполняет шаг без повторов.
func (e *Executor) runSingleStep(ctx context.Context, step dsl.Step, stepID string, parallel int, name string) error {
	switch step.Type {
	case "llm":
		_, path, err := e.RunLLMStep(ctx, stepID, nil)
		if err != nil {
			return err
		}
		e.log.Info("step end", slog.String("step", stepID), slog.String("type", "llm"), slog.String("name", name), slog.String("artifact_path", path))
		return nil
	case "shell":
		if _, err := e.RunShellStep(ctx, stepID, nil); err != nil {
			return err
		}
		e.log.Info("step end", slog.String("step", stepID), slog.String("type", "shell"), slog.String("name", name))
		return nil
	case "plan":
		if _, err := e.RunPlanStep(ctx, stepID, nil); err != nil {
			return err
		}
		e.log.Info("step end", slog.String("step", stepID), slog.String("type", "plan"), slog.String("name", name))
		return nil
	case "matrix":
		if err := e.RunMatrixStep(ctx, stepID, parallel); err != nil {
			return err
		}
		e.log.Info("step end", slog.String("step", stepID), slog.String("type", "matrix"), slog.String("name", name))
		return nil
	default:
		return fmt.Errorf("executor: unsupported step type: %s", step.Type)
	}
}

// New создаёт Executor с подготовленным менеджером артефактов.
func New(cfg *dsl.Config, client LLMClient, artifactDir string, log *slog.Logger) (*Executor, error) {
	if cfg == nil {
		return nil, fmt.Errorf("executor: configuration cannot be nil")
	}
	if client == nil {
		return nil, fmt.Errorf("executor: LLM client is required")
	}

	if strings.TrimSpace(artifactDir) == "" {
		artifactDir = cfg.Agent.ArtifactDir
	}
	// Синхронизируем agent.artifact_dir в шаблонах с фактическим менеджером артефактов
	cfg.Agent.ArtifactDir = artifactDir

	manager, err := artifacts.NewManager(artifactDir)
	if err != nil {
		return nil, err
	}

	if log == nil {
		log = slog.Default()
	}

	stepByID := make(map[string]dsl.Step, len(cfg.Steps))
	for _, step := range cfg.Steps {
		stepByID[step.ID] = step
	}

	return &Executor{
		cfg:       cfg,
		client:    client,
		artifacts: manager,
		log:       log.With(slog.String("component", "executor")),
		stepByID:  stepByID,
		tools:     registry.New(cfg),
	}, nil
}

// Config возвращает оригинальную конфигурацию.
func (e *Executor) Config() *dsl.Config {
	return e.cfg
}

// ArtifactManager возвращает менеджер артефактов.
func (e *Executor) ArtifactManager() *artifacts.Manager {
	return e.artifacts
}

// getStep безопасно читает шаг по id
func (e *Executor) getStep(id string) (dsl.Step, bool) {
	e.muSteps.RLock()
	s, ok := e.stepByID[id]
	e.muSteps.RUnlock()
	return s, ok
}

// RunShellStep выполняет шаг type=shell и возвращает путь до лога, если будет реализован вывод как артефакт.
func (e *Executor) RunShellStep(ctx context.Context, stepID string, extra map[string]any) (string, error) {
	step, ok := e.getStep(stepID)
	if !ok {
		return "", fmt.Errorf("executor: step %s not found", stepID)
	}
	if step.Type != "shell" || step.Shell == nil {
		return "", fmt.Errorf("executor: step %s is not of type shell", stepID)
	}
	// Рендерим inputs
	inputs, err := e.renderInputs(step, extra)
	if err != nil {
		return "", err
	}
	// Контекст для шаблонов шага shell
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
	run, err := step.Shell.Run.Execute(tctx)
	if err != nil {
		return "", fmt.Errorf("executor: failed to render shell.run for step %s: %w", stepID, err)
	}
	// DEBUG: логируем сценарий shell перед выполнением
	e.log.Debug("shell step script", slog.String("step", stepID), slog.String("run", crop(run, 300)))
	dir, err := step.Shell.Dir.Execute(tctx)
	if err != nil {
		return "", fmt.Errorf("executor: failed to render shell.dir for step %s: %w", stepID, err)
	}
	timeout := 5 * time.Minute
	if step.Shell.Timeout != nil {
		timeout = step.Shell.Timeout.Duration
	}
	// Запускаем bash -lc "run"
	shCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(shCtx, "bash", "-lc", run)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = strings.TrimSpace(dir)
	}
	var stdout, stderr bytes.Buffer
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
	err = cmd.Run()
	if err != nil {
		return "", fmt.Errorf("executor: shell step failed: %v, stderr: %s", err, stderr.String())
	}
	// Обработка outputs для shell шага
	if err := e.processShellOutputs(step, stdout.String(), stderr.String(), inputs, extra); err != nil {
		return "", err
	}
	// DEBUG: при конце шага — аутпуты и лог (обрезанные)
	e.log.Debug("shell step outputs",
		slog.String("step", stepID),
		slog.String("stdout", crop(stdout.String(), 150)),
		slog.String("stderr", crop(stderr.String(), 150)),
	)
	e.log.Info("shell step done", slog.String("step", stepID))
	return "", nil
}
