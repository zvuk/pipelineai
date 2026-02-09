package matrix

import (
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ReadFile читает YAML-файл manifest и возвращает произвольную структуру (map/array/scalars).
func ReadFile(path string) (any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("matrix: failed to read manifest %s: %w", path, err)
	}
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("matrix: failed to parse manifest %s: %w", path, err)
	}
	// Нормализуем ключи map[any]any -> map[string]any
	return normalize(v), nil
}

// normalize рекурсивно конвертирует map[any]any в map[string]any
func normalize(v any) any {
	switch t := v.(type) {
	case map[any]any:
		m := make(map[string]any, len(t))
		for k, vv := range t {
			m[fmt.Sprint(k)] = normalize(vv)
		}
		return m
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, vv := range t {
			m[k] = normalize(vv)
		}
		return m
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = normalize(t[i])
		}
		return out
	default:
		return v
	}
}

// SelectItems извлекает срез элементов из manifest по dot-пути selectPath.
func SelectItems(manifest any, selectPath string) ([]map[string]any, error) {
	p := strings.TrimSpace(selectPath)
	if p == "" {
		p = "items"
	}
	cur := manifest
	if p != "." {
		parts := strings.Split(p, ".")
		for _, part := range parts {
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("matrix: select '%s' expects an object before '%s'", selectPath, part)
			}
			v, ok := m[part]
			if !ok {
				return nil, fmt.Errorf("matrix: key '%s' not found in manifest", part)
			}
			cur = v
		}
	}
	arr, ok := cur.([]any)
	if !ok {
		return nil, fmt.Errorf("matrix: selected path '%s' is not an array", selectPath)
	}
	out := make([]map[string]any, 0, len(arr))
	for i, it := range arr {
		switch vv := it.(type) {
		case map[string]any:
			out = append(out, vv)
		default:
			return nil, fmt.Errorf("matrix: item at index %d is not an object", i)
		}
	}
	return out, nil
}
