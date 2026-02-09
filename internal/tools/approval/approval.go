package approval

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/zvuk/pipelineai/pkg/dsl"
)

type ShellApprover struct {
	Rules []CompiledShellRule
}

type CompiledShellRule struct {
	Regex   *regexp.Regexp
	Message string
}

type ApplyPatchApprover struct {
	Rules []ApplyRule
}

type ApplyRule struct {
	GlobPatterns []string
	AllowCreate  *bool
	AllowUpdate  *bool
	AllowDelete  *bool
}

// BuildEffectiveApprovers строит итоговые аппруверы для шага: step переопределяет сценарий.
func BuildEffectiveApprovers(cfg *dsl.Config, step *dsl.Step) (shell *ShellApprover, apply *ApplyPatchApprover, err error) {
	// Функция для сборки shell/apply отдельно, с приоритетом step.
	// Если на шаге есть любой approver для инструмента — используем только их.
	shell, err = compileShellApprover(pickApprover("shell", cfg.Approvers, step.Approvers))
	if err != nil {
		return nil, nil, err
	}
	apply, err = compileApplyApprover(pickApprover("apply_patch", cfg.Approvers, step.Approvers))
	if err != nil {
		return nil, nil, err
	}
	return shell, apply, nil
}

// pickApprover выбирает approvers для инструмента с приоритетом на уровне шага.
func pickApprover(tool string, cfgApprovers, stepApprovers []dsl.Approver) []dsl.Approver {
	var stepHas bool
	for _, a := range stepApprovers {
		if strings.TrimSpace(a.Tool) == tool {
			stepHas = true
			break
		}
	}
	var list []dsl.Approver
	if stepHas {
		for _, a := range stepApprovers {
			if strings.TrimSpace(a.Tool) == tool {
				list = append(list, a)
			}
		}
		return list
	}
	for _, a := range cfgApprovers {
		if strings.TrimSpace(a.Tool) == tool {
			list = append(list, a)
		}
	}
	return list
}

func compileShellApprover(approvers []dsl.Approver) (*ShellApprover, error) {
	if len(approvers) == 0 {
		return nil, nil
	}
	// Пустой rules означает разрешено всё.
	var compiled []CompiledShellRule
	for _, ap := range approvers {
		if ap.Rules == nil {
			continue
		}
		rulesSlice, ok := ap.Rules.([]any)
		if !ok || len(rulesSlice) == 0 {
			continue
		}
		for _, r := range rulesSlice {
			m, ok := r.(map[string]any)
			if !ok {
				continue
			}
			rxStr := strings.TrimSpace(fmt.Sprint(m["regex"]))
			msgStr := strings.TrimSpace(fmt.Sprint(m["message"]))
			if rxStr == "" || msgStr == "" {
				continue
			}
			rx, err := regexp.Compile(rxStr)
			if err != nil {
				return nil, fmt.Errorf("approval: failed to compile regex %q: %w", rxStr, err)
			}
			compiled = append(compiled, CompiledShellRule{Regex: rx, Message: msgStr})
		}
	}
	return &ShellApprover{Rules: compiled}, nil
}

func compileApplyApprover(approvers []dsl.Approver) (*ApplyPatchApprover, error) {
	if len(approvers) == 0 {
		return nil, nil
	}
	var rules []ApplyRule
	for _, ap := range approvers {
		if ap.Rules == nil {
			continue
		}
		rulesSlice, ok := ap.Rules.([]any)
		if !ok || len(rulesSlice) == 0 {
			continue
		}
		for _, r := range rulesSlice {
			m, ok := r.(map[string]any)
			if !ok {
				continue
			}
			var patterns []string
			if v, ok := m["glob_patterns"]; ok {
				if arr, ok := v.([]any); ok {
					for _, it := range arr {
						s := strings.TrimSpace(fmt.Sprint(it))
						if s != "" {
							patterns = append(patterns, s)
						}
					}
				}
			}
			var c, u, d *bool
			if v, ok := m["allow_create"]; ok {
				b := parseBool(v)
				c = &b
			}
			if v, ok := m["allow_update"]; ok {
				b := parseBool(v)
				u = &b
			}
			if v, ok := m["allow_delete"]; ok {
				b := parseBool(v)
				d = &b
			}
			rules = append(rules, ApplyRule{GlobPatterns: patterns, AllowCreate: c, AllowUpdate: u, AllowDelete: d})
		}
	}
	return &ApplyPatchApprover{Rules: rules}, nil
}

func parseBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		return s == "true" || s == "1" || s == "yes"
	default:
		return false
	}
}

// IsShellCommandForbidden проверяет команду на совпадение с любым запретом.
func (s *ShellApprover) IsShellCommandForbidden(cmdline string) (bool, string) {
	if s == nil || len(s.Rules) == 0 {
		return false, ""
	}
	for _, r := range s.Rules {
		if r.Regex.MatchString(cmdline) {
			return true, r.Message
		}
	}
	return false, ""
}

type FileOp string

const (
	OpCreate FileOp = "create"
	OpUpdate FileOp = "update"
	OpDelete FileOp = "delete"
)

// IsApplyAllowed проверяет разрешение операции по пути с учетом правил.
// Семантика: если есть совпадающие правила — операция разрешена, если хотя бы в одном из них allow_* == true.
// Если правил не найдено — операция разрешена.
func (a *ApplyPatchApprover) IsApplyAllowed(path string, baseDir string, op FileOp) bool {
	if a == nil || len(a.Rules) == 0 {
		return true
	}
	matched := false
	allowed := false
	for _, r := range a.Rules {
		if len(r.GlobPatterns) == 0 {
			continue
		}
		// матч по абсолютному и по относительному (от baseDir)
		absSlash := filepath.ToSlash(path)
		relSlash := ""
		if strings.TrimSpace(baseDir) != "" {
			if rel, err := filepath.Rel(baseDir, path); err == nil {
				relSlash = filepath.ToSlash(rel)
			}
		}
		for _, g := range r.GlobPatterns {
			ok, _ := doublestar.PathMatch(g, absSlash)
			if ok {
				matched = true
				switch op {
				case OpCreate:
					if r.AllowCreate != nil && *r.AllowCreate {
						allowed = true
					}
				case OpUpdate:
					if r.AllowUpdate != nil && *r.AllowUpdate {
						allowed = true
					}
				case OpDelete:
					if r.AllowDelete != nil && *r.AllowDelete {
						allowed = true
					}
				}
				break
			}
			if relSlash != "" {
				okRel, _ := doublestar.PathMatch(g, relSlash)
				if okRel {
					matched = true
					switch op {
					case OpCreate:
						if r.AllowCreate != nil && *r.AllowCreate {
							allowed = true
						}
					case OpUpdate:
						if r.AllowUpdate != nil && *r.AllowUpdate {
							allowed = true
						}
					case OpDelete:
						if r.AllowDelete != nil && *r.AllowDelete {
							allowed = true
						}
					}
					break
				}
			}
		}
	}
	if !matched {
		return true
	}
	return allowed
}
