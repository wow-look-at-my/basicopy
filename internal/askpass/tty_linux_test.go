package askpass

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// screen collects what the terminal shows.
type screen struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (s *screen) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// usePTY points the package at a new pseudo terminal. It returns the keyboard
// side, what the terminal shows, and the terminal's own descriptor.
func usePTY(t *testing.T) (keyboard *os.File, shown *screen, slave int) {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	require.NoError(t, err)
	require.NoError(t, unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0))
	path := fmt.Sprintf("/dev/pts/%d", n)
	slave, err = unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	require.NoError(t, err)

	old := ttyPath
	ttyPath = path
	keyboard = os.NewFile(uintptr(fd), "ptmx")
	shown = &screen{}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := keyboard.Read(buf)
			if n > 0 {
				shown.mu.Lock()
				shown.buf.Write(buf[:n])
				shown.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		ttyPath = old
		_ = unix.Close(slave)
		_ = keyboard.Close()
	})
	return keyboard, shown, slave
}

func echoOn(t *testing.T, fd int) bool {
	t.Helper()
	tio, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	require.NoError(t, err)
	return tio.Lflag&unix.ECHO != 0
}

type ttyAnswer struct {
	line string
	ok   bool
}

func askInBackground(ctx context.Context, prompt string, echo bool) <-chan ttyAnswer {
	done := make(chan ttyAnswer, 1)
	go func() {
		line, ok := (&Person{}).Ask(ctx, prompt, echo)
		done <- ttyAnswer{line, ok}
	}()
	return done
}

func waitAnswer(t *testing.T, done <-chan ttyAnswer) ttyAnswer {
	t.Helper()
	select {
	case a := <-done:
		return a
	case <-time.After(10 * time.Second):
		t.Fatal("no answer came back from the terminal")
		return ttyAnswer{}
	}
}

// TestSecretIsReadWithoutEcho: the passphrase must not show on the terminal,
// and the terminal must echo again afterwards.
func TestSecretIsReadWithoutEcho(t *testing.T) {
	t.Serial()
	keyboard, shown, slave := usePTY(t)
	assert.True(t, ttyAvailable())

	done := askInBackground(context.Background(), keyPrompt, false)
	require.Eventually(t, func() bool { return !echoOn(t, slave) }, 5*time.Second, time.Millisecond)
	_, err := keyboard.WriteString("hunter2 pass\n")
	require.NoError(t, err)

	a := waitAnswer(t, done)
	assert.True(t, a.ok)
	assert.Equal(t, "hunter2 pass", a.line)
	assert.True(t, echoOn(t, slave), "the terminal must echo again after the secret")
	require.Eventually(t, func() bool { return strings.Contains(shown.String(), keyPrompt) }, 5*time.Second, time.Millisecond)
	assert.NotContains(t, shown.String(), "hunter2")
}

// TestHostKeyAnswerIsEchoed: "yes" to a host key question is typed in the open.
func TestHostKeyAnswerIsEchoed(t *testing.T) {
	t.Serial()
	keyboard, shown, _ := usePTY(t)
	const q = "Are you sure you want to continue connecting (yes/no/[fingerprint])? "

	done := askInBackground(context.Background(), q, true)
	require.Eventually(t, func() bool { return strings.Contains(shown.String(), q) }, 5*time.Second, time.Millisecond)
	_, err := keyboard.WriteString("yes\n")
	require.NoError(t, err)

	a := waitAnswer(t, done)
	assert.True(t, a.ok)
	assert.Equal(t, "yes", a.line)
	require.Eventually(t, func() bool { return strings.Contains(shown.String(), "yes") }, 5*time.Second, time.Millisecond)
}

// TestCancelEndsTheQuestion: Ctrl-C at the prompt must not leave the copy
// waiting, and must give the terminal its echo back.
func TestCancelEndsTheQuestion(t *testing.T) {
	t.Serial()
	keyboard, _, slave := usePTY(t)
	ctx, cancel := context.WithCancel(context.Background())

	done := askInBackground(ctx, keyPrompt, false)
	require.Eventually(t, func() bool { return !echoOn(t, slave) }, 5*time.Second, time.Millisecond)
	cancel()

	a := waitAnswer(t, done)
	assert.False(t, a.ok)
	assert.Empty(t, a.line)
	assert.True(t, echoOn(t, slave))
	// Let the reader that the cancel left behind finish.
	_, _ = keyboard.WriteString("\n")
}

// TestNoTerminal: with no terminal to open, nobody is asked.
func TestNoTerminal(t *testing.T) {
	t.Serial()
	old := ttyPath
	ttyPath = "/nonexistent/tty"
	defer func() { ttyPath = old }()
	assert.False(t, ttyAvailable())
	_, ok := (&Person{}).Ask(context.Background(), keyPrompt, false)
	assert.False(t, ok)
}
