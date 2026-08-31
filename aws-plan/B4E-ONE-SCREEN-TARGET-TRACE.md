# B4-E One-screen Target trace

독자        저장소 소유자와 이후 AWS TUI 구현·검토 담당자  
목적        Route 53 alias에서 실제 target까지 반복 진입하지 않고 한 화면에서 경로를 확인하게 한다  
대상 환경   `bb aws browse`, Route 53 alias, ALB/NLB·CloudFront·S3·EC2 read-only provider  
최종 검토   2026-08-31
다음 검토   AWS ELB relation/DNS contract 또는 trace 상한 변경 시
상태        B4-E 자동 trace, 실패 화면 collapse, 실제 ALB/NLB 검증 완료

## 결론

Route 53 alias Overview의 `Target trace`는 한 번의 Enter로 지원되는 forward edge를 breadth-first로 조회하고, Alias→Load Balancer/CloudFront→Listener/Rule→Target Group/Origin→registered target을 하나의 들여쓰기 목록에 누적한다. 사용자는 각 리소스의 전체 Detail이 필요할 때만 결과 행을 연다. 기존 `Alias target`은 API evidence를 먼저 읽는 수동 경로로 유지한다.

## 요구사항과 제약

- 요구사항: 선형 경로를 단계마다 Enter하지 않고 한 화면에 누적한다.
- 요구사항: ALB의 listener rule 분기와 여러 target group을 모두 보존한다.
- 요구사항: 일부 조회 실패가 이미 성공한 경로를 지우지 않아야 한다.
- 제약: 새로운 AWS API나 write permission을 추가하지 않고 기존 exact read operation만 조합한다.
- 제약: 한 trace는 최대 64 query와 500 resource이며 동일 canonical target을 한 번만 조회한다.

## 동작 구조

1. alias target DNS 또는 CloudFront domain을 depth 0 evidence row로 먼저 표시한다.
2. verified profile·region을 상속해 exact provider request를 순차 실행한다.
3. 각 projection의 지원되는 outgoing target을 queue에 추가하되, 이미 조회했거나 이미 관찰된 canonical target은 다시 queue에 넣지 않는다.
4. 완료 전에도 누적 projection을 stream으로 갱신한다.
5. 모든 분기가 끝나면 Ready, 일부 실패면 성공 노드와 typed failure를 함께 표시한다.

지원 edge는 ELB listeners, ALB rules, target groups, target health, registered EC2/ALB target과 CloudFront의 inferred S3 origin이다. SG/VPC처럼 ingress target 경로가 아닌 관계는 자동 trace에서 따라가지 않는다.

## 실패 모드

- 첫 DescribeLoadBalancers가 denied: alias DNS evidence row를 유지하고 `access denied · elbv2:DescribeLoadBalancers · <safe code>`를 표시한다.
- 중간 branch가 denied/throttled/timed out: 다른 queue item을 계속 처리한 뒤 첫 typed failure와 성공 노드를 함께 표시한다.
- 일반 linked-resource exact read가 0건 실패: 빈 child 화면을 history에 남기지 않고 부모 Overview로 복귀해 오류를 표시한다.
- query/resource 상한 도달: 수집된 경로를 유지하고 `trace_limit`을 명시한다.
- 사용자 취소: trace context를 취소해 진행 중 query와 이후 queue 실행을 중단한다.

## 검증과 한계

- model test가 Target trace category의 단일 intent dispatch와 failed child collapse를 검증한다.
- runtime test가 DNS→ALB→Listener→Rule의 누적 순서, depth metadata, denied root evidence 보존을 검증한다.
- `lg-udg-ops`의 `DescribeLoadBalancers` explicit deny 기록은 그대로 유효하다. 자동 trace는 조작 횟수를 줄이고 근거를 보존하지만 deny를 우회하지 않는다.
- 2026-08-31 `lg-udg-adm`, `ap-northeast-2` 실계정 검증에서 `mont.udg.line.games.` ALB trace는 19개 resource, `pmm.udg.line.games.` NLB→ALB trace는 27개 resource로 각각 `Ready` 종료했다.
- 실계정 첫 smoke에서 관찰된 parent LB 재조회 `ValidationError`는 observed-resource queue deduplication과 ARN exact-read pagination 제거로 수정했으며 같은 두 경로로 재검증했다.
