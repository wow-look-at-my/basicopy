package interrupt

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sigIgnored reads the SigIgn mask of a process from /proc and reports whether sig is in it.
func sigIgnored(t *testing.T, pid int, sig syscall.Signal) bool {
	t.Helper()
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	require.NoError(t, err)
	for _, line := range strings.Split(string(data), "\n") {
		if hex, ok := strings.CutPrefix(line, "SigIgn:"); ok {
			mask, err := strconv.ParseUint(strings.TrimSpace(hex), 16, 64)
			require.NoError(t, err)
			return mask&(1<<(uint(sig)-1)) != 0
		}
	}
	t.Fatal("no SigIgn line")
	return false
}

func startSleep(t *testing.T, shield bool) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	var err error
	if shield {
		err = Shield(cmd.Start)
	} else {
		err = cmd.Start()
	}
	require.NoError(t, err)
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

// TestShieldChildIgnoresInterrupt: a child started under Shield ignores SIGINT,
// a child started outside it does not, and this process gets SIGINT again afterwards.
func TestShieldChildIgnoresInterrupt(t *testing.T) {
	sigs := make(chan os.Signal, 1)
	Notify(sigs)

	shielded := startSleep(t, true)
	plain := startSleep(t, false)
	assert.True(t, sigIgnored(t, shielded.Process.Pid, syscall.SIGINT), "a shielded child must ignore SIGINT")
	assert.False(t, sigIgnored(t, plain.Process.Pid, syscall.SIGINT), "an ordinary child must keep the default SIGINT")
	assert.False(t, sigIgnored(t, os.Getpid(), syscall.SIGINT), "this process must handle SIGINT again after Shield")

	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGINT))
	select {
	case s := <-sigs:
		assert.Equal(t, os.Interrupt, s)
	case <-time.After(5 * time.Second):
		t.Fatal("SIGINT did not reach the Notify channel after Shield")
	}
}
