package vfs

import (
	"fmt"
	"runtime"
	"strings"
)

// Location is a parsed source or destination argument: either a local path or a
// remote one written the way scp/rsync write it, "[user@]host:/path", or as an
// "ssh://[user@]host[:port]/path" URL.
type Location struct {
	User   string // empty means "whatever ssh would pick" (ssh_config / current user)
	Host   string // empty for a local path
	Port   string // empty means ssh's default (or whatever ssh_config says)
	Path   string // the path on that host; "." means the remote login directory
	Remote bool

	// Prefix is the "user@host:" text for output, kept in the user's own spelling.
	Prefix string
}

// String renders the location the way the user would have typed it.
func (l Location) String() string { return l.Prefix + l.Path }

// ConnKey lets two arguments naming the same host share one ssh process.
func (l Location) ConnKey() string { return l.User + "@" + l.Host + ":" + l.Port }

const sshScheme = "ssh://"

// Parse splits an argument into a local path or a remote host+path.
//
// The rules match scp/rsync so muscle memory transfers: a colon before the
// first "/" makes it remote ("pc:/srv", "me@pc:notes"), and anything else is
// local. A local path whose first segment really does contain a colon can be
// forced local by writing it as "./weird:name" or with an absolute path.
func Parse(arg string) (Location, error) {
	if strings.HasPrefix(arg, sshScheme) {
		return parseURL(arg)
	}

	host, path, ok := splitHostPath(arg)
	if !ok {
		return Location{Path: arg}, nil
	}
	user, hostname := splitUser(host)
	if hostname == "" {
		return Location{}, fmt.Errorf("remote path %q has no host before the colon", arg)
	}
	if user == "" && strings.Contains(host, "@") {
		return Location{}, fmt.Errorf("remote path %q has no user before the @", arg)
	}
	if path == "" {
		path = "." // "host:" means the remote login directory
	}
	return Location{
		User:   user,
		Host:   unbracket(hostname),
		Path:   path,
		Remote: true,
		Prefix: host + ":",
	}, nil
}

// splitHostPath finds the scp-style "host:path" split, reporting ok=false for a
// plain local path.
func splitHostPath(arg string) (host, path string, ok bool) {
	// A bracketed IPv6 host ("[::1]:/path") keeps its colons inside brackets.
	if strings.HasPrefix(arg, "[") {
		if end := strings.Index(arg, "]"); end > 0 && strings.HasPrefix(arg[end+1:], ":") {
			return arg[:end+1], arg[end+2:], true
		}
	}
	i := strings.IndexByte(arg, ':')
	if i < 0 {
		return "", "", false
	}
	// A "/" before the colon means the colon is inside a path component.
	if strings.ContainsAny(arg[:i], "/\\") {
		return "", "", false
	}
	if i == 0 {
		return "", "", false // ":/foo" is not a host
	}
	// On Windows a single-letter prefix is a drive, not a host: "C:\Users".
	if runtime.GOOS == "windows" && i == 1 {
		return "", "", false
	}
	return arg[:i], arg[i+1:], true
}

// parseURL handles the explicit ssh://[user@]host[:port]/path form, the only
// spelling that can carry a port.
func parseURL(arg string) (Location, error) {
	rest := arg[len(sshScheme):]
	authority, path := rest, ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		authority, path = rest[:i], rest[i:]
	}
	if path == "" {
		path = "."
	}
	user, hostport := splitUser(authority)
	host, port, err := splitHostPort(hostport)
	if err != nil {
		return Location{}, fmt.Errorf("invalid %s: %v", arg, err)
	}
	if host == "" {
		return Location{}, fmt.Errorf("invalid %s: no host", arg)
	}
	prefix := hostport + ":"
	if user != "" {
		prefix = user + "@" + prefix
	}
	return Location{
		User:   user,
		Host:   host,
		Port:   port,
		Path:   path,
		Remote: true,
		Prefix: prefix,
	}, nil
}

func splitUser(s string) (user, host string) {
	if i := strings.LastIndex(s, "@"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}

// splitHostPort separates an optional :port, tolerating a bracketed IPv6 host.
func splitHostPort(s string) (host, port string, err error) {
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return "", "", fmt.Errorf("unterminated [ in host %q", s)
		}
		host = s[1:end]
		switch rest := s[end+1:]; {
		case rest == "":
		case strings.HasPrefix(rest, ":"):
			port = rest[1:]
		default:
			return "", "", fmt.Errorf("unexpected %q after host", rest)
		}
		return host, port, nil
	}
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return s[:i], s[i+1:], nil
	}
	return s, "", nil
}

func unbracket(host string) string {
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return host[1 : len(host)-1]
	}
	return host
}

// AnyRemote reports whether any of the given arguments names a remote path.
func AnyRemote(args ...string) bool {
	for _, a := range args {
		if a == "" {
			continue
		}
		if loc, err := Parse(a); err == nil && loc.Remote {
			return true
		}
	}
	return false
}
