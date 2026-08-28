# AWS TUI 관계 탐색 계약

독자        저장소 소유자와 relation/provider 구현자
목적        현재 구현에서 출발해 관계의 정확성·근거·우선순위·완료 조건을 고정한다
대상 환경   live AWS SDK provider와 선택적 snapshot graph
최종 검토   2026-08-28
다음 검토   B4 shortcut query 설계 시
상태        Tier 0·Tier 1-A 완료, Tier 1-B storage PoC 완료 — live sync/public query는 B4 계획

관련 문서   [확장 설계](design-aws-tui-202608.md) · [기존 아키텍처](ARCHITECTURE.md) · [기존 UX](DESIGN.md)

## 결론 — 서비스 목록이 아니라 끊기지 않는 질문 단위로 구현한다

기존 초안은 EC2, WAF, RDS, ECS, EKS, KMS 등 수십 종류의 edge를 한 번에 나열했지만 현재 provider 범위와 evidence 정확도를 반영하지 않았다. 이 문서는 관계를 네 Tier로 나누고, 한 Tier는 사용자 질문 하나를 끝까지 답할 때만 완료로 본다.

- Tier 0: 현재 provider로 이미 가능한 live chain을 계약화한다.
- Tier 1: 사용 빈도가 높은 ingress chain과 SG reverse lookup을 추가한다.
- Tier 2: snapshot 없이는 비효율적인 멀티계정 네트워크와 shortcut query를 추가한다.
- Tier 3: datastore·container·serverless·보안 진단은 실제 수요가 확인될 때 확장한다.

## 관계 한 건은 target보다 evidence가 더 중요하다

모든 relation은 다음 의미를 가진다.

```text
source_key   canonical ResourceKey
target_ref   canonical ResourceKey 또는 아직 미해결인 ARN/DNS/CIDR/account reference
kind         attached-to, member-of, alias-to, routes-to, references, trusts 등
direction    outgoing 또는 incoming query 결과
evidence_kind id-exact, api-exact, correlated, inferred, ambiguous, unsupported
condition    path pattern, host/path rule, protocol/port, CIDR, policy statement 등
evidence     service, operation, reason, observed profile/account/region, observed_at
coverage     이 결론을 만들 때 searched/succeeded/failed/not-observed scope
```

### 정확도 규칙

- 현행 `RelationKind`의 `id-exact`·`api-exact`는 exact 그룹, `correlated`·`inferred`는 heuristic 그룹, `ambiguous`·`unsupported`는 unresolved 그룹으로 화면에 요약할 수 있다. 저장·JSON 계약에서는 원래 kind를 잃지 않는다.
- `id-exact`·`api-exact`: API field가 stable ID/ARN을 직접 제공하거나 target API로 다시 확인했다.
- `correlated`·`inferred`: DNS 이름, endpoint suffix, policy 문자열처럼 규칙 기반 매칭이다.
- `ambiguous`·`unsupported`: 참조는 보이지만 owner account/region/resource를 확인하지 못했거나 아직 provider가 없다.
- `denied`, `login required`, `not searched`는 `not found`가 아니다.
- 동일 target이라도 condition이 다르면 별도 edge다. CloudFront behavior path와 SG protocol/port/source를 합치지 않는다.
- 역방향은 저장 편의를 위해 가짜 edge를 복제하는 기능이 아니다. source/target index를 반대로 조회하고 원래 evidence를 그대로 보여준다.

## Tier 0 — 현재 live chain을 정확하게 고정한다

### T0-A. EC2 구성 추적

```text
EC2 instance
├─ EBS volume
├─ Security Group → inbound/outbound rules
├─ VPC
├─ Subnet
└─ instance profile → IAM role
                     ├─ trust policy
                     ├─ attached managed policy → default policy document
                     └─ inline policy → policy document
```

완료 조건:

1. Name tag가 있으면 Name-first, 없으면 native name, 마지막으로 ID를 표시한다.
2. instance에서 각 relation category를 열 때 필요한 narrowed read만 실행한다.
3. exact linked lookup이 한 건이면 `Resources (1)` 화면 없이 target Summary로 이동한다.
4. SG rule, policy document, Tags는 별도 searchable/scrollable 화면에서 손실 없이 읽힌다.
5. Left와 `Ctrl-o`는 이전 query/cursor/scroll이 보존된 화면으로 돌아간다.

### T0-B. Domain에서 지원 origin까지 추적

```text
Route 53 hosted zone → record
record exact CloudFront alias → distribution
distribution → behavior(path condition) → origin
standard S3 origin DNS → inferred bucket → GetBucketLocation exact verification
```

완료 조건:

1. AWS CloudFront fixed alias zone과 DNS suffix가 exact일 때만 distribution lookup을 시작한다.
2. default behavior와 cache behavior별 path pattern을 별도 relation condition으로 보존한다.
3. S3 bucket은 DNS에서 inferred 상태로 시작하고 `GetBucketLocation` 성공 후에만 verified target이 된다.
4. custom/external origin은 unresolved evidence로 남기며 같은 account 소유라고 단정하지 않는다.
5. partial provider failure가 앞에서 확인된 chain을 지우지 않는다.

## Tier 1 — 운영 질문 두 개를 우선 완성한다

### T1-A. 이 domain의 실제 compute target은 무엇인가

```text
Route 53 record
→ ALB/NLB
→ listener
→ listener rule (host/path condition)
→ target group
→ target (instance/IP/lambda/ALB)
→ EC2 또는 unresolved target
```

필요한 신규 SDK surface는 ELBv2의 load balancer, listener, rule, target group, target health read다. ACM은 chain의 compute target 완성 후 별도 relation으로 추가한다.

완료 조건:

- listener priority와 rule condition 순서를 보존한다.
- target type별 identity를 구분하고 IP target을 임의 EC2로 연결하지 않는다.
- forward와 `EC2 ← target group ← rule ← load balancer ← record` reverse query가 같은 evidence를 사용한다.
- exact domain 하나를 열어 최종 target 또는 unresolved 이유까지 도달한다.

### T1-B. 이 SG를 지우거나 바꾸면 무엇이 영향받는가

```text
Security Group
├─ → attached ENI/resource
├─ → references another SG in a rule
├─ ← referenced by another SG rule
└─ ← attached resource
```

현재 EC2 provider에 ENI attachment coverage가 없으므로 `EC2 instances only`를 전체 attachment로 표현하지 않는다. ELB, RDS, Lambda, endpoint 등 서비스별 attachment가 추가될 때 coverage를 확장한다.

2026-08-28 B3는 persistent storage, atomic run, reverse index와 coverage를 완료했다. B4-A는 기존 EC2 extractor를 public snapshot sync에 연결하고 full SG rule condition, ENI ID, cross-account exact target, observer provenance를 저장한 뒤 incoming `refs` CLI까지 연결했다. B4-B1은 같은 run에 VPC Peering participant edge와 unsearched remote account coverage를 추가했다. 근거는 [B3 PoC 결과](B3-SNAPSHOT-GRAPH-POC.md), [B4-A 결과](B4A-SG-SNAPSHOT-REFS.md), [B4-B1 결과](B4B1-VPC-PEERING.md), [ADR-002](ADR-002-SQLITE-SNAPSHOT-GRAPH.md)다.

완료 조건:

- rule relation에 ingress/egress, protocol, port range, source/destination, description을 condition으로 보존한다.
- cross-account SG reference는 target account가 확인된 경우에만 cross-account exact로 표시한다.
- reverse result에 searched account/region coverage를 표시한다.
- attachment coverage가 불완전하면 `No attached EC2 instances; other services not searched`처럼 범위를 명시한다.

## Tier 2 — snapshot graph가 필요한 횡단 질문

### T2-A. 멀티계정 네트워크 연결 전경

```text
VPC → route table → route
                  ├─ VPC peering → peer VPC
                  ├─ TGW attachment → TGW → route table/attachment → VPC
                  └─ VPC endpoint → endpoint service → NLB
```

VPC peering, TGW, PrivateLink는 소유 account와 사용 account가 다를 수 있어 snapshot coverage가 없으면 reverse query의 완전성을 판단하기 어렵다. 하나의 account 실패를 빈 결과로 합치지 않는다.

### T2-B. shortcut query

| query | 답할 질문 | 정확성 경계 |
|---|---|---|
| `refs <resource>` | 무엇이 이 resource를 참조하거나 사용하는가 | relation coverage를 함께 반환 |
| `whois <IP>` | 어떤 ENI/resource/account가 IP를 소유하는가 | 수집된 private/public IP exact match만 |
| `whois <domain>` | record에서 지원 target까지 어디로 이어지는가 | DNS chain loop/depth와 unresolved 표시 |
| `rel path A B` | 저장된 resource relation graph에 연결 경로가 있는가 | packet reachability를 뜻하지 않음 |
| `diff <runA> <runB>` | resource와 relation이 무엇이 추가·삭제·변경됐는가 | 두 run의 coverage 차이를 별도 표시 |

`path A B`를 “트래픽이 갈 수 있는가”로 설명하지 않는다. packet reachability는 protocol/port, route selection, SG statefulness, NACL ordering, target health까지 평가하는 별도 `network path` 설계가 필요하다.

## Tier 3 — 수요 확인 후 확장할 backlog

| 영역 | 후보 chain | 승격 조건 |
|---|---|---|
| Datastore | RDS→subnet group/VPC/SG/KMS/snapshot, S3→policy/replication/KMS | 실제 장애·변경 검토 질문이 반복될 때 |
| Container | ECS service→task definition/target group/task ENI, EKS→nodegroup/ASG/OIDC role | 사용하는 compute platform 우선순위가 확인될 때 |
| Serverless | Lambda→role/VPC/event source/resource policy | cross-account invocation 조사 수요가 확인될 때 |
| Edge security | CloudFront→ACM/WAF/logging, WAF→rule group/IP set/association | Count/override 진단이 운영 요구로 확정될 때 |
| IAM/KMS | role trust reverse, policy attachment reverse, KMS external principal | effective-permission이 아닌 reference inventory로 범위 합의될 때 |
| Diagnostics | orphan, broken reference, CIDR overlap, public exposure | base edge coverage와 false-positive 기준이 먼저 검증될 때 |

backlog 항목은 provider와 extractor가 생기기 전까지 TUI menu에 빈 category로 노출하지 않는다.

## relation 화면은 edge table과 evidence detail로 구성한다

```text
AWS > web-sg > Relations                      SNAPSHOT · 8/8 accounts · 17m old
Filter / ingress                              4/11 edges
───────────────────────────────────────────────────────────────────────────────
  DIR  RELATION        TARGET          CONDITION          ACCOUNT       CONF
> ←    referenced-by   batch-sg        tcp/6379           data          exact
  →    references      db-sg           tcp/3306           prod          exact
  ←    attached-to     web-api         eni-0123           prod          exact
  →    source-cidr     10.20.0.0/16    udp/53             unresolved    exact
───────────────────────────────────────────────────────────────────────────────
→/enter target  e evidence  x cross-account  / filter  ← back  ? help
```

- 기본 정렬은 relation kind, target Name, stable ID다. priority/ordered semantics가 있는 rule list는 원래 순서를 고정한다.
- `e`는 operation, reason, observed context/time, coverage를 보여준다.
- target이 canonical resource면 Enter로 Summary를 연다. unresolved target이면 evidence detail만 연다.
- cross-account는 marker와 account column을 함께 사용한다. 색상만 사용하지 않는다.
- `LIVE`와 `SNAPSHOT` source label은 모든 relation 화면에 고정한다.

## extractor 한 개의 완료 조건

새 relation kind는 다음을 모두 만족해야 완료다.

1. source와 target identity 규칙이 문서화되어 있다.
2. exact/inferred/unresolved 판정 규칙과 반례 fixture가 있다.
3. provider fake가 정확한 read operation과 pagination을 검증한다.
4. forward와 reverse query가 같은 edge/evidence를 반환한다.
5. condition field가 edge dedupe key에 포함된다.
6. denied/login-required/not-searched coverage가 빈 결과와 구분된다.
7. TUI row, evidence detail, JSON query가 같은 의미를 보존한다.
8. snapshot edge를 열 때 live verification 성공·실패 경로가 test된다.
9. read-only interface와 CloudTrail acceptance 목록이 갱신된다.

## 구현 순서

1. 현재 `Relation`과 `RelationEvidence`를 위 계약에 맞춰 gap 분석한다.
2. Tier 0-A EC2 chain의 condition/evidence fixture를 고정한다.
3. Tier 0-B Route 53→CloudFront→S3 chain의 exact/inferred 경계를 고정한다.
4. Tier 1-A ELBv2 golden chain으로 domain 최종 연결 추적을 먼저 완성한다.
5. Tier 1-B SG reverse chain으로 snapshot schema·coverage·reverse index PoC를 검증한다.
6. Tier 2 shortcut query를 추가한다.
7. Tier 3는 실제 사용 로그와 운영 질문이 생길 때 하나씩 승격한다.

## 열린 결정

- [ ] inferred DNS/ARN relation의 기존 `RelationKind`에 더해 reason code를 얼마나 세분화할지.
- [ ] snapshot diff에서 normalized field 변경과 relation condition 변경을 어떤 granularity로 표시할지.
