# Decision log

## 2026-08-11 — Plan review is a read-only view over the review parser

Decision: `bb tfx browse` renders an existing plan for reading. It runs
`terraform show -json` and nothing else, and it shares `tfx review`'s parser,
changed-path definition, and rule evaluation instead of introducing a second
interpretation of what a plan says.

Why a separate command rather than a flag on `sum` or `review`: `sum` delegates
to `tf-summarize` and `review` is an automation pipeline that writes artifacts.
Reading one plan interactively is neither, and giving it its own name keeps the
mutation-free boundary obvious from the command line.

Why the values are placeholders: a plan carries secrets. The renderer resolves
`before_sensitive`, `after_sensitive`, and `after_unknown` before a value can
reach a label, a description, or search metadata, and a block marked sensitive
covers every path inside it. This is the existing rule that previews must not
expose secrets, applied to plan values.

Why no prompt without a terminal: browsing produces no value to return, so a
numbered prompt would ask a question whose answer is discarded. Without a usable
terminal the same reading is written as a table on stdout, and `--json` returns
the schema-v1 envelope.

What this does not change: apply, destroy, and their account-bound session
safeguards are not on this path, and no state, plan file, or artifact is written.

## 2026-08-11 — Staged selection is one program

Decision: a multi-level selection runs inside a single Bubble Tea program that
holds a stack of levels. Entering a value pushes the next level, Escape pops back
to the previous one, and the alternate screen is entered once for the whole walk.
Commands describe the level graph as a function from the values chosen so far to
the next level; `bb sec` is the first caller.

Why: the Service -> Field -> Action contract was already documented, but it was
implemented as three nested loops that each started and tore down their own
alternate-screen program. The contract held only because the loops reproduced it
by hand, and every level change was a full program restart.

What this changes for the operator: returning to a level restores the query and
cursor that were active there instead of resetting them, because the level was
never destroyed. Cancelling the new-field prompt after choosing Rename now ends
the command rather than returning to the action list; selection and mutation stay
separated, and one invocation still performs at most one action.

What this does not change: Escape still clears the query before navigating,
Ctrl+C still exits immediately, selection still performs no mutation, the plain
and `BB_SELECTOR=plain` walks remain deterministic and now share the same level
graph, and UI still renders only on stderr.

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

At this stage credential-bearing `assume` was not reproduced: AWS CLI continued to own SSO
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
environment-selection forms of `bb wenv` and `bb assume`; management commands
are never evaluated. Shell startup may evaluate this generated output but does
not source the legacy binbox repository.

Why binbox owns it: applying environment variables to a parent shell requires a
small shell boundary, and generating it from the installed binary keeps that
boundary versioned with the CLI. Why Orca does not: no agent or worktree
lifecycle is involved. Why LazyVim does not: this is terminal shell behavior,
not editor configuration.

## 2026-08-11 — AWS CLI-owned assume and confirmed wenv apply

Decision: `bb assume` restores profile selection, current-shell application,
unset, current identity, and scoped exec by delegating credential resolution to
`aws configure export-credentials`. bb does not parse SSO cache files, call SSO
role APIs directly, or keep a credential cache. Credential-bearing stdout is
refused when attached directly to a terminal and is captured only by the
generated shell wrapper; `assume exec` keeps credentials in one child process.

`bb wenv show` is inspection-only. `bb wenv apply` renders the current-to-target
environment diff on stderr and emits eval-safe stdout only after confirmation.
Legacy implicit `kubectl config` mutation remains retired: `KUBE_CONTEXT` and
`KUBE_NAMESPACE` are declarative environment values.

## 2026-08-11 — Search-first shared TUI

Decision: bb owns one responsive Bubble Tea selection surface shared by project,
tmux session, AWS profile, wenv, and secret service/field selection. Printable
input filters immediately without a `/` mode switch; the first navigation key
moves; results show safe command-specific metadata, counts, empty states, and a
stable selected value. Rendering stays on stderr and `NO_COLOR`, non-TTY, dumb
terminal, and numbered fallbacks remain supported.

Non-Git yes/no mutation gates share a compact confirmation card whose initial
selection is Cancel. Existing target previews, immutable Terraform snapshots,
provider re-observation, and the typed Terraform account challenge remain the
security authority; the TUI changes presentation, not the mutation threshold.
Git-related CLI behavior remains outside this rollout.

## 2026-08-11 — Frequent secret use stays scoped and metadata-only

Decision: invoking `bb sec` opens a two-stage manager built from the shared
selector: choose service/field metadata, then choose Copy, Replace field, Remove
field, or Remove service. Copy is the first safe action. Replace and removal
remain default-Cancel gates, and hidden entry begins only after overwrite
approval. Secret values never appear in TUI content.

`bb sec exec <service> -- <command>` overlays normalized service/field variables
on one child process without printing export statements or changing the parent
environment. Piped replacement requires `--force`; interactive replacement
requires confirmation. The existing local age key/ciphertext format remains the
only storage backend.

## 2026-08-11 — Secrets use scoped hierarchy and atomic field rename

Decision: `bb sec` navigates Service→Field→Action. The service screen shows only
service names and field counts; entering a service reveals its sorted field
names. Escape returns Action→Field→Service and exits only from the service
screen, while Ctrl+C exits immediately. This supersedes the earlier flat
`service / field` list because repeated service names made frequent use harder
to scan.

Rename field is available in the action screen and as
`bb sec rename <service> <field> <new-field>`. It validates the unused target,
defaults to Cancel, moves the existing value in memory, and performs the same
locked atomic ciphertext replacement and backup as other secret mutations.
Neither the old value nor the new value is rendered or journaled.

## 2026-08-11 — Native zsh completion and human-first output

Decision: `bb shell init zsh` registers native `compdef` completion in addition
to its existing wenv/assume wrapper. `bb completion zsh` exposes the completion
independently. Static candidates cover supported non-Git commands and options;
dynamic candidates read only local project/session, tmux, wenv, AWS profile,
and secret service/field names. Secret values and AWS credentials never enter
the candidate protocol. Git and gx candidates remain excluded.

bb-owned structured reads render deterministic labels, nested sections, or
tables by default. `--json` remains the explicit automation format and retains
the schema-v1 envelope. JSON artifact export and external provider streams keep
their purpose-specific formats. Human rendering strips terminal controls before
printing and does not change stored data or machine output.
