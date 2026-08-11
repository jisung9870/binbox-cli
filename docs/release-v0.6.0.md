# v0.6.0

`v0.6.0` restores the two environment workflows that remained materially
different during the personal-device observation period.

## Added

- `bb wenv show <name>` for inspection without applying.
- `bb wenv apply [name] [--yes]` with current-to-target preview and confirmation.
- `bb assume [profile]`, `list`, `current`, `unset`, `exec`, and `profile`
  compatibility commands backed by AWS CLI credential resolution.

## Security and ownership

- bb never stores or journals assumed credentials.
- Direct terminal output of credential-bearing exports is refused; generated
  shell integration captures and evaluates successful output.
- `assume exec` injects credentials into only the selected child process.
- AWS CLI continues to own SSO login, credential resolution, and caches.
- Wenv cancellation emits no stdout and therefore cannot partially apply.
