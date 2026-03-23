package tokens

import (
	"path"
	"strings"
)

const (
	// DefaultFallbackContextWindow задаёт размер контекстного окна по умолчанию для неизвестных моделей.
	DefaultFallbackContextWindow = 128000
	// DefaultToolWarnPercent задаёт порог предупреждения по размеру результата инструмента.
	DefaultToolWarnPercent = 10
	// DefaultAutoCompactPercent задаёт порог автокомпактизации контекста.
	DefaultAutoCompactPercent = 85
	// DefaultSafetyMarginPercent задаёт запас для предоценки размера следующего запроса.
	DefaultSafetyMarginPercent = 15
)

// ModelProfile описывает известные свойства модели, важные для оценки токенов.
type ModelProfile struct {
	// RequestedModel хранит исходное имя модели из конфигурации или окружения.
	RequestedModel string
	// NormalizedModel хранит нормализованное имя модели без префикса провайдера.
	NormalizedModel string
	// DisplayName хранит короткое отображаемое имя распознанной модели.
	DisplayName string
	// HFTokenizerModelID хранит идентификатор репозитория HuggingFace с tokenizer.json.
	HFTokenizerModelID string
	// ContextWindow хранит размер контекстного окна модели в токенах.
	ContextWindow int
}

type knownModel struct {
	displayName        string
	matchers           []string
	hfTokenizerModelID string
	contextWindow      int
}

var knownModels = []knownModel{
	{
		displayName:        "gpt-oss-120b",
		matchers:           []string{"gpt-oss-120b"},
		hfTokenizerModelID: "openai/gpt-oss-120b",
		contextWindow:      131072,
	},
	{
		displayName:        "qwen3-coder-480b",
		matchers:           []string{"qwen3-coder-480b"},
		hfTokenizerModelID: "Qwen/Qwen3-Coder-480B-A35B-Instruct",
		contextWindow:      262144,
	},
	{
		displayName:        "qwen3-next-80b-a3b-instruct",
		matchers:           []string{"qwen3-next-80b-a3b-instruct"},
		hfTokenizerModelID: "Qwen/Qwen3-Next-80B-A3B-Instruct",
		contextWindow:      262144,
	},
	{
		displayName:        "glm-4.6",
		matchers:           []string{"glm-4.6"},
		hfTokenizerModelID: "zai-org/GLM-4.6",
		contextWindow:      200000,
	},
	{
		displayName:        "qwen3-235b-instruct",
		matchers:           []string{"qwen3-235b-instruct", "qwen3-235b-a22b-instruct"},
		hfTokenizerModelID: "Qwen/Qwen3-235B-A22B-Instruct-2507",
		contextWindow:      262144,
	},
	{
		displayName:        "minimax-m2",
		matchers:           []string{"minimax-m2"},
		hfTokenizerModelID: "MiniMaxAI/MiniMax-M2",
		contextWindow:      196608,
	},
}

// ResolveModelProfile подбирает профиль модели по имени и при необходимости применяет override окна контекста.
func ResolveModelProfile(model string, contextWindowOverride *int) ModelProfile {
	normalized := normalizeModelName(model)
	for _, candidate := range knownModels {
		for _, matcher := range candidate.matchers {
			if strings.Contains(normalized, matcher) {
				profile := ModelProfile{
					RequestedModel:     model,
					NormalizedModel:    normalized,
					DisplayName:        candidate.displayName,
					HFTokenizerModelID: candidate.hfTokenizerModelID,
					ContextWindow:      candidate.contextWindow,
				}
				if contextWindowOverride != nil && *contextWindowOverride > 0 {
					profile.ContextWindow = *contextWindowOverride
				}
				return profile
			}
		}
	}

	profile := ModelProfile{
		RequestedModel:  model,
		NormalizedModel: normalized,
		DisplayName:     normalized,
		ContextWindow:   DefaultFallbackContextWindow,
	}
	if contextWindowOverride != nil && *contextWindowOverride > 0 {
		profile.ContextWindow = *contextWindowOverride
	}
	return profile
}

func normalizeModelName(model string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return "unknown"
	}
	base := path.Base(trimmed)
	base = strings.TrimSpace(base)
	base = strings.ToLower(base)
	base = strings.ReplaceAll(base, "_", "-")
	base = strings.ReplaceAll(base, " ", "-")
	return base
}
