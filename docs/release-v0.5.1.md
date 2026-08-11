# v0.5.1

`v0.5.1` is a compatibility patch for personal-device cutover.

## Fixed

- Import multiline legacy `EXPORTS=( ... )` arrays used by existing wenv
  presets without evaluating shell syntax.
- Continue rejecting command substitution, separators, malformed entries, and
  unterminated arrays.
- Document the actual platform-specific configuration fallback used when
  `XDG_CONFIG_HOME` is not set.

## Verification

- The legacy macOS wenv directory is accepted in check mode with two importable
  presets while its source hashes remain unchanged.
- Unit, race, vet, installer, and deterministic release guard suites pass.
