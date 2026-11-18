// display.go draws the live progress region: a tml view pinned below the log.

package engine

import (
	"embed"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"sync"
	"time"

	"github.com/wow-look-at-my/tml"
	"github.com/wow-look-at-my/tml/sema"
	"golang.org/x/term"
)

var progressInterval = 200 * time.Millisecond

// maxTransferRows caps the rows of files in flight. The rest fold into one "+N more" row.
const maxTransferRows = 8

//go:embed ui/progress
var progressUI embed.FS

// progressView is the component the region draws, loaded once per process.
var progressView = sync.OnceValues(func() (*tml.View, error) {
	sub, err := fs.Sub(progressUI, "ui/progress")
	if err != nil {
		return nil, err
	}
	return tml.Load(sub, "Progress.tml", tml.Options{Dark: true})
})

// display is the region on stderr. When stdoutShared is true, stdout is the same terminal, so
// its lines go above the region too.
type display struct {
	live         *tml.Live
	stdoutShared bool
}

// progressEnabled reports whether to draw the live progress region. Default (auto)
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
		return isTerminal(r.stderr)
	}
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func (r *runner) runProgress(stop <-chan struct{}) {
	if !r.progressEnabled() {
		return
	}
	live, err := r.newRegion()
	if err != nil {
		r.warn("progress display: %v", err)
		return
	}
	r.outMu.Lock()
	r.display = &display{live: live, stdoutShared: isTerminal(r.stdout)}
	r.outMu.Unlock()
	<-stop
	r.outMu.Lock()
	defer r.outMu.Unlock()
	r.stopRegion()
}

// newRegion starts a region. Ctrl-C belongs to the engine, which drains the queue under it.
func (r *runner) newRegion() (*tml.Live, error) {
	view, err := progressView()
	if err != nil {
		return nil, err
	}
	live := tml.NewLive(view, func() tml.Props { return r.progressProps(time.Now()) }, tml.LiveOptions{
		Output:      r.stderr,
		Interval:    progressInterval,
		HostSignals: true,
	})
	if err := live.Start(); err != nil {
		return nil, err
	}
	return live, nil
}

// stopRegion prints the pending lines and takes the region off the screen. The caller holds outMu.
func (r *runner) stopRegion() {
	if r.display == nil {
		return
	}
	if err := r.display.live.Stop(); err != nil {
		fmt.Fprintf(r.stderr, "basicopy: warning: progress display: %v\n", err)
	}
	r.display = nil
}

// rateMeter smooths the byte rate between frames. Only the loop of the region touches it.
type rateMeter struct {
	rate     float64
	prev     int64
	prevTime time.Time
}

func (m *rateMeter) update(moved int64, now time.Time) float64 {
	if !m.prevTime.IsZero() {
		if dt := now.Sub(m.prevTime).Seconds(); dt > 0 {
			m.rate = 0.4*float64(moved-m.prev)/dt + 0.6*m.rate
		}
	}
	m.prev, m.prevTime = moved, now
	return m.rate
}

// progressProps is one frame: the counts, the phase of the run, a row per file in
// flight, and the TOTAL row.
func (r *runner) progressProps(now time.Time) tml.Props {
	files, totalFiles := r.files.Load(), r.totalFiles.Load()
	skipped, failed := r.skipped.Load(), r.failed.Load()
	totalBytes := r.totalBytes.Load()
	raw := r.moved.Load()
	moved := raw
	if totalBytes > 0 && moved > totalBytes {
		moved = totalBytes
	}
	scanning := r.scanning.Load()
	current := r.meter.update(raw, now)
	avg := r.averageRate(raw, now)

	// While the walk runs the totals are a lower bound: "+" marks them, and the ETA is a minimum.
	more := ""
	if scanning {
		more = "+"
	}

	head := fmt.Sprintf("basicopy: %d/%d%s files", files, totalFiles, more)
	if skipped > 0 {
		head += fmt.Sprintf(", %d skipped", skipped)
	}
	headStyle := "head"
	if failed > 0 {
		head += fmt.Sprintf(", %d failed", failed)
		headStyle = "head.failed"
	}
	if r.gate != nil {
		head += fmt.Sprintf(" · %d workers", r.gate.current())
	}
	if avg > 0 {
		head += fmt.Sprintf(" · avg %s/s", fmtBytes(int64(avg)))
	}

	status, statusStyle := r.progressStatus(scanning, files, totalFiles, moved, totalBytes)

	var rows []map[string]sema.Value
	active := r.active.snapshot()
	for i, t := range active {
		if i == maxTransferRows {
			rows = append(rows, map[string]sema.Value{
				"label": sema.StringValue(fmt.Sprintf("+%d more", len(active)-maxTransferRows)),
				"state": sema.StringValue("more"),
				"known": sema.BoolValue(false),
			})
			break
		}
		rows = append(rows, transferRow(t, now))
	}
	rows = append(rows, totalRow(files, totalFiles, moved, totalBytes, current, avg, more))

	return tml.Props{
		"head":        sema.StringValue(head),
		"headStyle":   sema.StringValue(headStyle),
		"status":      sema.StringValue(status),
		"statusStyle": sema.StringValue(statusStyle),
		"rows":        sema.RecordListValue(rows),
	}
}

// progressStatus names the phase of the run, or "" while it only copies.
func (r *runner) progressStatus(scanning bool, files, totalFiles, moved, totalBytes int64) (string, string) {
	if r.draining.Load() {
		return "interrupted: finishing the queue (Ctrl-C again to quit now)", "status"
	}
	dirMetaDone, dirMetaTotal := r.dirMetaDone.Load(), r.dirMetaTotal.Load()
	if !scanning && dirMetaTotal > 0 && dirMetaDone < dirMetaTotal &&
		(totalFiles == 0 || files >= totalFiles) &&
		(totalBytes == 0 || moved >= totalBytes) {
		pct := 100 * float64(dirMetaDone) / float64(dirMetaTotal)
		return fmt.Sprintf("metadata %d/%d dirs (%.1f%%)", dirMetaDone, dirMetaTotal, pct), "status"
	}
	if scanning {
		return "scanning the source; the totals still grow", "status.dim"
	}
	return "", "status"
}

// transferRow is a file in flight. An empty file has no fraction, so the row hides its bar.
func transferRow(t *transfer, now time.Time) map[string]sema.Value {
	done := min(t.done.Load(), t.size)
	row := map[string]sema.Value{
		"label": sema.StringValue(t.label),
		"state": sema.StringValue("active"),
		"known": sema.BoolValue(t.size > 0),
		"sizes": sema.StringValue(fmt.Sprintf("%10s / %s", fmtBytes(done), fmtBytes(t.size))),
	}
	if t.size > 0 {
		fraction := float64(done) / float64(t.size)
		row["value"] = sema.StringValue(fmt.Sprintf("%.4f", fraction))
		row["percent"] = sema.StringValue(fmt.Sprintf("%3.0f%%", fraction*100))
	}
	if elapsed := now.Sub(t.start).Seconds(); elapsed > 0 && done > 0 {
		speed := float64(done) / elapsed
		row["speed"] = sema.StringValue(fmtBytes(int64(speed)) + "/s")
		row["eta"] = sema.StringValue("ETA " + fmtETA(t.size-done, speed))
	}
	return row
}

// totalRow is the whole run. Its fraction is bytes, or files when every file is empty.
func totalRow(files, totalFiles, moved, totalBytes int64, current, avg float64, more string) map[string]sema.Value {
	right := "?"
	if totalBytes > 0 {
		right = fmtBytes(totalBytes) + more
	}
	row := map[string]sema.Value{
		"label": sema.StringValue("TOTAL"),
		"state": sema.StringValue("total"),
		"sizes": sema.StringValue(fmt.Sprintf("%10s / %s", fmtBytes(moved), right)),
		"speed": sema.StringValue(fmtBytes(int64(current)) + "/s"),
	}
	fraction := -1.0
	switch {
	case totalBytes > 0:
		fraction = float64(moved) / float64(totalBytes)
		row["eta"] = sema.StringValue("ETA " + fmtETA(totalBytes-moved, avg) + more)
	case totalFiles > 0:
		fraction = float64(files) / float64(totalFiles)
	}
	row["known"] = sema.BoolValue(fraction >= 0)
	if fraction >= 0 {
		fraction = math.Min(fraction, 1)
		row["value"] = sema.StringValue(fmt.Sprintf("%.4f", fraction))
		row["percent"] = sema.StringValue(fmt.Sprintf("%3.0f%%", fraction*100))
	}
	return row
}
