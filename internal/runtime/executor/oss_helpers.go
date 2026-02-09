package executor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// isGPTOSSModel проверяет, относится ли модель к семейству */gpt-oss-*.
func isGPTOSSModel(model string) bool {
	m := strings.TrimSpace(strings.ToLower(model))
	if m == "" {
		return false
	}
	return strings.Contains(m, "gpt-oss-") || strings.HasPrefix(m, "gpt-oss-")
}

// collectAgentsDocs ищет и склеивает AGENTS.md от корня до baseDir (если файлы существуют).
func collectAgentsDocs(baseDir string) string {
	dir := baseDir
	if dir == "" {
		if wd, err := os.Getwd(); err == nil {
			dir = wd
		}
	}
	dir = filepath.Clean(dir)
	if dir == "" {
		return ""
	}

	// Сформируем путь вверх до корня
	var chain []string
	for {
		chain = append(chain, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// Разворачиваем: от корня к базовой директории
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}

	var parts []string
	for _, d := range chain {
		p := filepath.Join(d, "AGENTS.md")
		if b, err := os.ReadFile(p); err == nil {
			s := strings.TrimSpace(string(b))
			if s != "" {
				parts = append(parts, s)
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

// buildEnvironmentContext формирует текст для блока <environment_context>.
func buildEnvironmentContext() string {
	cwd := ""
	if wd, err := os.Getwd(); err == nil {
		cwd = wd
	}
	// Значения по умолчанию — соответствуют среде без сэндбокса и с доступом в сеть
	approval := "never"
	sandbox := "danger-full-access"
	network := "enabled"
	shell := preferredShell()

	var b strings.Builder
	if cwd != "" {
		b.WriteString("  <cwd>")
		b.WriteString(cwd)
		b.WriteString("</cwd>\n")
	}
	b.WriteString("  <approval_policy>")
	b.WriteString(approval)
	b.WriteString("</approval_policy>\n")
	b.WriteString("  <sandbox_mode>")
	b.WriteString(sandbox)
	b.WriteString("</sandbox_mode>\n")
	b.WriteString("  <network_access>")
	b.WriteString(network)
	b.WriteString("</network_access>\n")
	b.WriteString("  <shell>")
	b.WriteString(shell)
	b.WriteString("</shell>")
	return b.String()
}

// preferredShell возвращает имя shell для инструкций.
func preferredShell() string {
	// На Linux/macOS — bash, на Windows — powershell
	if runtime.GOOS == "windows" {
		return "powershell"
	}
	return "bash"
}
