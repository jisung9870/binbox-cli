# v0.10.0

`v0.10.0` adds native zsh completion and human-first structured output.

- `bb shell init zsh` now registers checkout-independent completion; standalone
  loading is available through `bb completion zsh`.
- Completion covers non-Git commands/options plus safe local project, session,
  tmux, wenv, AWS profile, and secret service/field metadata.
- Secret values and AWS credentials never enter completion candidates.
- bb-owned structured reads now render labels, nested sections, and tables by
  default. `--json` preserves the existing schema-v1 envelope for automation.
- Explicit JSON exports and external provider streams retain their existing
  formats.
