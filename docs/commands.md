# bb command reference

This document describes the supported surface of the installed Go binary. Run
`bb <command> --help` for exact flags. External tools remain the owners of their
provider state; `bb doctor` reports whether they are available.

| Area | Commands | Contract |
|---|---|---|
| Build and health | `version`, `doctor`, `doctor nvim` | Versioned output and required/optional capability checks |
| Shell | `shell init zsh` | Emits checkout-independent integration; only successful wenv/assume environment output is evaluated |
| Projects | `project list/add/show/remove`, `project import sessionizer --check/--apply` | XDG registry, stable IDs, read-only legacy source, content-addressed recovery copy |
| Human tmux | `tm`, `tm projects`, `tm sessions`, `tm attach/kill/dirs/layout` | Embedded fuzzy selector with numbered fallback, exact target re-observation, tmux remains process owner |
| Session intent | `session list/start/stop/open` | bb-owned intent records; explicit `tmux`, `orca`, or `shell` backend |
| Execution journal | `run`, `run list/show/export`, `export` | Records command basename, argument count, time, and exit only; never argument values |
| Git | `git root/branch/log`, `gx root/branch/log` | Typed reads plus explicit branch mutations |
| Kubernetes | `kx context/namespace/log/exec/port-forward` | Direct kubectl argv with explicit context, namespace, pod, and ports |
| AWS SSM | `assm shell/port-forward` | Direct AWS CLI Session Manager invocation with explicit instance and ports |
| AWS profiles | `profile list/show/add/edit/rm/login` | SSO profiles in AWS config only; AWS CLI owns credentials, login, and cache |
| AWS credentials | `assume [profile]/list/current/unset/exec/profile` | AWS CLI resolves credentials; bb stores none and emits them only to the shell pipe or a scoped child process |
| Environments | `wenv list/current/show/apply/set/rm/export/import` | Declarative non-secret XDG JSON; preview/confirmation before apply; legacy shell is parsed, never sourced |
| Secrets | `sec init/list/set/get/copy/env/rm` | Existing age key/ciphertext format; plaintext remains in memory or pipes |
| Terraform | `tfx init/validate/fmt/plan/sum/session/status/apply/destroy/end/state/review/clean` | Account-, scope-, expiry-, and plan-bound destructive safeguards |
| Trivy | `tvx image/repo/config/ci/sbom/report/k8s/clean/doctor` | Fixed security policies and explicit guarded node collection |
| Local ports | `port inspect/kill` | Exact sorted PID observation followed by confirmation and re-observation |
| LazyVim | `setup nvim` | Validates a separate config identity and links only with apply plus consent |
| MCP | `mcp inventory/audit` | Redacted, read-only inventory; no mutation, proxy, install, or credentials |
| Orca | `orca status`, `agents` | Status/pointer only; Orca exclusively owns agents, worktrees, terminals, schedules, and DAGs |

## State and recovery

- Configuration: `$XDG_CONFIG_HOME/bb` when set; otherwise the platform user
  config directory (`~/Library/Application Support/bb` on macOS and commonly
  `~/.config/bb` on Linux)
- State and journals: `${XDG_STATE_HOME:-~/.local/state}/bb`
- AWS profiles: the AWS CLI's existing `~/.aws/config`
- Secret store: the existing binbox age key/ciphertext paths
- LazyVim configuration: a separate `lazyvim-config` checkout selected by path
  and linked under `$XDG_CONFIG_HOME/nvim`, or `~/.config/nvim` when XDG is unset

Writes use owner-only files, locks, atomic replacement, and concurrent-change
checks where applicable. Destructive commands require explicit targets and
confirmation. JSON-capable commands use the schema-v1 envelope documented in
the README.

## Deliberate exclusions

`bb` does not implement an agent scheduler, worktree manager, dashboard, MCP
proxy, automatic MCP installer, generic shell dispatcher, or self-mutating
checkout updater. It also does not store AWS credentials or SSO cache data.
