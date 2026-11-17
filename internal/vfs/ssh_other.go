//go:build !unix

// ssh_other.go holds startWithoutTerminal.

package vfs

import "os/exec"

// Off Unix a child cannot be cut from the terminal this way, so an old ssh keeps its own prompts.
func startWithoutTerminal(*exec.Cmd) {}
