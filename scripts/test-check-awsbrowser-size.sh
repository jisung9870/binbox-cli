#!/usr/bin/env sh
# Exercise target selection, release flags, reporting, and the exclusive limit
# with a deterministic fake compiler; the release preflight runs the real build.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/bb-awsbrowser-size-test.XXXXXX")
cleanup() {
  rm -rf "$TEST_ROOT"
}
trap cleanup EXIT HUP INT TERM
mkdir -p "$TEST_ROOT/bin"
cp "$ROOT/scripts/testdata/fake-go-size.sh" "$TEST_ROOT/bin/go"
chmod +x "$TEST_ROOT/bin/go"

result=$(PATH="$TEST_ROOT/bin:$PATH" FAKE_GO_SIZE=1024 MAX_BYTES=1025 "$ROOT/scripts/check-awsbrowser-size.sh")
expected='linux/amd64 1024
linux/arm64 1024
darwin/amd64 1024
darwin/arm64 1024'
if [ "$result" != "$expected" ]; then
  printf 'unexpected four-target evidence:\n%s\n' "$result" >&2
  exit 1
fi

if PATH="$TEST_ROOT/bin:$PATH" FAKE_GO_SIZE=1024 TARGETS=linux/amd64 MAX_BYTES=1024 \
  "$ROOT/scripts/check-awsbrowser-size.sh" >"$TEST_ROOT/size.out" 2>"$TEST_ROOT/size.err"; then
  printf '%s\n' 'expected equality with the exclusive limit to fail' >&2
  exit 1
fi
grep -F 'must be under MAX_BYTES=1024 for linux/amd64' "$TEST_ROOT/size.err" >/dev/null

if PATH="$TEST_ROOT/bin:$PATH" TARGETS=windows/amd64 "$ROOT/scripts/check-awsbrowser-size.sh" \
  >"$TEST_ROOT/target.out" 2>"$TEST_ROOT/target.err"; then
  printf '%s\n' 'expected an unsupported release target to fail' >&2
  exit 1
fi
grep -F 'unsupported release target: windows/amd64' "$TEST_ROOT/target.err" >/dev/null

printf '%s\n' 'AWS browser size gate tests passed'
