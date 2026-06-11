package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	typeGauge   = "gauge"
	typeCounter = "counter"
)

// Sample is one Prometheus text exposition sample.
type Sample struct {
	Name   string            `json:"name"`
	Help   string            `json:"help,omitempty"`
	Type   string            `json:"type,omitempty"`
	Value  float64           `json:"value"`
	Labels map[string]string `json:"labels,omitempty"`
}

// Collector stores run-local metrics and flushes them to configured sinks.
type Collector struct {
	cfg    Config
	log    *slog.Logger
	mu     sync.Mutex
	common map[string]string
	items  []Sample
}

// New creates a metrics collector. A disabled collector is cheap and safe to use.
func New(cfg Config, log *slog.Logger) *Collector {
	if log == nil {
		log = slog.Default()
	}
	common := make(map[string]string, len(cfg.Labels))
	for k, v := range cfg.Labels {
		if key := sanitizeLabelName(k); key != "" && strings.TrimSpace(v) != "" {
			common[key] = strings.TrimSpace(v)
		}
	}
	return &Collector{
		cfg:    cfg,
		log:    log.With(slog.String("component", "metrics")),
		common: common,
	}
}

// Enabled reports whether at least one metrics sink is configured.
func (c *Collector) Enabled() bool {
	if c == nil {
		return false
	}
	return c.cfg.Enabled &&
		(strings.TrimSpace(c.cfg.PushgatewayURL) != "" ||
			strings.TrimSpace(c.cfg.RemoteWriteURL) != "" ||
			strings.TrimSpace(c.cfg.FilePath) != "")
}

// AddCommonLabel adds a label to all subsequent and rendered samples.
func (c *Collector) AddCommonLabel(key, value string) {
	if c == nil {
		return
	}
	key = sanitizeLabelName(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return
	}
	c.mu.Lock()
	c.common[key] = value
	c.mu.Unlock()
}

// Observe adds a metric sample.
func (c *Collector) Observe(name, help, typ string, value float64, labels map[string]string) {
	if c == nil || !c.cfg.Enabled {
		return
	}
	name = sanitizeMetricName(name)
	if name == "" || math.IsNaN(value) || math.IsInf(value, 0) {
		return
	}
	if typ == "" {
		typ = typeGauge
	}
	c.mu.Lock()
	c.items = append(c.items, Sample{
		Name:   name,
		Help:   strings.TrimSpace(help),
		Type:   typ,
		Value:  value,
		Labels: cleanLabels(labels),
	})
	c.mu.Unlock()
}

// Flush writes collected metrics to all configured sinks. Flush errors are returned
// after every sink has had a chance to run.
func (c *Collector) Flush(ctx context.Context) error {
	if c == nil || !c.Enabled() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if c.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.cfg.Timeout)
		defer cancel()
	}

	samples, common := c.snapshot()
	extra, err := readExtraSamples(c.cfg.ExtraJSONPath)
	if err != nil {
		c.log.WarnContext(ctx, "failed to read extra metrics", slog.String("error", err.Error()))
	} else if len(extra) > 0 {
		samples = append(samples, extra...)
	}
	if len(samples) == 0 {
		return nil
	}

	var errs []error
	if path := strings.TrimSpace(c.cfg.FilePath); path != "" {
		body := RenderText(samples, common, nil)
		if err := writeMetricsFile(path, body); err != nil {
			errs = append(errs, err)
		}
	}
	if endpoint := strings.TrimSpace(c.cfg.PushgatewayURL); endpoint != "" {
		grouping := c.groupingLabels(common)
		body := RenderText(samples, common, grouping)
		if err := PushGateway(ctx, endpoint, c.cfg.PushgatewayJob, grouping, body); err != nil {
			errs = append(errs, err)
		}
	}
	if endpoint := strings.TrimSpace(c.cfg.RemoteWriteURL); endpoint != "" {
		if err := RemoteWrite(ctx, endpoint, samples, common, time.Now().UTC()); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return joinErrors(errs)
	}
	return nil
}

func (c *Collector) snapshot() ([]Sample, map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	samples := append([]Sample(nil), c.items...)
	common := make(map[string]string, len(c.common))
	for k, v := range c.common {
		common[k] = v
	}
	return samples, common
}

func (c *Collector) groupingLabels(common map[string]string) map[string]string {
	grouping := make(map[string]string, len(c.cfg.GroupingLabels))
	for k, v := range c.cfg.GroupingLabels {
		key := sanitizeLabelName(k)
		value := strings.TrimSpace(v)
		if key == "" || value == "" {
			continue
		}
		grouping[key] = value
	}
	for key := range grouping {
		if value := strings.TrimSpace(common[key]); value != "" {
			grouping[key] = value
		}
	}
	return grouping
}

func cleanLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		key := sanitizeLabelName(k)
		value := strings.TrimSpace(v)
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func readExtraSamples(path string) ([]Sample, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var samples []Sample
	if err := json.Unmarshal(data, &samples); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	cleaned := make([]Sample, 0, len(samples))
	for _, sample := range samples {
		sample.Name = sanitizeMetricName(sample.Name)
		sample.Type = strings.TrimSpace(sample.Type)
		if sample.Type == "" {
			sample.Type = typeGauge
		}
		sample.Help = strings.TrimSpace(sample.Help)
		sample.Labels = cleanLabels(sample.Labels)
		if sample.Name == "" || math.IsNaN(sample.Value) || math.IsInf(sample.Value, 0) {
			continue
		}
		cleaned = append(cleaned, sample)
	}
	return cleaned, nil
}

func writeMetricsFile(path string, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

func joinErrors(errs []error) error {
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			parts = append(parts, err.Error())
		}
	}
	sort.Strings(parts)
	return fmt.Errorf("metrics: %s", strings.Join(parts, "; "))
}
