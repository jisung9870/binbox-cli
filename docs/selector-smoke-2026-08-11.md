# Selector smoke record — 2026-08-11

## Result

The embedded Bubble Tea selector preserved the stderr-only UI contract and
returned stable values in both a direct macOS PTY and tmux 3.6b. No external
`fzf` executable was installed or invoked.

## Automated coverage

| Contract | Evidence |
|---|---|
| Fuzzy filtering selects the stable value rather than the display label | `TestBubbleSelectorFiltersAndReturnsStableValue` |
| Enter with no visible result does not quit | `TestBubbleSelectorEmptyFilterDoesNotExit` |
| Escape clears an active filter before cancelling an unfiltered selector | `TestBubbleSelectorEscapeClearsFilterBeforeCancelling` |
| Small/large resize and a 250-item list remain selectable | `TestBubbleSelectorResizesAndHandlesLongLists` |
| `tm` project/session and `wenv` preserve stable values | `TestCommandSelectorsReturnStableValuesWithoutStdout` |
| `sec copy` selects the service/field pair and writes only to the clipboard process | `TestSecCopySelectorUsesStableValueAndKeepsStdoutClean` |
| Numbered fallback remains available for non-TTY input | `TestBuiltInSelectorUsesNumberOrExactName` |

The `wenv` integration assertion compares stdout byte-for-byte with
`export TARGET='zeta'\n`; selector rendering is required to appear only on
stderr.

## Terminal matrix

| Host | Context | Input | Result |
|---|---|---|---|
| macOS | direct PTY in Orca terminal | `/`, `zeta`, Enter | pass; stdout `export TARGET='zeta'` |
| macOS | tmux 3.6b | `/`, `zeta`, Enter | pass; stdout `export TARGET='zeta'` |
| macOS | non-TTY pipe/test buffer | numbered `2` | pass; stable value selected, UI on stderr |
| macOS | Terminal.app/iTerm2-specific rendering | not run | visual-only follow-up; model, PTY, and tmux contracts are covered above |

The app-specific row is not used as correctness evidence because this project
does not maintain pixel/layout fixtures for terminal applications. A visual
problem reported in either application should be reproduced as a model or PTY
regression before changing selector behavior.

## Verification

```text
go test ./...          PASS
go test -race ./...    PASS
go vet ./...           PASS
direct PTY smoke       PASS
tmux PTY smoke         PASS
```

Temporary smoke fixtures were moved to the user's Trash after verification;
no HOME or XDG configuration was changed.
