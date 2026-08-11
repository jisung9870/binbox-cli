# macOS cutover record — 2026-08-11

## Result

The personal macOS device now resolves `bb` to the installed `v0.5.2` arm64
binary rather than a legacy-checkout symlink. Shell startup uses
`eval "$(bb shell init zsh)"`; the previous checkout-sourced block was removed
after an owner-only backup was created.

No machine-specific path, credential, secret value, or local recovery artifact
is committed in this record.

## Migration evidence

| Check | Result |
|---|---|
| Installed release identity | `0.5.2`, commit `4c39722270d8e91492daf1335e06892f4c8091ff` |
| Project import | 63 imported; second apply 0 imported / 63 already present; collisions 0 |
| `tm projects` | 63 registry records |
| Wenv import | 2 multiline legacy presets imported |
| Existing age store | 7 service names readable; no values printed or copied |
| Neovim | desired existing link recognized; network-free headless probe passed |
| Doctor | 14/14 documented owner capabilities available |
| Shell | `.zshrc` syntax passed; generated `bb` function returned version 0.5.2 |
| Source preservation | sessionizer, wenv, ciphertext, and key pre/post hashes matched |

The first cutover attempt exposed two macOS compatibility gaps. Multiline
legacy `EXPORTS=( ... )` arrays were fixed and released in `v0.5.1`; Neovim's
unset-XDG fallback to `~/.config/nvim` was fixed and released in `v0.5.2`.
Both fixes have regression tests and passed the full release gate.

## Recovery

An owner-only local recovery directory contains the pre-cutover shell file,
legacy executable symlink, Neovim link, byte-identical sessionizer source,
encrypted secret store, key, and an exact restoration procedure. Installer
rollback backups preserve the legacy symlink and intermediate binaries.

The automated installer matrix proves failed validation restores the previous
target. A disruptive rollback of the active personal shell was not needed.

## Observation window

The seven-day legacy-only usage observation runs from 2026-08-11 through
2026-08-18. An Orca automation performs a read-only daily contract check from
August 12 through August 18, updates only the Orca worktree comment, and removes
itself after the final run. Repository/archive actions remain outside this
automated cutover; the user will make the legacy repository private or remove
it separately after the observation window.
