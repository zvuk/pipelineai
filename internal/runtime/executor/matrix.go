package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zvuk/pipelineai/internal/runtime/matrix"
)

// RunMatrixStep разворачивает элементы из manifest и исполняет шаблонный шаг для каждого элемента с учётом параллелизма.
func (e *Executor) RunMatrixStep(ctx context.Context, stepID string, parallel int) error {
	step, ok := e.getStep(stepID)
	if !ok {
		return fmt.Errorf("executor: step %s not found", stepID)
	}
	if step.Type != "matrix" || step.Matrix == nil || step.Run == nil {
		return fmt.Errorf("executor: step %s is not of type matrix", stepID)
	}

	// Базовый контекст для шаблонов matrix.from_yaml
	tctx := map[string]any{
		"agent":    e.templateAgent(),
		"step":     templateStep(step),
		"defaults": e.templateDefaults(),
		"outputs":  e.outputsContext(),
	}

	manifestPath, err := step.Matrix.FromYAML.Execute(tctx)
	if err != nil {
		return fmt.Errorf("executor: failed to render matrix.from_yaml for step %s: %w", stepID, err)
	}
	manifestPath = strings.TrimSpace(manifestPath)
	if manifestPath == "" {
		return fmt.Errorf("executor: matrix.from_yaml evaluated to empty for step %s", stepID)
	}

	// Читаем manifest и извлекаем items
	v, err := matrix.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	items, err := matrix.SelectItems(v, step.Matrix.Select)
	if err != nil {
		return err
	}
	e.log.Info("matrix expand", slog.String("step", stepID), slog.Int("items", len(items)))

	if len(items) == 0 {
		return nil
	}
	if parallel <= 0 {
		parallel = 1
	}
	if parallel > len(items) {
		parallel = len(items)
	}

	// Шаг-шаблон, который будет исполняться для каждого элемента
	runStepID := strings.TrimSpace(step.Run.Step)
	tplStep, ok := e.getStep(runStepID)
	if !ok {
		return fmt.Errorf("executor: matrix.run.step %s not found", runStepID)
	}

	// Для matrix-шага поддерживаем retries/allow_failure на уровне дочернего шага (tplStep).
	// Это позволяет изолировать нестабильность LLM/инструментов на уровне отдельных файлов.
	childAttempts := tplStep.Retries
	if childAttempts <= 0 {
		childAttempts = 1
	}
	childAllowFailure := tplStep.AllowFailure || step.AllowFailure

	// Запустим воркеры
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	errs := make(chan error, len(items))

	for idx, item := range items {
		// Вычисляем item_id
		// Контекст для шаблона item_id включает .item
		idCtx := map[string]any{"item": item, "index": idx}
		itemID, err := step.Matrix.ItemID.Execute(idCtx)
		if err != nil {
			return fmt.Errorf("executor: failed to render matrix.item_id for step %s: %v", stepID, err)
		}
		itemID = strings.TrimSpace(itemID)
		if itemID == "" {
			return fmt.Errorf("executor: matrix.item_id evaluated to empty at index %d", idx)
		}

		// Готовим inject
		inject := map[string]any{}
		for k, tpl := range step.Matrix.Inject {
			val, err := tpl.Execute(map[string]any{
				"item":     item,
				"agent":    e.templateAgent(),
				"step":     templateStep(step),
				"defaults": e.templateDefaults(),
				"outputs":  e.outputsContext(),
			})
			if err != nil {
				return fmt.Errorf("executor: failed to render matrix.inject[%s] for step %s: %v", k, stepID, err)
			}
			inject[k] = strings.TrimSpace(val)
		}

		// Полный контекст для дочернего шага: .matrix
		matrixCtx := map[string]any{
			"matrix": map[string]any{
				"item":    item,
				"item_id": itemID,
				// items — весь массив элементов (может пригодиться в шаблонах)
				"items": items,
			},
		}
		// приклеим inject как .matrix.<k>
		if len(inject) > 0 {
			mm := matrixCtx["matrix"].(map[string]any)
			for k, v := range inject {
				mm[k] = v
			}
		}

		// Создадим каталог статусов на элемент
		if root := strings.TrimSpace(e.artifacts.Root()); root != "" {
			_ = os.MkdirAll(filepath.Join(root, "items", itemID), 0o755)
		}

		sem <- struct{}{}
		wg.Add(1)
		go func(itemID string, extra map[string]any) {
			defer wg.Done()
			defer func() { <-sem }()

			started := time.Now().UTC()
			status := map[string]any{
				"step":        stepID,
				"run_step":    runStepID,
				"item_id":     itemID,
				"started_at":  started.Format(time.RFC3339Nano),
				"attempts":    childAttempts,
				"allow_fail":  childAllowFailure,
				"ok":          false,
				"error":       "",
				"finished_at": "",
			}

			var lastErr error

			// Повторно выполняем дочерний шаг в соответствии с childAttempts.
			for attempt := 1; attempt <= childAttempts; attempt++ {
				// Исполним шаг-шаблон по типу
				switch tplStep.Type {
				case "llm":
					_, _, lastErr = e.RunLLMStep(ctx, runStepID, extra)
				case "shell":
					_, lastErr = e.RunShellStep(ctx, runStepID, extra)
				default:
					lastErr = fmt.Errorf("executor: matrix.run.step %s unsupported type: %s", runStepID, tplStep.Type)
				}

				if lastErr == nil {
					status["ok"] = true
					break
				}

				status["error"] = lastErr.Error()
				e.log.Error("matrix item attempt failed",
					slog.String("step", stepID),
					slog.String("run_step", runStepID),
					slog.String("item_id", itemID),
					slog.Int("attempt", attempt),
					slog.Int("retries", childAttempts),
					slog.String("error", lastErr.Error()),
				)

				// Бэкофф между попытками, если ещё есть повторы
				if attempt < childAttempts {
					backoff := time.Duration(500*attempt) * time.Millisecond
					if backoff > 3*time.Second {
						backoff = 3 * time.Second
					}
					select {
					case <-time.After(backoff):
					case <-ctx.Done():
						lastErr = ctx.Err()
						attempt = childAttempts
					}
				}
			}

			finished := time.Now().UTC()
			if lastErr == nil {
				status["ok"] = true
			} else {
				// Все попытки исчерпаны
				if childAllowFailure {
					e.log.Error("matrix item failed after retries, allowed to fail",
						slog.String("step", stepID),
						slog.String("run_step", runStepID),
						slog.String("item_id", itemID),
						slog.Int("retries", childAttempts),
						slog.String("error", lastErr.Error()),
					)
					// Не пробрасываем ошибку наверх, сценарий продолжает выполнение.
				} else {
					e.log.Error("matrix item failed after retries",
						slog.String("step", stepID),
						slog.String("run_step", runStepID),
						slog.String("item_id", itemID),
						slog.Int("retries", childAttempts),
						slog.String("error", lastErr.Error()),
					)
					errs <- lastErr
				}
			}
			status["finished_at"] = finished.Format(time.RFC3339Nano)
			// Запишем статус в .agent/artifacts/items/<id>/status.json
			_ = e.writeItemStatus(itemID, status)
		}(itemID, matrixCtx)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// writeItemStatus сохраняет status.json для конкретного item_id.
func (e *Executor) writeItemStatus(itemID string, status map[string]any) error {
	root := e.artifacts.Root()
	if strings.TrimSpace(root) == "" {
		return nil
	}
	dir := filepath.Join(root, "items", itemID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	p := filepath.Join(dir, "status.json")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(status)
}
