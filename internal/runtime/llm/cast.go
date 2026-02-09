package llm

import (
	"encoding/json"
	"fmt"
	"strings"

	openai "github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/packages/param"
)

// BuildMessageParams преобразует список внутренних сообщений в параметр SDK OpenAI.
func BuildMessageParams(messages []Message) ([]openai.ChatCompletionMessageParamUnion, error) {
	params := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages))

	for _, message := range messages {
		switch message.Role {
		case RoleSystem:
			params = append(params, openai.SystemMessage(message.Content))
		case RoleUser:
			params = append(params, openai.UserMessage(message.Content))
		case RoleAssistant:
			// Если есть tool_calls — используем явный параметр Assistant с ToolCalls
			if len(message.ToolCalls) > 0 {
				assistant := openai.ChatCompletionAssistantMessageParam{}
				if c := strings.TrimSpace(message.Content); c != "" {
					assistant.Content.OfString = param.NewOpt(c)
				}
				assistant.ToolCalls = make([]openai.ChatCompletionMessageToolCallUnionParam, 0, len(message.ToolCalls))
				for _, tc := range message.ToolCalls {
					assistant.ToolCalls = append(assistant.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
						OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
							ID: tc.ID,
							Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
								Name:      tc.Function.Name,
								Arguments: tc.Function.Arguments,
							},
						},
					})
				}
				params = append(params, openai.ChatCompletionMessageParamUnion{OfAssistant: &assistant})
				continue
			}
			// Если есть function_call (deprecated) — заполняем соответствующее поле
			if message.FunctionCall != nil {
				assistant := openai.ChatCompletionAssistantMessageParam{}
				if c := strings.TrimSpace(message.Content); c != "" {
					assistant.Content.OfString = param.NewOpt(c)
				}
				assistant.FunctionCall = openai.ChatCompletionAssistantMessageParamFunctionCall{
					Name:      message.FunctionCall.Name,
					Arguments: message.FunctionCall.Arguments,
				}
				params = append(params, openai.ChatCompletionMessageParamUnion{OfAssistant: &assistant})
				continue
			}
			// Обычный ассистент без tool_calls
			params = append(params, openai.ChatCompletionMessageParamOfAssistant(message.Content))
		case RoleTool:
			if strings.TrimSpace(message.ToolCallID) == "" {
				return nil, fmt.Errorf("llm: tool messages require tool_call_id")
			}
			params = append(params, openai.ToolMessage(message.Content, message.ToolCallID))
		default:
			return nil, fmt.Errorf("llm: unsupported role %q", message.Role)
		}
	}

	return params, nil
}

// BuildToolParams преобразует список объявленных инструментов в параметр SDK OpenAI.
func BuildToolParams(tools []Tool) []openai.ChatCompletionToolUnionParam {
	if len(tools) == 0 {
		return nil
	}
	res := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for _, t := range tools {
		// По умолчанию считаем тип function
		fn := t.Function
		def := openai.FunctionDefinitionParam{
			Name: fn.Name,
		}
		if desc := strings.TrimSpace(fn.Description); desc != "" {
			def.Description = param.NewOpt(desc)
		}
		// Параметры — как есть (JSON Schema object)
		if fn.Parameters != nil {
			if mp, ok := fn.Parameters.(map[string]any); ok {
				def.Parameters = mp
			} else if mp2, ok := fn.Parameters.(map[string]interface{}); ok {
				def.Parameters = mp2
			} else if s, ok := fn.Parameters.(string); ok {
				var m map[string]any
				if err := json.Unmarshal([]byte(s), &m); err == nil {
					def.Parameters = m
				}
			}
		}
		res = append(res, openai.ChatCompletionFunctionTool(def))
	}
	return res
}

// ConvertCompletion приводит ChatCompletion SDK к внутренней структуре ответа.
func ConvertCompletion(completion *openai.ChatCompletion) ChatCompletionResponse {
	resp := ChatCompletionResponse{
		ID:      completion.ID,
		Created: completion.Created,
		Model:   completion.Model,
		Usage: Usage{
			PromptTokens:     int(completion.Usage.PromptTokens),
			CompletionTokens: int(completion.Usage.CompletionTokens),
			TotalTokens:      int(completion.Usage.TotalTokens),
		},
	}

	resp.Choices = make([]ChatCompletionChoice, 0, len(completion.Choices))
	for _, choice := range completion.Choices {
		resp.Choices = append(resp.Choices, ChatCompletionChoice{
			Index:        int(choice.Index),
			Message:      ConvertMessage(choice.Message),
			FinishReason: choice.FinishReason,
		})
	}

	return resp
}

// ConvertMessage преобразует сообщение OpenAI в внутреннюю структуру Message.
func ConvertMessage(message openai.ChatCompletionMessage) Message {
	result := Message{
		Role:    string(message.Role),
		Content: strings.TrimSpace(message.Content),
	}

	if message.FunctionCall.Name != "" || message.FunctionCall.Arguments != "" {
		result.FunctionCall = &FunctionCall{
			Name:      message.FunctionCall.Name,
			Arguments: message.FunctionCall.Arguments,
		}
	}

	if len(message.ToolCalls) > 0 {
		result.ToolCalls = make([]ToolCall, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			switch tc := call.AsAny().(type) {
			case openai.ChatCompletionMessageFunctionToolCall:
				result.ToolCalls = append(result.ToolCalls, ToolCall{
					ID:   tc.ID,
					Type: string(tc.Type),
					Function: FunctionCall{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				})
			}
		}
	}

	// Пытаемся извлечь reasoning из сырого JSON, если провайдер его возвращает
	if raw := message.RawJSON(); strings.Contains(raw, "\"reasoning\"") {
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err == nil {
			if rv, ok := m["reasoning"]; ok {
				switch t := rv.(type) {
				case string:
					result.Reasoning = strings.TrimSpace(t)
				case map[string]any:
					// Некоторые провайдеры могут вложить reasoning объектом
					if s, ok := t["content"].(string); ok {
						result.Reasoning = strings.TrimSpace(s)
					}
				}
			}
		}
	}

	return result
}
