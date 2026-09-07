package archive

import (
	"io"
	"time"
)

// Progress 描述任务执行的当前进度快照。
type Progress struct {
	ProcessedBytes   int64
	TotalBytes       int64
	ProcessedEntries int
	TotalEntries     int
	CurrentEntry     string
}

// Percent 返回 0-100 进度；totalBytes 未知（0）时返回 -1（不确定）。
func (p Progress) Percent() int {
	if p.TotalBytes <= 0 {
		return -1
	}
	v := int(float64(p.ProcessedBytes) / float64(p.TotalBytes) * 100)
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return v
}

// ProgressFn 由调用方提供，worker 用其把进度写回 DB（节流）。
type ProgressFn func(p Progress)

const progressThrottle = 256 * time.Millisecond

type tracker struct {
	totalBytes       int64
	totalEntries     int
	processedBytes   int64
	processedEntries int
	current          string
	fn               ProgressFn
	last             time.Time
}

func newTracker(totalBytes int64, totalEntries int, fn ProgressFn) *tracker {
	return &tracker{
		totalBytes:   totalBytes,
		totalEntries: totalEntries,
		fn:           fn,
		last:         time.Now(),
	}
}

func (t *tracker) setCurrent(name string) {
	t.current = name
	t.notify(true)
}

func (t *tracker) addBytes(n int64) {
	if n <= 0 {
		return
	}
	t.processedBytes += n
	t.notify(false)
}

func (t *tracker) entryDone() {
	t.processedEntries++
	t.notify(true)
}

func (t *tracker) snapshot() Progress {
	return Progress{
		ProcessedBytes:   t.processedBytes,
		TotalBytes:       t.totalBytes,
		ProcessedEntries: t.processedEntries,
		TotalEntries:     t.totalEntries,
		CurrentEntry:     t.current,
	}
}

func (t *tracker) notify(force bool) {
	now := time.Now()
	if force || now.Sub(t.last) >= progressThrottle {
		t.last = now
		if t.fn != nil {
			t.fn(t.snapshot())
		}
	}
}

// countWriter 包装一个 io.Writer，统计写入字节数并上报进度。
type countWriter struct {
	w io.Writer
	t *tracker
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 {
		c.t.addBytes(int64(n))
	}
	return n, err
}
