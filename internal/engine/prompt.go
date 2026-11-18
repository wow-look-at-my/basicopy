// prompt.go holds findPerson.

package engine

import (
	"context"
	"fmt"

	"github.com/wow-look-at-my/basicopy/internal/askpass"
)

// findPerson is a var so tests can supply or remove the person at the keyboard.
var findPerson = askpass.FindPerson

// prompter shows the questions of ssh on the terminal of the run.
type prompter struct {
	r      *runner
	ctx    context.Context
	person *askpass.Person
}

func (p *prompter) Ask(prompt string, echo bool) (string, bool) {
	r := p.r
	r.outMu.Lock()
	defer r.outMu.Unlock()
	// The question needs the terminal, so the region comes off the screen and returns after the answer.
	d := r.display
	if d == nil {
		return p.person.Ask(p.ctx, prompt, echo)
	}
	r.stopRegion()
	answer, ok := p.person.Ask(p.ctx, prompt, echo)
	if live, err := r.newRegion(); err != nil {
		fmt.Fprintf(r.stderr, "basicopy: warning: progress display: %v\n", err)
	} else {
		r.display = &display{live: live, stdoutShared: d.stdoutShared}
	}
	return answer, ok
}

// Notify prints under --quiet too: the copy waits for the person to act.
func (p *prompter) Notify(host string, extra bool, msg string) {
	if extra {
		p.r.elog("basicopy: ssh %s (another connection): %s", host, msg)
		return
	}
	p.r.elog("basicopy: ssh %s: %s", host, msg)
}
