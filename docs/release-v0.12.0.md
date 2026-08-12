# v0.12.0

`v0.12.0` connects declarative `wenv` presets to the encrypted `bb sec` store
without persisting secret plaintext in environment configuration.

- `bb wenv set` accepts `KEY=sec://<service>/<field>` for secret-backed values.
- `bb wenv show` displays the stored reference rather than decrypting it.
- `bb wenv apply` and `bb wenv export` resolve references only when producing
  their explicitly requested shell exports.
- Apply previews redact an existing secret-like environment value and identify
  the target only as `<secret:service/field>`.
- Resolution is all-or-nothing: a missing service or field emits no exports.
- Plaintext secret-like values and malformed `sec://` references remain
  rejected by both direct preset creation and legacy import.

Example:

```sh
bb sec set awx w-token
bb wenv set awx \
  CONTROLLER_HOST=https://at.core.line.games \
  CONTROLLER_OAUTH_TOKEN=sec://awx/w-token
bb wenv apply awx
```
