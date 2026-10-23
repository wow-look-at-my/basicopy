// Package interrupt owns the process's Ctrl-C handling. It also starts child
// processes that do not get the terminal's Ctrl-C. An ssh connection stays up
// while an interrupted copy finishes its queue.
package interrupt

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

var (
	mu sync.Mutex
	ch chan<- os.Signal
)

// Notify sends SIGINT and SIGTERM to c.
func Notify(c chan<- os.Signal) {
	mu.Lock()
	defer mu.Unlock()
	ch = c
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
}

// Shield runs start with SIGINT ignored. A child process that start launches
// keeps the ignore, so the terminal's Ctrl-C does not stop it. A Ctrl-C that
// arrives while start runs is lost.
func Shield(start func() error) error {
	mu.Lock()
	defer mu.Unlock()
	signal.Ignore(os.Interrupt)
	defer func() {
		if ch != nil {
			signal.Notify(ch, os.Interrupt)
		} else {
			signal.Reset(os.Interrupt)
		}
	}()
	return start()
}
