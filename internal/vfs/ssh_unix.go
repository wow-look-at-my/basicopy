//go:build unix

// ssh_unix.go holds startWithoutTerminal.

package vfs

import (
	"os/exec"
	"syscall"
)

// startWithoutTerminal makes cmd start in a session of its own, so it has no controlling terminal.
func startWithoutTerminal(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
