# Data fidelity of the built binary on the host it runs on. CI runs this file on Linux, macOS and Windows.
# fstree.mjs builds the same fixture tree on every host, so one expected answer covers all of them.

setup: node dats/fstree.mjs make {shared.src}

tests:
	- desc: a tree copied without dereference is entry-for-entry the source
	  cmd: |
		set -e
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {shared.src} --contents --target-dir {outputs.dst} --no-follow-symlinks -q
		node dats/fstree.mjs compare {shared.src} {outputs.dst} --only=data
	  outputs:
		stdout:
			- "same: 40 entries"

	- desc: the default dereferences in-tree links and keeps a dangling link
	  cmd: |
		set -e
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {shared.src} --contents --target-dir {outputs.dst} -q --no-symlink-warnings
		cat {outputs.dst}/link-file
		cat {outputs.dst}/link-dir/a/b/c/d/e/f/g/h/leaf.txt
		node dats/fstree.mjs entry {outputs.dst} link-dangling
	  outputs:
		stdout:
			0: "^hello$"
			1: "^leaf$"
			2: '"type":"symlink","link":"no-such-target"'

	- desc: a second run over an unchanged tree copies nothing
	  cmd: |
		set -e
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {shared.src} --contents --target-dir {outputs.dst} --no-follow-symlinks -q
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {shared.src} --contents --target-dir {outputs.dst} --no-follow-symlinks --json
	  outputs:
		stdout:
			- '"files": 0,'
			- '"updated": 0,'
			- '"bytes": 0,'
			- '"failed": 0'

	- desc: a changed size is copied again
	  cmd: |
		set -e
		node dats/fstree.mjs make {outputs.src}
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {outputs.src} --contents --target-dir {outputs.dst} --no-follow-symlinks -q
		node dats/fstree.mjs write {outputs.src}/text/hello.txt 'hello, longer'
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {outputs.src} --contents --target-dir {outputs.dst} --no-follow-symlinks -q
		cat {outputs.dst}/text/hello.txt
		echo
		node dats/fstree.mjs compare {outputs.src} {outputs.dst} --only=data,mtime
	  outputs:
		stdout:
			0: "^hello, longer$"
			1: "^same: 40 entries$"

	- desc: a same-size edit under the old mtime is skipped by default and copied by --checksum
	  cmd: |
		set -e
		node dats/fstree.mjs make {outputs.src}
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {outputs.src} --contents --target-dir {outputs.dst} --no-follow-symlinks -q
		node dats/fstree.mjs write {outputs.src}/text/hello.txt 'HELLO!'
		node dats/fstree.mjs touch {outputs.src}/text/hello.txt 2001-02-10T04:05:06.123Z
		node dats/fstree.mjs touch {outputs.dst}/text/hello.txt 2001-02-10T04:05:06.123Z
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {outputs.src} --contents --target-dir {outputs.dst} --no-follow-symlinks -q
		cat {outputs.dst}/text/hello.txt
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {outputs.src} --contents --target-dir {outputs.dst} --no-follow-symlinks -q --checksum
		cat {outputs.dst}/text/hello.txt
	  outputs:
		stdout:
			0: "^hello$"
			1: "^HELLO!$"

	- desc: an entry only in the target stays without --mirror and goes with it
	  cmd: |
		set -e
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {shared.src} --contents --target-dir {outputs.dst} --no-follow-symlinks -q
		mkdir {outputs.dst}/stray-dir
		node dats/fstree.mjs write {outputs.dst}/stray-dir/stray.txt stray
		node dats/fstree.mjs write {outputs.dst}/text/stray.txt stray
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {shared.src} --contents --target-dir {outputs.dst} --no-follow-symlinks -q
		node dats/fstree.mjs compare {shared.src} {outputs.dst} --only=data || true
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {shared.src} --contents --target-dir {outputs.dst} --no-follow-symlinks -q --mirror
		node dats/fstree.mjs compare {shared.src} {outputs.dst} --only=data
	  outputs:
		stdout:
			- "extra: stray-dir"
			- "extra: stray-dir/stray.txt"
			- "extra: text/stray.txt"
			- "3 difference(s)"
			- "same: 40 entries"

	- desc: --target-file copies one file to exactly that name
	  cmd: |
		set -e
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {shared.src}/big.bin --target-file {outputs.out}/renamed.bin -q
		node dats/fstree.mjs entry {shared.src} big.bin
		node dats/fstree.mjs entry {outputs.out} renamed.bin
	  outputs:
		stdout:
			0: '"size":4194307,"sha256":"12413b94426b2dcf789a55200945c8189aa88cbe16dafe52b12e15d37542e0ca","mtimeNs":"981605106123462200"'
			1: '"size":4194307,"sha256":"12413b94426b2dcf789a55200945c8189aa88cbe16dafe52b12e15d37542e0ca","mtimeNs":"981605106123462200"'

	- desc: --dry-run writes nothing
	  cmd: |
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {shared.src} --contents --target-dir {outputs.dst} --no-follow-symlinks --dry-run -q
		if [ -e {outputs.dst} ]; then echo dst present; else echo dst absent; fi
	  outputs:
		stdout:
			- "dst absent"

	- desc: --move leaves the target equal to an untouched twin of the source and removes the source
	  cmd: |
		set -e
		node dats/fstree.mjs make {outputs.src}
		node dats/fstree.mjs make {outputs.twin}
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {outputs.src} --contents --target-dir {outputs.dst} --no-follow-symlinks --move -q
		node dats/fstree.mjs compare {outputs.twin} {outputs.dst} --only=data,mtime,hardlink --no-dir-mtime
		find {outputs.src} -type f | wc -l | tr -d ' '
	  outputs:
		stdout:
			0: "^same: 40 entries$"
			1: "^0$"

	- desc: the fixture hashes are the same on every host
	  cmd: node dats/fstree.mjs snap {shared.src}
	  outputs:
		stdout:
			- '"path": "big.bin"'
			- '"sha256": "12413b94426b2dcf789a55200945c8189aa88cbe16dafe52b12e15d37542e0ca"'
			- '"path": "names/emoji-🚀.txt"'
			- '"path": "names/日本語.txt"'
			- '"path": "names/unicodé-ñ.txt"'
