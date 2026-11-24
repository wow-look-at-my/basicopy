// Vars, not consts, so tests can shorten them.

package engine

import (
	"runtime"
	"time"

	"github.com/wow-look-at-my/basicopy/internal/control"
	"github.com/wow-look-at-my/basicopy/internal/device"
	"github.com/wow-look-at-my/basicopy/internal/sysload"
	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// Vars, not consts, so tests can shorten them. Connections resize less often.
var (
	controlInterval = 250 * time.Millisecond
	connInterval    = 500 * time.Millisecond
)

// scaler is the controller and the devices it watches.
type scaler struct {
	c          *control.Controller
	dst, src   device.Info
	rotational bool
}

// seedController sets the first W. Run calls it before a worker starts, so no file starts above that W.
func (r *runner) seedController() scaler {
	sc := scaler{
		dst: r.probeDevice(r.localDestProbe()),
		src: r.probeDevice(r.localSourceProbe()),
	}
	sc.rotational = sc.dst.Rotational() || sc.src.Rotational()
	start := controllerStartW(sc.rotational, len(r.reg.Remotes()) > 0, r.gate.max)
	sc.c = control.New(1, r.gate.max, start)
	r.setW(start)
	return sc
}

// runController is the auto-scaling loop. Each tick it measures the work rate
// and the guard signals, and the controller sets the limit of the gate.
func (r *runner) runController(sc scaler, stop <-chan struct{}) {
	c, dstInfo, srcInfo, rotational := sc.c, sc.dst, sc.src, sc.rotational

	dstUtil := device.NewUtilSampler(dstInfo.Name)
	var srcUtil *device.UtilSampler
	if srcInfo.Name != "" && srcInfo.Name != dstInfo.Name {
		srcUtil = device.NewUtilSampler(srcInfo.Name)
	}
	cpu := sysload.New()
	conns := newConnReporter()

	ticker := time.NewTicker(controlInterval)
	defer ticker.Stop()
	connTicker := time.NewTicker(connInterval)
	defer connTicker.Stop()
	prevWork := r.workDone()
	prevProgress := r.progressCount()
	prevTime := time.Now()

	for {
		select {
		case <-stop:
			return
		case <-connTicker.C:
			conns.tick(r, r.reg.Remotes())
		case now := <-ticker.C:
			nw := r.workDone()
			np := r.progressCount()
			dt := now.Sub(prevTime).Seconds()
			dw := nw - prevWork
			stalled := np == prevProgress
			prevWork, prevProgress, prevTime = nw, np, now
			if dt <= 0 {
				continue
			}
			s := control.Sample{
				Throughput: float64(dw) / dt,
				SysCPU:     -1,
				DevUtil:    -1,
				Rotational: rotational,
				// All workers busy, nothing finished: I/O is stalled; grow W, don't hold it.
				Blocked:   stalled && r.gate.saturated(),
				Unsettled: r.unsettled(),
			}
			if v, ok := cpu.Sample(); ok {
				s.SysCPU = v
			}
			s.DevUtil = sampleUtil(dstUtil, srcUtil)
			r.setW(c.Update(s))
		}
	}
}

// entryWork is the work that one finished file counts for, in bytes.
const entryWork = 64 << 10

// workDone is what the controller compares across W. A remote write reports once per buffer, so the wire counts for it.
func (r *runner) workDone() int64 {
	return r.localMoved.Load() + r.traffic() + entryWork*r.jobsDone.Load()
}

// traffic is the bytes exchanged with every remote host so far.
func (r *runner) traffic() int64 {
	if r.reg == nil {
		return 0
	}
	var n int64
	for _, fsys := range r.reg.Remotes() {
		if pooled, ok := fsys.(vfs.Pooled); ok {
			n += pooled.Traffic()
		}
	}
	return n
}

// unsettled reports that the streams that move data now are not W streams. A
// throughput from such a tick says nothing about W.
func (r *runner) unsettled() bool {
	inUse, limit, waiting := r.gate.load()
	if inUse > limit {
		// Files that started before a trim still run.
		return true
	}
	if inUse < limit && waiting == 0 {
		// The walk supplies less work than W streams take.
		return true
	}
	for _, fsys := range r.reg.Remotes() {
		if pooled, ok := fsys.(vfs.Pooled); ok && pooled.Waiting() > 0 {
			// A stream that waits for a connection moves no data.
			return true
		}
	}
	return false
}

// observeW is a test seam: it sees each W the controller sets.
var observeW func(w int)

func (r *runner) setW(w int) {
	r.gate.setLimit(w)
	if observeW != nil {
		observeW(w)
	}
}

// Returns "" when that end is remote: device.Lookup only classifies local disks.
func (r *runner) localDestProbe() string {
	if r.dfs == nil || !r.dfs.Local() {
		return ""
	}
	if r.targetDir != "" {
		return r.targetDir
	}
	return r.dfs.Dir(r.targetFile)
}

func (r *runner) localSourceProbe() string {
	if len(r.opts.Sources) == 0 {
		return ""
	}
	loc, err := vfs.Parse(r.opts.Sources[0])
	if err != nil || loc.Remote {
		return ""
	}
	return loc.Path
}

func (r *runner) probeDevice(path string) device.Info {
	if path == "" {
		return device.Info{}
	}
	return device.Lookup(path)
}

// connReporter says, under --verbose, when a host's pool grew; W decides the size.
type connReporter struct{ reported map[string]int }

func newConnReporter() *connReporter { return &connReporter{reported: map[string]int{}} }

func (c *connReporter) seen(host string) int {
	if n, ok := c.reported[host]; ok {
		return n
	}
	return 1
}

func (c *connReporter) tick(r *runner, remotes []vfs.FS) {
	for _, fsys := range remotes {
		pooled, ok := fsys.(vfs.Pooled)
		if !ok {
			continue
		}
		open, _ := pooled.Pool()
		if open > c.seen(fsys.Host()) {
			c.reported[fsys.Host()] = open
			r.verbose("ssh: %s now using %d connections", fsys.Host(), open)
		}
	}
}

func sampleUtil(dst, src *device.UtilSampler) float64 {
	util := -1.0
	if dst != nil {
		if v, ok := dst.Sample(); ok {
			util = v
		}
	}
	if src != nil {
		if v, ok := src.Sample(); ok && v > util {
			util = v
		}
	}
	return util
}

// controllerStartW seeds W from the device type: conservative on rotational
// (avoid seek thrashing), generous on a local SSD to fill queue depth.
func controllerStartW(rotational, remote bool, max int) int {
	if rotational {
		if max < 2 {
			return max
		}
		return 2
	}
	if remote {
		if max < 4 {
			return max
		}
		return 4
	}
	start := 2 * runtime.NumCPU()
	if start < 4 {
		start = 4
	}
	if start > max {
		start = max
	}
	return start
}
