package vfs

import (
	"os"
	"testing"

	"github.com/wow-look-at-my/basicopy/internal/askpass"
)

// A real ssh in these tests runs this test binary as its askpass program, as it runs basicopy.
func TestMain(m *testing.M) {
	if askpass.Requested() {
		os.Exit(askpass.Helper(os.Args[1:], os.Stdout))
	}
	os.Exit(m.Run())
}
