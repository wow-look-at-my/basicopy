// Package askpass makes basicopy the askpass program of each ssh that it starts.
// Each ssh prompt then comes to one Broker in the process of the copy. The Broker
// asks the person one time and gives the same answer to each later connection. A
// key with a passphrase opens many connections for one prompt.
package askpass

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wow-look-at-my/go-containers/set"
)

// envBroker carries "address/token/session" from the Broker to the helper. It is internal plumbing.
const envBroker = "BASICOPY_ASKPASS_BROKER"

// kindNotify is the SSH_ASKPASS_PROMPT value of a message that needs no answer.
const kindNotify = "none"

// Human is the person who runs the copy.
type Human interface {
	// Ask shows prompt and returns the typed answer. ok is false when nobody answers.
	Ask(prompt string, echo bool) (answer string, ok bool)
	// Notify shows a message from ssh that needs an action and no answer. extra marks a connection that widens a pool.
	Notify(host string, extra bool, msg string)
}

type request struct {
	Token   string `json:"token"`
	Session string `json:"session"`
	PID     int    `json:"pid"` // the ssh process that asks
	Kind    string `json:"kind"`
	Prompt  string `json:"prompt"`
}

type reply struct {
	OK     bool   `json:"ok"`
	Answer string `json:"answer"`
}

// asker is one prompt of one ssh process.
type asker struct {
	pid    int
	prompt string
}

// Broker answers the prompts of each ssh process that carries its environment.
type Broker struct {
	human Human
	ln    net.Listener
	token string
	self  string

	askMu sync.Mutex // one question at a time, so a second asker finds the answer of the first

	mu       sync.Mutex
	answers  map[string]string // prompt -> the last answer from the person
	sessions map[string]*Session
	next     int
	heard    bool
	open     set.Set[net.Conn] // helper connections that wait for an answer
	closed   bool
}

// Start opens a Broker that asks human.
func Start(human Human) (*Broker, error) {
	self, err := selfPath()
	if err != nil {
		return nil, err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	b := &Broker{
		human:    human,
		ln:       ln,
		token:    hex.EncodeToString(raw),
		self:     self,
		answers:  map[string]string{},
		sessions: map[string]*Session{},
	}
	go b.serve()
	return b, nil
}

// selfPath finds this executable. The command line comes first: under a loader
// the path that the kernel reports can be the loader.
func selfPath() (string, error) {
	if p, err := lookSelf(os.Args[0]); err == nil {
		return p, nil
	}
	return os.Executable()
}

// Close stops the Broker. A helper that waits gets no answer.
func (b *Broker) Close() error {
	err := b.ln.Close()
	b.mu.Lock()
	b.closed = true
	for c := range b.open.All() {
		_ = c.Close()
	}
	b.mu.Unlock()
	return err
}

// hold records a helper connection, so Close can end it. It reports false after Close.
func (b *Broker) hold(c net.Conn) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	b.open.Add(c)
	return true
}

func (b *Broker) drop(c net.Conn) {
	b.mu.Lock()
	b.open.Remove(c)
	b.mu.Unlock()
	_ = c.Close()
}

// Heard reports whether an ssh process got an answer through this Broker. It
// proves that the ssh on this machine sends its prompts here.
func (b *Broker) Heard() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.heard
}

// Session is the prompt channel of one ssh connection.
type Session struct {
	b           *Broker
	id          string
	host        string
	interactive bool

	// Guarded by b.mu.
	given  map[asker]bool
	refuse func(prompt string)
}

// Session registers one ssh connection to host. An interactive connection may
// ask the person. Another connection gets only answers that the person gave
// before.
func (b *Broker) Session(host string, interactive bool) *Session {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	s := &Session{b: b, id: fmt.Sprint(b.next), host: host, interactive: interactive, given: map[asker]bool{}}
	b.sessions[s.id] = s
	return s
}

// OnRefuse sets the function that runs when the Broker has no answer for this
// connection. The connection cannot continue after that.
func (s *Session) OnRefuse(f func(prompt string)) {
	s.b.mu.Lock()
	s.refuse = f
	s.b.mu.Unlock()
}

// Env returns base with the variables that make ssh send its prompts to the Broker.
func (s *Session) Env(base []string) []string {
	drop := []string{"SSH_ASKPASS=", "SSH_ASKPASS_REQUIRE=", envBroker + "="}
	out := make([]string, 0, len(base)+len(drop))
next:
	for _, kv := range base {
		for _, prefix := range drop {
			if strings.HasPrefix(kv, prefix) {
				continue next
			}
		}
		out = append(out, kv)
	}
	return append(out,
		"SSH_ASKPASS="+s.b.self,
		"SSH_ASKPASS_REQUIRE=force",
		envBroker+"="+s.b.ln.Addr().String()+"/"+s.b.token+"/"+s.id,
	)
}

// Close ends the session. A later prompt from its ssh process gets no answer.
func (s *Session) Close() {
	s.b.mu.Lock()
	delete(s.b.sessions, s.id)
	s.b.mu.Unlock()
}

func (b *Broker) serve() {
	for {
		c, err := b.ln.Accept()
		if err != nil {
			return
		}
		go b.handle(c)
	}
}

// requestTimeout bounds the wait for a helper to send its request.
const requestTimeout = 10 * time.Second

func (b *Broker) handle(c net.Conn) {
	if !b.hold(c) {
		_ = c.Close()
		return
	}
	defer b.drop(c)
	_ = c.SetReadDeadline(time.Now().Add(requestTimeout))
	var req request
	if err := json.NewDecoder(c).Decode(&req); err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(b.token)) != 1 {
		return
	}
	b.mu.Lock()
	s := b.sessions[req.Session]
	b.mu.Unlock()
	if s == nil {
		return
	}
	if req.Kind == kindNotify {
		b.human.Notify(s.host, !s.interactive, req.Prompt)
		// ssh stops the helper when the action is done.
		_, _ = io.Copy(io.Discard, c)
		return
	}
	answer, ok := b.answer(s, req)
	_ = json.NewEncoder(c).Encode(reply{OK: ok, Answer: answer})
}

// answer finds the reply to one prompt: a remembered answer, or a new one from
// the person when the connection may ask.
func (b *Broker) answer(s *Session, req request) (string, bool) {
	b.askMu.Lock()
	defer b.askMu.Unlock()

	key := asker{pid: req.PID, prompt: req.Prompt}
	b.mu.Lock()
	if s.given[key] {
		// The same process asks again, so the answer that it got was wrong.
		delete(b.answers, req.Prompt)
	}
	s.given[key] = true
	answer, known := b.answers[req.Prompt]
	refuse := s.refuse
	b.mu.Unlock()

	if known {
		b.markHeard()
		return answer, true
	}
	if !s.interactive {
		if refuse != nil {
			refuse(req.Prompt)
		}
		return "", false
	}
	echo := echoes(req.Prompt)
	answer, ok := b.human.Ask(req.Prompt, echo)
	if !ok {
		return "", false
	}
	b.mu.Lock()
	if !echo {
		// An empty answer is a decision too: it skips a key.
		b.answers[req.Prompt] = answer
	}
	b.heard = true
	b.mu.Unlock()
	return answer, true
}

func (b *Broker) markHeard() {
	b.mu.Lock()
	b.heard = true
	b.mu.Unlock()
}

// echoes reports whether the answer to prompt is not a secret. ssh does not say,
// so the host key question is found by its text.
func echoes(prompt string) bool {
	return strings.Contains(prompt, "(yes/no")
}

// Requested reports whether this process must act as the askpass helper.
func Requested() bool { return os.Getenv(envBroker) != "" }

// Helper is the askpass program that ssh runs. It sends the prompt in args to
// the Broker and writes the answer to stdout.
func Helper(args []string, stdout io.Writer) int {
	return helper(os.Getenv, os.Getppid(), args, stdout)
}

func helper(getenv func(string) string, asker int, args []string, stdout io.Writer) int {
	parts := strings.Split(getenv(envBroker), "/")
	if len(parts) != 3 {
		return 1
	}
	prompt := ""
	if len(args) > 0 {
		prompt = args[0]
	}
	c, err := net.DialTimeout("tcp", parts[0], requestTimeout)
	if err != nil {
		return 1
	}
	defer c.Close()
	req := request{Token: parts[1], Session: parts[2], PID: asker, Kind: getenv("SSH_ASKPASS_PROMPT"), Prompt: prompt}
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return 1
	}
	var rep reply
	// For a notification no reply comes: this read ends when ssh stops the helper.
	if err := json.NewDecoder(c).Decode(&rep); err != nil || !rep.OK {
		return 1
	}
	fmt.Fprintln(stdout, rep.Answer)
	return 0
}
