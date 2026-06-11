package metrics

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// RenderText renders samples in Prometheus text exposition format.
func RenderText(samples []Sample, common map[string]string, exclude map[string]string) string {
	var buf bytes.Buffer
	seenMeta := map[string]struct{}{}
	for _, sample := range mergeDuplicateSamples(samples, common, exclude) {
		name := sanitizeMetricName(sample.Name)
		if name == "" {
			continue
		}
		if _, ok := seenMeta[name]; !ok {
			if help := strings.TrimSpace(sample.Help); help != "" {
				fmt.Fprintf(&buf, "# HELP %s %s\n", name, escapeHelp(help))
			}
			typ := strings.TrimSpace(sample.Type)
			if typ == "" {
				typ = typeGauge
			}
			fmt.Fprintf(&buf, "# TYPE %s %s\n", name, typ)
			seenMeta[name] = struct{}{}
		}
		fmt.Fprintf(&buf, "%s%s %s\n", name, renderLabels(sample.Labels), formatFloat(sample.Value))
	}
	return buf.String()
}

func mergeDuplicateSamples(samples []Sample, common map[string]string, exclude map[string]string) []Sample {
	merged := make([]Sample, 0, len(samples))
	indexByKey := map[string]int{}
	for _, sample := range samples {
		name := sanitizeMetricName(sample.Name)
		if name == "" {
			continue
		}
		sample.Name = name
		if strings.TrimSpace(sample.Type) == "" {
			sample.Type = typeGauge
		}
		sample.Labels = mergeLabels(common, sample.Labels, exclude)
		key := name + "\xff" + labelsKey(sample.Labels)
		if idx, ok := indexByKey[key]; ok {
			if sample.Type == typeCounter || strings.HasSuffix(sample.Name, "_total") {
				merged[idx].Value += sample.Value
			} else {
				merged[idx].Value = sample.Value
			}
			continue
		}
		indexByKey[key] = len(merged)
		merged = append(merged, sample)
	}
	return merged
}

func mergeLabels(common map[string]string, labels map[string]string, exclude map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range common {
		key := sanitizeLabelName(k)
		if key == "" || strings.TrimSpace(v) == "" {
			continue
		}
		if _, skip := exclude[key]; skip {
			continue
		}
		out[key] = strings.TrimSpace(v)
	}
	for k, v := range labels {
		key := sanitizeLabelName(k)
		if key == "" || strings.TrimSpace(v) == "" {
			continue
		}
		if _, skip := exclude[key]; skip {
			continue
		}
		out[key] = strings.TrimSpace(v)
	}
	return out
}

func renderLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, key, escapeLabelValue(labels[key])))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func labelsKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+labels[key])
	}
	return strings.Join(parts, "\xff")
}

func sanitizeMetricName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	var b strings.Builder
	for i, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || r == ':' || (i > 0 && r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		if i == 0 && r >= '0' && r <= '9' {
			b.WriteRune('_')
			b.WriteRune(r)
			continue
		}
		if unicode.IsLetter(r) || (i > 0 && unicode.IsDigit(r)) {
			b.WriteRune(r)
			continue
		}
		b.WriteRune('_')
	}
	return b.String()
}

func sanitizeLabelName(name string) string {
	return sanitizeMetricName(name)
}

func escapeHelp(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return value
}

func escapeLabelValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}

func formatFloat(value float64) string {
	return strconvFormatFloat(value)
}
