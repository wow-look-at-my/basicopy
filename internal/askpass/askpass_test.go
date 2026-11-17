package askpass

import (
	"bytes"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const keyPrompt = "Enter passphrase for key '/home/me/.ssh/id_ed25519': "

// fakeHuman answers from a script and records what it was shown.
type fakeHuman struct {
	mu      sync.Mutex
	answers []string
	asked   []string
	echoed  []bool
	notes   []string
}

func (h *fakeHuman) Ask(prompt string, echo bool) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.asked = append(h.asked, prompt)
	h.echoed = append(h.echoed, echo)
	if len(h.answers) == 0 {
		return "", false
	}
	answer := h.answers[0]
	h.answers = h.answers[1:]
	return answer, true
}

func (h *fakeHuman) Notify(host string, extra bool, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	note := host + ": " + msg
	if extra {
		note = host + " (extra): " + msg
	}
	h.notes = append(h.notes, note)
}

func (h *fakeHuman) askCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.asked)
}

func (h *fakeHuman) noteList() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.notes...)
}

func startBroker(t *testing.T, answers ...string) (*Broker, *fakeHuman) {
	t.Helper()
	h := &fakeHuman{answers: answers}
	b, err := Start(h)
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	return b, h
}

// envOf turns the environment of a session into a lookup, as the helper sees it.
func envOf(s *Session, extra ...string) func(string) string {
	vars := map[string]string{}
	for _, kv := range append(s.Env(nil), extra...) {
		k, v, _ := strings.Cut(kv, "=")
		vars[k] = v
	}
	return func(k string) string { return vars[k] }
}

// ask runs the helper the way ssh process pid would, and returns what ssh reads.
func ask(s *Session, pid int, prompt string) (answer string, code int) {
	var out bytes.Buffer
	code = helper(envOf(s), pid, []string{prompt}, &out)
	return out.String(), code
}

// TestOneAnswerServesEveryConnection is the point of the package: the person
// types a passphrase one time, and each connection that widens the pool gets it.
func TestOneAnswerServesEveryConnection(t *testing.T) {
	b, h := startBroker(t, "hunter2")
	assert.False(t, b.Heard())

	first := b.Session("pc", true)
	got, code := ask(first, 100, keyPrompt)
	assert.Zero(t, code)
	assert.Equal(t, "hunter2\n", got)
	assert.True(t, b.Heard(), "an answer proves that ssh sends its prompts to the broker")

	for pid := 200; pid < 203; pid++ {
		got, code := ask(b.Session("pc", false), pid, keyPrompt)
		assert.Zero(t, code)
		assert.Equal(t, "hunter2\n", got)
	}
	assert.Equal(t, 1, h.askCount(), "the person must be asked one time only")
	assert.Equal(t, []bool{false}, h.echoed, "a passphrase is read with no echo")
}

// TestWrongAnswerIsAskedAgain: ssh repeats a prompt when the answer did not
// open the key. The person gets another try, and the pool gets the good answer.
func TestWrongAnswerIsAskedAgain(t *testing.T) {
	b, h := startBroker(t, "typo", "hunter2")
	first := b.Session("pc", true)

	got, _ := ask(first, 100, keyPrompt)
	assert.Equal(t, "typo\n", got)
	got, _ = ask(first, 100, keyPrompt)
	assert.Equal(t, "hunter2\n", got)
	assert.Equal(t, 2, h.askCount())

	got, code := ask(b.Session("pc", false), 200, keyPrompt)
	assert.Zero(t, code)
	assert.Equal(t, "hunter2\n", got)
}

// TestExtraConnectionNeverAsksThePerson: a connection that widens the pool gets
// no answer to a new question, and its owner is told, so it can stop that ssh.
func TestExtraConnectionNeverAsksThePerson(t *testing.T) {
	b, h := startBroker(t, "never used")
	extra := b.Session("pc", false)
	var refused []string
	extra.OnRefuse(func(prompt string) { refused = append(refused, prompt) })

	got, code := ask(extra, 200, "Verification code: ")
	assert.Equal(t, 1, code)
	assert.Empty(t, got)
	assert.Equal(t, []string{"Verification code: "}, refused)
	assert.Zero(t, h.askCount())
	assert.False(t, b.Heard())
}

// TestExtraConnectionGetsAnAnswerOneTime: when the remembered answer fails on a
// pool connection, that connection stops. It does not try the answer again.
func TestExtraConnectionGetsAnAnswerOneTime(t *testing.T) {
	b, h := startBroker(t, "123456")
	_, _ = ask(b.Session("pc", true), 100, "Verification code: ")

	extra := b.Session("pc", false)
	refused := 0
	extra.OnRefuse(func(string) { refused++ })
	got, code := ask(extra, 200, "Verification code: ")
	assert.Zero(t, code)
	assert.Equal(t, "123456\n", got)

	_, code = ask(extra, 200, "Verification code: ")
	assert.Equal(t, 1, code)
	assert.Equal(t, 1, refused)
	assert.Equal(t, 1, h.askCount())
}

// TestJumpHostAsksTheSameKeyTwice: with ProxyJump, ssh processes of one
// connection ask for the same key. The second question is not a wrong answer.
func TestJumpHostAsksTheSameKeyTwice(t *testing.T) {
	b, h := startBroker(t, "hunter2")
	first := b.Session("pc", true)
	_, _ = ask(first, 100, keyPrompt)
	got, code := ask(first, 101, keyPrompt)
	assert.Zero(t, code)
	assert.Equal(t, "hunter2\n", got)
	assert.Equal(t, 1, h.askCount())

	extra := b.Session("pc", false)
	_, code = ask(extra, 200, keyPrompt)
	assert.Zero(t, code)
	_, code = ask(extra, 201, keyPrompt)
	assert.Zero(t, code)
}

// TestSkippedKeyStaysSkipped: an empty answer makes ssh try the next key. A
// pool connection must repeat that decision, not stop at the first key.
func TestSkippedKeyStaysSkipped(t *testing.T) {
	b, _ := startBroker(t, "", "hunter2")
	first := b.Session("pc", true)
	_, _ = ask(first, 100, "Enter passphrase for key 'a': ")
	_, _ = ask(first, 100, "Enter passphrase for key 'b': ")

	extra := b.Session("pc", false)
	got, code := ask(extra, 200, "Enter passphrase for key 'a': ")
	assert.Zero(t, code)
	assert.Equal(t, "\n", got)
	got, code = ask(extra, 200, "Enter passphrase for key 'b': ")
	assert.Zero(t, code)
	assert.Equal(t, "hunter2\n", got)
}

// TestHostKeyQuestionIsEchoedAndNotRemembered: "yes" to one host is not an
// answer for a later question.
func TestHostKeyQuestionIsEchoedAndNotRemembered(t *testing.T) {
	const q = "The authenticity of host 'pc' can't be established.\nAre you sure you want to continue connecting (yes/no/[fingerprint])? "
	b, h := startBroker(t, "yes", "no")
	got, _ := ask(b.Session("pc", true), 100, q)
	assert.Equal(t, "yes\n", got)
	got, _ = ask(b.Session("other", true), 300, q)
	assert.Equal(t, "no\n", got)
	assert.Equal(t, []bool{true, true}, h.echoed)

	extra := b.Session("pc", false)
	_, code := ask(extra, 200, q)
	assert.Equal(t, 1, code, "a pool connection must not accept a host key by itself")
}

// TestNobodyAnswers: with no answer from the person, ssh gets a failed askpass.
func TestNobodyAnswers(t *testing.T) {
	b, h := startBroker(t)
	got, code := ask(b.Session("pc", true), 100, keyPrompt)
	assert.Equal(t, 1, code)
	assert.Empty(t, got)
	assert.Equal(t, 1, h.askCount())
	assert.False(t, b.Heard())
}

// TestNotifyReachesThePerson: a key touch request is shown, and the helper
// waits until ssh stops it. Here the broker closes, which ends the wait.
func TestNotifyReachesThePerson(t *testing.T) {
	const msg = "Confirm user presence for key ED25519-SK SHA256:abc"
	b, h := startBroker(t)
	extra := b.Session("pc", false)

	done := make(chan int, 1)
	go func() {
		var out bytes.Buffer
		done <- helper(envOf(extra, "SSH_ASKPASS_PROMPT=none"), 200, []string{msg}, &out)
	}()
	require.Eventually(t, func() bool { return len(h.noteList()) == 1 }, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"pc (extra): " + msg}, h.noteList())
	select {
	case <-done:
		t.Fatal("the helper must wait while the touch is pending")
	case <-time.After(50 * time.Millisecond):
	}

	require.NoError(t, b.Close())
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the helper must end when the broker closes")
	}
	assert.Zero(t, h.askCount())
}

// TestStrangerGetsNothing: a process without the token, or with a closed
// session, gets no answer and reaches nobody.
func TestStrangerGetsNothing(t *testing.T) {
	b, h := startBroker(t, "hunter2")
	s := b.Session("pc", true)
	env := envOf(s)

	wrongToken := func(k string) string {
		if k == envBroker {
			return b.ln.Addr().String() + "/" + strings.Repeat("0", 32) + "/" + s.id
		}
		return env(k)
	}
	var out bytes.Buffer
	assert.Equal(t, 1, helper(wrongToken, 100, []string{keyPrompt}, &out))

	// A request that is not the protocol is dropped.
	c, err := net.Dial("tcp", b.ln.Addr().String())
	require.NoError(t, err)
	_, _ = c.Write([]byte("not json\n"))
	var rep reply
	assert.Error(t, json.NewDecoder(c).Decode(&rep))
	_ = c.Close()

	s.Close()
	assert.Equal(t, 1, helper(env, 100, []string{keyPrompt}, &out))

	malformed := func(string) string { return "no-separators" }
	assert.Equal(t, 1, helper(malformed, 100, []string{keyPrompt}, &out))
	unreachable := func(string) string { return "127.0.0.1:1/token/1" }
	assert.Equal(t, 1, helper(unreachable, 100, nil, &out))

	assert.Empty(t, out.String())
	assert.Zero(t, h.askCount())
}

// TestClosedBrokerAnswersNobody: after Close, a helper cannot connect.
func TestClosedBrokerAnswersNobody(t *testing.T) {
	b, h := startBroker(t, "hunter2")
	s := b.Session("pc", true)
	require.NoError(t, b.Close())
	_, code := ask(s, 100, keyPrompt)
	assert.Equal(t, 1, code)
	assert.Zero(t, h.askCount())
}

// TestEnvPointsSSHAtTheBroker: the person's own askpass settings are replaced
// for the child, and all other variables stay.
func TestEnvPointsSSHAtTheBroker(t *testing.T) {
	b, _ := startBroker(t)
	s := b.Session("pc", true)
	env := s.Env([]string{"HOME=/home/me", "SSH_ASKPASS=/usr/bin/ksshaskpass", "SSH_ASKPASS_REQUIRE=never", envBroker + "=stale"})

	assert.Contains(t, env, "HOME=/home/me")
	assert.Contains(t, env, "SSH_ASKPASS="+b.self)
	assert.Contains(t, env, "SSH_ASKPASS_REQUIRE=force")
	assert.NotContains(t, env, "SSH_ASKPASS=/usr/bin/ksshaskpass")
	assert.NotContains(t, env, "SSH_ASKPASS_REQUIRE=never")
	assert.NotContains(t, env, envBroker+"=stale")
	assert.Len(t, env, 4)
	assert.FileExists(t, b.self)
}

func TestRequested(t *testing.T) {
	t.Setenv(envBroker, "")
	assert.False(t, Requested())
	t.Setenv(envBroker, "127.0.0.1:1/token/1")
	assert.True(t, Requested())
	var out bytes.Buffer
	assert.Equal(t, 1, Helper([]string{keyPrompt}, &out), "no broker listens there")
}
