package engine

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// Tunables (vars so tests can shorten them).
var (
	watchInterval = 1 * time.Second
	stallTimeout  = 30 * time.Second
)

// stallDetector reports a stall when a monotonic progress figure stops advancing
// for at least timeout. It is pure (clock is injected) so it can be unit-tested.
type stallDetector struct {
	timeout time.Duration
	last    int64
	since   time.Time
	primed  bool
}

func (d *stallDetector) update(progress int64, now time.Time) (stalled bool) {
	if !d.primed || progress != d.last {
		d.last, d.since, d.primed = progress, now, true
		return false
	}
	return now.Sub(d.since) >= d.timeout
}

// progressCount is a monotonic measure of forward progress across all work kinds.
// It includes discovered copy work and moved (bytes written mid-copy), so neither
// a long discovery walk nor a single large in-flight file is mistaken for a stall.
func (r *runner) progressCount() int64 {
	return r.moved.Load() + r.totalBytes.Load() + r.totalFiles.Load() +
		r.files.Load() + r.dirs.Load() + r.symlinks.Load() +
		r.linked.Load() + r.deleted.Load() + r.skipped.Load() +
		r.dirMetaDone.Load()
}

// runWatchdog watches for a total lack of forward progress and, after stallTimeout,
// aborts (non-TTY) or prompts the user (TTY).
func (r *runner) runWatchdog(ctx context.Context, stop <-chan struct{}) {
	det := &stallDetector{timeout: stallTimeout}
	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case now := <-ticker.C:
			if !det.update(r.progressCount(), now) {
				continue
			}
			cont, next := r.onStall()
			if !cont {
				r.setAbort(fmt.Errorf("no forward progress for %s; aborting", det.timeout))
				return
			}
			det.timeout = next
			det.primed = false // restart the window
		}
	}
}

// onStall decides what to do when progress has stalled. Without a controlling
// terminal it aborts; with one it asks the user whether to keep waiting.
func (r *runner) onStall() (continueWaiting bool, next time.Duration) {
	if !isTerminal() {
		r.elog("basicopy: no forward progress for %s", stallTimeout)
		return false, 0
	}
	r.elog("basicopy: stalled for %s -- [c]ontinue 30s / [m]inutes (5) / [f]orever / [a]bort?", stallTimeout)
	var resp string
	_, _ = fmt.Fscanln(os.Stdin, &resp)
	switch strings.ToLower(strings.TrimSpace(resp)) {
	case "f", "forever":
		return true, time.Duration(math.MaxInt64)
	case "m", "minutes":
		return true, 5 * time.Minute
	case "a", "abort":
		return false, 0
	default:
		return true, stallTimeout
	}
}

func isTerminal() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
