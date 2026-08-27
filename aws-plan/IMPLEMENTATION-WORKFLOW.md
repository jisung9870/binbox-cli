# AWS Browse v2 구현 작업 방식

독자        구현 에이전트, 통합 담당자, 검증 담당자
목적        멀티에이전트가 충돌 없이 AWS Browse v2를 단계별 구현하고 검증하는 방식을 고정한다
대상 환경   2026-08-28 working tree, Go 1.25, Codex native subagents
최종 검토   2026-08-28
다음 검토   PTY/tmux·release·실계정 gate 완료 시
상태        구현 wave 완료, finalization gate 진행 중
등급        L2, 구현 종료까지 사용

관련 문서   [PRD](PRD.md) · [설계](DESIGN.md) · [동작 시나리오](SCENARIOS.md) · [아키텍처](ARCHITECTURE.md) · [ADR-001](ADR-001-HYBRID-AWS-ACCESS.md) · [검토 기록](REVIEW.md)

구현은 vertical slice와 검증 gate를 사용했다. Credential·endpoint → Home/runtime → EC2 → IAM → Route 53 → cross-profile core → production wiring이 자동화 fixture와 함께 구현됐으며, 문서/completion·PTY/tmux·release gate를 순서대로 닫는 중이다. 실제 AWS credential과 CloudTrail 검증은 repository owner가 승인·제공해야 하는 마지막 외부 gate다.

## 구현 시작은 Phase 0 계약으로 결정했다

`omx ralplan preflight --json`은 현재 환경에서 `unsupported_documented_leader_proof`로 종료하므로 구현 시작 gate로 사용하지 않는다. OMX는 선택적 launcher로만 취급하며, OMX의 leader proof나 role routing 성공 여부는 AWS Browse v2의 기능·보안 계약과 무관하다.

구현은 다음 조건을 확인한 뒤 시작한다.

1. repository의 기존 변경과 이번 구현 범위를 구분한다.
2. [ADR-001 P0 gate](ADR-001-HYBRID-AWS-ACCESS.md#p0-validation-gate)의 검증 대상·실패 조건·허용 파일을 첫 작업에 고정한다.
3. 현재 session이 native subagent를 허용하고 작업을 독립적으로 나눌 수 있을 때만 lane을 병렬로 실행한다. 제공되지 않으면 Leader가 같은 gate를 순차적으로 수행한다.

즉 orchestration runtime은 실행 방식을 바꿀 수는 있어도 구현 자체를 막지 않는다. 다음 phase 진입 여부는 아래 기능·보안 gate만으로 판정한다.

## 모델은 작업 위험과 반복성에 따라 배치한다

| 역할 | 모델·추론 | 책임 | 금지 |
|---|---|---|---|
| Leader | `gpt-5.6-sol` high | 범위, 통합, interface 변경 승인 | 검증 없이 phase 종료 |
| `explore` | `gpt-5.6-luna` low | 파일·symbol·call path 조사 | 설계·보안 판정 |
| `researcher` | `gpt-5.6-terra` high | AWS SDK 공식 API·version 근거 | repository 사실 추정 |
| `architect` | `gpt-5.6-sol` xhigh | credential·endpoint·cache·동시성 | 직접 대규모 구현 |
| 보안 `executor` | `gpt-5.6-sol` high | credential bridge와 SDK runtime | UI 파일 수정 |
| 기능 `executor` | `gpt-5.6-terra` medium | provider·mapper·query 구현 | credential 경계 변경 |
| `designer` | `gpt-5.6-terra` high | TUI model/view/navigation | SDK client 구현 |
| `test-engineer` | `gpt-5.6-terra` medium | fake·race·golden·failure test | production contract 독단 변경 |
| `verifier`/`code-reviewer` | `gpt-5.6-sol` high | 독립 완료·보안·회귀 판정 | 작성자 결과 무검증 수용 |

이 배치는 Sol을 복잡한 전문 작업, Terra를 intelligence/cost 균형, Luna를 반복적 고처리량 작업에 쓰는 [OpenAI GPT-5.6 model guidance](https://developers.openai.com/api/docs/guides/latest-model)를 따른다. 실제 model override는 현재 session의 정책과 제공 surface가 허용할 때만 적용한다. Luna 결과가 의미 판단을 요구하거나 Terra 작업이 credential·endpoint·account identity를 건드리면 Sol lane으로 승격한다.

## 한 번에 Leader와 최대 세 lane만 실행한다

동시 실행 slot은 Leader 포함 4개다. 작업 파동별 구성은 다음과 같다.

| Wave | 병렬 lane | 종료 조건 |
|---|---|---|
| 0 계약 검증 | Luna explore, Terra researcher, Sol architect | Phase 0 test plan과 SDK version 고정 |
| 1 runtime | Sol security executor, Terra test engineer, Sol verifier | ADR-001 gate 통과 |
| 2 shell·EC2 | Terra feature executor, Terra designer, Terra test engineer | Home과 EC2 vertical slice 통과 |
| 3 IAM·Route 53 | Terra feature executor, Terra designer, Terra test engineer | 각 service slice 통과 |
| 4 cross-profile | Sol executor, Terra test engineer, Sol architect | concurrency·identity·partial result 통과 |
| 5 종료 감사 | Luna mechanical audit, Sol code-reviewer, Sol verifier | 전체 검증과 미결 0건 |

Wave 안에서도 prerequisite가 있는 작업은 병렬화하지 않는다. 예를 들어 provider interface가 확정되기 전에 TUI와 test가 서로 다른 event type을 만들지 않는다.

## 파일 소유권은 wave 시작 전에 잠근다

공유 working tree에서는 한 파일을 두 agent가 동시에 수정하지 않는다.

| 소유자 | 기본 소유 경로 | 공유 변경 절차 |
|---|---|---|
| Leader | `internal/bb/app.go`, `aws.go`, `completion.go`, `go.mod`, `go.sum`, 문서 | review 후 직접 통합 |
| Security executor | `awsbrowser/credentials.go`, `awscli.go`, `sdk.go`, `errors.go` | public interface 변경은 Leader 승인 |
| Feature executor | `awsbrowser/providers/*`, `query.go`, `store.go`, `relation.go` | model event 변경은 Designer와 동기화 |
| Designer | `awsbrowser/model.go`, `view.go`, `messages.go` | provider를 concrete client로 참조 금지 |
| Test engineer | `awsbrowser/*_test.go`, helper fixture, golden | production 수정 요청은 Leader에게 반환 |

Agent는 배정받지 않은 파일을 발견·검토할 수 있지만 수정하지 않는다. Scope 확장, shared file 충돌, interface 변경이 필요하면 작업을 멈추고 Leader에게 정확한 file과 이유를 보고한다.

## 각 vertical slice는 같은 루프를 사용한다

한 slice는 한 사용자 행동이 끝까지 동작하는 최소 단위다. `Home 열기`, `EC2 list 열기`, `SG detail 열기`, `role policy 열기`, `domain cross-profile search`가 각각 slice다.

1. Leader가 acceptance와 허용 파일을 고정한다.
2. Test engineer가 실패하는 contract test 또는 fixture를 먼저 만든다.
3. Executor/Designer가 test를 통과시키는 최소 구현을 한다.
4. 작성 agent가 targeted test, race 대상 test, build를 실행한다.
5. Code reviewer가 correctness·security·scope creep을 검토한다.
6. Verifier가 acceptance와 실제 명령 출력이 일치하는지 독립 확인한다.
7. Leader가 통합하고 다음 slice의 interface를 해제한다.

Test를 먼저 만들기 어려운 순수 layout 작업은 golden expectation을 먼저 고정한다. Temporary stub, skipped test, TODO를 남긴 slice는 완료로 처리하지 않는다.

## 단계별 gate가 다음 코드의 시작을 결정한다

### Gate 0: credential·endpoint가 실제 SDK version에서 안전하다

[ADR-001 P0 gate](ADR-001-HYBRID-AWS-ACCESS.md#p0-validation-gate)의 11개 항목을 모두 자동화한다. 특히 다음 실패는 Phase 1 진입을 막는다.

- poison endpoint listener request가 1회 이상 발생한다.
- named profile이 ambient credential account로 검증된다.
- credential generation 변경 response가 이전 account key에 commit된다.
- resource operation용 AWS CLI subprocess가 실행된다.
- credential, CLI raw stdout/stderr가 log 또는 fixture failure에 노출된다.
- stripped binary가 release target 하나라도 40 MiB를 넘는다.

### Gate 1: Home과 runtime shell이 zero-call이다

- 첫 `View()` 전 CLI process와 SDK request 0회.
- non-TTY browse도 CLI/SDK 0회, stdout empty, stderr usage, exit 2.
- last subscriber cancellation과 completed-page retention 통과.

### Gate 2~4: service slice가 사용자 흐름을 끝낸다

- EC2: instance → SG/EBS/VPC/Subnet과 Tag/SG full viewer.
- IAM: instance profile → role → attached/inline/trust policy.
- Route 53: zone → progressive record와 exact domain search.
- 선택하지 않은 category, tab, relation의 SDK call은 0회.

### Gate 5: cross-profile이 identity와 partial result를 보존한다

- active SDK operation 최대 4, account당 최대 2, Route 53 account당 최대 1.
- current-first streaming, duplicate provenance, `not found/not searched/denied/login required` 분리.
- cancel 후 새 worker 0개, SDK request와 active credential child 잔존 0개.

## 검증 명령은 작은 범위에서 전체 범위로 확장한다

각 slice는 변경 package test부터 실행한다. Phase gate에서는 아래 전체 검증을 실행한다.

```bash
go test ./...
go test -race ./internal/bb/awsbrowser/...
go vet ./...
go build ./cmd/bb
```

Release size gate는 repository release script와 같은 target/flag를 사용한다. 실계정 smoke와 CloudTrail 확인은 local fake·poison·race gate가 모두 통과한 뒤 별도 승인된 read-only credential로만 실행한다. AWS mutation operation은 어떤 단계에서도 실행하지 않는다.

## Agent handoff는 결과와 증거를 함께 남긴다

모든 agent는 완료 시 다음 형식으로 Leader에게 반환한다.

```text
Outcome:
Changed files:
Contract added or changed:
Validation command and result:
Known risk or blocker:
Recommended next owner:
```

“완료했다”는 설명만 있는 handoff는 수용하지 않는다. Test output, compile evidence, file reference 중 하나 이상이 있어야 한다.

## 실패 시 같은 phase에서 되돌린다

이 구현은 unreleased AWS browser 교체이며 AWS resource와 사용자 data를 변경하지 않아 source rollback이 가능하다. 기존 dirty working tree의 사용자 변경은 rollback 대상이 아니다.

Rollback 판단 기준:

- 같은 gate가 두 번 연속 실패하고 원인이 interface/architecture에 있다.
- 기존 비-AWS command test가 새 변경 때문에 실패한다.
- read-only 또는 endpoint invariant를 증명할 수 없다.
- phase 변경이 허용 경로 밖 파일로 확장된다.

Rollback 절차:

1. Leader가 `git diff -- <phase-owned-paths>`로 이번 phase와 기존 사용자 변경을 구분한다.
2. Agent가 추가한 file과 hunk만 inverse `apply_patch`로 제거한다. `git reset --hard`, broad checkout, recursive delete는 사용하지 않는다.
3. `go test ./...`와 `go vet ./...`로 phase 전 상태를 확인한다.
4. Architecture 원인이면 구현을 재시도하지 않고 ADR/plan을 먼저 수정한다.

예상 rollback 소요는 한 slice 10~20분, SDK dependency와 runtime 전체 30~60분이다(2026-08-27 추정, 실제 구현 전). 되돌린 뒤에도 기존 unreleased CLI-preload 초안을 자동 복구하거나 release 대상으로 간주하지 않는다.

## 일정은 gate 통과 속도로 관리한다

2026-08-27 기준 예상 작업량은 Phase 0 1일, shell·EC2 2~3일, IAM 1~2일, Route 53 1~2일, cross-profile·hardening 2~3일이다. 이는 native subagent가 제공되고 3개 worker lane이 충돌 없이 작동한다는 가정의 추정이며, wall-clock 약속이 아니다. 순차 실행 시에는 다시 산정한다.

담당 구조:

- 실행·통합 책임: Leader.
- 보안 계약 승인: Architect와 Verifier가 각각 독립 판정.
- AWS 실계정 smoke 승인과 credential 제공: repository owner.
- 외부 publish, commit, push, release: 별도 사용자 요청 전에는 수행하지 않는다.

## 재검토 트리거

- Codex native subagent의 생성·메시지·대기 계약이 바뀐다.
- SDK endpoint firewall을 public API로 증명할 수 없다.
- provider 10개, profile 50개, stripped binary 40 MiB 중 하나를 넘는다.
- 두 agent가 같은 파일을 반복적으로 수정해야 해 ownership 분리가 실패한다.
- 실제 사용에서 exact domain/role 검색이 주요 조사 흐름을 해결하지 못한다.
