# B1 AWS TUI 관계 계약 기준선

독자        저장소 소유자와 B2 이후 relation/provider 구현 담당자
목적        reverse index 이전에 Tier 0 forward edge의 의미·조건·증거 계약을 고정한다
대상 환경   live AWS SDK provider와 `bb aws query` JSON
최종 검토   2026-08-28
다음 검토   B2 ELBv2 chain 구현 완료 시
상태        완료

관련 문서   [확장 설계](design-aws-tui-202608.md) · [관계 계약](spec-aws-tui-relations-202608.md) · [B0 기준선](B0-BASELINE.md)

## 결론 — relation 의미와 신뢰도를 분리했다

기존 `RelationKind`는 `id-exact`, `api-exact`, `correlated`, `inferred`, `ambiguous`, `unsupported` 신뢰도 계약으로 유지했다. 별도 `RelationSemantics`와 provider field를 추가해 edge 의미를 `relation_type`, `direction`, `condition`으로 표현한다. 현재 live extractor는 `direction=outgoing`만 생성하며 B3 reverse lookup은 원본 edge를 복제하지 않고 `incoming` 조회 결과로 표현한다.

JSON query는 provider가 만든 `fields.relations`, `fields.alias_relation`, `fields.zone_relation`을 보존한다. TUI의 `ProjectResourceFields`도 같은 필드를 읽으므로 두 경로에서 relation type·direction·condition·kind·reason·operation·scope·observed_at 의미가 같다. Coverage는 edge마다 복제하지 않고 해당 live query의 profile/region completion과 partial failure 상태가 소유한다.

## 고정한 Tier 0 forward extractor

| Source | Target | relation_type | condition | evidence kind |
|---|---|---|---|---|
| EC2 instance | VPC, Subnet | `member-of` | 없음 | `id-exact` |
| EC2 instance | Security Group | `uses` | `network interface` | `id-exact` |
| EC2 instance | EBS volume | `attached-to` | device name | `id-exact` |
| EC2 instance | IAM instance profile | `uses` | 없음 | `id-exact` |
| IAM instance profile | IAM role | `uses` | 없음 | `api-exact` |
| IAM role | managed/inline policy | `uses` | `attached` 또는 `inline` | `api-exact` |
| managed policy | policy version | `has-version` | version ID | `api-exact` |
| Route 53 record | hosted zone | `member-of` | 없음 | `api-exact` |
| Route 53 alias record | CloudFront distribution domain | `alias-to` | record type + `alias` | `api-exact` |
| CloudFront distribution | S3 bucket | `routes-to` | behavior path pattern | `inferred` |
| CloudFront distribution | custom/external origin | `routes-to` | behavior path pattern | `unsupported`, evidence only |

EC2의 기존 volume→instance, SG/VPC, SG reference, subnet/VPC, route-table association/target 관계에도 같은 semantic field를 적용했다. CloudFront의 `report/*`와 `character/*`가 같은 bucket을 향해도 condition이 다르므로 별도 edge와 TUI row로 유지한다.

## UI와 검색 계약

- relation row에 relation type을 표시한다.
- evidence preview에 type, direction, condition과 기존 kind/scope/operation/time/reason을 함께 표시한다.
- local filter는 label/target뿐 아니라 type, direction, condition, confidence, evidence에서도 찾는다.
- canonical target이 없는 custom origin은 navigation을 만들지 않고 evidence-only row로 유지한다.

## 2026-08-28 검증 결과

| 검증 | 결과 |
|---|---|
| `go test ./... -count=1` | 통과, 모든 Go package 성공 |
| `go test -race ./internal/bb/awsbrowser/... -count=1` | 통과, browser·integration·provider 성공 |
| `go vet ./...` | 통과 |
| `go build -trimpath -ldflags='-s -w' ./cmd/bb` | 통과, 14,173,458 bytes |
| stripped binary hard cap | 통과, 40 MiB 미만 |
| `git diff --check` | 통과 |

Fixture는 EC2 instance의 VPC/Subnet/SG/EBS/IAM 관계, IAM role/policy/version 관계, Route 53 CloudFront alias, CloudFront path별 S3/custom origin, TUI projection/evidence 표시, JSON query field 보존을 검증한다.

## B1 범위 밖

ELBv2 compute chain은 B2, reverse index와 snapshot coverage PoC는 B3다. 실제 AWS credential을 사용하는 live smoke와 CloudTrail read-only operation 검토는 이번 로컬 완료 판정에 포함하지 않았다.
