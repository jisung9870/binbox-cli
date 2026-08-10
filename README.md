# binbox-next

`binbox-next` is the prototype of the installable, single-binary `bb` CLI. It
consolidates stateful local workspace behavior in Go while leaving live agent,
worktree, scheduler, and DAG ownership with Orca and editor configuration with
the separate `lazyvim-config` repository.

```sh
go build -o ./bin/bb ./cmd/bb
./bin/bb help
go test ./...
```

Current operational flows:

```sh
# Inspect or apply the legacy project source without changing it.
bb project import sessionizer --check --json
bb project import sessionizer --apply --json

# Inspect bb-owned execution records and produce a non-executing open plan.
bb run list --json
bb session open prj_... --backend shell --json

# Validate and explicitly link an already-present LazyVim config.
bb setup nvim --config-dir /path/to/lazyvim-config --dry-run --json
bb setup nvim --config-dir /path/to/lazyvim-config --apply --consent --json
bb doctor nvim --config-dir /path/to/lazyvim-config --json
```

The MVP stores configuration under `$XDG_CONFIG_HOME/bb` and state/journals
under `$XDG_STATE_HOME/bb` (with standard home-directory fallbacks). It does
not require `BB_ROOT`, a `libexec` tree, or helper scripts on `PATH`.

Machine-readable reads accept `--json` and return the schema-v1 envelope:

```json
{"schema_version":1,"ok":true,"data":{},"warnings":[],"error":null}
```

The compatibility importer is check-first and supports an explicit apply mode:

```sh
bb project import sessionizer --check --json
bb project import sessionizer --check --file /path/to/dirs --json
bb project import sessionizer --apply --file /path/to/dirs --json
```

It understands the shared `#` comment, `~` expansion, parent-root, and
`=direct-directory` grammar. It reports dead paths, duplicates, and registry
collisions. `--apply` writes only bb's XDG registry and a byte-identical,
content-addressed recovery copy; the legacy source remains untouched.

Release assets are produced with `scripts/release.sh`. The verified installer
defaults to `~/.local/bin`, never uses sudo, and requires explicit `--force` or
`--migrate` for protected existing targets. See [operations](docs/operations.md).

See [architecture](docs/architecture.md), [migration plan](docs/migration-plan.md),
and [decision log](docs/decision-log.md). The legacy and installer evidence is
preserved under `research/`.
