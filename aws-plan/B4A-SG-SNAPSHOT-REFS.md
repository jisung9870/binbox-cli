# B4-A SG snapshot sync와 incoming refs 결과

독자        binbox-cli 개발·운영자와 B4-B 구현 담당자
목적        지정한 AWS context group에서 SG 참조 증거를 수집하고 불완전 coverage와 함께 역방향 조회하는 public CLI 계약을 고정한다
대상 환경   Go 1.25.11, darwin/arm64 개발 환경, `CGO_ENABLED=0`, local SQLite snapshot
최종 검토   2026-08-28
다음 검토   B4-B network relation collector 착수 전 또는 snapshot schema 변경 시
상태        B4-A 구현·자동 fixture·네 release target 검증 완료, 실계정 smoke 미수행

관련 문서   [확장 설계](design-aws-tui-202608.md) · [관계 계약](spec-aws-tui-relations-202608.md) · [B3 PoC](B3-SNAPSHOT-GRAPH-POC.md) · [SQLite ADR](ADR-002-SQLITE-SNAPSHOT-GRAPH.md)

## 지정 group의 SG 증거만 foreground에서 수집한다

B4-A는 B3의 optional SQLite store를 두 public command에 연결했다. Sync는 기존 `aws-contexts.json`에 등록된 group을 반드시 지정해야 하며 암묵적으로 모든 profile을 조회하지 않는다. Refs는 AWS CLI를 초기화하지 않고 active snapshot을 read-only로 연다.

```text
bb aws sync sg --group <configured-context-group> [--json]
bb aws refs sg <sg-id> --account <12-digit-id> --region <region> [--partition <partition>] [--json]
```

Sync는 별도 sync-owner lock으로 중복 수집을 막되 AWS 수집 중에는 현재 active snapshot의 read lock을 잡지 않는다. 따라서 장시간 수집 중에도 refs는 이전 complete run을 읽을 수 있다. 수집이 끝난 뒤에만 snapshot lock을 잡고 하나의 transaction으로 complete run과 active pointer를 교체한다. 취소되면 이전 active run을 유지한다. 각 profile×region scope는 3분으로 제한되고 fan-out 동시성은 최대 4다.

## 수집 범위는 EC2 SG·rule·instance 세 operation이다

허용 operation은 `DescribeSecurityGroups`, `DescribeSecurityGroupRules`, `DescribeInstances`다. Rule은 source SG에서 referenced SG로 향하는 exact `references` edge를 만들며 condition에 rule ID, ingress/egress, protocol, port range, referenced group/account, description을 보존한다. Instance는 instance에서 SG로 향하는 `uses` edge와 network interface ID를 보존한다.

ELBv2, RDS, Lambda, ECS, VPC endpoint가 SG를 사용하는지는 수집하지 않는다. 각 성공 profile×region에 이 다섯 service를 `not-observed/ec2-only`로 기록하므로 `refs`가 0건이어도 전체 AWS attachment가 없다고 단정하지 않는다.

## refs는 edge와 coverage를 같은 run에서 반환한다

Human과 JSON 결과는 `source=snapshot`, run ID, 완료 시각, age, schema version, canonical target, target 관찰 여부, incoming edge, observer profile/account/region, succeeded/failed/not-observed coverage를 포함한다.

- coverage가 complete이고 edge가 0건이면 `0 references found`다.
- failed 또는 not-observed scope가 있으면 `0 observed references; result incomplete`다.
- target observation이 없으면 `resource not observed in active snapshot`을 함께 표시한다.
- incoming edge가 10,000건을 넘으면 첫 10,000건만 반환하고 human/JSON 모두 truncation을 명시한다.

Read command는 missing snapshot을 생성하지 않고 sync 실행 방법이 포함된 capability error를 반환한다. Corrupt snapshot의 quarantine과 재생성은 write-capable sync open에서만 수행한다.

## schema v2는 observer를 edge와 분리한다

동일 edge를 여러 profile이 관찰해도 canonical relation은 중복 저장하지 않고 `relation_observer`가 각 profile/account/region과 관찰 시각을 보존한다. Confidence, reason 또는 scope가 다르면 서로 다른 evidence row로 남겨 관찰 시각과 evidence가 섞이지 않는다.

Schema v1에는 relation별 profile provenance가 없으므로 migration은 source를 관찰한 모든 profile을 추정하지 않는다. 대신 observer profile을 `legacy-unknown`으로 표시하고 source account와 relation scope만 보존한다.

## 자동 검증은 통과했고 실계정 smoke는 남아 있다

2026-08-28 개발 환경에서 다음 결과를 확인했다.

| 검증 | 결과 |
|---|---:|
| `CGO_ENABLED=0 go test ./... -count=1` | 통과 |
| snapshot race | 통과 |
| AWS browser/integration/provider/snapshot race | 통과 |
| `go vet ./...` | 통과 |
| linux/amd64 stripped binary | 19,857,556 bytes |
| linux/arm64 stripped binary | 18,415,764 bytes |
| darwin/amd64 stripped binary | 20,236,432 bytes |
| darwin/arm64 stripped binary | 18,891,314 bytes |
| exclusive 40MiB hard cap | 네 target 통과 |

수직 fixture는 SG-to-SG rule과 instance-to-SG relation을 sync한 뒤 같은 run에서 reverse 조회해 2건을 반환한다. Cross-account target, ENI ID, full rule condition, partial coverage, duplicate observation, 실제 v1 DDL migration, residual WAL read-only 복구, read-only nonmutation과 bounded concurrency는 별도 fixture로 검증했다.

전체 `internal/bb` race는 기존 interactive secret-manager test가 외부 helper 입력을 기다려 자동 gate에서 종료됐다. 이번 변경 범위의 snapshot CLI race와 AWS browser package race는 분리 실행해 검증하며, 이 제한을 전체 race 통과로 표현하지 않는다.

## B4-A에 포함하지 않은 기능

- VPC peering, TGW, PrivateLink collector
- `whois`, graph `path`, snapshot `diff`
- packet reachability 판정
- TUI snapshot source와 live verification
- background·periodic sync
- ELBv2, RDS, Lambda, ECS, VPC endpoint attachment 수집
- AWS Organizations account discovery

## 실패 모드와 재검토 조건

- profile 또는 region 한 scope가 실패하면 그 scope의 resource를 버리고 sanitized failure kind를 coverage에 기록한다. 다른 성공 scope는 같은 complete run에 남는다.
- 동일 account가 operation 사이에 바뀌면 `context-changed`로 실패하며 잘못된 edge를 commit하지 않는다.
- one-run 500MiB, store 1GiB, 30 account, 전체 sync 5분, query p95 200ms 중 하나를 넘으면 B3 storage·concurrency 설계를 재검토한다.
- B4-B가 다른 service attachment를 수집하면 해당 service의 `not-observed` row를 succeeded/failed coverage로 대체한다.
