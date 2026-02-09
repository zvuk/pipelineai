package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zvuk/pipelineai/internal/runtime/llm"
	"github.com/zvuk/pipelineai/internal/tools/approval"
	"github.com/zvuk/pipelineai/internal/tools/prompts"
	"github.com/zvuk/pipelineai/pkg/dsl"
)

// RunLLMStep выполняет шаг type=llm и сохраняет ответ модели.
func (e *Executor) RunLLMStep(ctx context.Context, stepID string, extra map[string]any) (llm.ChatCompletionResponse, string, error) {
	step, ok := e.getStep(stepID)
	if !ok {
		return llm.ChatCompletionResponse{}, "", fmt.Errorf("executor: step %s not found", stepID)
	}
	if step.Type != "llm" || step.LLM == nil {
		return llm.ChatCompletionResponse{}, "", fmt.Errorf("executor: step %s is not of type llm", stepID)
	}

	// Сначала вычисляем inputs для шага
	inputs, err := e.renderInputs(step, extra)
	if err != nil {
		return llm.ChatCompletionResponse{}, "", err
	}
	// Рендерим промпты и начальный запрос (с учётом inputs/outputs)
	systemPrompt, userPrompt, req, err := e.buildPromptsAndRequest(step, inputs, extra)
	if err != nil {
		return llm.ChatCompletionResponse{}, "", err
	}

	// В режиме DEBUG — сохранить текущую историю сообщений в log/<step_id>.json
	e.updateChatLogIfDebug(ctx, step.ID, req.Messages)

	// DEBUG: при старте шага — инпуты и промпты (обрезанные)
	{
		var inParts []string
		for id, v := range inputs {
			if strings.TrimSpace(v.Path) != "" {
				inParts = append(inParts, fmt.Sprintf("%s=file:%s", id, v.Path))
			} else if strings.TrimSpace(v.Inline) != "" {
				inParts = append(inParts, fmt.Sprintf("%s=inline:%s", id, crop(v.Inline, 150)))
			}
		}
		inputsSummary := strings.Join(inParts, "; ")
		e.log.DebugContext(ctx, "llm step inputs/prompts",
			slog.String("step", stepID),
			slog.String("inputs", crop(inputsSummary, 600)),
			slog.String("system", crop(systemPrompt, 150)),
			slog.String("user", crop(userPrompt, 150)),
		)
	}

	// Подключаем JSON‑схемы инструментов в запрос
	req = e.attachToolSchemas(req, step)

	if step.LLM.Temperature != nil {
		temp := *step.LLM.Temperature
		req.Temperature = &temp
	}

	e.log.InfoContext(ctx, "run llm step", slog.String("step", stepID), slog.Int("messages", len(req.Messages)))

	// Основной цикл: выполняем запросы к модели, пока она возвращает tool/function calls
	// Построим approvers для шага
	shellAppr, applyAppr, err := approval.BuildEffectiveApprovers(e.cfg, &step)
	if err != nil {
		return llm.ChatCompletionResponse{}, "", err
	}

	// Вычислим таймаут инструментов: defaults -> step override
	toolTimeout := e.resolveToolTimeout(&step)
	// ---

	resp, finalMessages, err := e.runAgentLoop(ctx, req, &step, toolTimeout, shellAppr, applyAppr)
	if err != nil {
		return llm.ChatCompletionResponse{}, "", err
	}

	// Собираем запись артефакта и сохраняем весь конечный диалог messages
	record, _ := buildLLMArtifactRecord(systemPrompt, userPrompt, resp, finalMessages)
	path, err := e.artifacts.WriteLLMResponse(stepID, record)
	if err != nil {
		return llm.ChatCompletionResponse{}, "", err
	}

	// Обрабатываем outputs шага (file/log) и регистрируем их для последующих шагов
	if err := e.processLLMOutputs(step, resp, inputs, extra); err != nil {
		return llm.ChatCompletionResponse{}, "", err
	}

	// DEBUG: при конце шага — аутпуты и краткий ответ
	finalText := ""
	if len(resp.Choices) > 0 {
		finalText = strings.TrimSpace(resp.Choices[0].Message.Content)
	}
	e.log.DebugContext(ctx, "llm step outputs",
		slog.String("step", stepID),
		slog.String("answer", crop(finalText, 150)),
	)

	return resp, path, nil
}

// runAgentLoop управляет итерациями диалога с моделью до финального ответа без tool calls.
func (e *Executor) runAgentLoop(ctx context.Context, req llm.ChatCompletionRequest, step *dsl.Step, toolTimeout time.Duration, shellAppr *approval.ShellApprover, applyAppr *approval.ApplyPatchApprover) (llm.ChatCompletionResponse, []llm.Message, error) {
	var last llm.ChatCompletionResponse
	// Защита от зацикливания: храним последние 3 сигнатуры вызова инструмента
	lastToolSigs := make([]string, 0, 3)
	// Текущая рабочая директория для инструментов (обновляется shell.cd)
	currentWorkdir := ""
	for {
		// Перед каждым запросом — обновим историю диалога в DEBUG
		e.updateChatLogIfDebug(ctx, step.ID, req.Messages)
		// Таймаут/отмена сценария
		select {
		case <-ctx.Done():
			return last, req.Messages, fmt.Errorf("executor: step timed out: %w", ctx.Err())
		default:
		}

		resp, err := e.client.CreateChatCompletion(ctx, req)
		if err != nil {
			return last, req.Messages, err
		}
		last = resp
		if len(resp.Choices) == 0 {
			return resp, req.Messages, nil
		}
		choice := resp.Choices[0]
		// INFO: ответ модели (обрезанный)
		if strings.TrimSpace(choice.Message.Content) != "" {
			e.log.InfoContext(ctx, "model answer", slog.String("step", step.ID), slog.String("content", crop(choice.Message.Content, 90)))
		}
		switch {
		case len(choice.Message.ToolCalls) > 0:
			// Защита от зацикливания: сравниваем последние 3 сигнатуры tool_calls
			sig := buildToolCallSignature(choice.Message)
			if sig != "" {
				lastToolSigs = append(lastToolSigs, sig)
				if len(lastToolSigs) > 3 {
					lastToolSigs = lastToolSigs[len(lastToolSigs)-3:]
				}
				if len(lastToolSigs) == 3 && lastToolSigs[0] == lastToolSigs[1] && lastToolSigs[1] == lastToolSigs[2] {
					// Зафиксируем сообщение ассистента и вернём предупреждение инструментом
					req.Messages = append(req.Messages, llm.Message{Role: llm.RoleAssistant, Content: choice.Message.Content, ToolCalls: choice.Message.ToolCalls})
					tc := choice.Message.ToolCalls[0]
					warning := "Tool execution declined: you appear to be looping. Change strategy and continue."
					req.Messages = append(req.Messages, llm.Message{Role: llm.RoleTool, Content: warning, ToolCallID: tc.ID})
					e.log.WarnContext(ctx, "loop detected: declining tool execution")
					continue
				}
			}
			// Выполняем строго один tool_call за итерацию, чтобы история была:
			// assistant(tool_calls[1]) -> tool -> (следующая итерация)
			tc := choice.Message.ToolCalls[0]
			// Сохраняем сообщение ассистента, но с одиночным вызовом
			req.Messages = append(req.Messages, llm.Message{Role: llm.RoleAssistant, Content: choice.Message.Content, ToolCalls: []llm.ToolCall{tc}})
			// INFO: вызов инструмента (имя, параметры обрезанные)
			argsShort := crop(strings.TrimSpace(tc.Function.Arguments), 90)
			e.log.InfoContext(ctx, "tool call", slog.String("step", step.ID), slog.String("tool", tc.Function.Name), slog.String("args", argsShort))
			out := e.tools.ExecCall(ctx, tc, step.LLM.ToolsAllowed, currentWorkdir, toolTimeout, shellAppr, applyAppr)
			if out.NewWorkdir != "" {
				currentWorkdir = out.NewWorkdir
			}
			// Ответ инструмента
			payload, _ := json.Marshal(out)
			req.Messages = append(req.Messages, llm.Message{Role: llm.RoleTool, Content: string(payload), ToolCallID: tc.ID})
			e.logToolExecution(ctx, step.ID, tc.Function.Name, tc.Function.Arguments, out.Ok, out.ToolError, out.ExitCode, out.Stderr, out.Stdout)
			// После одного инструмента — возвращаемся в начало цикла для нового запроса
			continue
		case choice.Message.FunctionCall != nil:
			// Поддержка legacy function_call в Chat Completions — выполняем один вызов
			sig := choice.Message.FunctionCall.Name + ":" + strings.TrimSpace(choice.Message.FunctionCall.Arguments)
			if sig != "" {
				lastToolSigs = append(lastToolSigs, sig)
				if len(lastToolSigs) > 3 {
					lastToolSigs = lastToolSigs[len(lastToolSigs)-3:]
				}
				if len(lastToolSigs) == 3 && lastToolSigs[0] == lastToolSigs[1] && lastToolSigs[1] == lastToolSigs[2] {
					req.Messages = append(req.Messages, llm.Message{Role: llm.RoleAssistant, Content: choice.Message.Content, FunctionCall: choice.Message.FunctionCall})
					warning := "Tool execution declined: you appear to be looping. Change strategy and continue."
					req.Messages = append(req.Messages, llm.Message{Role: llm.RoleTool, Content: warning, ToolCallID: "fc_0"})
					e.log.WarnContext(ctx, "loop detected: declining legacy function_call execution")
					continue
				}
			}
			req.Messages = append(req.Messages, llm.Message{Role: llm.RoleAssistant, Content: choice.Message.Content, FunctionCall: choice.Message.FunctionCall})
			// Выполним вызов
			tc := llm.ToolCall{ID: "fc_0", Type: "function", Function: *choice.Message.FunctionCall}
			argsShort := crop(strings.TrimSpace(tc.Function.Arguments), 90)
			e.log.InfoContext(ctx, "tool call", slog.String("step", step.ID), slog.String("tool", tc.Function.Name), slog.String("args", argsShort))
			out := e.tools.ExecCall(ctx, tc, step.LLM.ToolsAllowed, currentWorkdir, toolTimeout, shellAppr, applyAppr)
			if out.NewWorkdir != "" {
				currentWorkdir = out.NewWorkdir
			}
			payload, _ := json.Marshal(out)
			req.Messages = append(req.Messages, llm.Message{Role: llm.RoleTool, Content: string(payload), ToolCallID: "fc_0"})
			e.logToolExecution(ctx, step.ID, tc.Function.Name, tc.Function.Arguments, out.Ok, out.ToolError, out.ExitCode, out.Stderr, out.Stdout)
			continue
		default:
			// Попытка распознать inline-вызов инструмента в content (fallback для моделей без tool_calls)
			if name, args := tryExtractInlineToolCall(choice.Message.Content); name != "" {
				req.Messages = append(req.Messages, llm.Message{Role: llm.RoleAssistant, Content: choice.Message.Content})
				tc := llm.ToolCall{ID: "inline_0", Type: "function", Function: llm.FunctionCall{Name: name, Arguments: args}}
				argsShort := crop(strings.TrimSpace(args), 90)
				e.log.InfoContext(ctx, "tool call", slog.String("step", step.ID), slog.String("tool", name), slog.String("args", argsShort))
				out := e.tools.ExecCall(ctx, tc, step.LLM.ToolsAllowed, currentWorkdir, toolTimeout, shellAppr, applyAppr)
				if out.NewWorkdir != "" {
					currentWorkdir = out.NewWorkdir
				}
				payload, _ := json.Marshal(out)
				req.Messages = append(req.Messages, llm.Message{Role: llm.RoleTool, Content: string(payload), ToolCallID: tc.ID})
				// Продолжим диалог
				e.logToolExecution(ctx, step.ID, name, args, out.Ok, out.ToolError, out.ExitCode, out.Stderr, out.Stdout)
				continue
			}
			// Финальный ответ (нет вызовов tool/function) — сохраняем ответ ассистента
			req.Messages = append(req.Messages, llm.Message{Role: llm.RoleAssistant, Content: choice.Message.Content})
			// Обновим историю диалога непосредственно перед завершением (DEBUG)
			e.updateChatLogIfDebug(ctx, step.ID, req.Messages)
			return resp, req.Messages, nil
		}
	}
}

// updateChatLogIfDebug — вспомогательный метод, который пишет историю чата
// в <artifact_dir>/log/chat.json только при уровне логирования DEBUG.
func (e *Executor) updateChatLogIfDebug(ctx context.Context, stepID string, messages []llm.Message) {
	// Если DEBUG не включен — выходим сразу
	if !e.log.Enabled(ctx, slog.LevelDebug) {
		return
	}
	// Пишем историю сообщений как есть, сверху вниз — от первого к последнему
	// Ошибки записи игнорируем, чтобы не мешать основному исполнению
	_, _ = e.artifacts.WriteChatLog(stepID, messages)
}

// logToolExecution пишет единые INFO/DEBUG логи выполнения инструмента.
func (e *Executor) logToolExecution(ctx context.Context, stepID, toolName, args string, ok bool, toolError string, exitCode int, stderr, stdout string) {
	argsShort := crop(strings.TrimSpace(args), 90)
	e.log.InfoContext(ctx, "tool result",
		slog.String("step", stepID),
		slog.String("tool", toolName),
		slog.String("args", argsShort),
		slog.Bool("ok", ok),
	)
	if ok || !e.log.Enabled(ctx, slog.LevelDebug) {
		return
	}
	e.log.DebugContext(ctx, "tool error details",
		slog.String("step", stepID),
		slog.String("tool", toolName),
		slog.String("args_full", strings.TrimSpace(args)),
		slog.String("tool_error", strings.TrimSpace(toolError)),
		slog.Int("exit_code", exitCode),
		slog.String("stderr", stderr),
		slog.String("stdout", stdout),
	)
}

// buildToolCallSignature формирует стабильную строковую сигнатуру для набора tool_calls сообщения.
// Используется только для детекции бесконечной петли однообразных вызовов инструментов.
func buildToolCallSignature(msg llm.Message) string {
	if len(msg.ToolCalls) > 0 {
		var b strings.Builder
		for _, tc := range msg.ToolCalls {
			b.WriteString("[")
			b.WriteString(strings.TrimSpace(tc.Function.Name))
			b.WriteString(":")
			b.WriteString(strings.TrimSpace(tc.Function.Arguments))
			b.WriteString("]")
		}
		return b.String()
	}
	if msg.FunctionCall != nil {
		return strings.TrimSpace(msg.FunctionCall.Name) + ":" + strings.TrimSpace(msg.FunctionCall.Arguments)
	}
	return ""
}

// tryExtractInlineToolCall пытается вытащить вызов инструмента из content вида:
// "shell {"command":[...]}" или "apply_patch {"input":"*** Begin Patch..."}"
func tryExtractInlineToolCall(content string) (string, string) {
	s := strings.TrimSpace(content)
	if s == "" {
		return "", ""
	}
	// Поиск маркера имени инструмента
	for _, name := range []string{"shell", "apply_patch", "repo_browser.exec"} {
		idx := strings.Index(s, name)
		if idx >= 0 {
			rest := s[idx+len(name):]
			br := strings.Index(rest, "{")
			if br < 0 {
				continue
			}
			obj := rest[br:]
			// Выделим JSON-объект по балансировке скобок
			level := 0
			end := -1
			for i, ch := range obj {
				if ch == '{' {
					level++
				}
				if ch == '}' {
					level--
					if level == 0 {
						end = i
						break
					}
				}
			}
			if end > 0 {
				jsonPart := obj[:end+1]
				if name == "repo_browser.exec" {
					return "shell", jsonPart
				}
				return name, jsonPart
			}
		}
	}
	return "", ""
}

// buildPromptsAndRequest рендерит system/user промпты, добавляет инструкции по инструментам и формирует начальный запрос к модели.
func (e *Executor) buildPromptsAndRequest(step dsl.Step, inputs map[string]ioValue, extra map[string]any) (string, string, llm.ChatCompletionRequest, error) {
	templateCtx := map[string]any{
		// Глобальные настройки агента
		"agent": e.templateAgent(),
		// Текущий шаг и его конфигурация
		"step": templateStep(step),
		// Объявленные пользовательские функции
		"functions": e.cfg.Functions,
		// Defaults из DSL (могут пригодиться в шаблонах)
		"defaults": e.templateDefaults(),
		// Inputs шага (path/inline)
		"inputs": inputsToTemplate(inputs),
		// Доступ к outputs предыдущих шагов
		"outputs": e.outputsContext(),
	}
	// Дополнительный контекст (например, matrix)
	if extra != nil {
		for k, v := range extra {
			templateCtx[k] = v
		}
	}
	// system_prompt: из файла по system_prompt_path, иначе inline; при отсутствии — используем дефолтный встроенный шаблон
	systemPrompt, err := e.renderPromptMaybeFromFile(step.LLM.SystemPromptPath, step.LLM.SystemPrompt, templateCtx)
	if err != nil {
		return "", "", llm.ChatCompletionRequest{}, fmt.Errorf("executor: failed to resolve system_prompt for step %s: %w", step.ID, err)
	}
	systemPrompt = strings.TrimSpace(systemPrompt)
	if systemPrompt == "" {
		// Подхватываем встроенный дефолтный системный промпт и рендерим его тем же контекстом
		ts, terr := dsl.NewTemplateString(prompts.DefaultSystem)
		if terr == nil {
			if rendered, rerr := ts.Execute(templateCtx); rerr == nil {
				systemPrompt = strings.TrimSpace(rendered)
			} else {
				systemPrompt = strings.TrimSpace(prompts.DefaultSystem)
			}
		} else {
			systemPrompt = strings.TrimSpace(prompts.DefaultSystem)
		}
	}

	// Для моделей семейства */gpt-oss-* НЕ добавляем текстовые инструкции по инструментам в system
	ossModel := isGPTOSSModel(e.cfg.Agent.Model)
	if !ossModel && len(step.LLM.ToolsAllowed) > 0 {
		var toolsSection strings.Builder
		toolsSection.WriteString("\n\n# Tools\n")
		for _, t := range step.LLM.ToolsAllowed {
			switch strings.TrimSpace(t) {
			case "shell":
				toolsSection.WriteString("\n")
				toolsSection.WriteString(prompts.ShellTool)
				toolsSection.WriteString("\n")
			case "apply_patch":
				toolsSection.WriteString("\n")
				toolsSection.WriteString(prompts.ApplyPatch)
				toolsSection.WriteString("\n")
			default:
				// Пользовательские функции: если есть prompt — добавим его и JSON Schema параметров в system_prompt
				name := strings.TrimSpace(t)
				for _, fn := range e.cfg.Functions {
					if strings.TrimSpace(fn.Name) == name {
						if !fn.Prompt.IsZero() {
							txt, err := fn.Prompt.Execute(templateCtx)
							if err != nil {
								return "", "", llm.ChatCompletionRequest{}, fmt.Errorf("executor: failed to render function prompt for %s: %w", name, err)
							}
							toolsSection.WriteString("\n")
							toolsSection.WriteString(strings.TrimSpace(txt))
							toolsSection.WriteString("\n")
						}
						// Вставим схему параметров как справочную секцию, чтобы модели без поддержки tools могли ориентироваться
						if fn.Parameters != nil {
							if schema, err := json.MarshalIndent(fn.Parameters, "", "  "); err == nil {
								toolsSection.WriteString("Parameters JSON Schema for ")
								toolsSection.WriteString(name)
								toolsSection.WriteString(":\n```json\n")
								toolsSection.Write(schema)
								toolsSection.WriteString("\n```\n")
							}
						}
						break
					}
				}
			}
		}
		sp := strings.Builder{}
		sp.WriteString(systemPrompt)
		sp.WriteString(toolsSection.String())
		systemPrompt = sp.String()
	}
	// user_prompt: из файла по user_prompt_path, иначе inline
	userPrompt, err := e.renderPromptMaybeFromFile(step.LLM.UserPromptPath, step.LLM.UserPrompt, templateCtx)
	if err != nil {
		return "", "", llm.ChatCompletionRequest{}, fmt.Errorf("executor: failed to resolve user_prompt for step %s: %w", step.ID, err)
	}
	userPrompt = strings.TrimSpace(userPrompt)
	if userPrompt == "" {
		return "", "", llm.ChatCompletionRequest{}, fmt.Errorf("executor: user_prompt for step %s is empty", step.ID)
	}

	// Для gpt-oss формируем пользовательское сообщение с XML-блоками <user_instructions> и <environment_context>
	if ossModel {
		ui := strings.TrimSpace(collectAgentsDocs(e.cfg.BaseDir))
		ec := strings.TrimSpace(buildEnvironmentContext())
		var b strings.Builder
		if ui != "" {
			b.WriteString("<user_instructions>\n")
			b.WriteString(ui)
			if !strings.HasSuffix(ui, "\n") {
				b.WriteString("\n")
			}
			b.WriteString("</user_instructions>\n\n")
		}
		b.WriteString("<environment_context>\n")
		b.WriteString(ec)
		if !strings.HasSuffix(ec, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("</environment_context>\n\n")
		b.WriteString(userPrompt)
		userPrompt = b.String()
	}

	messages := make([]llm.Message, 0, 2)
	if systemPrompt != "" {
		messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: systemPrompt})
	}
	messages = append(messages, llm.Message{Role: llm.RoleUser, Content: userPrompt})
	req := llm.ChatCompletionRequest{
		Model:    e.cfg.Agent.Model,
		Messages: messages,
	}
	// Для gpt-oss не пробрасываем user/temperature/top_p/max_tokens/verbosity — только model, messages (+tools при наличии)
	if !ossModel {
		// Пробрасываем флаг reasoning из настроек агента
		req.IncludeReasoning = e.cfg.Agent.Reasoning
		if step.LLM.Temperature != nil {
			temp := *step.LLM.Temperature
			req.Temperature = &temp
		}
		if step.LLM.MaxTokens != nil && *step.LLM.MaxTokens > 0 {
			limit := *step.LLM.MaxTokens
			req.MaxTokens = &limit
		}
	}
	return systemPrompt, userPrompt, req, nil
}

// attachToolSchemas добавляет JSON‑описания инструментов в запрос
func (e *Executor) attachToolSchemas(req llm.ChatCompletionRequest, step dsl.Step) llm.ChatCompletionRequest {
	if step.LLM != nil && len(step.LLM.ToolsAllowed) > 0 {
		tools := e.tools.ToolsForAllowed(step.LLM.ToolsAllowed)
		if len(tools) > 0 {
			req.Tools = tools
		}
	}
	return req
}

// resolveToolTimeout определяет итоговый таймаут инструментов для шага
func (e *Executor) resolveToolTimeout(step *dsl.Step) time.Duration {
	tt := 60 * time.Second
	if e.cfg.Defaults != nil && e.cfg.Defaults.ToolTimeout != nil {
		tt = e.cfg.Defaults.ToolTimeout.Duration
	}
	if step.ToolTimeout != nil {
		tt = step.ToolTimeout.Duration
	}
	return tt
}

// renderPromptMaybeFromFile читает содержимое промпта из файла, если задан *_path, иначе рендерит inline-шаблон.
func (e *Executor) renderPromptMaybeFromFile(pathTpl dsl.TemplateString, inline dsl.TemplateString, ctx any) (string, error) {
	if !pathTpl.IsZero() {
		p, err := pathTpl.Execute(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to render prompt path: %w", err)
		}
		p = strings.TrimSpace(p)
		if p == "" {
			return "", fmt.Errorf("prompt path evaluated to empty")
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(e.cfg.BaseDir, p)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("failed to read prompt file %s: %w", p, err)
		}
		ts, err := dsl.NewTemplateString(string(data))
		if err != nil {
			return "", fmt.Errorf("failed to parse prompt template from %s: %w", p, err)
		}
		rendered, err := ts.Execute(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to render prompt template from %s: %w", p, err)
		}
		return strings.TrimSpace(rendered), nil
	}
	// Иначе inline
	txt, err := inline.Execute(ctx)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(txt), nil
}
