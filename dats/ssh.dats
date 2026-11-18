# How the built binary starts ssh. A stand-in ssh on PATH records what it gets and fails,
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

	- desc: a prompt of ssh comes to the copy, the person's own askpass answers it, and ssh gets the answer
	  cmd: |
		mkdir -p {outputs.bin} {outputs.src}
		printf '%s\n' '#!/bin/sh' 'echo "shown: $1" >> "$FAKE_SSH_OUT"' 'echo hunter2' > {outputs.bin}/askpass
		cp dats/fakessh/ssh.sh {outputs.bin}/ssh
		chmod +x {outputs.bin}/ssh {outputs.bin}/askpass
		echo hello > {outputs.src}/a.txt
		export FAKE_SSH_OUT={outputs.bin}/out FAKE_SSH_VERSION=9.6p1
		unset DISPLAY WAYLAND_DISPLAY
		SSH_ASKPASS={outputs.bin}/askpass SSH_ASKPASS_REQUIRE=force PATH="{outputs.bin}:$PATH" \
			"$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {outputs.src} -q --target-dir pc:/srv
		echo "exit=$?"
		grep -v '^terminal=' {outputs.bin}/out
	  outputs:
		stdout:
			0: "^exit=1$"
			1: "^display=none require=force args=-s pc sftp$"
			2: "^shown: Enter passphrase for key: $"
			3: "^answer=hunter2$"

	# script gives the copy a terminal. The typed line comes after the prompt has turned the echo off.
	- desc: on a terminal the copy shows the prompt of ssh there, reads the answer with no echo, and ssh keeps its terminal
	  cmd: |
		mkdir -p {outputs.bin} {outputs.src}
		cp dats/fakessh/ssh.sh {outputs.bin}/ssh
		chmod +x {outputs.bin}/ssh
		echo hello > {outputs.src}/a.txt
		export FAKE_SSH_OUT={outputs.bin}/out FAKE_SSH_VERSION=9.6p1
		export PATH="{outputs.bin}:$PATH"
		unset SSH_ASKPASS SSH_ASKPASS_REQUIRE DISPLAY WAYLAND_DISPLAY
		if script --version > /dev/null 2>&1; then
			(sleep 2; echo hunter2; sleep 2) | script -qc "$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy {outputs.src} -q --target-dir pc:/srv" /dev/null > {outputs.bin}/screen 2>&1
		else
			(sleep 2; echo hunter2; sleep 2) | script -q /dev/null "$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {outputs.src} -q --target-dir pc:/srv > {outputs.bin}/screen 2>&1
		fi
		cat {outputs.bin}/out
		echo "prompts=$(grep -c 'Enter passphrase for key: ' {outputs.bin}/screen)"
		echo "echoed=$(grep -c hunter2 {outputs.bin}/screen)"
	  outputs:
		stdout:
			0: "^terminal=yes$"
			1: "^display=none require=force args=-s pc sftp$"
			2: "^answer=hunter2$"
			3: "^prompts=1$"
			4: "^echoed=0$"

	# An ssh from before SSH_ASKPASS_REQUIRE asks on its terminal when it has one. The copy starts it with none.
	- desc: an old ssh starts with no terminal and with a display, so its prompt comes to the copy too
	  cmd: |
		mkdir -p {outputs.bin} {outputs.src}
		cp dats/fakessh/ssh.sh {outputs.bin}/ssh
		chmod +x {outputs.bin}/ssh
		echo hello > {outputs.src}/a.txt
		export FAKE_SSH_OUT={outputs.bin}/out FAKE_SSH_VERSION=8.0p1
		export PATH="{outputs.bin}:$PATH"
		unset SSH_ASKPASS SSH_ASKPASS_REQUIRE DISPLAY WAYLAND_DISPLAY
		if script --version > /dev/null 2>&1; then
			(sleep 2; echo hunter2; sleep 2) | script -qc "$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy {outputs.src} -q --target-dir pc:/srv" /dev/null > {outputs.bin}/screen 2>&1
		else
			(sleep 2; echo hunter2; sleep 2) | script -q /dev/null "$GO_TOOLCHAIN_DATS_BUILD_DIR/basicopy" {outputs.src} -q --target-dir pc:/srv > {outputs.bin}/screen 2>&1
		fi
		cat {outputs.bin}/out
		echo "prompts=$(grep -c 'Enter passphrase for key: ' {outputs.bin}/screen)"
		echo "echoed=$(grep -c hunter2 {outputs.bin}/screen)"
	  outputs:
		stdout:
			0: "^terminal=no$"
			1: "^display=basicopy:0 require=force args=-x -s pc sftp$"
			2: "^answer=hunter2$"
			3: "^prompts=1$"
			4: "^echoed=0$"
