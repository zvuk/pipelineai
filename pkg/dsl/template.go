package dsl

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"
	"text/template"
	"time"

	"go.yaml.in/yaml/v3"
)

// templateFuncMap содержит доступные функции для всех шаблонов DSL.
var templateFuncMap = template.FuncMap{
	// env — безопасное чтение переменной окружения.
	// Возвращает значение, если ключ не входит в список запрещённых.
	// При наличии fallback — возвращает его, если ключ запрещён или переменная не задана.
	// ВНИМАНИЕ: ошибки намеренно не возвращаем, чтобы рендеринг шаблонов не падал.
	"env": func(key string, fallback ...string) string {
		// санитайзер: проверка ключа на запрет
		k := strings.TrimSpace(key)
		ku := strings.ToUpper(k)
		if isForbiddenEnvKey(ku) {
			if len(fallback) > 0 {
				return fallback[0]
			}
			return ""
		}
		if value, ok := os.LookupEnv(k); ok {
			return value
		}
		if len(fallback) > 0 {
			return fallback[0]
		}
		return ""
	},
	"trim":    strings.TrimSpace,
	"now":     time.Now,
	"toUpper": strings.ToUpper,
	"toLower": strings.ToLower,
	"contains": func(s, substr string) bool {
		return strings.Contains(s, substr)
	},
	"hasPrefix": func(s, prefix string) bool {
		return strings.HasPrefix(s, prefix)
	},
	"hasSuffix": func(s, suffix string) bool {
		return strings.HasSuffix(s, suffix)
	},
	"join": strings.Join,
	"split": func(s, sep string) []string {
		return strings.Split(s, sep)
	},
	"replace": strings.ReplaceAll,
	"default": func(def, value string) string {
		if strings.TrimSpace(value) == "" {
			return def
		}
		return value
	},
	// shq — безопасное заключение в одиночные кавычки для bash
	// Преобразует каждую ' в последовательность: '\''
	"shq": func(s string) string {
		// Комментарий: реализуем POSIX-совместимое экранирование одиночных кавычек в одинарной строке
		if s == "" {
			return "''"
		}
		// Заменяем ' на '\'' и оборачиваем в одинарные кавычки
		esc := strings.ReplaceAll(s, "'", "'\\''")
		return "'" + esc + "'"
	},
}

// forbiddenEnvKeyPatterns — регулярные выражения для отсечения секретов по имени ключа.
// Включают маркёры: KEY, TOKEN, SECRET, PASSWORD. Матчат регистронезависимо.
var forbiddenEnvKeyPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(^|[_-])KEY($|[_-])`),      // API_KEY, SECRET_KEY, KEY_*
	regexp.MustCompile(`(?i)APIKEY`),                   // APIKEY (слитно)
	regexp.MustCompile(`(?i)(^|[_-])TOKEN($|[_-])`),    // *_TOKEN, TOKEN_*
	regexp.MustCompile(`(?i)(^|[_-])SECRET($|[_-])`),   // *_SECRET, SECRET_*
	regexp.MustCompile(`(?i)(^|[_-])PASSWORD($|[_-])`), // *_PASSWORD, PASSWORD_*
}

// isForbiddenEnvKey возвращает true, если имя переменной похоже на секрет.
func isForbiddenEnvKey(upperKey string) bool {
	// Разрешаем паттерн-указатели вида *_ENV (как TEST_LLM_API_KEY_ENV),
	// которые хранят имя другой переменной, а не сам секрет.
	if strings.HasSuffix(upperKey, "_ENV") {
		return false
	}
	for _, re := range forbiddenEnvKeyPatterns {
		if re.MatchString(upperKey) {
			return true
		}
	}
	return false
}

// TemplateString хранит исходный текст и подготовленный шаблон.
type TemplateString struct {
	raw  string
	tmpl *template.Template
}

// NewTemplateString создаёт шаблон из исходной строки.
func NewTemplateString(raw string) (TemplateString, error) {
	tmpl, err := template.New("dsl_string").Funcs(templateFuncMap).Option("missingkey=error").Parse(raw)
	if err != nil {
		return TemplateString{}, fmt.Errorf("dsl: failed to parse template %q: %w", raw, err)
	}
	return TemplateString{raw: raw, tmpl: tmpl}, nil
}

// String возвращает исходный текст без вычисления.
func (t *TemplateString) String() string {
	return t.raw
}

// Execute вычисляет шаблон с переданным контекстом.
func (t *TemplateString) Execute(ctx any) (string, error) {
	if t.tmpl == nil {
		return t.raw, nil
	}
	var buf bytes.Buffer
	if err := t.tmpl.Execute(&buf, ctx); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// IsZero возвращает true, если строка пустая.
func (t *TemplateString) IsZero() bool {
	return strings.TrimSpace(t.raw) == ""
}

// UnmarshalYAML реализует поддержку yaml.v3 для TemplateString.
func (t *TemplateString) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode {
		return fmt.Errorf("dsl: expected a string, got %s", value.ShortTag())
	}
	raw := value.Value
	tmpl, err := template.New("dsl_string").Funcs(templateFuncMap).Option("missingkey=error").Parse(raw)
	if err != nil {
		return fmt.Errorf("dsl: failed to parse template %q: %w", raw, err)
	}
	t.raw = raw
	t.tmpl = tmpl
	return nil
}

// RenderStringWithDefaults рендерит произвольную строку-шаблон с тем же набором функций,
// но с политикой missingkey=default — отсутствие ключа не вызывает ошибку (используется в пользовательских функциях).
func RenderStringWithDefaults(raw string, data any) (string, error) {
	tmpl, err := template.New("dsl_default").Funcs(templateFuncMap).Option("missingkey=default").Parse(raw)
	if err != nil {
		return "", fmt.Errorf("dsl: failed to parse template %q: %w", raw, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Duration обёртка над time.Duration для корректного парсинга из YAML.
type Duration struct {
	time.Duration
}

// UnmarshalYAML парсит строку с длительностью в формате Go.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode {
		return fmt.Errorf("dsl: expected a duration string, got %s", value.ShortTag())
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value.Value))
	if err != nil {
		return fmt.Errorf("dsl: failed to parse duration %q: %w", value.Value, err)
	}
	d.Duration = parsed
	return nil
}

// MarshalYAML требуется для совместимости yaml.v3.
func (d *Duration) MarshalYAML() (any, error) {
	return d.Duration.String(), nil
}
