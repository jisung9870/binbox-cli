# v0.14.0

`v0.14.0` makes environment and secret maintenance available from the
interactive TUI without exposing secret values or unsafe shell output.

- `bb wenv` now opens a staged Preset→Action→Variable manager.
- Presets can be applied, inspected, created, renamed, and removed.
- Variables can be added, updated, and removed inside an existing preset.
- Destructive preset and variable actions require confirmation.
- Management actions keep stdout empty; only Apply emits eval-safe exports for
  the shell integration.
- `sec://service/field` values remain references while inspecting or editing and
  resolve only during apply/export.
- `bb sec` offers `Add secret` from the service selector and `Add field` inside
  an existing service, including when the encrypted store is empty.
- Secret entry continues through the existing hidden-input, encrypted write
  path; selectors contain names and metadata only.
