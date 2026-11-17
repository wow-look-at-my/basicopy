package vfs

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHostIsNotWindowsOnLinux(t *testing.T) {
	assert.False(t, hostIsWindows())
}
