package prompts

import _ "embed"

// Встроенные системные промпты для инструментов и дефолтные инструкции агента.

//go:embed shell_tool_instructions.md
var ShellTool string

//go:embed apply_patch_tool_instructions.md
var ApplyPatch string

//go:embed default_system_prompt.md
var DefaultSystem string
