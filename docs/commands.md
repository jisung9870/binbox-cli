# bb command reference

This document describes the supported surface of the installed Go binary. Run
`bb <command> --help` for exact flags. External tools remain the owners of their
provider state; `bb doctor` reports whether they are available.

The AWS browse/query rows describe the unreleased branch surface. Automated
implementation and production wiring exist, but final terminal/release/real
AWS gates are pending.

| Area | Commands | Contract |
|---|---|---|
| Build and health | `version`, `doctor`, `doctor nvim` | Versioned output and required/optional capability checks |
| Shell | `setup shell`, `shell init zsh`, `completion zsh` | `setup shell` adds the idempotent `.zshrc` integration; emitted integration includes native completion and evaluates only successful wenv/AWS-assume environment output |
| Projects | `project list/add/show/remove`, `project import sessionizer --check/--apply` | XDG registry, stable IDs, read-only legacy source, content-addressed recovery copy |
| Human tmux | `tm`, `tm projects`, `tm sessions`, `tm attach/kill/dirs/layout` | Search-first TUI with project/session metadata and numbered fallback; exact target re-observation; tmux remains process owner |
| Git | `gx root/branch/log` | Direct Git reads plus explicit branch mutations |
| Kubernetes | `kx context/namespace/log/exec/port-forward` | Direct kubectl argv with explicit context, namespace, pod, and ports |
| AWS SSM | `assm shell/port-forward` | Direct AWS CLI Session Manager invocation with explicit instance and ports |
| AWS resources | `aws browse [--profile NAME] [--region REGION]`; `aws query ec2 instances`; `aws query ami <ami-id>`; `aws query domain <fqdn>`; `aws query role <exact-name>`; `aws sync <sg\|graph> --group <name>`; `aws refs <sg\|vpc> <resource-id> --account <id> --region <region>` | Full-terminal, local-first progressive TUI plus scoped human/JSON query; exact AMI search reports the AMI owner account separately from every account/profile where the AMI is visible; Home exposes regional EC2 Launch Templates and ALB/NLB catalogs; selecting a launch template lazily loads its versions and shows AMI, instance type, IAM profile, SG, subnet, storage, and metadata settings; opening the AMI relation from a launch-template version or EC2 instance performs an exact `DescribeImages` read and shows AMI state, owner, architecture, platform, creation time, and root-device details; version summaries report only whether User Data exists, while explicitly opening `User Data` performs a separate exact-version read and displays the Base64-decoded script with a sensitive-value warning; `:launch-templates`, `:elbv2`, `:alb`, and `:nlb` open their catalogs; `/` focuses local filtering, and `Ctrl-o`/`Ctrl-i` move through browser history; profile-less browse starts with the local configured-profile selector while explicit `--profile` starts at Home; typing locally filters loaded resources, tags, profiles, groups, and regions without AWS calls; Right/Enter opens and Left returns one screen; resources open on compact Summary and full fields open through Detail; exact linked singleton reads skip `Resources (1)`; `c` changes profile/current region/current-or-all-group scope and verifies derived account/principal through STS; optional groups come from `$XDG_CONFIG_HOME/bb/aws-contexts.json` or the OS user-config fallback (`~/.config/bb` on WSL/Linux); grouped EC2/VPC/ELBv2 reads merge bounded per-region results while IAM/Route 53/CloudFront remain global and linked resources pin their observed region; rows prefer `Name`, then native name, then ID; Tags open in a dedicated category; Security Group Summary opens inbound/outbound rules, IAM Role Summary opens attached/inline policies and policy documents, Route 53 Hosted Zone Summary opens DNS records, ALB/NLB Summary opens listeners and registered targets with ALB-only listener rules, and a CloudFront Alias traces through path-aware origins to an S3 bucket region; typed SDK EC2/IAM/Route 53/CloudFront/ELBv2/S3 reads and explicit-submit cross-profile search; browse is interactive and has no `--json`; explicit foreground graph sync atomically combines SG and VPC Peering evidence, while `sg` remains a narrower compatibility sync; refs returns at most 10,000 incoming edges with truncation, age, observer, and coverage metadata |
| AWS SSO | `aws sso [session]`, `aws sso list` | Search-first SSO-session login; AWS CLI owns browser authentication and the token cache |
| AWS credentials | `aws assume [profile]/list/current/unset/exec` | Search-first account/role profile selection; AWS CLI resolves credentials; bb stores none and emits them only to the shell pipe or a scoped child process |
| AWS compatibility | `profile ...`, `assume ...` | Existing profile configuration and assume commands remain available as compatibility surfaces |

The repository includes `docs/examples/aws-contexts.udg.json` as a portable,
credential-free UDG group example. Copy it to `~/.config/bb/aws-contexts.json`
on WSL/Linux before opening `aws browse`.
| Environments | `wenv`, `wenv list/current/show/apply/set/rm/export/import` | Staged preset CRUD TUI; declarative XDG JSON with `sec://service/field` references; redacted preview/default-cancel confirmation before apply; legacy shell is parsed, never sourced |
| Secrets | `sec`, `sec init/list/add/set/meta/check/rename/get/copy/env/exec/rm/trash/restore/harden` | Service→Field→Action manager with `Add secret`/`Add field` metadata-only choices and expiry badges; `service/field` paths; clipboard paste and timed clipboard clear; value-free expiry/URL/note metadata with `check`; 7-day encrypted trash; `harden claude|codex` agent deny rules; default-cancel overwrite/rename/service removal/purge; hidden input; child-scoped exec; existing age key/ciphertext format |
| Terraform | `tfx init/validate/fmt/plan/sum/browse/session/status/apply/destroy/end/state/review/clean` | Account-, scope-, expiry-, and plan-bound destructive safeguards; `browse` only reads a plan |
| Trivy | `tvx image/repo/config/ci/sbom/report/k8s/clean/doctor` | Fixed security policies and explicit guarded node collection |
| Local ports | `port inspect/kill` | Exact sorted PID observation followed by confirmation and re-observation |
| LazyVim | `setup nvim` | Validates a separate config identity and links only with apply plus consent |
| MCP | `mcp`, `mcp list/show/add/edit/rm/sync/check/audit/launch/header/serve/allow` | XDG CRUD registry and staged TUI; owner-CLI synchronization to Claude/Codex; required environment names only; no secret values, proxy, or server installation |

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

`bb sec add gitlab/token --paste --expires 90d --url <reissue page>` stores a
value from the clipboard, clears the clipboard, and records metadata in
`secrets.meta.json` next to the store. That file holds expiry, URL, and note
only, but its keys reveal `service/field` names in plaintext (mode 0600); JWT values fill the expiry from their `exp` claim. `bb sec list` and the
manager show `D-N` or `expired` badges, and `bb sec check` lists expired fields
and fields expiring within 14 days (`--within`), exiting 1 when there are any.
It reads only the metadata, so it can run from a prompt or a scheduler without
the age key. Replacing a value keeps its expiry and prints a reminder to update
it with `bb sec meta`.

`bb sec harden claude|codex` shows deny rules for agent shells and, after
confirmation, adds them to Claude's `settings.json` `permissions.deny`
(`CLAUDE_CONFIG_DIR` respected; other keys keep their order) or writes Codex's
`$CODEX_HOME/rules/bb-sec.rules` as `forbidden` prefix rules. They cover the
value-printing and agent-chosen-child commands (`bb sec get|env|copy|exec`,
`bb wenv apply|export|exec`, `bb mcp header`), registry changes
(`bb mcp add|edit|launch|allow`, Claude `Edit` of the two registries),
`BB_ALLOW_SECRET_OUTPUT`,
`age -d`, environment-printing `ps` forms and the age key file. Agents use the
`bb mcp serve` tools instead. The rules match command prefixes and are a
guardrail, not containment.

`bb sec rm` moves fields, with their metadata, into `secrets.trash.json.age`,
which is encrypted to the same key and pruned after 7 days. `bb sec trash` lists
it, `bb sec restore <service[/field]>` brings the newest copy back without
overwriting an existing field, and `--purge` skips the trash and also drops
earlier trashed copies of the same field (encrypted store backups under the
state directory are unaffected). `bb sec copy`
clears the clipboard after 30 seconds only if it still holds the copied value;
the detached clearer receives a SHA-256 digest on a pipe, never the value.

`bb mcp` opens a Server→Action manager. A server uses either local stdio or
streamable HTTP and declares its Claude/Codex targets. `sync` calls the selected
client's supported `mcp` CLI rather than editing client configuration files.
For authenticated servers, the registry stores only environment-variable names
or `sec://<service>/<field>` references, never values.

- A stdio server given `--secret-env GITLAB_TOKEN=gitlab/token` is synchronized
  as `<absolute bb> mcp launch <name>`. When the client starts it, `launch`
  decrypts only that server's references, overlays them on the inherited
  environment (replacing a stale shell value of the same name), and replaces
  itself with the server process. No wenv preset needs to be applied before
  starting the client, and the values exist only in that server's environment.
  A same-user process can still read that environment (`ps -E`), as with any
  environment-based secret.
- An HTTP server given `--bearer-token-secret jira/token` is synchronized to
  Claude with `claude mcp add-json` and `headersHelper: <bb> mcp header <name>`.
  Claude runs the helper at connect and after a 401/403. `bb mcp header` prints
  the Authorization header only when `CLAUDE_CODE_MCP_SERVER_NAME` names that
  server; this prevents accidental output, not a deliberate same-user caller.
  Codex has no helper, so such a server defaults to `--targets claude` and a
  Codex target is rejected; give Codex `--bearer-token-env` and a wenv preset
  instead.
- `launch` reads the registry on every start, so whoever can edit `mcp.json`
  decides which secrets the next server start receives. That is the same
  same-user trust `bb sec exec` already relies on.
- Otherwise put the value in `bb sec`, expose it through a `wenv` `sec://`
  reference, and apply that preset before starting the client.

`bb mcp allow add <command> [--service S]... [--preset P]... [-- <prefix>...]`
binds an exec rule to the services or presets it may run with; a rule with no
binding runs with any entry the global `allow service|preset` scope permits.
`--bundle gh-read|glab-read|aws-identity` (with a binding) adds read-only
subcommand sets that refuse `-R`/`--repo`/`--hostname`/`--endpoint-url` and URL
arguments. A broader rule that would mask a narrower rule's binding is refused. The
`bb mcp serve` tools `sec_list` (with recorded expiry) and `sec_check` report
expiry without values, and exec results warn about an expiring credential.

`mcp check` reports missing variables, `missing_secrets` for references absent
from the store, and registrations. Claude performs its own health check during `get`;
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
- Optional AWS relationship snapshot: `${XDG_STATE_HOME:-~/.local/state}/bb/aws/snapshot.db`
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

`bb` does not implement an agent scheduler, worktree manager, authoritative
dashboard/inventory database, AWS resource mutation, MCP proxy/server lifecycle,
automatic MCP installer, generic shell dispatcher, or self-mutating checkout
updater. The explicit local AWS relationship snapshot is rebuildable and is not
an authoritative inventory. `bb` also does not store AWS credentials or SSO cache data.
