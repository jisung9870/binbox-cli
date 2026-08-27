# bb command reference

This document describes the supported surface of the installed Go binary. Run
`bb <command> --help` for exact flags. External tools remain the owners of their
provider state; `bb doctor` reports whether they are available.

| Area | Commands | Contract |
|---|---|---|
| Build and health | `version`, `doctor`, `doctor nvim` | Versioned output and required/optional capability checks |
| Shell | `setup shell`, `shell init zsh`, `completion zsh` | `setup shell` adds the idempotent `.zshrc` integration; emitted integration includes native completion and evaluates only successful wenv/AWS-assume environment output |
| Projects | `project list/add/show/remove`, `project import sessionizer --check/--apply` | XDG registry, stable IDs, read-only legacy source, content-addressed recovery copy |
| Human tmux | `tm`, `tm projects`, `tm sessions`, `tm attach/kill/dirs/layout` | Search-first TUI with project/session metadata and numbered fallback; exact target re-observation; tmux remains process owner |
| Git | `gx root/branch/log` | Direct Git reads plus explicit branch mutations |
| Kubernetes | `kx context/namespace/log/exec/port-forward` | Direct kubectl argv with explicit context, namespace, pod, and ports |
| AWS SSM | `assm shell/port-forward` | Direct AWS CLI Session Manager invocation with explicit instance and ports |
| AWS SSO | `aws sso [session]`, `aws sso list` | Search-first SSO-session login; AWS CLI owns browser authentication and the token cache |
| AWS credentials | `aws assume [profile]/list/current/unset/exec` | Search-first account/role profile selection; AWS CLI resolves credentials; bb stores none and emits them only to the shell pipe or a scoped child process |
| AWS compatibility | `profile ...`, `assume ...` | Existing profile configuration and assume commands remain available as compatibility surfaces |
| Environments | `wenv`, `wenv list/current/show/apply/set/rm/export/import` | Staged preset CRUD TUI; declarative XDG JSON with `sec://service/field` references; redacted preview/default-cancel confirmation before apply; legacy shell is parsed, never sourced |
| Secrets | `sec`, `sec init/list/set/rename/get/copy/env/exec/rm` | Service→Field→Action manager with `Add secret`/`Add field` metadata-only choices; default-cancel rename/overwrite/removal; hidden input; child-scoped exec; existing age key/ciphertext format |
| Terraform | `tfx init/validate/fmt/plan/sum/browse/session/status/apply/destroy/end/state/review/clean` | Account-, scope-, expiry-, and plan-bound destructive safeguards; `browse` only reads a plan |
| Trivy | `tvx image/repo/config/ci/sbom/report/k8s/clean/doctor` | Fixed security policies and explicit guarded node collection |
| Local ports | `port inspect/kill` | Exact sorted PID observation followed by confirmation and re-observation |
| LazyVim | `setup nvim` | Validates a separate config identity and links only with apply plus consent |
| MCP | `mcp`, `mcp list/show/add/edit/rm/sync/check/audit` | XDG CRUD registry and staged TUI; owner-CLI synchronization to Claude/Codex; required environment names only; no secret values, proxy, or server installation |

Interactive selection renders only on stderr. Printable input searches without a
mode switch; `↑/↓` or `Ctrl+N/P` move, Enter selects, and Escape clears then
cancels. Multi-level selections run in one screen: in `bb sec`, Escape navigates
Action→Field→Service before exiting and each level keeps the query and cursor it
had when you left it; Ctrl+C exits immediately. `BB_SELECTOR=plain` forces
numbered prompts, where an empty answer steps back one level, and `NO_COLOR=1`
retains the TUI layout without ANSI color.

`bb wenv` opens a Preset→Action→Variable manager. Existing presets can be
applied, inspected, updated, renamed, or removed; `Add preset` creates a preset
from one or more `KEY=VALUE` entries. Management actions emit no stdout, so the
shell wrapper evaluates output only when Apply succeeds. `bb sec` similarly
appends `Add secret` at the service level and `Add field` within an existing
service. Secret values still use the hidden terminal prompt and encrypted store
path used by `bb sec set`.

`bb mcp` opens a Server→Action manager. A server uses either local stdio or
streamable HTTP and declares its Claude/Codex targets. `sync` calls the selected
client's supported `mcp` CLI rather than editing client configuration files.
For authenticated servers, the registry stores only environment-variable names.
Put the actual value in `bb sec`, expose it through a `wenv` `sec://` reference,
apply that preset before starting the client, and use `mcp check` to find missing
variables or registrations. Claude performs its own health check during `get`;
Codex `get` verifies registration, not a protocol handshake.

bb-owned structured reads render human-readable labels or tables by default.
Pass `--json` to receive the stable schema-v1 envelope. Export commands whose
explicit purpose is a JSON artifact continue to write JSON. External provider
streams retain the owning CLI's format.

## State and recovery

- Configuration: `$XDG_CONFIG_HOME/bb` when set; otherwise the platform user
  config directory (`~/Library/Application Support/bb` on macOS and commonly
  `~/.config/bb` on Linux)
- State and journals: `${XDG_STATE_HOME:-~/.local/state}/bb`
- AWS profiles: the AWS CLI's existing `~/.aws/config`
- Secret store: the existing binbox age key/ciphertext paths
- MCP registry: config `mcp.json`, containing server metadata and environment
  variable names but no environment values
- LazyVim configuration: a separate `lazyvim-config` checkout selected by path
  and linked under `$XDG_CONFIG_HOME/nvim`, or `~/.config/nvim` when XDG is unset

Writes use owner-only files, locks, atomic replacement, and concurrent-change
checks where applicable. Destructive commands require explicit targets and
confirmation. JSON-capable commands use the schema-v1 envelope documented in
the README.

`wenv` stores secret-like variables only as `sec://<service>/<field>` references.
`show` displays the reference rather than its value. `apply` and `export` resolve
all references from the encrypted `bb sec` store before emitting any exports, so
a missing service or field cannot produce a partially applied environment.

## Deliberate exclusions

`bb` does not implement an agent scheduler, worktree manager, dashboard, MCP
proxy/server lifecycle, automatic MCP installer, generic shell dispatcher, or self-mutating
checkout updater. It also does not store AWS credentials or SSO cache data.
