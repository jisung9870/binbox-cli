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

# Compatibility endpoint used by the current LazyVim config.
bb tm projects --json
bb tm projects --plain
bb tm sessions --json
bb tm --project prj_...
bb tm attach --session dev
bb tm layout --layout golang --session dev --path "$PWD"

# Local port inspection and guarded termination.
bb port inspect 8080 --json
bb port kill 8080

# Explicit Git, Kubernetes, and AWS SSM compatibility adapters.
bb gx root
bb gx log --limit 20
bb gx branch switch feature/example
bb kx log api-pod -n staging --tail 100
bb kx port-forward api-pod 8080:80 -n staging
bb assm shell i-0123456789abcdef0

# AWS SSO, declarative environments, and the existing age store.
bb profile add dev --sso-session corp --account-id 123456789012 --role-name Admin
bb aws sso corp
bb aws assume dev
bb aws assume current
bb aws assume exec dev -- aws sts get-caller-identity
bb wenv import --check
bb wenv import --apply
bb wenv show dev
bb wenv apply dev
# Interactively apply/inspect presets and create, update, rename, or remove them.
bb wenv
# Keep only an encrypted-secret reference in the wenv preset. Applying the
# preset resolves it to CONTROLLER_OAUTH_TOKEN in the current shell.
bb wenv set awx CONTROLLER_HOST=https://at.core.line.games \
  CONTROLLER_OAUTH_TOKEN=sec://awx/w-token
bb wenv apply awx
# Interactive terminals prompt without echo.
bb sec set github token
# Automation can still pipe an exact value.
printf '%s' "$TOKEN" | bb sec set github token
# Existing values require confirmation or an explicit automation override.
printf '%s' "$TOKEN" | bb sec set github token --force
# Create or manage entries without displaying values. Empty stores offer
# Add secret; existing services offer Add field.
bb sec
# Rename a field without exposing or re-entering its value.
bb sec rename github token access-token
# Scope normalized secret variables to one child process.
bb sec exec database -- psql

# Register MCP servers once in bb, then synchronize the selected clients.
# Only environment-variable names are stored; values remain in bb sec/wenv.
bb mcp add jira --http https://jira.example.test/mcp \
  --bearer-token-env JIRA_TOKEN --targets claude,codex
bb wenv set mcp JIRA_TOKEN=sec://jira/token
bb wenv apply mcp
bb mcp sync claude jira
bb mcp sync codex jira
bb mcp check jira
# Run without arguments for staged add/show/edit/sync/check/remove management.
bb mcp

# Add checkout-independent zsh integration and native completion to ~/.zshrc.
bb setup shell

# The equivalent manual configuration is:
eval "$(bb shell init zsh)"

# Completion can also be loaded independently.
source <(bb completion zsh)

# Terraform compatibility with account-bound apply/destroy safeguards.
bb tfx status --json
bb tfx init -upgrade
bb tfx plan -var-file=qa.tfvars
bb tfx sum tree
# Read an existing plan. Never applies; sensitive values are never printed.
bb tfx browse
bb tfx browse tfplan --json
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

The MVP stores configuration under `$XDG_CONFIG_HOME/bb` and operational state
under `$XDG_STATE_HOME/bb` (with standard home-directory fallbacks). It does
not require `BB_ROOT`, a `libexec` tree, or helper scripts on `PATH`.
Interactive choices use bb's search-first Bubble Tea TUI on real terminals:
typing filters immediately, arrows move on the first press, and command-specific
metadata helps distinguish matches. Destructive non-Git confirmations use the
same responsive, default-cancel visual language. Pipes, tests, dumb terminals,
and `BB_SELECTOR=plain` retain deterministic text prompts; `NO_COLOR=1` removes
TUI color. fzf is not a bb runtime dependency. `bb shell init zsh` emits the
small wrapper that evaluates only
successful `bb wenv <preset>` output in the current shell. It has no checkout,
`BB_ROOT`, or libexec dependency.

bb-owned structured reads are formatted as human-readable labels and tables by
default. Add `--json` for automation and the stable schema-v1 envelope:

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
scripts/install.sh --github-cli --version 0.10.0
bb setup shell
```

See [operations](docs/operations.md) for the release and trust contract.

See the [documentation index](docs/README.md), [changelog](CHANGELOG.md),
[v0.15.1 terminal response fix release record](docs/release-v0.15.1.md),
[v0.15.0 MCP manager release record](docs/release-v0.15.0.md),
[internal implementation guide](docs/internals.md),
[command reference](docs/commands.md),
[legacy comparison](docs/legacy-comparison.md),
[architecture](docs/architecture.md), [migration plan](docs/migration-plan.md),
[non-Git parity audit](docs/non-git-parity-audit-2026-08-11.md),
[macOS cutover record](docs/cutover-macos-2026-08-11.md),
[v0.10.0 zsh/output smoke record](docs/zsh-output-smoke-v0.10.0.md),
[v0.9.0 hierarchical secret manager smoke record](docs/sec-manager-smoke-v0.9.0.md),
[v0.8.1 compact secret manager smoke record](docs/sec-manager-smoke-v0.8.1.md),
[v0.8.0 secret manager smoke record](docs/sec-manager-smoke-v0.8.0.md),
[v0.7.1 secret audit](docs/sec-audit-v0.7.1.md),
[v0.7.0 TUI smoke record](docs/tui-smoke-v0.7.0.md),
[tfx browse smoke record](docs/tfx-browse-smoke-2026-08-11.md), the historical
[selector smoke record](docs/selector-smoke-2026-08-11.md), and the
[decision log](docs/decision-log.md). The legacy and installer evidence is
preserved under `research/`.
