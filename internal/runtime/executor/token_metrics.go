package executor

import (
	"strings"

	"github.com/zvuk/pipelineai/internal/runtime/llm"
	"github.com/zvuk/pipelineai/internal/runtime/tokens"
	"github.com/zvuk/pipelineai/pkg/dsl"
)

type stepTokenMetrics struct {
	Requests                    int       `json:"requests"`
	Compactions                 int       `json:"compactions"`
	ToolCalls                   int       `json:"tool_calls"`
	ToolWarnings                int       `json:"tool_warnings"`
	ToolHardCapSuppressions     int       `json:"tool_hard_cap_suppressions"`
	ModelContextWindow          int       `json:"model_context_window"`
	AutoCompactThreshold        int       `json:"auto_compact_threshold_tokens"`
	CompactTargetTokens         int       `json:"compact_target_tokens"`
	ToolWarnThreshold           int       `json:"tool_warn_threshold_tokens"`
	ToolHardCapThreshold        int       `json:"tool_hard_cap_threshold_tokens"`
	ResponseReserveTokens       int       `json:"response_reserve_tokens"`
	LastPromptTokens            int       `json:"last_prompt_tokens"`
	EstimatedNextPromptTokens   int       `json:"estimated_next_prompt_tokens"`
	LastEstimateExact           bool      `json:"last_estimate_exact"`
	LastEstimateStrategy        string    `json:"last_estimate_strategy,omitempty"`
	LastEstimateWarning         string    `json:"last_estimate_warning,omitempty"`
	MaxObservedPromptDrift      int       `json:"max_observed_prompt_drift,omitempty"`
	CumulativeToolMessageTokens int       `json:"cumulative_tool_message_tokens"`
	LargestMessageTokens        int       `json:"largest_message_tokens,omitempty"`
	LargestMessageRole          string    `json:"largest_message_role,omitempty"`
	LargestMessageOrdinal       int       `json:"largest_message_ordinal,omitempty"`
	BudgetExceededReason        string    `json:"budget_exceeded_reason,omitempty"`
	CumulativeUsage             llm.Usage `json:"cumulative_usage"`
	FinalResponseUsage          llm.Usage `json:"final_response_usage"`
}

func newStepTokenMetrics(cfg *dsl.Config, profile tokens.ModelProfile) *stepTokenMetrics {
	window := profile.ContextWindow
	return &stepTokenMetrics{
		ModelContextWindow:    window,
		AutoCompactThreshold:  thresholdTokens(window, autoCompactPercent(cfg)),
		CompactTargetTokens:   thresholdTokens(window, compactTargetPercent(cfg)),
		ToolWarnThreshold:     thresholdTokens(window, toolWarnPercent(cfg)),
		ToolHardCapThreshold:  thresholdTokens(window, toolHardCapPercent(cfg)),
		ResponseReserveTokens: responseReserveTokens(cfg),
	}
}

func (m *stepTokenMetrics) recordResponse(resp llm.ChatCompletionResponse, predictedPromptTokens int) {
	m.Requests++
	m.recordUsage(resp.Usage)
	if predictedPromptTokens > 0 && resp.Usage.PromptTokens > 0 {
		drift := predictedPromptTokens - resp.Usage.PromptTokens
		if drift < 0 {
			drift = -drift
		}
		if drift > m.MaxObservedPromptDrift {
			m.MaxObservedPromptDrift = drift
		}
	}
}

func (m *stepTokenMetrics) recordUsage(usage llm.Usage) {
	m.CumulativeUsage.PromptTokens += usage.PromptTokens
	m.CumulativeUsage.CompletionTokens += usage.CompletionTokens
	m.CumulativeUsage.TotalTokens += usage.TotalTokens
	m.FinalResponseUsage = usage
	if usage.PromptTokens > 0 {
		m.LastPromptTokens = usage.PromptTokens
	}
}

func (m *stepTokenMetrics) recordToolMessageTokens(tokens int) {
	if tokens > 0 {
		m.CumulativeToolMessageTokens += tokens
	}
}

func (m *stepTokenMetrics) recordBudgetExceeded(reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return
	}
	m.BudgetExceededReason = reason
}

func (m *stepTokenMetrics) syncTracker(tracker *promptTokenTracker) {
	if tracker == nil {
		return
	}
	m.EstimatedNextPromptTokens = tracker.EstimatedNextPromptTokens()
	if tracker.lastPromptTokens > 0 {
		m.LastPromptTokens = tracker.lastPromptTokens
	}
	m.LastEstimateExact = tracker.EstimateExact()
	m.LastEstimateStrategy = tracker.EstimateStrategy()
	m.LastEstimateWarning = tracker.EstimateWarning()
	m.LargestMessageTokens = tracker.LargestMessageTokens()
	m.LargestMessageRole = tracker.LargestMessageRole()
	m.LargestMessageOrdinal = tracker.LargestMessageOrdinal()
}

type promptTokenTracker struct {
	counter               tokens.Counter
	profile               tokens.ModelProfile
	baseEstimate          tokens.Estimate
	lastPromptTokens      int
	rawDeltaTokens        int
	deltaExact            bool
	deltaStrategies       []string
	deltaWarnings         []string
	largestMessageTokens  int
	largestMessageRole    string
	largestMessageOrdinal int
	nextMessageOrdinal    int
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
	t.baseEstimate = t.counter.EstimateRequest(t.profile.RequestedModel, &t.profile.ContextWindow, req)
	t.lastPromptTokens = 0
	t.rawDeltaTokens = 0
	t.deltaExact = true
	t.deltaStrategies = nil
	t.deltaWarnings = nil
	t.largestMessageTokens = 0
	t.largestMessageRole = ""
	t.largestMessageOrdinal = 0
	t.nextMessageOrdinal = 0
	for _, msg := range req.Messages {
		estimate := t.counter.EstimateMessage(t.profile.RequestedModel, &t.profile.ContextWindow, msg)
		t.observeMessageEstimate(msg, estimate)
	}
}

func (t *promptTokenTracker) UpdateFromResponse(resp llm.ChatCompletionResponse, req llm.ChatCompletionRequest) {
	if resp.Usage.PromptTokens > 0 {
		t.baseEstimate = tokens.Estimate{
			Tokens:   resp.Usage.PromptTokens,
			Exact:    true,
			Strategy: "provider_usage",
		}
		t.lastPromptTokens = resp.Usage.PromptTokens
		t.rawDeltaTokens = 0
		t.deltaExact = true
		t.deltaStrategies = nil
		t.deltaWarnings = nil
		return
	}
	t.ResetToRequest(req)
}

func (t *promptTokenTracker) AppendMessage(msg llm.Message) int {
	estimate := t.counter.EstimateMessage(t.profile.RequestedModel, &t.profile.ContextWindow, msg)
	t.rawDeltaTokens += estimate.Tokens
	if !estimate.Exact {
		t.deltaExact = false
	}
	if txt := strings.TrimSpace(estimate.Strategy); txt != "" {
		t.deltaStrategies = appendUniqueString(t.deltaStrategies, txt)
	}
	if txt := strings.TrimSpace(estimate.Warning); txt != "" {
		t.deltaWarnings = appendUniqueString(t.deltaWarnings, txt)
	}
	t.observeMessageEstimate(msg, estimate)
	return estimate.Tokens
}

func (t *promptTokenTracker) EstimatedNextPromptTokens() int {
	base := t.baseEstimate.Tokens
	if strings.TrimSpace(t.baseEstimate.Strategy) != "provider_usage" {
		base = withSafetyMargin(base)
	}
	return base + withSafetyMargin(t.rawDeltaTokens)
}

func (t *promptTokenTracker) ContextWindow() int {
	return t.profile.ContextWindow
}

func (t *promptTokenTracker) EstimateExact() bool {
	if !t.baseEstimate.Exact {
		return false
	}
	return t.deltaExact
}

func (t *promptTokenTracker) EstimateStrategy() string {
	parts := make([]string, 0, 1+len(t.deltaStrategies))
	if txt := strings.TrimSpace(t.baseEstimate.Strategy); txt != "" {
		parts = append(parts, txt)
	}
	parts = appendUniqueStrings(parts, t.deltaStrategies)
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	default:
		return strings.Join(parts, "+")
	}
}

func (t *promptTokenTracker) EstimateWarning() string {
	parts := make([]string, 0, 1+len(t.deltaWarnings))
	if txt := strings.TrimSpace(t.baseEstimate.Warning); txt != "" {
		parts = append(parts, txt)
	}
	parts = appendUniqueStrings(parts, t.deltaWarnings)
	return strings.Join(parts, "; ")
}

func (t *promptTokenTracker) LargestMessageTokens() int {
	return t.largestMessageTokens
}

func (t *promptTokenTracker) LargestMessageRole() string {
	return t.largestMessageRole
}

func (t *promptTokenTracker) LargestMessageOrdinal() int {
	return t.largestMessageOrdinal
}

func (t *promptTokenTracker) observeMessageEstimate(msg llm.Message, estimate tokens.Estimate) {
	t.nextMessageOrdinal++
	if estimate.Tokens <= t.largestMessageTokens {
		return
	}
	t.largestMessageTokens = estimate.Tokens
	t.largestMessageRole = msg.Role
	t.largestMessageOrdinal = t.nextMessageOrdinal
}

func toolWarnPercent(cfg *dsl.Config) int {
	if cfg != nil && cfg.Agent.ToolOutputWarnPercent != nil && *cfg.Agent.ToolOutputWarnPercent > 0 {
		return *cfg.Agent.ToolOutputWarnPercent
	}
	return tokens.DefaultToolWarnPercent
}

func toolHardCapPercent(cfg *dsl.Config) int {
	if cfg != nil && cfg.Agent.ToolOutputHardCapPercent != nil && *cfg.Agent.ToolOutputHardCapPercent > 0 {
		return *cfg.Agent.ToolOutputHardCapPercent
	}
	return tokens.DefaultToolHardCapPercent
}

func autoCompactPercent(cfg *dsl.Config) int {
	if cfg != nil && cfg.Agent.AutoCompactPercent != nil && *cfg.Agent.AutoCompactPercent > 0 {
		return *cfg.Agent.AutoCompactPercent
	}
	return tokens.DefaultAutoCompactPercent
}

func compactTargetPercent(cfg *dsl.Config) int {
	if cfg != nil && cfg.Agent.CompactTargetPercent != nil && *cfg.Agent.CompactTargetPercent > 0 {
		return *cfg.Agent.CompactTargetPercent
	}
	return tokens.DefaultCompactTargetPercent
}

func responseReserveTokens(cfg *dsl.Config) int {
	if cfg != nil && cfg.Agent.ResponseReserveTokens != nil && *cfg.Agent.ResponseReserveTokens > 0 {
		return *cfg.Agent.ResponseReserveTokens
	}
	return tokens.DefaultResponseReserveTokens
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

func appendUniqueStrings(dst []string, src []string) []string {
	for _, item := range src {
		dst = appendUniqueString(dst, item)
	}
	return dst
}

func appendUniqueString(dst []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return dst
	}
	for _, item := range dst {
		if item == value {
			return dst
		}
	}
	return append(dst, value)
}
