# Neovim integration boundary

`bb` does not contain, clone, mutate, or restore the separately versioned
LazyVim configuration. It accepts an already present config directory and
validates its `init.lua`, parseable `lazy-lock.json`, local Git revision, origin
URL, and lockfile SHA-256 against an explicit identity where supplied.

The proposed `app.go` hook is intentionally tiny: parse `bb setup nvim` flags
into `NvimSetupRequest`, call `PlanNvimSetup` for normal and dry-run output, and
call `ApplyNvimSetup` only when both `--apply` and `--consent` are present.
`bb doctor nvim [--headless]` should translate flags into `NvimDoctorOptions`
and print `DoctorNvim`'s report. It should not reuse the broad existing `doctor`
command's package checks as proof that an editor config is healthy.

Target conflict handling is deliberately conservative. A missing
`$XDG_CONFIG_HOME/nvim` can be linked, and an existing desired link is an
idempotent success. Files, directories, broken links, and other links are
reported without changes. If a future UI lets the user migrate a target, it
must first call `BackupNvimTarget` with a specific unused backup path and
persist the returned pair. `NvimBackup.Restore` refuses to overwrite a
newly-created target and moves only that recorded backup back, making rollback
explicit and recoverable.

The optional headless probe invokes `nvim --headless -u NONE +qa` only after executable,
identity, lockfile, and desired-link checks pass. It performs no `Lazy` sync,
clone, network access, or package installation.
