# The sending half of the cross-host check. It writes into $XPLAT_OUT, so it runs only under --no-sandbox in CI.
# This host builds the fixture, copies it with basicopy, and leaves the copy and its manifest for the other hosts.
tests:
	- desc: this host's copy of the fixture goes out with its manifest
	  cmd: |
		set -e
		test -n "$XPLAT_OUT"
		node dats/fstree.mjs make {outputs.src}
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {outputs.src} --contents --target-dir "$XPLAT_OUT/tree" --no-follow-symlinks -q
		node dats/fstree.mjs compare {outputs.src} "$XPLAT_OUT/tree"
		node dats/fstree.mjs snap "$XPLAT_OUT/tree" > "$XPLAT_OUT/manifest.json"
		node dats/fstree.mjs host > "$XPLAT_OUT/host"
	  outputs:
		stdout:
			- "same: 40 entries"
