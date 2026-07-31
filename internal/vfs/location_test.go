package vfs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLocation(t *testing.T) {
	cases := []struct {
		arg    string
		remote bool
		user   string
		host   string
		port   string
		path   string
		prefix string
	}{
		{arg: "/srv/data", path: "/srv/data"},
		{arg: "relative/dir", path: "relative/dir"},
		{arg: "./has:colon", path: "./has:colon"},
		{arg: "/abs/has:colon", path: "/abs/has:colon"},
		{arg: ":/leading", path: ":/leading"},

		{arg: "myotherpc:/home/mhayine/whatever", remote: true, host: "myotherpc", path: "/home/mhayine/whatever", prefix: "myotherpc:"},
		{arg: "mhayine@myotherpc:/srv", remote: true, user: "mhayine", host: "myotherpc", path: "/srv", prefix: "mhayine@myotherpc:"},
		{arg: "pc:notes", remote: true, host: "pc", path: "notes", prefix: "pc:"},
		{arg: "pc:", remote: true, host: "pc", path: ".", prefix: "pc:"},
		{arg: "[2001:db8::1]:/srv", remote: true, host: "2001:db8::1", path: "/srv", prefix: "[2001:db8::1]:"},

		{arg: "ssh://pc/srv", remote: true, host: "pc", path: "/srv", prefix: "pc:"},
		{arg: "ssh://me@pc:2222/srv/data", remote: true, user: "me", host: "pc", port: "2222", path: "/srv/data", prefix: "me@pc:2222:"},
		{arg: "ssh://[2001:db8::1]:2222/srv", remote: true, host: "2001:db8::1", port: "2222", path: "/srv", prefix: "[2001:db8::1]:2222:"},
		{arg: "ssh://pc", remote: true, host: "pc", path: ".", prefix: "pc:"},
	}
	for _, c := range cases {
		t.Run(c.arg, func(t *testing.T) {
			loc, err := Parse(c.arg)
			require.NoError(t, err)
			assert.Equal(t, c.remote, loc.Remote, "remote")
			assert.Equal(t, c.user, loc.User, "user")
			assert.Equal(t, c.host, loc.Host, "host")
			assert.Equal(t, c.port, loc.Port, "port")
			assert.Equal(t, c.path, loc.Path, "path")
			if c.remote {
				assert.Equal(t, c.prefix, loc.Prefix, "prefix")
				assert.Equal(t, c.prefix+c.path, loc.String())
			}
		})
	}
}

func TestParseLocationErrors(t *testing.T) {
	for _, arg := range []string{"@pc:/srv", "ssh:///srv", "ssh://[bad/srv"} {
		_, err := Parse(arg)
		assert.Error(t, err, "%q must not parse as a location", arg)
	}
}

func TestConnKeySharesOneConnectionPerHost(t *testing.T) {
	a, err := Parse("me@pc:/one")
	require.NoError(t, err)
	b, err := Parse("me@pc:/two")
	require.NoError(t, err)
	other, err := Parse("you@pc:/two")
	require.NoError(t, err)

	assert.Equal(t, a.ConnKey(), b.ConnKey(), "two paths on one host share a connection")
	assert.NotEqual(t, a.ConnKey(), other.ConnKey(), "a different login is a different connection")
}

func TestAnyRemote(t *testing.T) {
	assert.True(t, AnyRemote("/local", "pc:/remote"))
	assert.False(t, AnyRemote("/local", "", "./also-local"))
}

func TestSSHArgs(t *testing.T) {
	loc, err := Parse("ssh://me@pc:2222/srv")
	require.NoError(t, err)
	assert.Equal(t, []string{"ssh", "-p", "2222", "-l", "me", "-s", "pc", "sftp"}, sshArgs(loc, false))

	// A connection opened to widen the pool must never be able to prompt.
	assert.Equal(t,
		[]string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-p", "2222", "-l", "me", "-s", "pc", "sftp"},
		sshArgs(loc, true))

	plain, err := Parse("pc:/srv")
	require.NoError(t, err)
	assert.Equal(t, []string{"ssh", "-s", "pc", "sftp"}, sshArgs(plain, false),
		"with no user or port, ssh is left to consult the user's own config")
}

func TestSameHost(t *testing.T) {
	assert.True(t, SameHost(Local, Local))
	assert.False(t, SameHost(Local, nil))

	remote := &remoteFS{host: "pc"}
	assert.False(t, SameHost(Local, remote))
	assert.True(t, SameHost(remote, &remoteFS{host: "pc"}))
	assert.False(t, SameHost(remote, &remoteFS{host: "other"}))
}
