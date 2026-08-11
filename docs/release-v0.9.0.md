# v0.9.0

`v0.9.0` makes frequent secret management easier to scan and navigate.

- `bb sec` now opens Service→Field→Action screens instead of one repeated flat
  `service / field` list. Services show field counts and fields remain compact.
- Escape navigates back one level inside the manager; Ctrl+C exits immediately.
- The action screen adds default-cancel field rename. The same operation is
  available directly as `bb sec rename <service> <field> <new-field> [--yes]`.
- Rename preserves the encrypted value without displaying or re-entering it,
  rejects collisions, and uses the existing locked atomic store update.
- `bb sec copy` follows the same service-first selection and asks for a field
  only when the chosen service contains multiple fields.
