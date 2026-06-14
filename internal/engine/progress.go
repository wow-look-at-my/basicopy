package engine

import (
	"context"
	"fmt"
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
			fmt.Fprintf(r.stderr, "\r\033[K%d files, %s, %s/s",
				r.files.Load(), fmtBytes(nb), fmtBytes(int64(rate)))
			r.outMu.Unlock()
		}
	}
}

func (r *runner) clearProgressLine() {
	r.outMu.Lock()
	fmt.Fprint(r.stderr, "\r\033[K")
	r.outMu.Unlock()
}

func fmtBytes(n int64) string {
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
