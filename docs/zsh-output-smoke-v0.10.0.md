# zsh completion and human output smoke record — v0.10.0

## Result

The generated `bb completion zsh` script passed `zsh -n`, loaded under `zsh -f`,
and registered `_comps[bb]=_bb`. `bb shell init zsh` retained current-shell wenv
and assume behavior while registering the same completion without a checkout
path.

Dynamic candidate tests covered wenv, AWS profile, project, active session,
tmux session, secret service, and secret field names. Git/gx top-level entries,
secret values, AWS credentials, and terminal control characters were absent.

Default project, session, tmux, port, MCP, Orca, run-history, and Neovim-owned
structured output rendered as labels or tables. The corresponding `--json`
paths retained schema-v1 envelopes. Explicit journal export and external owner
streams were unchanged.
