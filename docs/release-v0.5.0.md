# v0.5.0

`v0.5.0` makes interactive selection self-contained and makes the release
pipeline reproducible on both macOS and Linux.

## Highlights

- `tm`, `wenv`, and `sec copy` use an embedded Bubble Tea/Bubbles fuzzy
  selector on real terminals.
- Non-TTY input/output, dumb terminals, and `BB_SELECTOR=plain` retain the
  deterministic numbered selector. Selector UI stays on stderr so stdout JSON
  and shell-eval contracts are unchanged.
- `fzf` is no longer required for these interactive flows.
- macOS canonical `/private/var` paths are handled consistently in project,
  tmux, sessionizer, and Neovim link checks.
- macOS `lsof` output is parsed using its standard NODE and NAME columns.
- Release archives and SHA-256 manifests are now generated with Go's standard
  library instead of GNU-specific `tar` and `sha256sum` behavior.

## Compatibility boundaries

- This release does not add or change Git-facing `bb git` or `bb gx` workflows.
- Orca remains the owner of agent and worktree lifecycle.
- Existing XDG registries, age ciphertext, sessionizer input, and LazyVim
  ownership boundaries are unchanged.

## Upgrade

For this private repository, use the existing authenticated GitHub CLI session:

```sh
scripts/install.sh --github-cli --version 0.5.0
```

The installer verifies the release checksum and staged binary version, keeps a
recoverable copy of the previous binary, and restores it if post-install
validation fails.

## Release evidence

- `go test ./...`
- `go test -race ./internal/bb ./internal/releasearchive`
- `go vet ./...`
- `scripts/test-install.sh`
- `scripts/test-release-guard.sh`
- two identical four-platform builds produce the same checksum manifest
