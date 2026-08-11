# Design

## Source of truth

- Status: Active
- Last refreshed: 2026-08-11
- Primary product surfaces: interactive selectors used by `tm`, `wenv`, `assume`, and secret management, zsh completion, human-readable command output, non-Git yes/no mutation confirmations, and scoped child-process execution
- Evidence reviewed: `internal/bb/select.go`, `internal/bb/confirm.go`, `internal/bb/sec.go`, `internal/bb/completion.go`, `internal/bb/human.go`, `internal/bb/identity_test.go`, `internal/bb/completion_test.go`, `docs/zsh-output-smoke-v0.10.0.md`, `docs/sec-manager-smoke-v0.9.0.md`, `docs/sec-manager-smoke-v0.8.1.md`, `docs/sec-manager-smoke-v0.8.0.md`, `docs/sec-audit-v0.7.1.md`, `docs/selector-smoke-2026-08-11.md`, `docs/tui-smoke-v0.7.0.md`, light/dark palette render, `docs/architecture.md`, `docs/decision-log.md`, and `README.md`

## Brand

- Personality: calm, capable, compact, and safe; a tool for one frequent operator rather than a generic dashboard.
- Trust signals: explicit selected row, visible result count, stable key hints, clear cancel behavior, and no hidden mutation.
- Avoid: copying fzf's layout for familiarity alone, decorative noise, Nerd Font requirements, excessive borders, and color-only state communication.

## Product goals

- Goals: make search and completion immediately discoverable, make common selection possible without mode knowledge, preserve fast keyboard operation, present polished human-readable output by default, retain explicit stable JSON for automation, and make frequent secret use possible without printing plaintext into the parent shell.
- Non-goals: pixel compatibility with fzf, mouse-first operation, a general TUI framework, Git workflow expansion, or previews that can expose secrets.
- Success signals: Tab completes supported commands/options and safe local metadata; default structured reads render labels/tables rather than JSON; `--json` preserves schema-v1 envelopes; typing any printable character immediately filters; the first arrow press moves selection; Enter selects; Escape clears the query before navigating back or cancelling; secret management exposes only service/field metadata; service selection scopes the field list; rename, destructive, or overwrite actions default to Cancel; scoped execution passes values only to the child process; stderr-only rendering and stable-value output remain unchanged.

## Personas and jobs

- Primary personas: the repository owner using bb repeatedly from zsh, tmux, Terminal.app, iTerm2, or an Orca terminal.
- User jobs: locate one project/profile/environment/item quickly, distinguish similar names, confirm the target, and return to the invoking command without residual terminal output.
- Key contexts of use: lists from a handful to hundreds of entries, narrow terminal panes, repeated daily use, and security-sensitive profile or secret labels.

## Information architecture

- Primary navigation: one search field, one ranked result list, and one persistent compact key-hint footer.
- Core routes/screens: one reusable selector surface, a secret Service selector followed by a Field selector and safe Action selector, one reusable default-cancel confirmation card, zsh completion states, and one generic human output hierarchy for bb-owned structured results.
- Content hierarchy: service list with field counts -> selected service's flat field list -> safe action -> confirmation when required -> key hints. Field names are not repeated beneath every service in the first screen.

## Design principles

- Search is the default action: printable input filters immediately; `/` remains an optional shortcut, not required knowledge.
- One key, one visible effect: arrows move on the first press, Enter selects on the first press, and Escape has a predictable clear-then-cancel sequence.
- Context without clutter: show one muted metadata line only when it helps distinguish choices.
- Human first, machine explicit: bb-owned structured reads render concise labels or tables by default and use `--json` for the stable schema envelope.
- Scoped secret navigation: enter a service before choosing one of its fields; show field counts on services and keep field rows flat and compact.
- Safety survives styling: UI stays on stderr, selected stable values stay on stdout, and sensitive values never become preview metadata.
- Secret actions are staged: selection never performs a mutation; copy is the default safe action, replacement confirms before hidden entry, and rename/removal confirm before writing. Rename moves the existing in-memory value to a validated unused field name and never displays it.
- Tradeoffs: direct typing gives up unmodified `j`/`k` navigation while the query is active; arrows and `ctrl+n`/`ctrl+p` remain unambiguous navigation keys.

## Visual language

- Color: adaptive terminal colors with one restrained accent for title/cursor, high-contrast selected text, muted metadata, and a monochrome `NO_COLOR` path. The light accent measures 5.89:1 on the terminal background and 4.87:1 on the selected-row background; the dark accent measures 7.72:1 and 5.17:1 respectively.
- Typography: terminal-native text only; no icon font dependency. Use simple glyphs such as `>` and `•` with ASCII fallbacks where needed.
- Spacing/layout rhythm: one-line header, one-line search/status row, dense one- or two-line results, and one-line footer; avoid full-screen empty padding.
- Shape/radius/elevation: no simulated elevation; an optional subtle border only on sufficiently wide terminals.
- Motion: cursor blink only; no animated loading or decorative transitions in the selector.
- Imagery/iconography: none.

## Components

- Existing components to reuse: Bubble Tea program lifecycle, Bubbles fuzzy filter/ranking, Bubbles text input/list behavior where it matches the interaction contract, and Lip Gloss styles already supplied transitively.
- New/changed components: a bb-owned selector view, always-visible search row, result counter, explicit empty state, compact help footer, command-specific optional detail/keywords, a default-cancel confirmation card, a three-stage Service/Field/Action secret manager, native zsh completion, and a generic control-safe human renderer.
- Variants and states: service browsing, field browsing, searching, filtered, completion with/without dynamic candidates, human detail/table/empty output, JSON envelope, no results, empty source, action selection, rename input/confirmation, overwrite confirmation, removal confirmation, narrow terminal, no-color, and numbered fallback.
- Token/component ownership: selector styles and keymap live beside `internal/bb/select.go`; do not introduce a repository-wide design-system package.

## Accessibility

- Target standard: keyboard-complete operation, readable contrast, no required color perception, and stable text labels.
- Keyboard/focus behavior: Tab completes commands/options and safe candidates through native zsh completion; printable keys search; arrows or `ctrl+n`/`ctrl+p` move; PageUp/PageDown page; Home/End jump; Enter selects; Backspace edits; Escape clears query, then returns Action→Field→Service, then exits from Service; Ctrl+C always exits the manager.
- Contrast/readability: selected state uses both a marker and style; matched characters use emphasis in addition to color; metadata remains readable on light and dark terminals.
- Screen-reader semantics: retain the numbered plain selector as the deterministic accessible fallback through `BB_SELECTOR=plain`.
- Reduced motion and sensory considerations: honor `NO_COLOR`; avoid animation and rapid redraw outside input changes.

## Responsive behavior

- Supported breakpoints/devices: character terminals from 24x8 upward; optimize for 40-120 columns and 10-40 rows.
- Layout adaptations: below 50 columns hide metadata and border; below 10 rows reduce visible results and omit secondary help; never force an 80x20 visual canvas into a smaller pane.
- Touch/hover differences: not applicable; mouse support is deferred.

## Interaction states

- Loading: not currently needed because choices are prepared before launch; reserve a text-only state if asynchronous sources are introduced.
- Empty: explain that the source has no selectable items and exit without entering an unusable selector.
- Error: return to the caller with the existing typed error; do not leave alternate-screen artifacts.
- Success: immediately close and return exactly one stable value.
- Disabled: unavailable actions are omitted from the footer rather than shown as inactive decoration.
- Offline/slow network, if applicable: provider fetching remains outside the selector; the selector itself performs no network work.

## Content voice

- Tone: short, direct, and action-oriented.
- Terminology: use `Search`, `results`, `select`, `clear`, and `cancel` consistently; use the command domain in the title such as `Select AWS profile`.
- Microcopy rules: show keys beside verbs (`↑↓ move`, `enter select`, `esc clear/cancel`); use `No matches for “query”` rather than an empty list.

## Implementation constraints

- Framework/styling system: Go with Bubble Tea, Bubbles, and Lip Gloss; no external `fzf` process and no new dependency for this redesign.
- Design-token constraints: adaptive ANSI colors, `NO_COLOR`, and terminal width-aware truncation; preserve labels and stable values separately.
- Performance constraints: filtering and rendering 250 items must remain perceptually immediate and must not start provider or filesystem work per keystroke.
- Compatibility constraints: `bb shell init zsh` remains the only required `.zshrc` line and registers completion without checkout paths; dynamic candidates use local names/IDs only and never values or credentials; all TUI writes go to stderr; `--json` preserves the schema-v1 envelope; non-TTY, dumb terminal, and `BB_SELECTOR=plain` behavior remain deterministic; `sec exec` replaces only the selected service's normalized environment keys in the child and never mutates the parent environment.
- Test/screenshot expectations: generated zsh syntax/registration smoke, candidate non-disclosure tests, human-vs-JSON output regressions, model-level key sequence tests, width/height snapshots or golden text with color disabled, PTY smoke in direct zsh and tmux, and regressions for stdout cleanliness and alternate-screen cleanup.

## Open questions

- None. Light, dark, and `NO_COLOR` variants are validated; reopen this section if the palette or terminal rendering library changes.
