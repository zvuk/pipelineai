package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zvuk/pipelineai/internal/runtime/llm"
	"github.com/zvuk/pipelineai/internal/tools"
	ap "github.com/zvuk/pipelineai/internal/tools/applypatch"
	"github.com/zvuk/pipelineai/internal/tools/approval"
	sh "github.com/zvuk/pipelineai/internal/tools/shell"
	"github.com/zvuk/pipelineai/internal/tools/specs"
	"github.com/zvuk/pipelineai/pkg/dsl"
)

// ExecResult — унифицированный результат выполнения инструмента для возврата модели.
type ExecResult struct {
	Tool string `json:"tool"`
	Ok   bool   `json:"ok"`
	// Всегда включаем stdout/stderr/exit_code, даже если пустые/0 — детерминизм для модели
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
	ExitCode int      `json:"exit_code"`
	Summary  string   `json:"summary,omitempty"`
	Added    []string `json:"added,omitempty"`
	Modified []string `json:"modified,omitempty"`
	Deleted  []string `json:"deleted,omitempty"`
	// Длительность в мс отдельным числом
	ElapsedMs  int64  `json:"elapsed_ms"`
	NewWorkdir string `json:"new_workdir,omitempty"`
	// ToolError — человекочитаемое сообщение об ошибке
	ToolError string `json:"tool_error,omitempty"`
	Warning   string `json:"warning,omitempty"`
	// Suppressed означает, что полный результат не был возвращён модели из-за большого объёма.
	Suppressed bool `json:"suppressed,omitempty"`
	// EstimatedTokens — локальная оценка размера suppress'ed payload в токенах.
	EstimatedTokens int `json:"estimated_tokens,omitempty"`
	// ThresholdTokens — порог suppress/warn в токенах.
	ThresholdTokens int    `json:"threshold_tokens,omitempty"`
	Preview         string `json:"preview,omitempty"`
	// HardSuppressed означает, что даже force-повтор не вернёт полный payload в контекст.
	HardSuppressed bool `json:"hard_suppressed,omitempty"`
	// ArtifactPath указывает путь к сохранённому полному payload инструмента.
	ArtifactPath string `json:"artifact_path,omitempty"`
}

// Registry — реестр встроенных инструментов и пользовательских функций.
type Registry struct {
	// Пользовательские функции по имени
	functions map[string]dsl.Function
	// agent — упрощённое представление agent-конфига для шаблонов функций (name, model, artifact_dir, openai, reasoning).
	agent map[string]any
}

// New создаёт реестр по конфигурации DSL.
func New(cfg *dsl.Config) *Registry {
	funcs := make(map[string]dsl.Function)
	for _, f := range cfg.Functions {
		name := strings.TrimSpace(f.Name)
		if name == "" {
			continue
		}
		funcs[name] = f
	}

	// Подготовим контекст agent для шаблонов функций (аналогично executor.templateAgent).
	a := cfg.Agent
	agentCtx := map[string]any{
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

	return &Registry{
		functions: funcs,
		agent:     agentCtx,
	}
}

// ToolsForAllowed возвращает JSON‑спеки функций/инструментов, разрешённых на шаге.
func (r *Registry) ToolsForAllowed(allowed []string) []llm.Tool {
	if len(allowed) == 0 {
		return nil
	}
	tools := make([]llm.Tool, 0, len(allowed))
	for _, name := range allowed {
		n := strings.TrimSpace(name)
		switch n {
		case "shell":
			tools = append(tools, specs.ShellToolSpec())
		case "apply_patch":
			tools = append(tools, specs.ApplyPatchToolSpecJSON())
		default:
			if f, ok := r.functions[n]; ok {
				tools = append(tools, llm.Tool{
					Type: "function",
					Function: llm.ToolFunctionSpec{
						Name:        f.Name,
						Description: f.Description,
						Parameters:  withForceFullOutputParam(f.Parameters),
					},
				})
			}
		}
	}
	return tools
}

// isAllowed проверяет, разрешен ли инструмент на шаге.
func isAllowed(name string, allowed []string) bool {
	if len(allowed) == 0 {
		return false
	}
	for _, a := range allowed {
		if strings.TrimSpace(a) == name {
			return true
		}
	}
	return false
}

// ExecCall выполняет один вызов инструмента/функции.
// - Учитывает фильтр allowed
// - Встроенные инструменты: shell, apply_patch
// - Пользовательские функции: implementation: shell (templated)
func (r *Registry) ExecCall(
	ctx context.Context,
	tc llm.ToolCall,
	allowed []string,
	currentWorkdir string,
	defaultTimeout time.Duration,
	shellAppr *approval.ShellApprover,
	applyAppr *approval.ApplyPatchApprover,
) ExecResult {
	name := strings.TrimSpace(tc.Function.Name)
	argsJSON := strings.TrimSpace(tc.Function.Arguments)

	if !isAllowed(name, allowed) {
		return ExecResult{Tool: name, Ok: false, ToolError: "tool is not allowed for this step"}
	}

	switch name {
	case "shell":
		// Разбираем параметры инструмента shell
		var payload struct {
			Command   []string `json:"command"`
			Workdir   string   `json:"workdir"`
			TimeoutMs int64    `json:"timeout_ms"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &payload); err != nil {
			return ExecResult{Tool: name, Ok: false, ToolError: "invalid shell arguments JSON"}
		}
		// Поддержим альтернативный ключ cmd/command
		if len(payload.Command) == 0 {
			var alt struct {
				Cmd       []string `json:"cmd"`
				Command   []string `json:"command"`
				Workdir   string   `json:"workdir"`
				TimeoutMs int64    `json:"timeout_ms"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &alt); err == nil {
				if len(alt.Command) > 0 {
					payload.Command = alt.Command
				} else if len(alt.Cmd) > 0 {
					payload.Command = alt.Cmd
				}
				if payload.Workdir == "" {
					payload.Workdir = alt.Workdir
				}
				if payload.TimeoutMs == 0 {
					payload.TimeoutMs = alt.TimeoutMs
				}
			}
		}
		// Поддержим строковый формат: {"command": "find . -type f | head"} и {"command": "bash -lc \"...\""}
		if len(payload.Command) == 0 {
			var gm map[string]any
			if err := json.Unmarshal([]byte(argsJSON), &gm); err == nil {
				getStr := func(k string) string {
					if v, ok := gm[k]; ok {
						if s, ok := v.(string); ok {
							return s
						}
					}
					return ""
				}
				cmdStr := getStr("command")
				if cmdStr == "" {
					cmdStr = getStr("cmd")
				}
				if strings.TrimSpace(cmdStr) != "" {
					ts := strings.TrimSpace(cmdStr)
					low := strings.ToLower(ts)
					if strings.HasPrefix(low, "bash -lc ") {
						script := strings.TrimSpace(ts[len("bash -lc "):])
						script = strings.Trim(script, "\"'")
						payload.Command = []string{"bash", "-lc", script}
					} else {
						payload.Command = []string{"bash", "-lc", ts}
					}
				}
				if payload.Workdir == "" {
					payload.Workdir = getStr("workdir")
				}
				if payload.TimeoutMs == 0 {
					if v, ok := gm["timeout_ms"]; ok {
						switch tv := v.(type) {
						case float64:
							payload.TimeoutMs = int64(tv)
						case int64:
							payload.TimeoutMs = tv
						case string:
							if n, err := strconv.ParseInt(strings.TrimSpace(tv), 10, 64); err == nil {
								payload.TimeoutMs = n
							}
						}
					}
				}
			}
		}

		// Запрет: модель не должна вызывать apply_patch через shell или bash-скрипты
		// Возвращаем понятную ошибку для модели.
		const apErr = "apply_patch is not available in shell. Call the apply_patch tool directly as a tool. If you need to mention it in text, wrap it in backticks like `apply_patch`."
		if len(payload.Command) > 0 {
			// Прямой вызов утилиты
			if strings.EqualFold(strings.TrimSpace(payload.Command[0]), "apply_patch") {
				return ExecResult{Tool: name, Ok: false, ToolError: apErr}
			}
			// Скрипт bash -lc — блокируем только реальный вызов, а не упоминания
			if len(payload.Command) >= 3 && strings.EqualFold(strings.TrimSpace(payload.Command[0]), "bash") && strings.TrimSpace(payload.Command[1]) == "-lc" {
				script := payload.Command[2]
				if tools.ContainsApplyPatchInvocation(script) {
					return ExecResult{Tool: name, Ok: false, ToolError: apErr}
				}
			}
		}
		to := defaultTimeout
		if payload.TimeoutMs > 0 {
			to = time.Duration(payload.TimeoutMs) * time.Millisecond
		}
		if payload.Workdir == "" {
			payload.Workdir = currentWorkdir
		}
		res, err := sh.Exec(ctx, sh.Args{Command: payload.Command, Workdir: payload.Workdir, Timeout: to}, shellAppr)
		if res.Blocked {
			return ExecResult{Tool: name, Ok: false, Stdout: res.Stdout, Stderr: res.Stderr, ExitCode: res.ExitCode, ElapsedMs: res.Elapsed.Milliseconds(), NewWorkdir: res.NewWorkdir, ToolError: res.Message}
		}
		if err != nil {
			return ExecResult{Tool: name, Ok: false, Stdout: res.Stdout, Stderr: res.Stderr, ExitCode: res.ExitCode, ElapsedMs: res.Elapsed.Milliseconds(), NewWorkdir: res.NewWorkdir, ToolError: err.Error()}
		}
		return ExecResult{Tool: name, Ok: true, Stdout: res.Stdout, Stderr: res.Stderr, ExitCode: res.ExitCode, ElapsedMs: res.Elapsed.Milliseconds(), NewWorkdir: res.NewWorkdir}

	case "apply_patch":
		var payload struct {
			Input   string `json:"input"`
			Patch   string `json:"patch"`
			Workdir string `json:"workdir,omitempty"`
			DryRun  bool   `json:"dry_run,omitempty"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &payload); err != nil {
			// Альтернативный формат: {"command":["apply_patch","<patch>"]}
			var alt struct {
				Command []string `json:"command"`
			}
			if err2 := json.Unmarshal([]byte(argsJSON), &alt); err2 == nil && len(alt.Command) >= 2 {
				payload.Input = alt.Command[1]
			} else {
				return ExecResult{Tool: name, Ok: false, ToolError: "invalid apply_patch arguments JSON"}
			}
		}
		// алиас
		if strings.TrimSpace(payload.Input) == "" && strings.TrimSpace(payload.Patch) != "" {
			payload.Input = payload.Patch
		}
		if payload.Workdir == "" {
			payload.Workdir = currentWorkdir
		}
		// Нормализуем лидирующие пустые строки, если модель добавила перевод строки перед заголовком
		patch := strings.TrimLeft(payload.Input, "\r\n\t ")
		res, err := ap.Exec(ap.Args{Patch: patch, Workdir: payload.Workdir, DryRun: payload.DryRun}, applyAppr)
		if err != nil {
			return ExecResult{Tool: name, Ok: false, Summary: res.Summary, Added: res.Added, Modified: res.Modified, Deleted: res.Deleted, ElapsedMs: res.Elapsed.Milliseconds(), ToolError: err.Error()}
		}
		return ExecResult{Tool: name, Ok: true, Summary: res.Summary, Added: res.Added, Modified: res.Modified, Deleted: res.Deleted, ElapsedMs: res.Elapsed.Milliseconds()}
	default:
		// Пользовательская функция
		f, ok := r.functions[name]
		if !ok {
			return ExecResult{Tool: name, Ok: false, ToolError: "unknown tool"}
		}
		// Поддерживаем только implementation.type == shell
		if strings.TrimSpace(strings.ToLower(f.Implementation.Type)) != "shell" {
			return ExecResult{Tool: name, Ok: false, ToolError: fmt.Sprintf("unsupported implementation type: %s", f.Implementation.Type)}
		}
		// Разберём параметры функции в map
		var params map[string]any
		if argsJSON != "" {
			if err := json.Unmarshal([]byte(argsJSON), &params); err != nil {
				return ExecResult{Tool: name, Ok: false, ToolError: "invalid function arguments JSON"}
			}
		} else {
			params = map[string]any{}
		}

		// Нормализуем file:// URL до локального пути (для удобства моделей)
		if raw, ok := params["url"].(string); ok {
			u := strings.TrimSpace(raw)
			if strings.HasPrefix(strings.ToLower(u), "file://") {
				// file:///abs/path -> /abs/path; file://relative -> relative
				if strings.HasPrefix(u, "file:///") {
					u = "/" + strings.TrimPrefix(u, "file:///")
				} else {
					u = strings.TrimPrefix(u, "file://")
				}
				params["url"] = u
			}
		}
		// Простейшая валидация required из JSON Schema (если задано)
		if parametersHasRequired(f) {
			for _, key := range requiredKeysFromFunction(f) {
				if _, ok := params[key]; !ok {
					return ExecResult{Tool: name, Ok: false, ToolError: fmt.Sprintf("missing required parameter: %s", key)}
				}
			}
		}

		// Рендерим bash-скрипт по шаблону shell_template (missingkey=default)
		raw := f.Implementation.ShellTemplate.String()
		script, err := dsl.RenderStringWithDefaults(raw, map[string]any{
			"params": params,
			"agent":  r.agent,
		})
		if err != nil {
			return ExecResult{Tool: name, Ok: false, ToolError: fmt.Sprintf("failed to render shell template: %v", err)}
		}
		// Запускаем через встроенный shell
		res, err := sh.Exec(ctx, sh.Args{Command: []string{"bash", "-lc", script}, Workdir: currentWorkdir, Timeout: defaultTimeout}, shellAppr)
		if res.Blocked {
			return ExecResult{Tool: name, Ok: false, Stdout: res.Stdout, Stderr: res.Stderr, ExitCode: res.ExitCode, ElapsedMs: res.Elapsed.Milliseconds(), NewWorkdir: res.NewWorkdir, ToolError: res.Message}
		}
		if err != nil {
			return ExecResult{Tool: name, Ok: false, Stdout: res.Stdout, Stderr: res.Stderr, ExitCode: res.ExitCode, ElapsedMs: res.Elapsed.Milliseconds(), NewWorkdir: res.NewWorkdir, ToolError: err.Error()}
		}
		return ExecResult{Tool: name, Ok: true, Stdout: res.Stdout, Stderr: res.Stderr, ExitCode: res.ExitCode, ElapsedMs: res.Elapsed.Milliseconds(), NewWorkdir: res.NewWorkdir}
	}
}

func requiredKeysFromFunction(f dsl.Function) []string {
	var keys []string
	if f.Parameters == nil {
		return keys
	}
	if rv, ok := f.Parameters["required"]; ok {
		if arr, ok := rv.([]any); ok {
			for _, it := range arr {
				key := strings.TrimSpace(fmt.Sprint(it))
				if key != "" {
					keys = append(keys, key)
				}
			}
		}
	}
	return keys
}

func parametersHasRequired(f dsl.Function) bool { _, ok := f.Parameters["required"]; return ok }

func withForceFullOutputParam(params map[string]any) map[string]any {
	if params == nil {
		return nil
	}
	out := make(map[string]any, len(params))
	for k, v := range params {
		out[k] = v
	}

	props, ok := out["properties"].(map[string]any)
	if !ok {
		return out
	}
	if _, exists := props["force_full_output"]; exists {
		return out
	}

	propsCopy := make(map[string]any, len(props)+1)
	for k, v := range props {
		propsCopy[k] = v
	}
	propsCopy["force_full_output"] = map[string]any{
		"type":        "boolean",
		"description": "When true, return the full tool output even if it is large.",
	}
	out["properties"] = propsCopy
	return out
}
