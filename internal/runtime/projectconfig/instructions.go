package projectconfig

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zvuk/pipelineai/pkg/dsl"
)

// StaticTemplateContext возвращает project-контекст без рендера instruction blocks.
func StaticTemplateContext(pc *dsl.ProjectConfig) map[string]any {
	resources := map[string]string{}
	if pc != nil {
		for k, v := range pc.Resources {
			resources[k] = v
		}
	}
	return map[string]any{
		"resources":          resources,
		"instruction_blocks": map[string]string{},
	}
}

// TemplateContext рендерит именованные instruction blocks для текущего prompt-контекста.
func TemplateContext(pc *dsl.ProjectConfig, ctx map[string]any) (map[string]any, error) {
	out := StaticTemplateContext(pc)
	blocks := map[string]string{}
	if pc != nil {
		for _, block := range pc.InstructionBlocks {
			id := strings.TrimSpace(block.ID)
			if id == "" {
				continue
			}
			text, err := renderInstructionBlock(block, ctx)
			if err != nil {
				return nil, err
			}
			blocks[id] = text
		}
	}
	out["instruction_blocks"] = blocks
	return out, nil
}

func renderInstructionBlock(block dsl.InstructionBlock, ctx map[string]any) (string, error) {
	files := currentFiles(ctx)
	var b strings.Builder

	if !block.Title.IsZero() {
		title, err := block.Title.Execute(ctx)
		if err != nil {
			return "", fmt.Errorf("project config: failed to render instruction block %s title: %w", block.ID, err)
		}
		title = strings.TrimSpace(title)
		if title != "" {
			b.WriteString("### ")
			b.WriteString(title)
			b.WriteString("\n\n")
		}
	}
	if !block.Content.IsZero() {
		content, err := block.Content.Execute(ctx)
		if err != nil {
			return "", fmt.Errorf("project config: failed to render instruction block %s content: %w", block.ID, err)
		}
		content = strings.TrimSpace(content)
		if content != "" {
			b.WriteString(content)
			b.WriteString("\n\n")
		}
	}

	renderedItems := make([]string, 0, len(block.Items))
	for _, item := range block.Items {
		if !instructionItemMatches(item, files) {
			continue
		}
		text, err := renderInstructionItem(item, ctx)
		if err != nil {
			return "", fmt.Errorf("project config: failed to render instruction block %s item: %w", block.ID, err)
		}
		text = strings.TrimSpace(text)
		if text != "" {
			renderedItems = append(renderedItems, text)
		}
	}
	if len(renderedItems) > 0 {
		for _, item := range renderedItems {
			b.WriteString("- ")
			b.WriteString(item)
			b.WriteString("\n")
		}
	}

	return strings.TrimSpace(b.String()), nil
}

func renderInstructionItem(item dsl.InstructionItem, ctx map[string]any) (string, error) {
	if !item.Content.IsZero() {
		content, err := item.Content.Execute(ctx)
		if err != nil {
			return "", err
		}
		content = strings.TrimSpace(content)
		if content != "" {
			return content, nil
		}
	}

	path := ""
	if !item.Path.IsZero() {
		value, err := item.Path.Execute(ctx)
		if err != nil {
			return "", err
		}
		path = strings.TrimSpace(value)
	}
	label := ""
	if !item.Label.IsZero() {
		value, err := item.Label.Execute(ctx)
		if err != nil {
			return "", err
		}
		label = strings.TrimSpace(value)
	}
	if path == "" && label == "" {
		return "", nil
	}

	var b strings.Builder
	if path != "" {
		b.WriteString("`")
		b.WriteString(path)
		b.WriteString("`")
	}
	if label != "" {
		if b.Len() > 0 {
			b.WriteString(" — ")
		}
		b.WriteString(label)
	}
	if item.Required {
		b.WriteString(" (required)")
	}
	return b.String(), nil
}

func instructionItemMatches(item dsl.InstructionItem, files []string) bool {
	if item.When == nil {
		return true
	}
	hasConditions := len(item.When.AnyGlob) > 0 || len(item.When.AnyExt) > 0
	if !hasConditions {
		return true
	}
	if len(files) == 0 {
		return false
	}
	for _, file := range files {
		if anyGlobMatch(file, item.When.AnyGlob) || anyExtMatch(file, item.When.AnyExt) {
			return true
		}
	}
	return false
}

func currentFiles(ctx map[string]any) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(v string) {
		for _, part := range strings.Split(v, ",") {
			p := strings.TrimSpace(part)
			if p == "" {
				continue
			}
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	if matrixRaw, ok := ctx["matrix"]; ok {
		if matrix, ok := matrixRaw.(map[string]any); ok {
			add(asString(matrix["file_path"]))
			add(asString(matrix["file_paths_csv"]))
			if item, ok := matrix["item"].(map[string]any); ok {
				add(asString(item["primary_file_path"]))
				add(asString(item["file_paths_csv"]))
				if files, ok := item["files"].([]map[string]any); ok {
					for _, f := range files {
						add(asString(f["file_path"]))
					}
				}
				if files, ok := item["files"].([]any); ok {
					for _, raw := range files {
						if f, ok := raw.(map[string]any); ok {
							add(asString(f["file_path"]))
						}
					}
				}
			}
		}
	}
	add(asString(ctx["file_path"]))
	return out
}

func anyGlobMatch(path string, globs []string) bool {
	if len(globs) == 0 {
		return false
	}
	for _, pattern := range globs {
		if globMatch(pattern, path) {
			return true
		}
	}
	return false
}

func globMatch(pattern string, value string) bool {
	p := strings.TrimSpace(pattern)
	if p == "" {
		return false
	}
	var b strings.Builder
	b.WriteString("^")
	for _, r := range p {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			if strings.ContainsRune(`.+()|[]{}^$\`, r) {
				b.WriteRune('\\')
			}
			b.WriteRune(r)
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	return re.MatchString(value)
}

func anyExtMatch(path string, extList []string) bool {
	if len(extList) == 0 {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	for _, candidate := range extList {
		if ext == strings.ToLower(strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		return ""
	}
}
