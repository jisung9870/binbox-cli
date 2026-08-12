# v0.15.0

`v0.15.0` turns MCP support into a credential-safe registry and synchronization
boundary for Claude Code and Codex, while removing command surfaces that had no
clear owner or duplicated surviving commands.

- `bb mcp` opens a staged Server→Action CRUD manager.
- `bb mcp list/show/add/edit/rm` manages stdio and streamable HTTP server
  metadata in the owner-only XDG registry.
- `bb mcp sync claude|codex` registers servers through each client's supported
  MCP CLI instead of directly rewriting version-dependent client config files.
- `bb mcp check` validates registry entries, stdio executables, required
  environment names, client registration, and Claude-reported connection
  health without displaying environment values.
- Bearer authentication stores only an environment-variable name. The value
  remains in `bb sec`, reaches the shell through a `wenv` `sec://` reference,
  and is inherited by the client process.
- Completion exposes MCP server names and command options but no environment
  values or credentials.
- `bb mcp audit` remains a content-free existence/SHA-256 metadata check for
  the bb, Claude, and Codex configuration paths.
- Unused `agents`, `session`, `orca status`, read-only `git`, `run`, and journal
  export surfaces are removed. Orca, tmux, Git, Claude, and Codex retain their
  respective lifecycle and provider ownership.

Release verification includes the full Go test suite, `go vet`, targeted race
tests, zsh syntax validation, owner-only registry permissions, and isolated
real-CLI registration through both `claude mcp` and `codex mcp`.
