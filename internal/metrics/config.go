package metrics

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config describes optional PipelineAI metrics export.
type Config struct {
	Enabled        bool
	PushgatewayURL string
	PushgatewayJob string
	RemoteWriteURL string
	FilePath       string
	ExtraJSONPath  string
	Labels         map[string]string
	GroupingLabels map[string]string
	Timeout        time.Duration
}

// LoadConfigFromEnv reads metrics settings from environment variables.
func LoadConfigFromEnv() Config {
	enabled, enabledSet := lookupBoolEnv("PAI_METRICS_ENABLED")
	pushgatewayURL := strings.TrimSpace(os.Getenv("PAI_METRICS_PUSHGATEWAY_URL"))
	remoteWriteURL := strings.TrimSpace(os.Getenv("PAI_METRICS_REMOTE_WRITE_URL"))
	filePath := strings.TrimSpace(os.Getenv("PAI_METRICS_FILE"))
	if !enabledSet {
		enabled = pushgatewayURL != "" || remoteWriteURL != "" || filePath != ""
	}

	cfg := Config{
		Enabled:        enabled,
		PushgatewayURL: pushgatewayURL,
		PushgatewayJob: envOrDefault("PAI_METRICS_PUSHGATEWAY_JOB", "pipelineai"),
		RemoteWriteURL: remoteWriteURL,
		FilePath:       filePath,
		ExtraJSONPath:  strings.TrimSpace(os.Getenv("PAI_METRICS_EXTRA_JSON")),
		Labels:         parseLabels(os.Getenv("PAI_METRICS_LABELS")),
		GroupingLabels: parseLabels(os.Getenv("PAI_METRICS_GROUPING_LABELS")),
		Timeout:        parseDurationEnv("PAI_METRICS_TIMEOUT", 5*time.Second),
	}

	runID := strings.TrimSpace(os.Getenv("PAI_METRICS_RUN_ID"))
	if runID == "" {
		runID = newRunID()
	}
	cfg.Labels["run_id"] = runID
	if parseBoolEnv("PAI_METRICS_GROUP_BY_RUN", true) {
		cfg.GroupingLabels["run_id"] = runID
	}

	if app := strings.TrimSpace(os.Getenv("APP_NAME")); app != "" {
		cfg.Labels["app"] = app
	}
	if env := strings.TrimSpace(os.Getenv("ENV")); env != "" {
		cfg.Labels["env"] = env
	}

	return cfg
}

func envOrDefault(name string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func parseBoolEnv(name string, fallback bool) bool {
	value, ok := lookupBoolEnv(name)
	if !ok {
		return fallback
	}
	return value
}

func lookupBoolEnv(name string) (bool, bool) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return false, false
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "y", "on":
		return true, true
	case "0", "false", "no", "n", "off":
		return false, true
	default:
		return false, false
	}
}

func parseDurationEnv(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func parseLabels(raw string) map[string]string {
	labels := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key = sanitizeLabelName(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		labels[key] = value
	}
	return labels
}

func newRunID() string {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err == nil {
		return strconv.FormatInt(time.Now().UTC().UnixNano(), 10) + "-" + hex.EncodeToString(buf[:])
	}
	return strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
}
