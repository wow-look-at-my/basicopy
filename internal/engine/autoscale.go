package engine

import (
	"context"
	"path/filepath"
	"runtime"
	"time"

	"github.com/wow-look-at-my/basicopy/internal/control"
	"github.com/wow-look-at-my/basicopy/internal/device"
	"github.com/wow-look-at-my/basicopy/internal/sysload"
)

// controlInterval is the controller's tick period (var, not const, so tests can
// shorten it).
var controlInterval = 250 * time.Millisecond

// runController is the auto-scaling loop. It seeds the worker limit from the
// device class, then each tick measures achieved throughput plus the CPU and
// device-util guard signals and lets the controller resize the gate.
func (r *runner) runController(ctx context.Context, stop <-chan struct{}) {
	dstProbe := r.opts.TargetDir
	if dstProbe == "" {
		dstProbe = filepath.Dir(r.opts.TargetFile)
	}
	srcProbe := ""
	if len(r.opts.Sources) > 0 {
		srcProbe = r.opts.Sources[0]
	}

	dstInfo := device.Lookup(dstProbe)
	srcInfo := device.Lookup(srcProbe)
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

	ticker := time.NewTicker(controlInterval)
	defer ticker.Stop()
	prevBytes := r.moved.Load()
	prevTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
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
