#!/usr/bin/env sh
# Build a stripped bb binary outside the repository and print its byte size.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TARGET_OS=${GOOS:-$(go env GOOS)}
TARGET_ARCH=${GOARCH:-$(go env GOARCH)}
MAX_BYTES=${MAX_BYTES:-41943040}

case "$MAX_BYTES" in
  ''|*[!0-9]*)
    printf '%s\n' 'MAX_BYTES must be a non-negative integer' >&2
    exit 2
    ;;
esac

TMP_PARENT=${TMPDIR:-/tmp}
WORK=$(mktemp -d "$TMP_PARENT/bb-awsbrowser-size.XXXXXX")
cleanup() {
  rm -rf "$WORK"
}
trap cleanup EXIT HUP INT TERM

(
  cd "$ROOT"
  CGO_ENABLED=0 GOOS=$TARGET_OS GOARCH=$TARGET_ARCH \
    go build -trimpath -buildvcs=false -ldflags='-s -w' -o "$WORK/bb" ./cmd/bb
)

SIZE=$(wc -c <"$WORK/bb" | tr -d '[:space:]')
printf '%s\n' "$SIZE"

if [ "$SIZE" -gt "$MAX_BYTES" ]; then
  printf 'stripped bb size %s exceeds MAX_BYTES=%s for %s/%s\n' "$SIZE" "$MAX_BYTES" "$TARGET_OS" "$TARGET_ARCH" >&2
  exit 1
fi
