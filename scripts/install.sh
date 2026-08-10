#!/usr/bin/env bash
# Verified, user-local bb installer.  It intentionally never invokes sudo.
set -euo pipefail

PROGRAM=bb
REPOSITORY=${BB_REPOSITORY:-jisung9870/binbox-cli}
VERSION=${BB_VERSION:-}
INSTALL_DIR=${XDG_BIN_HOME:-"${HOME:?HOME must be set}/.local/bin"}
DRY_RUN=false
FORCE=false
MIGRATE=false

usage() {
  cat <<'EOF'
Usage: install.sh [--version VERSION] [--install-dir DIR] [--dry-run] [--force] [--migrate]

Install a verified bb release into a user-owned directory.  --force permits
replacement of a regular file or non-checkout symlink; --migrate permits
replacement of a symlink into a Git checkout.  No option uses sudo.
EOF
}
die() { printf '%s\n' "install: $*" >&2; exit 1; }
note() { printf '%s\n' "install: $*" >&2; }

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) [ "$#" -ge 2 ] || die '--version requires a value'; VERSION=$2; shift 2;;
    --install-dir) [ "$#" -ge 2 ] || die '--install-dir requires a value'; INSTALL_DIR=$2; shift 2;;
    --dry-run) DRY_RUN=true; shift;;
    --force) FORCE=true; shift;;
    --migrate) MIGRATE=true; shift;;
    -h|--help) usage; exit 0;;
    *) die "unknown option: $1";;
  esac
done

case "$INSTALL_DIR" in ''|/) die 'refusing an empty or root install directory';; esac
case "$VERSION" in v*) VERSION=${VERSION#v};; esac

host_os() {
  case "$(uname -s)" in Linux) printf linux;; Darwin) printf darwin;; *) die "unsupported operating system: $(uname -s)";; esac
}
host_arch() {
  case "$(uname -m)" in x86_64|amd64) printf amd64;; aarch64|arm64) printf arm64;; *) die "unsupported architecture: $(uname -m)";; esac
}
OS=$(host_os); ARCH=$(host_arch)
# Overrides are test/automation inputs, never a way to install a foreign binary.
[ -z "${BB_INSTALL_OS:-}" ] || [ "$BB_INSTALL_OS" = "$OS" ] || die "foreign target requested: $BB_INSTALL_OS/$ARCH (host is $OS/$ARCH)"
[ -z "${BB_INSTALL_ARCH:-}" ] || [ "$BB_INSTALL_ARCH" = "$ARCH" ] || die "foreign target requested: $OS/$BB_INSTALL_ARCH (host is $OS/$ARCH)"

fetch() {
  url=$1 out=$2
  if command -v curl >/dev/null 2>&1; then curl -fsSL --retry 3 --connect-timeout 15 "$url" -o "$out"
  elif command -v wget >/dev/null 2>&1; then wget -qO "$out" "$url"
  else die 'curl or wget is required'; fi
}
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then openssl dgst -sha256 "$1" | awk '{print $NF}'
  else die 'sha256sum, shasum, or openssl is required'; fi
}
in_git_checkout() {
  command -v git >/dev/null 2>&1 && git -C "$(dirname "$1")" rev-parse --is-inside-work-tree >/dev/null 2>&1
}
link_target() {
  # readlink -f is not portable to macOS; this covers the absolute checkout links
  # created by the legacy installer and relative links in the destination directory.
  target=$(readlink "$1") || return 1
  case "$target" in /*) printf '%s\n' "$target";; *) printf '%s/%s\n' "$(unset CDPATH; cd -- "$(dirname "$1")" && pwd -P)" "$target";; esac
}

if [ -z "$VERSION" ]; then
  latest_url="https://api.github.com/repos/$REPOSITORY/releases/latest"
  if [ "$DRY_RUN" = true ]; then
    note "dry-run: would resolve latest version from $latest_url"
    VERSION='latest'
  else
    latest=$(mktemp "${TMPDIR:-/tmp}/bb-latest.XXXXXX")
    trap 'rm -f "$latest"' EXIT
    fetch "$latest_url" "$latest" || die 'could not resolve the latest release; pass --version for an explicit release'
    VERSION=$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"v\{0,1\}\([^"]*\)".*/\1/p' "$latest" | head -n 1)
    [ -n "$VERSION" ] || die 'latest release response did not contain tag_name'
    rm -f "$latest"; trap - EXIT
  fi
fi
case "$VERSION" in ''|*[!0-9A-Za-z._-]*) die "invalid version: $VERSION";; esac

archive="bb_${VERSION}_${OS}_${ARCH}.tar.gz"
base=${BB_DOWNLOAD_BASE:-"https://github.com/$REPOSITORY/releases/download/v$VERSION"}
archive_url="$base/$archive"
checksums_url="$base/checksums.txt"
destination="$INSTALL_DIR/$PROGRAM"

note "target: $OS/$ARCH, version: $VERSION"
if [ "$DRY_RUN" = true ]; then
  note "dry-run: would download $archive_url and verify $checksums_url"
  note "dry-run: would atomically install $destination"
  exit 0
fi

if [ -x "$destination" ] && [ ! -L "$destination" ]; then
	installed_version=$("$destination" version 2>/dev/null || true)
	if [ "$installed_version" = "$VERSION" ]; then
		note "$destination already provides version $VERSION"
		exit 0
	fi
fi
if [ -e "$destination" ] || [ -L "$destination" ]; then
  if [ -L "$destination" ]; then
    resolved=$(link_target "$destination") || die "cannot read existing symlink: $destination"
    if in_git_checkout "$resolved"; then
      [ "$MIGRATE" = true ] || die "existing bb is a checkout symlink ($destination -> $resolved); rerun with --migrate (the checkout will not be deleted)"
    elif [ "$FORCE" != true ]; then
      die "refusing to replace foreign symlink: $destination (rerun with --force)"
    fi
  elif [ "$FORCE" != true ]; then
    die "refusing to replace existing file: $destination (rerun with --force)"
  fi
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/bb-install.XXXXXX")
cleanup() { rm -rf "$work"; }
trap cleanup EXIT HUP INT TERM
fetch "$archive_url" "$work/$archive" || die "download failed: $archive_url"
fetch "$checksums_url" "$work/checksums.txt" || die "download failed: $checksums_url"
expected=$(awk -v f="$archive" '$2 == f || $2 == "*" f {print $1; exit}' "$work/checksums.txt")
[ -n "$expected" ] || die "checksum entry missing for $archive"
actual=$(sha256_of "$work/$archive")
[ "$actual" = "$expected" ] || die "checksum mismatch for $archive"

mkdir "$work/extract"
tar -xzf "$work/$archive" -C "$work/extract" || die "could not unpack $archive"
if [ ! -f "$work/extract/$PROGRAM" ] || [ -L "$work/extract/$PROGRAM" ]; then
  die 'archive does not contain a regular bb binary'
fi
chmod 0755 "$work/extract/$PROGRAM"
staged_version=$("$work/extract/$PROGRAM" version 2>/dev/null || true)
[ "$staged_version" = "$VERSION" ] || die "archive binary reports version '$staged_version', expected '$VERSION'"
if command -v file >/dev/null 2>&1; then
  kind=$(file -b "$work/extract/$PROGRAM")
  case "$OS/$ARCH:$kind" in
    linux/amd64:*ELF*x86-64*|linux/arm64:*ELF*aarch64*|darwin/amd64:*Mach-O*x86_64*|darwin/arm64:*Mach-O*arm64*) ;;
    *) die "archive binary is not for this host ($OS/$ARCH): $kind";;
  esac
fi

mkdir -p "$INSTALL_DIR" || die "cannot create install directory: $INSTALL_DIR"
[ -w "$INSTALL_DIR" ] || die "install directory is not writable: $INSTALL_DIR"
temp=$(mktemp "$INSTALL_DIR/.bb.new.XXXXXX") || die 'cannot create atomic replacement file'
if ! cp "$work/extract/$PROGRAM" "$temp" || ! chmod 0755 "$temp"; then rm -f "$temp"; die 'could not prepare replacement binary'; fi

backup=''
if [ -e "$destination" ] || [ -L "$destination" ]; then
	backup=$(mktemp "$INSTALL_DIR/.bb.backup.XXXXXX") || { rm -f "$temp"; die 'cannot reserve backup path'; }
	rm -f "$backup"
  mv "$destination" "$backup" || { rm -f "$temp"; die "could not back up existing $destination"; }
fi
if ! mv -f "$temp" "$destination"; then
  [ -z "$backup" ] || mv "$backup" "$destination" || true
  die "atomic replacement failed; previous binary restored when possible"
fi
installed_version=$("$destination" version 2>/dev/null || true)
if [ "$installed_version" != "$VERSION" ]; then
	rm -f "$destination"
	[ -z "$backup" ] || mv "$backup" "$destination" || true
	die "installed binary validation failed; previous binary restored when possible"
fi
note "installed $destination"
[ -z "$backup" ] || note "previous installation backed up to $backup"
