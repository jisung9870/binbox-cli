#!/usr/bin/env bash
set -euo pipefail

ROOT=$(unset CDPATH; cd -- "$(dirname -- "$0")/.." && pwd)
TEST_PARENT="$ROOT/.tmp"
mkdir -p "$TEST_PARENT"
TEST_ROOT=$(mktemp -d "$TEST_PARENT/release-retention-test.XXXXXX")
cleanup() {
  rm -rf "$TEST_ROOT"
  rmdir "$TEST_PARENT" 2>/dev/null || true
}
trap cleanup EXIT

mkdir -p "$TEST_ROOT/bin"
cat >"$TEST_ROOT/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ "${GH_FAIL:-}" != 1 ] || exit 1
case "${1:-}" in
  api)
    cat <<'RELEASES'
v0.11.0	false
v0.11.0-rc.2	true
v0.11.0-rc.1	true
v0.10.0	false
v0.9.0	false
v0.8.1	false
v0.8.0	false
v0.7.1	false
RELEASES
    ;;
  *) exit 2;;
esac
EOF
chmod 0755 "$TEST_ROOT/bin/gh"

output=$(PATH="$TEST_ROOT/bin:$PATH" GITHUB_REPOSITORY=owner/repo \
  "$ROOT/scripts/release-retention.sh" --dry-run)

printf '%s\n' "$output" | grep -F $'KEEP\tprerelease\tv0.11.0-rc.2' >/dev/null
printf '%s\n' "$output" | grep -F $'DELETE_CANDIDATE\tprerelease\tv0.11.0-rc.1' >/dev/null
printf '%s\n' "$output" | grep -F $'KEEP\tstable\tv0.8.0' >/dev/null
printf '%s\n' "$output" | grep -F $'DELETE_CANDIDATE\tstable\tv0.7.1' >/dev/null
printf '%s\n' "$output" | grep -F '2 deletion candidate(s); no changes made' >/dev/null

if printf '%s\n' "$output" | grep -F 'gh release delete' >/dev/null; then
  echo 'retention dry-run unexpectedly attempted deletion' >&2
  exit 1
fi

if PATH="$TEST_ROOT/bin:$PATH" GH_FAIL=1 GITHUB_REPOSITORY=owner/repo \
  "$ROOT/scripts/release-retention.sh" --dry-run >/dev/null 2>&1; then
  echo 'retention dry-run ignored a GitHub API failure' >&2
  exit 1
fi
