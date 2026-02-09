package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zvuk/pipelineai/internal/logger"
	"github.com/zvuk/pipelineai/internal/runtime/llm"
	"github.com/zvuk/pipelineai/pkg/config"
)

const (
	defaultSystemPrompt = "Ты — ассистент PipelineAI. Отвечай кратко."
	defaultUserPrompt   = "Проверка связи."
)

func newLLMSmokeCommand(log *slog.Logger) *cobra.Command {
	var (
		systemFlag      string
		userFlag        string
		modelOverride   string
		temperatureFlag float64
		timeoutOverride time.Duration
	)

	cmd := &cobra.Command{
		Use:   "llm-smoke",
		Short: "Выполняет пробный запрос к LLM",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLLMSmoke(cmd.Context(), smokeOptions{
				System:          systemFlag,
				User:            userFlag,
				ModelOverride:   modelOverride,
				Temperature:     float32(temperatureFlag),
				TimeoutOverride: timeoutOverride,
				Log:             log,
				Stdout:          cmd.OutOrStdout(),
				Stderr:          cmd.ErrOrStderr(),
			})
		},
	}

	cmd.Flags().StringVar(&systemFlag, "system", defaultSystemPrompt, "системное сообщение для LLM")
	cmd.Flags().StringVar(&userFlag, "user", defaultUserPrompt, "пользовательское сообщение для LLM")
	cmd.Flags().StringVar(&modelOverride, "model", "", "переопределяет модель из окружения")
	cmd.Flags().Float64Var(&temperatureFlag, "temperature", 0, "температура выборки (0 — детерминированно)")
	cmd.Flags().DurationVar(&timeoutOverride, "timeout", 0, "локальное переопределение таймаута запроса")

	return cmd
}

type smokeOptions struct {
	System          string
	User            string
	ModelOverride   string
	Temperature     float32
	TimeoutOverride time.Duration
	Log             *slog.Logger
	Stdout          io.Writer
	Stderr          io.Writer
}

func runLLMSmoke(ctx context.Context, opts smokeOptions) error {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	settings, err := config.Load()
	if err != nil {
		return err
	}

	if trimmed := strings.TrimSpace(opts.ModelOverride); trimmed != "" {
		settings.LLMModel = trimmed
	}
	if timeout := opts.TimeoutOverride; timeout > 0 {
		settings.LLMRequestTimeout = timeout
	}

	modelCfg := llm.ModelConfig{
		BaseURL:        settings.LLMBaseURL,
		APIKey:         settings.LLMAPIKey,
		Model:          settings.LLMModel,
		RequestTimeout: settings.LLMRequestTimeout,
	}

	client, err := llm.NewClient(modelCfg, opts.Log)
	if err != nil {
		return err
	}

	messages := make([]llm.Message, 0, 2)
	if system := strings.TrimSpace(opts.System); system != "" {
		messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: system})
	}

	userMessage := strings.TrimSpace(opts.User)
	if userMessage == "" {
		return fmt.Errorf("user message must be provided via --user flag")
	}
	messages = append(messages, llm.Message{Role: llm.RoleUser, Content: userMessage})

	tempValue := opts.Temperature
	req := llm.ChatCompletionRequest{
		Model:       settings.LLMModel,
		Messages:    messages,
		Temperature: &tempValue,
	}

	reqCtx, cancel := context.WithTimeout(ctx, settings.LLMRequestTimeout+5*time.Second)
	defer cancel()
	reqCtx = logger.WithContext(reqCtx, opts.Log)

	opts.Log.InfoContext(reqCtx, "running llm-smoke", slog.String("model", req.Model), slog.Duration("timeout", settings.LLMRequestTimeout))

	resp, err := client.CreateChatCompletion(reqCtx, req)
	if err != nil {
		return err
	}

	if len(resp.Choices) == 0 {
		opts.Log.WarnContext(reqCtx, "model returned no choices")
		return nil
	}

	choice := resp.Choices[0]
	opts.Log.InfoContext(reqCtx, "model response received",
		slog.String("model", resp.Model),
		slog.String("finish_reason", choice.FinishReason),
		slog.Int("prompt_tokens", resp.Usage.PromptTokens),
		slog.Int("completion_tokens", resp.Usage.CompletionTokens),
		slog.Int("total_tokens", resp.Usage.TotalTokens),
		slog.String("response_id", resp.ID),
		slog.String("content", strings.TrimSpace(choice.Message.Content)),
	)

	if len(choice.Message.ToolCalls) > 0 {
		opts.Log.InfoContext(reqCtx, "model produced tool calls", slog.Int("tool_calls", len(choice.Message.ToolCalls)))
		for _, call := range choice.Message.ToolCalls {
			opts.Log.InfoContext(reqCtx, "tool call",
				slog.String("id", call.ID),
				slog.String("name", call.Function.Name),
				slog.String("args", call.Function.Arguments),
			)
		}
	}

	fmt.Fprintln(opts.Stdout, strings.TrimSpace(choice.Message.Content))
	return nil
}
