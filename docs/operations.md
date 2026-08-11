# Operations

## Release

Tags matching `v*` and an explicit workflow dispatch build four assets:
Linux/macOS on amd64/arm64. The build uses `CGO_ENABLED=0`, trimmed paths, no
VCS-derived variance, a deterministic source timestamp, and linker-injected
version, commit, and build time. Release output includes SHA-256 checksums and
an SPDX SBOM. GitHub build provenance attestation is also emitted when the
repository is public; GitHub does not support attestations for user-owned
private repositories.

Release builds refuse a dirty checkout, a `COMMIT` other than `HEAD`, or
anything except an exact annotated `vVERSION` tag pointing at `HEAD`.
`ALLOW_UNTAGGED_BUILD=1` exists solely for local reproducibility and installer
fixture tests; it must not be used for a release.

Local verification:

```sh
ALLOW_UNTAGGED_BUILD=1 VERSION=0.5.0 COMMIT=$(git rev-parse HEAD) \
  SOURCE_DATE_EPOCH=$(git show -s --format=%ct HEAD) scripts/release.sh
go run ./cmd/releasearchive verify --manifest dist/checksums.txt
```

## Install and recovery

```sh
scripts/install.sh --version 0.7.0 --dry-run
scripts/install.sh --version 0.7.0
```

For this private repository, use the authenticated GitHub CLI mode instead of
unauthenticated release URLs:

```sh
gh auth status
scripts/install.sh --github-cli --version 0.7.0
```

This mode delegates download authorization to `gh`; the installer neither
reads nor stores a GitHub token. GitHub repository ACLs, the authenticated
`gh` session, and TLS are the private-release trust boundary. `checksums.txt`
detects accidental corruption or an incomplete download, but is not an
independent signature because it is fetched from the same release. Public
releases add GitHub build provenance attestation; private user-owned releases
cannot use that GitHub attestation service.

The installer selects the host OS/architecture, downloads the archive and
checksum manifest, verifies SHA-256 and the staged binary's reported version,
then uses a same-directory rename. An already-installed matching version is a
no-op. Regular files and foreign links require `--force`; a link into a Git
checkout requires `--migrate`. The checkout is never deleted. Replacements get
a unique backup, and failed post-install validation restores it when possible.

Installer contract tests use only `.tmp/` under this repository:

```sh
scripts/test-install.sh
scripts/test-release-guard.sh
```

## Sessionizer migration

`--check` never writes. `--apply` rechecks the source hash, writes an owner-only
content-addressed copy under `$XDG_STATE_HOME/bb/migration-backups`, then
atomically updates only `$XDG_CONFIG_HOME/bb/projects.json`. Repeating the same
apply adds no duplicate project. Identity collisions are reported and skipped.

Recovery metadata names the exact source digest and backup. Restoring the bb
registry is a manual, explicit operation; the legacy source itself never needs
restoration because bb does not modify it.

## LazyVim tm compatibility

The current LazyVim client reads registered paths through this narrow,
versioned endpoint:

```sh
bb tm projects --json
```

It returns one schema-v1 envelope with `data.projects`; records include stable
`id`, display `name`, and canonical `path`. It reads only bb's XDG registry and
does not inspect, import, or alter LazyVim/sessionizer files.

For a local human terminal, `bb tm` presents the shared search-first TUI and
opens the chosen directory through external `tmux`. Non-TTY use and
`BB_SELECTOR=plain` fall back to the numbered prompt. Use
`bb tm --project prj_...` for an explicit non-interactive choice,
including scripts and tests. This command neither contacts Orca nor records or
manages an Orca/tmux lifecycle; `tmux` owns the session it attaches or creates.

The historical `bb agents` surface is not reproduced. It returns capability
unavailable with an Orca recovery pointer because Orca remains the only agent
and worktree lifecycle owner.

## LazyVim setup

`bb setup nvim` accepts only a local, already-present config directory. It
validates `init.lua`, parseable `lazy-lock.json`, optional repository/revision/
lockfile identity, and the target under `$XDG_CONFIG_HOME`. Dry-run is the
default. Linking requires both `--apply` and `--consent`, refuses every conflict,
and treats an already-correct link as an idempotent success.

`bb doctor nvim` checks the selected config, target link, lockfile, and Neovim
availability. Optional `--headless` runs Neovim with `-u NONE`; it does not load
the selected config, bootstrap plugins, or perform network restoration.

## Remaining cutover gate

The personal-device LazyVim consumer now uses bb directly and no longer exposes
Workbench lifecycle commands. Archiving setup/workbench remains intentionally
non-automatic: repeat the data inventory and rollback checks on every machine
that has additional state, especially the company device, before archiving its
local checkout or remote repository.
