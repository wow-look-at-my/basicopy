package engine

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/basicopy/internal/fsx"
	"github.com/wow-look-at-my/basicopy/internal/options"
	"github.com/wow-look-at-my/basicopy/internal/vfs"
)

func TestQueueFIFOAndClose(t *testing.T) {
	q := newQueue[int]()
	for i := 0; i < 5; i++ {
		q.push(i)
	}
	q.close()
	for i := 0; i < 5; i++ {
		v, ok := q.pop()
		require.True(t, ok)
		assert.Equal(t, i, v)
	}
	_, ok := q.pop()
	assert.False(t, ok, "pop on a closed, empty queue must return false")
}

func TestQueueConcurrentConsumersSeeEveryItem(t *testing.T) {
	q := newQueue[int]()
	const n = 10000
	var mu sync.Mutex
	seen := map[int]bool{}
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				v, ok := q.pop()
				if !ok {
					return
				}
				mu.Lock()
				seen[v] = true
				mu.Unlock()
			}
		}()
	}
	for i := 0; i < n; i++ {
		q.push(i)
	}
	q.close()
	wg.Wait()
	assert.Len(t, seen, n)
}

// The walk must find every file while no copy runs. A bounded queue blocks the
// walk once it is full, so the totals stay low until the copy nears its end.
func TestWalkRunsAheadOfCopy(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	const n = 3000
	for i := 0; i < n; i++ {
		writeFile(t, filepath.Join(src, fmt.Sprintf("f%04d", i)), []byte("x"), 0o644)
	}
	o := &options.Options{Sources: []string{src}, TargetDir: filepath.Join(root, "dst"), Progress: "never"}
	require.NoError(t, o.Validate())
	r := &runner{
		opts:        o,
		stdout:      io.Discard,
		stderr:      io.Discard,
		onStack:     map[string]bool{},
		hardlinkMap: map[string]hlPrimary{},
		jobs:        newQueue[fileJob](),
		metaJobs:    newQueue[*dirState](),
		reg:         vfs.NewRegistry(),
		copyOpts:    fsx.CopyOptions{Preserve: true},
	}
	defer r.reg.Close()
	require.NoError(t, r.resolveTarget())

	done := make(chan error, 1)
	go func() { done <- r.walk(context.Background()) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the walk blocked on the copy queue with no workers to drain it")
	}
	assert.EqualValues(t, n, r.totalFiles.Load())
	assert.EqualValues(t, n, r.totalBytes.Load())
}
