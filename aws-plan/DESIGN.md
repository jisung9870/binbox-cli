# AWS Browse v2 Design: Console의 정보 구조를 터미널에 맞게 재구성한다

관련 문서: [PRD](PRD.md) · [동작 시나리오](SCENARIOS.md) · [아키텍처](ARCHITECTURE.md) · [ADR-001](ADR-001-HYBRID-AWS-ACCESS.md) · [구현 작업 방식](IMPLEMENTATION-WORKFLOW.md) · [검토 기록](REVIEW.md) · [인덱스](README.md)

## Source of truth

- Status: Implementation, automated Linux PTY, and automated release gates complete; optional tmux/interactive resize observation and owner-approved real AWS/CloudTrail acceptance remain
- Last refreshed: 2026-08-28
- Primary product surfaces: `bb aws browse` 전용 TUI, plain terminal fallback, scoped human/JSON query
- Evidence reviewed: `internal/bb/aws_browse.go`, `internal/bb/aws_query.go`, `internal/bb/awsbrowser/*`, `internal/bb/awsbrowser/providers/*`, `internal/bb/awsbrowser/integration/*`, root `DESIGN.md`, `docs/product-aws-resource-browser-202608.md`, `docs/design-aws-resource-browser-202608.md`, AWS SDK for Go v2·AWS CLI·Route 53·EC2·IAM 공식 문서, 독립 UX·아키텍처·dependency·코드 감사
- Supersedes for the AWS surface: root `DESIGN.md`의 preloaded graph, one-list staged selector, 50열 미만 metadata 숨김 계약

## Brand

- Personality: AWS Console보다 빠르고, AWS CLI 원문보다 연결 관계가 명확한 운영자용 inspector.
- Trust signals: `READ ONLY`, account/profile/principal/region, 조회 시각, exact/inferred, searched/not searched coverage가 항상 보인다.
- Avoid: dashboard 장식, AWS Console의 모든 메뉴 복제, resource count를 위한 선행 조회, 색상만으로 상태 구분, 의미값을 숨기는 ellipsis.

## Product goals

- Goals: 서비스 카테고리를 즉시 열고, 필요한 데이터만 가져오며, EC2↔network/storage/IAM/DNS 관계를 같은 history 안에서 이동한다.
- Non-goals: resource mutation, persistent inventory, mouse-first console clone, 모든 AWS service 지원, effective permission 계산.
- Success signals:
  - Home 첫 frame은 구현 목표 250ms 이내이고 초기 AWS CLI process와 SDK request는 0회다.
  - EC2 category 이후 instance → Security → SG full inbound rule까지 검색 입력을 제외하고 Enter 3회 이내다.
  - instance → IAM → role → policy document까지 role 재입력 없이 Enter 4회 이내다.
  - domain/role query를 한 번 제출하면 console/profile 전환 없이 모든 discovered profile 결과가 current-first로 streaming된다.

위 시간과 interaction 수는 2026-08-27 설계 목표이며 구현 후 PTY·fixture로 검증한다.

## Personas and jobs

- Primary personas: 여러 AWS account/role을 관리하는 인프라 운영자 한 명.
- User jobs: resource 소유 account 찾기, instance 연결 구성 확인, SG rule 읽기, EBS 확인, instance role과 policy 추적, DNS target 확인.
- Key contexts of use: 장애 중 빠른 확인, 변경 전 영향 범위 검토, 좁은 tmux pane, SSO가 일부 만료된 multi-profile 환경.

## Information architecture

- Primary navigation: Home service catalog → resource list → detail tabs → relation target. Cross-profile search와 context overlay는 어느 화면에서도 연다.
- Core routes/screens:
  - Home
  - EC2 Instances / Volumes / Security Groups
  - Route 53 Hosted Zones / Records / Domain Search
  - IAM Roles / Policies
  - VPC & Networking
  - Cross-profile Search
  - Profile Coverage / Errors
- Content hierarchy: breadcrumb/context → current route title/state → list/filter → detail/tab → footer help.

```text
AWS Browser (READ ONLY)
├─ Home
│  ├─ EC2
│  ├─ Route 53 / Domain Search
│  ├─ IAM Roles
│  ├─ VPC & Networking
│  └─ Cross-profile Search
├─ Resource list
├─ Resource detail
│  ├─ Overview
│  ├─ service-specific tabs
│  ├─ Relations
│  └─ Tags
└─ Relation target → another resource detail
```

이 구조에서 볼 것은 service root로 돌아가 ID를 재검색하지 않고 relation target을 새 route로 push한다는 점이다.

## Design principles

- Immediate shell: Home은 로컬 정보만으로 먼저 그린다. account identity는 `unresolved`로 두고 첫 AWS action에서만 검증한다.
- Fetch follows intent: category, tab, relation을 열 때만 대응 provider를 호출한다.
- Summary is not loss: 목록은 줄일 수 있지만 상세 viewer는 ARN, CIDR, rule, tag, policy 원문을 잃지 않는다.
- Context is data: account/profile/principal/region은 header 장식이 아니라 모든 resource identity와 history의 일부다.
- Partial beats blocked: 성공 결과는 느리거나 실패한 profile을 기다리지 않고 바로 보인다.
- Back means restore: 이전 화면의 query, cursor, scroll, selected tab을 복구한다.
- Console IA, terminal interaction: 서비스→목록→상세→관계 구조는 따르되 web navigation chrome은 복제하지 않는다.
- Tradeoff: AWS surface는 generic selector와 시각·구현 일관성이 줄지만, async loading과 structured detail의 정보 완독성을 얻는다.

## Visual language

- Color: 기존 adaptive terminal palette와 `NO_COLOR`를 재사용한다. accent 하나, warning/error/selected는 텍스트 marker와 함께 쓴다.
- Typography: terminal-native text. ARN/ID/policy JSON은 고정폭 그대로이며 별도 font/icon 의존성이 없다.
- Spacing/layout rhythm: 한 줄 context header, 한 줄 route/filter, content panes, 한 줄 status/footer. border보다 정렬과 whitespace를 우선한다.
- Shape/radius/elevation: full-screen alt-screen surface. pane 구분선은 충분한 폭에서만 사용한다.
- Motion: spinner는 현재 요청에만 사용한다. 결과 append 외 장식 animation은 없다.
- Imagery/iconography: ASCII-safe marker를 기본으로 하고 Unicode arrow는 선택적이다.

## Components

### Existing components to reuse

- Bubble Tea lifecycle와 cancellation message pattern.
- 기존 adaptive color, `safeTerminalText`, `NO_COLOR`, alt-screen cleanup 계약.
- 기존 `bb assume`/SSO의 direct argv와 stdout/stderr 분리 원칙. Browser는 이를 그대로 확장하지 않고 context-aware credential bridge seam을 별도로 둔다.

### New/changed components

- `awsBrowserModel`: route stack, focused pane, current context, in-flight request registry를 소유한다.
- `serviceCatalog`: 네트워크 요청 없이 category와 `Not loaded/Cached/Loading/Denied` 상태를 표시한다.
- `resourceTable`: structured columns, local filter, pagination/loading footer.
- `detailPane`: service별 tab과 relation affordance를 렌더링한다.
- `documentViewer`: policy JSON, trust policy, 긴 tag value를 wrap·scroll·검색한다.
- `ruleViewer`: SG ingress/egress를 source 단위 normalized row/card로 표시한다.
- `crossProfileSearchOverlay`: query type, scope, profile progress, streaming result를 표시한다.
- `contextOverlay`: profile/account/role/region을 확인하고 현재 탐색 context를 전환한다.
- `statusCenter`: category/profile별 denied, login required, timeout, throttled 상태를 연결한다.
- `sdkContextFactory`: context별 credential provider, SDK config/cache, narrowed read client를 한 번 만들고 재사용한다.

### Variants and states

- resource row: normal, selected, stale, inferred relation, unavailable.
- loader: idle/not loaded, queued, loading, partial, ready, stale, failed, cancelled.
- cross-profile result: exact, inferred, duplicate provenance, profile failure.
- detail value: scalar, relation, collection, sensitive-omitted. P0 AWS browse에는 credential/secret value가 들어오지 않는다.

### Token/component ownership

공통 terminal token은 root design을 재사용하되 AWS layout·tab·table·viewer는 `awsbrowse` package/model이 소유한다. generic `selectStage`를 async framework로 확장하지 않는다.

## Core screens

### Home은 category를 보여주기 위해 AWS를 조회하지 않는다

```text
┌ AWS Browser · READ ONLY ────────────────────────────────────────────┐
│ Profile dev  Account unresolved  Principal unresolved  ap-northeast-2 │
├ Services / tasks ───────────────────────────────────────────────────┤
│ > EC2 Instances                         Not loaded                  │
│   Route 53 Hosted Zones                 Not loaded · AWS global    │
│   IAM Roles                             Not loaded · AWS global    │
│   VPC & Networking                      Not loaded                  │
│   Cross-profile search                  Domain, role · scope on open│
├─────────────────────────────────────────────────────────────────────┤
│ ↑↓ move  enter open  ctrl+g cross-profile  ? help  ctrl+c quit     │
└─────────────────────────────────────────────────────────────────────┘
```

Home에서 count를 표시하려고 AWS CLI process나 resource API를 호출하지 않는다. 이전에 현재 session에서 읽은 값이 있을 때만 `Cached 42 · 14:32:08`처럼 표시한다. profile 수는 cross-profile overlay를 열고 network-free discovery가 끝난 뒤 표시한다.

### EC2는 넓은 화면에서 목록과 상세를 함께 본다

```text
AWS > 123456789012/dev > ap-northeast-2 > EC2       Cached 14:32  Ctrl+R refresh
Filter: web_
┌ Instances (3/42) ─────────────────┬ web-api-01 · i-0123 · running ────────┐
│ > web-api-01  running  t3.large   │ Overview Network Storage Security      │
│   web-api-02  running  t3.large   │ IAM Tags(12)                           │
│   web-batch   stopped  m6i.large  │                                        │
│                                   │ AZ          ap-northeast-2a             │
│                                   │ Private IP  10.0.1.24                   │
│                                   │ VPC         vpc-01a9…   enter open      │
│                                   │ SG          web-prod    enter open      │
│                                   │ EBS         2 volumes   enter open      │
│                                   │ Role        web-runtime enter open      │
└───────────────────────────────────┴────────────────────────────────────────┘
```

목록의 ID는 폭 때문에 줄일 수 있지만 선택한 resource detail의 ID/ARN에는 무손실 viewer로 가는 `open` affordance가 있어야 한다.

오른쪽 pane은 선택 row의 read-only preview다. `Enter`가 instance detail route를 push한 뒤 `Tab/Shift+Tab`은 detail tab을 이동하고, `↑↓`는 현재 tab의 relation row를 이동하며, `Enter`는 relation target을 연다. SG 검증 sequence는 `Enter instance → Tab Security → Enter SG`이고 Inbound tab이 기본이다. IAM 검증 sequence는 `Enter instance → Tab IAM → Enter role → Tab Attached/Inline → Enter policy`이며 document가 기본 view다.

### SG rule은 넓으면 table, 좁으면 card다

```text
AWS > … > i-0123 > sg-0789       Inbound | Outbound | EC2 instances only | Tags
Rule 2/7
Protocol     TCP
Port range   443
Source       sg-0444 / api-alb          enter open source SG
Description  HTTPS from production ALB
Rule ID      sgr-0abc1234
```

- source가 여러 개인 permission은 source 한 개당 row로 정규화한다.
- CIDR, IPv6, prefix list, referenced SG, port, description, rule ID를 detail에서 `…`로 자르지 않는다.
- referenced SG는 relation target이다. 외부 account SG면 account ID를 표시하고 이동 불가 이유를 설명한다.
- 빈 attachment 상태는 `No attached EC2 instances; other service attachments not checked`로 표시한다.

### Tags는 선택해야 열리고, 열리면 전부 보인다

```text
Tags (14)                                              Filter: owner
> owner          platform-observability
  cost-center    shared-infra-and-security
  migration      2026-q3-wave-very-long-value-that-wraps-across-lines
```

Tag는 Overview에서 `Tags (N)`만 표시한다. viewer는 key/value 검색, wrap, vertical scroll을 제공한다. Clipboard integration은 P1이다.

### IAM policy는 effective permission처럼 보이지 않게 한다

```text
Role web-runtime
Summary | Attached (not loaded) | Inline (not loaded) | Trust | Tags

ARN          arn:aws:iam::123456789012:role/web-runtime
Path         /service/
Last used    2h ago
Policies     not loaded: open Attached or Inline
```

Attached tab을 처음 열어 목록을 받은 뒤에만 policy row를 표시한다.

```text
Role web-runtime
Summary | Attached (3) | Inline (not loaded) | Trust | Tags

> AmazonS3ReadOnlyAccess     AWS managed
  app-secrets-read           Customer
```

policy row를 열 때만 document를 가져온다. 화면에는 `Policy document · not an effective-permission evaluation`을 표시한다.

### Cross-profile search는 제출 후에만 profile을 fan-out한다

```text
Cross-profile search > Domain
Query  api.example.com
Scope  All configured profiles (12)   Type Exact domain  change scope

Results 3 · searching 2 · login required 1 · denied 1
> api.example.com A      123456789012/dev   AWS global  10.0.1.24
  api.example.com Alias  999999999999/prod  AWS global  dualstack.…elb…

Profiles
✓ dev      123456789012  1 match  0.8s
… audit    searching
! legacy   SSO login required
× locked   route53:ListHostedZones denied
```

- 첫 결과는 전체 fan-out 완료를 기다리지 않는다.
- submitted search가 실행 중이면 `Esc`는 search subscriber를 취소하고 overlay 이전 화면으로 돌아간다. 제출 전 draft query나 resource list의 local filter에서는 `Esc`가 text를 먼저 지운다.
- `not found`, `not searched`, `denied`, `login required`를 별도 집계한다.
- `all`은 AWS CLI가 발견한 local profile 전체를 뜻한다. AWS Organizations 전체 account coverage를 뜻하지 않는다.
- Route 53 coverage와 regional target-resolution coverage를 따로 표시한다.

## Keyboard and focus

- `↑↓`, `PgUp/PgDn`, `Home/End`: row 이동.
- `Enter`: list의 resource detail route 또는 detail의 선택 relation target을 연다. Preview pane은 focus를 받지 않는다.
- printable typing: 현재 view의 검색을 시작한다. list, rule, tag view에서는 row filter이고 policy, trust document에서는 text find다. 검색 중에는 일반 문자를 command로 해석하지 않는다.
- `Esc`: view-local query가 있으면 clear한다. category/detail loading이면 request subscriber를 취소하고 현재 route를 유지한다. submitted cross-profile overlay이면 search를 취소하고 overlay 이전 화면으로 돌아간다. 그 외에는 history back이며 root idle에서는 exit다.
- `Tab` / `Shift+Tab`: detail tab 이동. `[`/`]`와 중복 계약을 만들지 않는다.
- `Ctrl+G`: cross-profile search.
- `Ctrl+R`: 기존 data를 유지한 채 현재 view를 refresh한다.
- `Ctrl+C`: 실행 중 SDK request와 active credential export child를 취소하고 즉시 종료.
- `?`: idle view에서는 현재 focus 기준 help overlay다. filter나 document find 편집 중에는 검색 문자로 입력된다. help overlay의 `Esc`는 overlay만 닫는다.

Focus는 marker와 `LIST`, `FILTER`, `DETAIL` 텍스트를 함께 사용한다. key help는 현재 상태에서 동작하는 명령만 보여준다.

## Accessibility

- Target standard: terminal 제약 안에서 WCAG 2.2 AA 대비와 비색상 상태 표현을 목표로 한다.
- Keyboard/focus behavior: 모든 기능은 keyboard로 도달 가능하고 focus·back stack을 예측할 수 있어야 한다.
- Contrast/readability: 선택은 `>`와 style, 오류는 `! denied`처럼 marker와 단어를 함께 쓴다.
- Screen-reader semantics: 실제 character-device TTY에서 `BB_SELECTOR=plain`을 지정하면 `open <n>`, `back`, `refresh`, `quit` command loop로 관계 탐색을 지원한다.
- Reduced motion and sensory considerations: `NO_COLOR`, ASCII fallback, 최소 spinner, 깜빡임 없는 loading text.

## Responsive behavior

- Supported breakpoints/devices:
  - 120열 이상: service sidebar + resource list + detail의 3-pane.
  - 80–119열: list + detail 2-pane, service는 route/home으로 이동.
  - 40–79열: 한 화면에 list 또는 detail card 하나, 값 wrap, footer 두 줄.
  - 시작 terminal이 40x12 미만: alt-screen 진입 전 plain command loop.
- Layout adaptations: 화면 폭이 줄면 pane을 route로 바꾸지, critical metadata를 숨기지 않는다.
- Resize behavior: TUI 실행 중 40x12 미만으로 줄어들면 `Terminal too small (need 40x12). Resize or rerun with BB_SELECTOR=plain.`을 표시하고 진행 중 request와 route를 유지한다.
- Touch/hover differences: 지원하지 않는다. mouse는 P2 이후 별도 검토다.

## Interaction states

- Loading: `Loading EC2 for dev/ap-northeast-2… 1.4s · Esc cancel`.
- Empty: `0 instances in this region`처럼 성공한 빈 결과임을 표시한다.
- Filter empty: `No matches for “web”`로 source empty와 구분한다.
- Error: 해당 category/profile 안에 inline 표시하고 header warning badge와 연결한다.
- Success: `42 resources · fetched 14:32:08`을 표시한다.
- Disabled: 열 수 없는 inferred target은 이유를 detail에 표시한다.
- Offline/slow network: cached data가 있으면 `Showing cached 42 · refresh failed`로 유지한다.
- Partial cross-profile search: `17 results · 8/12 complete · 2 denied · 1 login required · 1 searching`.
- Cancelled: 이전 성공 data를 유지하고 진행 중 badge만 제거한다.

## Content voice

- Tone: 짧고 진단 가능하게 쓴다.
- Terminology: `AWS profile`, `account`, `principal`, 검증된 경우의 `role`, `region`, `AWS global`, `cross-profile`, `context`, `exact`, `inferred`, `not searched`.
- Avoid terminology: 구성된 검색 대상을 명사 `assume`로 부르지 않는다. `not found`를 권한 실패에 사용하지 않는다.
- Microcopy rules: 상태 + 원인 + 다음 안전 행동 순서로 쓴다. Profile 이름을 아는 경우 `SSO login required: bb profile login corp`를 쓴다. `bb aws sso`는 SSO session 이름이 확인된 경우에만 안내한다.

## Implementation constraints

- Framework/styling system: Go, Bubble Tea v2, Lip Gloss v2. AWS surface는 dedicated model을 사용한다.
- Design-token constraints: common color와 safe text만 공유한다. generic selector의 96열 cap과 description hiding은 가져오지 않는다.
- Performance constraints: Home은 CLI process와 SDK request에 독립적이고 local filter는 network-free다. Resource work는 caller context를 받는 SDK request로, credential export는 context-aware child로만 시작한다.
- Compatibility constraints: TUI는 stderr, machine output은 stdout을 쓴다. Browser runtime의 AWS CLI v2 capability는 profile discovery·credential export로 제한되고, AWS SDK for Go v2는 STS·EC2·IAM·Route 53 transport·retry·typed error를 맡는다. 기존 SSO login은 별도 호환 surface다. Provider가 paginator/cursor와 성공 page commit을 소유하며 CLI resource fallback은 없다. resource mutation은 없다. non-TTY `bb aws browse`는 AWS 호출과 prompt 없이 `bb aws query` 안내를 반환한다.
- Test/screenshot expectations: 120x30, 80x24, 50x16, 40x12 golden; loading/cancel/resize/back-stack model test; fake narrowed SDK client와 credential bridge fixture; endpoint poison test; `NO_COLOR`; forced-plain TTY와 non-TTY/EOF 분리; alt-screen cleanup; control-character sanitization.

## Open questions

- [ ] 실계정 smoke 후 120열 3-pane의 service/list/detail 폭 비율을 조정할지 / owner / 긴 resource name과 policy navigation 밀도에 영향.
