//go:build unix

package vfs

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests run the real ssh against a real sshd on the loopback interface.

const testPassphrase = "hunter2 pass"

// scriptedHuman is the person at the keyboard: it types the given answers in order.
type scriptedHuman struct {
	mu      sync.Mutex
	answers []string
	asked   []string
	notes   []string
}

func (h *scriptedHuman) Ask(prompt string, echo bool) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.asked = append(h.asked, prompt)
	if len(h.answers) == 0 {
		return "", false
	}
	answer := h.answers[0]
	h.answers = h.answers[1:]
	return answer, true
}

func (h *scriptedHuman) Notify(host string, extra bool, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if extra {
		msg = "pool: " + msg
	}
	h.notes = append(h.notes, msg)
}

// touchRequests returns the touch requests that the person saw for pool connections.
func (h *scriptedHuman) touchRequests() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, n := range h.notes {
		if strings.HasPrefix(n, "pool: ") {
			out = append(out, n)
		}
	}
	return out
}

func (h *scriptedHuman) questions() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.asked...)
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// findTool finds an OpenSSH program. sshd is usually outside a normal user's PATH.
func findTool(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range []string{"/usr/sbin", "/usr/local/sbin", "/sbin", "/usr/libexec"} {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

func runTool(t *testing.T, name string, args ...string) {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	require.NoError(t, err, "%s %v: %s", name, args, out)
}

// realHost starts an sshd that accepts one key, protected by testPassphrase,
// and makes "ssh" on PATH reach it under the returned host name. knownHost
// false leaves the host key unknown, so ssh asks about it.
func realHost(t *testing.T, knownHost bool) (host, knownHosts string) {
	t.Helper()
	host, knownHosts, _ = startHost(t, hostSpec{knownHost: knownHost, passphrase: testPassphrase})
	return host, knownHosts
}

// hostSpec says what the login to a test host needs.
type hostSpec struct {
	knownHost  bool
	passphrase string
	// securityKey makes the key one that a security key holds, so each login needs a touch.
	securityKey bool
}

// need finds a program that the test cannot run without. CI must have it.
func need(t *testing.T, name string, paths ...string) string {
	t.Helper()
	found := findTool(name)
	for _, p := range paths {
		if found != "" {
			break
		}
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			found = p
		}
	}
	if found == "" {
		require.Empty(t, os.Getenv("CI"), "%s is not installed; CI must run this test", name)
		t.Skipf("%s is not installed", name)
	}
	return found
}

// touchDelay is how long the person takes to touch the key in these tests.
const touchDelay = "0.3"

// startHost starts an sshd that accepts one key and makes "ssh" on PATH reach
// it under the returned host name. dir holds the files of the host.
func startHost(t *testing.T, spec hostSpec) (host, knownHosts, dir string) {
	t.Helper()
	t.Serial()
	tools := map[string]string{}
	for _, name := range []string{"ssh", "sshd", "ssh-keygen"} {
		tools[name] = need(t, name)
	}
	me, err := user.Current()
	require.NoError(t, err)

	dir = t.TempDir()
	hostKey := filepath.Join(dir, "hostkey")
	userKey := filepath.Join(dir, "id")
	runTool(t, tools["ssh-keygen"], "-q", "-t", "ed25519", "-N", "", "-f", hostKey)
	keyOptions := ""
	if spec.securityKey {
		// OpenSSH's own stand-in for a security key. It signs at once, so a wrapper around the helper adds the wait for a touch.
		provider := need(t, "sk-dummy.so", "/usr/lib/openssh/regress/misc/sk-dummy/sk-dummy.so")
		helper := need(t, "ssh-sk-helper", "/usr/lib/openssh/ssh-sk-helper", "/usr/libexec/openssh/ssh-sk-helper", "/usr/lib/ssh/ssh-sk-helper")
		runTool(t, tools["ssh-keygen"], "-q", "-t", "ed25519-sk", "-w", provider, "-N", spec.passphrase, "-f", userKey)
		keyOptions = "  SecurityKeyProvider " + provider + "\n"
		touch := filepath.Join(dir, "touch-helper")
		script := fmt.Sprintf("#!/bin/sh\nsleep %s\n[ -e '%s' ] && exit 1\nexec '%s' \"$@\"\n", touchDelay, filepath.Join(dir, "no-touch"), helper)
		require.NoError(t, os.WriteFile(touch, []byte(script), 0o755))
		t.Setenv("SSH_SK_HELPER", touch)
	} else {
		runTool(t, tools["ssh-keygen"], "-q", "-t", "ed25519", "-N", spec.passphrase, "-f", userKey)
	}
	pub, err := os.ReadFile(userKey + ".pub")
	require.NoError(t, err)
	authorized := filepath.Join(dir, "authorized_keys")
	require.NoError(t, os.WriteFile(authorized, pub, 0o600))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())

	serverConfig := filepath.Join(dir, "sshd_config")
	require.NoError(t, os.WriteFile(serverConfig, []byte(fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s
PidFile %s
AuthorizedKeysFile %s
PermitRootLogin yes
StrictModes no
UsePAM no
PasswordAuthentication no
KbdInteractiveAuthentication no
MaxStartups 100
Subsystem sftp internal-sftp
`, port, hostKey, filepath.Join(dir, "pid"), authorized)), 0o600))

	if os.Geteuid() == 0 {
		// A root sshd refuses to start without its privilege separation directory.
		_ = os.MkdirAll("/run/sshd", 0o755)
	}
	logPath := filepath.Join(dir, "sshd.log")
	server := exec.Command(tools["sshd"], "-D", "-f", serverConfig, "-E", logPath)
	require.NoError(t, server.Start())
	t.Cleanup(func() {
		_ = server.Process.Kill()
		_ = server.Wait()
		if t.Failed() {
			log, _ := os.ReadFile(logPath)
			t.Logf("sshd log:\n%s", log)
		}
	})
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	require.Eventually(t, func() bool {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}, 10*time.Second, 20*time.Millisecond, "sshd did not start to listen")

	knownHosts = filepath.Join(dir, "known_hosts")
	if spec.knownHost {
		hostPub, err := os.ReadFile(hostKey + ".pub")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(knownHosts, []byte(fmt.Sprintf("[127.0.0.1]:%d %s", port, hostPub)), 0o600))
	}
	host = "basicopy-test-host"
	clientConfig := filepath.Join(dir, "ssh_config")
	require.NoError(t, os.WriteFile(clientConfig, []byte(fmt.Sprintf(`Host %s
  HostName 127.0.0.1
  Port %d
  User %s
  IdentityFile %s
  IdentitiesOnly yes
  IdentityAgent none
  PreferredAuthentications publickey
  UserKnownHostsFile %s
  GlobalKnownHostsFile /dev/null
%s`, host, port, me.Username, userKey, knownHosts, keyOptions)), 0o600))

	// basicopy has no option to name a config file, by design. The test gives it one through a wrapper.
	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.Mkdir(bin, 0o755))
	wrapper := fmt.Sprintf("#!/bin/sh\nexec '%s' -F '%s' \"$@\"\n", tools["ssh"], clientConfig)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ssh"), []byte(wrapper), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return host, knownHosts, dir
}

// widen holds transfers open until the pool has tried to grow by extra connections.
func widen(t *testing.T, fsys FS, path string, extra int) {
	t.Helper()
	rf := fsys.(*remoteFS)
	var files []File
	for i := 0; i <= extra; i++ {
		f, err := fsys.Open(path)
		require.NoError(t, err)
		files = append(files, f)
		// Mark every connection busy, so the next transfer must get its own.
		rf.mu.Lock()
		for _, c := range rf.conns {
			c.inflight.Store(1)
		}
		rf.mu.Unlock()
	}
	rf.mu.Lock()
	for _, c := range rf.conns {
		c.inflight.Store(0)
	}
	rf.mu.Unlock()
	for _, f := range files {
		require.NoError(t, f.Close())
	}
}

func tempFileOnHost(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "f.txt")
	require.NoError(t, os.WriteFile(path, []byte("hello"), 0o644))
	return path
}

// TestPassphraseKeyWidensThePool is the regression test for a key with a
// passphrase: one question to the person. Each later connection is a new ssh
// process that gets the answer from the broker.
func TestPassphraseKeyWidensThePool(t *testing.T) {
	host, _ := realHost(t, true)
	human := &scriptedHuman{answers: []string{testPassphrase}}
	var diag syncBuffer
	reg := NewRegistry()
	reg.Human = human
	reg.Diag = &diag
	t.Cleanup(func() { _ = reg.Close() })

	path := tempFileOnHost(t)
	fsys, _, err := reg.Resolve(host + ":" + path)
	require.NoError(t, err, "diagnostics: %s", diag.String())

	widen(t, fsys, path, 3)
	open, frozen := fsys.(Pooled).Pool()
	assert.Equal(t, 4, open, "each concurrent transfer must get its own ssh connection; diagnostics: %s", diag.String())
	assert.False(t, frozen, "diagnostics: %s", diag.String())

	asked := human.questions()
	require.Len(t, asked, 1, "the person must type the passphrase one time")
	assert.Contains(t, asked[0], "Enter passphrase for key")
	assert.Empty(t, diag.String())
}

// TestWrongPassphraseGetsAnotherTry: ssh asks again after a wrong passphrase,
// and the pool then uses the answer that worked.
func TestWrongPassphraseGetsAnotherTry(t *testing.T) {
	host, _ := realHost(t, true)
	human := &scriptedHuman{answers: []string{"a typo", testPassphrase}}
	var diag syncBuffer
	reg := NewRegistry()
	reg.Human = human
	reg.Diag = &diag
	t.Cleanup(func() { _ = reg.Close() })

	path := tempFileOnHost(t)
	fsys, _, err := reg.Resolve(host + ":" + path)
	require.NoError(t, err, "diagnostics: %s", diag.String())
	assert.Len(t, human.questions(), 2)

	widen(t, fsys, path, 1)
	open, frozen := fsys.(Pooled).Pool()
	assert.Equal(t, 2, open, "diagnostics: %s", diag.String())
	assert.False(t, frozen)
	assert.Len(t, human.questions(), 2, "a pool connection must not ask the person")
}

// TestUnknownHostKeyIsAskedOnce: the host key question reaches the person
// through the broker, and "yes" lets the copy connect.
func TestUnknownHostKeyIsAskedOnce(t *testing.T) {
	host, _ := realHost(t, false)
	human := &scriptedHuman{answers: []string{"yes", testPassphrase}}
	reg := NewRegistry()
	reg.Human = human
	t.Cleanup(func() { _ = reg.Close() })

	path := tempFileOnHost(t)
	fsys, _, err := reg.Resolve(host + ":" + path)
	require.NoError(t, err)
	asked := human.questions()
	require.Len(t, asked, 2)
	assert.Contains(t, asked[0], "(yes/no")
	assert.Contains(t, asked[1], "Enter passphrase for key")

	// The first connection recorded the host key, so a pool connection has nothing new to ask.
	widen(t, fsys, path, 1)
	open, frozen := fsys.(Pooled).Pool()
	assert.Equal(t, 2, open)
	assert.False(t, frozen)
	assert.Len(t, human.questions(), 2)
}

// TestPoolConnectionWithANewQuestionStops: a connection that widens the pool
// meets a question that the person never answered. That ssh is stopped, the
// copy says why one time, and the transfers share the connections that exist.
func TestPoolConnectionWithANewQuestionStops(t *testing.T) {
	host, knownHosts := realHost(t, true)
	human := &scriptedHuman{answers: []string{testPassphrase}}
	var diag syncBuffer
	reg := NewRegistry()
	reg.Human = human
	reg.Diag = &diag
	t.Cleanup(func() { _ = reg.Close() })

	path := tempFileOnHost(t)
	fsys, _, err := reg.Resolve(host + ":" + path)
	require.NoError(t, err)

	// The host key is forgotten, so the next ssh asks about it.
	require.NoError(t, os.Remove(knownHosts))
	widen(t, fsys, path, 2)

	open, frozen := fsys.(Pooled).Pool()
	assert.Equal(t, 1, open)
	assert.True(t, frozen)
	assert.Len(t, human.questions(), 1, "only the first connection may ask the person")
	assert.Equal(t, 1, strings.Count(diag.String(), "only you can answer that"), "diagnostics: %s", diag.String())
	assert.Contains(t, diag.String(), "ssh: "+host+": another connection asks \"The authenticity of host")
}

// TestSecurityKeyTouchIsShownForEachConnection is the regression test for a
// key that needs a touch. Each pool connection is a new login, so each needs
// a touch. The person must see ssh's request for every one, and the pool grows
// as the touches come.
func TestSecurityKeyTouchIsShownForEachConnection(t *testing.T) {
	host, _, _ := startHost(t, hostSpec{knownHost: true, securityKey: true})
	human := &scriptedHuman{}
	var diag syncBuffer
	reg := NewRegistry()
	reg.Human = human
	reg.Diag = &diag
	t.Cleanup(func() { _ = reg.Close() })

	path := tempFileOnHost(t)
	fsys, _, err := reg.Resolve(host + ":" + path)
	require.NoError(t, err, "diagnostics: %s", diag.String())

	widen(t, fsys, path, 2)
	open, frozen := fsys.(Pooled).Pool()
	assert.Equal(t, 3, open, "diagnostics: %s", diag.String())
	assert.False(t, frozen, "diagnostics: %s", diag.String())

	requests := human.touchRequests()
	require.Len(t, requests, 2, "each pool connection must show its touch request")
	for _, r := range requests {
		assert.Contains(t, r, "Confirm user presence for key ED25519-SK")
	}
	assert.Empty(t, human.questions(), "a touch is not a question")
}

// TestSecurityKeyWithPassphraseAsksOnceAndShowsEachTouch: the key file has a
// passphrase and the key needs a touch. The passphrase is typed one time, and
// the touch request shows for each connection.
func TestSecurityKeyWithPassphraseAsksOnceAndShowsEachTouch(t *testing.T) {
	host, _, _ := startHost(t, hostSpec{knownHost: true, securityKey: true, passphrase: testPassphrase})
	human := &scriptedHuman{answers: []string{testPassphrase}}
	var diag syncBuffer
	reg := NewRegistry()
	reg.Human = human
	reg.Diag = &diag
	t.Cleanup(func() { _ = reg.Close() })

	path := tempFileOnHost(t)
	fsys, _, err := reg.Resolve(host + ":" + path)
	require.NoError(t, err, "diagnostics: %s", diag.String())

	widen(t, fsys, path, 2)
	open, frozen := fsys.(Pooled).Pool()
	assert.Equal(t, 3, open, "diagnostics: %s", diag.String())
	assert.False(t, frozen, "diagnostics: %s", diag.String())
	assert.Len(t, human.questions(), 1, "the passphrase must be typed one time")
	assert.Len(t, human.touchRequests(), 2)
}

// TestSecurityKeyWithoutATouchKeepsThePool: the person does not touch the key
// for a pool connection. That login fails, and the copy stays on the
// connection it has.
func TestSecurityKeyWithoutATouchKeepsThePool(t *testing.T) {
	host, _, dir := startHost(t, hostSpec{knownHost: true, securityKey: true})
	human := &scriptedHuman{}
	var diag syncBuffer
	reg := NewRegistry()
	reg.Human = human
	reg.Diag = &diag
	t.Cleanup(func() { _ = reg.Close() })

	path := tempFileOnHost(t)
	fsys, _, err := reg.Resolve(host + ":" + path)
	require.NoError(t, err, "diagnostics: %s", diag.String())

	require.NoError(t, os.WriteFile(filepath.Join(dir, "no-touch"), nil, 0o600))
	widen(t, fsys, path, 2)

	open, frozen := fsys.(Pooled).Pool()
	assert.Equal(t, 1, open)
	assert.True(t, frozen)
	assert.Len(t, human.touchRequests(), 1, "the request was shown, and no second one after the refusal")
	assert.Contains(t, diag.String(), "Permission denied", "ssh's own words reach the person")
}

func TestFirstLine(t *testing.T) {
	assert.Equal(t, "Password:", firstLine("  Password: \n"))
	assert.Equal(t, "The authenticity of host 'pc' can't be established.", firstLine("The authenticity of host 'pc' can't be established.\nED25519 key fingerprint is SHA256:abc.\n"))
	long := firstLine(strings.Repeat("x", 300))
	assert.Len(t, long, 103)
	assert.True(t, strings.HasSuffix(long, "..."))
}

// TestNobodyAnswersFailsTheFirstConnection: when the person gives no answer,
// the copy stops with an error. It does not wait.
func TestNobodyAnswersFailsTheFirstConnection(t *testing.T) {
	host, _ := realHost(t, true)
	human := &scriptedHuman{}
	reg := NewRegistry()
	reg.Human = human
	t.Cleanup(func() { _ = reg.Close() })

	_, _, err := reg.Resolve(host + ":/")
	require.Error(t, err)
	assert.NotEmpty(t, human.questions())
	assert.True(t, strings.Contains(err.Error(), host), err.Error())
}
