#!/usr/bin/env bash
# Release lifecycle entrypoint; never deletes bb configuration or secrets.
set -euo pipefail
repo=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd -P)
action=${1:-help}
[[ $# == 0 ]] || shift
install_dir=${XDG_BIN_HOME:-"$HOME/.local/bin"}
version=''
transport=auto
while [[ $# -gt 0 ]]; do
  case "$1" in
    --install-dir|--version)
      [[ $# -ge 2 ]] || { echo "Missing value: $1" >&2; exit 2; }
      if [[ $1 == --install-dir ]]; then install_dir=$2; else version=$2; fi
      shift 2 ;;
    --github-cli) transport=gh; shift ;;
    --public) transport=public; shift ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done
[[ -n $install_dir && $install_dir != / ]] || exit 2
binary="$install_dir/bb"
receipt="$install_dir/.bb.managed.sha256"
digest() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}
case "$action" in
  install|upgrade)
    args=(--install-dir "$install_dir" --force)
    [[ -z $version ]] || args+=(--version "$version")
    if [[ $transport == gh ]] || { [[ $transport == auto ]] && command -v gh >/dev/null 2>&1 && gh auth status -h github.com >/dev/null 2>&1; }; then
      args+=(--github-cli)
    fi
    # The verified installer handles download, checksum, backup and replacement.
    bash "$repo/scripts/install.sh" "${args[@]}"
    digest "$binary" > "$receipt"
    echo "Ready: $binary ($("$binary" version))"
    ;;
  uninstall)
    if [[ ! -e $binary && ! -L $binary ]]; then echo 'bb is already uninstalled.'; exit 0; fi
    if [[ -L $binary || ! -f $receipt || $(digest "$binary") != "$(cat "$receipt")" ]]; then
      echo 'bb is not owned by this manager or was modified. Run manage.sh install to install a verified release first.' >&2
      exit 1
    fi
    saved=$(mktemp -d "$install_dir/.bb.uninstalled.XXXXXX")
    mv "$binary" "$saved/bb"
    mv "$receipt" "$saved/receipt.sha256"
    echo "Uninstalled bb; binary backup: $saved/bb"
    echo 'Configuration, secrets, state and shell configuration are preserved.'
    ;;
  help|-h|--help)
    echo 'Usage: bash manage.sh install|upgrade|uninstall [--version VERSION] [--install-dir DIR] [--github-cli|--public]'
    echo 'install/upgrade: latest verified release by default; authenticated gh is detected automatically'
    echo 'uninstall: remove the managed binary from PATH, preserving data and a binary backup'
    ;;
  *) echo "Unknown action: $action" >&2; exit 2 ;;
esac
