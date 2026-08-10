# Legacy analysis for the `bb` MVP

## Scope and evidence

This is a read-only assessment of the existing `binbox`, LazyVim configuration,
Workbench, and top-level setup repository. It is a migration design, not a claim
that a legacy command is already implemented by the new binary. The target is a
single Go `bb` binary with an intentionally smaller, stable public surface.

## Inventory and disposition

| Legacy area | Current responsibility | Disposition | Migration decision |
|---|---|---|---|
| `bb` dispatcher, help, completion entry point | Finds executable shell tools and exposes one PATH command | **Migrate** | Preserve the one-command ergonomics as Go subcommands; do not preserve executable discovery as an extension model in the MVP. |
| `binbox-doctor` | Dependency and installation diagnostics | **Migrate** | Become `bb doctor`: capability-oriented, read-only, structured output with recovery guidance. |
| `tm projects` and sessionizer directories | Project candidate discovery from `~/.config/tmux-sessionizer/dirs` | **Migrate** | Support import/read compatibility and a normalized project view; retain legacy path and grammar during transition. |
| `tm` interactive selection, layouts, attach, kill | tmux-centric interaction and lifecycle | **Defer** | MVP may observe or launch explicit sessions through `bb session`; retain `tm` for fzf flows, layouts, and destructive bulk operations. |
| `agents` pane scrape and jump | tmux-pane-based Codex/Claude status | **Defer** | New `bb run` records only its own redacted metadata. Do not infer provider state by scraping terminal output. |
| `sec` age-backed secret CRUD | Local encrypted secrets | **Defer** | Keep current tool or Workbench migration path. The MVP journal must never contain secret values. |
| `wenv` shell presets and kube-context mutation | Environment selection and shell export | **Defer** | Do not source shell in Go. Offer inventory only until a typed, non-evaluating migration format is accepted. |
| `kx`, `tfx`, `tvx`, `gx`, `dx`, `assume`, `assm`, `md2jira`, `portcheck` | Domain-specific interactive glue for Kubernetes, Terraform, Git, AWS, Jira, and ports | **Retire** from the new public contract | Continue as separate legacy scripts while useful; no compatibility promise in the MVP. |
| `binbox-setup`, `bb upgrade`, generated aliases | Installation, shell mutation, and git-based update | **Retire** from the binary | A single-binary installer/release flow must be separately designed; `bb` must not alter shell RC files or self-update in MVP. |
| `binbox-check`, `bb new` | Repository-authoring helpers | **Retire** | Development-only responsibilities, not user runtime behavior. |
| Workbench project registry | Canonical projects and typed JSON schema-v1 output | **Keep** as a reference contract | Reuse its explicit IDs, canonical paths, JSON-envelope discipline, and non-destructive removal policy; avoid claiming ownership of its files. |
| Workbench managed sessions/worktrees/agents | Typed lifecycle and ownership verification | **Keep** as a reference contract | Adopt its observe-before-act and exact-target safety rules. Do not duplicate its registries or destructive APIs. |
| Workbench server/dashboard/secrets/environments | Local control plane and stateful product capabilities | **Defer** | New `bb` may report availability only. It must not proxy, configure, or mutate Workbench in MVP. |
| LazyVim Workbench client | Async versioned-JSON consumer with a five-second timeout | **Keep** | Preserve schema-version checks, single JSON document on stdout, diagnostics on stderr, and graceful unavailable state. |
| LazyVim fallback chain | `wb` projects, then `bb tm projects`, then sessionizer dirs | **Migrate** | Make `bb project list --json` a stable replacement only after the existing fallback receives representative compatibility evidence. |
| LazyVim/editor configuration and plugin lockfile | Editor UX, keymaps, plugin lifecycle | **Keep outside `bb`** | A separate LazyVim-config install/link contract owns it; `bb` can diagnose presence but never edit it. |
| Top-level setup scripts and lock files | Bootstrap orchestration across repositories | **Retire** from `bb` | Keep as transitional installer documentation until each repository has an explicit independent installation story. |

## Proposed Go public command contract

The public command set is deliberately noun-oriented and non-interactive by
default. All commands accept `--help`. Read commands support `--json`; JSON emits
one schema-versioned envelope to stdout, while warnings and diagnostics go to
stderr. Exit code `2` denotes invalid invocation, `3` denotes a requested
capability that is unavailable, and other non-zero codes denote operational
failure.

| Command | Minimum behavior | Safety and compatibility rules |
|---|---|---|
| `bb version [--json]` | Print binary version, build metadata, supported journal schema, and compiled feature set. | Must work without configuration or external executables. JSON redacts build environment details that reveal local paths. |
| `bb doctor [--json] [--strict]` | Report core state plus optional Git, tmux, Workbench, Orca CLI, and LazyVim capabilities. | Read-only; results include name, scope, status, reason, and recovery. Default failure is core-only; `--strict` includes applicable optional checks. Never repair or install. |
| `bb project list [--json]` | List known projects and optionally identified legacy/sessionizer candidates. | Canonicalize paths for identity, but redact or user-relativize journal display paths. Keep an origin (`bb`, `workbench`, or `sessionizer`). |
| `bb project show <id> [--json]` | Return one normalized project record. | IDs are stable lowercase identifiers; ambiguous/duplicate legacy paths fail clearly. Never directly edit Workbench or sessionizer files. |
| `bb project import sessionizer [--check|--apply] [--file <path>] [--json]` | Validate and optionally import the documented sessionizer directory grammar. | Accept only comments, home expansion, normal roots, and the explicit-directory prefix. Never source/evaluate data. `--check` is non-mutating; `--apply` is idempotent and preserves source data. |
| `bb session list [--json]` | Inventory observable sessions with provider, project association, and ownership confidence. | Observation never grants stop/attach ownership; avoid scrollback, environment values, and command arguments. |
| `bb session open <project-id> [--backend auto|tmux|orca|shell] [--json]` | Plan and, where supported, open a project with a selected backend. | Explicit backend has no silent fallback. Unsupported MVP backends return capability-unavailable instead of guessing. |
| `bb session attach <session-id>` | Attach only to a verified, user-selected session. | Interactive attachment is terminal-only; JSON callers receive an action plan/capability result. Destructive session operations are outside the MVP. |
| `bb run list [--json]` | List entries in the new local, redacted run journal. | Journal entries are audit metadata, not authority over external tmux, Workbench, or Orca processes. |
| `bb run start <project-id> [--agent <name>] [--backend <name>] [--json]` | Record intent and invoke an explicit supported integration. | Persist intent before launch and reconcile after. Require capability/ownership checks; do not scrape provider output or proxy credentials. |
| `bb run show <run-id> [--json]` / `bb run export [--format json]` | Inspect/export redacted journal entries. | Export schema/version, timestamps, outcome, and opaque references only; never secret values, shell environments, scrollback, raw prompts, or unrestricted paths. |
| `bb mcp status [--json]` | Inventory MCP/Orca integration capability and the redacted audit/export boundary. | Read-only; no server start, configuration change, or sensitive configuration enumeration. |
| `bb mcp audit [--json]` | Export a redacted journal-oriented audit view for a local MCP consumer. | Inventory/audit only: no config mutation, installation, proxying, remote orchestration, terminal control, or credential forwarding. |

The MVP intentionally has no generic `bb <legacy-tool>` forwarding contract.
That avoids a superficially compatible dispatcher whose behavior varies with
locally executable scripts. A transitional shell alias or documented legacy
PATH installation can keep specialist tools available while callers migrate.

## Migration and compatibility plan

1. Establish the binary without changing legacy installation, aliases, or
   sessionizer files. `version`, `doctor`, and read-only inventory should work
   in an empty XDG state directory.
2. Freeze a schema-v1 JSON envelope: one document on stdout, explicit
   `schema_version`, `ok`, `data`, `warnings`, and structured error details.
   Consumers reject unknown schema versions rather than guessing.
3. Introduce `bb project import sessionizer --check` first. Compare candidates,
   normalized IDs, missing directories, and collisions without writing either
   side. Offer repeat-safe `--apply` only after review.
4. Let LazyVim prefer the new project endpoint only after it demonstrates the
   same timeout, JSON validation, and fallback behavior. Keep Workbench-first
   and legacy fallback paths for a measured usage period; record source
   observations rather than treating one success as retirement evidence.
5. Migrate session/run behavior by creating records only for actions started by
   `bb`; legacy tmux/provider state remains observed external state. Never
   manufacture ownership from a matching name.
6. Keep Workbench's project, secret, environment, worktree, and agent stores
   as independent authority. Provide capability/status links only until an
   explicit bidirectional data contract is approved.
7. Retire fallbacks only after fresh-install, pre-existing sessionizer, stale
   path, duplicate-path, unavailable-integration, malformed-JSON, and
   downgrade/read-old-state acceptance checks pass.

## Ownership boundaries

| Owner | Owns | Does not own |
|---|---|---|
| **binbox-next (`bb`)** | Portable local CLI UX, XDG-owned project/run journal, capability checks, normalized read models, and redacted export. | Orca lifecycle, terminal provider state, LazyVim configuration, secret contents, or legacy shell-tool behavior. |
| **Orca** | Worktree/terminal lifecycle, coordination provenance, agent dispatch authority, and its own browser/emulator surfaces. | `bb` configuration/journals; it is discovered through read-only capability/status integration unless an explicit future contract authorizes more. |
| **LazyVim config** | Editor plugins, keymaps, picker UX, editor-local settings, and its own install/link/lockfile lifecycle. | Project/session/run truth; it consumes versioned command output asynchronously and must not directly read or rewrite `bb`/Workbench state. |
| **Workbench (transitional peer)** | Its typed registries, managed-session/worktree/agent ownership, dashboard, environments, and secrets store. | A hidden implementation detail of `bb`; import/observation must not create mutual configuration mutation. |

This avoids a CLI claiming authority over an externally created terminal and an
editor/plugin install becoming coupled to a project-state migration.

## Compatibility and data-format risks

| Risk | Why it matters | Mitigation |
|---|---|---|
| `bb` name remains a shell dispatcher | Replacement can break aliases, completions, and non-interactive tmux/Neovim calls. | Ship an explicit cutover path, retain legacy executable access temporarily, and test non-interactive invocation. |
| Sessionizer grammar is shared with LazyVim | Changing path, comments, home expansion, or explicit-directory prefix silently changes discovery. | Treat grammar/path as import-only compatibility; use strict parsing and cross-client fixtures. |
| `wenv` is executable shell configuration | Naive migration can run arbitrary shell or leak values. | Parse a narrow typed subset, reject unsupported syntax, never source/evaluate. |
| Secret stores are encrypted local data | Careless copying loses access or creates divergent decryptable copies. | Keep secrets out of MVP; later migration needs check/apply, backups, source preservation, and metadata-only reporting. |
| JSON consumers expect schema-v1 envelopes | Extra stdout, altered fields, or drift breaks Neovim/automation. | One JSON document, stderr diagnostics, schema checks, invalid/trailing-JSON and error fixtures. |
| Paths differ across macOS, WSL, Windows | Raw equality creates duplicates and reveals machine details. | Canonicalize locally, store stable IDs/origins, redact export paths, make mappings explicit. |
| tmux IDs and pane state are ephemeral | Names/indexes can target wrong objects; scrollback exposes data. | Use stable provider identifiers, re-observe before actions, record minimal metadata. |
| Orca/Workbench version or availability varies | Silent fallback can create a wrong terminal or false success. | Capability-unavailable results with recovery; explicit choices never silently fall back. |
| Existing calls use `bb tm` and `bb agents` | Removing before consumer migration breaks editor/tmux workflows. | Maintain observations and acceptance tests; defer retirement until primary usage and fallback coverage exist. |
| Journal/audit can capture sensitive context | Prompts, environments, arguments, and terminals may contain credentials. | Allowlist/redact fields, restrict file permissions, and test redaction. |

## Acceptance criteria before legacy retirement

- `bb doctor --json` is read-only, schema-versioned, and distinguishes core,
  optional, and disabled capabilities.
- Project inventory/import is idempotent and leaves legacy sessionizer input
  unchanged; malformed and duplicate entries are visible, not silently dropped.
- LazyVim consumes the new JSON contract asynchronously, detects schema mismatch,
  and falls back without blocking its UI.
- Missing Orca, tmux, or Workbench produces a clear capability result and never
  installs, proxies, mutates configuration, or guesses terminal control.
- Exported run/MCP audit output contains no secret value, shell environment,
  scrollback, raw prompt, or unaudited provider payload.
- Specialist legacy scripts remain independently callable until each replacement
  has an approved contract and migration evidence.
