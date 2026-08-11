# v0.7.0

`v0.7.0` replaces the minimally configured Bubble Tea list with a bb-owned,
search-first TUI shared across non-Git interactive workflows.

## Selection

- Printable input filters immediately; `/` remains a harmless compatibility
  shortcut rather than required mode knowledge.
- The first arrow or `Ctrl+N/P` press moves the selected result.
- Result counts, no-match feedback, match emphasis, stable key hints, paging,
  and clear-then-cancel Escape behavior are visible and consistent.
- Project paths, tmux attachment/window state, AWS region/role, wenv variable
  counts/keys, and secret service/field names provide safe search context.
- Narrow terminals hide metadata and borders; `NO_COLOR` retains all semantic
  markers without ANSI color; numbered fallback remains available.

## Confirmation

- Non-Git yes/no mutation gates share a compact confirmation card.
- Cancel is selected by default; `y` is an explicit confirmation shortcut.
- Terraform immutable-plan, identity, scope, and re-observation safeguards are
  unchanged, as are tmux and port target re-observation checks.

## Stream safety

- TUI rendering remains on stderr.
- stdout stays reserved for stable machine output, shell exports, or the owner
  command's existing result.
- Secret values and AWS credentials are never selector metadata.
