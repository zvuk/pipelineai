package artifacts

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Manager управляет структурой каталогов артефактов PipelineAI.
type Manager struct {
	root string
}

// NewManager создаёт менеджер артефактов и гарантирует наличие базового каталога.
func NewManager(root string) (*Manager, error) {
	if root == "" {
		return nil, fmt.Errorf("artifacts: root directory must not be empty")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("artifacts: failed to create directory %s: %w", root, err)
	}
	return &Manager{root: root}, nil
}

// Root возвращает абсолютный или относительный путь к каталогу артефактов.
func (m *Manager) Root() string {
	return m.root
}

// WriteChatLog перезаписывает историю диалога в файле <root>/log/<step_id>.json.
// payload должен быть JSON-сериализуемым (как правило, массив сообщений).
func (m *Manager) WriteChatLog(stepID string, payload any) (string, error) {
	// пишем историю сообщений сверху вниз
	if strings.TrimSpace(stepID) == "" {
		return "", fmt.Errorf("artifacts: stepID must not be empty")
	}
	dir := filepath.Join(m.root, "log")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("artifacts: failed to create directory %s: %w", dir, err)
	}
	path := filepath.Join(dir, stepID+".json")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("artifacts: failed to open file %s: %w", path, err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		return "", fmt.Errorf("artifacts: failed to write json to %s: %w", path, err)
	}
	return path, nil
}

// WriteLLMResponse сохраняет json-ответ LLM в каталоге llm/<step-id>.json.
func (m *Manager) WriteLLMResponse(stepID string, payload any) (string, error) {
	// сохраняет json-ответ LLM в каталоге llm/<step-id>/<ts>.json с дедупликацией по meta.hash
	if strings.TrimSpace(stepID) == "" {
		return "", fmt.Errorf("artifacts: stepID must not be empty")
	}

	// Разворачиваем каталог llm/<step-id>
	dir := filepath.Join(m.root, "llm", stepID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("artifacts: failed to create directory %s: %w", dir, err)
	}

	// Пытаемся извлечь meta.hash из входного payload (ожидается map[string]any)
	var newHash string
	if mp, ok := payload.(map[string]any); ok {
		if metaRaw, ok := mp["meta"].(map[string]any); ok {
			if h, ok := metaRaw["hash"].(string); ok {
				newHash = strings.TrimSpace(h)
			}
		}
	}

	// Если есть предыдущий файл, сравним хэш и при совпадении вернём существующий
	prevPath, prevHash, _ := latestLLMArtifact(dir)
	if newHash != "" && prevHash != "" && newHash == prevHash {
		return prevPath, nil
	}

	// Имя файла по UTC времени, безопасный формат для имени файла
	ts := time.Now().UTC().Format("20060102T150405.000000000Z")
	path := filepath.Join(dir, fmt.Sprintf("%s.json", ts))

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("artifacts: failed to open file %s: %w", path, err)
	}
	defer file.Close()

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		return "", fmt.Errorf("artifacts: failed to write json to %s: %w", path, err)
	}

	return path, nil
}

// WriteToolPayload сохраняет полный payload инструмента в каталоге tools/<step-id>/<ts>-<tool>.json.
func (m *Manager) WriteToolPayload(stepID string, toolName string, payload any) (string, error) {
	if strings.TrimSpace(stepID) == "" {
		return "", fmt.Errorf("artifacts: stepID must not be empty")
	}
	if strings.TrimSpace(toolName) == "" {
		toolName = "tool"
	}
	safeToolName := sanitizeArtifactName(toolName)
	dir := filepath.Join(m.root, "tools", stepID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("artifacts: failed to create directory %s: %w", dir, err)
	}

	ts := time.Now().UTC().Format("20060102T150405.000000000Z")
	path := filepath.Join(dir, fmt.Sprintf("%s-%s.json", ts, safeToolName))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("artifacts: failed to open file %s: %w", path, err)
	}
	defer file.Close()

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		return "", fmt.Errorf("artifacts: failed to write json to %s: %w", path, err)
	}
	return path, nil
}

// latestLLMArtifact возвращает путь и meta.hash последнего по имени JSON файла в каталоге шага.
func latestLLMArtifact(dir string) (string, string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", err
	}
	files := make([]fs.DirEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(strings.ToLower(name), ".json") {
			files = append(files, e)
		}
	}
	if len(files) == 0 {
		return "", "", fmt.Errorf("artifacts: no files")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
	last := files[len(files)-1]
	path := filepath.Join(dir, last.Name())
	f, err := os.Open(path)
	if err != nil {
		return path, "", err
	}
	defer f.Close()
	var payload map[string]any
	if err := json.NewDecoder(f).Decode(&payload); err != nil {
		return path, "", err
	}
	if metaRaw, ok := payload["meta"].(map[string]any); ok {
		if h, ok := metaRaw["hash"].(string); ok {
			return path, strings.TrimSpace(h), nil
		}
	}
	return path, "", nil
}

func sanitizeArtifactName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "tool"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "tool"
	}
	return out
}
