package shell

import (
	"bytes"
	"fmt"
	"os"
	"strings"
)

type boundedCollector struct {
	maxBytes        int
	headLimit       int
	tailLimit       int
	persistOverflow bool
	persistAlways   bool

	captured   bytes.Buffer
	head       bytes.Buffer
	tail       tailBuffer
	totalBytes int64
	totalLines int64
	truncated  bool
	spillFile  *os.File
	spillPath  string
}

func newBoundedCollector(maxBytes int, persistOverflow bool, persistAlways bool) *boundedCollector {
	if maxBytes <= 0 {
		maxBytes = 256 * 1024
	}
	headLimit := maxBytes / 2
	tailLimit := maxBytes - headLimit
	if headLimit <= 0 {
		headLimit = maxBytes
	}
	if tailLimit <= 0 {
		tailLimit = headLimit
	}
	return &boundedCollector{
		maxBytes:        maxBytes,
		headLimit:       headLimit,
		tailLimit:       tailLimit,
		persistOverflow: persistOverflow,
		persistAlways:   persistAlways,
		tail:            tailBuffer{limit: tailLimit},
	}
}

func (c *boundedCollector) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if c.persistAlways {
		if err := c.ensureSpill(false, nil); err != nil {
			return 0, err
		}
		if _, err := c.spillFile.Write(p); err != nil {
			return 0, err
		}
	}

	previousTotal := c.totalBytes
	c.totalBytes += int64(len(p))
	c.totalLines += int64(bytes.Count(p, []byte{'\n'}))

	c.appendHead(p)
	c.tail.Write(p)

	if !c.truncated {
		if previousTotal+int64(len(p)) <= int64(c.maxBytes) {
			_, _ = c.captured.Write(p)
			return len(p), nil
		}
		c.truncated = true
		if c.persistOverflow && !c.persistAlways {
			if err := c.ensureSpill(true, p); err != nil {
				return 0, err
			}
			return len(p), nil
		}
		return len(p), nil
	}

	if c.persistOverflow && !c.persistAlways {
		if err := c.ensureSpill(false, nil); err != nil {
			return 0, err
		}
		if _, err := c.spillFile.Write(p); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (c *boundedCollector) appendHead(p []byte) {
	if c.head.Len() >= c.headLimit {
		return
	}
	remaining := c.headLimit - c.head.Len()
	if remaining <= 0 {
		return
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	_, _ = c.head.Write(p)
}

func (c *boundedCollector) ensureSpill(seedCurrent bool, current []byte) error {
	if c.spillFile != nil {
		if seedCurrent && len(current) > 0 {
			_, err := c.spillFile.Write(current)
			return err
		}
		return nil
	}
	file, err := os.CreateTemp("", "pipelineai-shell-capture-*")
	if err != nil {
		return fmt.Errorf("shell tool: failed to create temporary capture file: %w", err)
	}
	if _, err := file.Write(c.captured.Bytes()); err != nil {
		file.Close()
		_ = os.Remove(file.Name())
		return fmt.Errorf("shell tool: failed to seed capture file: %w", err)
	}
	if seedCurrent && len(current) > 0 {
		if _, err := file.Write(current); err != nil {
			file.Close()
			_ = os.Remove(file.Name())
			return fmt.Errorf("shell tool: failed to append capture file: %w", err)
		}
	}
	c.spillFile = file
	c.spillPath = file.Name()
	return nil
}

func (c *boundedCollector) Close() error {
	if c.spillFile == nil {
		return nil
	}
	err := c.spillFile.Close()
	c.spillFile = nil
	return err
}

func (c *boundedCollector) Preview() string {
	if !c.truncated {
		return c.captured.String()
	}
	head := strings.TrimRight(c.head.String(), "\n")
	tail := strings.TrimLeft(c.tail.String(), "\n")
	var b strings.Builder
	if head != "" {
		b.WriteString(head)
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "... output truncated; total_bytes=%d total_lines=%d ...", c.totalBytes, c.totalLines)
	if tail != "" {
		b.WriteString("\n")
		b.WriteString(tail)
	}
	return strings.TrimSpace(b.String())
}

func (c *boundedCollector) CapturePath() string {
	return strings.TrimSpace(c.spillPath)
}

type tailBuffer struct {
	limit int
	data  []byte
}

func (t *tailBuffer) Write(p []byte) {
	if t.limit <= 0 || len(p) == 0 {
		return
	}
	if len(p) >= t.limit {
		t.data = append(t.data[:0], p[len(p)-t.limit:]...)
		return
	}
	need := len(t.data) + len(p) - t.limit
	if need > 0 {
		t.data = append(t.data[:0], t.data[need:]...)
	}
	t.data = append(t.data, p...)
}

func (t *tailBuffer) String() string {
	return string(t.data)
}
