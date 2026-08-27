# AWS Browse v2 기획 묶음

독자        저장소 소유자와 이후 구현·검토 담당자
목적        AWS Console의 반복적인 서비스·계정 이동을 대체할 전용 read-only TUI의 제품·UX·기술 계약을 확정한다
대상 환경   AWS SDK for Go v2, AWS CLI v2, zsh/tmux/Orca 터미널, 여러 AWS profile과 계정·리전
최종 검토   2026-08-28
다음 검토   PTY/tmux·release·실계정 gate 완료 시
상태        자동화 구현과 production wiring 완료, 최종 gate 진행 중, 실계정 smoke 대기

`bb aws browse`는 시작할 때 전체 AWS inventory를 만들지 않는다. 로컬 정보로 서비스 카탈로그를 즉시 열고, EC2·Route 53·IAM 같은 카테고리에 들어가거나 연결 리소스를 선택할 때 필요한 조회만 실행한다. 도메인·IAM role처럼 소유 계정을 모를 수 있는 검색은 사용자가 검색을 확정한 뒤 여러 AWS profile을 제한된 동시성으로 자동 조회한다.

## 이번 기획에서 바뀌는 결론

- 시작 전 전체 snapshot 수집을 폐기한다.
- 공용 staged selector 대신 비동기 로딩·표·상세 탭·오버레이를 지원하는 AWS 전용 TUI를 만든다.
- 리소스의 출처를 `partition + account + region + type + id`로 식별한다.
- Tag, Security Group rule, IAM policy는 한 줄로 줄이지 않고 별도 전체 화면에서 읽는다.
- EC2 → EBS/SG/instance profile → IAM role → policy 이동을 같은 탐색 stack 안에서 처리한다.
- 도메인·role cross-profile 검색은 모든 키 입력마다 실행하지 않고, 검색 확정 시 선택 범위의 profile을 자동 fan-out 한다.
- AWS 변경 API는 계속 금지한다. Browser runtime에서 AWS CLI는 profile discovery와 ambient/named credential export만 맡고, AWS SDK for Go v2가 STS·EC2·IAM·Route 53 read operation을 실행한다. 기존 SSO login/assume 명령은 별도 호환 surface다.

## 문서 지도

- [PRD.md](PRD.md): 문제, 사용자 흐름, 기능 범위, 완료 조건.
- [DESIGN.md](DESIGN.md): 정보 구조, 화면, 키보드 동작, 반응형·접근성 계약.
- [SCENARIOS.md](SCENARIOS.md): 실제 키 입력, 화면 전이, AWS 호출 시점을 묶은 동작 예시.
- [ARCHITECTURE.md](ARCHITECTURE.md): lazy provider, 멀티계정 검색, cache, 오류·권한·검증 구조.
- [ADR-001-HYBRID-AWS-ACCESS.md](ADR-001-HYBRID-AWS-ACCESS.md): SDK data-plane과 CLI credential/control-plane 채택 결정.
- [IMPLEMENTATION-WORKFLOW.md](IMPLEMENTATION-WORKFLOW.md): 역할·모델, wave, 파일 소유권, 단계별 gate와 rollback 방식.
- [REVIEW.md](REVIEW.md): 반복 독립 검토 결과, 반영 여부, 구현 영향 범위.

## 기존 작업과의 관계

기존 CLI-preload `internal/bb/aws_browse.go`와 문서 설계는 Git history와 decision log에 superseded 근거로 남는다. 현재 branch에는 credential/runtime/provider/query/store, progressive TUI, scoped query, bounded cross-profile search와 production wiring의 자동화 구현이 있으며 최종 acceptance가 진행 중이다. 이미 배포된 `bb aws assume`과 `bb assume`은 호환 surface이므로 강제 rename하지 않지만, 새 TUI와 내부 모델에서는 대상을 `assume`이 아니라 `AWS profile` 또는 `account/role context`라고 부른다.

## 구현 기본값

- exact domain/role cross-profile 검색은 AWS CLI가 인식하는 모든 profile을 기본 범위로 사용한다. 현재 context를 먼저 검색하고 사용자는 제출 전에 범위를 줄일 수 있다.
- AWS resource 조회는 SDK만 사용한다. CLI resource-operation fallback은 만들지 않는다.
- P0의 EC2/VPC 탐색은 선택한 region 하나만 조회한다. multi-region fan-out은 P1이다.
- 40x12까지 single-pane card layout을 제공한다. 시작 terminal이 더 작으면 plain command loop를 열고, 실행 중 resize로 작아지면 resize 또는 plain 재실행 안내를 표시한다.
- unreleased v1 `aws_browse.go`는 별도 호환 layer 없이 교체한다. 이전 코드는 Git history에 남는다.
