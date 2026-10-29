# The receiving half of the cross-host check. $XPLAT_IN holds one directory per sending host, as produce.dats left it.
# A manifest from another host is compared on data and exact mtimes. Its mode spelling is that host's own.
tests:
	- desc: the {matrix.origin} copy holds the same bytes as this host's fixture
	  matrix:
		origin: [linux, darwin, windows]
	  cmd: |
		set -e
		cat "$XPLAT_IN/{matrix.origin}/host"
		node dats/fstree.mjs make {outputs.local}
		node dats/fstree.mjs snap {outputs.local} > {outputs.local.json}
		node dats/fstree.mjs verify {outputs.local.json} "$XPLAT_IN/{matrix.origin}/tree" --only=data
	  outputs:
		stdout:
			0: "^{matrix.origin}$"
			1: "^same: 40 entries$"

	- desc: the hand-off of the {matrix.origin} tree kept what its manifest says
	  matrix:
		origin: [linux, darwin, windows]
	  cmd: node dats/fstree.mjs verify "$XPLAT_IN/{matrix.origin}/manifest.json" "$XPLAT_IN/{matrix.origin}/tree" --only=data,mtime
	  outputs:
		stdout:
			- "same: 40 entries"

	- desc: basicopy on this host copies the {matrix.origin} tree exactly
	  matrix:
		origin: [linux, darwin, windows]
	  cmd: |
		set -e
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" "$XPLAT_IN/{matrix.origin}/tree" --contents --target-dir {outputs.dst} --no-follow-symlinks -q
		node dats/fstree.mjs compare "$XPLAT_IN/{matrix.origin}/tree" {outputs.dst} --only=data,mtime,mode
		node dats/fstree.mjs verify "$XPLAT_IN/{matrix.origin}/manifest.json" {outputs.dst} --only=data,mtime
	  outputs:
		stdout:
			0: "^same: 40 entries$"
			1: "^same: 40 entries$"
