#!/usr/bin/env bash
# Isolated installer contract tests.  Uses a fake downloader and never reads HOME.
set -euo pipefail

ROOT=$(unset CDPATH; cd -- "$(dirname -- "$0")/.." && pwd)
TEST_PARENT="$ROOT/.tmp"
mkdir -p "$TEST_PARENT"
TEST_ROOT=$(mktemp -d "$TEST_PARENT/bb-installer-test.XXXXXX")
cleanup() { rm -rf "$TEST_ROOT"; rmdir "$TEST_PARENT" 2>/dev/null || true; }
trap cleanup EXIT
export HOME="$TEST_ROOT/home"
export XDG_BIN_HOME="$HOME/bin"
export TMPDIR="$TEST_ROOT/tmp"
mkdir -p "$HOME" "$TMPDIR" "$TEST_ROOT/bin" "$TEST_ROOT/assets"

version=1.2.3; os=$(uname -s | tr '[:upper:]' '[:lower:]'); [ "$os" = linux ] || os=darwin
case "$(uname -m)" in x86_64|amd64) arch=amd64;; *) arch=arm64;; esac
archive="bb_${version}_${os}_${arch}.tar.gz"
mkdir "$TEST_ROOT/payload"; cat > "$TEST_ROOT/payload/bb" <<EOF
#!/bin/sh
if [ "\${1:-}" = version ]; then echo '$version'; else echo bb; fi
EOF
chmod 755 "$TEST_ROOT/payload/bb"
tar -C "$TEST_ROOT/payload" -czf "$TEST_ROOT/assets/$archive" bb
(cd "$TEST_ROOT/assets" && sha256sum "$archive" > checksums.txt)
cat > "$TEST_ROOT/bin/curl" <<'EOF'
#!/bin/sh
out=''; url=''
while [ "$#" -gt 0 ]; do case "$1" in -o) out=$2; shift 2;; -*) shift;; *) url=$1; shift;; esac; done
cp "$BB_TEST_ASSETS/$(basename "$url")" "$out"
EOF
case "$os/$arch" in
  linux/amd64) file_kind='ELF 64-bit LSB executable, x86-64';;
  linux/arm64) file_kind='ELF 64-bit LSB executable, ARM aarch64';;
  darwin/amd64) file_kind='Mach-O 64-bit executable x86_64';;
  darwin/arm64) file_kind='Mach-O 64-bit executable arm64';;
esac
cat > "$TEST_ROOT/bin/file" <<EOF
#!/bin/sh
echo '$file_kind'
EOF
cat > "$TEST_ROOT/bin/gh" <<'EOF'
#!/bin/sh
case "${1:-}" in
  auth) exit 0;;
  release)
    case "${2:-}" in
      view) printf '%s\n' 'v1.2.3'; exit 0;;
      download)
        out=''
        while [ "$#" -gt 0 ]; do
          case "$1" in --dir) out=$2; shift 2;; *) shift;; esac
        done
        cp "$BB_TEST_ASSETS/$BB_TEST_ARCHIVE" "$out/$BB_TEST_ARCHIVE"
        cp "$BB_TEST_ASSETS/checksums.txt" "$out/checksums.txt"
        exit 0;;
    esac;;
esac
exit 1
EOF
chmod 755 "$TEST_ROOT/bin/curl" "$TEST_ROOT/bin/file" "$TEST_ROOT/bin/gh"
export PATH="$TEST_ROOT/bin:$PATH" BB_TEST_ASSETS="$TEST_ROOT/assets" BB_DOWNLOAD_BASE='https://example.invalid/release'
export BB_TEST_ARCHIVE="$archive"

"$ROOT/scripts/install.sh" --version "$version" --install-dir "$XDG_BIN_HOME"
[ -x "$XDG_BIN_HOME/bb" ]
"$ROOT/scripts/install.sh" --version "$version" --install-dir "$XDG_BIN_HOME"
printf '#!/bin/sh\necho foreign\n' > "$XDG_BIN_HOME/bb"; chmod 755 "$XDG_BIN_HOME/bb"
if "$ROOT/scripts/install.sh" --version "$version" --install-dir "$XDG_BIN_HOME" 2>/dev/null; then exit 1; fi
"$ROOT/scripts/install.sh" --version "$version" --install-dir "$XDG_BIN_HOME" --force
before=$(sha256sum "$XDG_BIN_HOME/bb" | awk '{print $1}')
if "$ROOT/scripts/install.sh" --version "$version" --install-dir "$XDG_BIN_HOME" --dry-run >/dev/null 2>&1; then :; else exit 1; fi
[ "$before" = "$(sha256sum "$XDG_BIN_HOME/bb" | awk '{print $1}')" ]
"$ROOT/scripts/install.sh" --dry-run --install-dir "$XDG_BIN_HOME" >/dev/null
mkdir "$TEST_ROOT/checkout"; git -C "$TEST_ROOT/checkout" init -q
ln -sf "$TEST_ROOT/checkout/bb" "$XDG_BIN_HOME/bb"
if "$ROOT/scripts/install.sh" --version "$version" --install-dir "$XDG_BIN_HOME" 2>/dev/null; then exit 1; fi
"$ROOT/scripts/install.sh" --version "$version" --install-dir "$XDG_BIN_HOME" --migrate
[ -L "$XDG_BIN_HOME/bb" ] && exit 1
[ "$("$XDG_BIN_HOME/bb" version)" = "$version" ]
rm -f "$XDG_BIN_HOME/bb"
"$ROOT/scripts/install.sh" --version "$version" --install-dir "$XDG_BIN_HOME" --github-cli
[ "$("$XDG_BIN_HOME/bb" version)" = "$version" ]
rm -f "$XDG_BIN_HOME/bb"
"$ROOT/scripts/install.sh" --install-dir "$XDG_BIN_HOME" --github-cli
[ "$("$XDG_BIN_HOME/bb" version)" = "$version" ]
"$ROOT/scripts/install.sh" --github-cli --dry-run --install-dir "$XDG_BIN_HOME" >/dev/null
printf '%s\n' 'installer tests passed'
