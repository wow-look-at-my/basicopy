// progress.go holds the line splitter and the byte, ETA and duration formats the display uses.

package engine

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

func (r *runner) averageRate(moved int64, now time.Time) float64 {
	if moved <= 0 || r.startedAt.IsZero() {
		return 0
	}
	if elapsed := now.Sub(r.startedAt).Seconds(); elapsed > 0 {
		return float64(moved) / elapsed
	}
	return 0
}

// lineWriter hands each complete line written to it to r.printLine on stderr.
type lineWriter struct {
	r   *runner
	mu  sync.Mutex
	buf []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := strings.TrimRight(string(w.buf[:i]), "\r")
		w.buf = w.buf[i+1:]
		w.r.printLine(w.r.stderr, "%s", line)
	}
}

func fmtBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func fmtETA(remaining int64, rate float64) string {
	if remaining <= 0 {
		return "0s"
	}
	if rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return "--"
	}
	return fmtDuration(time.Duration(float64(remaining) / rate * float64(time.Second)))
}

func fmtDuration(d time.Duration) string {
	if d < time.Second {
		d = time.Second
	}
	d = d.Round(time.Second)
	h := int(d / time.Hour)
	d -= time.Duration(h) * time.Hour
	m := int(d / time.Minute)
	d -= time.Duration(m) * time.Minute
	s := int(d / time.Second)
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}
