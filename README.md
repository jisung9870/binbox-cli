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

The MVP stores configuration under `$XDG_CONFIG_HOME/bb` and state/journals
under `$XDG_STATE_HOME/bb` (with standard home-directory fallbacks). It does
not require `BB_ROOT`, a `libexec` tree, or helper scripts on `PATH`.

Machine-readable reads accept `--json` and return the schema-v1 envelope:

```json
{"schema_version":1,"ok":true,"data":{},"warnings":[],"error":null}
```

The first compatibility importer is deliberately check-only:

```sh
bb project import sessionizer --check --json
bb project import sessionizer --check --file /path/to/dirs --json
```

It understands the shared `#` comment, `~` expansion, parent-root, and
`=direct-directory` grammar. It reports dead paths, duplicates, and registry
collisions without changing either file.

See [architecture](docs/architecture.md), [migration plan](docs/migration-plan.md),
and [decision log](docs/decision-log.md). The legacy and installer evidence is
preserved under `research/`.
