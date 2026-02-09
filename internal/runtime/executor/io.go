package executor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zvuk/pipelineai/internal/runtime/llm"
	"github.com/zvuk/pipelineai/pkg/dsl"
)

// ioValue представляет универсальное значение ввода/вывода шага.
// Для file значим только Path; для inline — Inline.
type ioValue struct {
	Path   string
	Inline string
}

// renderInputs вычисляет inputs шага и возвращает карту по id.
// Ошибки валидируются на этапе DSL, здесь — только рендер и проверка существования для file.
func (e *Executor) renderInputs(step dsl.Step, extra map[string]any) (map[string]ioValue, error) {
	result := make(map[string]ioValue, len(step.Inputs))
	if len(step.Inputs) == 0 {
		return result, nil
	}
	// Контекст шаблонов для inputs: разрешаем обращаться к outputs предыдущих шагов
	tctx := map[string]any{
		"agent":    e.templateAgent(),
		"step":     templateStep(step),
		"defaults": e.templateDefaults(),
		"outputs":  e.outputsContext(),
	}
	// Пробрасываем дополнительный контекст (например, matrix)
	if extra != nil {
		for k, v := range extra {
			tctx[k] = v
		}
	}

	for _, in := range step.Inputs {
		id := strings.TrimSpace(in.ID)
		if id == "" {
			continue
		}
		t := strings.ToLower(strings.TrimSpace(in.Type))
		switch t {
		case "file":
			p, err := in.Path.Execute(tctx)
			if err != nil {
				return nil, fmt.Errorf("executor: failed to render inputs[%s].path: %w", id, err)
			}
			p = strings.TrimSpace(p)
			if p == "" {
				return nil, fmt.Errorf("executor: inputs[%s].path evaluated to empty", id)
			}
			if _, err := os.Stat(p); err != nil {
				return nil, fmt.Errorf("executor: inputs[%s].path does not exist: %v", id, err)
			}
			result[id] = ioValue{Path: p}
		case "inline":
			txt, err := in.Template.Execute(tctx)
			if err != nil {
				return nil, fmt.Errorf("executor: failed to render inputs[%s].template: %w", id, err)
			}
			result[id] = ioValue{Inline: txt}
		default:
			return nil, fmt.Errorf("executor: unsupported input type: %s", in.Type)
		}
	}
	return result, nil
}

// outputsContext возвращает карту ранее произведённых выходов, пригодную для шаблонов: outputs.<step>.<id>.path/inline
func (e *Executor) outputsContext() map[string]map[string]map[string]string {
	e.muProd.RLock()
	defer e.muProd.RUnlock()
	out := make(map[string]map[string]map[string]string, len(e.produced))
	for stepID, items := range e.produced {
		inner := make(map[string]map[string]string, len(items))
		for id, v := range items {
			inner[id] = map[string]string{
				"path":   v.Path,
				"inline": v.Inline,
			}
		}
		out[stepID] = inner
	}
	return out
}

// recordOutput сохраняет произведённый вывод шага, чтобы он был доступен последующим шагам через {{ outputs.<step>.<id>.path }}
func (e *Executor) recordOutput(stepID, outputID string, v ioValue) {
	e.muProd.Lock()
	if e.produced == nil {
		e.produced = make(map[string]map[string]ioValue)
	}
	if _, ok := e.produced[stepID]; !ok {
		e.produced[stepID] = make(map[string]ioValue)
	}
	e.produced[stepID][outputID] = v
	e.muProd.Unlock()
}

// processLLMOutputs обрабатывает outputs шага type=llm: записывает файлы/логи и регистрирует outputs в контексте.
func (e *Executor) processLLMOutputs(step dsl.Step, resp llm.ChatCompletionResponse, inputs map[string]ioValue, extra map[string]any) error {
	outs := step.Outputs
	if len(outs) == 0 {
		return nil
	}
	// контекст для рендера путей: агент, шаг, defaults, inputs, outputs (уже выполненных шагов)
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
	// Финальное содержимое ответа ассистента
	var finalText string
	if len(resp.Choices) > 0 {
		finalText = resp.Choices[0].Message.Content
	}

	for _, out := range outs {
		id := strings.TrimSpace(out.ID)
		if id == "" {
			continue
		}
		t := strings.ToLower(strings.TrimSpace(out.Type))
		from := strings.ToLower(strings.TrimSpace(out.From))
		switch t {
		case "file":
			// Вычисляем путь
			p, err := out.Path.Execute(tctx)
			if err != nil {
				return fmt.Errorf("executor: failed to render outputs[%s].path: %w", id, err)
			}
			p = strings.TrimSpace(p)
			if p == "" {
				return fmt.Errorf("executor: outputs[%s].path evaluated to empty", id)
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return fmt.Errorf("executor: failed to create dir for output %s: %w", p, err)
			}
			var data []byte
			switch from {
			case "llm_text":
				data = []byte(strings.TrimSpace(finalText))
			case "llm_json":
				// Пишем весь ответ модели
				b, err := json.MarshalIndent(resp, "", "  ")
				if err != nil {
					return fmt.Errorf("executor: failed to marshal llm_json for output %s: %v", id, err)
				}
				data = b
			default:
				// Неподдержанный источник — пишем пусто, но не прерываем выполнение
				data = []byte(strings.TrimSpace(finalText))
			}
			if err := os.WriteFile(p, data, 0o644); err != nil {
				return fmt.Errorf("executor: failed to write output file %s: %w", p, err)
			}
			e.recordOutput(step.ID, id, ioValue{Path: p})
		case "log":
			// Пишем в лог содержимое по назначению
			switch from {
			case "llm_text":
				e.log.Info(strings.TrimSpace(finalText))
			case "llm_json":
				e.log.Info("step output (log json)", "step", step.ID, "id", id, "response", resp)
			default:
				e.log.Info("step output (log) unsupported from", "step", step.ID, "id", id, "from", out.From)
			}
		default:
			return fmt.Errorf("executor: unsupported output type: %s", out.Type)
		}
	}
	return nil
}

// processShellOutputs обрабатывает outputs шага type=shell, поддерживая источники command_stdout/command_stderr
func (e *Executor) processShellOutputs(step dsl.Step, stdout, stderr string, inputs map[string]ioValue, extra map[string]any) error {
	if len(step.Outputs) == 0 {
		return nil
	}
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
	for _, out := range step.Outputs {
		id := strings.TrimSpace(out.ID)
		if id == "" {
			continue
		}
		from := strings.ToLower(strings.TrimSpace(out.From))
		// Специальный режим: from=path — не пишет файл, а только регистрирует уже существующий путь.
		if from == "path" {
			p, err := out.Path.Execute(tctx)
			if err != nil {
				return fmt.Errorf("executor: failed to render outputs[%s].path: %w", id, err)
			}
			p = strings.TrimSpace(p)
			if p == "" {
				return fmt.Errorf("executor: outputs[%s].path evaluated to empty", id)
			}
			if _, err := os.Stat(p); err != nil {
				return fmt.Errorf("executor: outputs[%s].path does not exist: %v", id, err)
			}
			e.recordOutput(step.ID, id, ioValue{Path: p})
			continue
		}
		destType := strings.ToLower(strings.TrimSpace(out.Type))
		var data []byte
		switch from {
		case "command_stdout":
			data = []byte(stdout)
		case "command_stderr":
			data = []byte(stderr)
		default:
			// неизвестный источник — пропустим молча
			continue
		}
		switch destType {
		case "file":
			p, err := out.Path.Execute(tctx)
			if err != nil {
				return fmt.Errorf("executor: failed to render outputs[%s].path: %w", id, err)
			}
			p = strings.TrimSpace(p)
			if p == "" {
				return fmt.Errorf("executor: outputs[%s].path evaluated to empty", id)
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return fmt.Errorf("executor: failed to create dir for output %s: %w", p, err)
			}
			if err := os.WriteFile(p, data, 0o644); err != nil {
				return fmt.Errorf("executor: failed to write output file %s: %w", p, err)
			}
			e.recordOutput(step.ID, id, ioValue{Path: p})
		case "log":
			e.log.Info("step output (shell log)", "step", step.ID, "id", id, "from", from, "bytes", len(data))
		}
	}
	return nil
}

// inputsToTemplate конвертирует карту значений в вид, удобный для шаблонов: inputs.<id>.path/inline
func inputsToTemplate(in map[string]ioValue) map[string]map[string]string {
	out := make(map[string]map[string]string, len(in))
	for k, v := range in {
		out[k] = map[string]string{"path": v.Path, "inline": v.Inline}
	}
	return out
}
