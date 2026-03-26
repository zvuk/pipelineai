package specs

import (
	"github.com/zvuk/pipelineai/internal/runtime/llm"
)

// ShellToolSpec возвращает JSON‑схему инструмента shell для Chat Completions API.
func ShellToolSpec() llm.Tool {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "The command to execute",
			},
			"workdir": map[string]any{
				"type":        "string",
				"description": "The working directory to execute the command in",
			},
			"timeout_ms": map[string]any{
				"type":        "number",
				"description": "The timeout for the command in milliseconds",
			},
			"force_full_output": map[string]any{
				"type":        "boolean",
				"description": "When true, return the full tool output even if it is large. This flag is ignored if the full payload would overflow the current context window or exceed the hard tool-output cap.",
			},
		},
		"required":              []string{"command"},
		"additional_properties": false,
	}

	return llm.Tool{
		Type: "function",
		Function: llm.ToolFunctionSpec{
			Name:        "shell",
			Description: "Runs a shell command and returns its output.",
			Parameters:  params,
		},
	}
}

// ApplyPatchToolSpecJSON возвращает JSON‑инструмент apply_patch с полнотой описания (копия из codex-rs).
func ApplyPatchToolSpecJSON() llm.Tool {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"input": map[string]any{
				"type":        "string",
				"description": "The entire contents of the apply_patch command",
			},
			"force_full_output": map[string]any{
				"type":        "boolean",
				"description": "When true, return the full tool output even if it is large. This flag is ignored if the full payload would overflow the current context window or exceed the hard tool-output cap.",
			},
		},
		"required":              []string{"input"},
		"additional_properties": false,
	}

	desc := "Use the `apply_patch` tool to edit files.\nYour patch language is a stripped‑down, file‑oriented diff format designed to be easy to parse and safe to apply. You can think of it as a high‑level envelope:\n\n*** Begin Patch\n[ one or more file sections ]\n*** End Patch\n\nWithin that envelope, you get a sequence of file operations.\nYou MUST include a header to specify the action you are taking.\nEach operation starts with one of three headers:\n\n*** Add File: <path> - create a new file. Every following line is a + line (the initial contents).\n*** Delete File: <path> - remove an existing file. Nothing follows.\n*** Update File: <path> - patch an existing file in place (optionally with a rename).\n\nMay be immediately followed by *** Move to: <new path> if you want to rename the file.\nThen one or more “hunks”, each introduced by @@ (optionally followed by a hunk header).\nWithin a hunk each line starts with:\n\nFor instructions on [context_before] and [context_after]:\n- By default, show 3 lines of code immediately above and 3 lines immediately below each change. If a change is within 3 lines of a previous change, do NOT duplicate the first change’s [context_after] lines in the second change’s [context_before] lines.\n- If 3 lines of context is insufficient to uniquely identify the snippet of code within the file, use the @@ operator to indicate the class or function to which the snippet belongs. For instance, we might have:\n@@ class BaseClass\n[3 lines of pre-context]\n- [old_code]\n+ [new_code]\n[3 lines of post-context]\n\n- If a code block is repeated so many times in a class or function such that even a single `@@` statement and 3 lines of context cannot uniquely identify the snippet of code, you can use multiple `@@` statements to jump to the right context. For instance:\n\n@@ class BaseClass\n@@ \t def method():\n[3 lines of pre-context]\n- [old_code]\n+ [new_code]\n[3 lines of post-context]\n\nThe full grammar definition is below:\nPatch := Begin { FileOp } End\nBegin := \"*** Begin Patch\" NEWLINE\nEnd := \"*** End Patch\" NEWLINE\nFileOp := AddFile | DeleteFile | UpdateFile\nAddFile := \"*** Add File: \" path NEWLINE { \"+\" line NEWLINE }\nDeleteFile := \"*** Delete File: \" path NEWLINE\nUpdateFile := \"*** Update File: \" path NEWLINE [ MoveTo ] { Hunk }\nMoveTo := \"*** Move to: \" newPath NEWLINE\nHunk := \"@@\" [ header ] NEWLINE { HunkLine } [ \"*** End of File\" NEWLINE ]\nHunkLine := (\" \" | \"-\" | \"+\") text NEWLINE\n\nA full patch can combine several operations:\n\n*** Begin Patch\n*** Add File: hello.txt\n+Hello world\n*** Update File: src/app.py\n*** Move to: src/main.py\n@@ def greet():\n-print(\"Hi\")\n+print(\"Hello, world!\")\n*** Delete File: obsolete.txt\n*** End Patch\n\nIt is important to remember:\n\n- You must include a header with your intended action (Add/Delete/Update)\n- You must prefix new lines with `+` even when creating a new file\n- File references can only be relative, NEVER ABSOLUTE."

	return llm.Tool{
		Type: "function",
		Function: llm.ToolFunctionSpec{
			Name:        "apply_patch",
			Description: desc,
			Parameters:  params,
		},
	}
}
