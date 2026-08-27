# AWS resource browser product pitch

독자        저장소 소유자
목적        AWS 콘솔 없이 필요한 리소스와 관계를 따라가는 read-only TUI의 범위를 고정한다
대상 환경   AWS CLI v2, AWS SDK for Go v2, 여러 AWS profile·계정, 선택 리전
최종 검토   2026-08-28
다음 검토   PTY/tmux·release·실계정 CloudTrail gate 완료 시
상태        자동화 구현과 production wiring 완료, 최종 gate 진행 중, 실계정 smoke 대기

`bb aws browse`는 네트워크 호출 없이 Home을 먼저 열고 사용자가 선택한 category·resource·relation만 progressive load하는 read-only 터미널 브라우저다. AWS CLI는 profile discovery와 credential export만 담당하고, AWS SDK for Go v2의 좁은 typed provider가 STS identity와 EC2·IAM·Route 53 조회를 담당한다. 실제 AWS credential과 CloudTrail 검증은 repository owner 승인이 필요한 외부 gate로 남아 있다.

## Problem — 콘솔에서는 관계와 계정 문맥을 반복해서 다시 찾는다

사용자가 빠르게 답하려는 질문은 다음과 같다.

- 이 EC2에 어떤 EBS, Security Group, VPC, Subnet, instance profile이 연결됐는가?
- role의 attached/inline/trust policy는 무엇인가?
- 이 Route 53 record와 같은 이름을 어느 profile에서 볼 수 있는가?
- 정확한 role 또는 EC2 instance를 어느 account/profile에서 볼 수 있는가?
- 일부 profile이 denied 또는 login required일 때 어디까지 검색됐는가?

## 사용자와 안전 기대

- zsh, 좁은 tmux pane, Orca terminal에서 장애 확인과 변경 전 영향 검토에 사용한다.
- AWS resource mutation API를 호출하지 않고 credential을 출력·저장하지 않는다.
- 화면마다 profile/account/principal/region과 loading·coverage·partial 상태를 보존한다.
- cross-profile search는 입력 중 실행하지 않고 사용자가 query를 제출한 뒤에만 시작한다.

## 구현된 P0 surface

- `bb aws browse [--profile NAME] [--region REGION]`: 전용 progressive TUI이며 `--json`을 받지 않는다.
- `bb aws query ec2 instances [--profile NAME] [--region REGION] [--json]`: current context의 scoped query다.
- `bb aws query domain <fqdn> ... [--scope current|all] [--json]`와 `bb aws query role <exact-name> ... [--scope current|all] [--json]`: explicit-submit exact search다.
- EC2/EBS/SG/VPC/Subnet/Route Table, IAM role/instance profile/policy, Route 53 hosted zone/record를 typed provider와 normalized relation으로 읽는다.
- profile별 credential generation과 STS identity를 context에 묶고 generation/account/partition이 바뀐 stale response를 commit하지 않는다.
- current-first cross-profile search, bounded concurrency, duplicate provenance, `not found/not searched/denied/login required` coverage를 보존한다.
- non-TTY browse는 AWS를 호출하거나 prompt하지 않고 stderr에 scoped query 안내를 쓰며 exit 2를 반환한다.

## 소유 경계

- AWS CLI: `configure list-profiles`, `configure export-credentials`, 기존 SSO login/assume 호환 surface.
- AWS SDK for Go v2: STS identity와 narrowed EC2/IAM/Route 53 List/Describe/Get operation, pagination, cancellation, typed error.
- bb: local-first TUI model, query coordinator, session memory cache, relation projection, error/coverage 표시.
- AWS: credential/token cache와 CloudTrail audit source of truth.

## 비목표

- AWS resource 생성·변경·삭제, EC2 start/stop, IAM effective-permission 계산.
- 키 입력마다 자동 fan-out, 모든 AWS service의 범용 inventory, persistent cache/daemon/local DB.
- custom endpoint 지원, credential 원문 저장, CLI resource-operation fallback.
- P0의 ENI/EIP, ELBv2, CloudFront, multi-region fan-out.

## 대안과 선택

`claws`, Steampipe, CloudQuery는 더 넓은 service/inventory 범위를 제공한다. bb는 기존 SSO/assume UX와 단일 바이너리 안에서 필요한 EC2↔IAM↔DNS 관계, 명시적 scope, 좁은 read-only interface를 검증 가능하게 유지하는 쪽을 선택했다. 초기 direct AWS CLI JSON snapshot/staged-selector 안은 [decision log](decision-log.md)와 `aws-plan/`에 역사로 남지만 hybrid SDK architecture가 이를 supersede한다.

## 완료 gate

- 자동화: fixture/provider/query/model/golden test, race, vet, build, no-skip, 네 release target의 stripped 40 MiB cap.
- terminal: direct PTY와 tmux에서 alt-screen cleanup, resize, narrow startup plain fallback, stderr/stdout 경계를 확인한다.
- 외부: 승인된 read-only credential로 static/SSO/role/credential-process context와 12-profile latency를 확인하고 CloudTrail에서 승인된 credential/auth 및 STS/EC2/IAM/Route 53 read operation 외 호출이 없음을 검토한다.

자동화 구현과 production wiring이 존재한다는 사실만으로 release-ready 또는 실계정 검증 완료를 뜻하지 않는다. PTY/tmux, release gate, 실제 credential/CloudTrail smoke가 모두 닫힐 때 P0 완료로 바꾼다.
