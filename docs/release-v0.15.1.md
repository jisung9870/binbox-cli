# v0.15.1

`v0.15.1` prevents terminal background-color responses from appearing as
`11;rgb:...` text in the shell after running `bb`.

- Bubble Tea, Bubbles, and Lip Gloss are upgraded together to their v2 APIs.
- Terminal background detection is requested only while an interactive TUI is
  active, and Bubble Tea v2 consumes the response through its input parser.
- `NO_COLOR` selectors and confirmations do not request a terminal background
  color.
- Explicit light and dark palettes replace package-global adaptive-color
  detection while preserving the existing visual treatment.
- Selector search, staged navigation, confirmation behavior, responsive layout,
  alternate-screen rendering, and terminal-control sanitization remain covered
  by regression tests.

Release verification includes the full Go test suite, `go vet`, targeted race
tests for the changed TUI paths, installer and release-guard contract tests, and
a pseudo-terminal check that observes an OSC 11 request and confirms its
response is consumed rather than rendered or left in subsequent input.
