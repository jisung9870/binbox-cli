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

- Whether `lazyvim-config` ever needs a managed clone path; local path,
  repository, revision, lockfile, and explicit link consent are implemented.
- Compatibility duration for the legacy sessionizer fallback.
- Whether the retained Workbench repository contains personal data that needs
  an explicit export before that repository is archived. Its LazyVim UI is
  retired; Orca remains the lifecycle interface.

## 2026-08-11 — LazyVim Workbench UI retired

Decision: LazyVim keeps only the asynchronous `bb tm projects --json` consumer
and a read-only sessionizer fallback. The Workbench project alias and its
agent/worktree/doctor commands are removed without replacement in bb or
LazyVim.

Why binbox owns project inventory: it is portable local workspace metadata.
Why Orca owns lifecycle: agents, worktrees, terminals, schedulers, and DAGs
require one live authority. Why LazyVim owns only the picker: it presents paths
for a human editor session and does not mutate lifecycle state.

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

## 2026-08-10 — Private release trust boundary

Decision: private releases are installed through an already-authenticated
GitHub CLI session. The installer never reads or persists its token. GitHub
repository access control, that authenticated session, and TLS are the approved
trust boundary for this single-user private repository. The co-hosted SHA-256
manifest detects corruption but is not treated as an independent signature.
Public repositories additionally receive GitHub build provenance attestations.

Reason: creating a signing credential or changing repository visibility would
cross an explicit credential/ownership decision gate. Release production is
still fail-closed: it requires a clean checkout, matching commit metadata, and
an exact annotated version tag.

## 2026-08-10 — Selective legacy feature migration

Decision: migrate stable behavior, not the legacy libexec mechanism. Read-only
tmux, Git, port, and Terraform surfaces are typed Go commands that invoke
system CLIs with direct argument vectors. Terraform session status reads the
legacy `binbox/tfsession` format without rewriting or deleting it. Credential,
secret, and destructive infrastructure commands remain explicit decision gates.

Why binbox owns it: these are portable human CLI contracts and local safety
checks. Why Orca does not: none manages agents, worktrees, schedulers, or DAGs.
Why LazyVim does not: it consumes only versioned project/session inventory and
does not own Git, Terraform, or process inspection behavior.

## 2026-08-10 — Explicit targets replace legacy interactive mutation

Decision: tmux, Git, Kubernetes, AWS SSM, port termination, and Terraform
compatibility commands are typed Go adapters that pass direct argument vectors
to the system owner CLI. Mutating commands require explicit targets; destructive
ones print the target and re-observe its identity before acting. At this stage,
interactive selection used bb's dependency-free numbered selector.

Why binbox owns it: these are portable, human-invoked CLI safety contracts.
Why Orca does not: none creates or manages an agent, worktree, scheduler, DAG,
or terminal lifecycle. Why LazyVim does not: editor configuration may invoke
the commands but does not own provider state or mutation policy.

Credential-bearing `assume` is not reproduced: AWS CLI continues to own SSO
login, credentials, and cache. Executable legacy `wenv` syntax remains retired;
the declarative non-secret subset and the existing age ciphertext format are
implemented by the contracts recorded below.

## 2026-08-11 — External credential ownership and built-in selection

Decision: AWS CLI owns SSO login, credentials, and cache; `bb profile` writes
only SSO configuration. `bb sec` preserves the age key/ciphertext format and
never journals values. `wenv` never sources legacy shell and stores only
declarative non-secret variables. A built-in numbered selector initially
replaced fzf while the shell wrapper retained current-shell application.

## 2026-08-11 — Embedded fuzzy selection without fzf

Decision: interactive bb commands use Bubble Tea/Bubbles for fuzzy selection on
real terminals. Non-TTY input/output, dumb terminals, and `BB_SELECTOR=plain`
retain the numbered prompt. UI is written to stderr so JSON and shell-evaluated
stdout contracts remain unchanged. Git-facing CLI workflows are outside this
selector rollout.

## 2026-08-11 — Shell integration is generated by the binary

Decision: `bb shell init zsh` prints a checkout-independent wrapper. It invokes
the binary directly for all commands and evaluates stdout only for successful
environment-selection forms of `bb wenv`; management commands are never
evaluated. Shell startup may evaluate this generated output but does not source
the legacy binbox repository.

Why binbox owns it: applying environment variables to a parent shell requires a
small shell boundary, and generating it from the installed binary keeps that
boundary versioned with the CLI. Why Orca does not: no agent or worktree
lifecycle is involved. Why LazyVim does not: this is terminal shell behavior,
not editor configuration.
