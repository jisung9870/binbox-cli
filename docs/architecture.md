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
The CLI uses Bubble Tea/Bubbles and Lip Gloss for its search-first selection and
default-cancel confirmation TUI, and otherwise keeps integrations behind direct
external CLI boundaries. It has no runtime
checkout, `BB_ROOT`, libexec, script PATH, daemon, dashboard, or MCP proxy.

| Surface | Current minimum behavior | State/effect |
|---|---|---|
| `bb version` | Prints the CLI version | None |
| `bb doctor [--json]` | Checks core integrations plus optional Docker, port, AWS session, age, Trivy, and Terraform-summary tools | Read-only PATH inspection; fzf is not required |
| `bb project add/list/remove` | Maintains a local project registry with stable `prj_` IDs | `$XDG_CONFIG_HOME/bb/projects.json`, mode `0600` |
| `bb project import sessionizer --check` | Expands the shared parent/`=direct` grammar and reports candidates, dead paths, duplicates, and collisions | Strictly read-only; source and registry remain byte-identical |
| `bb project import sessionizer --apply` | Adds non-conflicting candidates with stable origin metadata | Writes bb registry/recovery state only; verifies the source hash and preserves source bytes |
| `bb session start/stop/list` | Maintains bb-owned session intent records with stable `ses_` IDs; it does not claim a tmux/Orca session | `$XDG_STATE_HOME/bb/sessions.json`, mode `0600` |
| `bb run <command> [args]` | Executes an explicit external command | Journal stores only executable basename, argument count, exit code, and timestamp |
| `bb run list/show/export` | Reads bb-owned run records with stable `run_` IDs and outcomes | No provider scraping or external lifecycle authority |
| `bb session open <project-id>` | Returns an explicit backend plan | Never opens or destroys a terminal; Orca is capability-unavailable by design |
| `bb tm projects --plain|--json` | Returns the sessionizer/LazyVim-compatible normalized project view | Read-only bb registry; never rewrites the shared legacy source |
| `bb tm sessions --json` | Preserves legacy typed tmux session fields | Session-level observation only; no panes, commands, or scrollback |
| `bb tm [--project <id>]` | Selects through bb's search-first responsive TUI (numbered fallback), then attaches or creates `bb-<project-id>` through tmux | No fzf, shell evaluation, Orca invocation, lifecycle registry, or ownership claim |
| `bb profile ...` | Manages AWS SSO profiles and delegates login | Writes only `~/.aws/config` atomically with backups; credentials/cache remain AWS CLI-owned |
| `bb assume ...` | Restores current-shell and scoped-command credential UX through AWS CLI resolution | No bb credential parsing/cache; stdout credentials are refused on a terminal and evaluated only through generated shell integration |
| `bb wenv ...` | Imports an allowlisted non-executable legacy subset and previews/applies declarative environments | XDG JSON, secret-like key rejection, search-first selector, default-cancel confirmation, numbered fallback |
| `bb sec ...` | Uses the existing age-encrypted JSON/key format through Service→Field→Action navigation | Plaintext remains in memory/pipes; field rename moves the in-memory value only; ciphertext mutation is locked, atomic, and backed up |
| `bb tm attach/kill/dirs/layout` | Operates on an exact tmux session or bb project registry entry; layouts are fixed Go-owned recipes | Destructive actions show targets and re-observe before direct tmux argv; no legacy directory-file writes |
| `bb git root/branch/log` | Returns bounded Git repository metadata | Direct read-only Git argument vectors; no shell evaluation |
| `bb gx ...` | Provides explicit Git branch/root/log compatibility without shell or fzf dependence | Branch deletion shows and re-observes the exact ref and refuses the current branch |
| `bb kx ...` / `bb assm ...` | Streams explicit kubectl and AWS SSM operations | Direct argv only; identifiers/ports are validated and no credentials are read or persisted |
| `bb port inspect/kill` | Reports listeners and optionally sends SIGTERM to an exact PID set | Kill requires lsof, prints/optionally confirms, then re-observes before mutation |
| `bb tfx init/validate/fmt/plan/sum/session/apply/destroy/status/end/review/clean/state ...` | Preserves the guarded Terraform workflow and legacy account-bound safety session | Direct execution; exact legacy TSV compatibility; review output uses fresh owner-only XDG state; every destructive path is bounded, confirmed, and re-observed; plan mutation uses a private immutable snapshot |
| `bb tvx image/repo/config/ci/sbom/report/k8s/clean/doctor` | Preserves the Trivy workflow and fixed CI/report policies | Direct Trivy arguments; node collector requires confirmation; no config or credential mutation |
| `bb mcp inventory/audit` | Reports candidate presence and content hashes without returning configuration content | Appends redacted metadata-only `mcp_audit` journal event; never mutates config |
| `bb sec` | Selects a service, one scoped field, then a safe copy/replace/rename/remove action | Plaintext never enters selector labels, descriptions, stdout, or the journal |
| `bb sec exec <service> -- ...` | Decrypts once and overlays normalized fields on one child environment | Parent environment and encrypted store are unchanged; no export text is emitted |
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
- Git, Kubernetes, AWS SSM, process, and Terraform adapters accept structured
  targets and invoke their owner CLI with a direct argument vector. They never
  evaluate shell strings or persist provider credentials.
- The bb-owned TUI writes UI to stderr and keeps stdout machine/eval-safe.
  Printable input filters immediately; results expose only safe metadata and a
  stable value; destructive non-Git confirmations default to Cancel. Non-TTY
  input/output and `BB_SELECTOR=plain` use deterministic text fallbacks, while
  `NO_COLOR` disables color without removing selection markers or key hints.
  The tm selector passes only the selected value as a direct tmux argument:
  tmux remains the owner of its sessions and Orca is never consulted.
- Sessionizer compatibility is explicit import, not a live mirror. New tmux
  sessions use stable `bb-<project-id>` names; basename-named legacy sessions
  are left untouched to avoid adopting a same-name session for another path.
- Doctor retains the versioned capability shape (including nullable `path`),
  while applying bb's own dependency policy: Git is core and feature-specific
  tools are optional rather than making the whole CLI unavailable.
- Terraform apply and destroy never hand Terraform the caller-controlled plan
  pathname. `bb` rejects symlink/non-regular plan sources, copies the
  already-open file descriptor to a synced `0700` state subdirectory with a
  `0600` snapshot, presents the source name and SHA-256 to the user, and passes
  only that private snapshot to Terraform after revalidation. The snapshot is
  removed on confirmation cancel, revalidation failure, Terraform return, or
  other error; the caller's plan source is never changed by `bb`.
- Terraform review logs and plan JSON are written to a fresh
  `${XDG_STATE_HOME}/bb/tfx-review-*` directory with `0700`/`0600` permissions
  through an anchored filesystem root. They are never placed in a repository.
  Cleanup recognizes only fixed bb artifact names; caller-controlled plan
  environment variables cannot authorize deletion of source files.
- Recovery-relevant state remains inspectable/exportable. Future state schema
  changes require versioned readers, backups, and non-destructive migration.

## Distribution and `bb setup nvim`

Release CI builds `linux`/`darwin` for `amd64`/`arm64` and publishes SHA-256
checksums plus provenance/SBOM. The installer uses an atomic replacement at a
configurable path (default `~/.local/bin/bb`). Verification occurs before
replacement and the previous executable remains recoverable until `bb version`
succeeds. A foreign file or checkout symlink is never replaced without explicit
migration consent.

`bb setup nvim` now implements local selection, dry-run, identity/conflict
validation, and consent-gated linking for an already-present `lazyvim-config`.
`bb doctor nvim` validates the same contract. Network acquisition, prerequisite
package installation, and plugin restoration remain separate and unimplemented;
the binary never embeds or silently clones the configuration.

## Explicit non-goals

No dashboard; no MCP proxy/config mutation/automatic install; no credential
store; no shell evaluation; no generic legacy script forwarding; and no Orca
agent/worktree/scheduler/DAG implementation.
