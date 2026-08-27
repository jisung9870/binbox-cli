#!/usr/bin/env sh
# Build the four release targets with release-equivalent linker flags and
# enforce an exclusive 40 MiB limit without retaining artifacts.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TARGETS=${TARGETS:-"linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"}
MAX_BYTES=${MAX_BYTES:-41943040}
VERSION=${VERSION:-size-gate}
COMMIT=${COMMIT:-0000000000000000000000000000000000000000}
BUILD_TIME=${BUILD_TIME:-1970-01-01T00:00:00Z}

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

LDFLAGS="-s -w -buildid= -X github.com/jisung9870/binbox-cli/internal/bb.Version=$VERSION -X github.com/jisung9870/binbox-cli/internal/bb.Commit=$COMMIT -X github.com/jisung9870/binbox-cli/internal/bb.BuildTime=$BUILD_TIME"

for target in $TARGETS; do
  case "$target" in
    linux/amd64|linux/arm64|darwin/amd64|darwin/arm64) ;;
    *)
      printf 'unsupported release target: %s\n' "$target" >&2
      exit 2
      ;;
  esac
  target_os=${target%/*}
  target_arch=${target#*/}
  output="$WORK/bb-$target_os-$target_arch"
  (
    cd "$ROOT"
    CGO_ENABLED=0 GOOS=$target_os GOARCH=$target_arch \
      go build -trimpath -buildvcs=false -ldflags "$LDFLAGS" -o "$output" ./cmd/bb
  )
  size=$(wc -c <"$output" | tr -d '[:space:]')
  printf '%s %s\n' "$target" "$size"
  if [ "$size" -ge "$MAX_BYTES" ]; then
    printf 'release bb size %s must be under MAX_BYTES=%s for %s\n' "$size" "$MAX_BYTES" "$target" >&2
    exit 1
  fi
done
