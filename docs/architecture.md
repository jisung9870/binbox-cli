# Architecture

## Intent

`bb` is one installable Go binary for portable local project, session-intent,
execution-journal, dependency-health, and integration-inventory workflows. It
does not reproduce Orca lifecycle management, embed the LazyVim configuration,
or keep the old shell dispatcher/libexec architecture.

## Ownership boundaries

| Owner | Owns | Integration boundary |
|---|---|---|
| binbox (`bb`) | CLI contract, XDG project/session records, redacted run journal, doctor, MCP inventory/audit, install of its own binary | May invoke declared external CLIs and retain opaque result pointers; must re-check capabilities and never infer lifecycle authority |
| Orca | Agent, terminal, worktree, Run/Task/Dispatch, scheduler, and DAG lifecycle | `bb` exposes read-only availability/status only; no parallel registry, scheduler, or terminal-control implementation |
| `lazyvim-config` | Neovim and tmux configuration, plugins, keymaps, lockfiles, and editor-local setup | Future `bb setup nvim` selects/verifies/links a separately versioned checkout; it never embeds or silently overwrites the config |
| Workbench/setup (transitional) | Existing data and compatibility evidence during migration | No new long-lived features; functions move to `bb`, remain with Orca/LazyVim, or are archived |

The ownership test for every feature is: portable CLI state belongs to binbox;
live multi-agent lifecycle belongs to Orca; human editor/terminal configuration
belongs to LazyVim. A feature without one clear owner is deferred.

## Runtime shape

The module has a thin `cmd/bb` entry point and an internal application package.
The MVP intentionally uses the Go standard library only. It has no runtime
checkout, `BB_ROOT`, libexec, script PATH, daemon, dashboard, or MCP proxy.

| Surface | Current minimum behavior | State/effect |
|---|---|---|
| `bb version` | Prints the CLI version | None |
| `bb doctor [--json]` | Checks `git`, `tmux`, `kubectl`, `aws`, `terraform`, and Orca availability with purpose/recovery text | Read-only PATH inspection |
| `bb project add/list/remove` | Maintains a local project registry with stable `prj_` IDs | `$XDG_CONFIG_HOME/bb/projects.json`, mode `0600` |
| `bb project import sessionizer --check` | Expands the shared parent/`=direct` grammar and reports candidates, dead paths, duplicates, and collisions | Strictly read-only; source and registry remain byte-identical |
| `bb session start/stop/list` | Maintains bb-owned session intent records with stable `ses_` IDs; it does not claim a tmux/Orca session | `$XDG_STATE_HOME/bb/sessions.json`, mode `0600` |
| `bb run <command> [args]` | Executes an explicit external command | Journal stores only executable basename, argument count, exit code, and timestamp |
| `bb mcp inventory/audit` | Reports candidate presence and content hashes without returning configuration content | Appends redacted metadata-only `mcp_audit` journal event; never mutates config |
| `bb export [--output path]` | Exports journal events as JSON | Read-only journal access; optional `0600` output |
| `bb orca status` | Calls Orca's read-only JSON status endpoint | No Orca mutation or duplicated state |

Subcommand contracts will be versioned before external consumers switch. Until
then this is an MVP surface, not a compatibility promise for every legacy shell
tool.

## State, safety, and recovery

- Configuration: `${XDG_CONFIG_HOME:-$HOME/.config}/bb`.
- State: `${XDG_STATE_HOME:-$HOME/.local/state}/bb`.
- JSON registries are written with owner-only permissions. Journal events are
  newline-delimited JSON and export as a JSON array.
- Mutating registry operations take an exclusive sibling lock and replace state
  through a synced owner-only temporary file plus atomic rename. Concurrent CLI
  writers therefore cannot silently discard one another's update.
- `--json` uses the schema-v1 envelope with `ok`, `data`, `warnings`, and
  structured `error`. Exit `2` is invalid invocation, `3` is unavailable
  capability, and `1` is an operational failure.
- Journals use an allowlist. Raw prompts, environments, command arguments,
  terminal output, credentials, and MCP configuration contents are not stored.
- External resources are observed before action; local records never establish
  ownership of tmux, Workbench, or Orca objects.
- Recovery-relevant state remains inspectable/exportable. Future state schema
  changes require versioned readers, backups, and non-destructive migration.

## Distribution and `bb setup nvim`

Release CI should build `linux`/`darwin` for `amd64`/`arm64`, publish SHA-256
checksums plus provenance/SBOM, and install atomically to a configurable path
(default `~/.local/bin/bb`). Verification occurs before replacement and the
previous executable remains recoverable until `bb version` succeeds. A foreign
file or checkout symlink is never replaced without explicit migration consent.

`bb setup nvim` is designed but not implemented in this MVP. Its contract has
four separately confirmable steps: acquire or select a `lazyvim-config` revision,
install prerequisites with dry-run support, link `$XDG_CONFIG_HOME/nvim` after
identity/conflict checks, and validate with doctor/headless Neovim. Acquisition
and link mutation require explicit flags; plugin/network restoration is separate.

## Explicit non-goals

No dashboard; no MCP proxy/config mutation/automatic install; no credential
store; no shell evaluation; no generic legacy script forwarding; and no Orca
agent/worktree/scheduler/DAG implementation.
