package executor

import (
	"strings"

	"github.com/zvuk/pipelineai/pkg/dsl"
)

// renderStepMeta рендерит name/description для шага, игнорируя ошибки рендера (возвращает trimmed текст).
func (e *Executor) renderStepMeta(step dsl.Step) (name, description string) {
	// Контекст без inputs: agent/step/defaults/outputs достаточен для заголовков
	tctx := map[string]any{
		"agent":    e.templateAgent(),
		"step":     templateStep(step),
		"defaults": e.templateDefaults(),
		"outputs":  e.outputsContext(),
	}
	if !step.Name.IsZero() {
		if v, err := step.Name.Execute(tctx); err == nil {
			name = strings.TrimSpace(v)
		}
	}
	if !step.Description.IsZero() {
		if v, err := step.Description.Execute(tctx); err == nil {
			description = strings.TrimSpace(v)
		}
	}
	return name, description
}
