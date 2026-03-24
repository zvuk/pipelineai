package executor

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zvuk/pipelineai/internal/runtime/llm"
	"github.com/zvuk/pipelineai/internal/tools/registry"
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
	tc llm.ToolCall,
	out registry.ExecResult,
	forceFullOutput bool,
	signature string,
	warned map[string]struct{},
	metrics *stepTokenMetrics,
) registry.ExecResult {
	if metrics == nil {
		return out
	}

	payload, err := json.Marshal(out)
	if err != nil {
		return out
	}
	estimate := e.tokenizer.CountText(e.cfg.Agent.Model, e.cfg.Agent.ModelContextWindow, string(payload))
	hardCapExceeded := metrics.ToolHardCapThreshold > 0 && estimate.Tokens >= metrics.ToolHardCapThreshold
	warnThresholdExceeded := metrics.ToolWarnThreshold > 0 && estimate.Tokens >= metrics.ToolWarnThreshold
	if !hardCapExceeded && !warnThresholdExceeded {
		return out
	}

	artifactPath := ""
	if e.artifacts != nil {
		if path, err := e.artifacts.WriteToolPayload(stepID, tc.Function.Name, out); err == nil {
			artifactPath = path
		}
	}

	if hardCapExceeded {
		metrics.ToolHardCapSuppressions++
		return suppressedToolResult(
			out,
			estimate.Tokens,
			metrics.ToolHardCapThreshold,
			"The full payload was stored outside of the dialog because it exceeds the hard context cap. Narrow the request or inspect artifact_path via shell. force_full_output=true is ignored for this tool result.",
			artifactPath,
			true,
		)
	}

	if forceFullOutput {
		return out
	}
	if _, seen := warned[signature]; !seen {
		warned[signature] = struct{}{}
		metrics.ToolWarnings++
	}
	return suppressedToolResult(
		out,
		estimate.Tokens,
		metrics.ToolWarnThreshold,
		"Repeat the call with force_full_output=true only if the complete payload is strictly needed in context. The full payload was also stored in artifact_path for inspection outside the dialog.",
		artifactPath,
		false,
	)
}

func suppressedToolResult(
	out registry.ExecResult,
	estimatedTokens int,
	thresholdTokens int,
	hint string,
	artifactPath string,
	hardSuppressed bool,
) registry.ExecResult {
	warning := fmt.Sprintf(
		"Tool output was suppressed because it is too large for the current context budget: estimated %d tokens, threshold %d tokens. %s",
		estimatedTokens,
		thresholdTokens,
		strings.TrimSpace(hint),
	)
	return registry.ExecResult{
		Tool:            out.Tool,
		Ok:              out.Ok,
		Stdout:          "",
		Stderr:          "",
		ExitCode:        out.ExitCode,
		Summary:         out.Summary,
		Added:           out.Added,
		Modified:        out.Modified,
		Deleted:         out.Deleted,
		ElapsedMs:       out.ElapsedMs,
		NewWorkdir:      out.NewWorkdir,
		ToolError:       out.ToolError,
		Warning:         warning,
		Suppressed:      true,
		HardSuppressed:  hardSuppressed,
		EstimatedTokens: estimatedTokens,
		ThresholdTokens: thresholdTokens,
		Preview:         buildToolResultPreview(out),
		ArtifactPath:    artifactPath,
	}
}

func buildToolResultPreview(out registry.ExecResult) string {
	var parts []string
	if txt := strings.TrimSpace(out.Summary); txt != "" {
		parts = append(parts, "summary: "+txt)
	}
	if txt := strings.TrimSpace(out.ToolError); txt != "" {
		parts = append(parts, "tool_error: "+txt)
	}
	if txt := strings.TrimSpace(out.Stdout); txt != "" {
		parts = append(parts, "stdout: "+crop(txt, 400))
	}
	if txt := strings.TrimSpace(out.Stderr); txt != "" {
		parts = append(parts, "stderr: "+crop(txt, 400))
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}
