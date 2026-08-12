# Internal implementation guide

## Purpose and status

This is the maintainer entry point for the Go implementation of `bb` at
v0.10.0. It explains where behavior lives and the contracts a code change must
preserve. Product ownership and non-goals remain authoritative in
[architecture.md](architecture.md); user-facing syntax remains authoritative in
[commands.md](commands.md) and `bb <command> --help`.

## Request lifecycle

Every invocation follows the same small path:

```text
cmd/bb/main.go
  -> bb.New(stdout, stderr, environment)
  -> App.Run(arguments)
  -> App.dispatch(arguments)
  -> domain handler
  -> human renderer, JSON envelope, or direct owner-CLI stream
  -> CommandError -> documented process exit code
```

`cmd/bb/main.go` only wires process streams and exit handling. `App` stores its
input, output, environment snapshot, clock, command factory, terminal detector,
and password reader as replaceable fields so tests can exercise behavior without
modifying the host system. Command routing is centralized in `App.dispatch`.

## Source map

| File or area | Responsibility |
|---|---|
| `cmd/bb/main.go` | Process entry point, stderr fallback, exit status |
| `internal/bb/app.go` | Application wiring, top-level dispatch, projects, and doctor |
| `internal/bb/mcp.go` | MCP registry CRUD/TUI, client synchronization, checks, and metadata-only audit |
| `internal/bb/contract.go` | Schema-v1 envelope, structured errors, flags, and exit codes |
| `internal/bb/human.go` | Default labels, nested sections, tables, ordering, and terminal-safe scalar rendering |
| `internal/bb/storage.go` | Owner-only file locking, synced temporary writes, and atomic replacement |
| `internal/bb/select.go` | Bubble Tea search selector and deterministic plain fallback |
| `internal/bb/confirm.go` | Responsive, default-cancel mutation confirmation |
| `internal/bb/completion.go` | Native zsh definition and safe dynamic candidates |
| `internal/bb/shell.go` | Checkout-independent current-shell wrappers |
| `internal/bb/tm.go`, `sessionizer.go` | Project/session selection, tmux commands, and legacy project import |
| `internal/bb/profile.go`, `assume.go`, `wenv.go`, `sec.go` | AWS config, scoped credentials, declarative environments, and encrypted secrets |
| `internal/bb/external.go`, `legacy_read.go` | Direct-argv Git, Kubernetes, SSM, and local inspection adapters |
| `internal/bb/tfx.go`, `tvx.go` | Guarded Terraform and fixed-policy Trivy workflows |
| `internal/bb/nvim*.go` | LazyVim config validation, planning, linking, and doctor checks |
| `internal/releasearchive` | Deterministic archives and checksum manifests |

The package is intentionally flat. Add behavior beside its owning command
instead of introducing a framework layer unless more than one real command
already needs the same invariant.

## Output and error contract

### Streams

| Stream | Allowed content | Must not contain |
|---|---|---|
| stdout | Human result, schema-v1 JSON, explicit export, shell code, or an external owner CLI's documented stream | TUI frames, prompts, progress messages |
| stderr | TUI, confirmations, previews, diagnostics, and owner CLI stderr | Secret values or credential-bearing exports |

This separation is functional, not cosmetic: zsh integration evaluates stdout
only after a successful `wenv` or `assume` command. Interactive UI must continue
to use stderr.

### Human and JSON modes

bb-owned structured reads use `printHuman` by default. `--json` returns exactly
one schema-v1 envelope:

```json
{"schema_version":1,"ok":true,"data":{},"warnings":[],"error":null}
```

On failure, `ok` is false, `data` is null, and `error` contains a stable code
and message. Explicit JSON exports and passthrough streams owned by Terraform,
Trivy, kubectl, AWS CLI, Git, or another provider are not automatically wrapped.
When adding `--json`, update `jsonRequested` if the flag position is special.

### Exit codes

| Code | Meaning | Constructor or path |
|---|---|---|
| `0` | Success | Handler returns nil |
| `1` | Operational failure | Ordinary error or `CommandError` with operational status |
| `2` | Invalid invocation | `invalid` or `usage` |
| `3` | Required capability unavailable | `unavailable` |

`App.Run` prints a JSON error envelope when JSON mode was requested. The process
entry point prints an unreported text error to stderr and exits through
`bb.ExitCode`.

## State and persistence

The default roots are `${XDG_CONFIG_HOME:-platform config}/bb` and
`${XDG_STATE_HOME:-$HOME/.local/state}/bb`.

| Data | Default location | Ownership and write rule |
|---|---|---|
| Projects | config `projects.json` | bb-owned; locked atomic JSON, mode `0600` |
| Wenv presets | config `wenv.json` | bb-owned; declarative values only |
| MCP servers | config `mcp.json` | bb-owned; locked atomic metadata and environment names only; no secret values |
| Migration recovery | state `migration-backups/` | Content-addressed legacy source copy and recovery metadata |
| AWS profile config | `~/.aws/config` | AWS CLI-owned format; bb creates state backups before atomic writes |
| Secret store/key | `${BINBOX_SECRETS_FILE}` / `${BINBOX_AGE_KEY}`, defaulting under `~/.config/binbox` | Existing age-compatible format; locked ciphertext replacement and state backup |
| Terraform safety session | `${XDG_STATE_HOME:-$HOME/.local/state}/binbox/tfsession` | Deliberate legacy-compatible TSV contract |
| Terraform plan/review scratch | fresh owner-only directories below bb state | Private snapshots and review artifacts, removed or retained only by the documented workflow |

Mutating JSON and ciphertext paths must hold the sibling lock across the
read-check-write sequence. Writes use a `0600` temporary file, `fsync`, atomic
rename, and directory sync. A command must not infer ownership of an external
resource from a local record; it re-observes the exact tmux session, process,
Terraform identity, ref, or source before mutation.

## Interactive UI contract

`selectOne` chooses Bubble Tea only when both input and stderr are usable
terminals and `BB_SELECTOR` does not force plain mode. Otherwise it uses a
numbered prompt that accepts an index or exact name. Both paths return the
stable `Value`, never the display label.

The shared selector contract is:

- typing filters immediately and may search safe metadata;
- arrows and Ctrl+N/P move from the first press;
- Escape clears a query before it cancels the selection;
- Ctrl+C cancels immediately;
- terminal resize may hide metadata and borders but must preserve selection,
  result count, and cancellation semantics;
- `NO_COLOR` removes color without removing semantic markers;
- labels, descriptions, questions, and copied query text pass through the
  terminal-text sanitizer;
- TUI output stays on stderr and selected values do not leak through stdout.

Confirmations select Cancel by default and accept an explicit `y` shortcut.
Destructive handlers must still revalidate after confirmation; the visual card
is not a concurrency or identity safeguard by itself.

## Secrets, credentials, and completion

Secret plaintext may exist only in memory, the age process pipe, an explicitly
requested stdout read/export, clipboard delivery, or one child environment.
It must never enter selector metadata, completion candidates, confirmations,
diagnostics or tests outside synthetic fixtures. Field rename
moves the in-memory value and rewrites ciphertext without displaying it.

AWS CLI owns SSO login, credential resolution, and caches. `assume` does not
persist credentials. It refuses credential-bearing shell text on a terminal;
the generated zsh wrapper evaluates successful output, while `assume exec`
scopes values to one child.

Native completion intentionally omits Git-facing commands. Dynamic completion
may read local names and IDs from bb state, AWS config, and encrypted secret
metadata, but it must not return secret values, AWS credentials, terminal
control characters, or duplicates. Completion must call the installed `bb`
binary and remain independent of a repository checkout.

MCP synchronization invokes `claude mcp` or `codex mcp` with a direct argument
vector. The registry may contain required environment-variable names and a
bearer-token environment-variable name, never their values. HTTP connections
remain direct from the owner client; stdio processes remain client-owned.

## External command boundary

Adapters validate structured identifiers and execute an owner CLI with a direct
argument vector. Do not construct a shell command string or pass provider
credentials into bb storage. Mutations follow this sequence:

1. Resolve and validate the exact target.
2. Observe its current identity and state.
3. Show a terminal-safe preview and request default-cancel confirmation when
   interactive confirmation is part of the command contract.
4. Re-observe the same identity after confirmation.
5. Invoke the owner CLI directly and propagate its result.

Terraform adds an immutable private plan snapshot and account/scope checks to
this sequence. Local port and tmux mutations similarly compare exact observed
targets before acting.

## Adding or changing a command

1. Put the handler in the file that owns the domain and route it from
   `App.dispatch` only when it is a new top-level command.
2. Define help and usage before parsing mutations. Use `invalid`, `usage`, and
   `unavailable` consistently.
3. Decide whether output is bb-owned structured data, shell output, an explicit
   export, or an external passthrough; then preserve the stream contract.
4. For bb-owned structured output, add human default and `--json` envelope tests.
5. Reuse `selectOne`, `confirmAction`, direct-argv execution, and locked atomic
   storage instead of creating command-specific variants.
6. Add dynamic completion only for safe local metadata; add a regression test
   proving sensitive values cannot appear.
7. Add targeted tests for cancellation, unavailable dependencies, invalid
   input, output separation, and the mutation re-observation boundary.
8. Update command help, [commands.md](commands.md), relevant architecture or
   operations docs, and the `Unreleased` changelog section.

## Verification and release

Run the narrowest changed-package test first, then the complete repository
checks:

```sh
go test ./...
go test -race ./...
go vet ./...
scripts/test-install.sh
scripts/test-release-guard.sh
git diff --check
```

Interactive changes also require a real zsh/tmux PTY smoke record covering
resize, cancellation, stdout/stderr separation, and terminal cleanup. Secret or
Terraform changes require focused safety tests for plaintext exclusion,
re-observation, snapshots, cleanup, and failure paths.

Releases are created from a clean commit with an annotated `v<version>` tag.
`scripts/release.sh` builds deterministic CGO-disabled archives for Linux and
macOS on amd64 and arm64, emits and verifies SHA-256 checksums, and embeds
version, commit, and build time. The GitHub workflow adds an SPDX SBOM and, for
public repositories, build provenance. See [operations.md](operations.md) for
the installer, release guard, recovery, and rollback procedures.
