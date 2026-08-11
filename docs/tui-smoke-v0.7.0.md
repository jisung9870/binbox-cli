# TUI smoke record — v0.7.0

## Result

The search-first selector and default-cancel confirmation card passed direct PTY
and isolated tmux smoke checks without introducing an fzf runtime dependency.
Selector rendering stayed on stderr/alternate-screen output and the invoking
command's stdout remained limited to its existing result.

## Interaction evidence

| Context | Size | Flow | Result |
|---|---:|---|---|
| Direct macOS PTY | 80x24 | type `beta` -> Enter -> inspect wenv preview -> `y` | one filtered result, selected stable preset, confirmation defaulted to Cancel, stdout contained only two eval-safe exports |
| Isolated tmux socket | 80x24 | type `beta` -> Enter -> `y` | search, metadata, confirmation, terminal cleanup, and exit 0 passed |
| Isolated tmux socket with `NO_COLOR=1` | 24x8 | type `beta` -> Enter -> `y` | result count, select/cancel hints, both buttons, explicit `y confirm`, stdout exports, and exit 0 remained visible and bounded |
| Light/dark palette render | 80x24-equivalent | compare selector and confirmation states | title, selected row, muted metadata, border, and default-Cancel state remained distinct in both themes |

The tmux checks used a dedicated socket and `remain-on-exit`; the socket was
removed after capture. Fixtures used isolated repository-local XDG config and
non-secret `COLOR`/`REGION` values.

## Adaptive palette validation

The light and dark adaptive colors were rendered side by side using the same
selector and confirmation composition. Accent text meets WCAG AA contrast in
both normal and selected states: 5.89:1 and 4.87:1 on light backgrounds, and
7.72:1 and 5.17:1 on dark backgrounds. Muted text measures 6.10:1 on light and
7.46:1 on dark. Borders are decorative and are not used as the sole state or
content indicator.

## Automated coverage

- Direct printable-rune fuzzy search and immediate Enter selection.
- First arrow movement without consuming or clearing the query.
- Escape clear-then-cancel, Ctrl+C cancel, empty results, paging, Unicode input,
  metadata search, and stable values with duplicate labels.
- Width/height bounds at 24x8, 40x9, 49x12, 50x12, 80x20, and 120x40.
- `NO_COLOR` output without ANSI styling and with a non-color selection marker.
- Default-cancel confirmation, explicit keyboard confirmation, long-question
  wrapping, and clean stdout in text fallback mode.
- Terminal control and bidirectional-control sanitization for labels,
  descriptions, prompts, and confirmation questions.
- Existing Terraform/tmux/port target re-observation and immutable Terraform
  plan tests remain enabled.

## Validation

```text
go test ./...                  PASS
go test -race ./internal/bb   PASS
go vet ./...                  PASS
scripts/test-install.sh       PASS
scripts/test-release-guard.sh PASS
direct PTY smoke              PASS
tmux 80x24 smoke              PASS
tmux 24x8 NO_COLOR smoke      PASS
light/dark palette review     PASS
```
