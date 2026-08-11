# tfx browse smoke record — 2026-08-11

## Result

`bb tfx browse` reads an existing Terraform plan and renders it as a two-level
view: a resource list ordered by blast radius, and a read-only attribute list per
resource. Sensitive and not-yet-known values are replaced with placeholders in
every output path. The command invokes `terraform show -json` and nothing else,
so the account-bound apply and destroy safeguards are not on this path.

## Rendered states

Captured from the model at 84x18 with color disabled, using a fixture plan that
carries one sensitive attribute, one value known only after apply, one plain
scalar change, all four action shapes, and one unchanged resource.

```
╭──────────────────────────────────────────────────────────────────────────────────╮
│ tfplan · 4 changes · 2 destroy · 4 review                            4/4 results │
│ Search  Type to search…                                                          │
│ ──────────────────────────────────────────────────────────────────────────────── │
│ > destroy  aws_s3_bucket.logs                                                    │
│     1 changed · needs review                                                     │
│   replace  aws_lb.edge                                                           │
│     1 changed · needs review                                                     │
│   update   aws_db_instance.main                                                  │
│     3 changed · needs review                                                     │
│   create   aws_instance.web                                                      │
│     1 changed · needs review                                                     │
│ ──────────────────────────────────────────────────────────────────────────────── │
│ ↑↓/ctrl+n,p move  enter select  esc clear/cancel  ctrl+c quit                    │
╰──────────────────────────────────────────────────────────────────────────────────╯
```

```
╭──────────────────────────────────────────────────────────────────────────────────╮
│ update  aws_db_instance.main                                         3/3 results │
│ Search  Type to search…                                                          │
│ ──────────────────────────────────────────────────────────────────────────────── │
│ > allocated_storage                                                              │
│     10 → 20                                                                      │
│   endpoint                                                                       │
│     old.example.com → (known after apply)                                        │
│   password                                                                       │
│     (sensitive) → (sensitive)                                                    │
│ ──────────────────────────────────────────────────────────────────────────────── │
│ ↑↓/ctrl+n,p move  esc clear/back  ctrl+c quit                                    │
╰──────────────────────────────────────────────────────────────────────────────────╯
```

The attribute level is read-only: its footer omits `enter select`, and Enter does
not close the viewer or complete the walk. Escape is the only way back, which
keeps the key used to drill in from also being the key that exits.

The unchanged `aws_vpc.main` (`no-op`) is absent from the list, and the resource
order is destroy, replace, update, create, so a change that can lose data is
never below the fold.

## Non-disclosure evidence

| Path | Check | Result |
|---|---|---|
| Summary structures | marshal all summaries, search for the fixture secret and its replacement | absent |
| `--json` envelope | full stdout searched for the fixture secret | absent |
| Non-terminal table | full stdout searched for the fixture secret | absent |
| Selector metadata | sensitive attributes render as `(sensitive)` on both sides before reaching label, description, or search text | confirmed by the attribute view above |

Sensitivity is read from the plan's own `before_sensitive` and `after_sensitive`
structures, and unknown values from `after_unknown`. A block marked sensitive
covers every path inside it, not only the exact path, so marking `tags` sensitive
hides `tags.owner` as well.

## Boundary evidence

| Condition | Expected | Result |
|---|---|---|
| Commands executed | only `terraform show -json <plan>` | one invocation, verified by recording every command the app spawns |
| Missing plan file | reported before terraform runs | `plan file not found: <path> (run 'bb tfx plan' first)` |
| Plan with no changes | stated plainly, no empty selector | `Plan has no changes.` |
| No usable terminal | table on stdout, no prompt | table rendered, no `[1-N]` prompt emitted |
| `--json` | schema-v1 envelope with counts and resources | `changes=4 destroy=2 needs_review=4` |

## Agreement with `tfx review`

`browse` and `review` share one parser, one definition of which attribute paths
changed, and one review-rule evaluation. The tag-only update that `review`
classifies as `EXPECTED` is not marked for review by `browse`, and the fixture
plan that `review` classifies as `REVIEW` marks every listed resource. The
attribute set shown is therefore exactly the set the rules were applied to.

One consequence worth knowing: attributes that exist only in `after_unknown` and
are absent from both `before` and `after` are not listed, because neither side
has a value to differ. This matches what `tfx review` evaluates.
