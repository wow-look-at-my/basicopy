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
	connInterval    = 2 * time.Second
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
	conns := newConnScaler()

	ticker := time.NewTicker(controlInterval)
	defer ticker.Stop()
	connTicker := time.NewTicker(connInterval)
	defer connTicker.Stop()
	prevBytes := r.moved.Load()
	prevTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case now := <-connTicker.C:
			conns.tick(r, c, now)
		case now := <-ticker.C:
			nb := r.moved.Load()
			dt := now.Sub(prevTime).Seconds()
			db := nb - prevBytes
			prevBytes, prevTime = nb, now
			if dt <= 0 {
				continue
			}
			s := control.Sample{
				Throughput: float64(db) / dt,
				SysCPU:     -1,
				DevUtil:    -1,
				Rotational: rotational,
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

// connScaler sizes the SSH connection pool with the same closed loop that sizes
// the worker pool, because the question is the same one: does adding another
// stream raise throughput?
//
// A single ssh connection is a single TCP flow, encrypted by a single core, and
// served by a single sftp-server process on the far end. Deep request pipelining
// hides the link's latency within that connection, and for most copies that is
// the whole story -- so the pool starts at one and stays there. When it is not
// the whole story (cipher-bound on a fast LAN, a lossy long-haul link, a remote
// disk that wants concurrent readers) the symptom is precise: the workers are
// all busy, the worker controller has settled at a plateau, and throughput is
// flat. That is exactly the condition under which this loop offers another
// connection and keeps it only if the plateau rises.
type connScaler struct {
	ctl       *control.Controller
	prevBytes int64
	prevTime  time.Time
	primed    bool
	reported  int
}

func newConnScaler() *connScaler {
	return &connScaler{ctl: control.New(1, vfs.MaxConnections, 1)}
}

func (s *connScaler) tick(r *runner, workers *control.Controller, now time.Time) {
	remotes := r.reg.Remotes()
	if len(remotes) == 0 {
		return
	}
	bytes := r.moved.Load()

	// Only judge a connection change while the worker side is saturated and
	// settled: otherwise the throughput swing being measured belongs to the
	// worker controller's own probing, and the two loops would chase each
	// other's noise.
	if !r.gate.saturated() || !workers.Steady() {
		s.prevBytes, s.prevTime, s.primed = bytes, now, false
		return
	}
	if !s.primed {
		s.prevBytes, s.prevTime, s.primed = bytes, now, true
		return
	}
	dt := now.Sub(s.prevTime).Seconds()
	db := bytes - s.prevBytes
	s.prevBytes, s.prevTime = bytes, now
	if dt <= 0 {
		return
	}

	width := s.ctl.Update(control.Sample{Throughput: float64(db) / dt, SysCPU: -1, DevUtil: -1})
	for _, fsys := range remotes {
		sc, ok := fsys.(vfs.Scalable)
		if !ok {
			continue
		}
		sc.SetWidth(width)
		if _, open, _ := sc.Pool(); open > s.reported {
			s.reported = open
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
