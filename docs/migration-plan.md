# Migration plan

## Disposition summary

The detailed read-only inventory is in `research/legacy-analysis.md`. In short:

| Disposition | Areas |
|---|---|
| Keep/migrate into `bb` | single-command UX, doctor, project discovery/registry, bb-owned session intent, run journal, read-only integration inventory |
| Keep outside `bb` | Orca lifecycle; LazyVim/tmux config; transitional Workbench-owned personal data until explicitly migrated |
| Defer | interactive tmux layouts, agent-pane inference, secrets, executable `wenv`, Workbench control-plane features |
| Retire/archive | libexec dispatcher, checkout-coupled setup/upgrade, authoring helpers, domain-specific shell glue unless separately justified |

## Phases

1. **MVP isolation.** Ship the Go binary beside the legacy checkout. Validate
   empty XDG directories, owner-only files, journal redaction, help/minimum
   behavior, and missing optional tools. Do not change aliases or source data.
2. **Freeze public data contracts (contract v1 implemented).** The schema-v1
   JSON envelope, structured error classes, exit codes, stable IDs, and atomic
   locked state writes now form the test-backed baseline. Backward readers are
   still required before any future persisted-record shape changes.
3. **Project compatibility (check/apply implemented).** The non-evaluating
   sessionizer importer compares canonical IDs/paths, duplicates, stale paths,
   and registry collisions. Apply is idempotent, verifies source continuity,
   preserves exact recovery bytes, and writes only bb's registry/state.
4. **Observed session/run migration.** Create ownership only for actions begun
   by `bb`. Existing tmux/Workbench/Orca objects remain external observations.
   Explicit backends never silently fall back.
5. **Binary release migration (automation implemented).** Publish checksummed artifacts and an atomic
   installer. Detect the old checkout symlink, explain the transition, require
   `--migrate`, and never delete the source checkout automatically.
6. **LazyVim contract (local setup/doctor implemented).** `bb setup nvim` accepts a separately versioned
   `lazyvim-config` path or immutable revision. Validate identity, XDG link
   target, lockfile, prerequisites, and headless startup without embedding the
   configuration.
7. **Retirement (decision gate).** Archive setup/workbench functionality only after the criteria
   below pass for a documented compatibility and rollback window.

## Compatibility stop points

Implementation pauses for an explicit decision before changing a persisted
legacy format/public command, storing credentials, mutating MCP configuration,
or requiring a write to legacy binbox/LazyVim. Until approved, adapters are
read-only/check-first and source data is preserved.

## Test and release evidence

- Unit tests with isolated `HOME`, `XDG_CONFIG_HOME`, and `XDG_STATE_HOME`.
- Golden JSON and negative fixtures for schema, exit codes, malformed/trailing
  input, duplicate IDs, unavailable integrations, and redaction.
- Installer matrix for supported OS/architectures, checksum failure, rollback,
  idempotence, downgrade policy, source-symlink migration, and foreign targets.
- Nvim matrix for path/ref selection, identity mismatch, dry-run zero mutation,
  conflict/backup/restore, XDG overrides, lockfile, and headless startup.
- End-to-end fresh install and independent binary/config upgrades in ephemeral
  environments; never use a developer's real rc/config paths.

## Acceptance criteria for retiring setup/workbench

- Reproducible checksummed releases pass fresh install, upgrade, downgrade
  policy, rollback, and all supported-platform tests.
- A source-checkout user migrates without deletion or loss of shell customization,
  config, sessionizer data, or recovery path.
- Doctor has stable versioned JSON/exit semantics and actionable recovery for
  every required failure while optional tools remain clearly optional.
- `bb setup nvim` uses the separate config identity/revision contract and does
  not touch an unapproved existing path.
- LazyVim consumes the new contract asynchronously with schema checks and a
  tested fallback during the compatibility window.
- MCP audit exports no secret/config content and never mutates, proxies, or
  installs; Orca remains the sole live lifecycle owner.
- CI contains negative migration tests, and a documented rollback window plus
  user/usage evidence shows no remaining legacy-only dependency.

Workbench may then be archived after any intentionally retained personal data
has an explicit owner/export. The top-level setup repository may be retired
when every surviving repository has an independent install and doctor story.
