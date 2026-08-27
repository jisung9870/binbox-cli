# AWS resource browser product pitch

독자        저장소 소유자
목적        AWS 콘솔 없이 연결 리소스를 따라가는 개인용 read-only TUI의 범위를 고정한다
대상 환경   AWS CLI v2, 단일 AWS 계정·리전
최종 검토   2026-08-27
다음 검토   P0 실계정 smoke 완료 시
상태        P0 구현중

`bb aws browse`는 AWS 리소스를 서비스 목록이 아니라 **연결 관계로 탐색하는 read-only 터미널 브라우저**다. P0는 EC2에서 EBS·Security Group·VPC·Subnet으로 이동하고, Route 53 레코드가 EC2 IP/DNS와 일치하면 해당 인스턴스로 이동하는 데 집중한다. 자격증명과 API 페이지네이션은 AWS CLI가 계속 소유한다.

## Problem — 콘솔에서는 보이지만 관계를 다시 찾는 데 화면 이동이 반복된다

실제로 답하려는 질문은 다음과 같다.

- 이 EC2에 어떤 EBS와 Security Group이 붙어 있는가?
- 이 Security Group을 사용하는 EC2 목록과 규칙은 무엇인가?
- 이 VPC 안의 인스턴스·Subnet·Route Table·Security Group은 무엇인가?
- 이 Route 53 레코드 값은 어느 EC2와 일치하는가?
- 지금 보고 있는 계정·프로필·리전은 무엇인가?

AWS 콘솔은 각 서비스 상세를 제공하지만, 서비스 사이를 이동할 때 검색 조건과 리소스 ID를 다시 입력하게 된다. 원한 것은 대시보드가 아니라 한 리소스의 관계를 키보드로 계속 따라가는 inspector다.

## 사용자와 사용 맥락

- 사용자: 저장소 소유자 한 명.
- 사용 시점: 장애 확인, 변경 전 영향 범위 확인, DNS·네트워크 연결 확인.
- 실행 맥락: zsh/tmux/Orca 터미널, 이미 로그인한 AWS SSO profile, 대부분 하나의 리전을 집중 조사.
- 안전 기대: 조회 도구가 AWS 리소스를 변경할 가능성이 없어야 하며 자격증명을 자체 저장하지 않아야 한다.

## Appetite — P0는 두세 번의 집중 작업으로 끝낸다

기간은 고정하고 범위를 줄인다. P0는 단일 리전의 핵심 그래프와 글로벌 IAM/Route 53 목록까지만 포함한다. 전체 리전, 정책 문서 해석, 모든 Route 53 alias 서비스 resolver는 P1 이후다.

## Solution — Service → Resource → Details/Relations → Linked Resource

```text
AWS account/profile/region
├─ EC2 → instance → Details | EBS volumes | Security groups | VPC | Subnet
├─ Route 53 → hosted zone → DNS records → matched EC2 | inferred alias target
├─ IAM → user | role → Details
└─ VPC → VPC → instances | subnets | security groups | route tables
```

사용자는 모든 단계에서 바로 입력해 검색하고, Enter로 들어가며, Escape로 이전 검색 상태가 보존된 화면으로 돌아간다. 상세 값은 read-only 화면이고 관계 행만 다음 리소스 목록으로 이어진다.

## 기존 대안과 차별점

- [claws](https://github.com/clawscli/claws)는 70개 AWS 서비스, cross-resource navigation, profile/region 전환, read-only 모드를 이미 제공한다. 범용 AWS TUI가 목적이면 가장 먼저 검토할 대안이다. 이 프로젝트는 기존 `bb` SSO/assume 흐름과 동일한 단일 바이너리 UX, 좁은 read-only 계약, 필요한 관계만 직접 통제하는 것이 목적이다.
- [Steampipe AWS plugin](https://hub.steampipe.io/plugins/turbot/aws/queries)은 광범위한 리소스를 SQL로 조인하는 데 강하지만, 리소스를 클릭하듯 따라가는 짧은 TUI 흐름이 기본 인터페이스는 아니다.
- [CloudQuery](https://www.cloudquery.io/docs/cli/introduction)는 동기화된 장기 asset inventory와 SQL 분석에 맞고, 실시간 개인 터미널 탐색에는 저장·동기화 계층이 과하다.
- AWS CLI 원문은 가장 정확한 소스지만 여러 `describe/list` 결과의 ID 관계를 사용자가 직접 조인해야 한다.

## P0 경계 — 이것만 되면 실제 점검에 쓸 수 있다

- 명령: `bb aws browse [--profile NAME] [--region REGION] [--json]`.
- EC2 instance ↔ EBS volume, Security Group, VPC, Subnet 양방향 탐색.
- VPC → EC2/Subnet/Security Group/Route Table 탐색.
- Route 53 hosted zone → record 탐색과 A/CNAME 값의 EC2 IP/DNS 상관관계.
- AliasTarget는 DNS와 canonical hosted zone을 보여주고 서비스 종류를 추정하되, 확정되지 않은 링크라고 표시.
- IAM user/role 기본 목록과 상세.
- 한 서비스의 `AccessDenied`가 다른 서비스 결과를 막지 않는 partial-result 정책.
- non-TTY human summary와 schema-v1 JSON envelope.
- AWS CLI만 호출하고 create/update/delete/attach/detach/start/stop 계열은 호출하지 않음.

## No-gos — P0에서 하지 않는다

- AWS 리소스 생성·변경·삭제와 EC2 start/stop.
- 전체 리전·멀티계정 동시 수집.
- Route 53 Domains의 등록/갱신 관리; hosted zone과 record만 다룬다.
- IAM effective permissions 계산, policy simulator, trust graph.
- ALB/NLB, CloudFront, API Gateway, S3 등 모든 alias 대상의 확정 상세 이동.
- 백그라운드 daemon, 로컬 DB, 주기적 inventory 저장, 웹 대시보드.

## Rabbit holes — 범위를 지키는 규칙

- AWS SDK를 새로 넣지 않는다. 기존 저장소 계약대로 AWS CLI가 SSO/profile/credentials와 페이지네이션을 소유한다.
- 키 입력 중 네트워크 요청을 하지 않는다. P0는 TUI 진입 전 snapshot을 만들고 이후 탐색은 메모리에서 수행한다.
- DNS 문자열만으로 AWS 리소스를 확정하지 않는다. ID/ARN은 `exact`, IP/DNS 상관관계와 alias service 분류는 `heuristic`으로 표시한다.
- 서비스별 세부 필드를 완전 복제하지 않는다. 관계 판단과 운영 확인에 필요한 필드만 정규화한다.

## 마일스톤과 완료 조건

- P0: 위 MVP 경계의 fixture 테스트, 전체 `go test ./...`, `go vet ./...`, 실계정 read-only smoke가 통과한다.
- P1: lazy loading/cache와 취소 가능한 로딩 상태를 추가하고, ENI/EIP 및 ELBv2 resolver로 SG/DNS 연결 정확도를 높인다.
- P2: 선택한 여러 리전 또는 계정을 제한된 동시성으로 탐색하고, IAM instance profile/policy 관계를 추가한다.

## 재검토 트리거

- P0 snapshot 수집이 일반 계정에서 10초를 반복적으로 넘으면 lazy loading을 P1보다 앞당긴다.
- 리소스 1,000개 이상에서 검색/렌더링이 즉시 반응하지 않으면 서비스별 pagination UI와 cache를 도입한다.
- 관계 adapter가 10개 서비스를 넘으면 CLI JSON parser를 서비스별 파일/interface로 분리하거나 AWS SDK v2 전환을 별도 ADR로 검토한다.
- 범용성 요구가 커져 `claws`와 기능 중복이 커지면 native 구현을 멈추고 외부 도구 연동 또는 기여로 전환한다.
