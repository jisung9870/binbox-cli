# Operations

## Release

Tags matching `v*` and an explicit workflow dispatch build four assets:
Linux/macOS on amd64/arm64. The build uses `CGO_ENABLED=0`, trimmed paths, no
VCS-derived variance, a deterministic source timestamp, and linker-injected
version, commit, and build time. Release output includes SHA-256 checksums and
an SPDX SBOM. GitHub build provenance attestation is also emitted when the
repository is public; GitHub does not support attestations for user-owned
private repositories.

Local verification:

```sh
VERSION=0.1.0 COMMIT=$(git rev-parse HEAD) \
  SOURCE_DATE_EPOCH=$(git show -s --format=%ct HEAD) scripts/release.sh
(cd dist && sha256sum -c checksums.txt)
```

## Install and recovery

```sh
scripts/install.sh --version 0.1.0 --dry-run
scripts/install.sh --version 0.1.0
```

The installer selects the host OS/architecture, downloads the archive and
checksum manifest, verifies SHA-256 and the staged binary's reported version,
then uses a same-directory rename. An already-installed matching version is a
no-op. Regular files and foreign links require `--force`; a link into a Git
checkout requires `--migrate`. The checkout is never deleted. Replacements get
a unique backup, and failed post-install validation restores it when possible.

Installer contract tests use only `.tmp/` under this repository:

```sh
scripts/test-install.sh
```

## Sessionizer migration

`--check` never writes. `--apply` rechecks the source hash, writes an owner-only
content-addressed copy under `$XDG_STATE_HOME/bb/migration-backups`, then
atomically updates only `$XDG_CONFIG_HOME/bb/projects.json`. Repeating the same
apply adds no duplicate project. Identity collisions are reported and skipped.

Recovery metadata names the exact source digest and backup. Restoring the bb
registry is a manual, explicit operation; the legacy source itself never needs
restoration because bb does not modify it.

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

The new repository is ready to produce release candidates, but setup/workbench
retirement is intentionally not automatic. It requires writing consumer/fallback
changes in the existing binbox or LazyVim repositories and observing at least
one compatibility window. That crosses the project's explicit stop boundary and
must be separately authorized after a published candidate is exercised.
