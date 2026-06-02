package options

import "testing"

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"", 0, true},
		{"0", 0, true},
		{"512", 512, true},
		{"512b", 512, true},
		{"4k", 4000, true},
		{"4kb", 4000, true},
		{"4ki", 4096, true},
		{"4kib", 4096, true},
		{"64mib", 64 << 20, true},
		{"64MiB", 64 << 20, true},
		{"1MB", 1_000_000, true},
		{"1.5kib", 1536, true},
		{"2gib", 2 << 30, true},
		{"1tib", 1 << 40, true},
		{"  8MiB  ", 8 << 20, true},
		{"abc", 0, false},
		{"10xb", 0, false},
		{"mib", 0, false},
		{"-5", 0, false},
	}
	for _, c := range cases {
		got, err := ParseSize(c.in)
		if c.ok && err != nil {
			t.Errorf("ParseSize(%q) unexpected error: %v", c.in, err)
			continue
		}
		if !c.ok && err == nil {
			t.Errorf("ParseSize(%q) expected error, got %d", c.in, got)
			continue
		}
		if c.ok && got != c.want {
			t.Errorf("ParseSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func baseValid() Options {
	return Options{Sources: []string{"a"}, TargetDir: "dst", Progress: "auto"}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		mod  func(*Options)
		ok   bool
	}{
		{"valid target-dir", func(o *Options) {}, true},
		{"valid target-file", func(o *Options) { o.TargetDir = ""; o.TargetFile = "f" }, true},
		{"no sources", func(o *Options) { o.Sources = nil }, false},
		{"no destination", func(o *Options) { o.TargetDir = "" }, false},
		{"both destinations", func(o *Options) { o.TargetFile = "f" }, false},
		{"target-file multi source", func(o *Options) { o.TargetDir = ""; o.TargetFile = "f"; o.Sources = []string{"a", "b"} }, false},
		{"mirror needs target-dir", func(o *Options) { o.TargetDir = ""; o.TargetFile = "f"; o.Mirror = true }, false},
		{"mirror with target-dir ok", func(o *Options) { o.Mirror = true }, true},
		{"verbose+quiet", func(o *Options) { o.Verbose = true; o.Quiet = true }, false},
		{"bad progress", func(o *Options) { o.Progress = "loud" }, false},
		{"negative threads", func(o *Options) { o.MaxThreads = -1 }, false},
	}
	for _, c := range cases {
		o := baseValid()
		c.mod(&o)
		err := o.Validate()
		if c.ok && err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: expected error, got nil", c.name)
		}
	}
}
