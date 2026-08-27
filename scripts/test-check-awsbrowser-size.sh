#!/usr/bin/env sh
# Check the size gate's portable contract without retaining a build artifact.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/bb-awsbrowser-size-test.XXXXXX")
cleanup() {
  rm -rf "$TEST_ROOT"
}
trap cleanup EXIT HUP INT TERM

size=$(GOOS=linux GOARCH=amd64 MAX_BYTES=41943040 "$ROOT/scripts/check-awsbrowser-size.sh")
case "$size" in
  ''|*[!0-9]*)
    printf 'size gate emitted non-numeric evidence: %s\n' "$size" >&2
    exit 1
    ;;
esac

if GOOS=linux GOARCH=amd64 MAX_BYTES=1 "$ROOT/scripts/check-awsbrowser-size.sh" >"$TEST_ROOT/size.out" 2>"$TEST_ROOT/size.err"; then
  printf '%s\n' 'expected size gate failure below the binary size' >&2
  exit 1
fi

printf '%s\n' 'AWS browser size gate tests passed'
