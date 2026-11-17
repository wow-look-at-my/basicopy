#!/bin/sh
sleep %s
[ -e '%s' ] && exit 1
exec '%s' "$@"
