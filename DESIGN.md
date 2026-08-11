# Design

## Source of truth

- Status: Draft
- Last refreshed: 2026-08-11
- Primary product surfaces: interactive selectors used by `tm`, `wenv`, `assume`, and `sec copy`, plus non-Git yes/no mutation confirmations
- Evidence reviewed: `internal/bb/select.go`, `internal/bb/identity_test.go`, `docs/selector-smoke-2026-08-11.md`, `docs/architecture.md`, `docs/decision-log.md`, and `README.md`

## Brand

- Personality: calm, capable, compact, and safe; a tool for one frequent operator rather than a generic dashboard.
- Trust signals: explicit selected row, visible result count, stable key hints, clear cancel behavior, and no hidden mutation.
- Avoid: copying fzf's layout for familiarity alone, decorative noise, Nerd Font requirements, excessive borders, and color-only state communication.

## Product goals

- Goals: make search immediately discoverable, make common selection possible without mode knowledge, preserve fast keyboard operation, and present a polished but compact terminal surface.
- Non-goals: pixel compatibility with fzf, mouse-first operation, a general TUI framework, Git workflow expansion, or previews that can expose secrets.
- Success signals: typing any printable character immediately filters; the first arrow press moves selection; Enter selects; Escape clears the query before cancelling; controls are understandable without documentation; stderr-only rendering and stable-value output remain unchanged.

## Personas and jobs

- Primary personas: the repository owner using bb repeatedly from zsh, tmux, Terminal.app, iTerm2, or an Orca terminal.
- User jobs: locate one project/profile/environment/item quickly, distinguish similar names, confirm the target, and return to the invoking command without residual terminal output.
- Key contexts of use: lists from a handful to hundreds of entries, narrow terminal panes, repeated daily use, and security-sensitive profile or secret labels.

## Information architecture

- Primary navigation: one search field, one ranked result list, and one persistent compact key-hint footer.
- Core routes/screens: one reusable selector surface plus one reusable default-cancel confirmation card; command-specific content is supplied through metadata rather than separate screens.
- Content hierarchy: selector title -> search query and result count -> selected result -> supporting metadata -> key hints.

## Design principles

- Search is the default action: printable input filters immediately; `/` remains an optional shortcut, not required knowledge.
- One key, one visible effect: arrows move on the first press, Enter selects on the first press, and Escape has a predictable clear-then-cancel sequence.
- Context without clutter: show one muted metadata line only when it helps distinguish choices.
- Safety survives styling: UI stays on stderr, selected stable values stay on stdout, and sensitive values never become preview metadata.
- Tradeoffs: direct typing gives up unmodified `j`/`k` navigation while the query is active; arrows and `ctrl+n`/`ctrl+p` remain unambiguous navigation keys.

## Visual language

- Color: adaptive terminal colors with one restrained accent for title/cursor, high-contrast selected text, muted metadata, and a monochrome `NO_COLOR` path.
- Typography: terminal-native text only; no icon font dependency. Use simple glyphs such as `>` and `•` with ASCII fallbacks where needed.
- Spacing/layout rhythm: one-line header, one-line search/status row, dense one- or two-line results, and one-line footer; avoid full-screen empty padding.
- Shape/radius/elevation: no simulated elevation; an optional subtle border only on sufficiently wide terminals.
- Motion: cursor blink only; no animated loading or decorative transitions in the selector.
- Imagery/iconography: none.

## Components

- Existing components to reuse: Bubble Tea program lifecycle, Bubbles fuzzy filter/ranking, Bubbles text input/list behavior where it matches the interaction contract, and Lip Gloss styles already supplied transitively.
- New/changed components: a bb-owned selector view, always-visible search row, result counter, explicit empty state, compact help footer, command-specific optional detail/keywords, and a default-cancel confirmation card.
- Variants and states: browsing, searching, filtered, no results, empty source, narrow terminal, no-color, and numbered fallback.
- Token/component ownership: selector styles and keymap live beside `internal/bb/select.go`; do not introduce a repository-wide design-system package.

## Accessibility

- Target standard: keyboard-complete operation, readable contrast, no required color perception, and stable text labels.
- Keyboard/focus behavior: printable keys search; arrows or `ctrl+n`/`ctrl+p` move; PageUp/PageDown page; Home/End jump; Enter selects; Backspace edits; Escape clears query then cancels; Ctrl+C always cancels.
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
- Compatibility constraints: all UI writes to stderr; stdout remains byte-stable for shell evaluation and machine consumers; non-TTY, dumb terminal, and `BB_SELECTOR=plain` behavior remain numbered and deterministic.
- Test/screenshot expectations: model-level key sequence tests, width/height snapshots or golden text with color disabled, PTY smoke in direct zsh and tmux, and regressions for stdout cleanliness and alternate-screen cleanup.

## Open questions

- [ ] Validate the accent palette in both a light and dark terminal before marking this document Active; owner: visual review; impact: contrast only, not interaction behavior.
