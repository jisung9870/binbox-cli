# Secret command audit — v0.7.1

## Result

The existing encrypted store and key were healthy, but direct `sec set` input
waited for EOF and echoed plaintext. The v0.7.1 implementation uses a hidden
terminal prompt that completes on Enter while preserving piped input for
automation. The age storage format is unchanged.

## Command coverage

| Command | Result |
|---|---|
| `sec init` | Creates a 0600 key and encrypted empty store; recovers a key-only partial initialization |
| `sec list [service]` | Sorts names; a missing requested service is an error |
| `sec set` | Decrypts first, prompts without echo on TTY, accepts piped stdin, rejects empty or oversized values |
| `sec get` | Resolves an explicit field or the sole field and writes only the requested value |
| `sec copy` | Search-first service/field selection keeps values out of the UI and stdout |
| `sec env` | Sorts and shell-quotes exports; rejects collisions before output and creates valid numeric-leading names |
| `sec rm` | Requires an existing service/field, confirms by default, and preserves encrypted backups |

Every read rejects symlink or non-file store/key paths and refuses an age key
that is accessible by group or other users.

## Evidence

- Real installed v0.7.0 reproduced the EOF wait and visible echo in an isolated
  PTY using non-secret fixtures.
- The fixed binary completed on Enter and rendered only `Secret value:` plus a
  newline; the fixture value was not echoed.
- Unit coverage includes hidden input, exact piped CRUD, oversize rejection,
  partial-init recovery, missing targets, environment collisions, quoting,
  unsafe key permissions, stable copy selection, and the full list/env/remove
  lifecycle.
