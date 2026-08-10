# Installer and test review

## Scope and evidence

This review is based on a read-only inspection of `/home/ubuntu/setup/binbox`,
`/home/ubuntu/setup/nvim`, `/home/ubuntu/setup`, and the Workbench contract
documents.  No files in those repositories were changed.  A compile-free
syntax check passed for the binbox entrypoint/setup/doctor scripts, the nvim
setup/doctor/test scripts, and the setup aggregate doctor/contract test.

## Recommended release and install contract

`bb` should become one versioned, self-contained executable distributed as a
GitHub Release asset for at least `darwin-arm64`, `darwin-amd64`,
`linux-amd64`, and `linux-arm64`.  Publish `checksums.txt` (SHA-256), an
SBOM/provenance artifact where supported, and a short bootstrap script which
selects the OS/architecture, verifies the checksum before replacement, writes
to `~/.local/bin/bb` atomically, and never needs `sudo`.  The installer must
support `--version`, `--install-dir`, `--dry-run`, and an explicit
`--force`; it must preserve a non-bb regular file, leave the previous binary
in place on download/verification failure, and make `bb version` identify the
release version, commit, platform, and build time.

This replaces the current source checkout model, not the user's configuration:
today `bb setup` makes `~/.local/bin/bb` a symlink into a mutable checkout and
adds a managed shell block that sources files from that checkout.  A single
binary can own command dispatch, completion installation, and shell hooks, but
it should not silently clone, pull, or mutate configuration repositories.
`bb upgrade` should be retained as a compatibility command during migration
and then become an alias to the verified binary updater, with a clear error if
the executable is still a source-checkout shim.

### Proposed install states

| State | Required behavior |
|---|---|
| Fresh install | Create the binary atomically, ensure `~/.local/bin` is on PATH, then offer `bb setup shell` as a separate explicit action. |
| Existing managed binary | Verify/update atomically; retain the prior binary until the new one passes `bb version`. |
| Existing source checkout link | Detect it, explain the migration, and replace only after `--migrate`/confirmation; never delete the checkout. |
| Foreign file or link | Fail without mutation unless `--force`; report the exact path and backup/recovery action. |
| Offline/corrupt asset | Fail closed before replacement and emit a machine-readable error code. |

## `bb setup nvim` and the separate LazyVim contract

`bb setup nvim` should be an orchestrator and verifier, not an embedded copy of
LazyVim.  The LazyVim configuration remains a separately versioned
`lazyvim-config` checkout; its install/link contract must therefore be an
explicit input: either `--config-dir <path>` (must contain the expected
configuration identity) or `--repo <url> --ref <immutable-release-or-commit>`.
The command may clone to a documented managed location only with an explicit
flag, and must never overwrite an existing `~/.config/nvim` directory.

The contract should distinguish these independently observable operations:

1. **Acquire:** verify the selected LazyVim config revision and its
   `lazy-lock.json`; no Neovim user path changes.
2. **Install prerequisites:** platform-specific package/runtime work, with a
   complete dry-run plan and no implied link creation.
3. **Link:** create `XDG_CONFIG_HOME/nvim` -> selected config directory after
   validating target identity, backing up a conflicting user path only after
   explicit consent.
4. **Validate:** run `bb doctor nvim` and a headless startup check; plugin
   restoration remains an explicit, networked step.

This matches the useful separation already visible in the nvim scripts:
`--install` and `--link` are distinct, while both macOS and WSL link the
repository to `~/.config/nvim`.  The new interface must preserve
`XDG_CONFIG_HOME`, support macOS and WSL/Linux, report the selected config
path/revision in JSON, and keep machine-local `local.lua`, tmux configuration,
and sessionizer data outside the release binary.  It must also preserve the
existing warning that the `tmux-sessionizer/dirs` format is shared with the
nvim configuration; changing it is a coordinated LazyVim release, not a bb
installer change.

## Doctor contract

Use one stable envelope for `bb doctor` and subprofiles:

```json
{"schema_version":2,"ok":false,"data":{"profile":"nvim","checks":[]},"warnings":[],"error":null}
```

Each check needs a stable `id`, `scope` (`core`, `optional`, `disabled`),
`status` (`pass`, `warn`, `fail`, `skip`), observed value, expected value, and
actionable recovery text.  `bb doctor` should be read-only; `--strict` should
turn warnings that block a requested profile into a nonzero result, while
`--json` must emit exactly one document on stdout.  Exit codes should be:
`0` healthy, `1` required checks failed, `2` invalid request, and `3`
capability unavailable.

`bb doctor nvim` must test executable version, chosen config path/revision,
the resolved `XDG_CONFIG_HOME/nvim` symlink and target, lockfile presence,
required runtime/tools, and headless startup.  It should identify optional
integrations separately.  This is intentionally compatible with the existing
nvim doctor, which already checks the deployment symlink, lockfile, and
headless startup, and with binbox doctor, which already provides a versioned
JSON envelope.  Do not merge it with Workbench doctor: Workbench's documented
doctor owns Workbench core/provider state and is explicitly read-only; its
compatibility observations are a migration signal, not an installer owner.

## Migration test plan

Run the following in isolated temporary HOME/XDG directories on Linux and
macOS, with a WSL job for WSL-specific integrations.  Tests must stub network,
PATH, OS detection, download responses, and filesystem failures; no test may
use a developer's real shell rc or nvim config.

| Area | Cases |
|---|---|
| Release installer | Every supported target selection; checksum/signature failure; interrupted download; atomic replacement rollback; file permissions; idempotent same-version install; downgrade policy; foreign file/link refusal; source-symlink migration. |
| Shell setup | zsh/bash and XDG paths; one managed block only; malformed-marker refusal; source checkout removed after binary migration; PATH shadowing diagnostics; completion install/removal. |
| Nvim acquire/link | Existing config directory/link/foreign link; correct and incorrect config identity; `--config-dir`; immutable ref; dry-run has zero mutations; backup/restore; idempotent rerun; XDG override; macOS/WSL platform branches. |
| Doctor | Text and one-document JSON; schema compatibility; all status/exit-code combinations; strict behavior; optional/disabled checks; link mismatch; missing lockfile; unavailable `nvim`; headless startup failure. |
| Compatibility | Existing `bb setup`, `bb doctor`, shell aliases, noninteractive `bb <tool>`, and `bb upgrade` behavior through the announced deprecation window. |
| End-to-end | Fresh machine -> binary -> shell -> separately acquired config -> link -> doctor; upgrade each component independently; rollback binary/config link; source-checkout-to-release migration. |

Use Bats for filesystem and shell contracts, Go unit/integration tests for
release selection, download verification, and JSON contracts, and an ephemeral
container/VM matrix for platform evidence.  Add golden JSON fixtures and a
negative test for every recovery/error code before treating that code as public.

## Acceptance criteria for retiring setup/workbench

Retire the old setup/workbench path only when all of the following are true:

- A signed/checksummed single-binary release is reproducibly built and tested
  on every supported platform; fresh install, upgrade, and rollback pass.
- `bb setup nvim` has a documented separate-config acquire/link interface and
  passes the isolated migration matrix without touching an unapproved user
  file.
- Doctor v2 has stable JSON/version/exit-code semantics, covers bb and nvim
  prerequisites, and reports a clear remediation for every failed required
  check.
- At least one release window demonstrates that source-checkout users can
  migrate with no lost checkout, shell customization, config, or local data;
  legacy commands warn but remain supported during that window.
- Workbench remains limited to its long-lived personal objects and observed
  references, while Orca owns worktree, terminal, agent, Run/Task/Dispatch
  lifecycle.  No new installer behavior depends on Workbench's legacy runtime
  ownership or dashboard actions.
- CI includes the migration/negative tests above and release artifacts are
  independently verified from a clean environment.  Retire only after a
  documented rollback period and telemetry/user evidence show no active
  legacy-only dependency.

## Ownership rationale

The release binary owns installation of itself, its shell integration, and its
own doctor schema because those are version-coupled, portable capabilities.
LazyVim owns its configuration, plugin lockfile, and configuration-specific
health because it is separately versioned and has platform/config-local state.
Orca owns live worktree/terminal/agent orchestration; Workbench may retain
personal data and observe references, but must not regain runtime ownership.
The top-level setup repository is a transitional composition layer only: its
current manifest correctly expresses ordering, but that ordering is not a
reason to make a release binary mutate every repository.

## Independent findings and patch proposals

1. **No release path exists in the inspected binbox project.**  Its Makefile
   only exposes check/doctor/test/install, and CI runs shellcheck plus Bats on
   Ubuntu/macOS.  Add a Go-based release target (or equivalent reproducible
   packaging pipeline), release CI, signed/checksummed assets, and installer
   integration tests before claiming a single-binary install.
2. **Current `bb setup` is intentionally checkout-coupled.**  It chmods files,
   symlinks `~/.local/bin/bb` to `<checkout>/bb`, and writes source lines into
   shell rc files.  Replace this with a binary-owned setup implementation and
   a guarded `--migrate` path; do not reuse the current behavior as the
   release installer.
3. **The current nvim setup is already usefully split but its identity contract
   is implicit.**  Both platform scripts link the repository path directly to
   `~/.config/nvim`; add explicit config identity/revision validation and
   `--config-dir`/managed-clone selection before exposing it via bb.
4. **Doctor behavior is fragmented.**  Binbox offers JSON but no strict
   profile model; nvim has deployment/startup validation but no common JSON
   envelope; setup doctor aggregates repository state and Workbench state.
   Implement a bb-owned v2 doctor adapter first, preserving each component's
   read-only semantics and avoiding a premature merge with Workbench doctor.
5. **Existing tests provide a good shell baseline but not migration evidence.**
   Binbox Bats covers symlink, marker, idempotency, and conflict cases; nvim's
   test script covers syntax, help, dry-run, sync dry-run, and symlink helpers.
   Add isolated HOME/XDG, artifact-integrity, release-upgrade, and config-link
   migration cases before deprecation.
