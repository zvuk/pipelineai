package executor

import (
	"github.com/zvuk/pipelineai/internal/runtime/llm"
	"github.com/zvuk/pipelineai/internal/runtime/tokens"
	"github.com/zvuk/pipelineai/pkg/dsl"
)

type stepTokenMetrics struct {
	Requests                  int       `json:"requests"`
	Compactions               int       `json:"compactions"`
	ToolWarnings              int       `json:"tool_warnings"`
	ModelContextWindow        int       `json:"model_context_window"`
	AutoCompactThreshold      int       `json:"auto_compact_threshold_tokens"`
	ToolWarnThreshold         int       `json:"tool_warn_threshold_tokens"`
	LastPromptTokens          int       `json:"last_prompt_tokens"`
	EstimatedNextPromptTokens int       `json:"estimated_next_prompt_tokens"`
	CumulativeUsage           llm.Usage `json:"cumulative_usage"`
	FinalResponseUsage        llm.Usage `json:"final_response_usage"`
}

func newStepTokenMetrics(cfg *dsl.Config, profile tokens.ModelProfile) *stepTokenMetrics {
	window := profile.ContextWindow
	return &stepTokenMetrics{
		ModelContextWindow:   window,
		AutoCompactThreshold: thresholdTokens(window, autoCompactPercent(cfg)),
		ToolWarnThreshold:    thresholdTokens(window, toolWarnPercent(cfg)),
	}
}

func (m *stepTokenMetrics) recordResponse(resp llm.ChatCompletionResponse) {
	m.Requests++
	m.CumulativeUsage.PromptTokens += resp.Usage.PromptTokens
	m.CumulativeUsage.CompletionTokens += resp.Usage.CompletionTokens
	m.CumulativeUsage.TotalTokens += resp.Usage.TotalTokens
	m.FinalResponseUsage = resp.Usage
	if resp.Usage.PromptTokens > 0 {
		m.LastPromptTokens = resp.Usage.PromptTokens
	}
}

func (m *stepTokenMetrics) syncTracker(tracker *promptTokenTracker) {
	if tracker == nil {
		return
	}
	m.EstimatedNextPromptTokens = tracker.EstimatedNextPromptTokens()
	if tracker.lastPromptTokens > 0 {
		m.LastPromptTokens = tracker.lastPromptTokens
	}
}

type promptTokenTracker struct {
	counter          tokens.Counter
	profile          tokens.ModelProfile
	baseEstimate     int
	lastPromptTokens int
	hasPromptUsage   bool
	rawDeltaTokens   int
}

func newPromptTokenTracker(counter tokens.Counter, profile tokens.ModelProfile, req llm.ChatCompletionRequest) *promptTokenTracker {
	t := &promptTokenTracker{
		counter: counter,
		profile: profile,
	}
	t.ResetToRequest(req)
	return t
}

func (t *promptTokenTracker) ResetToRequest(req llm.ChatCompletionRequest) {
	estimate := t.counter.EstimateRequest(t.profile.RequestedModel, &t.profile.ContextWindow, req)
	t.baseEstimate = withSafetyMargin(estimate.Tokens)
	t.lastPromptTokens = 0
	t.hasPromptUsage = false
	t.rawDeltaTokens = 0
}

func (t *promptTokenTracker) UpdateFromResponse(resp llm.ChatCompletionResponse, req llm.ChatCompletionRequest) {
	t.rawDeltaTokens = 0
	if resp.Usage.PromptTokens > 0 {
		t.lastPromptTokens = resp.Usage.PromptTokens
		t.hasPromptUsage = true
		return
	}
	t.ResetToRequest(req)
}

func (t *promptTokenTracker) AppendMessage(msg llm.Message) {
	estimate := t.counter.EstimateMessage(t.profile.RequestedModel, &t.profile.ContextWindow, msg)
	t.rawDeltaTokens += estimate.Tokens
}

func (t *promptTokenTracker) EstimatedNextPromptTokens() int {
	base := t.baseEstimate
	if t.hasPromptUsage {
		base = t.lastPromptTokens
	}
	return base + withSafetyMargin(t.rawDeltaTokens)
}

func (t *promptTokenTracker) ContextWindow() int {
	return t.profile.ContextWindow
}

func toolWarnPercent(cfg *dsl.Config) int {
	if cfg != nil && cfg.Agent.ToolOutputWarnPercent != nil && *cfg.Agent.ToolOutputWarnPercent > 0 {
		return *cfg.Agent.ToolOutputWarnPercent
	}
	return tokens.DefaultToolWarnPercent
}

func autoCompactPercent(cfg *dsl.Config) int {
	if cfg != nil && cfg.Agent.AutoCompactPercent != nil && *cfg.Agent.AutoCompactPercent > 0 {
		return *cfg.Agent.AutoCompactPercent
	}
	return tokens.DefaultAutoCompactPercent
}

func thresholdTokens(contextWindow, percent int) int {
	if contextWindow <= 0 || percent <= 0 {
		return 0
	}
	return (contextWindow * percent) / 100
}

func withSafetyMargin(value int) int {
	if value <= 0 {
		return 0
	}
	return (value*(100+tokens.DefaultSafetyMarginPercent) + 99) / 100
}
