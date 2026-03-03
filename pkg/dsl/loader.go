package dsl

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

type rawConfig struct {
	Version   int        `yaml:"version"`
	Agent     rawAgent   `yaml:"agent"`
	Defaults  *Defaults  `yaml:"defaults,omitempty"`
	Functions []Function `yaml:"functions,omitempty"`
	Steps     []Step     `yaml:"steps"`
	Approvers []Approver `yaml:"approvers,omitempty"`
}

type rawAgent struct {
	Name        TemplateString `yaml:"name"`
	Model       TemplateString `yaml:"model"`
	ArtifactDir TemplateString `yaml:"artifact_dir"`
	OpenAI      rawAgentOpenAI `yaml:"openai"`
}

type rawAgentOpenAI struct {
	BaseURL   TemplateString `yaml:"base_url"`
	APIKeyEnv TemplateString `yaml:"api_key_env"`
}

// LoadFile читает YAML-конфигурацию агента с диска, валидирует её и возвращает структуру.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("dsl: failed to read file %s: %w", path, err)
	}

	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("dsl: failed to parse YAML %s: %w", path, err)
	}

	cfg, err := normalize(raw)
	if err != nil {
		return nil, err
	}

	if err := Validate(cfg); err != nil {
		return nil, err
	}

	// Сохраним базовую директорию конфигурации для разрешения относительных путей
	cfg.BaseDir = filepath.Dir(path)
	// Проверим *_path поля для промптов: существование и непустоту
	if err := validatePromptPaths(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

func normalize(raw rawConfig) (*Config, error) {
	render := func(field TemplateString, fieldName string) (string, error) {
		value, err := field.Execute(nil)
		if err != nil {
			return "", fmt.Errorf("dsl: failed to evaluate template for %s: %w", fieldName, err)
		}
		return strings.TrimSpace(value), nil
	}

	name, err := render(raw.Agent.Name, "agent.name")
	if err != nil {
		return nil, err
	}
	model, err := render(raw.Agent.Model, "agent.model")
	if err != nil {
		return nil, err
	}
	artifactDir, err := render(raw.Agent.ArtifactDir, "agent.artifact_dir")
	if err != nil {
		return nil, err
	}
	baseURL, err := render(raw.Agent.OpenAI.BaseURL, "agent.openai.base_url")
	if err != nil {
		return nil, err
	}
	apiKeyEnv, err := render(raw.Agent.OpenAI.APIKeyEnv, "agent.openai.api_key_env")
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Version:   raw.Version,
		Defaults:  raw.Defaults,
		Functions: raw.Functions,
		Steps:     raw.Steps,
		Approvers: raw.Approvers,
		Agent: Agent{
			Name:        name,
			Model:       model,
			ArtifactDir: artifactDir,
			OpenAI: AgentOpenAI{
				BaseURL:   baseURL,
				APIKeyEnv: apiKeyEnv,
			},
		},
	}

	// Принудительно проставляем additional_properties=false в parameters пользовательских функций
	for i := range cfg.Functions {
		params := cfg.Functions[i].Parameters
		if params == nil {
			continue
		}
		// Устанавливаем флаг независимо от исходного значения
		params["additional_properties"] = false
		cfg.Functions[i].Parameters = params
	}

	return cfg, nil
}

// validatePromptPaths проверяет, что указанные пути к файлам промптов существуют и непустые.
// Также не допускает одновременного указания inline и *_path варианта.
func validatePromptPaths(cfg *Config) error {
	var problems []string
	for i := range cfg.Steps {
		s := &cfg.Steps[i]
		if strings.TrimSpace(s.Type) != "llm" || s.LLM == nil {
			continue
		}
		// Конфликты inline vs path
		if !s.LLM.SystemPrompt.IsZero() && !s.LLM.SystemPromptPath.IsZero() {
			problems = append(problems, fmt.Sprintf("steps[%d]: both system_prompt and system_prompt_path are set", i))
		}
		if !s.LLM.UserPrompt.IsZero() && !s.LLM.UserPromptPath.IsZero() {
			problems = append(problems, fmt.Sprintf("steps[%d]: both user_prompt and user_prompt_path are set", i))
		}
		// Файлы
		if problem := validatePromptPath(cfg.BaseDir, i, "system_prompt_path", s.LLM.SystemPromptPath); problem != "" {
			problems = append(problems, problem)
		}
		if problem := validatePromptPath(cfg.BaseDir, i, "user_prompt_path", s.LLM.UserPromptPath); problem != "" {
			problems = append(problems, problem)
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("dsl: configuration errors (prompt paths):\n - %s", strings.Join(problems, "\n - "))
	}
	return nil
}

func validatePromptPath(baseDir string, stepIdx int, fieldName string, tpl TemplateString) string {
	if tpl.IsZero() {
		return ""
	}
	p, err := tpl.Execute(nil)
	if err != nil {
		// Шаблон пути может зависеть от runtime-контекста (.matrix/.outputs и т.п.),
		// который недоступен на этапе загрузки конфигурации.
		// В этом случае пропускаем строгую проверку существования и валидируем путь во время выполнения шага.
		return ""
	}
	p = strings.TrimSpace(p)
	if p == "" {
		return fmt.Sprintf("steps[%d]: %s evaluated to empty", stepIdx, fieldName)
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(baseDir, p)
	}
	if err := fileNonEmpty(p); err != nil {
		return fmt.Sprintf("steps[%d]: %s invalid: %v", stepIdx, fieldName, err)
	}
	return ""
}

// fileNonEmpty — проверка, что файл существует и не пустой.
func fileNonEmpty(p string) error {
	st, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("file does not exist: %v", err)
	}
	if st.IsDir() {
		return fmt.Errorf("path points to a directory: %s", p)
	}
	if st.Size() == 0 {
		return fmt.Errorf("file is empty: %s", p)
	}
	return nil
}
