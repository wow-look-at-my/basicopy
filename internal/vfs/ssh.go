// Transport is a single live channel to a host's SFTP subsystem: the pipes the client talks over, plus a shutdown func.

package vfs

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"github.com/wow-look-at-my/basicopy/internal/askpass"
	"github.com/wow-look-at-my/basicopy/internal/interrupt"
	"github.com/wow-look-at-my/go-containers/set"
)

// Transport is a single live channel to a host's SFTP subsystem: the
// pipes the client talks over, plus a shutdown func.
type Transport struct {
	Stdout io.Reader
	Stdin  io.WriteCloser
	Close  func() error
	// Kill stops the channel at once, also before the far side has answered. Nil means Close does that.
	Kill func()
}

// Connect describes one ssh connection to start.
type Connect struct {
	Loc Location
	// Extra marks a connection that widens a pool. It must not hold up the copy.
	Extra bool
	// Stderr receives the diagnostics of ssh.
	Stderr io.Writer
	// Prompts answers the questions of ssh. With nil, ssh asks on the terminal itself.
	Prompts *askpass.Broker
	// SSHOpts are ssh_config options that this connection passes as -o arguments.
	SSHOpts []string
}

// StartTransport is a var so tests can swap in an in-process SFTP server.
var StartTransport = startSSH

// sshArgs builds the ssh command line. extra marks a pool-widening connection:
// ConnectTimeout bounds it against an unreachable host. batch adds
// BatchMode=yes, so ssh fails the dial where it would ask a question. sshOpts
// are the user's -o options, and their place in the line is their precedence.
func sshArgs(loc Location, sshOpts []string, extra, batch bool) []string {
	argv := []string{"ssh"}
	if batch {
		argv = append(argv, "-o", "BatchMode=yes")
	}
	if extra {
		argv = append(argv, "-o", "ConnectTimeout=10")
	}
	if loc.Port != "" {
		argv = append(argv, "-p", loc.Port)
	}
	if loc.User != "" {
		argv = append(argv, "-l", loc.User)
	}
	// ssh keeps the first value it sees for an option, so sshOpts go last.
	for _, o := range sshOpts {
		argv = append(argv, "-o", o)
	}
	return append(argv, "-s", loc.Host, "sftp")
}

// startSSH runs ssh and returns its stdio pipes. Shelling out to the ssh binary
// (as rsync does) is deliberate. Every OpenSSH mechanism the user already relies
// on -- ~/.ssh/config host aliases and identities, the agent, ProxyJump,
// known_hosts, password and 2FA prompts. Keeps working, and basicopy grows no
// connection settings of its own.
func startSSH(c Connect) (*Transport, error) {
	// An extra connection keeps BatchMode until an answer proves that this ssh asks the Broker.
	batch := c.Extra
	var session *askpass.Session
	if c.Prompts != nil {
		session = c.Prompts.Session(hostLabel(c.Loc), !c.Extra)
		batch = c.Extra && !c.Prompts.Heard()
	}
	argv := sshArgs(c.Loc, c.SSHOpts, c.Extra, batch)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stderr = c.Stderr
	var proc atomic.Pointer[os.Process]
	if session != nil {
		cmd.Env = session.Env(os.Environ())
		session.OnRefuse(func(prompt string) {
			fmt.Fprintf(c.Stderr, "ssh: %s: another connection asks %q, and only you can answer that; the copy keeps the connections it has\n",
				hostLabel(c.Loc), firstLine(prompt))
			// Stop ssh before it sends an empty answer to the host.
			if p := proc.Load(); p != nil {
				_ = p.Kill()
			}
		})
	}
	fail := func(err error) (*Transport, error) {
		if session != nil {
			session.Close()
		}
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fail(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fail(err)
	}
	// The first Ctrl-C lets the copy drain its queue, which needs the connection alive.
	if err := interrupt.Shield(cmd.Start); err != nil {
		return fail(fmt.Errorf("start ssh: %w", err))
	}
	proc.Store(cmd.Process)
	track(cmd.Process)
	return &Transport{
		Stdout: stdout,
		Stdin:  stdin,
		Kill:   func() { _ = cmd.Process.Kill() },
		Close: func() error {
			defer untrack(cmd.Process)
			if session != nil {
				defer session.Close()
			}
			// The server exits when its stdin closes; kill it if it lingers.
			_ = stdin.Close()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
			return nil
		},
	}, nil
}

// firstLine cuts a prompt to one short line for a message.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	const max = 100
	if len(s) > max {
		s = s[:max] + "..."
	}
	return s
}

var (
	procsMu sync.Mutex
	procs   set.Set[*os.Process]
)

func track(p *os.Process) {
	procsMu.Lock()
	procs.Add(p)
	procsMu.Unlock()
}

func untrack(p *os.Process) {
	procsMu.Lock()
	procs.Remove(p)
	procsMu.Unlock()
}

// KillAll kills every live ssh process.
func KillAll() {
	procsMu.Lock()
	defer procsMu.Unlock()
	for p := range procs.All() {
		_ = p.Kill()
	}
}

// clientOptions keep many requests per file in flight, so round-trip time (not
// bandwidth) is what limits a transfer to a distant host.
var clientOptions = []sftp.ClientOption{
	sftp.MaxPacket(32 << 10),
	sftp.MaxConcurrentRequestsPerFile(64),
	sftp.UseConcurrentReads(true),
	sftp.UseConcurrentWrites(true),
}

// dialConn opens a single ssh connection and starts an SFTP session on it. A
// close of stop ends a dial that still waits, for example on a key touch.
func dialConn(c Connect, stop <-chan struct{}) (*conn, error) {
	t, err := StartTransport(c)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", c.Loc.Host, err)
	}
	ready := make(chan struct{})
	defer close(ready)
	if stop != nil {
		go func() {
			select {
			case <-stop:
				if t.Kill != nil {
					t.Kill()
				} else {
					_ = t.Close()
				}
			case <-ready:
			}
		}()
	}
	client, err := sftp.NewClientPipe(t.Stdout, t.Stdin, clientOptions...)
	if err != nil {
		_ = t.Close()
		return nil, fmt.Errorf("start sftp session on %s: %w", c.Loc.Host, err)
	}
	return &conn{client: client, close: t.Close}, nil
}

// authNoteDelay is how long a pool-widening dial may run before the copy says that it waits.
var authNoteDelay = 5 * time.Second

// Dial connects to a remote location, returning an FS whose connection pool can
// later widen itself on demand. The first connection owns the real stderr.
// Connections that widen the pool write their diagnostics to diag (nil means
// os.Stderr). prompts (optional) answers the questions of every connection, and
// every connection gets sshOpts.
func Dial(loc Location, sshOpts []string, diag io.Writer, prompts *askpass.Broker) (FS, error) {
	if !loc.Remote {
		return Local, nil
	}
	first, err := dialConn(Connect{Loc: loc, Stderr: os.Stderr, Prompts: prompts, SSHOpts: sshOpts}, nil)
	if err != nil {
		return nil, err
	}
	if diag == nil {
		diag = os.Stderr
	}
	var noted atomic.Bool
	return newRemoteFS(first, hostLabel(loc), func(stop <-chan struct{}) (*conn, error) {
		// An agent can hold a dial for a touch that this process cannot see. Say so one time.
		note := time.AfterFunc(authNoteDelay, func() {
			if noted.CompareAndSwap(false, true) {
				fmt.Fprintf(diag, "ssh: %s: another connection has not logged in yet; if your key needs a touch or an approval, it waits for that now\n", hostLabel(loc))
			}
		})
		defer note.Stop()
		return dialConn(Connect{Loc: loc, Extra: true, Stderr: diag, Prompts: prompts, SSHOpts: sshOpts}, stop)
	}), nil
}

func hostLabel(loc Location) string {
	if loc.User != "" {
		return loc.User + "@" + loc.Host
	}
	return loc.Host
}

// Registry hands out a single FS per host.
type Registry struct {
	// Diag receives ssh diagnostics from connections that widen a pool; nil means os.Stderr.
	Diag io.Writer
	// Human answers the questions of ssh. With nil, each ssh asks on the terminal itself.
	Human askpass.Human

	// SSHOptions are ssh_config options that every ssh process gets as -o arguments.
	SSHOptions []string

	mu      sync.Mutex
	conns   map[string]FS
	prompts *askpass.Broker
}

// brokerLocked starts the prompt broker on the first remote dial. Without one
// the connections still work: ssh asks its own questions, and a connection
// that widens a pool cannot ask any.
func (r *Registry) brokerLocked() *askpass.Broker {
	if r.prompts != nil || r.Human == nil {
		return r.prompts
	}
	b, err := askpass.Start(r.Human)
	if err != nil {
		diag := r.Diag
		if diag == nil {
			diag = os.Stderr
		}
		fmt.Fprintf(diag, "ssh: cannot relay prompts (%v); an extra connection cannot ask for a passphrase\n", err)
		r.Human = nil
		return nil
	}
	r.prompts = b
	return b
}

// NewRegistry returns an empty connection registry.
func NewRegistry() *Registry {
	return &Registry{conns: map[string]FS{}}
}

// Resolve parses a single command-line path and returns the filesystem it
// lives on together with the path within that filesystem. Remote hosts are
// dialed on earliest use.
func (r *Registry) Resolve(arg string) (FS, string, error) {
	loc, err := Parse(arg)
	if err != nil {
		return nil, "", err
	}
	fs, err := r.For(loc)
	if err != nil {
		return nil, "", err
	}
	return fs, loc.Path, nil
}

// For returns the FS for a parsed location, reusing an existing connection to
// the same host.
func (r *Registry) For(loc Location) (FS, error) {
	if !loc.Remote {
		return Local, nil
	}
	key := loc.ConnKey()

	r.mu.Lock()
	defer r.mu.Unlock()
	if fs, ok := r.conns[key]; ok {
		return fs, nil
	}
	fs, err := Dial(loc, r.SSHOptions, r.Diag, r.brokerLocked())
	if err != nil {
		return nil, err
	}
	r.conns[key] = fs
	return fs, nil
}

// Remotes returns every remote FS the registry has opened, for callers that
// drive connection scaling.
func (r *Registry) Remotes() []FS {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]FS, 0, len(r.conns))
	for _, fs := range r.conns {
		out = append(out, fs)
	}
	return out
}

// Close tears down every connection the registry opened.
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var firstErr error
	for key, fs := range r.conns {
		if err := fs.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(r.conns, key)
	}
	if r.prompts != nil {
		_ = r.prompts.Close()
		r.prompts = nil
	}
	return firstErr
}
