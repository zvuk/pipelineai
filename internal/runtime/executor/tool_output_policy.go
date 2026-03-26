package executor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zvuk/pipelineai/internal/runtime/llm"
	"github.com/zvuk/pipelineai/internal/tools/registry"
	"github.com/zvuk/pipelineai/pkg/dsl"
)

func sanitizeToolCallForExecution(tc llm.ToolCall) (llm.ToolCall, bool, string) {
	sanitized := tc
	args := strings.TrimSpace(tc.Function.Arguments)
	if args == "" {
		return sanitized, false, tc.Function.Name + ":"
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(args), &payload); err != nil {
		return sanitized, false, strings.TrimSpace(tc.Function.Name) + ":" + args
	}

	force := false
	if raw, ok := payload["force_full_output"].(bool); ok && raw {
		force = true
	}
	delete(payload, "force_full_output")

	if strings.TrimSpace(tc.Function.Name) == "shell" || strings.TrimSpace(tc.Function.Name) == "apply_patch" {
		if raw, ok := payload["force"].(bool); ok && raw {
			force = true
		}
		delete(payload, "force")
	}

	if encoded, err := json.Marshal(payload); err == nil {
		sanitized.Function.Arguments = string(encoded)
		return sanitized, force, strings.TrimSpace(tc.Function.Name) + ":" + sanitized.Function.Arguments
	}

	return sanitized, force, strings.TrimSpace(tc.Function.Name) + ":" + args
}

func (e *Executor) applyToolOutputPolicy(
	stepID string,
	step *dsl.Step,
	tc llm.ToolCall,
	out registry.ExecResult,
	forceFullOutput bool,
	tracker *promptTokenTracker,
	metrics *stepTokenMetrics,
) registry.ExecResult {
	defer cleanupTransientCaptureFiles(&out)

	if metrics == nil || tracker == nil {
		return out
	}

	fullMessageTokens, err := estimateToolMessageTokens(tracker, out)
	if err != nil {
		return out
	}

	mode := resolveToolResultMode(e.cfg, step)
	previewTokens := resolveToolResultPreviewTokens(e.cfg, step)
	fitLimit := requestFitLimit(tracker.ContextWindow(), metrics.ResponseReserveTokens)
	projectedPromptTokens := tracker.EstimatedNextPromptTokens() + withSafetyMargin(fullMessageTokens)
	warnThresholdExceeded := metrics.ToolWarnThreshold > 0 && fullMessageTokens >= metrics.ToolWarnThreshold
	hardCapExceeded := metrics.ToolHardCapThreshold > 0 && fullMessageTokens >= metrics.ToolHardCapThreshold
	fitLimitExceeded := fitLimit > 0 && projectedPromptTokens > fitLimit
	outputTruncated := out.StdoutTruncated || out.StderrTruncated

	persistMode := mode == toolResultModePersistOnOverflow || mode == toolResultModePersistAlways
	captureRef := ""
	captureKind := ""
	if mode == toolResultModePersistAlways || (persistMode && (warnThresholdExceeded || hardCapExceeded || fitLimitExceeded || outputTruncated)) {
		captureRef, captureKind = e.persistToolCapture(stepID, tc.Function.Name, &out)
		if captureRef != "" {
			metrics.recordToolCapture(captureRef)
		}
	}

	if !warnThresholdExceeded && !hardCapExceeded && !fitLimitExceeded && !outputTruncated {
		if mode == toolResultModePersistAlways && captureRef != "" {
			out.CaptureRef = captureRef
			out.ArtifactPath = captureRef
			out.CaptureKind = captureKind
			out.CapturePersisted = true
			out.ResultMode = mode
			out.SuggestedReads = suggestedReadsForCapture(captureRef, captureKind)
		}
		return out
	}

	if forceFullOutput && !hardCapExceeded && !fitLimitExceeded && !outputTruncated {
		if captureRef != "" {
			out.CaptureRef = captureRef
			out.ArtifactPath = captureRef
			out.CaptureKind = captureKind
			out.CapturePersisted = true
			out.SuggestedReads = suggestedReadsForCapture(captureRef, captureKind)
		}
		out.ResultMode = mode
		return out
	}

	reason := toolSuppressionReason(mode, warnThresholdExceeded, hardCapExceeded, fitLimitExceeded, outputTruncated, captureRef != "")
	if warnThresholdExceeded {
		metrics.ToolWarnings++
	}
	if hardCapExceeded {
		metrics.ToolHardCapSuppressions++
	}

	return buildSuppressedToolResult(
		out,
		reason,
		mode,
		fullMessageTokens,
		thresholdForDecision(metrics, hardCapExceeded, fitLimitExceeded),
		hardCapExceeded || fitLimitExceeded,
		captureRef,
		captureKind,
		previewTokens,
	)
}

func estimateToolMessageTokens(tracker *promptTokenTracker, out registry.ExecResult) (int, error) {
	payload, err := json.Marshal(out)
	if err != nil {
		return 0, err
	}
	estimate := tracker.counter.EstimateMessage(
		tracker.profile.RequestedModel,
		&tracker.profile.ContextWindow,
		llm.Message{Role: llm.RoleTool, Content: string(payload)},
	)
	return estimate.Tokens, nil
}

func toolSuppressionReason(mode string, warnThresholdExceeded bool, hardCapExceeded bool, fitLimitExceeded bool, outputTruncated bool, hasCapture bool) string {
	switch {
	case hardCapExceeded:
		if hasCapture {
			return "Tool output was moved out of the dialog because it exceeded the hard context cap. Read capture_ref in narrow chunks."
		}
		return "Tool output was removed from the dialog because it exceeded the hard context cap."
	case fitLimitExceeded:
		if hasCapture {
			return "Tool output was moved out of the dialog because adding it would overflow the current model context window. Read capture_ref in narrow chunks."
		}
		return "Tool output was removed from the dialog because adding it would overflow the current model context window."
	case outputTruncated:
		if hasCapture {
			return "Tool output exceeded the in-memory capture budget, so only a bounded preview is shown. Read capture_ref in narrow chunks."
		}
		return "Tool output exceeded the in-memory capture budget, so only a bounded preview is shown."
	case mode == toolResultModeTruncate:
		return "Tool output was truncated according to the current tool_result_mode. Repeat with force_full_output=true only if the full payload must stay in context."
	default:
		if hasCapture {
			return "Tool output is large, so a compact preview is shown instead of the full payload. Read capture_ref in narrow chunks or repeat with force_full_output=true only if the full payload must stay in context."
		}
		return "Tool output is large, so a compact preview is shown instead of the full payload. Repeat with force_full_output=true only if the full payload must stay in context."
	}
}

func thresholdForDecision(metrics *stepTokenMetrics, hardCapExceeded bool, fitLimitExceeded bool) int {
	if metrics == nil {
		return 0
	}
	switch {
	case hardCapExceeded:
		return metrics.ToolHardCapThreshold
	case fitLimitExceeded:
		return requestFitLimit(metrics.ModelContextWindow, metrics.ResponseReserveTokens)
	default:
		return metrics.ToolWarnThreshold
	}
}

func buildSuppressedToolResult(
	out registry.ExecResult,
	reason string,
	mode string,
	estimatedTokens int,
	thresholdTokens int,
	hardSuppressed bool,
	captureRef string,
	captureKind string,
	previewTokens int,
) registry.ExecResult {
	previewChars := previewTokens * 4
	if previewChars < 240 {
		previewChars = 240
	}
	preview := buildToolResultPreview(out, previewChars)
	return registry.ExecResult{
		Tool:             out.Tool,
		Ok:               out.Ok,
		Stdout:           "",
		Stderr:           "",
		ExitCode:         out.ExitCode,
		StdoutBytes:      out.StdoutBytes,
		StderrBytes:      out.StderrBytes,
		StdoutLines:      out.StdoutLines,
		StderrLines:      out.StderrLines,
		StdoutTruncated:  out.StdoutTruncated,
		StderrTruncated:  out.StderrTruncated,
		Summary:          out.Summary,
		Added:            out.Added,
		Modified:         out.Modified,
		Deleted:          out.Deleted,
		ElapsedMs:        out.ElapsedMs,
		NewWorkdir:       out.NewWorkdir,
		ToolError:        out.ToolError,
		Warning:          strings.TrimSpace(reason),
		Suppressed:       true,
		HardSuppressed:   hardSuppressed,
		EstimatedTokens:  estimatedTokens,
		ThresholdTokens:  thresholdTokens,
		Preview:          preview,
		CaptureRef:       captureRef,
		ArtifactPath:     captureRef,
		CaptureKind:      captureKind,
		CapturePersisted: captureRef != "",
		SuggestedReads:   suggestedReadsForCapture(captureRef, captureKind),
		ResultMode:       mode,
	}
}

func buildToolResultPreview(out registry.ExecResult, maxChars int) string {
	if maxChars <= 0 {
		maxChars = 400
	}
	sectionBudget := maxChars / 3
	if sectionBudget < 120 {
		sectionBudget = maxChars
	}

	var parts []string
	if txt := strings.TrimSpace(out.Summary); txt != "" {
		parts = append(parts, "summary: "+truncateMiddle(txt, sectionBudget))
	}
	if txt := strings.TrimSpace(out.ToolError); txt != "" {
		parts = append(parts, "tool_error: "+truncateMiddle(txt, sectionBudget))
	}
	if txt := strings.TrimSpace(out.Stdout); txt != "" {
		parts = append(parts, fmt.Sprintf(
			"stdout_preview: %s\nstdout_meta: bytes=%d lines=%d truncated=%t",
			truncateMiddle(txt, sectionBudget),
			out.StdoutBytes,
			out.StdoutLines,
			out.StdoutTruncated,
		))
	}
	if txt := strings.TrimSpace(out.Stderr); txt != "" {
		parts = append(parts, fmt.Sprintf(
			"stderr_preview: %s\nstderr_meta: bytes=%d lines=%d truncated=%t",
			truncateMiddle(txt, sectionBudget),
			out.StderrBytes,
			out.StderrLines,
			out.StderrTruncated,
		))
	}
	if len(parts) == 0 {
		return truncateMiddle(strings.TrimSpace(mustJSON(out)), maxChars)
	}
	return truncateMiddle(strings.TrimSpace(strings.Join(parts, "\n")), maxChars)
}

func truncateMiddle(text string, maxChars int) string {
	text = strings.TrimSpace(text)
	if maxChars <= 0 || len(text) <= maxChars {
		return text
	}
	if maxChars < 32 {
		return text[:maxChars]
	}
	head := (maxChars - 7) / 2
	tail := maxChars - 7 - head
	return text[:head] + "\n...\n" + text[len(text)-tail:]
}

func suggestedReadsForCapture(captureRef string, captureKind string) []string {
	captureRef = strings.TrimSpace(captureRef)
	if captureRef == "" {
		return nil
	}
	switch captureKind {
	case "shell_bundle":
		stdoutPath := filepath.Join(captureRef, "stdout.txt")
		stderrPath := filepath.Join(captureRef, "stderr.txt")
		return []string{
			fmt.Sprintf("head -200 %s", stdoutPath),
			fmt.Sprintf("tail -200 %s", stdoutPath),
			fmt.Sprintf("rg \"pattern\" %s", stdoutPath),
			fmt.Sprintf("sed -n 'START,ENDp' %s", stdoutPath),
			fmt.Sprintf("head -120 %s", stderrPath),
		}
	case "bundle":
		resultPath := filepath.Join(captureRef, "result.json")
		return []string{
			fmt.Sprintf("jq '.' %s", resultPath),
			fmt.Sprintf("rg \"pattern\" %s", resultPath),
		}
	default:
		return []string{
			fmt.Sprintf("jq '.' %s", captureRef),
			fmt.Sprintf("rg \"pattern\" %s", captureRef),
		}
	}
}

func (e *Executor) persistToolCapture(stepID string, toolName string, out *registry.ExecResult) (string, string) {
	if e == nil || e.artifacts == nil || out == nil {
		return "", ""
	}

	payload := *out
	payload.CaptureRef = ""
	payload.ArtifactPath = ""
	payload.SuggestedReads = nil
	payload.CapturePersisted = false
	payload.CaptureKind = ""
	payload.StdoutCapturePath = ""
	payload.StderrCapturePath = ""

	captureFiles := make(map[string]string)
	if path := strings.TrimSpace(out.StdoutCapturePath); path != "" {
		captureFiles["stdout.txt"] = path
	}
	if path := strings.TrimSpace(out.StderrCapturePath); path != "" {
		captureFiles["stderr.txt"] = path
	}
	if len(captureFiles) > 0 {
		path, err := e.artifacts.WriteToolCaptureBundle(stepID, toolName, payload, captureFiles)
		if err == nil {
			return path, "shell_bundle"
		}
	}

	path, err := e.artifacts.WriteToolPayload(stepID, toolName, payload)
	if err != nil {
		return "", ""
	}
	return path, "json"
}

func cleanupTransientCaptureFiles(out *registry.ExecResult) {
	if out == nil {
		return
	}
	removeIfTemp(out.StdoutCapturePath)
	removeIfTemp(out.StderrCapturePath)
	out.StdoutCapturePath = ""
	out.StderrCapturePath = ""
}

func removeIfTemp(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	if !strings.Contains(path, "pipelineai-shell-capture-") {
		return
	}
	_ = os.Remove(path)
}
