# B0 AWS TUI 구현 기준선

독자        저장소 소유자와 B1 이후 구현·검토 담당자
목적        관계 확장 전 보존해야 할 live browser 동작과 검증 증거를 고정한다
대상 환경   macOS darwin/arm64, Go 1.25.11, branch `feat/aws-resource-browser`
최종 검토   2026-08-28
다음 검토   B1 Relation/Evidence 계약 변경 완료 시
상태        완료

관련 문서   [확장 설계](design-aws-tui-202608.md) · [관계 계약](spec-aws-tui-relations-202608.md) · [루트 디자인 계약](../DESIGN.md) · [AWS 접근 ADR](ADR-001-HYBRID-AWS-ACCESS.md)

## 결론 — B1이 보존할 live 기준선이 검증됐다

구현 기준 commit은 `c1c4eebe3528c60e0f04a6edfe2d3e11d905fe6f`이며 2026-08-28 `origin/feat/aws-resource-browser`와 일치했다. B0는 새로운 AWS provider나 relation을 추가하지 않고 코드·문서·검증 기준을 고정했다.

## 보존할 구현 계약

- AWS CLI는 profile discovery와 credential export만 담당하고 resource read는 AWS SDK provider만 수행한다.
- profile이 credential 경계이며 account/principal은 STS 검증값을 사용한다.
- EC2 instances와 VPC는 current/all configured-region scope를 지원하고 global service는 region fan-out하지 않는다.
- TUI는 40x12 이상에서 전체 viewport를 사용하고 `:` command, `/` local filter, `Ctrl-o`/`Ctrl-i` history를 제공한다.
- resource는 Name tag, native name, ID 순서로 표시하고 Summary에서 Detail, Tags, relation category로 이동한다.
- exact linked singleton은 중간 `Resources (1)` 목록 없이 target Summary를 연다.
- relation evidence는 `id-exact`, `api-exact`, `correlated`, `inferred`, `ambiguous`, `unsupported`를 보존한다.
- denied, login required, not searched를 not found와 구분하고 성공한 page와 profile 결과를 유지한다.
- read-only provider interface, endpoint restriction, stderr/stdout 분리, non-TTY zero-call 계약을 유지한다.

## 문서 기준

화면·상호작용의 source of truth는 루트 `DESIGN.md`다. `aws-plan/DESIGN.md`의 초기 3-pane·tab mockup은 설계 기록이며 현행 full-viewport single-route 계약보다 우선하지 않는다. 관계 확장 범위와 단계는 `design-aws-tui-202608.md`, edge 정확성과 완료 조건은 `spec-aws-tui-relations-202608.md`가 소유한다.

## 2026-08-28 검증 결과

| 검증 | 결과 |
|---|---|
| `go test ./... -count=1` | 통과, 모든 Go package 성공 |
| `go test -race ./internal/bb/awsbrowser/... -count=1` | 통과, browser·integration·provider 성공 |
| `go vet ./...` | 통과 |
| `go build -trimpath -ldflags='-s -w' ./cmd/bb` | 통과, 14,156,818 bytes |
| stripped binary hard cap | 통과, 40 MiB 미만 |
| `git diff --check` | 통과 |

## 외부 acceptance는 B0 완료와 구분한다

실제 AWS credential을 사용하는 12-profile latency·identity 측정과 CloudTrail read-only operation 검토는 수행하지 않았다. 이 검증은 repository owner 승인이 필요한 외부 gate이며 fake provider와 로컬 PTY 결과로 완료했다고 간주하지 않는다.

## 다음 단계

B1은 기존 EC2→EBS/SG/VPC/Subnet/IAM과 Route 53→CloudFront→S3 relation의 condition, evidence, coverage 계약을 비교하고 필요한 model·fixture 변경만 수행한다.
