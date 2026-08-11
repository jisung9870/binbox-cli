# binbox-cli

`binbox-cli` is the installable, single-binary `bb` CLI. It
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

# Compatibility endpoint used by the current LazyVim config.
bb tm projects --json
bb tm projects --plain
bb tm sessions --json
bb tm --project prj_...
bb tm attach --session dev
bb tm layout --layout golang --session dev --path "$PWD"

# Read-only legacy replacements.
bb git root --json
bb git branch list --all --json
bb git log --limit 20 --json
bb port inspect 8080 --json
bb port kill 8080

# Explicit Git, Kubernetes, and AWS SSM compatibility adapters.
bb gx branch switch feature/example
bb kx log api-pod -n staging --tail 100
bb kx port-forward api-pod 8080:80 -n staging
bb assm shell i-0123456789abcdef0

# AWS SSO, declarative environments, and the existing age store.
bb profile add dev --sso-session corp --account-id 123456789012 --role-name Admin
bb profile login dev
bb wenv import --check
bb wenv import --apply
bb wenv dev
printf '%s' "$TOKEN" | bb sec set github token

# Checkout-independent zsh integration. Add this to ~/.zshrc so `bb wenv dev`
# changes the current shell without sourcing the legacy binbox repository.
eval "$(bb shell init zsh)"

# Terraform compatibility with account-bound apply/destroy safeguards.
bb tfx status --json
bb tfx init -upgrade
bb tfx plan -var-file=qa.tfvars
bb tfx sum tree
bb tfx session 15
bb tfx apply
bb tfx state list
bb tfx review

# Trivy policy wrapper with fixed CI and report formats.
bb tvx repo .
bb tvx ci repo .
bb tvx sbom image app:latest -o sbom.cdx.json

# Validate and explicitly link an already-present LazyVim config.
bb setup nvim --config-dir /path/to/lazyvim-config --dry-run --json
bb setup nvim --config-dir /path/to/lazyvim-config --apply --consent --json
bb doctor nvim --config-dir /path/to/lazyvim-config --json
```

The MVP stores configuration under `$XDG_CONFIG_HOME/bb` and state/journals
under `$XDG_STATE_HOME/bb` (with standard home-directory fallbacks). It does
not require `BB_ROOT`, a `libexec` tree, or helper scripts on `PATH`.
Interactive choices use an embedded Bubble Tea fuzzy selector on real terminals
and fall back to a numbered prompt for pipes, tests, and dumb terminals; fzf is
not a bb runtime dependency. Set `BB_SELECTOR=plain` to force the numbered
prompt. `bb shell init zsh` emits the small wrapper that evaluates only
successful `bb wenv <preset>` output in the current shell. It has no checkout,
`BB_ROOT`, or libexec dependency.

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
`--migrate` for protected existing targets. Private releases are downloaded
through the existing authenticated GitHub CLI session:

```sh
scripts/install.sh --github-cli --version 0.5.0
```

See [operations](docs/operations.md) for the release and trust contract.

See the [command reference](docs/commands.md),
[legacy comparison](docs/legacy-comparison.md),
[architecture](docs/architecture.md), [migration plan](docs/migration-plan.md),
[non-Git parity audit](docs/non-git-parity-audit-2026-08-11.md),
[selector smoke record](docs/selector-smoke-2026-08-11.md), and
[decision log](docs/decision-log.md). The legacy and installer evidence is
preserved under `research/`.
