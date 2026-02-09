package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zvuk/pipelineai/internal/runtime/executor"
	"github.com/zvuk/pipelineai/internal/runtime/llm"
	"github.com/zvuk/pipelineai/pkg/config"
	"github.com/zvuk/pipelineai/pkg/dsl"
)

func newRunCommand(log *slog.Logger) *cobra.Command {
	var (
		configPath      string
		stepID          string
		artifactPath    string
		timeoutOverride time.Duration
		parallel        int
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Запускает сценарий целиком или отдельный шаг из YAML-конфигурации",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStep(cmd.Context(), runOptions{
				ConfigPath:      configPath,
				StepID:          stepID,
				ArtifactDir:     artifactPath,
				TimeoutOverride: timeoutOverride,
				Parallel:        parallel,
				Log:             log,
				Out:             cmd.OutOrStdout(),
				Err:             cmd.ErrOrStderr(),
			})
		},
	}

	cmd.Flags().StringVar(&configPath, "config", "", "путь к .agents.yaml")
	cmd.Flags().StringVar(&stepID, "execute-step", "", "идентификатор шага для запуска (если не указан, выполняется весь сценарий)")
	cmd.Flags().StringVar(&artifactPath, "artifact-dir", "", "каталог артефактов (переопределяет agent.artifact_dir)")
	cmd.Flags().DurationVar(&timeoutOverride, "timeout", 0, "таймаут обращения к LLM (переопределяет окружение)")
	cmd.Flags().IntVar(&parallel, "parallel", 4, "локальный параллелизм для matrix-шага (кол-во одновременных элементов)")

	_ = cmd.MarkFlagRequired("config")

	return cmd
}

type runOptions struct {
	ConfigPath      string
	StepID          string
	ArtifactDir     string
	TimeoutOverride time.Duration
	Parallel        int
	Log             *slog.Logger
	Out             io.Writer
	Err             io.Writer
}

func runStep(ctx context.Context, opts runOptions) error {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}

	cfg, err := dsl.LoadFile(opts.ConfigPath)
	if err != nil {
		return err
	}

	settings, err := config.Load()
	if err != nil {
		return err
	}

	if timeout := opts.TimeoutOverride; timeout > 0 {
		settings.LLMRequestTimeout = timeout
	}

	baseURL := strings.TrimSpace(cfg.Agent.OpenAI.BaseURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(settings.LLMBaseURL)
	}
	if baseURL == "" {
		return fmt.Errorf("llm base URL must be defined either in YAML or environment")
	}

	model := strings.TrimSpace(cfg.Agent.Model)
	if model == "" {
		model = strings.TrimSpace(settings.LLMModel)
	}
	if model == "" {
		return fmt.Errorf("llm model must be defined either in YAML or environment")
	}

	apiKeyEnv := strings.TrimSpace(cfg.Agent.OpenAI.APIKeyEnv)
	apiKey := ""
	if apiKeyEnv != "" {
		apiKey = os.Getenv(apiKeyEnv)
	}
	if apiKey == "" {
		apiKey = strings.TrimSpace(settings.LLMAPIKey)
	}

	cfg.Agent.Model = model
	cfg.Agent.OpenAI.BaseURL = baseURL
	cfg.Agent.OpenAI.APIKeyEnv = apiKeyEnv

	modelCfg := llm.ModelConfig{
		BaseURL:        baseURL,
		APIKey:         apiKey,
		Model:          model,
		RequestTimeout: settings.LLMRequestTimeout,
	}

	client, err := llm.NewClient(modelCfg, opts.Log)
	if err != nil {
		return err
	}

	artifactDir := opts.ArtifactDir
	if strings.TrimSpace(artifactDir) == "" {
		artifactDir = cfg.Agent.ArtifactDir
	}

	// INFO: старт сценария — базовые параметры
	opts.Log.Info("scenario start",
		slog.String("agent", strings.TrimSpace(cfg.Agent.Name)),
		slog.String("model", model),
		slog.Duration("llm_timeout", settings.LLMRequestTimeout),
		slog.String("artifact_dir", artifactDir),
	)

	exec, err := executor.New(cfg, client, artifactDir, opts.Log)
	if err != nil {
		return err
	}

	// Рассчитываем таймаут сценария
	scenarioTO, err := scenarioTimeout(cfg, opts.StepID)
	if err != nil {
		return err
	}
	stepCtx, cancel := context.WithTimeout(ctx, scenarioTO)
	defer cancel()

	if strings.TrimSpace(opts.StepID) == "" {
		// Выполнить весь сценарий
		if err := exec.RunAll(stepCtx, opts.Parallel); err != nil {
			return err
		}
		fmt.Fprintln(opts.Out, "Сценарий выполнен.")
	} else {
		// Выполнить конкретный шаг с зависимостями
		if err := exec.RunWithNeeds(stepCtx, opts.StepID, opts.Parallel); err != nil {
			return err
		}
		fmt.Fprintf(opts.Out, "Шаг %s и его зависимости выполнены.\n", opts.StepID)
	}

	return nil
}
