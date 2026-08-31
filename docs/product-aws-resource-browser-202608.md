# AWS resource browser product pitch

독자        저장소 소유자
목적        AWS 콘솔 없이 필요한 리소스와 관계를 따라가는 read-only TUI의 범위를 고정한다
대상 환경   AWS CLI v2, AWS SDK for Go v2, 여러 AWS profile·계정, 선택 리전
최종 검토   2026-08-28
다음 검토   선택적 tmux/interactive resize 관찰 및 실계정 CloudTrail 승인 시
상태        구현·자동 Linux PTY·자동 release gate 완료, 수동/외부 acceptance 대기

`bb aws browse`는 `--profile`이 없으면 로컬 configured-profile 선택 화면을 먼저 열고, 명시적인 profile이 있으면 네트워크 호출 없이 Home을 먼저 연다. 이후 사용자가 선택한 category·resource·relation만 progressive load하는 read-only 터미널 브라우저다. AWS CLI는 profile discovery와 credential export만 담당하고, AWS SDK for Go v2의 좁은 typed provider가 STS identity와 EC2·IAM·Route 53·CloudFront·S3 조회를 담당한다. 실제 AWS credential과 CloudTrail 검증은 repository owner 승인이 필요한 외부 gate로 남아 있다.

리소스 목록은 비어 있지 않은 `Name` 태그, SG group name 같은 리소스 고유 이름, ID 순으로 제목을 선택하며 이름을 쓴 경우에도 ID를 보조 정보로 유지한다. 리소스를 열면 식별·상태 핵심 필드와 관계/Detail/Tags 카테고리를 함께 보여주는 Summary가 먼저 열린다. 전체 필드는 별도 Detail에서만 표시한다. 관계는 Security groups, Volumes, VPCs 같은 카테고리로 묶고, 태그는 상세 JSON 필드에서 제거해 정렬·검색 가능한 Tags 카테고리에서 표시한다. 카테고리를 여는 동작과 목록 검색은 로컬 처리이며, 카테고리 안의 리소스를 선택할 때만 linked-resource 조회가 시작된다. 정확한 linked-resource 조회 결과가 하나면 `Resources (1)` 화면을 생략하고 해당 Summary를 바로 열며, 규칙·레코드 컬렉션과 복수 결과는 목록을 유지한다. Security Group Summary는 Inbound/Outbound rules를, IAM Role Summary는 Attached/Inline policies를, Route 53 Hosted Zone Summary는 DNS records를 직접 연다. CloudFront Alias record는 동일 계정 distribution의 Summary로 이어지고, Origins에서 기본 경로와 각 cache behavior path를 따로 보존한 뒤 S3 bucket의 API로 확인된 region까지 추적한다. 관리형 정책은 기본 Policy document로 이어지고 인라인 정책은 전체 문서를 Detail에서 확인한다. 정책 문서와 Route 53 routing/alias 구조는 읽기 쉬운 여러 줄 JSON으로 표시한다. 브라우저 화면에서 Right/Enter는 열기, Left는 한 화면 뒤로가기다. 긴 Detail 필드는 Up/Down 또는 PageUp/PageDown으로 스크롤하며 TUI는 터미널 전체 viewport를 사용한다. `:` command, `/` filter, `Ctrl-o`/`Ctrl-i` history는 k9s-inspired 탐색을 제공한다.

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
- 화면마다 profile/account/principal/current region/region scope와 loading·coverage·partial 상태를 보존한다.
- 실행 인자를 다시 입력하지 않아도 `c`에서 configured profile/group/region을 검색해 고르고 current region과 `Current region`/`All <group> regions` scope를 바꿀 수 있다. group은 `$XDG_CONFIG_HOME/bb/aws-contexts.json` 또는 OS user-config fallback(WSL/Linux의 `~/.config/bb`)의 비밀 없는 profile/region 이름만 사용한다. profile/resource 검색은 이미 로드된 항목만 로컬 필터링한다. account/principal은 입력값이 아니라 선택한 profile의 STS identity로 검증한 뒤 적용한다.
- EC2 instance는 비어 있지 않은 `Name` 태그를 주 표시명으로 쓰고 ID를 보조로 유지하며, `Name`이 없으면 ID를 표시한다.
- cross-profile search는 입력 중 실행하지 않고 사용자가 query를 제출한 뒤에만 시작한다.

## 구현된 P0 surface

- `bb aws browse [--profile NAME] [--region REGION]`: 전용 progressive TUI이며 `--json`을 받지 않는다. `--profile`이 없으면 로컬 profile 선택기로 시작하고 명시하면 Home으로 바로 간다. 리소스 목록의 직접 입력은 로컬 검색이며, `c`는 검색 가능한 profile/group/current region/scope 선택, account/principal 검증, 적용을 TUI 안에서 수행한다. all scope의 EC2/VPC catalog는 current-first, 동시성 2로 region 결과와 coverage를 합치고 IAM/Route 53/CloudFront는 한 번만 읽는다. 리소스를 열면 그 결과의 실제 region으로 고정한다.
- `bb aws query ec2 instances [--profile NAME] [--region REGION] [--json]`: current context의 scoped query다.
- `bb aws query ami <ami-id> ... [--scope current|all] [--json]`: exact AMI를 configured profile/account에 조회해 AMI owner account와 실제 조회 가능한 account/profile을 구분해서 보여준다.
- `bb aws query domain <fqdn> ... [--scope current|all] [--json]`와 `bb aws query role <exact-name> ... [--scope current|all] [--json]`: explicit-submit exact search다.
- EC2/AMI/EBS/SG/VPC/Subnet/Route Table과 EC2 Launch Template/version, IAM role/instance profile/policy, Route 53 hosted zone/record, CloudFront distribution, S3 bucket region을 typed provider와 normalized relation으로 읽는다. EC2 instance와 Launch Template version의 AMI 관계를 열면 exact `DescribeImages` 조회로 AMI 상태·소유자·아키텍처·플랫폼·생성 시각·root device를 확인한다. 일반 Launch Template version 조회는 User Data 원문을 버리고 존재 여부만 보여준다. 사용자가 `User Data`를 명시적으로 열면 exact-version 조회를 별도 실행해 Base64 디코딩한 내용을 세션 메모리의 전용 화면에만 표시하며, 해당 화면은 민감값 포함 가능성을 경고한다.
- profile별 credential generation과 STS identity를 context에 묶고 generation/account/partition이 바뀐 stale response를 commit하지 않는다.
- current-first cross-profile search, bounded concurrency, duplicate provenance, `not found/not searched/denied/login required` coverage를 보존한다.
- non-TTY browse는 AWS를 호출하거나 prompt하지 않고 stderr에 scoped query 안내를 쓰며 exit 2를 반환한다.

## 소유 경계

- AWS CLI: `configure list-profiles`, `configure export-credentials`, 기존 SSO login/assume 호환 surface.
- AWS SDK for Go v2: STS identity와 narrowed EC2/IAM/Route 53/CloudFront/S3 List/Describe/Get operation, pagination, cancellation, typed error.
- bb: local-first TUI model, query coordinator, session memory cache, relation projection, error/coverage 표시.
- AWS: credential/token cache와 CloudTrail audit source of truth.

Launch Template와 연결된 AMI 탐색에는 기존 read-only 정책에 `ec2:DescribeLaunchTemplates`, `ec2:DescribeLaunchTemplateVersions`, `ec2:DescribeImages`가 필요하다. 이 Describe action들은 resource-level 권한을 지원하지 않으므로 IAM statement의 `Resource`는 `"*"`를 사용한다. Auto Scaling Group 조회 권한은 필요하지 않으며 이 기능은 ASG API를 호출하지 않는다.

## 비목표

- AWS resource 생성·변경·삭제, EC2 start/stop, IAM effective-permission 계산.
- 키 입력마다 자동 fan-out, 모든 AWS service의 범용 inventory, live 기본 경로의 mandatory persistent cache/daemon/local DB. 멀티계정 reverse/path/diff용 optional snapshot graph는 별도 확장 설계를 따른다.
- custom endpoint 지원, credential 원문 저장, CLI resource-operation fallback.
- P0의 ENI/EIP, ELBv2, CloudFront custom-origin downstream tracing.

## 대안과 선택

`claws`, Steampipe, CloudQuery는 더 넓은 service/inventory 범위를 제공한다. bb는 기존 SSO/assume UX와 단일 바이너리 안에서 필요한 EC2↔IAM↔DNS 관계, 명시적 scope, 좁은 read-only interface를 검증 가능하게 유지하는 쪽을 선택했다. 초기 direct AWS CLI JSON snapshot/staged-selector 안은 [decision log](decision-log.md)와 `aws-plan/`에 역사로 남지만 hybrid SDK architecture가 이를 supersede한다.

## 완료 gate

- 자동화(통과·커밋됨): fixture/provider/query/model/golden test, Linux PTY process test, race, vet, build, no-skip, 네 release target의 stripped 40 MiB cap.
- terminal: 자동 Linux PTY는 alt-screen cleanup, cancel, narrow startup plain fallback, stderr/stdout, non-TTY 경계를 확인한다. direct tmux와 실행 중 interactive resize 관찰은 선택적 수동 acceptance다.
- 외부: 승인된 read-only credential로 static/SSO/role/credential-process context와 12-profile latency를 확인하고 CloudTrail에서 승인된 credential/auth 및 STS/EC2/IAM/Route 53/CloudFront/S3 read operation 외 호출이 없음을 검토한다.

구현과 자동 release gate는 완료됐지만 release 자체나 실계정 검증 완료를 뜻하지 않는다. 선택적 direct tmux/interactive resize 관찰과 owner-approved 12-profile latency·identity·CloudTrail 증거는 수동/외부 acceptance로 남는다.
