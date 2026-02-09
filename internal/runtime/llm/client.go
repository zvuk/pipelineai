package llm

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/option"
)

// Client инкапсулирует доступ к Chat Completions API.
type Client struct {
	modelCfg ModelConfig
	service  openai.ChatCompletionService
	log      *slog.Logger
}

// NewClient создаёт клиента LLM на основе официального SDK OpenAI.
func NewClient(cfg ModelConfig, log *slog.Logger) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if log == nil {
		log = slog.Default()
	}
	log = log.With(slog.String("component", "llm.client"))

	httpClient := &http.Client{Timeout: cfg.RequestTimeout}

	opts := []option.RequestOption{
		option.WithHTTPClient(httpClient),
		option.WithBaseURL(strings.TrimRight(cfg.BaseURL, "/")),
	}

	if apiKey := strings.TrimSpace(cfg.APIKey); apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}

	client := openai.NewClient(opts...)

	return &Client{
		modelCfg: cfg,
		service:  client.Chat.Completions,
		log:      log,
	}, nil
}

// CreateChatCompletion выполняет запрос к Chat Completions API через SDK.
func (c *Client) CreateChatCompletion(ctx context.Context, req ChatCompletionRequest) (ChatCompletionResponse, error) {
	if len(req.Messages) == 0 {
		return ChatCompletionResponse{}, fmt.Errorf("llm: at least one message is required")
	}

	if strings.TrimSpace(req.Model) == "" {
		req.Model = c.modelCfg.Model
	}

	messageParams, err := BuildMessageParams(req.Messages)
	if err != nil {
		return ChatCompletionResponse{}, err
	}

	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(req.Model),
		Messages: messageParams,
	}

	// Явно запрещаем параллельные tool_calls, чтобы модель возвращала последовательные вызовы
	params.ParallelToolCalls = openai.Bool(false)

	// Проброс инструментов (function calling)
	if len(req.Tools) > 0 {
		params.Tools = BuildToolParams(req.Tools)
	}

	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		params.MaxTokens = openai.Int(int64(*req.MaxTokens))
	}
	if req.Temperature != nil {
		params.Temperature = openai.Float(float64(*req.Temperature))
	}
	if req.TopP != nil {
		params.TopP = openai.Float(float64(*req.TopP))
	}

	// Если включён reasoning — повышаем verbosity до high (где поддерживается)
	if req.IncludeReasoning {
		params.Verbosity = openai.ChatCompletionNewParamsVerbosityHigh
	}

	var completion *openai.ChatCompletion
	// Политика ретраев:
	// - для обычных ретраемых ошибок (400 max_tokens, 413, timeouts, network) — не более 3 ретраев;
	// - для серверных 5xx — не более 5 ретраев;
	// - экспоненциальный бэкофф, ограниченный 60 секундами.
	const maxDefaultRetries = 3
	const maxServerRetries = 5

	attempt := 0
	for {
		attempt++
		start := time.Now()
		completion, err = c.service.New(ctx, params)
		if err == nil {
			// Успешно — DEBUG: статистика токенов и время
			elapsed := time.Since(start)
			usage := completion.Usage
			c.log.DebugContext(
				ctx,
				"llm usage",
				slog.String("model", completion.Model),
				slog.Duration("elapsed", elapsed),
				slog.Int64("prompt_tokens", usage.PromptTokens),
				slog.Int64("completion_tokens", usage.CompletionTokens),
				slog.Int64("total_tokens", usage.TotalTokens),
			)
			return ConvertCompletion(completion), nil
		}

		// Решим, можно ли ретраить эту ошибку и сколько раз
		if !isRetryableError(err) {
			c.log.ErrorContext(ctx, "chat completion failed", slog.Any("error", err.Error()))
			return ChatCompletionResponse{}, err
		}

		maxRetries := maxDefaultRetries
		if isServerError(err) {
			maxRetries = maxServerRetries
		}

		if attempt > maxRetries {
			// Превышен лимит ретраев — возвращаем ошибку, дальше шаг/сценарий решают, как перезапускаться.
			c.log.ErrorContext(ctx, "chat completion failed after retries", slog.Int("attempts", attempt-1), slog.Any("error", err.Error()))
			return ChatCompletionResponse{}, err
		}

		// Экспоненциальный бэкофф (1s, 2s, 4s, ...) с ограничением 60s
		backoff := time.Duration(1<<uint(min(attempt-1, 6))) * time.Second
		if backoff > 60*time.Second {
			backoff = 60 * time.Second
		}
		c.log.WarnContext(ctx, "chat completion retry", slog.Int("attempt", attempt), slog.Any("error", err.Error()))
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ChatCompletionResponse{}, ctx.Err()
		}
	}
}

// isRetryableError пытается эвристически определить временную ошибку провайдера (5xx/timeout/network).
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	e := strings.ToLower(err.Error())
	// Классические серверные/сеть
	if strings.Contains(e, " 500 ") || strings.Contains(e, " 502 ") || strings.Contains(e, " 503 ") || strings.Contains(e, " 504 ") {
		return true
	}
	if strings.Contains(e, "internal server error") || strings.Contains(e, "service unavailable") || strings.Contains(e, "gateway") {
		return true
	}
	if strings.Contains(e, "timeout") || strings.Contains(e, "temporar") || strings.Contains(e, "connection reset") {
		return true
	}
	// Глюки прокси: 400 с max_tokens < 1
	if strings.Contains(e, " 400 ") && (strings.Contains(e, "max_tokens must be at least 1") || strings.Contains(e, "max tokens must be at least 1")) {
		return true
	}
	// Глюки прокси: 413 Payload Too Large — иногда временная ошибка
	if strings.Contains(e, " 413 ") || strings.Contains(e, "payload too large") || strings.Contains(e, "request entity too large") {
		return true
	}
	return false
}

// isServerError определяет 5xx-ошибки (сервер/прокси), для которых допускаем больше ретраев.
func isServerError(err error) bool {
	if err == nil {
		return false
	}
	e := strings.ToLower(err.Error())
	if strings.Contains(e, " 500 ") || strings.Contains(e, " 502 ") || strings.Contains(e, " 503 ") || strings.Contains(e, " 504 ") {
		return true
	}
	if strings.Contains(e, "internal server error") || strings.Contains(e, "service unavailable") || strings.Contains(e, "gateway") {
		return true
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
