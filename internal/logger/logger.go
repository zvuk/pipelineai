// Package logger — простой человеко‑читаемый цветной slog‑логгер без внешних зависимостей.
package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// ansi цвета (включаемые/выключаемые)
const (
	cReset  = "\x1b[0m"
	cDim    = "\x1b[2m"
	cBold   = "\x1b[1m"
	cRed    = "\x1b[31m"
	cGreen  = "\x1b[32m"
	cYellow = "\x1b[33m"
	cBlue   = "\x1b[34m"
	cMag    = "\x1b[35m"
	cCyan   = "\x1b[36m"
	cGray   = "\x1b[90m"
)

// HumanHandler — минималистичный обработчик slog, печатающий одну строку на запись с подсветкой.
type HumanHandler struct {
	w        io.Writer
	mu       sync.Mutex
	minLevel slog.Leveler
	useColor bool
	attrs    []slog.Attr
	group    string
}

func NewHumanHandler(w io.Writer, min slog.Leveler, useColor bool) *HumanHandler {
	return &HumanHandler{w: w, minLevel: min, useColor: useColor}
}

func (h *HumanHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.minLevel.Level()
}

func (h *HumanHandler) WithAttrs(as []slog.Attr) slog.Handler {
	cp := *h
	cp.attrs = append(append([]slog.Attr{}, h.attrs...), as...)
	return &cp
}

func (h *HumanHandler) WithGroup(name string) slog.Handler {
	cp := *h
	if h.group != "" {
		cp.group = h.group + "." + name
	} else {
		cp.group = name
	}
	return &cp
}

func colorize(use bool, color, s string) string {
	if !use || s == "" {
		return s
	}
	return color + s + cReset
}

func (h *HumanHandler) Handle(_ context.Context, r slog.Record) error {
	// Собираем итоговые атрибуты с учётом WithAttrs
	all := make([]slog.Attr, 0, len(h.attrs)+r.NumAttrs())
	all = append(all, h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		all = append(all, a)
		return true
	})

	// Вырисуем префикс: время + уровень
	ts := r.Time
	if ts.IsZero() {
		ts = time.Now()
	}
	level := r.Level
	lvlTxt := "INF"
	lvlColor := cGreen
	switch level {
	case slog.LevelDebug:
		lvlTxt = "DBG"
		lvlColor = cGray
	case slog.LevelInfo:
		lvlTxt = "INF"
		lvlColor = cGreen
	case slog.LevelWarn:
		lvlTxt = "WRN"
		lvlColor = cYellow
	case slog.LevelError:
		lvlTxt = "ERR"
		lvlColor = cRed
	default:
		lvlTxt = strings.ToUpper(level.String())
		lvlColor = cGreen
	}

	// Основное сообщение
	msg := r.Message

	// Сформируем ключ=значение с минимальной раскраской для известных ключей
	kv := make([]string, 0, len(all))
	for _, a := range all {
		if !a.Equal(slog.Attr{}) {
			k := a.Key
			v := fmt.Sprint(a.Value)
			switch k {
			case "step", "id":
				k = colorize(h.useColor, cBlue, k)
				v = colorize(h.useColor, cBlue, v)
			case "type", "model":
				k = colorize(h.useColor, cCyan, k)
				v = colorize(h.useColor, cCyan, v)
			case "name":
				k = colorize(h.useColor, cBold, k)
				v = colorize(h.useColor, cBold, v)
			case "description", "args", "params", "content":
				k = colorize(h.useColor, cDim, k)
				v = colorize(h.useColor, cDim, v)
			case "tool":
				k = colorize(h.useColor, cMag, k)
				v = colorize(h.useColor, cMag, v)
			case "ok", "result":
				k = colorize(h.useColor, cBold, k)
				if strings.EqualFold(v, "true") || strings.EqualFold(v, "ok") || strings.EqualFold(v, "pass") {
					v = colorize(h.useColor, cGreen, v)
				} else {
					v = colorize(h.useColor, cRed, v)
				}
			case "elapsed", "elapsed_ms", "tokens", "prompt_tokens", "completion_tokens", "total_tokens":
				k = colorize(h.useColor, cGray, k)
				v = colorize(h.useColor, cGray, v)
			default:
				// оставим как есть
			}
			kv = append(kv, fmt.Sprintf("%s=%s", k, v))
		}
	}

	line := fmt.Sprintf("%s %s %s %s",
		colorize(h.useColor, cGray, ts.Format("15:04:05")),
		colorize(h.useColor, lvlColor, lvlTxt),
		msg,
		strings.Join(kv, " "),
	)

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, line+"\n")
	return err
}

// New создаёт цветной slog.Logger. Уровень берётся из env LOG_LEVEL (debug|info|warn|error), цвет из NO_COLOR.
func New() (*slog.Logger, error) {
	lvl := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL")))
	var min slog.Level
	switch lvl {
	case "debug":
		min = slog.LevelDebug
	case "warn":
		min = slog.LevelWarn
	case "error":
		min = slog.LevelError
	default:
		min = slog.LevelInfo
	}
	noColor := strings.TrimSpace(os.Getenv("NO_COLOR"))
	useColor := !(noColor == "1" || strings.EqualFold(noColor, "true"))
	h := NewHumanHandler(os.Stdout, min, useColor)
	log := slog.New(h)
	// Сделаем дефолтным для пакетов, где логгер не проброшен
	slog.SetDefault(log)
	return log, nil
}

type ctxKey int

const loggerKey ctxKey = 1

// WithContext кладёт логгер в контекст.
func WithContext(ctx context.Context, log *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, log)
}

// FromContext достаёт логгер из контекста или возвращает slog.Default().
func FromContext(ctx context.Context) *slog.Logger {
	if v := ctx.Value(loggerKey); v != nil {
		if l, ok := v.(*slog.Logger); ok && l != nil {
			return l
		}
	}
	return slog.Default()
}
