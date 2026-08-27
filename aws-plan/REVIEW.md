# AWS Browse v2 기획 검토 기록

독자        저장소 소유자와 구현 승인자
목적        기존안 반증, 역할별 반복 검토, 반영 결정과 구현 영향 범위를 추적한다
대상 환경   2026-08-28 working tree, AWS SDK for Go v2와 AWS CLI v2 공식 문서
최종 검토   2026-08-28
다음 검토   선택적 tmux/interactive resize 관찰 및 실계정 gate 승인 시
상태        hybrid 구현·자동 Linux PTY·자동 release gate 완료, 수동/외부 acceptance 대기

관련 문서   [PRD](PRD.md) · [설계](DESIGN.md) · [동작 시나리오](SCENARIOS.md) · [아키텍처](ARCHITECTURE.md) · [ADR-001](ADR-001-HYBRID-AWS-ACCESS.md) · [구현 작업 방식](IMPLEMENTATION-WORKFLOW.md) · [인덱스](README.md)

검토 결과, 기존 P0의 “전체 snapshot 후 category 화면”과 이후의 “resource도 모두 AWS CLI child로 조회” 안을 폐기한다. 최종 기준은 zero-call Home, dedicated async TUI, relation-scoped SDK fetch, bounded cross-profile search다. Browser runtime의 AWS CLI capability는 profile discovery·credential export로 제한하며 기존 SSO login은 별도 호환 surface다.

## 검토 루프 0: 기존안은 시작 속도와 상세 읽기에서 탈락했다

### 관찰된 사실

- `internal/bb/aws_browse.go`의 현재 초안은 TUI 전에 STS, EC2/VPC 6종, Route 53 zone과 모든 record, IAM user/role을 직렬 수집한다.
- 한 hosted zone fixture도 11개의 AWS CLI call을 선행하도록 test가 고정돼 있다.
- category는 첫 화면에 보이지만 resource data는 이미 전부 수집된 뒤다.
- generic selector는 최대 96열이고 50열 미만에서 description을 숨긴다.
- Tag, SG rule, route는 comma-separated 한 줄 string으로 평탄화된다.
- resource key가 `type:id`라 account/region이 다른 동일 ID를 합치면 충돌한다.
- EC2 instance profile은 ARN 문자열만 표시하고 IAM role/policy 관계가 없다.

### 판정

- 기능 추가로 해결 불가: preload와 scope 없는 graph key가 data flow의 중심이다.
- UI styling으로 해결 불가: static `Choices`와 synchronous `next(path)`는 loading, stream, cancel, partial error가 없다.
- 기존안을 supersede하고 architecture와 UI model을 교체한다.

## 검토 루프 1: 제품 범위를 사용자 workflow로 줄였다

### 처음 제안에서 줄인 것

- 모든 AWS service browse는 비목표로 유지한다.
- fuzzy all-profile inventory search는 P1 이후로 미룬다.
- VPC route table 심화, ENI/EIP, ELB/CloudFront resolver는 P1 이후로 미룬다.
- persistent cache와 background daemon은 제외한다.
- IAM effective permission 계산은 policy document browse와 분리한다.

### P0에 남긴 workflow

1. zero-network Home과 category lazy fetch.
2. EC2 → EBS/SG/VPC/Subnet.
3. EC2 instance profile → IAM role → attached/inline/trust policy.
4. Tag와 SG rule 무손실 viewer.
5. single-profile Route 53 zone/record browse.
6. exact domain/role cross-profile search.
7. history, loading, cancel, refresh, partial error, scope 표시.

이 범위면 사용자가 설명한 EC2→SG/EBS/IAM과 Route 53→다른 account 탐색을 직접 줄이면서 provider 확장 경쟁은 피할 수 있다.

## 독립 검토 결과와 반영

| 발견 | 위험 | 반영 결정 | 문서 위치 |
|---|---|---|---|
| 첫 화면 전 전체 수집 | service/zone 증가에 따라 시작 지연 증가 | Home 전 AWS inventory call 0회 | PRD 성공 신호, Architecture lifecycle |
| generic selector 재사용 | async·table·viewer 상태를 표현하지 못함 | dedicated `awsbrowser.Model` | Design Components, Architecture package |
| `type:id` resource key | 멀티 account/region 충돌 | partition/account/region/type/id key | Architecture data model |
| Tag/SG/policy 한 줄 표시 | 핵심 값 손실 | collection viewer와 rule normalization | Design Core screens |
| 모든 keystroke fan-out 가능성 | throttling과 UI freeze | Enter submit 이후에만 bounded search | PRD/Architecture search contract |
| profile worker credential 혼선 | 현재 shell credential로 잘못된 account 조회 | ambient/named 모두 CLI credential bridge, profile-scoped SDK config/cache | Architecture Credential lifecycle |
| 첫 hit에서 검색 중단 | 동명 role, split-horizon DNS 누락 | 모든 scope coverage를 끝까지 표시 | Design Cross-profile search |
| exact/heuristic 2단계 | DNS 상관관계를 사실로 오인 | evidence 6단계 + reason/source | Architecture Relation confidence |
| `assume`를 검색 대상 명사로 사용 | profile/context/credential 동작 혼동 | UI/internal은 context, command는 호환 유지 | PRD/Architecture naming |
| IAM policy를 실제 권한으로 오인 | SCP/boundary/session policy 누락 | `not effective-permission evaluation` 표시 | Design IAM viewer |
| SG attachment를 EC2만 확인 | ELB/RDS/Lambda 사용처 누락 | P0 `EC2 attachments only`, ENI는 P1 | Architecture EC2 section |
| empty/error를 동일 처리 | `not found` 오판 | Empty/Forbidden/AuthRequired 등 typed state | Architecture Error model |
| exact domain에서 zone 전체 pagination | 큰 zone과 profile 수에 따라 지연 | name cursor, 300개 이하 bounded page, name 변경 시 중단 | Architecture Route 53 |
| non-TTY와 forced plain 혼합 | pipe에서 prompt 대기 | TTY plain loop와 non-TTY scoped query를 분리 | Architecture command contract |
| profile별 관측을 field merge | 어느 role도 보지 못한 합성 결과 | canonical identity 아래 observation 분리 | Architecture data model |
| CloudTrail read-only 기준이 auth 호출 제외 | role profile의 STS 호출을 오탐 | narrowed SDK resource interface와 CLI credential/auth operation을 분리 | PRD/Architecture verification |
| `global` 용어 중복 | AWS global과 cross-profile scope 혼동 | `AWS global`, `cross-profile`로 분리 | Design terminology |
| custom endpoint 신뢰 경계 없음 | signed request가 임의 host로 전송될 수 있음 | endpoint env/config 무시, opt-in은 P1 | Architecture Credential isolation |
| structured error flag 버전 불명 | patch version만으로 credential 오류 형식을 잘못 판단 | credential export argv capability test, unknown은 conservative 분류 | Architecture Error model |

## 반증 질문과 답

### “Category 첫 화면만 먼저 그리고 뒤에서 결국 전체 preload하면 되지 않는가?”

아니다. 첫 paint는 빨라져도 사용하지 않는 IAM/Route 53/EC2 call이 계속 발생하고, profile fan-out 시 비용이 계정 수만큼 증폭된다. 요청은 화면 표시가 아니라 사용자의 category/relation intent를 따라야 한다.

### “기존 selector를 async로 확장하면 중복을 줄일 수 있지 않은가?”

가능하지만 회귀 범위가 `tm`, `wenv`, `assume`, secret selector까지 넓어진다. AWS는 table, tabs, route history, streaming, cancellable provider가 필요해 selector 추상화보다 browser application에 가깝다. common style/sanitization만 재사용하는 편이 diff와 책임 경계가 작다.

### “여러 profile을 전부 검색하면 더 빠른가?”

사용자 조작은 줄지만 API 호출은 늘어난다. 그래서 자동 검색은 domain/role/resource exact query를 제출한 시점에만 실행하고, bounded concurrency·current-first·streaming result·coverage를 함께 사용한다. 일반 category browse는 현재 context만 조회한다.

### “동일 account profile은 하나만 검색하면 되지 않는가?”

같은 account여도 role과 policy가 달라 result visibility가 다를 수 있다. identity 확인 전 제거하지 않고, 결과를 canonical resource로 합칠 때 `available via profiles`를 보존한다.

### “`assume` 명령도 지금 바꿔야 하는가?”

강제 rename은 P0에 필요하지 않다. 이미 배포된 credential 적용 동사와 browser의 검색 context는 다른 개념이다. 내부/UI 용어는 즉시 `AWS context`로 고치고, CLI의 `bb aws context`/`use`는 호환 migration이 필요한 별도 결정으로 둔다. AWS profile 이름 자체는 변경하지 않는다.

## 요구사항 추적 검증

| 사용자 요구 | 설계 응답 | 검증 방법 | 상태 |
|---|---|---|---|
| 시작부터 전체 수집하지 않음 | zero-call Home, lazy provider | first paint 전 CLI process·SDK request 0 | 설계 완료 |
| EC2/Route 53 등 category 진입 | service catalog와 route | Home golden + navigation model test | 설계 완료 |
| Tag는 눌러서 전체 보기 | `Tags(N)` viewer | long value/narrow render golden | 설계 완료 |
| SG가 잘리지 않음 | source별 rule table/card | IPv4/IPv6/prefix/SG fixture | 설계 완료 |
| EC2→EBS/SG/role/policy | relation stack과 provider chain | fixture end-to-end model test | 설계 완료 |
| 다른 assume/account 자동 검색 | exact query cross-profile fan-out | slow/denied/auth/duplicate fixtures | 설계 완료 |
| AWS Console형 편한 TUI | service→list→detail tabs→relation | 4개 terminal width golden | 설계 완료 |
| 기능 늘어도 시작 속도 유지 | category/provider isolation | 미선택 provider call 0 assertion | 설계 완료 |

`설계 완료`는 구현이 끝났다는 의미가 아니라, 요구사항에 대한 검증 가능한 계약이 문서에 있다는 뜻이다.

## 문서 품질 점검

- 첫 3줄에 결정과 구조가 있음: 통과.
- PRD 강제 항목인 문제, 사용자, 목표/비목표, 대안, MVP, milestone: 통과.
- 설계 강제 항목인 요구사항/제약/가정, 현재→목표→차이, 책임, failure, 확장, 운영 인계: 통과.
- Design skill의 brand, IA, principles, visual, component, accessibility, responsive, states, voice, constraints, open questions: 통과.
- 모든 성능 수치는 측정값이 아니라 2026-08-27 설계 목표로 표시: 통과.
- 외부 동작 근거는 AWS 공식 문서로 제한: 통과.
- 같은 내용을 다이어그램과 장문으로 중복: 흐름 caption과 책임 설명으로 역할 분리, 통과.
- 4열을 넘는 표: 없음.
- destructive command: 없음.

## 검토 루프 2: 구현 경계의 모순을 닫았다

1. exact domain/role search는 모든 discovered profile을 기본 scope로 사용하고 현재 ambient context를 먼저 검색한다. 사용자는 제출 전에 scope를 줄일 수 있다.
2. P0 EC2/VPC는 선택 region 하나만 조회한다. Route 53 coverage와 regional target resolution coverage를 따로 표시한다.
3. 40x12까지 card layout을 제공하고 그보다 작은 terminal은 plain command loop를 사용한다.
4. resource identity와 profile-scoped observation을 분리해 권한·시각이 다른 결과를 합성하지 않는다.
5. exact Route 53 record는 P0부터 bounded cursor pagination을 사용한다.
6. ambient credential과 named-profile worker를 분리하고 `credential_source=Environment`를 별도 분류한다.
7. `bb aws query`가 non-interactive JSON 범위를 명시하고 non-TTY browse는 prompt나 AWS call을 시작하지 않는다.

이 판정에 따라 zero-call Home, narrowed SDK interface, cancellation, scope-aware store와 production CLI wiring이 구현됐다. 자동 Linux PTY process check, skip-free guard, release CI test/vet/AWS-browser race, 네 release target size check도 통과해 커밋됐다. direct tmux/interactive resize 관찰은 선택적 수동 항목이고, owner-approved 12-profile real AWS latency·identity·CloudTrail 증거는 외부 acceptance다. 후자를 자동화 증거로 대체하거나 현재 상태를 release/실계정 완료로 판정하지 않는다.

## 검토 루프 3: CLI-only 기준의 1차 blocker audit

- UX audit: 핵심 EC2→SG/EBS/IAM과 cross-profile domain/role 흐름에 blocker 없음.
- Architecture audit: 당시 CLI-only transport와 custom endpoint 격리 계약 기준으로 blocker 없음.
- 이 판정은 이후 hybrid 전환으로 transport 부분이 supersede됐다. 요구사항과 UX 결정은 유지하고 credential·endpoint·test 계약을 검토 루프 5에서 다시 열었다.

## 검토 루프 4: 화면 시뮬레이션으로 실행 순서의 빈틈을 닫았다

[동작 시나리오](SCENARIOS.md)를 추가한 뒤 UX, architecture, repository test 관점에서 다시 독립 검토했다. 화면만 읽을 때 보이지 않던 호출 순서와 lifecycle 충돌을 다음처럼 고쳤다.

| 발견 | 수정한 계약 | 검증 지점 |
|---|---|---|
| Home 0-call과 credential 초기화 충돌 | 첫 frame 뒤 첫 AWS action에서 credential bridge 시작 | Home model 첫 `View()`까지 CLI/SDK 0-call |
| STS와 provider 병렬 실행 시 account key 미확정 | credential export → SDK STS → provider 순서 | STS보다 provider가 먼저 실행되지 않음 |
| Home의 profile count가 CLI discovery를 선행시킴 | Home은 `scope on open`, overlay에서 network-free discovery | Home process 0, submit 전 AWS API 0 |
| role 진입 때 attached/inline 목록을 모두 preload | 각 policy tab을 처음 열 때 해당 목록만 조회 | 열지 않은 policy tab call 0 |
| `not loaded` tab 아래 policy row가 먼저 보임 | Summary와 loaded policy tab frame을 분리 | Summary에는 policy row 0개 |
| Attached 목록이 `GetPolicy` 전 default version을 표시 | 목록에서는 name/ARN/type만 표시 | version은 policy open 뒤 표시 |
| parent/child hosted zone에서 longest zone만 조회 | suffix가 맞는 모든 zone을 longest-first 조회 | parent/public/private 동명 fixture |
| 공유 query를 route pop 하나로 취소 가능 | 마지막 subscriber가 사라질 때만 SDK request와 credential child cancel | 두 subscriber 중 하나 pop 후 query 유지 |
| progressive page의 partial 의미가 모호함 | 완료 page는 보존, in-flight 불완전 page만 폐기 | page 2 cancel 뒤 page 1 유지 |
| 일반 loading과 search overlay의 `Esc` 충돌 | category는 현재 route 유지, submitted search는 overlay close | 상태별 key model test |
| 실행 중 작은 terminal로 plain 전환 lifecycle 없음 | 시작 크기만 plain 선택, resize는 안내 view | initial mode와 resize golden 분리 |
| list 전용 printable filter가 viewer search와 충돌 | row filter와 document find로 view별 정의 | Tag/policy `?`와 text 입력 test |
| `r refresh`가 printable filter와 충돌 | refresh는 `Ctrl+R`만 사용 | keymap golden과 generic selector 회귀 |
| top-level SG의 빈 attachment가 완전한 결과처럼 보임 | tab open 시 EC2 usage 조회 후 coverage 문구 표시 | cache 없음/empty/denied fixture |
| 현재 개발 환경 2.34.58이 문서의 2.36.18 하한보다 낮음 | patch 하한을 제거하고 credential export capability로 판정 | 2.34.58 credential fixture |
| stdout pipe를 non-TTY 예시로 사용 | stdin 또는 stderr의 실제 non-TTY와 exit 2로 고정 | stdin `/dev/null`, stdout empty, stderr usage |

당시 repository test 검토는 CLI resource allowlist를 확인했다. Hybrid 전환 뒤에는 SDK resource 동작을 in-process narrowed fake client로, CLI argv·environment·cancel을 browser 전용 helper subprocess로 나눠 검증한다. 이후 util-linux `script` 기반 Linux PTY process check가 추가되어 alt-screen lifecycle, cancel, narrow-startup fallback, stdout/stderr, non-TTY 경계를 자동 검증한다. direct tmux와 실행 중 interactive resize 관찰은 선택적 수동 acceptance로 남는다.

이 루프의 UI 판정은 유지하지만 transport 최종 판정은 아래 검토 루프 5로 대체한다.

## 검토 루프 5: AWS SDK와 CLI의 책임을 다시 나눴다

### 비교 결과

- 2026-08-27 개발 환경에서 network-free AWS CLI command 10회는 총 4.47초, 호출당 약 447ms였다. Network benchmark는 아니지만 profile fan-out마다 process/config 해석 비용이 반복됨을 보여준다.
- AWS SDK for Go v2는 connection/client 재사용, context cancellation, standard retry, paginator, typed Smithy error를 제공한다.
- AWS CLI는 이미 released `bb assume`, profile discovery, SSO login, `configure export-credentials`를 제공한다.
- 전부 CLI는 resource operation마다 process·JSON·pagination 비용이 반복되고, 전부 SDK는 기존 인증 UX를 중복 구현한다.

따라서 [ADR-001](ADR-001-HYBRID-AWS-ACCESS.md)은 CLI control-plane + SDK data-plane을 선택했다. CLI resource fallback은 두 구현의 의미와 test를 갈라놓으므로 금지한다.

### 독립 architecture·dependency·repository 재검토

| 발견 | 판정 | 반영 |
|---|---|---|
| environment credential이 explicit shared profile보다 우선 | named profile 오인증 blocker | 모든 context를 direct argv credential bridge로 통일, `WithSharedConfigProfile` credential 선택 금지 |
| 존재하지 않는 endpoint `LoadOptions` field 문구 | compile/security blocker | `cfg.BaseEndpoint=nil`, custom `IgnoreConfiguredEndpointsProvider`, default resolver, poison test로 교체 |
| credential refresh 뒤 profile account가 바뀔 수 있음 | canonical cache 오염 blocker | credential generation과 STS identity binding, 변경 response commit 금지 |
| 기존 `App.command`는 context 없음 | cancel contract blocker | released seam 유지, browser 전용 context-aware CLI executor와 SDK factory 주입 |
| SDK가 `App.env`가 아닌 process env를 읽음 | test/profile 격리 blocker | credential은 CLI bridge, SDK factory injection, `os.Setenv` 금지 |
| SDK credential cache를 이중 wrapping할 수 있음 | export 중복·refresh 불명확 | context당 `LoadDefaultConfig` 1회, loader의 cache 한 번만 사용 |
| profile UI에서 `bb aws sso <profile>` 안내 | 잘못된 복구 명령 | `bb profile login <profile>`로 수정 |
| binary 측정에 합격선 없음 | gate가 결정 불가 | stripped binary 40 MiB hard cap, baseline 5,361,794 bytes |
| `credential_process` 내부 network | bb endpoint 통제 밖 | profile 소유자의 credential trust boundary로 명시 |

당시 최종 기획 blocker는 0건이었고 endpoint source compile, poison listener 0회, credential generation rebinding은 이후 Phase 0 구현과 자동화에서 증명됐다. 승인된 실계정 12-profile latency·identity·CloudTrail 검토는 이 자동 증거와 별개의 외부 acceptance다.

## 구현 영향 파일과 회귀 범위

| 영역 | 현재 파일 | 변경 이유 | 필수 회귀 |
|---|---|---|---|
| AWS routing | `internal/bb/aws.go`, `internal/bb/app.go` | `browse` 교체, scoped `query` 추가 | help/unknown/non-TTY |
| AWS browser | `internal/bb/aws_browse.go`, 새 `internal/bb/awsbrowser/*` | preload graph를 async provider로 교체 | lazy/cancel/history/render |
| AWS dependencies | `go.mod`, `go.sum` | SDK core/config/STS/EC2/IAM/Route 53/Smithy 추가 | pinned version compile, license, binary 40 MiB |
| AWS runtime | 새 browser runtime/credential/SDK factory | context-aware CLI bridge와 profile-scoped SDK clients | env poison, expiry, generation, cancel |
| SDK providers | 새 service read interfaces와 mapper | typed SDK output을 browser model로 변환 | compile assertion, fake client, paginator/error |
| Completion | `internal/bb/completion.go`, `completion_test.go` | query grammar와 scope option | zsh candidates |
| Context | `profile.go`, `assume.go`, `assume_test.go` | browser-only discovery와 credential bridge, 기존 명령은 참고만 함 | released command 무변경 |
| App contract | `app_test.go`, `identity_test.go` | stderr/stdout, env 격리 | shell/JSON identity |
| Shared selector | `select.go`와 기존 tests | AWS 전용 model 분리 | 비-AWS selector 무변경 |

## 구현과 함께 supersede한 기존 문서

2026-08-28 documentation alignment에서 다음 기존 문서의 preload/single-account 결정을 `aws-plan/` 기준으로 갱신했다. 기존 rationale은 decision log에서 superseded 상태로 보존한다.

- root `DESIGN.md`
- `docs/product-aws-resource-browser-202608.md`
- `docs/design-aws-resource-browser-202608.md`
- `docs/commands.md`
- `docs/architecture.md`
- `docs/internals.md`
- `docs/decision-log.md`
- `README.md`, `CHANGELOG.md`

기존 unreleased `internal/bb/aws_browse.go`와 test도 새 architecture에 맞춰 교체하며, 사용자 소유의 다른 working-tree 변경은 건드리지 않는다.
