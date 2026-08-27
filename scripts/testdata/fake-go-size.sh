#!/usr/bin/env sh
set -eu

args=$*
case "$args" in *'-buildid='*) ;; *) exit 91;; esac
case "$args" in *'internal/bb.Version='*) ;; *) exit 92;; esac
case "$args" in *'internal/bb.Commit='*) ;; *) exit 93;; esac
case "$args" in *'internal/bb.BuildTime='*) ;; *) exit 94;; esac

output=
while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ]; then
    shift
    output=$1
    break
  fi
  shift
done
[ -n "$output" ] || exit 95
dd if=/dev/zero of="$output" bs=1 count="${FAKE_GO_SIZE:-1024}" 2>/dev/null
