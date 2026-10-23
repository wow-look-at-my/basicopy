package engine

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

var progressInterval = 200 * time.Millisecond

// progressEnabled reports whether to draw a live progress line. Default (auto)
// shows it only when stderr is a terminal; --progress=always forces it and
// =never suppresses it. Quiet, JSON, and dry-run modes never draw it.
func (r *runner) progressEnabled() bool {
	if r.opts.Quiet || r.opts.JSON || r.opts.DryRun {
		return false
	}
	switch r.opts.Progress {
	case "never":
		return false
	case "always":
		return true
	default: // "auto" / ""
		return term.IsTerminal(int(os.Stderr.Fd()))
	}
}

func (r *runner) runProgress(stop <-chan struct{}) {
	if !r.progressEnabled() {
		return
	}
	ticker := time.NewTicker(progressInterval)
	defer ticker.Stop()

	var rate float64
	prevBytes := r.moved.Load()
	prevTime := time.Now()

	for {
		select {
		case <-stop:
			r.clearProgressLine()
			return
		case now := <-ticker.C:
			nb := r.moved.Load()
			if dt := now.Sub(prevTime).Seconds(); dt > 0 {
				rate = 0.4*float64(nb-prevBytes)/dt + 0.6*rate
			}
			prevBytes, prevTime = nb, now
			r.drawProgress(r.progressLine(nb, rate, now))
		}
	}
}

func (r *runner) progressLine(moved int64, currentRate float64, now time.Time) string {
	files, totalFiles := r.files.Load(), r.totalFiles.Load()
	totalBytes := r.totalBytes.Load()
	dirMetaDone, dirMetaTotal := r.dirMetaDone.Load(), r.dirMetaTotal.Load()
	displayMoved := moved
	if totalBytes > 0 && displayMoved > totalBytes {
		displayMoved = totalBytes
	}
	currentSpeed := fmt.Sprintf("%s/s current", fmtBytes(int64(currentRate)))
	avgRate := r.averageRate(moved, now)
	avgSpeed := fmt.Sprintf("%s/s avg", fmtBytes(int64(avgRate)))

	scanning := r.scanning.Load()
	if !scanning && dirMetaTotal > 0 && dirMetaDone < dirMetaTotal &&
		(totalFiles == 0 || files >= totalFiles) &&
		(totalBytes == 0 || displayMoved >= totalBytes) {
		pct := 100 * float64(dirMetaDone) / float64(dirMetaTotal)
		return fmt.Sprintf("metadata %d/%d dirs (%.1f%%)",
			dirMetaDone, dirMetaTotal, pct)
	}

	// While the walk runs the totals are a lower bound: "+" marks them, and the ETA is a minimum.
	more, tail := "", ""
	if scanning {
		more, tail = "+", ", scanning"
	}

	if totalBytes > 0 {
		pct := 100 * float64(displayMoved) / float64(totalBytes)
		if pct > 100 {
			pct = 100
		}
		eta := fmtETA(totalBytes-displayMoved, avgRate) + more
		if totalFiles > 0 {
			return fmt.Sprintf("%d/%d%s files, %s/%s%s (%.1f%%), %s, %s, ETA %s%s",
				files, totalFiles, more, fmtBytes(displayMoved), fmtBytes(totalBytes), more, pct, avgSpeed, currentSpeed, eta, tail)
		}
		return fmt.Sprintf("%s/%s%s (%.1f%%), %s, %s, ETA %s%s",
			fmtBytes(displayMoved), fmtBytes(totalBytes), more, pct, avgSpeed, currentSpeed, eta, tail)
	}

	if totalFiles > 0 {
		pct := 100 * float64(files) / float64(totalFiles)
		if pct > 100 {
			pct = 100
		}
		return fmt.Sprintf("%d/%d%s files, %s (%.1f%%), %s, %s, ETA 0s%s%s",
			files, totalFiles, more, fmtBytes(displayMoved), pct, avgSpeed, currentSpeed, more, tail)
	}

	return fmt.Sprintf("%d files, %s, %s, %s%s", files, fmtBytes(displayMoved), avgSpeed, currentSpeed, tail)
}

func (r *runner) averageRate(moved int64, now time.Time) float64 {
	if moved <= 0 || r.startedAt.IsZero() {
		return 0
	}
	if elapsed := now.Sub(r.startedAt).Seconds(); elapsed > 0 {
		return float64(moved) / elapsed
	}
	return 0
}

func (r *runner) drawProgress(line string) {
	r.outMu.Lock()
	r.progress = line
	fmt.Fprintf(r.stderr, "\r\033[K%s", line)
	r.outMu.Unlock()
}

func (r *runner) clearProgressLine() {
	r.outMu.Lock()
	r.progress = ""
	fmt.Fprint(r.stderr, "\r\033[K")
	r.outMu.Unlock()
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
