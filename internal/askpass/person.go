// person.go holds lookSelf.

package askpass

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// lookSelf turns the first word of a command line into an absolute path.
func lookSelf(arg0 string) (string, error) {
	if arg0 == "" {
		return "", errors.New("no command name")
	}
	p := arg0
	if !strings.ContainsRune(arg0, filepath.Separator) {
		found, err := exec.LookPath(arg0)
		if err != nil {
			return "", err
		}
		p = found
	}
	p, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		return "", errors.New("not a file: " + p)
	}
	return p, nil
}

// Person reaches the person who runs the copy: on the terminal, or through the
// askpass program that the person's own environment names.
type Person struct {
	program string // empty means the terminal
}

// FindPerson reports how to ask the person, with the same rules that ssh
// applies to its environment. ok is false when there is no way to ask.
func FindPerson() (p *Person, ok bool) {
	return findPerson(os.Getenv, ttyAvailable())
}

func findPerson(getenv func(string) string, tty bool) (*Person, bool) {
	program := getenv("SSH_ASKPASS")
	require := strings.ToLower(getenv("SSH_ASKPASS_REQUIRE"))
	display := getenv("DISPLAY") != "" || getenv("WAYLAND_DISPLAY") != ""
	if program != "" && (require == "force" || (require == "prefer" && display)) {
		return &Person{program: program}, true
	}
	if tty {
		return &Person{}, true
	}
	if program != "" && display && require != "never" {
		return &Person{program: program}, true
	}
	return nil, false
}

// Ask shows prompt to the person and returns the answer. A cancel of ctx ends
// the wait with ok false.
func (p *Person) Ask(ctx context.Context, prompt string, echo bool) (answer string, ok bool) {
	if ctx.Err() != nil {
		return "", false
	}
	if p.program == "" {
		return askTTY(ctx, prompt, echo)
	}
	out, err := exec.CommandContext(ctx, p.program, prompt).Output()
	if err != nil {
		return "", false
	}
	line, _, _ := bytes.Cut(out, []byte("\n"))
	return strings.TrimSuffix(string(line), "\r"), true
}
