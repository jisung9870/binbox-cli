# Legacy binbox comparison

The old binbox was a checkout-coupled shell dispatcher. The new binbox-cli is
an installed Go binary with typed commands and explicit ownership boundaries.

| Concern | Legacy binbox | binbox-cli | Change for users |
|---|---|---|---|
| Installation | Symlink into a checkout plus `BB_ROOT`, libexec, aliases, and helper scripts | One `bb` binary in `~/.local/bin` | Repository checkout is no longer required at runtime |
| Shell startup | Sources `setup/binbox/shell/init.zsh` and aliases every libexec program | `eval "$(bb shell init zsh)"` | `bb wenv <name>` still changes the current zsh; generic aliases are removed |
| Interactive selection | fzf used by multiple commands | Search-first responsive Bubble Tea TUI with safe metadata and numbered fallback | Direct typing searches without a mode switch; fzf is no longer a dependency; non-TTY use remains deterministic |
| Projects | Shared sessionizer text file as live source | XDG JSON registry imported check-first | Legacy file stays unchanged and can be used as rollback input |
| tmux | Shell scripts and basename-oriented sessions | Stable project IDs, explicit session operations, direct tmux argv | Existing sessions remain external; new sessions avoid same-name collisions |
| Git | `gx` shell helpers and interactive selection | Typed `git` reads and explicit `gx` mutations | Branch targets are explicit; stale interactive mutation is removed |
| Kubernetes | fzf-driven context/pod selection | Explicit `kx` targets and validated ports | More typing, deterministic automation, safer mutations |
| AWS assume | Shell resolves SSO/role credentials and keeps a binbox cache | `bb assume` delegates credential resolution/cache/login state to AWS CLI | Current-shell, unset, current, and scoped exec UX return without a bb credential cache |
| AWS SSM | Shell adapter | Typed `assm` adapter | Explicit instance and port parameters |
| wenv | Executable shell presets sourced/evaluated, with implicit kubectl mutation | Allowlisted declarative JSON with explicit show/apply preview and confirmation | Arbitrary shell syntax and secret-like keys are rejected; Kubernetes values are exported but do not mutate kubectl config |
| sec | age-encrypted JSON with shell tooling/editor flow | Same key/ciphertext format with in-memory/piped CRUD | Existing data stays readable; full plaintext editor flow is retired |
| Terraform | Shell guards and session files | Typed direct calls with account/scope/expiry/plan checks and immutable plan snapshot | Apply/destroy require stronger identity confirmation |
| Trivy | Shell policy wrapper | Typed direct adapter with fixed policy flags | Policy flags cannot be silently overridden |
| Port inspection | `portcheck` shell helper | `port inspect/kill` with exact PID re-observation | Termination is confirmation-gated |
| Execution history | Ad hoc shell output | Not retained by bb; use the owning command/tool directly | No partial execution record to misinterpret as an audit trail |
| MCP | Implicit environment/config discovery risk | Inventory and redacted audit only | No mutation, proxy, installation, or credential forwarding |
| Agent/worktree lifecycle | Historical `agents` and Workbench surfaces | Not implemented; use the Orca app/CLI directly | Orca remains the single lifecycle authority |
| Editor configuration | Coupled setup scripts and shared project file | Separate LazyVim repository validated/linked by contract | nvim/tmux configuration keeps its own release cycle |
| Update mechanism | Git checkout upgrade/self-management | Checksummed release assets and atomic installer | Rollback restores the previous binary; source checkout is never deleted |

## Compatibility summary

Migrated commands cover project/tmux, Git, Kubernetes, AWS SSM and SSO profile
management, declarative environments, the existing age store, Terraform,
Trivy, port inspection, journals, doctor, and LazyVim linking. `agents`, generic
libexec dispatch, `dx`, `md2jira`, checkout-based `new/upgrade/check`, arbitrary
executable wenv presets, and a plaintext secret editor are intentionally not
ported.

During the compatibility window the legacy repository may remain private or
archived. It is rollback/reference material, not a runtime dependency once the
shell and LazyVim consumer cutovers have been applied.
