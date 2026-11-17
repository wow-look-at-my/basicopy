#!/bin/sh
sleep %s
[ -e '%s' ] && exit 1
[ -e '%s' ] && sleep 60
exec '%s' "$@"
