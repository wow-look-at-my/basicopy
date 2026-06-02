//go:build linux

package device

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wow-look-at-my/testify/assert"
	"github.com/wow-look-at-my/testify/require"
)

func TestParseIOTicks(t *testing.T) {
	// major minor name | reads(4) | writes(4) | inflight ioticks weighted
	data := "   7       0 loop0 0 0 0 0 0 0 0 0 0 0 0\n" +
		" 254       0 vda 16816 0 475882 17536 12609 0 304048 3488 0 1060 21243\n" +
		" 254      16 vdb 15886 0 125650 3945 0 0 0 0 0 76 3945\n"

	v, ok := parseIOTicks(data, "vda")
	require.True(t, ok)
	assert.EqualValues(t, 1060, v)

	v, ok = parseIOTicks(data, "vdb")
	require.True(t, ok)
	assert.EqualValues(t, 76, v)

	_, ok = parseIOTicks(data, "nope")
	assert.False(t, ok)
}

func TestReadSysUint(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "rotational")
	require.NoError(t, os.WriteFile(good, []byte("1\n"), 0o644))
	v, ok := readSysUint(good)
	require.True(t, ok)
	assert.EqualValues(t, 1, v)

	bad := filepath.Join(dir, "bad")
	require.NoError(t, os.WriteFile(bad, []byte("not-a-number"), 0o644))
	_, ok = readSysUint(bad)
	assert.False(t, ok)

	_, ok = readSysUint(filepath.Join(dir, "missing"))
	assert.False(t, ok)
}

func TestClassString(t *testing.T) {
	assert.Equal(t, "SSD", SSD.String())
	assert.Equal(t, "HDD", HDD.String())
	assert.Equal(t, "unknown", Unknown.String())
	assert.True(t, Info{Class: HDD}.Rotational())
	assert.False(t, Info{Class: SSD}.Rotational())
}

func TestLookupAndUtilTolerant(t *testing.T) {
	// Exercise the real sysfs/diskstats path; tolerate environments (containers)
	// that don't expose it.
	info := Lookup("/")
	if info.Name == "" {
		t.Skip("no sysfs block device info available in this environment")
	}
	assert.NotEmpty(t, info.DeviceID)

	u := NewUtilSampler(info.Name)
	_, ok := u.Sample()
	assert.False(t, ok, "first sample primes")
	util, ok := u.Sample()
	if ok {
		assert.GreaterOrEqual(t, util, 0.0)
		assert.LessOrEqual(t, util, 100.0)
	}
}

func TestUtilSamplerEmptyName(t *testing.T) {
	u := NewUtilSampler("")
	_, ok := u.Sample()
	assert.False(t, ok)
}
