#!/usr/bin/env bash
# Report GitHub Releases outside the retention window. This script is dry-run only.
set -euo pipefail

STABLE_KEEP=5
PRERELEASE_KEEP=1
REPOSITORY=${GITHUB_REPOSITORY:-}

usage() {
  cat <<'EOF'
Usage: release-retention.sh [--repo OWNER/REPO] [--dry-run]

Report published GitHub Releases outside the retention policy. The script never
deletes releases or tags. It keeps the newest 5 stable releases and newest 1
prerelease; drafts are ignored.
EOF
}
die() { printf '%s\n' "release-retention: $*" >&2; exit 1; }

while [ "$#" -gt 0 ]; do
  case "$1" in
    --repo) [ "$#" -ge 2 ] || die '--repo requires OWNER/REPO'; REPOSITORY=$2; shift 2;;
    --dry-run) shift;;
    -h|--help) usage; exit 0;;
    *) die "unknown option: $1";;
  esac
done

command -v gh >/dev/null 2>&1 || die 'gh is required'
if [ -z "$REPOSITORY" ]; then
  REPOSITORY=$(gh repo view --json nameWithOwner --jq .nameWithOwner 2>/dev/null) ||
    die 'could not resolve repository; pass --repo OWNER/REPO'
fi
case "$REPOSITORY" in */*) ;; *) die "invalid repository: $REPOSITORY";; esac

stable_seen=0
prerelease_seen=0
candidates=0

printf '%s\n' "release-retention: dry-run for $REPOSITORY"
printf '%s\n' "release-retention: keep newest $STABLE_KEEP stable and $PRERELEASE_KEEP prerelease; ignore drafts; preserve tags"

# The Releases API returns releases newest first. Pagination is required so old
# releases remain visible after the repository grows beyond 100 releases. Keep
# the response in a variable so an API failure terminates the dry-run instead of
# being mistaken for an empty repository.
releases=$(gh api --paginate "repos/$REPOSITORY/releases?per_page=100" \
  --jq '.[] | select(.draft == false) | [.tag_name, .prerelease] | @tsv')
while IFS=$'\t' read -r tag prerelease; do
  [ -n "$tag" ] || continue
  if [ "$prerelease" = true ]; then
    prerelease_seen=$((prerelease_seen + 1))
    if [ "$prerelease_seen" -le "$PRERELEASE_KEEP" ]; then
      printf 'KEEP\tprerelease\t%s\n' "$tag"
    else
      printf 'DELETE_CANDIDATE\tprerelease\t%s\n' "$tag"
      candidates=$((candidates + 1))
    fi
  else
    stable_seen=$((stable_seen + 1))
    if [ "$stable_seen" -le "$STABLE_KEEP" ]; then
      printf 'KEEP\tstable\t%s\n' "$tag"
    else
      printf 'DELETE_CANDIDATE\tstable\t%s\n' "$tag"
      candidates=$((candidates + 1))
    fi
  fi
done <<< "$releases"

printf '%s\n' "release-retention: $candidates deletion candidate(s); no changes made"
