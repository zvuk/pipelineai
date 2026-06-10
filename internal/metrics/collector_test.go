package metrics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestRenderTextMergesAndEscapesLabels(t *testing.T) {
	body := RenderText([]Sample{{
		Name:  "pipelineai_test_metric",
		Help:  "test metric",
		Type:  "gauge",
		Value: 1,
		Labels: map[string]string{
			`bad.label`: `value "quoted"`,
		},
	}}, map[string]string{"run_id": "run-1"}, nil)

	if !strings.Contains(body, "# HELP pipelineai_test_metric test metric") {
		t.Fatalf("missing HELP line: %s", body)
	}
	if !strings.Contains(body, `bad_label="value \"quoted\""`) {
		t.Fatalf("missing escaped label: %s", body)
	}
	if !strings.Contains(body, `run_id="run-1"`) {
		t.Fatalf("missing common label: %s", body)
	}
}

func TestRenderTextAggregatesDuplicateCounters(t *testing.T) {
	body := RenderText([]Sample{
		{
			Name:  "pipelineai_test_total",
			Type:  "counter",
			Value: 1,
			Labels: map[string]string{
				"status": "ok",
			},
		},
		{
			Name:  "pipelineai_test_total",
			Type:  "counter",
			Value: 2,
			Labels: map[string]string{
				"status": "ok",
			},
		},
	}, map[string]string{"run_id": "run-1"}, nil)

	if strings.Count(body, "pipelineai_test_total{") != 1 {
		t.Fatalf("duplicate counter samples were not merged: %s", body)
	}
	if !strings.Contains(body, `pipelineai_test_total{run_id="run-1",status="ok"} 3`) {
		t.Fatalf("counter samples were not aggregated: %s", body)
	}
}

func TestCollectorFlushesToFileAndPushgateway(t *testing.T) {
	var gotPath string
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
	}))
	defer server.Close()

	dir := t.TempDir()
	filePath := dir + "/metrics.prom"
	collector := New(Config{
		Enabled:        true,
		PushgatewayURL: server.URL,
		PushgatewayJob: "pipelineai-test",
		FilePath:       filePath,
		Labels: map[string]string{
			"run_id": "run-1",
			"env":    "test",
		},
		GroupingLabels: map[string]string{"run_id": "run-1"},
	}, nil)
	collector.Observe("pipelineai_test_metric", "test metric", "gauge", 2, map[string]string{"status": "ok"})

	if err := collector.Flush(context.Background()); err != nil {
		t.Fatalf("flush failed: %v", err)
	}
	fileBody, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read metrics file: %v", err)
	}
	if !strings.Contains(string(fileBody), `run_id="run-1"`) {
		t.Fatalf("file output must include run_id label: %s", string(fileBody))
	}
	if strings.Contains(gotBody, `run_id="run-1"`) {
		t.Fatalf("push body must not duplicate grouping labels: %s", gotBody)
	}
	if gotPath != "/metrics/job/pipelineai-test/run_id/run-1" {
		t.Fatalf("unexpected push path: %s", gotPath)
	}
}
