# Non-Git parity audit — 2026-08-11

## Decision

The supported non-Git surface has no repository-known daily workflow that
still requires the legacy binbox checkout. The installed Go binary owns local
state and orchestration adapters; provider state remains with the named
external tool. Git and `gx` are deliberately excluded from this audit and no
Git command implementation was changed.

## Command disposition and evidence

| Surface | Disposition | State/process owner | Smoke and contract evidence | Recovery |
|---|---|---|---|---|
| `version`, `help`, `doctor` | migrated | bb | `TestVersionAndHelp`, `TestDoctorMissingCapabilityRecoveryIsStable`; all 14 capabilities detected on the audit host | install a missing owner CLI using the exact `doctor --json` recovery string |
| `shell init zsh` | migrated | shell startup file selected by the user | `TestShellInitZshIsCheckoutIndependent` | restore the previous shell-startup line; the emitted wrapper has no checkout dependency |
| `project list/add/show/remove/import` | migrated | bb XDG config/state | `TestSessionizerCheckFixtureIsReadOnly`, `TestSessionizerApplyIsIdempotentAndKeepsLegacyBytes`, `TestSessionizerApplyRejectsSourceChangedAfterCheck` | re-import the unchanged source or use the content-addressed recovery copy |
| `tm`, `tm projects/sessions/attach/kill/dirs/layout` | migrated | bb registry; tmux owns live sessions | `TestTMProjectsUsesLazyVimEnvelope`, `TestTMAttachReobservesExactSession`, `TestTMKillRefusesSameNameReplacement`, selector PTY/tmux record | install tmux; use an explicit session/project after inspecting current state |
| `session list/start/stop/open` | migrated | bb intent records; selected backend owns execution | `TestProjectShowSessionOpenAndRunJournalCommands`, `TestStableProjectAndSessionIDs` | inspect XDG session records and retry with an explicit available backend |
| `run`, `run list/show/export`, `export` | migrated | bb journal | `TestRunJournalRedacts`, `TestRunPreservesCommandJSONArgument`, `TestExportProducesJSON` | inspect/export the redacted XDG journal; rerun the original owner command directly if needed |
| `kx context/namespace/log/exec/port-forward` | migrated | kubectl/Kubernetes | `TestKXUsesDirectKubectlArguments`, `TestKXRejectsUnsafePortAndTailValues` | install kubectl, inspect context/pod explicitly, then retry |
| `assm shell/port-forward` | migrated | AWS CLI and Session Manager | `TestASSMBuildsJSONParametersWithoutShell`, `TestExternalAdaptersReportMissingOwnerCLI` | install AWS CLI/plugin and retry with explicit instance/port arguments |
| `profile list/show/add/edit/rm/login`, `assume` | migrated with external credential ownership | AWS config and AWS CLI credential resolution/cache | profile preservation tests plus assume select/current/unset/exec and shell-output tests | restore the AWS config backup; authenticate through `bb profile login`; bb keeps no credential cache |
| `wenv list/current/show/apply/set/rm/export/import` | migrated with a narrower declarative contract | bb XDG config | import safety, byte-exact export, preview/confirm/cancel tests | re-import an allowlisted legacy preset; executable shell, secret-like variables, and implicit kubectl mutation stay retired |
| `sec init/list/set/get/copy/env/rm` | migrated with plaintext-editor retirement | existing age key/ciphertext; clipboard owner for copy | `TestSecCompatibleCRUDNeverPlacesValueInJournal`, `TestSecCopySelectorUsesStableValueAndKeepsStdoutClean` | restore the existing ciphertext/key backup; no plaintext recovery artifact is created |
| `port inspect/kill` | migrated | OS socket/process table | `TestPortInspectUsesPlatformReaderAndNeverInvokesKill`, `TestPortKillReobservesExactSortedPIDsAndUsesSIGTERM` | inspect again; termination requires confirmation and an unchanged PID set |
| `tfx` workflow | migrated with stronger mutation guards | Terraform; bb owns temporary guard/session state | apply/destroy snapshot, re-observation, cleanup, state mutation, review, and status tests in `tfx_test.go` | inspect account/scope/plan identity, recreate the bounded session, and retry explicitly |
| `tvx` workflow | migrated with fixed security policy | Trivy | direct argv, CI policy, format, Kubernetes, doctor, and unavailable tests in `tvx_test.go` | install Trivy or correct the explicit scan target; policy flags remain bb-owned |
| `setup nvim`, `doctor nvim` | migrated | separate LazyVim repository and selected XDG link | plan/apply/conflict/backup/restore/headless tests in `nvim_test.go` | use the recorded backup/restore action; bb never overwrites an unapproved target |
| `mcp inventory/audit` | migrated read-only subset | external MCP clients own configuration/lifecycle | `TestMCPInventoryDoesNotExposeConfigContent` | inspect the owning client; mutation, proxy, installation, and credential forwarding remain retired |
| `orca status`, `agents` | external owner | Orca | `TestAgentsPointsToOrcaOwnership`; live Orca status was ready during this audit | use the Orca app/CLI; bb does not recreate lifecycle operations |
| generic libexec dispatch, `dx`, `md2jira`, checkout `new/upgrade/check` | intentionally retired | original purpose-specific tools or none | absence is documented in `docs/commands.md` and `docs/legacy-feature-matrix.md` | invoke the owning tool directly or retain the private legacy repository as reference |
| arbitrary executable wenv and plaintext secret editor | intentionally retired | none | rejection and in-memory/piped secret tests above | convert to declarative non-secret variables or use the owning secret tool |

## Read-first migration evidence

- Sessionizer check/apply fixtures hash the source, preserve byte-identical
  recovery data, and assert that the legacy source bytes do not change.
- Wenv import parses an allowlist and rejects command substitution rather than
  sourcing the file.
- The secret compatibility test round-trips the existing age format without
  plaintext journal or file output.
- LazyVim tests require explicit repository identity/revision and prove that a
  dry run does not mutate the link.
- The audit host reported every documented owner dependency available through
  `doctor --json`; paths were observed locally and were not committed.

## Consistency result

- Non-Git command families in `dispatch`, `docs/commands.md`, and the legacy
  feature matrix have an owner, disposition, test evidence, and recovery path.
- Repository-auditable legacy-only non-Git daily use cases: **0**.
- Git CLI implementation changes in this audit: **0 lines**.
- Credentials, secret plaintext, and machine-specific paths committed: **0**.
- The remaining device-specific observation window belongs to P4 and does not
  expand the bb command surface.
