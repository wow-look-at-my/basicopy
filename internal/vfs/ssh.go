package vfs

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/pkg/sftp"
)

// Transport is a single live channel to a host's SFTP subsystem: the
// pipes the client talks over, plus a shutdown func.
type Transport struct {
	Stdout io.Reader
	Stdin  io.WriteCloser
	Close  func() error
}

// StartTransport is a var so tests can swap in an in-process SFTP server.
var StartTransport = startSSH

// sshArgs builds the ssh command line. extra marks a pool-widening
// connection: BatchMode=yes stops it from prompting for a credential (the
// dial fails instead), and ConnectTimeout bounds it against an unreachable
// host. The earliest connection is the user's own interactive ssh session and
// gets neither.
func sshArgs(loc Location, extra bool) []string {
	argv := []string{"ssh"}
	if extra {
		argv = append(argv, "-o", "BatchMode=yes", "-o", "ConnectTimeout=10")
	}
	if loc.Port != "" {
		argv = append(argv, "-p", loc.Port)
	}
	if loc.User != "" {
		argv = append(argv, "-l", loc.User)
	}
	return append(argv, "-s", loc.Host, "sftp")
}

// startSSH runs ssh and returns its stdio pipes. Shelling out to the ssh binary
// (as rsync does) is deliberate. Every OpenSSH mechanism the user already relies
// on -- ~/.ssh/config host aliases and identities, the agent, ProxyJump,
// known_hosts, password and 2FA prompts. Keeps working, and basicopy grows no
// connection settings of its own.
func startSSH(loc Location, extra bool) (*Transport, error) {
	argv := sshArgs(loc, extra)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stderr = os.Stderr // auth prompts and ssh diagnostics reach the user
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ssh: %w", err)
	}
	return &Transport{Stdout: stdout, Stdin: stdin, Close: func() error {
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
	}}, nil
}

// clientOptions keep many requests per file in flight, so round-trip time (not
// bandwidth) is what limits a transfer to a distant host.
var clientOptions = []sftp.ClientOption{
	sftp.MaxPacket(32 << 10),
	sftp.MaxConcurrentRequestsPerFile(64),
	sftp.UseConcurrentReads(true),
	sftp.UseConcurrentWrites(true),
}

// dialConn opens a single ssh connection and starts an SFTP session on it.
func dialConn(loc Location, extra bool) (*conn, error) {
	t, err := StartTransport(loc, extra)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", loc.Host, err)
	}
	client, err := sftp.NewClientPipe(t.Stdout, t.Stdin, clientOptions...)
	if err != nil {
		_ = t.Close()
		return nil, fmt.Errorf("start sftp session on %s: %w", loc.Host, err)
	}
	return &conn{client: client, close: t.Close}, nil
}

// Dial connects to a remote location, returning an FS whose connection pool can
// later widen itself on demand.
func Dial(loc Location) (FS, error) {
	if !loc.Remote {
		return Local, nil
	}
	first, err := dialConn(loc, false)
	if err != nil {
		return nil, err
	}
	return newRemoteFS(first, hostLabel(loc), func() (*conn, error) {
		return dialConn(loc, true)
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
	mu    sync.Mutex
	conns map[string]FS
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
	fs, err := Dial(loc)
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
	return firstErr
}
