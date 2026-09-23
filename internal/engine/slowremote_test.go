package engine

import (
	"encoding/binary"
	"fmt"
	"io"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/options"
	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

// A real sftp-server serves a single request at a time; pkg/sftp's
// in-process server does not. slowRemote proxies it to enforce that
// single request in flight per connection, reads delayed.
func slowRemote(t *testing.T, hydration time.Duration) {
	t.Helper()
	t.Serial()
	old := vfs.StartTransport
	vfs.StartTransport = func(vfs.Location, bool) (*vfs.Transport, error) {
		return startSlowServer(hydration)
	}
	t.Cleanup(func() { vfs.StartTransport = old })
}

var readCount atomic.Int64

func startSlowServer(hydration time.Duration) (*vfs.Transport, error) {
	// client -> proxy -> server, and server -> proxy -> client.
	reqIn, clientOut := io.Pipe()     // what the client writes
	srvIn, proxyToSrv := io.Pipe()    // what the server reads
	respIn, srvOut := io.Pipe()       // what the server writes
	clientIn, proxyToCli := io.Pipe() // what the client reads

	server, err := sftp.NewServer(struct {
		io.Reader
		io.WriteCloser
	}{srvIn, srvOut})
	if err != nil {
		return nil, err
	}
	go func() {
		_ = server.Serve()
		_ = server.Close()
	}()

	// A single token: forward the next request only after the prior response returns.
	turn := make(chan struct{}, 1)
	go func() {
		for {
			pkt, err := readPacket(reqIn)
			if err != nil {
				_ = proxyToSrv.Close()
				return
			}
			turn <- struct{}{}
			if len(pkt) > 0 && pkt[0] == 5 { // SSH_FXP_READ: the data path
				readCount.Add(1)
				time.Sleep(hydration)
			}
			if err := writePacket(proxyToSrv, pkt); err != nil {
				return
			}
		}
	}()
	go func() {
		for {
			pkt, err := readPacket(respIn)
			if err != nil {
				_ = proxyToCli.Close()
				return
			}
			if err := writePacket(proxyToCli, pkt); err != nil {
				return
			}
			<-turn
		}
	}()

	return &vfs.Transport{Stdout: clientIn, Stdin: clientOut, Close: func() error {
		_ = server.Close()
		_ = clientIn.Close()
		_ = reqIn.Close()
		return nil
	}}, nil
}

func readPacket(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > 1<<20 {
		return nil, fmt.Errorf("bad packet length %d", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

func writePacket(w io.Writer, body []byte) error {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

// TestSlowRemoteReadsRunInParallel is the regression test for the defect this
// whole mechanism exists to fix: pulling files whose reads block must not
// serialize. With a single connection the copy takes files×hydration; the
// pool has to widen until the waiting overlaps.
func TestSlowRemoteReadsRunInParallel(t *testing.T) {
	const (
		files     = 12
		hydration = 150 * time.Millisecond
	)
	restore := fastControlLoops(t)
	defer restore()
	slowRemote(t, hydration)

	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	for i := 0; i < files; i++ {
		writeFile(t, filepath.Join(src, fmt.Sprintf("f%02d.bin", i)), make([]byte, 4096), 0o644)
	}

	o := &options.Options{
		Sources:   []string{"pc:" + src},
		TargetDir: dst,
		Verbose:   true,
		Progress:  "never",
	}
	readCount.Store(0)
	start := time.Now()
	sum, out, _ := captureRun(t, o)
	elapsed := time.Since(start)

	require.EqualValues(t, files, sum.Files)
	require.Zero(t, sum.Failed)

	// A single connection cannot beat serial reads; use the reads actually issued.
	reads := readCount.Load()
	require.Positive(t, reads)
	serial := time.Duration(reads) * hydration
	assert.Less(t, elapsed, serial/2,
		"blocked reads must overlap across connections (took %v; one connection would need >= %v for %d reads)",
		elapsed, serial, reads)
	assert.Contains(t, out, "now using", "the pool must report the connections it opened")
}

// TestSlowRemoteDoesNotWidenWhenIdle: the same slow server, but a single file
// at a time asked of it. With nothing queued there is nothing to overlap, and
// the run must not spend handshakes on connections it cannot use.
func TestSlowRemoteDoesNotWidenWhenIdle(t *testing.T) {
	restore := fastControlLoops(t)
	defer restore()
	slowRemote(t, 20*time.Millisecond)

	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "only.bin"), make([]byte, 4096), 0o644)

	o := &options.Options{
		Sources:   []string{"pc:" + src},
		TargetDir: dst,
		Verbose:   true,
		Progress:  "never",
	}
	readCount.Store(0)
	sum, out, _ := captureRun(t, o)
	require.EqualValues(t, 1, sum.Files)
	assert.NotContains(t, out, "now using", "one transfer at a time needs exactly one connection")
}

// fastControlLoops shortens both control loops so a test observes scaling
// decisions in a reasonable wall-clock time.
func fastControlLoops(t *testing.T) func() {
	t.Helper()
	t.Serial()
	oldControl, oldConn := controlInterval, connInterval
	controlInterval, connInterval = 10*time.Millisecond, 25*time.Millisecond
	return func() { controlInterval, connInterval = oldControl, oldConn }
}
