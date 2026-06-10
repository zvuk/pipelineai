package executor

import (
	"time"

	paimetrics "github.com/zvuk/pipelineai/internal/metrics"
)

type metricsSink interface {
	Step(stepID, stepType, status string, elapsed time.Duration)
	MatrixItem(matrixStep, runStep, itemID, status string, elapsed time.Duration)
	LLM(stepID, status string, requests, promptTokens, completionTokens, totalTokens, toolCalls int)
	ToolCall(stepID, toolName string, ok bool)
	ShellError(stepID, stepType string, exitCode int, timedOut bool)
}

// SetMetrics подключает сборщик метрик к executor.
func (e *Executor) SetMetrics(collector *paimetrics.Collector) {
	if e == nil {
		return
	}
	e.metrics = collector
}

func (e *Executor) recordStepMetric(stepID, stepType, status string, elapsed time.Duration) {
	if e == nil || e.metrics == nil {
		return
	}
	e.metrics.Step(stepID, stepType, status, elapsed)
}

func (e *Executor) recordMatrixItemMetric(matrixStep, runStep, itemID, status string, elapsed time.Duration) {
	if e == nil || e.metrics == nil {
		return
	}
	e.metrics.MatrixItem(matrixStep, runStep, itemID, status, elapsed)
}

func (e *Executor) recordLLMMetric(stepID, status string, tokenMetrics *stepTokenMetrics) {
	if e == nil || e.metrics == nil || tokenMetrics == nil {
		return
	}
	usage := tokenMetrics.CumulativeUsage
	e.metrics.LLM(
		stepID,
		status,
		tokenMetrics.Requests,
		usage.PromptTokens,
		usage.CompletionTokens,
		usage.TotalTokens,
		tokenMetrics.ToolCalls,
	)
}

func (e *Executor) recordToolMetric(stepID, toolName string, ok bool) {
	if e == nil || e.metrics == nil {
		return
	}
	e.metrics.ToolCall(stepID, toolName, ok)
}

func (e *Executor) recordShellErrorMetric(stepID, stepType string, exitCode int, timedOut bool) {
	if e == nil || e.metrics == nil {
		return
	}
	e.metrics.ShellError(stepID, stepType, exitCode, timedOut)
}
