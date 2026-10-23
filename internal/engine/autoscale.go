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

// runController is the auto-scaling loop. It seeds the worker limit from the
// device class, then each tick measures achieved throughput plus the CPU. And
// device-util guard signals and lets the controller resize the gate.
func (r *runner) runController(stop <-chan struct{}) {
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
				// All workers busy, nothing finished: I/O is stalled; grow W, don't hold it.
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
// (avoid seek thrashing), generous on SSD/unknown to fill queue depth.
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
