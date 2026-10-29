// The report for an extended attribute that a copy leaves behind.

package fsx

import "sync/atomic"

// xattrSkipReport receives each attribute the copy skips.
var xattrSkipReport atomic.Pointer[func(path, name string, err error)]

// SetXattrSkipReport sets the receiver for skipped attributes. nil removes it.
func SetXattrSkipReport(report func(path, name string, err error)) {
	if report == nil {
		xattrSkipReport.Store(nil)
		return
	}
	xattrSkipReport.Store(&report)
}

func xattrSkipped(path, name string, err error) {
	if report := xattrSkipReport.Load(); report != nil {
		(*report)(path, name, err)
	}
}
