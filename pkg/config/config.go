package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

const (
	// DefaultEnvFile содержит имя файла окружения, который мы пытаемся загрузить автоматически.
	DefaultEnvFile = ".env"
	// TemplateEnvFile указывает на шаблон окружения, используемый в make init.
	TemplateEnvFile = ".tpl.env"
)

// Settings хранит конфигурацию приложения, прочитанную из переменных окружения.
type Settings struct {
	AppName            string        `env:"APP_NAME" envDefault:"pipelineai"`
	Environment        string        `env:"ENV" envDefault:"dev"`
	LLMBaseURL         string        `env:"LLM_BASE_URL" envDefault:"http://localhost:1234/v1"`
	LLMAPIKey          string        `env:"LLM_API_KEY"`
	LLMModel           string        `env:"LLM_MODEL" envDefault:"openai/gpt-oss-20b"`
	LLMRequestTimeout  time.Duration `env:"LLM_REQUEST_TIMEOUT" envDefault:"120s"`
	DefaultToolTimeout time.Duration `env:"DEFAULT_TOOL_TIMEOUT" envDefault:"60s"`
}

// LoadEnvFile загружает переменные окружения из указанного файла.
// При override = true существующие переменные будут перезаписаны.
func LoadEnvFile(path string, override bool) error {
	if override {
		return godotenv.Overload(path)
	}
	return godotenv.Load(path)
}

// LoadEnvFileIfExists загружает env-файл, игнорируя его отсутствие.
func LoadEnvFileIfExists(path string, override bool) error {
	err := LoadEnvFile(path, override)
	if err == nil {
		return nil
	}

	var pathErr *os.PathError
	if errors.As(err, &pathErr) && errors.Is(pathErr, os.ErrNotExist) {
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Load возвращает структуру настроек из текущего окружения.
func Load() (Settings, error) {
	cfg := Settings{}
	if err := env.Parse(&cfg); err != nil {
		return Settings{}, fmt.Errorf("config: failed to parse environment: %w", err)
	}

	if strings.TrimSpace(cfg.LLMBaseURL) == "" {
		return Settings{}, errors.New("config: LLM_BASE_URL is required")
	}

	if strings.TrimSpace(cfg.LLMModel) == "" {
		return Settings{}, errors.New("config: LLM_MODEL is required")
	}

	if cfg.LLMRequestTimeout <= 0 {
		return Settings{}, fmt.Errorf("config: LLM_REQUEST_TIMEOUT must be > 0, got %s", cfg.LLMRequestTimeout)
	}

	return cfg, nil
}
