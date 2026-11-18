//go:build unix

// tty_unix.go holds ttyPath.

package askpass

import (
	"context"
	"syscall"

	"golang.org/x/term"
)

// ttyPath is the controlling terminal (a var so tests can name a pty).
var ttyPath = "/dev/tty"

func openTTY() (int, error) {
	return syscall.Open(ttyPath, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
}

// ttyAvailable reports whether a prompt can run on the terminal: it opens,
// and its settings can be read.
func ttyAvailable() bool {
	fd, err := openTTY()
	if err != nil {
		return false
	}
	defer syscall.Close(fd)
	_, err = term.GetState(fd)
	return err == nil
}

// askTTY writes prompt to the terminal and reads one line, with no echo for a secret.
func askTTY(ctx context.Context, prompt string, echo bool) (string, bool) {
	fd, err := openTTY()
	if err != nil {
		return "", false
	}
	saved, err := term.GetState(fd)
	if err != nil {
		_ = syscall.Close(fd)
		return "", false
	}
	_, _ = syscall.Write(fd, []byte("\r"+prompt))

	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		if echo {
			line, err := readLine(fd)
			done <- result{line, err}
			return
		}
		secret, err := term.ReadPassword(fd)
		done <- result{string(secret), err}
	}()

	select {
	case r := <-done:
		if !echo {
			_, _ = syscall.Write(fd, []byte("\n"))
		}
		_ = syscall.Close(fd)
		return r.line, r.err == nil
	case <-ctx.Done():
		// The read cannot be stopped. Give the terminal its echo back and leave fd to the reader.
		_ = term.Restore(fd, saved)
		_, _ = syscall.Write(fd, []byte("\n"))
		return "", false
	}
}

func readLine(fd int) (string, error) {
	var line []byte
	buf := make([]byte, 1)
	for {
		n, err := syscall.Read(fd, buf)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return "", err
		}
		if n == 0 || buf[0] == '\n' {
			return string(line), nil
		}
		if buf[0] != '\r' {
			line = append(line, buf[0])
		}
	}
}
