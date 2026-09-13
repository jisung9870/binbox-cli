# v0.16.0

`v0.16.0` draws a boundary around the secret store for coding agents. A tool
result and a captured pipeline are both model context: a value printed there
survives in session records and summaries, so the commands that resolve secret
material now refuse rather than redact, and the agent-facing surface has no
value-returning tool at all.

- `bb sec get|env|copy` and `bb wenv apply|export` for presets that reference
  `sec://` require stdin and stderr to be terminals. stdout is deliberately
  excluded, so `eval "$(bb wenv dev)"` keeps working, and presets without
  references are never gated. `BB_ALLOW_SECRET_OUTPUT=1` is the explicit
  automation override.
- `bb mcp serve` speaks MCP over stdio. `sec_list`, `sec_ref`, `wenv_list`,
  `wenv_show`, and `wenv_set` deal in `sec://<service>/<field>` handles;
  `sec_exec` and `wenv_exec` inject values into one child process and return only
  its output, with the injected values substituted out. A value below four bytes
  is reported as unredacted rather than silently substituted, because replacing a
  one-character value would destroy the output it is meant to protect.
- `bb mcp allow` gates execution and is fail-closed: with no rule, `sec_exec` and
  `wenv_exec` run nothing. An argument prefix after `--` narrows a command to the
  subcommands it may run, so `aws sts get-caller-identity` can be permitted
  without permitting `aws s3 rm`. A broad entry and a narrow one for the same
  command are refused rather than silently coexisting. Optional `service` and
  `preset` scopes narrow which entries the tools may reach, and the listing
  reports whether either scope is in effect.
- Authorization resolves before the encrypted store is opened, so a command bb
  will not run is refused without touching a secret. Every attempt is appended to
  `$XDG_STATE_HOME/bb/mcp-serve-audit.jsonl` with the command name and outcome
  but never a value or the remaining arguments.
- `bb wenv exec <preset> -- <command>` scopes a resolved preset to one child
  process and prints nothing itself, so it needs no terminal. Unlike
  `bb sec exec` it maps any environment name onto any field instead of deriving
  names from the field and injecting every field of a service, which is what lets
  one service hold several role tokens that each become the same environment name
  in turn.
- The protocol layer is hand-written. The needed subset is `initialize`,
  `tools/list`, `tools/call`, and `ping`; the official Go SDK would pull oauth2,
  jwt, and x/tools into a binary whose job is handling secrets.

The Release workflow no longer runs tests. It builds, checksums, generates the
SBOM, and publishes; the test/vet/AWS-browser-race preflight and the
release-size checks are a local step before tagging, and nothing downstream
repeats them.

## Scope of the guarantee

This closes the accidental-leak path, not a determined one. An agent that can
run arbitrary shell commands as the same user can still reach the store through
`bb sec exec`, `bb wenv exec`, or the age key directly, since neither exec
surface consults the `bb mcp allow` list. Pair the gate with permission rules
that deny `bb sec get`, `bb sec copy`, `bb sec exec`, `bb wenv exec`, and
`BB_ALLOW_SECRET_OUTPUT`.

Release verification includes the full Go test suite and `go vet`, plus direct
checks against the built binary: the gate refuses in a non-terminal shell, the
MCP handshake negotiates `2025-06-18` and lists no value-returning tool,
off-prefix and out-of-scope calls are refused and audited without recording a
value, and a nested `--` survives both `bb mcp add` and `claude mcp add`. A live
MCP client session was not driven end to end; only the protocol exchange and the
client registration were exercised directly.
