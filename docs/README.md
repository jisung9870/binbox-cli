# Documentation index

This directory contains the current product contract, maintainer guidance,
migration evidence, and version-specific verification records for `bb`.

## Start here

| Need | Document |
|---|---|
| Install, configure, and try `bb` | [Project README](../README.md) |
| See what changed by version | [Changelog](../CHANGELOG.md) |
| Find a command and its behavior | [Command reference](commands.md) |
| Understand ownership and product boundaries | [Architecture](architecture.md) |
| Change or debug the Go implementation | [Internal implementation guide](internals.md) |
| Build, release, install, recover, or troubleshoot | [Operations](operations.md) |
| Review the current UX contract | [Design](../DESIGN.md) |
| Review the AWS resource browser scope | [AWS browser product pitch](product-aws-resource-browser-202608.md) |
| Review the AWS graph/API design | [AWS browser mini design](design-aws-resource-browser-202608.md) |

## Migration and compatibility

- [Migration plan](migration-plan.md)
- [Legacy comparison](legacy-comparison.md)
- [Legacy feature matrix](legacy-feature-matrix.md)
- [Non-Git parity audit](non-git-parity-audit-2026-08-11.md)
- [Work boundary](work-boundary.md)
- [Decision log](decision-log.md)
- [macOS cutover record](cutover-macos-2026-08-11.md)
- [Initial cutover record](cutover-2026-08-10.md)

These files preserve migration evidence. Current behavior is defined by the
installed command help, tests, [command reference](commands.md), and
[architecture](architecture.md); migration documents should not silently be
treated as a newer runtime contract.

## Release and verification records

Release summaries are available for
[v0.5.0](release-v0.5.0.md), [v0.5.1](release-v0.5.1.md),
[v0.5.2](release-v0.5.2.md), [v0.6.0](release-v0.6.0.md),
[v0.7.0](release-v0.7.0.md), [v0.7.1](release-v0.7.1.md),
[v0.8.0](release-v0.8.0.md), [v0.8.1](release-v0.8.1.md),
[v0.9.0](release-v0.9.0.md), [v0.10.0](release-v0.10.0.md),
[v0.12.0](release-v0.12.0.md),
[v0.13.0](release-v0.13.0.md), [v0.14.0](release-v0.14.0.md), and
[v0.15.0](release-v0.15.0.md), and [v0.15.1](release-v0.15.1.md).

Focused smoke and audit evidence:

- [tfx browse smoke record](tfx-browse-smoke-2026-08-11.md)

- [zsh completion and output smoke record](zsh-output-smoke-v0.10.0.md)
- [hierarchical secret manager smoke record](sec-manager-smoke-v0.9.0.md)
- [compact secret manager smoke record](sec-manager-smoke-v0.8.1.md)
- [integrated secret manager smoke record](sec-manager-smoke-v0.8.0.md)
- [secret safety audit](sec-audit-v0.7.1.md)
- [TUI smoke record](tui-smoke-v0.7.0.md)
- [historical selector smoke record](selector-smoke-2026-08-11.md)

## Documentation maintenance

When behavior changes:

1. Update command help and tests with the implementation.
2. Update `commands.md` for user-visible command behavior.
3. Update `architecture.md` or `internals.md` when ownership, data flow, state,
   output, or safety boundaries change.
4. Add the change under `Unreleased` in `CHANGELOG.md`.
5. At release time, move `Unreleased` entries into the tagged version and add a
   focused release record when operational evidence needs to be retained.

Do not place credentials, secret values, private configuration content, or raw
provider output in documentation or smoke records.
