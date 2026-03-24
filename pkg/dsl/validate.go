package dsl

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const dslVersion = 1

// Validate проверяет базовую корректность конфигурации.
func Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("dsl: configuration must not be nil")
	}

	var problems []string

	if cfg.Version != dslVersion {
		problems = append(problems, fmt.Sprintf("unsupported DSL version: %d", cfg.Version))
	}

	if strings.TrimSpace(cfg.Agent.Name) == "" {
		problems = append(problems, "agent.name is required")
	}
	if strings.TrimSpace(cfg.Agent.ArtifactDir) == "" {
		problems = append(problems, "agent.artifact_dir is required")
	}
	if cfg.Agent.ModelContextWindow != nil && *cfg.Agent.ModelContextWindow <= 0 {
		problems = append(problems, "agent.model_context_window must be > 0")
	}
	if cfg.Agent.ToolOutputWarnPercent != nil {
		if *cfg.Agent.ToolOutputWarnPercent <= 0 || *cfg.Agent.ToolOutputWarnPercent > 100 {
			problems = append(problems, "agent.tool_output_warn_percent must be within 1..100")
		}
	}
	if cfg.Agent.ToolOutputHardCapPercent != nil {
		if *cfg.Agent.ToolOutputHardCapPercent <= 0 || *cfg.Agent.ToolOutputHardCapPercent > 100 {
			problems = append(problems, "agent.tool_output_hard_cap_percent must be within 1..100")
		}
	}
	if cfg.Agent.AutoCompactPercent != nil {
		if *cfg.Agent.AutoCompactPercent <= 0 || *cfg.Agent.AutoCompactPercent > 100 {
			problems = append(problems, "agent.auto_compact_percent must be within 1..100")
		}
	}
	if cfg.Agent.CompactTargetPercent != nil {
		if *cfg.Agent.CompactTargetPercent <= 0 || *cfg.Agent.CompactTargetPercent > 100 {
			problems = append(problems, "agent.compact_target_percent must be within 1..100")
		}
	}
	if cfg.Agent.ResponseReserveTokens != nil && *cfg.Agent.ResponseReserveTokens <= 0 {
		problems = append(problems, "agent.response_reserve_tokens must be > 0")
	}
	if cfg.Agent.ToolOutputWarnPercent != nil && cfg.Agent.AutoCompactPercent != nil {
		if *cfg.Agent.ToolOutputWarnPercent >= *cfg.Agent.AutoCompactPercent {
			problems = append(problems, "agent.tool_output_warn_percent must be less than agent.auto_compact_percent")
		}
	}
	if cfg.Agent.ToolOutputWarnPercent != nil && cfg.Agent.ToolOutputHardCapPercent != nil {
		if *cfg.Agent.ToolOutputWarnPercent >= *cfg.Agent.ToolOutputHardCapPercent {
			problems = append(problems, "agent.tool_output_warn_percent must be less than agent.tool_output_hard_cap_percent")
		}
	}
	if cfg.Agent.AutoCompactPercent != nil && cfg.Agent.CompactTargetPercent != nil {
		if *cfg.Agent.CompactTargetPercent >= *cfg.Agent.AutoCompactPercent {
			problems = append(problems, "agent.compact_target_percent must be less than agent.auto_compact_percent")
		}
	}

	if len(cfg.Steps) == 0 {
		problems = append(problems, "no steps defined")
	}

	// Валидация пользовательских функций
	if err := validateFunctions(cfg.Functions); err != nil {
		problems = append(problems, err.Error())
	}

	seenIDs := make(map[string]struct{}, len(cfg.Steps))
	for idx, step := range cfg.Steps {
		path := fmt.Sprintf("steps[%d]", idx)
		if strings.TrimSpace(step.ID) == "" {
			problems = append(problems, fmt.Sprintf("%s: id is required", path))
		} else {
			if _, exists := seenIDs[step.ID]; exists {
				problems = append(problems, fmt.Sprintf("duplicate step id %q", step.ID))
			}
			seenIDs[step.ID] = struct{}{}
		}

		switch step.Type {
		case "llm":
			if step.LLM == nil {
				problems = append(problems, fmt.Sprintf("%s: failed to parse llm step configuration", path))
				continue
			}
			if step.LLM.UserPrompt.IsZero() && step.LLM.UserPromptPath.IsZero() {
				problems = append(problems, fmt.Sprintf("%s: user_prompt or user_prompt_path is required", path))
			}
			validateLLMPolicy(path, step.LLM, &problems)
		case "shell":
			runTpl, ok := bashRunTemplateForStep(step)
			if !ok {
				problems = append(problems, fmt.Sprintf("%s: failed to parse shell step configuration", path))
				continue
			}
			validateBashRunTemplate(path, "shell", runTpl, &problems)
		case "plan":
			validatePlanStep(path, step, &problems)
		case "matrix":
			if step.Matrix == nil {
				problems = append(problems, fmt.Sprintf("%s: failed to parse matrix step configuration", path))
				continue
			}
			if step.Matrix.FromYAML.IsZero() {
				problems = append(problems, fmt.Sprintf("%s.matrix.from_yaml is required", path))
			}
			if strings.TrimSpace(step.Matrix.Select) == "" {
				problems = append(problems, fmt.Sprintf("%s.matrix.select is required (e.g., 'items')", path))
			}
			if step.Matrix.ItemID.IsZero() {
				problems = append(problems, fmt.Sprintf("%s.matrix.item_id is required", path))
			}
			if step.Run == nil || strings.TrimSpace(step.Run.Step) == "" {
				problems = append(problems, fmt.Sprintf("%s.run.step is required", path))
			}
			// Валидация inject: ключи не пустые, значения — непустые шаблоны
			if step.Matrix.Inject != nil {
				for k, v := range step.Matrix.Inject {
					if strings.TrimSpace(k) == "" {
						problems = append(problems, fmt.Sprintf("%s.matrix.inject contains empty key", path))
					}
					if v.IsZero() {
						problems = append(problems, fmt.Sprintf("%s.matrix.inject[%s] must be non-empty", path, k))
					}
				}
			}
		default:
			problems = append(problems, fmt.Sprintf("%s: unsupported step type %q", path, step.Type))
		}

		// Валидация inputs
		if len(step.Inputs) > 0 {
			seenInputIDs := make(map[string]struct{}, len(step.Inputs))
			for inIdx, in := range step.Inputs {
				ipath := fmt.Sprintf("%s.inputs[%d]", path, inIdx)
				id := strings.TrimSpace(in.ID)
				if id == "" {
					problems = append(problems, fmt.Sprintf("%s.id is required", ipath))
				} else {
					if _, ok := seenInputIDs[id]; ok {
						problems = append(problems, fmt.Sprintf("%s: duplicate input id %q", ipath, id))
					}
					seenInputIDs[id] = struct{}{}
				}
				t := strings.ToLower(strings.TrimSpace(in.Type))
				switch t {
				case "file":
					if in.Path.IsZero() {
						problems = append(problems, fmt.Sprintf("%s.path is required for type=file", ipath))
					}
				case "inline":
					if in.Template.IsZero() {
						problems = append(problems, fmt.Sprintf("%s.template is required for type=inline", ipath))
					}
				default:
					problems = append(problems, fmt.Sprintf("%s.type must be one of [file, inline]", ipath))
				}
			}
		}

		// Проверка валидности approvers на уровне шага
		if err := validateApprovers(step.Approvers, path+".approvers"); err != nil {
			problems = append(problems, err.Error())
		}
	}

	// Дополнительная проверка для matrix: run.step должен ссылаться на существующий шаблонный шаг
	stepIndex := map[string]Step{}
	for _, s := range cfg.Steps {
		stepIndex[s.ID] = s
	}
	for i, s := range cfg.Steps {
		if strings.TrimSpace(s.Type) != "matrix" || s.Run == nil || strings.TrimSpace(s.Run.Step) == "" {
			continue
		}
		ref, ok := stepIndex[strings.TrimSpace(s.Run.Step)]
		if !ok {
			problems = append(problems, fmt.Sprintf("steps[%d].run.step references unknown step %q", i, s.Run.Step))
			continue
		}
		if !ref.Template {
			problems = append(problems, fmt.Sprintf("steps[%d].run.step=%q must be a template step (template: true)", i, s.Run.Step))
		}
		if ref.Type != "llm" && ref.Type != "shell" && ref.Type != "plan" {
			problems = append(problems, fmt.Sprintf("steps[%d].run.step=%q must be of type 'llm', 'shell' or 'plan'", i, s.Run.Step))
		}
	}

	// Проверка валидности approvers на уровне сценария
	if err := validateApprovers(cfg.Approvers, "approvers"); err != nil {
		problems = append(problems, err.Error())
	}

	if len(problems) == 0 {
		return nil
	}

	return fmt.Errorf("dsl: configuration errors:\n - %s", strings.Join(problems, "\n - "))
}

// validateBashRunTemplate проверяет общие правила для run-шаблонов shell/plan шагов.
func validateBashRunTemplate(path string, stepType string, run TemplateString, problems *[]string) {
	if run.IsZero() {
		*problems = append(*problems, fmt.Sprintf("%s: %s.run is required", path, stepType))
		return
	}
	// Скрипт уже исполняется через "bash -lc" раннером, поэтому вложенный вызов запрещён.
	runRaw := strings.TrimSpace(run.String())
	lower := strings.ToLower(runRaw)
	if strings.HasPrefix(lower, "bash ") || strings.Contains(lower, "bash -lc") {
		*problems = append(*problems, fmt.Sprintf("%s: %s.run must not include an explicit 'bash -lc' invocation", path, stepType))
	}
}

func validateLLMPolicy(path string, llm *StepLLM, problems *[]string) {
	if llm == nil {
		return
	}
	for _, item := range []struct {
		name  string
		value *int
	}{
		{name: "max_tokens", value: llm.MaxTokens},
		{name: "max_requests", value: llm.MaxRequests},
		{name: "max_tool_calls", value: llm.MaxToolCalls},
		{name: "max_cumulative_prompt_tokens", value: llm.MaxCumulativePromptTokens},
		{name: "max_cumulative_total_tokens", value: llm.MaxCumulativeTotalTokens},
		{name: "max_cumulative_tool_tokens", value: llm.MaxCumulativeToolTokens},
	} {
		if item.value != nil && *item.value <= 0 {
			*problems = append(*problems, fmt.Sprintf("%s.%s must be > 0", path, item.name))
		}
	}

	validator := strings.TrimSpace(llm.ResponseValidator)
	switch validator {
	case "", "review_file":
	default:
		*problems = append(*problems, fmt.Sprintf("%s.response_validator has unsupported value %q", path, llm.ResponseValidator))
	}
}

func validatePlanStep(path string, step Step, problems *[]string) {
	if step.Plan == nil {
		*problems = append(*problems, fmt.Sprintf("%s: failed to parse plan step configuration", path))
		return
	}

	engine := strings.ToLower(strings.TrimSpace(step.Plan.Engine))
	if engine == "" || engine == "shell" {
		validateBashRunTemplate(path, "plan", step.Plan.Run, problems)
		return
	}

	if engine != "partition" {
		*problems = append(*problems, fmt.Sprintf("%s: unsupported plan.engine %q (supported: shell, partition)", path, step.Plan.Engine))
		return
	}
	if !step.Plan.Run.IsZero() {
		*problems = append(*problems, fmt.Sprintf("%s: plan.run must be empty when plan.engine=partition", path))
	}

	p := step.Plan.Partition
	if p == nil {
		*problems = append(*problems, fmt.Sprintf("%s.plan.partition is required when plan.engine=partition", path))
		return
	}
	if p.SourcePath.IsZero() {
		*problems = append(*problems, fmt.Sprintf("%s.plan.partition.source_path is required", path))
	}
	if p.ManifestJSONPath.IsZero() {
		*problems = append(*problems, fmt.Sprintf("%s.plan.partition.manifest_json_path is required", path))
	}
	if p.ManifestYAMLPath.IsZero() {
		*problems = append(*problems, fmt.Sprintf("%s.plan.partition.manifest_yaml_path is required", path))
	}

	// Если включена материализация unit-ресурсов, базовые пути обязательны.
	if !p.UnitResourcesDir.IsZero() {
		if p.BasePromptPath.IsZero() {
			*problems = append(*problems, fmt.Sprintf("%s.plan.partition.base_prompt_path is required when unit_resources_dir is set", path))
		}
		if p.BaseRulesDir.IsZero() {
			*problems = append(*problems, fmt.Sprintf("%s.plan.partition.base_rules_dir is required when unit_resources_dir is set", path))
		}
	}

	validateOptionalNonNegativeIntTemplate(
		path+".plan.partition.switch_to_buckets_at",
		p.SwitchToBucketsAt,
		problems,
	)
	validateOptionalNonNegativeIntTemplate(
		path+".plan.partition.bucket_max_items",
		p.BucketMaxItems,
		problems,
	)
	validateOptionalNonNegativeIntTemplate(
		path+".plan.partition.bucket_max_weight",
		p.BucketMaxWeight,
		problems,
	)
	validateOptionalNonNegativeIntTemplate(
		path+".plan.partition.priority_weight",
		p.PriorityWeight,
		problems,
	)
}

func bashRunTemplateForStep(step Step) (TemplateString, bool) {
	switch step.Type {
	case "shell":
		if step.Shell == nil {
			return TemplateString{}, false
		}
		return step.Shell.Run, true
	default:
		return TemplateString{}, false
	}
}

func validateOptionalNonNegativeIntTemplate(path string, tpl TemplateString, problems *[]string) {
	if tpl.IsZero() {
		return
	}
	raw := strings.TrimSpace(tpl.String())
	if raw == "" {
		return
	}
	// Если значение вычисляется шаблоном во время выполнения, проверим его уже в рантайме.
	if strings.Contains(raw, "{{") {
		return
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("%s must be a non-negative integer", path))
		return
	}
	if n < 0 {
		*problems = append(*problems, fmt.Sprintf("%s must be >= 0", path))
	}
}

// validateApprovers валидирует массив approvers для инструмента.
func validateApprovers(approvers []Approver, path string) error {
	var errs []string
	for i, ap := range approvers {
		apath := fmt.Sprintf("%s[%d]", path, i)
		tool := strings.TrimSpace(ap.Tool)
		if tool == "" {
			errs = append(errs, fmt.Sprintf("%s.tool is required", apath))
			continue
		}

		// Разбор rules: ожидаем срез карт
		if ap.Rules == nil {
			// Нет правил — значит разрешено всё
			continue
		}
		rulesSlice, ok := ap.Rules.([]any)
		if !ok {
			errs = append(errs, fmt.Sprintf("%s.rules must be an array", apath))
			continue
		}
		if len(rulesSlice) == 0 {
			// Пустой массив — разрешено всё
			continue
		}

		switch tool {
		case "shell":
			for j, r := range rulesSlice {
				m, ok := r.(map[string]any)
				if !ok {
					errs = append(errs, fmt.Sprintf("%s.rules[%d] must be an object", apath, j))
					continue
				}
				rxv, ok := m["regex"]
				if !ok {
					errs = append(errs, fmt.Sprintf("%s.rules[%d].regex is required", apath, j))
					continue
				}
				msgv, ok := m["message"]
				if !ok {
					errs = append(errs, fmt.Sprintf("%s.rules[%d].message is required", apath, j))
					continue
				}
				rxStr := strings.TrimSpace(fmt.Sprint(rxv))
				if rxStr == "" {
					errs = append(errs, fmt.Sprintf("%s.rules[%d].regex must be non-empty", apath, j))
					continue
				}
				// Проверяем компиляцию регулярного выражения
				if _, err := regexp.Compile(rxStr); err != nil {
					errs = append(errs, fmt.Sprintf("%s.rules[%d].regex compile error: %v", apath, j, err))
				}
				msgStr := strings.TrimSpace(fmt.Sprint(msgv))
				if msgStr == "" {
					errs = append(errs, fmt.Sprintf("%s.rules[%d].message must be non-empty", apath, j))
				}
			}
		case "apply_patch":
			for j, r := range rulesSlice {
				m, ok := r.(map[string]any)
				if !ok {
					errs = append(errs, fmt.Sprintf("%s.rules[%d] must be an object", apath, j))
					continue
				}
				gpv, ok := m["glob_patterns"]
				if !ok {
					errs = append(errs, fmt.Sprintf("%s.rules[%d].glob_patterns is required", apath, j))
					continue
				}
				// Должен быть массив строк
				gpSlice, ok := gpv.([]any)
				if !ok {
					errs = append(errs, fmt.Sprintf("%s.rules[%d].glob_patterns must be an array", apath, j))
					continue
				}
				if len(gpSlice) == 0 {
					errs = append(errs, fmt.Sprintf("%s.rules[%d].glob_patterns must not be empty", apath, j))
				}
				var hasAny bool
				if v, ok := m["allow_create"]; ok {
					_ = v
					hasAny = true
				}
				if v, ok := m["allow_update"]; ok {
					_ = v
					hasAny = true
				}
				if v, ok := m["allow_delete"]; ok {
					_ = v
					hasAny = true
				}
				if !hasAny {
					errs = append(errs, fmt.Sprintf("%s.rules[%d] must specify at least one of allow_create/allow_update/allow_delete", apath, j))
				}
			}
		default:
			errs = append(errs, fmt.Sprintf("%s.tool must be one of [shell, apply_patch]", apath))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("dsl: configuration errors in approvers:\n - %s", strings.Join(errs, "\n - "))
	}
	return nil
}

// validateFunctions проверяет корректность пользовательских функций в конфигурации DSL.
func validateFunctions(functions []Function) error {
	var errs []string
	if len(functions) == 0 {
		return nil
	}
	reserved := map[string]struct{}{"shell": {}, "apply_patch": {}}
	seen := map[string]struct{}{}
	for i, f := range functions {
		p := fmt.Sprintf("functions[%d]", i)
		name := strings.TrimSpace(f.Name)
		if name == "" {
			errs = append(errs, fmt.Sprintf("%s.name is required", p))
		} else {
			if _, ok := reserved[name]; ok {
				errs = append(errs, fmt.Sprintf("%s.name conflicts with a built-in tool: %q", p, name))
			}
			if _, ok := seen[name]; ok {
				errs = append(errs, fmt.Sprintf("duplicate function name %q", name))
			}
			seen[name] = struct{}{}
		}
		if strings.TrimSpace(f.Description) == "" {
			errs = append(errs, fmt.Sprintf("%s.description is required and must be non-empty", p))
		}
		// Implementation должен быть: shell с непустым шаблоном
		implType := strings.ToLower(strings.TrimSpace(f.Implementation.Type))
		if implType != "shell" {
			errs = append(errs, fmt.Sprintf("%s.implementation.type must be 'shell'", p))
		}
		if f.Implementation.ShellTemplate.IsZero() {
			errs = append(errs, fmt.Sprintf("%s.implementation.shell_template is required", p))
		}
		// Prompt должен быть обязательным и непустым
		if f.Prompt.IsZero() {
			errs = append(errs, fmt.Sprintf("%s.prompt is required and must be non-empty", p))
		}
		// Parameters должны быть JSONSchema объектом с картой properties и массивом required
		if f.Parameters == nil {
			errs = append(errs, fmt.Sprintf("%s.parameters is required", p))
			continue
		}
		// type == object
		if tv, ok := f.Parameters["type"]; !ok || strings.ToLower(strings.TrimSpace(fmt.Sprint(tv))) != "object" {
			errs = append(errs, fmt.Sprintf("%s.parameters.type must be 'object'", p))
		}
		// properties должен быть объектом
		if pv, ok := f.Parameters["properties"]; !ok {
			errs = append(errs, fmt.Sprintf("%s.parameters.properties is required", p))
		} else {
			if _, ok := pv.(map[string]any); !ok {
				errs = append(errs, fmt.Sprintf("%s.parameters.properties must be an object", p))
			}
		}
		// required должен быть массивом; если не указан, по умолчанию считаем пустым массивом
		if rv, ok := f.Parameters["required"]; !ok {
			// Для функций без параметров и/или без обязательных полей
			f.Parameters["required"] = []any{}
		} else {
			if _, ok := rv.([]any); !ok {
				errs = append(errs, fmt.Sprintf("%s.parameters.required must be an array", p))
			}
		}
		// additional_properties должен быть false, чтобы не допустить произвольные параметры (требование API OpenAI)
		if apv, ok := f.Parameters["additional_properties"]; ok {
			switch b := apv.(type) {
			case bool:
				if b {
					errs = append(errs, fmt.Sprintf("%s.parameters.additional_properties must be false", p))
				}
			default:
				errs = append(errs, fmt.Sprintf("%s.parameters.additional_properties must be a boolean false", p))
			}
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("dsl: configuration errors in functions:\n - %s", strings.Join(errs, "\n - "))
}
