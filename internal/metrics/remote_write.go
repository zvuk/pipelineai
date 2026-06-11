package metrics

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/golang/snappy"
)

const remoteWriteVersion = "0.1.0"

type remoteWriteLabel struct {
	name  string
	value string
}

// RemoteWrite sends collected samples using Prometheus remote_write v1.
func RemoteWrite(ctx context.Context, endpoint string, samples []Sample, common map[string]string, timestamp time.Time) error {
	endpoint, err := remoteWriteEndpoint(endpoint)
	if err != nil {
		return err
	}
	body := renderRemoteWriteV1(samples, common, timestamp)
	if len(body) == 0 {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(snappy.Encode(nil, body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("X-Prometheus-Remote-Write-Version", remoteWriteVersion)
	req.Header.Set("User-Agent", "pipelineai")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("metrics: remote write request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("metrics: remote write returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(preview)))
	}
	return nil
}

func remoteWriteEndpoint(endpoint string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", fmt.Errorf("metrics: remote write URL is empty")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("metrics: invalid remote write URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("metrics: remote write URL must include scheme and host")
	}
	return parsed.String(), nil
}

func renderRemoteWriteV1(samples []Sample, common map[string]string, timestamp time.Time) []byte {
	var req bytes.Buffer
	tsMillis := timestamp.UnixMilli()
	for _, sample := range mergeDuplicateSamples(samples, common, nil) {
		name := sanitizeMetricName(sample.Name)
		if name == "" || math.IsNaN(sample.Value) || math.IsInf(sample.Value, 0) {
			continue
		}
		var series bytes.Buffer
		for _, label := range remoteWriteLabels(name, sample.Labels) {
			var labelMsg bytes.Buffer
			writeStringField(&labelMsg, 1, label.name)
			writeStringField(&labelMsg, 2, label.value)
			writeBytesField(&series, 1, labelMsg.Bytes())
		}
		var sampleMsg bytes.Buffer
		writeDoubleField(&sampleMsg, 1, sample.Value)
		writeInt64Field(&sampleMsg, 2, tsMillis)
		writeBytesField(&series, 2, sampleMsg.Bytes())
		writeBytesField(&req, 1, series.Bytes())
	}
	return req.Bytes()
}

func remoteWriteLabels(metricName string, labels map[string]string) []remoteWriteLabel {
	out := make([]remoteWriteLabel, 0, len(labels)+1)
	out = append(out, remoteWriteLabel{name: "__name__", value: metricName})
	for key, value := range labels {
		key = sanitizeLabelName(key)
		value = strings.TrimSpace(value)
		if key == "" || key == "__name__" || value == "" {
			continue
		}
		out = append(out, remoteWriteLabel{name: key, value: value})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].name < out[j].name
	})
	return out
}

func writeBytesField(buf *bytes.Buffer, field int, value []byte) {
	writeVarint(buf, uint64(field<<3|2))
	writeVarint(buf, uint64(len(value)))
	buf.Write(value)
}

func writeStringField(buf *bytes.Buffer, field int, value string) {
	writeBytesField(buf, field, []byte(value))
}

func writeDoubleField(buf *bytes.Buffer, field int, value float64) {
	writeVarint(buf, uint64(field<<3|1))
	var raw [8]byte
	binary.LittleEndian.PutUint64(raw[:], math.Float64bits(value))
	buf.Write(raw[:])
}

func writeInt64Field(buf *bytes.Buffer, field int, value int64) {
	writeVarint(buf, uint64(field<<3))
	writeVarint(buf, uint64(value))
}

func writeVarint(buf *bytes.Buffer, value uint64) {
	for value >= 0x80 {
		buf.WriteByte(byte(value) | 0x80)
		value >>= 7
	}
	buf.WriteByte(byte(value))
}
