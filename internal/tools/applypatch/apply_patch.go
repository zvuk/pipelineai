package applypatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zvuk/pipelineai/internal/tools/approval"
)

// Args описывает параметры вызова apply_patch.
type Args struct {
	Patch   string
	Workdir string
	DryRun  bool
}

// Result — итог операции применения патча.
type Result struct {
	Added    []string
	Modified []string
	Deleted  []string
	Summary  string
	Elapsed  time.Duration
}

// Exec применяет патч с учетом правил аппрувера. Путь в патче рассматривается как относительный к Workdir (или текущему каталогу).
func Exec(args Args, approver *approval.ApplyPatchApprover) (Result, error) {
	started := time.Now()
	hunks, err := parsePatch(args.Patch)
	if err != nil {
		return Result{}, err
	}

	var added, modified, deleted []string
	// dry‑run: только валидируем и строим summary
	if args.DryRun {
		for _, h := range hunks {
			abs := resolvePath(args.Workdir, h.Path)
			switch h.Kind {
			case hunkAdd:
				if approver != nil && !approver.IsApplyAllowed(abs, args.Workdir, approval.OpCreate) {
					return Result{}, fmt.Errorf("apply_patch: create denied by approver for %s", abs)
				}
				added = append(added, abs)
			case hunkDelete:
				if approver != nil && !approver.IsApplyAllowed(abs, args.Workdir, approval.OpDelete) {
					return Result{}, fmt.Errorf("apply_patch: delete denied by approver for %s", abs)
				}
				deleted = append(deleted, abs)
			case hunkUpdate:
				if approver != nil && !approver.IsApplyAllowed(abs, args.Workdir, approval.OpUpdate) {
					return Result{}, fmt.Errorf("apply_patch: update denied by approver for %s", abs)
				}
				modified = append(modified, abs)
			}
		}
		return Result{Added: added, Modified: modified, Deleted: deleted, Summary: buildSummary(added, modified, deleted), Elapsed: time.Since(started)}, nil
	}

	for _, h := range hunks {
		abs := resolvePath(args.Workdir, h.Path)
		switch h.Kind {
		case hunkAdd:
			if approver != nil && !approver.IsApplyAllowed(abs, args.Workdir, approval.OpCreate) {
				return Result{}, fmt.Errorf("apply_patch: create denied by approver for %s", abs)
			}
			if err := ensureParent(abs); err != nil {
				return Result{}, err
			}
			if err := os.WriteFile(abs, []byte(strings.Join(h.NewLines, "\n")), 0o644); err != nil {
				return Result{}, fmt.Errorf("apply_patch: failed to write %s: %w", abs, err)
			}
			added = append(added, abs)
		case hunkDelete:
			if approver != nil && !approver.IsApplyAllowed(abs, args.Workdir, approval.OpDelete) {
				return Result{}, fmt.Errorf("apply_patch: delete denied by approver for %s", abs)
			}
			if err := os.Remove(abs); err != nil {
				return Result{}, fmt.Errorf("apply_patch: failed to delete %s: %w", abs, err)
			}
			deleted = append(deleted, abs)
		case hunkUpdate:
			if approver != nil && !approver.IsApplyAllowed(abs, args.Workdir, approval.OpUpdate) {
				return Result{}, fmt.Errorf("apply_patch: update denied by approver for %s", abs)
			}
			// читаем исходный файл
			orig, err := os.ReadFile(abs)
			if err != nil {
				return Result{}, fmt.Errorf("apply_patch: failed to read %s: %w", abs, err)
			}
			newContent, err := applyUpdate(string(orig), h.Chunks)
			if err != nil {
				return Result{}, err
			}
			dest := abs
			if strings.TrimSpace(h.MoveTo) != "" {
				dest = resolvePath(args.Workdir, h.MoveTo)
				if approver != nil && !approver.IsApplyAllowed(dest, args.Workdir, approval.OpUpdate) {
					return Result{}, fmt.Errorf("apply_patch: update denied by approver for %s", dest)
				}
				if err := ensureParent(dest); err != nil {
					return Result{}, err
				}
			}
			if err := os.WriteFile(dest, []byte(newContent), 0o644); err != nil {
				return Result{}, fmt.Errorf("apply_patch: failed to write %s: %w", dest, err)
			}
			if dest != abs {
				_ = os.Remove(abs)
			}
			modified = append(modified, dest)
		}
	}
	return Result{Added: added, Modified: modified, Deleted: deleted, Summary: buildSummary(added, modified, deleted), Elapsed: time.Since(started)}, nil
}

// buildSummary строит краткую сводку изменений как список с префиксами A/M/D.
func buildSummary(added, modified, deleted []string) string {
	var b strings.Builder
	if len(added)+len(modified)+len(deleted) == 0 {
		return "No files were modified."
	}
	if len(added) > 0 {
		for _, p := range added {
			b.WriteString("A ")
			b.WriteString(p)
			b.WriteString("\n")
		}
	}
	if len(modified) > 0 {
		for _, p := range modified {
			b.WriteString("M ")
			b.WriteString(p)
			b.WriteString("\n")
		}
	}
	if len(deleted) > 0 {
		for _, p := range deleted {
			b.WriteString("D ")
			b.WriteString(p)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// ensureParent создаёт директорию родителя, если необходимо.
func ensureParent(path string) error {
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("apply_patch: failed to create parent dir for %s: %w", path, err)
		}
	}
	return nil
}

func resolvePath(workdir, rel string) string {
	p := filepath.Clean(rel)
	if filepath.IsAbs(p) {
		return p
	}
	if strings.TrimSpace(workdir) == "" {
		wd, _ := os.Getwd()
		return filepath.Join(wd, p)
	}
	return filepath.Join(workdir, p)
}

// --- Парсинг патча ---

type hunkKind int

const (
	hunkAdd hunkKind = iota + 1
	hunkDelete
	hunkUpdate
)

type fileHunk struct {
	Kind     hunkKind
	Path     string
	MoveTo   string        // для Update
	NewLines []string      // для Add
	Chunks   []updateChunk // для Update
}

type updateChunk struct {
	Old []string
	New []string
}

// parsePatch — детерминированный парсер формата apply_patch в один проход по строкам
func parsePatch(patch string) ([]fileHunk, error) {
	// Нормализуем перевод строк и уберём BOM/лидирующие пустые строки
	norm := strings.ReplaceAll(patch, "\r\n", "\n")
	norm = strings.TrimLeft(norm, "\ufeff\n\r\t ")
	lines := strings.Split(norm, "\n")
	idx := 0
	next := func() string {
		if idx >= len(lines) {
			return ""
		}
		s := lines[idx]
		idx++
		return s
	}
	peek := func() string {
		if idx >= len(lines) {
			return ""
		}
		return lines[idx]
	}

	// пропустим возможные пустые строки до заголовка
	for idx < len(lines) && strings.TrimSpace(lines[idx]) == "" {
		idx++
	}
	if idx >= len(lines) || strings.TrimSpace(next()) != "*** Begin Patch" {
		return nil, errors.New("apply_patch: the first line of the patch must be '*** Begin Patch'")
	}

	var hunks []fileHunk
	for idx < len(lines) {
		line := strings.TrimRight(peek(), "\r")
		if strings.TrimSpace(line) == "*** End Patch" {
			break
		}
		if strings.HasPrefix(line, "*** Add File: ") {
			_ = next()
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: "))
			var nl []string
			for idx < len(lines) {
				l := peek()
				if strings.HasPrefix(l, "*** ") || strings.HasPrefix(l, "@@") {
					break
				}
				_ = next()
				if strings.HasPrefix(l, "+") {
					nl = append(nl, strings.TrimPrefix(l, "+"))
				}
			}
			hunks = append(hunks, fileHunk{Kind: hunkAdd, Path: path, NewLines: nl})
			continue
		}
		if strings.HasPrefix(line, "*** Delete File: ") {
			_ = next()
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File: "))
			hunks = append(hunks, fileHunk{Kind: hunkDelete, Path: path})
			continue
		}
		if strings.HasPrefix(line, "*** Update File: ") {
			_ = next()
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: "))
			fh := fileHunk{Kind: hunkUpdate, Path: path}
			// optional Move to
			for idx < len(lines) {
				l := peek()
				if strings.HasPrefix(l, "*** Move to: ") {
					_ = next()
					fh.MoveTo = strings.TrimSpace(strings.TrimPrefix(l, "*** Move to: "))
					continue
				}
				if strings.HasPrefix(l, "@@") {
					_ = next() // уберем заголовок
					oldLines, newLines, consumed := parseOneChunk(lines, idx)
					idx = consumed
					fh.Chunks = append(fh.Chunks, updateChunk{Old: oldLines, New: newLines})
					continue
				}
				if strings.HasPrefix(l, "*** ") || strings.TrimSpace(l) == "*** End Patch" {
					break
				}
				_ = next() // пропуск неизвестной строки
			}
			hunks = append(hunks, fh)
			continue
		}
		_ = next() // пропускаем
	}
	return hunks, nil
}

// parseOneChunk парсит один блок до следующего маркера/хедера
func parseOneChunk(lines []string, start int) (oldLines []string, newLines []string, newIdx int) {
	i := start
	for i < len(lines) {
		l := lines[i]
		if strings.HasPrefix(l, "*** End of File") {
			i++
			break
		}
		if strings.HasPrefix(l, "@@") || strings.HasPrefix(l, "*** ") || strings.TrimSpace(l) == "*** End Patch" {
			break
		}
		if l == "" {
			i++
			continue
		}
		switch l[0] {
		case ' ':
			t := l[1:]
			oldLines = append(oldLines, t)
			newLines = append(newLines, t)
		case '+':
			newLines = append(newLines, l[1:])
		case '-':
			oldLines = append(oldLines, l[1:])
		}
		i++
	}
	return oldLines, newLines, i
}

// applyUpdate применяет набор чанков к исходному содержимому.
func applyUpdate(original string, chunks []updateChunk) (string, error) {
	lines := strings.Split(original, "\n")
	// убираем финальную пустую строку, чтобы совпадали индексы
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	// Поиск подпоследовательности (наивный) с режимами сравнения
	// 1) строгий
	// 2) послабление: игнорируем крайние пробелы (TrimSpace) — помогает принять патчи вида "- two" при строке "two"
	type matchMode int
	const (
		modeStrict matchMode = iota
		modeTrimSpace
	)
	eq := func(a, b string, m matchMode) bool {
		switch m {
		case modeStrict:
			return a == b
		case modeTrimSpace:
			return strings.TrimSpace(a) == strings.TrimSpace(b)
		default:
			return a == b
		}
	}
	seek := func(haystack, needle []string, start int, mode matchMode) int {
		for i := start; i+len(needle) <= len(haystack); i++ {
			ok := true
			for j := 0; j < len(needle); j++ {
				if !eq(haystack[i+j], needle[j], mode) {
					ok = false
					break
				}
			}
			if ok {
				return i
			}
		}
		return -1
	}
	seekWithModes := func(haystack, needle []string, start int) (idx int, usedMode matchMode) {
		if len(needle) == 0 {
			return -1, modeStrict
		}
		// строгий поиск
		if idx := seek(haystack, needle, start, modeStrict); idx >= 0 {
			return idx, modeStrict
		}
		// поиск с TrimSpace
		if idx := seek(haystack, needle, start, modeTrimSpace); idx >= 0 {
			return idx, modeTrimSpace
		}
		return -1, modeStrict
	}

	type rep struct {
		idx, oldLen int
		newSeg      []string
	}
	var reps []rep
	cursor := 0
	for _, ch := range chunks {
		if len(ch.Old) == 0 {
			// чистое добавление — в конец файла (перед последней пустой)
			insertionIdx := len(lines)
			reps = append(reps, rep{idx: insertionIdx, oldLen: 0, newSeg: ch.New})
			continue
		}
		// пробуем в строгом режиме, затем с TrimSpace
		idx, mode := seekWithModes(lines, ch.Old, cursor)
		// также пробуем без финальной пустой строки
		old := ch.Old
		newSeg := ch.New
		if idx < 0 {
			if len(old) > 0 && old[len(old)-1] == "" {
				old = old[:len(old)-1]
				idx, mode = seekWithModes(lines, old, cursor)
				if len(newSeg) > 0 && newSeg[len(newSeg)-1] == "" {
					newSeg = newSeg[:len(newSeg)-1]
				}
			}
		}
		if idx < 0 {
			return "", fmt.Errorf("apply_patch: failed to find expected lines in file")
		}
		// Если использовали режим с TrimSpace — нормализуем добавляемый сегмент (тоже TrimSpace)
		if mode == modeTrimSpace {
			ns := make([]string, 0, len(newSeg))
			for _, s := range newSeg {
				ns = append(ns, strings.TrimSpace(s))
			}
			newSeg = ns
		}
		reps = append(reps, rep{idx: idx, oldLen: len(old), newSeg: newSeg})
		cursor = idx + len(old)
	}

	// применяем замены в обратном порядке
	for i := len(reps) - 1; i >= 0; i-- {
		r := reps[i]
		if r.idx < 0 || r.idx > len(lines) {
			return "", fmt.Errorf("apply_patch: invalid replacement index")
		}
		if r.oldLen > 0 {
			end := r.idx + r.oldLen
			if end > len(lines) {
				end = len(lines)
			}
			lines = append(lines[:r.idx], lines[end:]...)
		}
		if len(r.newSeg) > 0 {
			seg := append([]string{}, r.newSeg...)
			left := append([]string{}, lines[:r.idx]...)
			right := append([]string{}, lines[r.idx:]...)
			lines = append(left, append(seg, right...)...)
		}
	}

	if len(lines) == 0 || lines[len(lines)-1] != "" {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n"), nil
}
