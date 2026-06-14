package engine

import (
	"context"
	"fmt"
	"math"
	"os"
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

// runProgress draws a periodic one-line status to stderr (rate is an EWMA of the
// byte throughput) and clears the line on exit so the final summary prints clean.
func (r *runner) runProgress(ctx context.Context, stop <-chan struct{}) {
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
		case <-ctx.Done():
			r.clearProgressLine()
			return
		case <-stop:
			r.clearProgressLine()
			return
		case now := <-ticker.C:
			nb := r.moved.Load()
			if dt := now.Sub(prevTime).Seconds(); dt > 0 {
				rate = 0.4*float64(nb-prevBytes)/dt + 0.6*rate
			}
			prevBytes, prevTime = nb, now
			r.outMu.Lock()
			fmt.Fprintf(r.stderr, "\r\033[K%s", r.progressLine(nb, rate))
			r.outMu.Unlock()
		}
	}
}

func (r *runner) progressLine(moved int64, rate float64) string {
	files, totalFiles := r.files.Load(), r.totalFiles.Load()
	totalBytes := r.totalBytes.Load()
	displayMoved := moved
	if totalBytes > 0 && displayMoved > totalBytes {
		displayMoved = totalBytes
	}
	speed := fmt.Sprintf("%s/s", fmtBytes(int64(rate)))

	if totalBytes > 0 {
		pct := 100 * float64(displayMoved) / float64(totalBytes)
		if pct > 100 {
			pct = 100
		}
		if totalFiles > 0 {
			return fmt.Sprintf("%d/%d files, %s/%s (%.1f%%), %s, ETA %s",
				files, totalFiles, fmtBytes(displayMoved), fmtBytes(totalBytes), pct, speed, fmtETA(totalBytes-displayMoved, rate))
		}
		return fmt.Sprintf("%s/%s (%.1f%%), %s, ETA %s",
			fmtBytes(displayMoved), fmtBytes(totalBytes), pct, speed, fmtETA(totalBytes-displayMoved, rate))
	}

	if totalFiles > 0 {
		pct := 100 * float64(files) / float64(totalFiles)
		if pct > 100 {
			pct = 100
		}
		return fmt.Sprintf("%d/%d files, %s (%.1f%%), %s, ETA 0s",
			files, totalFiles, fmtBytes(displayMoved), pct, speed)
	}

	return fmt.Sprintf("%d files, %s, %s", files, fmtBytes(displayMoved), speed)
}

func (r *runner) clearProgressLine() {
	r.outMu.Lock()
	fmt.Fprint(r.stderr, "\r\033[K")
	r.outMu.Unlock()
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
