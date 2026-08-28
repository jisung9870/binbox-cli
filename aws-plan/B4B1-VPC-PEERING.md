# B4-B1 VPC Peering network relation 결과

독자        binbox-cli 개발·운영자와 B4-B2 구현 담당자
목적        SG와 VPC Peering을 한 snapshot run에 보존하고 교차 계정 VPC 참조를 coverage와 함께 역조회한다
대상 환경   Go 1.25.11, darwin/arm64 개발 환경, `CGO_ENABLED=0`, local SQLite snapshot
최종 검토   2026-08-28
다음 검토   TGW collector 착수 전 또는 VPC Peering 실계정 smoke 완료 시
상태        B4-B1 구현·자동 fixture 검증 완료, 실계정 smoke 미수행

관련 문서   [확장 설계](design-aws-tui-202608.md) · [관계 계약](spec-aws-tui-relations-202608.md) · [B4-A](B4A-SG-SNAPSHOT-REFS.md)

## 결론 — network-only run 대신 SG와 Peering을 한 run에 합친다

`sync network`가 active snapshot의 SG edge를 덮어쓰지 않도록 combined command를 추가했다. 기존 SG-only command는 호환 유지한다.

```text
bb aws sync graph --group <configured-context-group> [--json]
bb aws refs vpc <vpc-id> --account <12-digit-id> --region <region> [--partition <partition>] [--json]
```

`graph`는 각 configured profile×region에 `ec2-sg`와 `ec2-vpc-peering` scope를 bounded concurrency 4로 수집하고 complete run 하나로 commit한다. TGW와 PrivateLink는 `not-observed/collector-not-implemented` coverage로 미구현 범위를 숨기지 않는다.

## API identity가 완전할 때만 exact participant edge를 저장한다

허용 operation은 `DescribeVpcPeeringConnections`다. Peering connection은 requester account/region과 peering ID를 canonical identity로 사용하고, connection에서 requester/accepter VPC로 `associated-with` edge를 만든다. Condition은 `role=requester` 또는 `role=accepter`, confidence는 `id-exact`, evidence operation과 observer profile/account/region은 별도로 보존한다.

Requester 또는 accepter의 owner ID, region, VPC ID가 빠지면 provider projection에 `unresolved:missing-*`을 기록하고 해당 endpoint edge는 만들지 않는다. 다른 endpoint의 exact edge는 유지한다. 임의 account나 region을 추정해 canonical VPC를 만들지 않는다.

## participant account coverage는 관찰과 직접 검색을 구분한다

Peering API에서 remote VPC identity를 관찰한 사실은 그 account 전체를 검색했다는 뜻이 아니다. Observer account와 다른 participant는 account/region별 `ec2-vpc-peering-participant` row와 `participant-account-not-searched`를 남긴다. 따라서 remote VPC edge가 보여도 해당 account의 전체 network inventory가 complete하다고 표시하지 않는다.

Context group에 양쪽 account profile이 모두 있어도 coverage는 profile별 provenance를 유지한다. B4-B 전체 완료 전에는 account-level aggregate completeness로 승격하지 않는다.

## 실패 모드와 한계

- 한 profile×region의 Peering read가 denied, timeout 또는 login-required면 해당 scope만 failed가 되고 다른 성공 scope는 같은 run에 남는다.
- requester identity가 불완전하면 peering node canonical key가 observer별로 달라질 수 있다. 해당 API 응답 fixture 또는 실계정에서 이 상태가 확인되면 unresolved node schema를 먼저 확장한다.
- VPC `refs`는 Peering `associated-with` edge만 반환한다. TGW, PrivateLink, route-table reachability는 이번 결과에 포함하지 않는다.
- packet reachability는 route, protocol, port 계약이 없으므로 판정하지 않는다.

## 자동 fixture는 통과했고 실계정 acceptance는 남아 있다

Cross-account, cross-region requester/accepter, Name tag, exact condition, missing accepter region, remote participant coverage, combined atomic run, VPC reverse lookup을 fixture로 검증했다. 2026-08-28 개발 환경의 gate 결과는 다음과 같다.

| 검증 | 결과 |
|---|---:|
| `CGO_ENABLED=0 go test ./... -count=1` | 통과 |
| 변경 경로 race | 통과 |
| `go vet ./...` | 통과 |
| linux/amd64 stripped binary | 19,951,764 bytes |
| linux/arm64 stripped binary | 18,546,836 bytes |
| darwin/amd64 stripped binary | 20,332,112 bytes |
| darwin/arm64 stripped binary | 18,975,346 bytes |
| exclusive 40MiB hard cap | 네 target 통과 |

실계정 acceptance는 read 권한이 있는 representative profile에서 다음만 확인한다. AWS resource mutation은 수행하지 않는다.

1. `bb aws sync graph --group <group> --json`
2. known requester와 accepter VPC에 `bb aws refs vpc ... --json`
3. CloudTrail 또는 권한 로그에서 STS와 allowlisted Describe operation만 호출됐는지 확인

전체 sync 5분, one-run 500MiB, store 1GiB, 30 account 중 하나를 넘으면 B3 storage·concurrency 설계를 재검토한다.
