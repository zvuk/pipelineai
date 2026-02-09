// Package llm содержит модели и клиент для взаимодействия с OpenAI-совместимыми LLM.
package llm

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// RoleSystem обозначает системные подсказки для модели.
	RoleSystem = "system"
	// RoleUser используется для пользовательских сообщений.
	RoleUser = "user"
	// RoleAssistant идентифицирует ответы ассистента.
	RoleAssistant = "assistant"
	// RoleTool помечает сообщения от инструмента после function calling.
	RoleTool = "tool"
)

var (
	errEmptyBaseURL = errors.New("llm: base URL is required")
	errEmptyModel   = errors.New("llm: model name is required")
)

// ModelConfig описывает параметры доступа к модели.
type ModelConfig struct {
	BaseURL        string
	APIKey         string
	Model          string
	RequestTimeout time.Duration
}

// Validate проверяет корректность настроек модели.
func (cfg ModelConfig) Validate() error {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return errEmptyBaseURL
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return errEmptyModel
	}
	if cfg.RequestTimeout <= 0 {
		return fmt.Errorf("llm: invalid request timeout: %s", cfg.RequestTimeout)
	}
	return nil
}

// Message представляет отдельное сообщение в диалоге с моделью.
type Message struct {
	Role         string        `json:"role"`
	Content      string        `json:"content,omitempty"`
	Name         string        `json:"name,omitempty"`
	ToolCallID   string        `json:"tool_call_id,omitempty"`
	ToolCalls    []ToolCall    `json:"tool_calls,omitempty"`
	FunctionCall *FunctionCall `json:"function_call,omitempty"`
	// Reasoning — необязательное поле от моделей, поддерживающих reasoning-текст
	Reasoning string `json:"reasoning,omitempty"`
}

// FunctionCall хранит данные о вызове функции из ответа модели.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCall описывает структуру tool_calls из Chat Completions API.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// Tool описывает объявленную функцию, которую модель может вызвать.
type Tool struct {
	Type     string           `json:"type"`
	Function ToolFunctionSpec `json:"function"`
}

// ToolFunctionSpec содержит описание пользовательской функции для function calling.
type ToolFunctionSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

// ToolChoice задаёт стратегию выбора инструмента моделью.
type ToolChoice struct {
	Type     string              `json:"type"`
	Function *ToolChoiceFunction `json:"function,omitempty"`
}

// ToolChoiceFunction ограничивает модель вызовом конкретной функции.
type ToolChoiceFunction struct {
	Name string `json:"name"`
}

// ChatCompletionRequest описывает входные данные для Chat Completions API.
type ChatCompletionRequest struct {
	Model       string      `json:"model"`
	Messages    []Message   `json:"messages"`
	MaxTokens   *int        `json:"max_tokens,omitempty"`
	Temperature *float32    `json:"temperature,omitempty"`
	TopP        *float32    `json:"top_p,omitempty"`
	Tools       []Tool      `json:"tools,omitempty"`
	ToolChoice  *ToolChoice `json:"tool_choice,omitempty"`
	User        string      `json:"user,omitempty"`
	Stream      bool        `json:"stream,omitempty"`
	// IncludeReasoning — запросить/сохранить reasoning, если модель возвращает его
	IncludeReasoning bool `json:"-"`
}

// ChatCompletionResponse описывает ответ Chat Completions API.
type ChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
	Usage   Usage                  `json:"usage"`
}

// ChatCompletionChoice представляет отдельный вариант ответа модели.
type ChatCompletionChoice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage содержит статистику токенов, возвращаемую API.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
