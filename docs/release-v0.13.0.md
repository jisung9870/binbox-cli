# v0.13.0

`v0.13.0` groups AWS authentication and credential activation under a clearer
`bb aws` command tree.

- `bb aws sso [session]` authenticates one configured AWS SSO session.
- Calling `bb aws sso` without a name opens a search-first selector populated
  from `[sso-session NAME]` sections; `bb aws sso list` prints those names.
- `bb aws assume [profile]` selects or activates account-, role-, and
  region-specific credentials resolved by the AWS CLI.
- `bb aws assume current`, `unset`, and `exec` retain the existing current-shell
  and child-process-scoped workflows.
- Expired-credential errors point to the exact `bb aws sso <session>` command
  when the selected profile declares an `sso_session`.
- Native zsh completion covers both SSO sessions and assume profiles without
  exposing tokens or credentials.
- Existing `bb profile` and `bb assume` commands remain available for
  compatibility.

Typical use:

```sh
bb aws sso lg-aws
bb aws assume lg-udg-ops
```
