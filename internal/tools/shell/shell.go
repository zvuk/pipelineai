package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tools "github.com/zvuk/pipelineai/internal/tools"
	"github.com/zvuk/pipelineai/internal/tools/approval"
)

// Args описывает параметры вызова shell инструмента.
type Args struct {
	// Command — команда и её аргументы.
	Command []string `json:"command"`
	// Workdir — рабочая директория выполнения.
	Workdir string `json:"workdir,omitempty"`
	// MaxCaptureBytes — максимальный объём stdout/stderr preview в памяти.
	MaxCaptureBytes int
	// PersistOverflow включает временный спилл полного вывода при переполнении preview.
	PersistOverflow bool
	// PersistAlways включает спилл полного вывода независимо от переполнения preview.
	PersistAlways bool
	Timeout       time.Duration
}

// Result — структурированный ответ инструмента.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Blocked  bool
	Message  string
	Elapsed  time.Duration
	// StdoutBytes и StderrBytes содержат размер полного вывода в байтах.
	StdoutBytes int64
	StderrBytes int64
	// StdoutLines и StderrLines содержат число строк в полном выводе.
	StdoutLines int64
	StderrLines int64
	// StdoutTruncated и StderrTruncated показывают, был ли preview усечён.
	StdoutTruncated bool
	StderrTruncated bool
	// StdoutCapturePath и StderrCapturePath указывают временные файлы с полным выводом.
	StdoutCapturePath string
	StderrCapturePath string
	// Отслеживание текущей директории (cd)
	ChangedWorkdir bool
	NewWorkdir     string
}

// Exec выполняет команду с учётом аппрувера и таймаута.
func Exec(ctx context.Context, args Args, approver *approval.ShellApprover) (Result, error) {
	start := time.Now()
	// Строка команды для сравнения с регэкспами (как в терминале)
	cmdline := strings.Join(args.Command, " ")

	if approver != nil {
		if forbid, msg := approver.IsShellCommandForbidden(cmdline); forbid {
			return Result{Blocked: true, Message: msg, Elapsed: time.Since(start), ExitCode: 0}, nil
		}
	}

	if len(args.Command) == 0 {
		return Result{}, errors.New("shell tool: command must not be empty")
	}

	// Явный запрет: apply_patch недоступен в shell/скриптах.
	// Если нужно упомянуть его в тексте — используйте бэктики: `apply_patch`.
	const apErr = "apply_patch is not available in shell. Call the apply_patch tool directly as a tool. If you need to mention it in text, wrap it in backticks like `apply_patch`."
	// Блокируем только реальные вызовы, а не упоминания
	if len(args.Command) > 0 {
		// Прямой вызов бинаря
		if strings.EqualFold(strings.TrimSpace(args.Command[0]), "apply_patch") {
			return Result{Blocked: true, Message: apErr, Elapsed: time.Since(start), ExitCode: 0}, nil
		}
		// bash -lc "..." — ищем вызов как отдельную команду
		if len(args.Command) >= 3 && args.Command[0] == "bash" && args.Command[1] == "-lc" {
			script := args.Command[2]
			if tools.ContainsApplyPatchInvocation(script) {
				return Result{Blocked: true, Message: apErr, Elapsed: time.Since(start), ExitCode: 0}, nil
			}
		}
	}

	// Обработка простого случая: команда только `cd <path>` — меняем директорию без запуска процесса
	if len(args.Command) >= 2 && args.Command[0] == "cd" {
		target := args.Command[1]
		base := args.Workdir
		if strings.TrimSpace(base) == "" {
			if wd, err := os.Getwd(); err == nil {
				base = wd
			}
		}
		newDir := target
		if !filepath.IsAbs(newDir) {
			newDir = filepath.Clean(filepath.Join(base, target))
		}
		return Result{ExitCode: 0, Elapsed: time.Since(start), ChangedWorkdir: true, NewWorkdir: newDir}, nil
	}

	// Таймаут: если контекст без дедлайна и задан Timeout, оборачиваем
	var cancel context.CancelFunc
	if _, has := ctx.Deadline(); !has && args.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, args.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, args.Command[0], args.Command[1:]...)
	effectiveDir := strings.TrimSpace(args.Workdir)
	// Специальная обработка bash -lc 'cd <dir> && ...' — распознаём начальный cd
	if len(args.Command) >= 3 && args.Command[0] == "bash" && args.Command[1] == "-lc" {
		script := args.Command[2]
		rx := regexp.MustCompile(`(?m)^\s*cd\s+([^\s;&|]+)\s*(?:&&|$)`) // простой разбор первого cd
		if m := rx.FindStringSubmatch(script); len(m) == 2 {
			target := m[1]
			base := effectiveDir
			if base == "" {
				if wd, err := os.Getwd(); err == nil {
					base = wd
				}
			}
			newDir := target
			if !filepath.IsAbs(newDir) {
				newDir = filepath.Clean(filepath.Join(base, target))
			}
			effectiveDir = newDir
		}
	}
	if effectiveDir != "" {
		cmd.Dir = effectiveDir
	}

	stdout := newBoundedCollector(args.MaxCaptureBytes, args.PersistOverflow, args.PersistAlways)
	defer func() {
		_ = stdout.Close()
	}()
	stderr := newBoundedCollector(args.MaxCaptureBytes, args.PersistOverflow, args.PersistAlways)
	defer func() {
		_ = stderr.Close()
	}()
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()

	res := Result{
		Stdout:            stdout.Preview(),
		Stderr:            stderr.Preview(),
		Elapsed:           time.Since(start),
		StdoutBytes:       stdout.totalBytes,
		StderrBytes:       stderr.totalBytes,
		StdoutLines:       stdout.totalLines,
		StderrLines:       stderr.totalLines,
		StdoutTruncated:   stdout.truncated,
		StderrTruncated:   stderr.truncated,
		StdoutCapturePath: stdout.CapturePath(),
		StderrCapturePath: stderr.CapturePath(),
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			res.ExitCode = 124
			return res, fmt.Errorf("shell tool: command timed out after %s", args.Timeout)
		}
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		res.ExitCode = -1
		return res, err
	}
	res.ExitCode = 0
	if effectiveDir != "" && effectiveDir != strings.TrimSpace(args.Workdir) {
		res.ChangedWorkdir = true
		res.NewWorkdir = effectiveDir
	}
	return res, nil
}
