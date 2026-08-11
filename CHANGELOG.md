# Changelog

All notable changes to `binbox-cli` are recorded here. The project follows
[Semantic Versioning](https://semver.org/); entries are derived from annotated
release tags and the corresponding release records.

## Unreleased

### Added

- Added `bb tfx browse [plan] [--json]`, a read-only Terraform plan viewer.
  Resources are ordered destroy, replace, update, create, and each one opens a
  read-only list of its changed attributes.
- Added `--json` and non-terminal table output for the same reading, using the
  existing schema-v1 envelope and human renderer.

### Security

- Replaced sensitive and not-yet-known plan values with `(sensitive)` and
  `(known after apply)` before they can reach a label, description, search
  metadata, table, or JSON envelope. A block marked sensitive covers every path
  inside it.

### Changed

- Ran multi-level selection inside one Bubble Tea program. `bb sec` no longer
  tears down and recreates the alternate screen when moving between Service,
  Field, and Action.
- Made returning to an earlier level restore the query and cursor it had, rather
  than resetting them.
- Made the TUI and `BB_SELECTOR=plain` walks share one level graph. An empty
  answer in the plain walk steps back one level, matching Escape.
- Ended the command when the new-field prompt is cancelled after choosing
  Rename, instead of returning to the action list.

## [0.10.0] - 2026-08-11

### Added

- Added checkout-independent native zsh completion through both
  `bb shell init zsh` and `bb completion zsh`.
- Added safe dynamic completion for projects, sessions, tmux sessions, wenv
  presets, AWS profiles, assume profiles, secret services, and secret fields.
- Added human-readable labels, sections, and tables as the default for
  bb-owned structured reads.
- Added `--json` schema-v1 envelopes for automation while retaining explicit
  JSON exports and external provider streams.

### Security

- Kept secret values and AWS credentials out of completion candidates.
- Sanitized terminal control characters at the human-rendering boundary.

See the [v0.10.0 release record](docs/release-v0.10.0.md).

## [0.9.0] - 2026-08-11

### Changed

- Reworked the secret manager into Service -> Field -> Action navigation.
- Made Escape return to the previous manager level and Ctrl+C exit immediately.

### Added

- Added `bb sec rename <service> <old-field> <new-field>` and the matching TUI
  action without exposing or re-entering the encrypted value.
- Made `bb sec copy` use the same service-first selection flow.

See the [v0.9.0 release record](docs/release-v0.9.0.md).

## [0.8.1] - 2026-08-11

### Changed

- Compacted secret identities into one `service / field` row.
- Removed empty description rows from selectors while preserving stable values
  and secret-safe search metadata.

See the [v0.8.1 release record](docs/release-v0.8.1.md).

## [0.8.0] - 2026-08-11

### Added

- Added the interactive `bb sec` manager for copy, replace, and remove actions.
- Added overwrite protection to `bb sec set`; automation must use `--force` for
  an existing value.
- Added `bb sec exec <service> -- <command>` for child-process-only secret
  injection.

See the [v0.8.0 release record](docs/release-v0.8.0.md).

## [0.7.1] - 2026-08-11

### Security

- Changed terminal secret entry to a no-echo prompt.
- Rejected oversized input, unsafe store/key file types and permissions,
  missing secret targets, and normalized environment-name collisions.
- Allowed recovery when a valid age key exists before the first encrypted
  store is created.

See the [v0.7.1 release record](docs/release-v0.7.1.md).

## [0.7.0] - 2026-08-11

### Changed

- Redesigned the Bubble Tea TUI around immediate search, first-press movement,
  result counts, metadata, paging, responsive narrow layouts, and `NO_COLOR`.
- Unified non-Git mutation prompts under a responsive confirmation card that
  defaults to Cancel.
- Kept all interactive rendering on stderr so stdout remains machine- and
  shell-safe.

See the [v0.7.0 release record](docs/release-v0.7.0.md).

## [0.6.0] - 2026-08-11

### Added

- Added safe `wenv show` and confirmed `wenv apply` flows.
- Added `assume`, `assume list/current/unset/exec`, and profile compatibility
  using AWS CLI-owned credential resolution.

### Security

- Refused credential-bearing shell exports directly to a terminal.
- Scoped assumed credentials to generated shell integration or one child
  process and excluded them from bb state and journals.

See the [v0.6.0 release record](docs/release-v0.6.0.md).

## [0.5.2] - 2026-08-11

### Fixed

- Corrected the Neovim target fallback to `~/.config/nvim` when
  `XDG_CONFIG_HOME` is unset on macOS.

See the [v0.5.2 release record](docs/release-v0.5.2.md).

## [0.5.1] - 2026-08-11

### Fixed

- Added multiline legacy `EXPORTS=(...)` import support without accepting
  executable shell syntax.
- Hardened selector cancellation, exact-name handling, and deterministic plain
  fallback behavior.

See the [v0.5.1 release record](docs/release-v0.5.1.md).

## [0.5.0] - 2026-08-11

### Added

- Replaced the fzf runtime dependency for `tm`, `wenv`, and `sec copy` with an
  embedded Bubble Tea fuzzy selector.
- Added deterministic numbered selection for non-TTY and plain-mode use.

### Fixed

- Normalized macOS `/private/var` project paths and standard `lsof` listener
  parsing.
- Made release archive and checksum generation portable through Go tooling.

See the [v0.5.0 release record](docs/release-v0.5.0.md).

## [0.4.1] - 2026-08-11

### Added

- Added checkout-independent shell integration and recorded retirement of the
  Workbench consumer path.

## [0.4.0] - 2026-08-11

### Added

- Migrated AWS profile, declarative environment, and age-encrypted secret
  workflows into the Go binary.

## [0.3.0] - 2026-08-11

### Added

- Migrated the remaining non-secret legacy workflows, including explicit
  provider adapters and local inspection commands.

## [0.2.0] - 2026-08-10

### Added

- Migrated core legacy workflows into typed Go command handlers.
- Added immutable private Terraform plan snapshots before apply and destroy.

## [0.1.0] - 2026-08-10

### Added

- Established the `bb` contract, XDG-owned local state, redacted journals,
  migration tooling, installer, private release delivery, and release guards.

The `v0.1.0-rc.1` through `v0.1.0-rc.5` tags were rollout candidates and are
superseded by v0.1.0.

[0.10.0]: https://github.com/jisung9870/binbox-cli/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/jisung9870/binbox-cli/compare/v0.8.1...v0.9.0
[0.8.1]: https://github.com/jisung9870/binbox-cli/compare/v0.8.0...v0.8.1
[0.8.0]: https://github.com/jisung9870/binbox-cli/compare/v0.7.1...v0.8.0
[0.7.1]: https://github.com/jisung9870/binbox-cli/compare/v0.7.0...v0.7.1
[0.7.0]: https://github.com/jisung9870/binbox-cli/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/jisung9870/binbox-cli/compare/v0.5.2...v0.6.0
[0.5.2]: https://github.com/jisung9870/binbox-cli/compare/v0.5.1...v0.5.2
[0.5.1]: https://github.com/jisung9870/binbox-cli/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/jisung9870/binbox-cli/compare/v0.4.1...v0.5.0
[0.4.1]: https://github.com/jisung9870/binbox-cli/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/jisung9870/binbox-cli/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/jisung9870/binbox-cli/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/jisung9870/binbox-cli/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/jisung9870/binbox-cli/releases/tag/v0.1.0
