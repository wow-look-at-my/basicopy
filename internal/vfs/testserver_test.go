package vfs

import (
	"io"
	"testing"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/require"
)

// pipeTransport is the seam that lets these tests speak the real SFTP protocol
// without an ssh binary, a network, or credentials: an in-process sftp server
// (the same package's server implementation, operating on real files) wired to
// the client through a pair of pipes. Everything above the pipes -- packets,
// handles, extensions, error codes -- is exactly what a remote host would do.
func pipeTransport(loc Location, extra bool) (*Transport, error) {
	srvIn, cliOut := io.Pipe()
	cliIn, srvOut := io.Pipe()

	server, err := sftp.NewServer(rwc{Reader: srvIn, WriteCloser: srvOut})
	if err != nil {
		return nil, err
	}
	// Closing the server when Serve returns mirrors what a real ssh subprocess
	// does when its stdin closes: the far end goes away and the client's read
	// side sees EOF instead of blocking forever.
	go func() {
		_ = server.Serve()
		_ = server.Close()
	}()

	return &Transport{Stdout: cliIn, Stdin: cliOut, Close: func() error {
		_ = server.Close()
		_ = srvIn.Close()
		_ = cliIn.Close()
		return nil
	}}, nil
}

type rwc struct {
	io.Reader
	io.WriteCloser
}

// testFS returns an FS talking to an in-process SFTP server, standing in for
// "somehost:".
func testFS(t *testing.T) FS {
	t.Helper()
	old := StartTransport
	StartTransport = pipeTransport
	t.Cleanup(func() { StartTransport = old })

	loc, err := Parse("somehost:/")
	require.NoError(t, err)
	fsys, err := Dial(loc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = fsys.Close() })
	return fsys
}
