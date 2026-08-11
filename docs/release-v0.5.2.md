# v0.5.2

`v0.5.2` fixes the LazyVim target discovered during macOS cutover verification.

## Fixed

- Resolve the Neovim configuration target as `$XDG_CONFIG_HOME/nvim` when set
  and `~/.config/nvim` otherwise, matching Neovim's XDG fallback on macOS.
- Keep bb's own platform config fallback independent from the Neovim target.

## Verification

- Dry-run and doctor recognize the existing macOS `~/.config/nvim` symlink.
- The headless, network-free Neovim probe succeeds without changing the link.
- Unit, race, vet, installer, and deterministic release guard suites pass.
