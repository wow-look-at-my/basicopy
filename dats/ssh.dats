# The ssh command line of the built binary. A stand-in ssh on PATH records its arguments and fails,
# so no host, network or credential is needed.

tests:
	- desc: every -o option reaches ssh, after the port and user written in the path
	  cmd: |
		mkdir -p {outputs.bin} {outputs.src}
		printf '%s\n' '#!/bin/sh' 'printf "%s\n" "$@" > "$FAKE_SSH_ARGS"' 'exit 1' > {outputs.bin}/ssh
		chmod +x {outputs.bin}/ssh
		echo hello > {outputs.src}/a.txt
		export FAKE_SSH_ARGS={outputs.bin}/args
		PATH="{outputs.bin}:$PATH" "$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {outputs.src} -q \
			--target-dir ssh://me@pc:2222/srv -o ControlPath=/run/ctl.sock --ssh-option IdentityFile=/keys/deploy
		echo "exit=$?"
		tr '\n' ' ' < {outputs.bin}/args
		echo
	  outputs:
		stdout:
			0: "^exit=1$"
			1: "^-p 2222 -l me -o ControlPath=/run/ctl.sock -o IdentityFile=/keys/deploy -s pc sftp $"

	- desc: a run without -o adds no option of its own to the first connection
	  cmd: |
		mkdir -p {outputs.bin} {outputs.src}
		printf '%s\n' '#!/bin/sh' 'printf "%s\n" "$@" > "$FAKE_SSH_ARGS"' 'exit 1' > {outputs.bin}/ssh
		chmod +x {outputs.bin}/ssh
		echo hello > {outputs.src}/a.txt
		export FAKE_SSH_ARGS={outputs.bin}/args
		PATH="{outputs.bin}:$PATH" "$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {outputs.src} -q --target-dir pc:/srv
		echo "exit=$?"
		tr '\n' ' ' < {outputs.bin}/args
		echo
	  outputs:
		stdout:
			0: "^exit=1$"
			1: "^-s pc sftp $"
