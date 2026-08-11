# Secret manager smoke record — v0.8.0

## Result

The integrated secret manager and scoped execution passed isolated PTY and
model-level checks using non-secret fixtures. The existing personal age store
was read only for health validation and was not modified.

## PTY flow

At 80x24, `bb sec` filtered three entries to `installed/smoke`, closed the first
alternate-screen selector, opened the four-action selector, filtered to Replace,
and displayed a default-Cancel confirmation card. After explicit `y`, the hidden
`Secret value:` prompt accepted Enter without echoing the fixture. Terminal
cleanup completed with exit 0.

`bb sec exec installed -- /bin/sh -c <non-printing assertion>` confirmed the
normalized child variable existed and exited 0 without printing its value.

## Automated coverage

- Child environment overlay replaces selected keys, preserves unrelated keys,
  leaves the parent unchanged, and rejects normalized-name collisions before
  starting a child.
- New values do not prompt; existing values default to Cancel and require
  confirmation or `--force`.
- Manager Copy, Replace, Remove field, and Remove service dispatch through
  stable service/field values in numbered fallback mode.
- Multi-stage plain prompts retain unread bytes instead of losing buffered
  confirmation or hidden-input data.
- Manager UI and stderr never include secret values; Copy keeps stdout empty.
