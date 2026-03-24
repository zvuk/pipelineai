package executor

import (
	"github.com/zvuk/pipelineai/pkg/dsl"
)

// templateAgent подготавливает карту с YAML-подобными ключами для использования в Go-шаблонах (snake_case).
func (e *Executor) templateAgent() map[string]any {
	a := e.cfg.Agent
	return map[string]any{
		"name":         a.Name,
		"model":        a.Model,
		"artifact_dir": a.ArtifactDir,
		"openai": map[string]any{
			"base_url":    a.OpenAI.BaseURL,
			"api_key_env": a.OpenAI.APIKeyEnv,
		},
		"reasoning":                    a.Reasoning,
		"model_context_window":         a.ModelContextWindow,
		"tool_output_warn_percent":     a.ToolOutputWarnPercent,
		"tool_output_hard_cap_percent": a.ToolOutputHardCapPercent,
		"auto_compact_percent":         a.AutoCompactPercent,
		"compact_target_percent":       a.CompactTargetPercent,
		"response_reserve_tokens":      a.ResponseReserveTokens,
		"tokenizer_cache_dir":          a.TokenizerCacheDir,
	}
}

// templateDefaults формирует карту defaults с ключами, совпадающими с YAML.
func (e *Executor) templateDefaults() map[string]any {
	if e.cfg.Defaults == nil {
		return nil
	}
	d := e.cfg.Defaults
	out := map[string]any{}
	if d.StepTimeout != nil {
		out["step_timeout"] = d.StepTimeout.Duration
	}
	if d.ScenarioTimeout != nil {
		out["scenario_timeout"] = d.ScenarioTimeout.Duration
	}
	if d.Env != nil {
		out["env"] = d.Env
	}
	if d.ToolTimeout != nil {
		out["tool_timeout"] = d.ToolTimeout.Duration
	}
	return out
}

// templateStep возвращает шаг как есть (структура).
func templateStep(s dsl.Step) any { return s }
