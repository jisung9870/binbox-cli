# Legacy feature migration matrix

This inventory is derived read-only from the legacy binbox repository. “Gated”
means the old command remains available during transition; it is not silently
forwarded through a checkout or embedded shell script.

| Legacy command | Current `bb` disposition | Status |
|---|---|---|
| `tm go`, `tm projects` | XDG project registry, `--plain`/`--json`, direct tmux open | Migrated with cutover contract |
| `tm sessions --json` | Same schema-v1 session fields, no pane scraping | Migrated |
| `tm attach/layout/kill/dirs` | Exact session re-observation, built-in layouts, and bb-registry-only directory changes | Migrated |
| `gx root/br/log` | `bb git` typed reads and `bb gx` direct compatibility commands | Migrated |
| `gx new/switch/clean` | Explicit branch create/switch/delete; destructive delete confirms and re-observes the exact ref | Migrated with explicit-argument contract |
| `tfx init/validate/fmt/plan/sum` | Direct Terraform/tf-summarize execution | Migrated |
| `tfx session/status/apply/destroy/end` | Exact legacy TSV, account/scope/expiry/plan revalidation, explicit confirmation | Migrated |
| `tfx state list` | Direct Terraform execution | Migrated |
| `tfx review/clean/state mutation` | Bounded review/clean plus exact state-address confirmation and revalidation | Migrated |
| `portcheck` | `bb port inspect`; kill prints, confirms, and re-observes the exact sorted PID set before SIGTERM | Migrated |
| `tvx` | Direct Trivy adapter with fixed CI/report policy and guarded node collector | Migrated |
| `kx`, `assm` | Explicit context/namespace/pod/instance arguments, direct kubectl/AWS argv, validated ports and JSON SSM parameters | Migrated with explicit-argument contract |
| `assume` | Reads/mutates AWS config and emits credentials | Credential gate |
| `wenv` | Legacy presets are executable shell | Data-format gate |
| `sec` | Existing encrypted store and key ownership | Secret gate |
| `agents` | Orca is the lifecycle owner | Retired from bb |
| `dx`, `md2jira`, `bb new/upgrade/check` | Checkout/developer/environment-specific behavior | Retire/archive |

The legacy repositories and shared sessionizer file remain read-only throughout
migration. No gated command may be declared migrated until its old data and
failure semantics have fixtures and a rollback path.

Interactive fzf discovery in legacy `gx`, `kx`, and `assm` is intentionally not
part of the stable replacement contract. The new commands require targets such
as branch, pod, context, and instance ID to be explicit; this keeps automation
deterministic and prevents a stale selection from becoming a mutation target.

The tmux cutover is intentionally not a live mirror. `bb tm projects` reads the
bb registry after explicit sessionizer import; later edits to the legacy dirs
file require another check/apply import. New sessions use `bb-<project-id>` and
do not silently adopt basename-named legacy sessions, because equal basenames
can refer to different paths. Existing legacy sessions remain attachable with
tmux until closed.

`bb doctor` preserves the schema-v1 capability fields, including nullable
`path`, but uses the new product's dependency policy: only Git is core and all
feature-specific tools are optional. It therefore does not reproduce the old
doctor's global failure when Docker/fzf/lsof/tmux are absent.
