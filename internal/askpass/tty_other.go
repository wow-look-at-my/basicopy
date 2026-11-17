//go:build !unix

// tty_other.go holds ttyAvailable.

package askpass

import "context"

// Off Unix there is no terminal support, so ssh does its own prompts.
func ttyAvailable() bool { return false }

func askTTY(context.Context, string, bool) (string, bool) { return "", false }
