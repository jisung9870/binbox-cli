# AWS Browse v2 동작 시나리오

독자        저장소 소유자와 구현·검토 담당자
목적        주요 조사 흐름에서 사용자가 누르는 키, 화면 전이, AWS 호출 시점을 구현 가능한 예시로 고정한다
대상 환경   AWS SDK for Go v2, credential export capability를 통과한 AWS CLI v2, 여러 AWS CLI profile, 120x30 기준 TTY
최종 검토   2026-08-28
다음 검토   선택적 tmux/interactive resize 관찰 및 real AWS smoke 결과 시
상태        model/golden·자동 Linux PTY·자동 release gate 완료, 수동/외부 acceptance 대기

관련 문서   [PRD](PRD.md) · [설계](DESIGN.md) · [아키텍처](ARCHITECTURE.md) · [ADR-001](ADR-001-HYBRID-AWS-ACCESS.md) · [구현 작업 방식](IMPLEMENTATION-WORKFLOW.md) · [검토 기록](REVIEW.md) · [인덱스](README.md)

아래 화면의 account ID, profile, resource 이름, 시간은 예시다. 화면 전이와 호출 조건은 구현 계약이다. 모든 흐름은 read-only이며 TUI에서 context를 열어도 parent shell의 AWS credential은 바뀌지 않는다.

## 시나리오 0: 실행하면 데이터 수집 없이 Home이 열린다

```console
$ bb aws browse --profile dev --region ap-northeast-2
```

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

이 첫 frame까지 AWS CLI process와 SDK request는 0회다. 사용자가 `EC2 Instances`를 열면 named `dev` credential export를 한 번 실행한 뒤 SDK `GetCallerIdentity`, SDK `DescribeInstances`를 순서대로 실행한다. `--profile`이 없는 ambient 진입은 profile 인자 없는 credential export를 사용한다. Cross-profile search를 열면 network-free profile discovery만 실행하고 검색 제출 전에는 credential export, STS, resource API를 호출하지 않는다.

## 시나리오 1: EC2 하나에서 SG, EBS, IAM policy를 이어서 확인한다

### 1. EC2 category를 연다

사용자 입력: Home의 `EC2 Instances`에서 `Enter`.

```text
AWS > dev > ap-northeast-2 > EC2

Loading instances for dev/ap-northeast-2... 0.4s · Esc cancel
```

credential export와 SDK `sts:GetCallerIdentity`가 성공한 뒤 처음 실행하는 resource 조회는 SDK `ec2:DescribeInstances`다. 선택하지 않은 Volumes, Security Groups, Route 53, IAM Roles, VPC category는 조회하지 않는다. STS·EC2 resource operation용 AWS CLI subprocess는 실행하지 않는다.

결과가 도착하면 선택 row의 preview부터 보인다.

```text
AWS > 123456789012/dev > ap-northeast-2 > EC2       Fetched 14:32:08
┌ Instances (3/42) ─────────────────┬ web-api-01 · i-0123456789abcdef0 ┐
│ > web-api-01  running  t3.large   │ Overview Network Storage Security│
│   web-api-02  running  t3.large   │ IAM Tags(12)                      │
│   web-batch   stopped  m6i.large  │                                   │
│                                   │ Private IP  10.0.1.24             │
│                                   │ VPC         vpc-01a9...           │
│                                   │ SG          web-prod              │
│                                   │ EBS         2 volumes             │
│                                   │ Role        web-runtime           │
└───────────────────────────────────┴───────────────────────────────────┘
```

오른쪽 pane은 preview라서 focus를 받지 않는다. 사용자가 `Enter`를 누르면 instance detail route가 history에 추가된다.

### 2. Security tab에서 SG를 연다

사용자 입력: `Enter instance` → `Tab`으로 `Security` 이동 → SG row에서 `Enter`.

```text
AWS > ... > i-0123456789abcdef0 > sg-0789abcdef0123456
Inbound | Outbound | EC2 instances only | Tags

Rule 2/7
Protocol     TCP
Port range   443
Source       sg-0444abcdef0123456 / api-alb
Description  HTTPS from production ALB
Rule ID      sgr-0abc1234def567890

enter open source SG · tab next view · esc back
```

SG를 열 때 선택한 ID로 `DescribeSecurityGroups`를 호출하고, 기본 `Inbound` view를 그릴 때 `DescribeSecurityGroupRules`를 호출한다. source가 여러 개인 permission은 source 하나당 card 또는 table row 하나로 나눈다. CIDR, IPv6, prefix list, referenced SG, port, description, rule ID는 상세 화면에서 자르지 않는다.

`EC2 instances only`는 attachment coverage 제한이다. 연결된 EC2가 없어도 `다른 서비스에도 미사용`이라고 판단하지 않는다.

### 3. 긴 Tag 값을 별도 viewer에서 읽는다

사용자 입력: `Tab`으로 `Tags` 이동 → `Enter`.

```text
Tags (14)                                      Filter: owner
> owner          platform-observability
  cost-center    shared-infra-and-security
  migration      2026-q3-wave-very-long-value-that-wraps-
                 across-lines-without-losing-characters

type filter · ↑↓ scroll · esc back
```

Overview와 list에는 `Tags (14)`만 표시한다. viewer는 key와 value를 모두 검색하고 긴 값은 다음 줄로 감싼다. P0는 clipboard 기능을 넣지 않는다.

### 4. 같은 instance에서 EBS를 확인한다

사용자 입력: `Esc`로 instance detail까지 돌아감 → `Storage` tab → volume row에서 `Enter`.

```text
AWS > ... > i-0123456789abcdef0 > vol-0123456789abcdef0
Overview | Attachments | Tags

State        in-use
Type         gp3
Size         200 GiB
IOPS         6000
Throughput   250 MiB/s
Encrypted    yes
KMS key      arn:aws:kms:ap-northeast-2:123456789012:key/...
Device       /dev/xvda
```

이 시점에 선택한 volume ID로 `DescribeVolumes`를 호출한다. EC2 목록을 열었다는 이유로 account의 전체 volume 목록을 미리 가져오지 않는다.

### 5. instance role에서 policy document까지 이동한다

사용자 입력: `Esc`로 instance detail까지 돌아감 → `IAM` tab → role row에서 `Enter`.

화면은 role을 여는 동안 내부 조회 단계를 짧게 표시한다.

```text
Resolving instance profile web-api-profile...
Loading role web-runtime...
```

내부 호출 순서는 다음과 같다.

```text
GetInstanceProfile
→ GetRole
```

```text
AWS > ... > i-0123456789abcdef0 > role/web-runtime
Summary | Attached (not loaded) | Inline (not loaded) | Trust | Tags

ARN          arn:aws:iam::123456789012:role/web-runtime
Path         /service/
Last used    2h ago
Policies     not loaded: open Attached or Inline
```

사용자 입력: `Tab`으로 `Attached` 이동. 이때 `ListAttachedRolePolicies`를 실행해 목록과 count를 채운다. `Inline` tab은 처음 열 때 `ListRolePolicies`를 실행한다.

```text
AWS > ... > role/web-runtime > Attached
Summary | Attached (3) | Inline (not loaded) | Trust | Tags

> AmazonS3ReadOnlyAccess     AWS managed
  app-secrets-read           Customer
```

사용자 입력: policy row에서 `Enter`를 눌러 document를 연다.

```text
Policy document · not an effective-permission evaluation
arn:aws:iam::123456789012:policy/app-secrets-read · default v3

{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "secretsmanager:GetSecretValue",
      "Resource": "arn:aws:secretsmanager:ap-northeast-2:123456789012:secret:app/*"
    }
  ]
}
```

managed policy는 선택 시점에만 `GetPolicy`와 default version의 `GetPolicyVersion`을 호출한다. inline policy는 선택 시점에만 `GetRolePolicy`를 호출한다. 이 화면은 SCP, permissions boundary, session policy를 합친 실제 권한 판정 결과가 아니다.

### 시나리오 1 호출 요약

| 사용자 행동 | CLI control-plane | 새 SDK resource 호출 | 호출하지 않는 것 |
|---|---|---|---|
| Home 표시 | 없음 | 없음 | 모든 inventory, STS |
| EC2 Instances 열기 | context 최초 credential export | `GetCallerIdentity`, `DescribeInstances` | Volumes/SG/IAM/Route 53 전체 목록 |
| SG 열기 | 없음 | 선택 SG의 describe와 rules | 다른 SG의 rules |
| Tag 열기 | 없음 | 기존 응답 사용 | 추가 AWS 호출 |
| EBS 열기 | 없음 | 선택 volume의 `DescribeVolumes` | 전체 volume 목록 |
| Role 열기 | 없음 | instance profile, role | policy 목록과 document |
| Attached/Inline tab 열기 | 없음 | 해당 tab의 policy 목록 | 다른 tab 목록과 document |
| Policy 열기 | 없음 | 선택 policy document | 다른 policy document |

## 시나리오 2: 도메인 소유 account를 모르면 여러 profile을 자동 검색한다

사용자 입력: 어느 화면에서든 `Ctrl+G` → type `Domain` → `api.example.com` 입력.

overlay를 열 때 network-free `aws configure list-profiles`로 scope를 채운다. 제출 전에는 STS와 AWS account/resource API를 호출하지 않는다.

```text
Cross-profile search > Domain
Query  api.example.com
Scope  All configured profiles (12)     change scope
Type   Exact domain

enter search · esc clear · tab change field
```

사용자 입력: `Enter`로 검색 확정.

```text
Cross-profile search > Domain                     Elapsed 1.1s
Query api.example.com · Exact · selected region ap-northeast-2

Results 2 · 7/12 complete · 1 denied · 1 login required · 3 searching
> api.example.com A      123456789012/dev   private  10.0.1.24
  api.example.com Alias  999999999999/prod  public   dualstack.api-prod...

Profiles
✓ dev      123456789012  1 match             0.8s
✓ prod     999999999999  1 match             1.0s
… audit    searching
! legacy   SSO login required: bb profile login legacy
× locked   route53:ListHostedZones denied
```

현재 context를 queue 첫 항목으로 처리하고 worker는 최대 4개만 동시에 실행한다. 첫 결과는 나머지 profile을 기다리지 않고 표시하지만, 동일 이름의 public/private record나 다른 account record가 있을 수 있으므로 첫 match에서 검색을 멈추지 않는다.

각 worker는 profile별 credential export를 expiration당 최대 한 번 실행한 뒤 같은 SDK config/cache로 STS와 Route 53을 호출한다. AWS CLI로 STS나 Route 53 resource operation을 실행하지 않는다.

profile별 exact search는 다음 범위만 조회한다.

1. hosted zone 목록을 session당 한 번 읽고 query suffix와 일치하는 parent/child public/private zone을 모두 찾은 뒤 longest-first로 조회한다.
2. candidate zone에서 `StartRecordName=api.example.com.`과 최대 300개 page를 사용한다.
3. 같은 record name의 type과 routing variant를 모으고 다음 record name이 달라지면 멈춘다.

결과 row를 열면 record detail과 target relation을 같은 TUI history에 추가한다. 해당 profile context는 TUI 내부에서만 사용하며 parent shell의 `AWS_PROFILE`, access key, session token을 바꾸지 않는다.

```text
AWS > 999999999999/prod > Route 53 > Z0123 > api.example.com
Record | Routing | Target | Provenance

Type          A · Alias
Zone          example.com. · Public
Target        dualstack.api-prod-123.elb.ap-northeast-2.amazonaws.com.
Confidence    inferred
Resolution    unresolved outside selected region
Observed via  prod
```

P0의 regional target resolver는 선택 region 하나만 확인한다. target을 그 region에서 찾지 못해도 다른 region 가능성이 남으면 `not found` 대신 `unresolved outside selected region`을 표시한다.

## 시나리오 3: 같은 이름의 IAM role을 모든 profile에서 찾는다

사용자 입력: `Ctrl+G` → type `IAM role` → exact name `deploy-role` → `Enter`.

```text
Cross-profile search > IAM role
Query deploy-role · Exact

Results 2 · 12/12 complete · 8 not found · 1 denied · 1 login required
> deploy-role  /platform/  123456789012/dev-admin   last used 2h ago
  deploy-role  /service/   999999999999/prod-read   last used 18d ago

Available via profiles
123456789012  dev-admin, dev-breakglass
999999999999  prod-read
```

각 profile worker는 exact name에 `GetRole`을 사용한다. `NoSuchEntity`만 `not found`로 분류한다. `AccessDenied`와 SSO 만료는 별도 coverage 상태다. 같은 account와 role을 보는 profile이 여러 개면 resource row는 하나로 합치고 `available via profiles`를 보존한다.

role result를 열 때 policy count는 아직 `not loaded`다. 사용자가 `Attached`나 `Inline` tab을 열 때만 policy 목록을 가져오고, policy row를 열 때 document를 가져온다.

부분 이름 검색은 자동으로 `ListRoles`를 실행하지 않는다. 사용자가 `deep search`를 선택하고 확인한 경우에만 선택 scope로 fan-out한다.

## 시나리오 4: 느린 profile과 권한 실패가 있어도 찾은 결과를 유지한다

검색 도중 한 profile이 throttling되고 다른 profile의 SSO가 만료된 예시다.

```text
Results 3 · 8/12 complete · 2 denied · 1 login required · 1 throttled

✓ dev       1 match
✓ prod      2 matches
! legacy    SSO login required: bb profile login legacy
× locked    route53:ListHostedZones denied
~ audit     throttled · SDK retrying · Esc cancel
```

- 한 profile의 실패는 이미 받은 결과를 지우지 않는다.
- `denied`나 `login required`를 `not found`에 포함하지 않는다.
- 검색이 끝나도 coverage가 불완전하면 결과 header에 `Partial coverage`를 남긴다.
- raw AWS payload, tag value, policy document, credential은 trace에 기록하지 않는다.

사용자 입력: 실행 중 `Esc`.

```text
Search cancelled · 3 results kept · 8/12 profiles completed
```

coordinator는 queue에 남은 worker를 시작하지 않고 실행 중 SDK request context와 active credential export child를 취소한다. 검색 overlay를 닫으면 이전 resource 화면과 back stack이 그대로 남는다.

## 시나리오 5: refresh가 실패하면 읽던 데이터를 없애지 않는다

사용자 입력: EC2 list에서 `Ctrl+R`.

```text
AWS > ... > EC2     Showing cached 42 · refreshing... · Esc cancel
```

refresh 성공 전까지 기존 목록을 보여준다. refresh가 실패하면 다음처럼 유지한다.

```text
AWS > ... > EC2     Showing cached 42 · refresh failed at 14:41:03
! Request timed out. Press Ctrl+R to retry.
```

부분 page나 decode 실패 응답으로 기존 cache를 덮지 않는다. 명시적 refresh가 성공할 때만 같은 query generation을 원자적으로 교체한다.

## 시나리오 6: 좁은 terminal과 자동화에서는 UI 계약이 달라진다

### 50x16 TTY는 한 화면에 card 하나를 보여준다

```text
AWS > EC2 > i-0123456789abcdef0
Security [4/6]

SG        web-prod
ID        sg-0789abcdef0123456
Inbound   7 rules
Outbound  3 rules

enter open · tab next · esc back
```

pane을 숨기면서 critical metadata를 버리지 않는다. list와 detail을 별도 route로 바꾸고 긴 값은 wrap한다.

### 40x12 미만으로 시작한 실제 TTY는 plain command loop를 연다

```console
$ BB_SELECTOR=plain bb aws browse
AWS Browser · READ ONLY
1  EC2 Instances        not loaded
2  Route 53             not loaded
3  IAM Roles            not loaded
4  Cross-profile search
command [open <n>|back|refresh|quit]: open 1
```

plain mode도 같은 lazy provider, history, read-only allowlist를 사용한다.

TUI 실행 중 terminal을 40x12 미만으로 resize하면 line mode로 바꾸지 않는다.

```text
Terminal too small (need 40x12).
Resize or rerun with BB_SELECTOR=plain.
```

진행 중 request와 route는 유지하며 terminal을 다시 키우면 같은 화면으로 돌아온다.

### pipe와 non-TTY는 prompt를 열지 않는다

```console
$ bb aws browse </dev/null
bb: aws browse requires an interactive TTY; use a scoped query:
  bb aws query ec2 instances --profile dev --region ap-northeast-2 --json
  bb aws query domain api.example.com --scope all --json
$ echo $?
2
```

usage와 query 안내는 stderr에 쓰고 stdout은 비운다. 이 경로는 AWS를 호출하지 않는다. 자동화는 명시적 범위의 `bb aws query`만 사용한다. stdout만 pipe하고 stdin과 stderr가 TTY이면 interactive TUI를 유지한다.

## 구현 acceptance로 바꾸는 기준

| 시나리오 | fixture 또는 model test가 증명할 것 | 실패 조건 |
|---|---|---|
| 0 Home | first frame 전 CLI process와 SDK request 0 | credential export, STS, resource call 1개 이상 |
| 1 EC2 관계 | 선택 relation만 호출, back stack과 full viewer 유지 | 전체 preload, 상세 값 손실 |
| 2 Domain | current-first, worker 상한 4, streaming, coverage | keypress fan-out, 첫 match 중단 |
| 3 Role | exact `GetRole`, provenance 보존 | `ListRoles` 자동 fan-out, account 충돌 |
| 4 Partial/cancel | 부분 결과 유지, queued/SDK request/credential child 취소 | 전체 실패 처리, request 또는 child 잔존 |
| 5 Refresh | old Ready 유지 후 atomic replace | loading 중 빈 화면, 실패 cache 덮어쓰기 |
| 6 Terminal modes | responsive golden과 non-TTY CLI/SDK 0-call | pipe prompt 대기, metadata 숨김 |

이 문서의 화면은 golden test의 초기 기준으로 사용한다. Linux PTY process test는 alt-screen lifecycle, cancel, narrow-startup fallback, stdout/stderr, non-TTY 경계를 자동 검증한다. 선택적 direct tmux/interactive resize 관찰에서 열 수, resource name 길이, NO_COLOR 가독성이 실패하면 화면 폭과 문구를 조정하되 lazy 호출, coverage, 무손실 상세, credential 격리 계약은 유지한다. 12-profile latency·identity·CloudTrail은 owner-approved real AWS 외부 acceptance다.
