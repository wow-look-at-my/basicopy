# Metadata fidelity of the built binary on the host it runs on. CI runs this file on Linux, macOS and Windows.
# An answer that differs by host prints the host name first, and the pattern names what each host must answer.
setup: node dats/fstree.mjs make {shared.src}

tests:
	- desc: file and directory mtimes keep every 100ns step
	  cmd: |
		set -e
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {shared.src} --contents --target-dir {outputs.dst} --no-follow-symlinks -q
		node dats/fstree.mjs compare {shared.src} {outputs.dst} --only=mtime
	  outputs:
		stdout:
			- "same: 40 entries"

	- desc: permission bits are kept, and so is the read-only attribute on Windows
	  cmd: |
		set -e
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {shared.src} --contents --target-dir {outputs.dst} --no-follow-symlinks -q
		node dats/fstree.mjs compare {shared.src} {outputs.dst} --only=mode
		node dats/fstree.mjs entry {outputs.dst} modes/readonly.txt
	  outputs:
		stdout:
			0: "^same: 40 entries$"
			1: '"mode":"(0444|ro)"'

	- desc: symlinks keep their target text without dereference
	  cmd: |
		set -e
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {shared.src} --contents --target-dir {outputs.dst} --no-follow-symlinks -q
		node dats/fstree.mjs entry {outputs.dst} link-file
		node dats/fstree.mjs entry {outputs.dst} link-dir
		node dats/fstree.mjs entry {outputs.dst} link-dangling
	  outputs:
		stdout:
			0: '^\{"path":"link-file","type":"symlink","link":"text/hello.txt"\}$'
			1: '^\{"path":"link-dir","type":"symlink","link":"deep"\}$'
			2: '^\{"path":"link-dangling","type":"symlink","link":"no-such-target"\}$'

	- desc: hardlinks are kept on every host
	  cmd: |
		set -e
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {shared.src} --contents --target-dir {outputs.dst} --no-follow-symlinks -q
		node dats/fstree.mjs hardlinks {shared.src} hard/a.bin hard/b.bin
		node dats/fstree.mjs hardlinks {outputs.dst} hard/a.bin hard/b.bin
	  outputs:
		stdout:
			0: "^(linux|darwin|windows) hardlinks: preserved$"
			1: "^(linux|darwin|windows) hardlinks: preserved$"

	- desc: --no-hardlinks copies each name of a hardlink on its own
	  cmd: |
		set -e
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {shared.src} --contents --target-dir {outputs.dst} --no-follow-symlinks --no-hardlinks -q
		node dats/fstree.mjs hardlinks {outputs.dst} hard/a.bin hard/b.bin
		node dats/fstree.mjs compare {shared.src} {outputs.dst} --only=data
	  outputs:
		stdout:
			0: "hardlinks: duplicated$"
			1: "^same: 40 entries$"

	- desc: a sparse file keeps its holes where the host stores them
	  cmd: |
		set -e
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {shared.src}/sparse.bin --target-file {outputs.dst}/sparse.bin -q
		node dats/fstree.mjs sparse {shared.src}/sparse.bin {outputs.dst}/sparse.bin
	  outputs:
		stdout:
			0: "^linux sparse: holes kept$|^darwin sparse: (holes kept|unmeasured \\(source holds no holes\\))$|^windows sparse: unmeasured$"

	- desc: extended attributes are kept on Linux and macOS, and an NTFS stream is not carried
	  cmd: |
		set -e
		node dats/fstree.mjs make {outputs.src}
		node dats/fstree.mjs xattr set {outputs.src}/text/hello.txt basicopy-file filevalue
		node dats/fstree.mjs xattr set {outputs.src}/deep basicopy-dir dirvalue
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {outputs.src} --contents --target-dir {outputs.dst} --no-follow-symlinks --verbose 1>&2
		node dats/fstree.mjs xattr get {outputs.dst}/text/hello.txt basicopy-file
		node dats/fstree.mjs xattr get {outputs.dst}/deep basicopy-dir
	  outputs:
		stdout:
			0: "^(linux|darwin) basicopy-file=filevalue$|^windows basicopy-file absent$"
			1: "^(linux|darwin) basicopy-dir=dirvalue$|^windows basicopy-dir absent$"

	- desc: --no-preserve keeps the data and drops the source mtimes
	  cmd: |
		set -e
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {shared.src} --contents --target-dir {outputs.dst} --no-follow-symlinks --no-preserve -q
		node dats/fstree.mjs compare {shared.src} {outputs.dst} --only=data
		node dats/fstree.mjs compare {shared.src} {outputs.dst} --only=mtime | tail -n 1
	  outputs:
		stdout:
			0: "^same: 40 entries$"
			1: "^[0-9]+ difference\\(s\\)$"

	- desc: a mode change on an unchanged file reaches the target on the next run
	  cmd: |
		set -e
		node dats/fstree.mjs make {outputs.src}
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {outputs.src} --contents --target-dir {outputs.dst} --no-follow-symlinks -q
		node dats/fstree.mjs chmod {outputs.src}/modes/private.txt 444
		node dats/fstree.mjs chmod {outputs.src}/modes/readonly.txt 644
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {outputs.src} --contents --target-dir {outputs.dst} --no-follow-symlinks -q
		node dats/fstree.mjs compare {outputs.src} {outputs.dst} --only=data,mode,mtime
	  outputs:
		stdout:
			- "same: 40 entries"

	- desc: a new mtime on unchanged content reaches the target under --checksum
	  cmd: |
		set -e
		node dats/fstree.mjs make {outputs.src}
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {outputs.src} --contents --target-dir {outputs.dst} --no-follow-symlinks -q
		node dats/fstree.mjs touch {outputs.src}/big.bin 2010-06-07T08:09:10.250Z
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {outputs.src} --contents --target-dir {outputs.dst} --no-follow-symlinks -q --checksum
		node dats/fstree.mjs compare {outputs.src} {outputs.dst} --only=data,mtime --no-dir-mtime
	  outputs:
		stdout:
			- "same: 40 entries"

	- desc: a changed read-only file replaces its read-only copy
	  cmd: |
		set -e
		node dats/fstree.mjs make {outputs.src}
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {outputs.src} --contents --target-dir {outputs.dst} --no-follow-symlinks -q
		node dats/fstree.mjs chmod {outputs.src}/modes/readonly.txt 644
		node dats/fstree.mjs write {outputs.src}/modes/readonly.txt 'new read-only content'
		node dats/fstree.mjs chmod {outputs.src}/modes/readonly.txt 444
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {outputs.src} --contents --target-dir {outputs.dst} --no-follow-symlinks -q
		cat {outputs.dst}/modes/readonly.txt
		echo
		node dats/fstree.mjs compare {outputs.src} {outputs.dst} --only=data,mode,mtime --no-dir-mtime
	  outputs:
		stdout:
			0: "^new read-only content$"
			1: "^same: 40 entries$"

	- desc: a directory mode that denies others is kept
	  cmd: |
		set -e
		"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {shared.src} --contents --target-dir {outputs.dst} --no-follow-symlinks -q
		node dats/fstree.mjs entry {outputs.dst} modes/locked
	  outputs:
		stdout:
			0: '"type":"dir","mtimeNs":"[0-9]+","mode":"(0700|rw)"\}$'
