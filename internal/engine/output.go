// printLine writes one full line.

package engine

import (
	"fmt"
	"io"
)

// printLine writes one full line. While the progress region is up, a line for its
// terminal goes above the region, so both never share a row.
func (r *runner) printLine(w io.Writer, format string, a ...any) {
	r.outMu.Lock()
	defer r.outMu.Unlock()
	if d := r.display; d != nil && (w == r.stderr || d.stdoutShared) {
		d.live.Printf(format, a...)
		return
	}
	fmt.Fprintf(w, format+"\n", a...)
}

func (r *runner) elog(format string, a ...any) {
	r.printLine(r.stderr, format, a...)
}

func (r *runner) warn(format string, a ...any) {
	if r.opts.Quiet {
		return
	}
	r.elog("basicopy: warning: "+format, a...)
}

func (r *runner) note(format string, a ...any) {
	if r.opts.Quiet {
		return
	}
	r.elog("basicopy: "+format, a...)
}

// verbose prints a low-interest per-entry line ("skip unchanged", "exclude")
// under --verbose only; --json suppresses it too.
func (r *runner) verbose(format string, a ...any) {
	if !r.opts.Verbose || r.opts.JSON {
		return
	}
	r.printLine(r.stdout, format, a...)
}

// item prints a per-entry action line. In a dry run it IS the output and
// prints by default; a real run needs --verbose. --quiet/--json suppress it.
func (r *runner) item(format string, a ...any) {
	if r.opts.Quiet || r.opts.JSON || (!r.opts.DryRun && !r.opts.Verbose) {
		return
	}
	r.printLine(r.stdout, format, a...)
}

// would returns the "would " itemize prefix in a dry run, empty otherwise, for
// action lines that share their format between dry and real runs.
func (r *runner) would() string {
	if r.opts.DryRun {
		return "would "
	}
	return ""
}
