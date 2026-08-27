#!/usr/bin/env sh
# The mandatory AWS browser suite must never hide a missing acceptance gate.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
if grep -R -n -E 't\.Skip(Now|f)?[[:space:]]*\(' "$ROOT/internal/bb/awsbrowser" --include='*_test.go'; then
  printf '%s\n' 'AWS browser tests contain a skip; move optional smoke tests outside the mandatory suite' >&2
  exit 1
fi
printf '%s\n' 'AWS browser skip-free gate passed'
