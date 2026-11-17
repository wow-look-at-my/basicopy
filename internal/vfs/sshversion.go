// sshversion.go holds sshNeedsNoTerminal.

package vfs

import (
	"os/exec"
	"strings"
)

// sshNeedsNoTerminal reports an ssh on PATH that predates SSH_ASKPASS_REQUIRE.
// It uses its askpass program only when it has no terminal and a display is set.
func sshNeedsNoTerminal() bool {
	out, err := exec.Command("ssh", "-V").CombinedOutput()
	if err != nil {
		return false
	}
	major, minor, ok := parseSSHVersion(string(out))
	return ok && (major < 8 || (major == 8 && minor < 4))
}

func parseSSHVersion(banner string) (major, minor int, ok bool) {
	_, rest, found := strings.Cut(banner, "OpenSSH_")
	if !found {
		return 0, 0, false
	}
	rest = strings.TrimLeft(rest, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ_")
	major, rest, ok = leadingNumber(rest)
	if !ok || !strings.HasPrefix(rest, ".") {
		return 0, 0, false
	}
	minor, _, ok = leadingNumber(rest[1:])
	return major, minor, ok
}

func leadingNumber(s string) (n int, rest string, ok bool) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' && i < 6 {
		n = n*10 + int(s[i]-'0')
		i++
	}
	return n, s[i:], i > 0
}

// placeholderDisplay makes an old ssh accept an askpass program. Nothing connects to it.
const placeholderDisplay = "DISPLAY=basicopy:0"

// withDisplay adds a display to env when it names none. added tells the caller
// to turn X11 forwarding off, so ssh does not try to reach the placeholder.
func withDisplay(env []string) (out []string, added bool) {
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		if (name == "DISPLAY" || name == "WAYLAND_DISPLAY") && value != "" {
			return env, false
		}
	}
	return append(env, placeholderDisplay), true
}
