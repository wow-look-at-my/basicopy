package engine

import (
	"context"
	"runtime"
	"time"

	"github.com/wow-look-at-my/basicopy/internal/control"
	"github.com/wow-look-at-my/basicopy/internal/device"
	"github.com/wow-look-at-my/basicopy/internal/sysload"
	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// Tick periods for the two control loops (vars, not consts, so tests can shorten
// them). Connections are re-evaluated far less often than workers because
// opening one costs a handshake and its effect takes a moment to show up in the
// throughput signal.
var (
	controlInterval = 250 * time.Millisecond
	connInterval    = 500 * time.Millisecond
)

// runController is the auto-scaling loop. It seeds the worker limit from the
// device class, then each tick measures achieved throughput plus the CPU and
// device-util guard signals and lets the controller resize the gate. On a remote
// copy it also drives the SSH connection pool from the same measurements (see
// connScaler).
func (r *runner) runController(ctx context.Context, stop <-chan struct{}) {
	dstInfo := r.probeDevice(r.localDestProbe())
	srcInfo := r.probeDevice(r.localSourceProbe())
	rotational := dstInfo.Rotational() || srcInfo.Rotational()

	start := controllerStartW(rotational, r.gate.max)
	c := control.New(1, r.gate.max, start)
	r.gate.setLimit(start)

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
	prevBytes := r.moved.Load()
	prevProgress := r.progressCount()
	prevTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-connTicker.C:
			conns.tick(r, r.reg.Remotes())
		case now := <-ticker.C:
			nb := r.moved.Load()
			np := r.progressCount()
			dt := now.Sub(prevTime).Seconds()
			db := nb - prevBytes
			stalled := np == prevProgress
			prevBytes, prevProgress, prevTime = nb, np, now
			if dt <= 0 {
				continue
			}
			s := control.Sample{
				Throughput: float64(db) / dt,
				SysCPU:     -1,
				DevUtil:    -1,
				Rotational: rotational,
				// Every worker occupied and not one byte, file, or directory
				// finished: the copy is waiting on the storage. More streams is
				// the only thing that overlaps that waiting, and the plateau
				// logic would otherwise read the flat line as "W is already
				// high enough".
				Blocked: stalled && r.gate.saturated(),
			}
			if v, ok := cpu.Sample(); ok {
				s.SysCPU = v
			}
			s.DevUtil = sampleUtil(dstUtil, srcUtil)
			r.gate.setLimit(c.Update(s))
		}
	}
}

// localDestProbe / localSourceProbe return a path to classify the backing device
// for, or "" when that end is remote: the kernel knowledge behind device.Lookup
// (rotational flag, %util) describes this machine's disks only, and guessing
// with a remote path would classify whatever happens to be mounted here at the
// same spelling.
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

// connReporter says, under --verbose, when a host's connection pool grew. There
// is nothing here that decides the pool's size: connections follow the transfers
// the copy has in flight (internal/vfs), and how many run at once is the worker
// controller's measured decision. This only reports it.
type connReporter struct{ reported map[string]int }

// The first connection is not news: every remote copy has one.
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

// sampleUtil returns the max %util across the destination and source samplers, or
// -1 if neither produced a reading this tick.
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

// controllerStartW seeds the worker limit from the device prior: conservative on
// rotational media (avoid seek-thrashing during the probe), generous on
// SSD/unknown where concurrency is needed to fill queue depth.
func controllerStartW(rotational bool, max int) int {
	if rotational {
		if max < 2 {
			return max
		}
		return 2
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
