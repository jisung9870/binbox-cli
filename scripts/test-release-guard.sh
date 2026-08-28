#!/usr/bin/env bash
# Exercise the release identity checks in isolated disposable clones.
set -euo pipefail

ROOT=$(unset CDPATH; cd -- "$(dirname -- "$0")/.." && pwd)
TEST_PARENT="$ROOT/.tmp"
mkdir -p "$TEST_PARENT"
TEST_ROOT=$(mktemp -d "$TEST_PARENT/bb-release-guard-test.XXXXXX")
cleanup() {
  chmod -R u+w "$TEST_ROOT" 2>/dev/null || true
  rm -rf "$TEST_ROOT"
  rmdir "$TEST_PARENT" 2>/dev/null || true
}
trap cleanup EXIT

make_repo() {
  repo=$1
  git clone -q --no-hardlinks "$ROOT" "$repo"
  mkdir -p "$repo/cmd/releasearchive" "$repo/internal/releasearchive"
  cp "$ROOT/scripts/release.sh" "$repo/scripts/release.sh"
  cp "$ROOT/cmd/releasearchive/main.go" "$repo/cmd/releasearchive/main.go"
  cp "$ROOT/internal/releasearchive/archive.go" "$repo/internal/releasearchive/archive.go"
	cp "$ROOT/THIRD_PARTY_NOTICES.md" "$repo/THIRD_PARTY_NOTICES.md"
  git -C "$repo" config user.name 'bb release guard test'
  git -C "$repo" config user.email 'bb-release-guard@example.invalid'
  git -C "$repo" add scripts/release.sh cmd/releasearchive internal/releasearchive THIRD_PARTY_NOTICES.md
  # Keep every fixture on a fresh commit even when the cloned script already
  # matches HEAD (the normal case when this test runs from a release tag).
  git -C "$repo" commit --allow-empty -qm 'test release guard'
}
expect_failure() {
  pattern=$1
  shift
  if "$@" >"$TEST_ROOT/failure.out" 2>&1; then
    printf '%s\n' 'expected release guard failure' >&2
    exit 1
  fi
  grep -Eq "$pattern" "$TEST_ROOT/failure.out"
}

missing="$TEST_ROOT/missing"
make_repo "$missing"
missing_head=$(git -C "$missing" rev-parse HEAD)
expect_failure 'requires annotated tag v9.9.9' env VERSION=9.9.9 COMMIT="$missing_head" "$missing/scripts/release.sh"

lightweight="$TEST_ROOT/lightweight"
make_repo "$lightweight"
lightweight_head=$(git -C "$lightweight" rev-parse HEAD)
git -C "$lightweight" tag v9.9.9
expect_failure 'requires annotated tag v9.9.9' env VERSION=9.9.9 COMMIT="$lightweight_head" "$lightweight/scripts/release.sh"

wrong="$TEST_ROOT/wrong"
make_repo "$wrong"
git -C "$wrong" tag -a v9.9.9 -m 'test tag'
touch "$wrong/after-tag"
git -C "$wrong" add after-tag
git -C "$wrong" commit -qm 'advance after tag'
wrong_head=$(git -C "$wrong" rev-parse HEAD)
expect_failure 'must point at HEAD' env VERSION=9.9.9 COMMIT="$wrong_head" "$wrong/scripts/release.sh"

dirty="$TEST_ROOT/dirty"
make_repo "$dirty"
git -C "$dirty" tag -a v9.9.9 -m 'test tag'
dirty_head=$(git -C "$dirty" rev-parse HEAD)
touch "$dirty/untracked"
expect_failure 'release checkout must be clean' env VERSION=9.9.9 COMMIT="$dirty_head" "$dirty/scripts/release.sh"
expect_failure 'COMMIT must equal HEAD' env VERSION=9.9.9 COMMIT=deadbeef "$dirty/scripts/release.sh"

success="$TEST_ROOT/success"
make_repo "$success"
git -C "$success" tag -a v9.9.9 -m 'test tag'
success_head=$(git -C "$success" rev-parse HEAD)
mkdir -p "$success/.tmp/cache" "$success/.tmp/modcache" "$success/.tmp/tmp"
(
  cd "$success"
  VERSION=9.9.9 COMMIT="$success_head" DIST="$success/dist" \
    TMPDIR="$success/.tmp/tmp" GOCACHE="$success/.tmp/cache" GOMODCACHE="$success/.tmp/modcache" \
    scripts/release.sh
)
test -f "$success/dist/checksums.txt"
cp "$success/dist/checksums.txt" "$TEST_ROOT/first-checksums.txt"
(
  cd "$success"
  VERSION=9.9.9 COMMIT="$success_head" DIST="$success/dist" \
    TMPDIR="$success/.tmp/tmp" GOCACHE="$success/.tmp/cache" GOMODCACHE="$success/.tmp/modcache" \
    scripts/release.sh
)
cmp "$TEST_ROOT/first-checksums.txt" "$success/dist/checksums.txt"

# The escape hatch is intentionally explicit and is only for local/test builds.
escape="$TEST_ROOT/escape"
make_repo "$escape"
escape_head=$(git -C "$escape" rev-parse HEAD)
mkdir -p "$escape/.tmp/cache" "$escape/.tmp/modcache" "$escape/.tmp/tmp"
(
  cd "$escape"
  ALLOW_UNTAGGED_BUILD=1 VERSION=9.9.9 COMMIT="$escape_head" DIST="$escape/dist" \
    TMPDIR="$escape/.tmp/tmp" GOCACHE="$escape/.tmp/cache" GOMODCACHE="$escape/.tmp/modcache" \
    scripts/release.sh
)
test -f "$escape/dist/checksums.txt"
printf '%s\n' 'release guard tests passed'
