package tokens

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/zvuk/pipelineai/internal/runtime/llm"
	"github.com/zvuk/pipelineai/pkg/dsl"
)

// Estimate описывает результат подсчёта или оценки числа токенов.
type Estimate struct {
	// Tokens хранит итоговую оценку числа токенов.
	Tokens int
	// Exact показывает, была ли оценка получена точным токенайзером.
	Exact bool
	// Strategy описывает использованную стратегию подсчёта.
	Strategy string
	// Warning содержит пояснение, если пришлось перейти на fallback-оценку.
	Warning string
}

// Counter описывает интерфейс счётчика токенов, используемый executor.
type Counter interface {
	ResolveModel(model string, contextWindowOverride *int) ModelProfile
	CountText(model string, contextWindowOverride *int, text string) Estimate
	EstimateMessage(model string, contextWindowOverride *int, msg llm.Message) Estimate
	EstimateMessages(model string, contextWindowOverride *int, messages []llm.Message) Estimate
	EstimateRequest(model string, contextWindowOverride *int, req llm.ChatCompletionRequest) Estimate
}

// Manager считает токены для текста и LLM-запросов с точным backend'ом и fallback-оценкой.
type Manager struct {
	log        *slog.Logger
	cacheDir   string
	provider   exactProvider
	warnedOnce map[string]struct{}
	mu         sync.Mutex
}

// NewManager создаёт менеджер токенов на основе конфигурации агента.
func NewManager(cfg *dsl.Config, log *slog.Logger) *Manager {
	cacheDir := resolveTokenizerCacheDir(cfg)
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		log:        log.With(slog.String("component", "tokens")),
		cacheDir:   cacheDir,
		provider:   newExactProvider(),
		warnedOnce: make(map[string]struct{}),
	}
}

// NewManagerWithProvider создаёт менеджер токенов с явно заданным exact provider, что удобно в тестах.
func NewManagerWithProvider(cacheDir string, provider exactProvider, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	if strings.TrimSpace(cacheDir) == "" {
		cacheDir = resolveTokenizerCacheDir(nil)
	}
	if provider == nil {
		provider = newExactProvider()
	}
	return &Manager{
		log:        log.With(slog.String("component", "tokens")),
		cacheDir:   cacheDir,
		provider:   provider,
		warnedOnce: make(map[string]struct{}),
	}
}

// ResolveModel возвращает профиль модели для дальнейших оценок токенов.
func (m *Manager) ResolveModel(model string, contextWindowOverride *int) ModelProfile {
	return ResolveModelProfile(model, contextWindowOverride)
}

// CountText считает токены для произвольного текстового фрагмента.
func (m *Manager) CountText(model string, contextWindowOverride *int, text string) Estimate {
	profile := m.ResolveModel(model, contextWindowOverride)
	return m.countWithProfile(profile, text)
}

// EstimateMessage считает токены для одного сообщения chat completion.
func (m *Manager) EstimateMessage(model string, contextWindowOverride *int, msg llm.Message) Estimate {
	profile := m.ResolveModel(model, contextWindowOverride)
	data, err := json.Marshal(struct {
		Role         string            `json:"role,omitempty"`
		Content      string            `json:"content,omitempty"`
		Name         string            `json:"name,omitempty"`
		ToolCallID   string            `json:"tool_call_id,omitempty"`
		ToolCalls    []llm.ToolCall    `json:"tool_calls,omitempty"`
		FunctionCall *llm.FunctionCall `json:"function_call,omitempty"`
		Reasoning    string            `json:"reasoning,omitempty"`
	}{
		Role:         msg.Role,
		Content:      msg.Content,
		Name:         msg.Name,
		ToolCallID:   msg.ToolCallID,
		ToolCalls:    msg.ToolCalls,
		FunctionCall: msg.FunctionCall,
		Reasoning:    msg.Reasoning,
	})
	if err != nil {
		return Estimate{
			Tokens:   approxTokenCount(msg.Content),
			Strategy: "approx_bytes_fallback",
			Warning:  fmt.Sprintf("failed to marshal message for token estimation: %v", err),
		}
	}
	return m.countWithProfile(profile, string(data))
}

// EstimateMessages считает токены для массива сообщений chat completion.
func (m *Manager) EstimateMessages(model string, contextWindowOverride *int, messages []llm.Message) Estimate {
	profile := m.ResolveModel(model, contextWindowOverride)
	data, err := json.Marshal(messages)
	if err != nil {
		combined := make([]string, 0, len(messages))
		for _, msg := range messages {
			combined = append(combined, msg.Content)
		}
		return Estimate{
			Tokens:   approxTokenCount(strings.Join(combined, "\n")),
			Strategy: "approx_bytes_fallback",
			Warning:  fmt.Sprintf("failed to marshal messages for token estimation: %v", err),
		}
	}
	return m.countWithProfile(profile, string(data))
}

// EstimateRequest считает токены для запроса к модели с учётом сообщений и tool schema.
func (m *Manager) EstimateRequest(model string, contextWindowOverride *int, req llm.ChatCompletionRequest) Estimate {
	profile := m.ResolveModel(model, contextWindowOverride)
	data, err := json.Marshal(struct {
		Messages []llm.Message `json:"messages,omitempty"`
		Tools    []llm.Tool    `json:"tools,omitempty"`
	}{
		Messages: req.Messages,
		Tools:    req.Tools,
	})
	if err != nil {
		return Estimate{
			Tokens:   m.EstimateMessages(model, contextWindowOverride, req.Messages).Tokens,
			Strategy: "approx_request_fallback",
			Warning:  fmt.Sprintf("failed to marshal request for token estimation: %v", err),
		}
	}
	return m.countWithProfile(profile, string(data))
}

func (m *Manager) countWithProfile(profile ModelProfile, text string) Estimate {
	if strings.TrimSpace(text) == "" {
		return Estimate{Tokens: 0, Exact: true, Strategy: "empty"}
	}
	if profile.HFTokenizerModelID != "" {
		if estimate, err := m.provider.CountText(profile, m.cacheDir, text); err == nil {
			return estimate
		} else {
			m.warnFallback(profile.DisplayName, err)
			return Estimate{
				Tokens:   approxTokenCount(text),
				Exact:    false,
				Strategy: "approx_bytes_fallback",
				Warning:  err.Error(),
			}
		}
	}
	return Estimate{
		Tokens:   approxTokenCount(text),
		Exact:    false,
		Strategy: "approx_bytes_fallback",
		Warning:  fmt.Sprintf("unknown model %q, using byte-based fallback", profile.RequestedModel),
	}
}

func (m *Manager) warnFallback(model string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := strings.TrimSpace(model)
	if _, ok := m.warnedOnce[key]; ok {
		return
	}
	m.warnedOnce[key] = struct{}{}
	m.log.Warn("falling back to approximate token estimation",
		slog.String("model", model),
		slog.String("error", err.Error()),
	)
}

func resolveTokenizerCacheDir(cfg *dsl.Config) string {
	if cfg != nil && strings.TrimSpace(cfg.Agent.TokenizerCacheDir) != "" {
		return strings.TrimSpace(cfg.Agent.TokenizerCacheDir)
	}
	if fromEnv := strings.TrimSpace(os.Getenv("PAI_TOKENIZER_CACHE_DIR")); fromEnv != "" {
		return fromEnv
	}
	if dir, err := os.UserCacheDir(); err == nil && strings.TrimSpace(dir) != "" {
		return filepath.Join(dir, "pipelineai", "tokenizers")
	}
	return filepath.Join(os.TempDir(), "pipelineai-tokenizers")
}

func approxTokenCount(text string) int {
	size := len(text)
	if size == 0 {
		return 0
	}
	return (size + 3) / 4
}
