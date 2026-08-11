# v0.8.0

`v0.8.0` turns the frequently used secret surface into a safe interactive and
child-scoped workflow while retaining the v0.7.1 age storage format.

- `bb sec` opens a search-first manager for existing service/field metadata and
  then offers Copy, Replace value, Remove field, or Remove service.
- Replace and remove actions default to Cancel; Replace begins hidden input only
  after confirmation.
- Direct `sec set` protects existing values. Automation must pass `--force` to
  replace without an interactive confirmation.
- `sec exec <service> -- <command>` injects normalized variables into one child
  process without emitting plaintext exports or changing the parent shell.
- Consecutive numbered prompts share input safely in `BB_SELECTOR=plain` mode.

Secret values remain absent from selector metadata, prompts, journals, and
manager stdout.
