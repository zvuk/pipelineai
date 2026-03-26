package executor

import (
	"strings"

	"github.com/zvuk/pipelineai/internal/runtime/tokens"
	"github.com/zvuk/pipelineai/pkg/dsl"
)

const (
	budgetModeHardStop               = "hard_stop"
	budgetModeWarn                   = "warn"
	budgetModeContinueWithCompaction = "continue_with_compaction"

	toolResultModeInline            = "inline"
	toolResultModeTruncate          = "truncate"
	toolResultModePersistOnOverflow = "persist_on_overflow"
	toolResultModePersistAlways     = "persist_always"

	defaultToolResultMode          = toolResultModePersistOnOverflow
	defaultToolResultPreviewTokens = 512
	defaultShellCaptureMaxBytes    = 256 * 1024
)

func resolveBudgetMode(cfg *dsl.Config, step *dsl.Step) string {
	if step != nil && step.LLM != nil {
		if mode := normalizeBudgetMode(step.LLM.BudgetMode); mode != "" {
			return mode
		}
	}
	if cfg != nil {
		if mode := normalizeBudgetMode(cfg.Agent.BudgetMode); mode != "" {
			return mode
		}
	}
	return budgetModeHardStop
}

func normalizeBudgetMode(mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case budgetModeHardStop, budgetModeWarn, budgetModeContinueWithCompaction:
		return strings.TrimSpace(strings.ToLower(mode))
	default:
		return ""
	}
}

func resolveToolResultMode(cfg *dsl.Config, step *dsl.Step) string {
	if step != nil && step.LLM != nil {
		if mode := normalizeToolResultMode(step.LLM.ToolResultMode); mode != "" {
			return mode
		}
	}
	if cfg != nil {
		if mode := normalizeToolResultMode(cfg.Agent.ToolResultMode); mode != "" {
			return mode
		}
	}
	return defaultToolResultMode
}

func normalizeToolResultMode(mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case toolResultModeInline, toolResultModeTruncate, toolResultModePersistOnOverflow, toolResultModePersistAlways:
		return strings.TrimSpace(strings.ToLower(mode))
	default:
		return ""
	}
}

func resolveToolResultPreviewTokens(cfg *dsl.Config, step *dsl.Step) int {
	if step != nil && step.LLM != nil && step.LLM.ToolResultPreviewTokens != nil && *step.LLM.ToolResultPreviewTokens > 0 {
		return *step.LLM.ToolResultPreviewTokens
	}
	if cfg != nil && cfg.Agent.ToolResultPreviewTokens != nil && *cfg.Agent.ToolResultPreviewTokens > 0 {
		return *cfg.Agent.ToolResultPreviewTokens
	}
	return defaultToolResultPreviewTokens
}

func resolveShellCaptureMaxBytes(cfg *dsl.Config, step *dsl.Step) int {
	if step != nil && step.LLM != nil && step.LLM.ShellCaptureMaxBytes != nil && *step.LLM.ShellCaptureMaxBytes > 0 {
		return *step.LLM.ShellCaptureMaxBytes
	}
	if cfg != nil && cfg.Agent.ShellCaptureMaxBytes != nil && *cfg.Agent.ShellCaptureMaxBytes > 0 {
		return *cfg.Agent.ShellCaptureMaxBytes
	}
	return defaultShellCaptureMaxBytes
}

func resolveDisableInlineToolCallFallback(cfg *dsl.Config, step *dsl.Step) bool {
	if step != nil && step.LLM != nil && step.LLM.DisableInlineToolCallFallback != nil {
		return *step.LLM.DisableInlineToolCallFallback
	}
	if cfg != nil {
		return cfg.Agent.DisableInlineToolCallFallback
	}
	return false
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
