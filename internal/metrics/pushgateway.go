package metrics

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// PushGateway sends Prometheus text exposition to a Pushgateway-compatible endpoint.
func PushGateway(ctx context.Context, baseURL, job string, grouping map[string]string, body string) error {
	endpoint, err := pushgatewayEndpoint(baseURL, job, grouping)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewBufferString(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("metrics: pushgateway request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("metrics: pushgateway returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(preview)))
	}
	return nil
}

func pushgatewayEndpoint(baseURL, job string, grouping map[string]string) (string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return "", fmt.Errorf("metrics: pushgateway URL is empty")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("metrics: invalid pushgateway URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("metrics: pushgateway URL must include scheme and host")
	}
	job = strings.TrimSpace(job)
	if job == "" {
		job = "pipelineai"
	}

	path := strings.TrimRight(parsed.Path, "/") + "/metrics/job/" + url.PathEscape(job)
	keys := make([]string, 0, len(grouping))
	for key := range grouping {
		if sanitizeLabelName(key) != "" && strings.TrimSpace(grouping[key]) != "" {
			keys = append(keys, sanitizeLabelName(key))
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		path += "/" + url.PathEscape(key) + "/" + url.PathEscape(strings.TrimSpace(grouping[key]))
	}
	parsed.Path = path
	return parsed.String(), nil
}
