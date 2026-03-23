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
	if metrics == nil || metrics.ToolWarnThreshold <= 0 {
		return out
	}

	payload, err := json.Marshal(out)
	if err != nil {
		return out
	}
	estimate := e.tokenizer.CountText(e.cfg.Agent.Model, e.cfg.Agent.ModelContextWindow, string(payload))
	if estimate.Tokens < metrics.ToolWarnThreshold {
		return out
	}
	if forceFullOutput {
		return out
	}
	if _, seen := warned[signature]; seen {
		return out
	}

	warned[signature] = struct{}{}
	metrics.ToolWarnings++

	warning := fmt.Sprintf(
		"Tool output was suppressed because it is too large for the current context budget: estimated %d tokens, threshold %d tokens. Narrow the request or repeat the same call with force_full_output=true to receive the complete payload.",
		estimate.Tokens,
		metrics.ToolWarnThreshold,
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
		EstimatedTokens: estimate.Tokens,
		ThresholdTokens: metrics.ToolWarnThreshold,
		Preview:         buildToolResultPreview(out),
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
