# Decision log

## 2026-08-10 — One Go binary

Decision: `bb` is a standard-library-first Go binary with no checkout, libexec,
`BB_ROOT`, or helper-script PATH dependency. External tools remain explicit
system capabilities checked by doctor.

Why binbox owns it: portable CLI behavior and its local state must version and
ship together. Why Orca does not: installation and local project metadata are
not agent lifecycle. Why LazyVim does not: editor configuration must not become
the runtime for general CLI workflows.

## 2026-08-10 — Orca is the lifecycle authority

Decision: `bb` may expose read-only Orca availability/status and opaque result
pointers, but does not implement agent, worktree, Run/Task/Dispatch, scheduler,
DAG, terminal, or dashboard lifecycle.

Why Orca owns it: it has the live authority and provenance needed for safe
multi-agent actions. Why binbox does not: a second registry creates split-brain
ownership. Why LazyVim does not: editor/tmux UI is a human workspace surface,
not lifecycle authority.

## 2026-08-10 — LazyVim remains separate

Decision: future `bb setup nvim` selects, verifies, and links a separately
versioned `lazyvim-config`; it does not embed or silently clone/overwrite it.

Why LazyVim owns it: plugins, keymaps, tmux configuration, and lockfiles evolve
on an editor release cycle. Why binbox participates: a portable installer can
provide explicit acquire/link/doctor orchestration. Why Orca does not: editor
configuration is unrelated to managed agent lifecycle.

## 2026-08-10 — XDG state and metadata-only journals

Decision: bb-owned configuration/state uses XDG paths with owner-only files.
Run journals retain only executable basename, argument count, exit code, and
timestamp; MCP audit retains candidate/existing counts and content hashes, not
configuration contents.

Why binbox owns it: this is recovery/audit state for actions performed through
the CLI. Why Orca does not: it neither produced nor owns these local records.
Why LazyVim does not: consuming UI must use a versioned command contract rather
than reading mutable state files directly.

## 2026-08-10 — MCP starts read-only

Decision: implement inventory and redacted audit only. No config mutation,
automatic install, proxy, credential forwarding, server lifecycle, or sensitive
configuration enumeration is included.

Reason: capability discovery is reversible and auditable; the excluded actions
would require credential and ownership decisions that have not been approved.

## 2026-08-10 — Migrate stateful behavior before shell breadth

Decision: do not port every legacy command or add a generic executable-forwarding
contract. Prioritize doctor, project/session intent, run journal, export, and
read-only integration inventory; specialist shell tools remain transitional or
are retired individually.

Reason: typed state and recovery semantics are the durable value. Recreating a
variable PATH dispatcher inside the binary would preserve coupling without a
stable public contract.

## Deferred decisions

- Exact `lazyvim-config` identity marker, managed clone path, and consent UX.
- Compatibility duration for legacy `bb tm`, aliases, and sessionizer fallback.
- Whether deferred secrets and typed environment presets ever belong in `bb`.
- Long-term release signing policy beyond GitHub provenance attestations.

Each deferred item that breaks a command/data contract, stores credentials,
mutates MCP, or requires legacy-repository writes is a decision gate, not an
implicit implementation detail.

## 2026-08-10 — Apply owns only bb state

Decision: sessionizer apply is permitted because it never writes the legacy
source. It verifies the bytes observed during check, stores a content-addressed
recovery copy, and atomically changes only bb's XDG registry.

Why binbox owns it: the destination registry and recovery journal are bb state.
Why Orca does not: no worktree or agent lifecycle changes. Why LazyVim does not:
the shared source remains untouched and available to its fallback parser.

## 2026-08-10 — Local-only LazyVim setup

Decision: `bb setup nvim` validates and links a user-selected local checkout
only after explicit apply and consent. It does not clone, install packages,
restore plugins, or overwrite an existing target. `doctor nvim --headless`
uses `-u NONE` to avoid config-triggered network activity.

Reason: this completes the safe install/link contract without taking ownership
of LazyVim content or crossing the credential/network decision boundary.

## 2026-08-10 — Contract v1 and check-first migration

Decision: machine-readable commands use envelope schema v1 and stable project/
session IDs. Registry mutations use process-safe locks and atomic replacement.
The legacy adapter defaults to explicit `--check`. Its separately requested
`--apply` mode imports only non-conflicting records into bb-owned XDG state,
after preserving the source bytes for recovery; it never changes the source.

Why binbox owns it: these are bb's public API and bb-owned XDG state. Why Orca
does not: no lifecycle object is created or controlled. Why LazyVim does not:
it remains a consumer of the shared sessionizer compatibility grammar, while
the source file stays machine-local and untouched.
