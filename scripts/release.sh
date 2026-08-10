#!/usr/bin/env bash
# Build deterministic release archives.  Invoke from the repository root.
set -euo pipefail

ROOT=$(unset CDPATH; cd -- "$(dirname -- "$0")/.." && pwd)
DIST=${DIST:-"$ROOT/dist"}
VERSION=${VERSION:?set VERSION to the release version (without a leading v)}
COMMIT=${COMMIT:-$(git -C "$ROOT" rev-parse --verify HEAD)}
SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-$(git -C "$ROOT" show -s --format=%ct HEAD)}
BUILD_TIME=${BUILD_TIME:-$(date -u -d "@$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r "$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ)}

case "$VERSION" in ''|*[!0-9A-Za-z._-]*) echo "invalid VERSION: $VERSION" >&2; exit 2;; esac
mkdir -p "$DIST"
rm -f "$DIST"/bb_*.tar.gz "$DIST"/checksums.txt
TMP_PARENT="$ROOT/.tmp"
mkdir -p "$TMP_PARENT"

# bb.Version, bb.Commit, and bb.BuildTime are intentionally linker-set
# release metadata.  Keep -buildid empty and paths trimmed for reproducibility.
LDFLAGS="-s -w -buildid= -X github.com/binbox/bb/internal/bb.Version=$VERSION -X github.com/binbox/bb/internal/bb.Commit=$COMMIT -X github.com/binbox/bb/internal/bb.BuildTime=$BUILD_TIME"
export CGO_ENABLED=0 GOFLAGS="${GOFLAGS:-} -trimpath -buildvcs=false"
export SOURCE_DATE_EPOCH

for TARGET in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  OS=${TARGET%/*}
  ARCH=${TARGET#*/}
  STAGE=$(mktemp -d "$TMP_PARENT/bb-release.XXXXXX")
  trap 'rm -rf "$STAGE"' EXIT
  GOOS=$OS GOARCH=$ARCH go build -ldflags "$LDFLAGS" -o "$STAGE/bb" "$ROOT/cmd/bb"
  chmod 0755 "$STAGE/bb"
  tar --format=ustar --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 --numeric-owner \
    -C "$STAGE" -czf "$DIST/bb_${VERSION}_${OS}_${ARCH}.tar.gz" bb
  rm -rf "$STAGE"
  trap - EXIT
done
rmdir "$TMP_PARENT" 2>/dev/null || true

(cd "$DIST" && sha256sum bb_"$VERSION"_*.tar.gz > checksums.txt)
