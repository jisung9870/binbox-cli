# v0.17.0

`v0.17.0` turns the secret store into something a person can keep current and an
agent can use without ever holding a value. Tokens carry an expiry, a reissue
URL and a usage record; they rotate with a way back; MCP servers take them
straight from `bb sec`; and the agent-facing surface reports what is expiring
and which commands may run with which token. The store format is unchanged.

- **Everyday use.** `service/field` paths everywhere, `add` as an alias of
  `set`, `add --paste` (the clipboard is cleared once stored), `copy` clearing
  the clipboard after 30 seconds if it still holds the value, and a 7-day
  encrypted trash behind `rm` with `trash` and `restore`.
- **Expiry and lifecycle.** A plaintext metadata sidecar holds expiry, URL,
  note, last use and verify targets, never a value. JWT `exp` claims fill the
  expiry. `list` shows `D-N` badges, `check` exits 1 on expired or expiring
  fields and `check --unused 90d` lists stale ones. `rotate` keeps the previous
  value in the trash and `rotate --undo` swaps back; `where` shows what
  references a secret; `import` stores a `.env` file without printing it.
- **Verification.** `bb sec verify` asks GitLab, GitHub, Jenkins or AWX who a
  token belongs to and records the expiry they report. Targets are set only from
  a terminal and signed with a key derived from the age identity, so editing the
  plaintext metadata cannot send a token elsewhere; requests are https-only,
  ignore environment proxies and never follow redirects.
- **MCP servers.** `bb mcp add --secret-env VAR=svc/field` registers a stdio
  server as `bb mcp launch <name>`, which resolves only that server's references
  and replaces itself with the server. `--bearer-token-secret` serves an HTTP
  server's token to Claude through `headersHelper`. `bb mcp audit --secrets`
  finds literal credentials in Claude and Codex configuration by location only,
  and `bb mcp adopt` moves them into `bb sec` and re-registers the server.
- **Agents.** `bb mcp allow add <cmd> --service S` binds a rule to the tokens it
  may use; read-only bundles (`gh-read`, `glab-read`, `aws-identity`) refuse
  host-selecting flags and URLs. `bb mcp serve` adds `sec_check` and
  `sec_verify`, reports expiry in `sec_list` and warns in exec results.
  `bb sec harden claude|codex` writes deny rules for the commands that print
  values or reconfigure what runs with them.
- **Fix.** `--json` commands that print their own result and exit non-zero no
  longer print a second error document.

## Scope of the guarantee

Deny rules and the `bb mcp header` caller check stop the obvious and accidental
paths. They match command text, so a shell function or alias that wraps
`bb sec exec`, a copied binary or a process reading the age key directly is not
covered; anything that can read the age identity can read every value. A
server's environment is visible to same-user `ps -E`, which the deny rules only
narrow. Each phase was reviewed for security separately and the findings were
fixed before release.

Release verification: `go test ./...`, `go vet ./...`, the AWS-browser race,
skip-free and four-target size checks, plus direct runs of the built binary
against a temporary store and configuration covering badges and `check`, trash
and restore, `mcp launch` keeping the server PID, the header gate, allow
bindings, rotate → verify → recorded expiry against a local issuer, undo, and
Codex rules checked with `codex execpolicy check`. `bb mcp audit --secrets` was
run read-only against real client configuration. Not exercised: Claude's
`headersHelper` and the Claude deny rules inside a live session, and `verify`
against real GitLab, Jenkins and AWX.
