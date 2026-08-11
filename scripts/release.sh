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

# A release archive must describe one committed, annotated tag.  Local
# reproducibility experiments may opt out deliberately, but CI/release use
# must never set this escape hatch.
if [ "${ALLOW_UNTAGGED_BUILD:-}" != 1 ]; then
  HEAD=$(git -C "$ROOT" rev-parse --verify HEAD)
  [ "$COMMIT" = "$HEAD" ] || {
    echo "COMMIT must equal HEAD for a release (COMMIT=$COMMIT, HEAD=$HEAD)" >&2
    exit 2
  }
  [ -z "$(git -C "$ROOT" status --porcelain --untracked-files=all)" ] || {
    echo "release checkout must be clean" >&2
    exit 2
  }
  TAG="v$VERSION"
  [ "$(git -C "$ROOT" cat-file -t "refs/tags/$TAG" 2>/dev/null || true)" = tag ] || {
    echo "release requires annotated tag $TAG" >&2
    exit 2
  }
  git -C "$ROOT" tag --points-at "$HEAD" | grep -Fx "$TAG" >/dev/null || {
    echo "release tag $TAG must point at HEAD" >&2
    exit 2
  }
fi

mkdir -p "$DIST"
rm -f "$DIST"/bb_*.tar.gz "$DIST"/checksums.txt
TMP_PARENT="$ROOT/.tmp"
mkdir -p "$TMP_PARENT"
WORK=$(mktemp -d "$TMP_PARENT/bb-release-work.XXXXXX")
cleanup_release() {
  chmod -R u+w "$WORK" 2>/dev/null || true
  rm -rf "$WORK"
  rmdir "$TMP_PARENT" 2>/dev/null || true
}
trap cleanup_release EXIT

# bb.Version, bb.Commit, and bb.BuildTime are intentionally linker-set
# release metadata.  Keep -buildid empty and paths trimmed for reproducibility.
LDFLAGS="-s -w -buildid= -X github.com/jisung9870/binbox-cli/internal/bb.Version=$VERSION -X github.com/jisung9870/binbox-cli/internal/bb.Commit=$COMMIT -X github.com/jisung9870/binbox-cli/internal/bb.BuildTime=$BUILD_TIME"
export CGO_ENABLED=0 GOFLAGS="${GOFLAGS:-} -trimpath -buildvcs=false"
export SOURCE_DATE_EPOCH
PACKAGER="$WORK/releasearchive"
(cd "$ROOT" && go build -o "$PACKAGER" ./cmd/releasearchive)

for TARGET in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  OS=${TARGET%/*}
  ARCH=${TARGET#*/}
  STAGE="$WORK/stage-$OS-$ARCH"
  mkdir -p "$STAGE"
  GOOS=$OS GOARCH=$ARCH go build -ldflags "$LDFLAGS" -o "$STAGE/bb" "$ROOT/cmd/bb"
  chmod 0755 "$STAGE/bb"
  "$PACKAGER" archive --input "$STAGE/bb" \
    --output "$DIST/bb_${VERSION}_${OS}_${ARCH}.tar.gz" \
    --name bb --epoch "$SOURCE_DATE_EPOCH"
done

"$PACKAGER" checksums --output "$DIST/checksums.txt" "$DIST"/bb_"$VERSION"_*.tar.gz
"$PACKAGER" verify --manifest "$DIST/checksums.txt"
