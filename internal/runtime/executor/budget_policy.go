package executor

import (
	"fmt"
	"strings"

	"github.com/zvuk/pipelineai/internal/runtime/llm"
	"github.com/zvuk/pipelineai/pkg/dsl"
)

func (e *Executor) enforceStepBudgetBeforeRequest(step *dsl.Step, req llm.ChatCompletionRequest, tracker *promptTokenTracker, metrics *stepTokenMetrics) error {
	if err := enforceLLMLimits(step, metrics); err != nil {
		if metrics != nil {
			metrics.recordBudgetExceeded(err.Error())
		}
		return err
	}
	if tracker == nil || metrics == nil {
		return nil
	}
	fitLimit := requestFitLimit(tracker.ContextWindow(), metrics.ResponseReserveTokens)
	estimate := tracker.EstimatedNextPromptTokens()
	if fitLimit > 0 && estimate > fitLimit {
		err := fmt.Errorf(
			"executor: estimated prompt %d tokens exceeds fit limit %d for model context window %d (response reserve %d)",
			estimate,
			fitLimit,
			tracker.ContextWindow(),
			metrics.ResponseReserveTokens,
		)
		metrics.recordBudgetExceeded(err.Error())
		return err
	}
	if req.Model == "" {
		err := fmt.Errorf("executor: request model must not be empty")
		metrics.recordBudgetExceeded(err.Error())
		return err
	}
	return nil
}

func enforceLLMLimits(step *dsl.Step, metrics *stepTokenMetrics) error {
	if step == nil || step.LLM == nil || metrics == nil {
		return nil
	}
	if limit := positiveInt(step.LLM.MaxRequests); limit > 0 && metrics.Requests >= limit {
		return handleBudgetLimit(metrics, fmt.Sprintf("executor: llm request limit reached for step %s: %d/%d", step.ID, metrics.Requests, limit))
	}
	if limit := positiveInt(step.LLM.MaxCumulativePromptTokens); limit > 0 && metrics.CumulativeUsage.PromptTokens >= limit {
		return handleBudgetLimit(metrics, fmt.Sprintf("executor: cumulative prompt token limit reached for step %s: %d/%d", step.ID, metrics.CumulativeUsage.PromptTokens, limit))
	}
	if limit := positiveInt(step.LLM.MaxCumulativeTotalTokens); limit > 0 && metrics.CumulativeUsage.TotalTokens >= limit {
		return handleBudgetLimit(metrics, fmt.Sprintf("executor: cumulative total token limit reached for step %s: %d/%d", step.ID, metrics.CumulativeUsage.TotalTokens, limit))
	}
	if limit := positiveInt(step.LLM.MaxCumulativeToolTokens); limit > 0 && metrics.CumulativeToolMessageTokens >= limit {
		return handleBudgetLimit(metrics, fmt.Sprintf("executor: cumulative tool transcript token limit reached for step %s: %d/%d", step.ID, metrics.CumulativeToolMessageTokens, limit))
	}
	return nil
}

func enforceToolCallLimit(step *dsl.Step, metrics *stepTokenMetrics) error {
	if step == nil || step.LLM == nil || metrics == nil {
		return nil
	}
	if limit := positiveInt(step.LLM.MaxToolCalls); limit > 0 && metrics.ToolCalls >= limit {
		return handleBudgetLimit(metrics, fmt.Sprintf("executor: tool call limit reached for step %s: %d/%d", step.ID, metrics.ToolCalls, limit))
	}
	return nil
}

func handleBudgetLimit(metrics *stepTokenMetrics, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil
	}
	if metrics == nil {
		return fmt.Errorf("%s", reason)
	}
	switch metrics.BudgetMode {
	case budgetModeWarn, budgetModeContinueWithCompaction:
		metrics.recordBudgetWarning(reason)
		return nil
	default:
		return fmt.Errorf("%s", reason)
	}
}

func requestFitLimit(contextWindow, reserve int) int {
	if contextWindow <= 0 {
		return 0
	}
	if reserve < 0 {
		reserve = 0
	}
	limit := contextWindow - reserve
	if limit <= 0 {
		return contextWindow - 1
	}
	return limit
}

func positiveInt(value *int) int {
	if value == nil || *value <= 0 {
		return 0
	}
	return *value
}

func normalizedValidatorName(step *dsl.Step) string {
	if step == nil || step.LLM == nil {
		return ""
	}
	return strings.TrimSpace(strings.ToLower(step.LLM.ResponseValidator))
}
