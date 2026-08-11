# v0.7.1

`v0.7.1` fixes interactive secret entry and hardens the complete `bb sec`
lifecycle without changing the existing age key or ciphertext format.

- `bb sec set <service> <field>` now prompts without echo on a terminal and
  completes on Enter; piped stdin remains byte-preserving automation input.
- Store decryption and initialization checks happen before accepting plaintext.
- Oversized input fails instead of being silently truncated.
- `sec init` can recover when a valid key exists but the store was never
  created, while restoring owner-only key permissions.
- Reads reject symlink/non-file stores and keys, plus age keys accessible by
  group or other users.
- Missing services and fields now fail for `list` and `rm` instead of appearing
  successful.
- `sec env` rejects normalized variable-name collisions before emitting any
  exports and prefixes names that would otherwise start with a digit.

Secret values remain absent from selector metadata, journals, prompts, and
error output.
