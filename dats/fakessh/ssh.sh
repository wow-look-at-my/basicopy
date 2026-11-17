#!/bin/sh
# A stand-in ssh for the suites. It reports the version. In FAKE_SSH_VERSION,
# writes what the copy gave it to FAKE_SSH_OUT, asks one question through its
# askpass program, and fails.
case "$1" in
-V)
	echo "OpenSSH_${FAKE_SSH_VERSION}, OpenSSL 3.0.13 30 Jan 2024" >&2
	exit 0
	;;
esac
if (: < /dev/tty) 2> /dev/null; then
	echo "terminal=yes" >> "$FAKE_SSH_OUT"
else
	echo "terminal=no" >> "$FAKE_SSH_OUT"
fi
echo "display=${DISPLAY:-none} require=${SSH_ASKPASS_REQUIRE:-none} args=$*" >> "$FAKE_SSH_OUT"
answer=$("$SSH_ASKPASS" "Enter passphrase for key: ")
echo "answer=$answer" >> "$FAKE_SSH_OUT"
exit 1
