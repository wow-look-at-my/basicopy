#!/bin/sh
# Makes the real ssh act as one from before SSH_ASKPASS_REQUIRE: it reports an
# old version, and it does not see that variable.
printf '%%s\n' "$*" >> '%s'
case "$1" in
-V)
	echo 'OpenSSH_8.0p1, OpenSSL 1.1.1k  FIPS 25 Mar 2021' >&2
	exit 0
	;;
esac
unset SSH_ASKPASS_REQUIRE
exec '%s' -F '%s' "$@"
