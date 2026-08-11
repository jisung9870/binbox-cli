# Hierarchical secret manager smoke record — v0.9.0

## Result

The secret manager was exercised against an isolated age key and encrypted
store. The 80x24 terminal first rendered services with field counts, then only
the selected service's fields, then Copy/Replace/Rename/Remove actions. Secret
values were absent from every screen and stdout remained empty.

Escape returned from Action→Field and Field→Service without leaving stale
alternate-screen content. Ctrl+C exited directly. Rename preserved the selected
value under the new field name, removed the old name, rejected an existing
target, and retained the encrypted store byte-for-byte when cancelled or
rejected.

Automated coverage includes deterministic numbered fallback, Bubble Tea
cancel/interruption distinction, hierarchy metadata, copy/replace/remove,
rename success/cancel/conflict, empty stores, and stdout/secret non-disclosure.
