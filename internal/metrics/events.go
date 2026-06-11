package metrics

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// RunStart records immutable run metadata.
func (c *Collector) RunStart(agent, model, configPath, executeStep, artifactDir string, started time.Time) {
	c.AddCommonLabel("agent", agent)
	c.AddCommonLabel("model", model)
	c.AddCommonLabel("config", configPath)
	c.AddCommonLabel("execute_step", valueOrAll(executeStep))
	c.AddCommonLabel("artifact_dir", artifactDir)
	c.Observe("pipelineai_run_info", "PipelineAI run metadata marker.", typeGauge, 1, nil)
	c.Observe("pipelineai_run_started_timestamp_seconds", "PipelineAI run start Unix timestamp.", typeGauge, float64(started.Unix()), nil)
}

// RunFinish records final run status.
func (c *Collector) RunFinish(started time.Time, err error) {
	status := "success"
	errorType := "none"
	if err != nil {
		status = "error"
		errorType = ClassifyError(err)
	}
	finished := time.Now()
	c.AddCommonLabel("status", status)
	c.AddCommonLabel("error_type", errorType)
	c.Observe("pipelineai_run_finished_timestamp_seconds", "PipelineAI run finish Unix timestamp.", typeGauge, float64(finished.Unix()), nil)
	c.Observe("pipelineai_run_duration_seconds", "PipelineAI run duration in seconds.", typeGauge, finished.Sub(started).Seconds(), map[string]string{
		"status": status,
	})
	c.Observe("pipelineai_run_total", "PipelineAI run count as one sample per exported run.", typeCounter, 1, map[string]string{
		"status": status,
	})
	if err != nil {
		c.Observe("pipelineai_errors_total", "PipelineAI errors as one sample per exported run.", typeCounter, 1, map[string]string{
			"error_type": errorType,
		})
	}
}

// Step records one executed step.
func (c *Collector) Step(stepID, stepType, status string, elapsed time.Duration) {
	c.Observe("pipelineai_step_total", "PipelineAI executed steps as one sample per exported run.", typeCounter, 1, map[string]string{
		"step":   stepID,
		"type":   stepType,
		"status": status,
	})
	c.Observe("pipelineai_step_duration_seconds", "PipelineAI step duration in seconds.", typeGauge, elapsed.Seconds(), map[string]string{
		"step":   stepID,
		"type":   stepType,
		"status": status,
	})
}

// MatrixItem records one matrix item result.
func (c *Collector) MatrixItem(matrixStep, runStep, itemID, status string, elapsed time.Duration) {
	c.Observe("pipelineai_matrix_items_total", "PipelineAI matrix items as one sample per exported run.", typeCounter, 1, map[string]string{
		"step":     matrixStep,
		"run_step": runStep,
		"item_id":  itemID,
		"status":   status,
	})
	c.Observe("pipelineai_matrix_item_duration_seconds", "PipelineAI matrix item duration in seconds.", typeGauge, elapsed.Seconds(), map[string]string{
		"step":     matrixStep,
		"run_step": runStep,
		"item_id":  itemID,
		"status":   status,
	})
}

// LLM records aggregate LLM usage for a step.
func (c *Collector) LLM(stepID, status string, requests, promptTokens, completionTokens, totalTokens, toolCalls int) {
	c.Observe("pipelineai_llm_requests_total", "PipelineAI LLM requests as one sample per exported run.", typeCounter, float64(nonNegative(requests)), map[string]string{
		"step":   stepID,
		"status": status,
	})
	c.Observe("pipelineai_llm_tokens_total", "PipelineAI LLM token usage as one sample per exported run.", typeCounter, float64(nonNegative(promptTokens)), map[string]string{
		"step": stepID,
		"kind": "prompt",
	})
	c.Observe("pipelineai_llm_tokens_total", "PipelineAI LLM token usage as one sample per exported run.", typeCounter, float64(nonNegative(completionTokens)), map[string]string{
		"step": stepID,
		"kind": "completion",
	})
	c.Observe("pipelineai_llm_tokens_total", "PipelineAI LLM token usage as one sample per exported run.", typeCounter, float64(nonNegative(totalTokens)), map[string]string{
		"step": stepID,
		"kind": "total",
	})
	c.Observe("pipelineai_llm_tool_calls_total", "PipelineAI LLM requested tool calls as one sample per exported run.", typeCounter, float64(nonNegative(toolCalls)), map[string]string{
		"step": stepID,
	})
}

// ToolCall records one executed tool call.
func (c *Collector) ToolCall(stepID, toolName string, ok bool) {
	status := "success"
	if !ok {
		status = "error"
	}
	c.Observe("pipelineai_tool_calls_total", "PipelineAI tool calls as one sample per exported run.", typeCounter, 1, map[string]string{
		"step":   stepID,
		"tool":   toolName,
		"status": status,
	})
}

// ShellError records a failed shell or plan step.
func (c *Collector) ShellError(stepID, stepType string, exitCode int, timedOut bool) {
	c.Observe("pipelineai_shell_errors_total", "PipelineAI shell or plan step errors as one sample per exported run.", typeCounter, 1, map[string]string{
		"step":      stepID,
		"type":      stepType,
		"exit_code": intLabel(exitCode),
		"timed_out": boolLabel(timedOut),
	})
}

// ClassifyError returns a low-cardinality error type suitable for metrics labels.
func ClassifyError(err error) string {
	if err == nil {
		return "none"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "llm"):
		return "llm"
	case strings.Contains(text, "shell step"), strings.Contains(text, "exit_code="):
		return "shell"
	case strings.Contains(text, "validation"), strings.Contains(text, "validator"):
		return "validation"
	case strings.Contains(text, "config"):
		return "config"
	default:
		return "runtime"
	}
}

func valueOrAll(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "all"
	}
	return value
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func intLabel(value int) string {
	return strconv.Itoa(value)
}

func boolLabel(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
