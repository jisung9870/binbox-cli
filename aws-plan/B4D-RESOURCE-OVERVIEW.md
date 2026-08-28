# B4-D Resource Overview

독자        저장소 소유자와 이후 AWS TUI 구현·검토 담당자  
목적        반복적인 category 진입 없이 리소스 핵심 정보와 연결 대상을 한 화면에서 확인하게 한다  
대상 환경   `bb aws browse`, 40–120열 full-screen TUI, 기존 live projection과 snapshot evidence  
최종 검토   2026-08-29  
다음 검토   실제 UDG 리소스에서 preview 밀도와 72열 breakpoint를 관찰한 뒤  
상태        B4-D1 Overview와 inline preview 구현 완료

## 결론

리소스를 열면 `Summary` 대신 서비스별 `Overview`가 나타난다. 화면 높이가 허용하면 `At a glance`가 EC2, SG, VPC, EBS, ALB/NLB, Route 53, IAM, CloudFront 유형별 핵심 필드를 우선 표시한다. 72열 이상에서는 `Explore` 행의 `PREVIEW` 열이 관계 대상과 태그를 최대 두 개까지 Name 우선으로 보여준다. 전체 필드, 전체 관계, 전체 태그가 필요할 때만 기존 Detail·category·Tags 화면으로 들어간다.

## 사용자 흐름

- 일반 확인: `Resource list → Overview`에서 상태, 네트워크 식별자, 주요 연결 대상, 태그를 읽는다.
- 관계 전체 확인: Overview에서 해당 Explore 행을 선택해 기존 관계 목록을 연다.
- 원본 수준 확인: `Detail`에서 projection의 전체 필드를 스크롤한다.
- 좁은 화면: 선택 가능한 Explore 행을 먼저 보존하고 공간이 부족하면 `At a glance`를 생략한다.

## 설계와 제약

- Overview는 이미 로드된 `ResourceProjection`만 사용한다. 화면 렌더링 때문에 AWS API 요청이나 snapshot sync가 추가되지 않는다.
- 서비스별 priority에 일치하는 필드는 최대 8개, generic fallback은 최대 6개다. JSON·배열 또는 120자를 넘는 값은 Overview fallback에서 제외한다.
- 관계 preview는 category별 처음 두 human label을 `·`로 연결하고 나머지는 `+N`으로 표시한다.
- Tags preview는 처음 두 `key=value`를 표시한다. 전체 목록과 local filter 계약은 기존 Tags 화면이 소유한다.
- 72열 미만에서는 PREVIEW 열을 제거한다. 40×12에서도 선택 행과 footer가 화면 밖으로 밀리지 않아야 한다.

## 실패 모드와 대응

- provider가 알려진 priority label을 반환하지 않음: 짧은 scalar field를 generic 순서로 최대 6개 표시한다.
- 관계 Label이 비어 있음: Target reference, 그다음 canonical Target ID를 fallback으로 사용한다.
- 필드나 관계가 없음: Detail과 Tags 진입점은 유지하고 preview는 `-`로 표시한다.
- 긴 preview가 열 폭을 초과함: 공통 table renderer가 현재 열 폭에서 안전하게 축약한다.

## 검증 기준

- 80×24 golden은 At a glance와 PREVIEW 열을 함께 보여야 한다.
- 40×12 golden은 Explore 선택 행과 footer를 유지하고 PREVIEW 열을 숨겨야 한다.
- model test는 관계·Tags 값이 Overview에 나타나며 category 진입이 추가 AWS 요청 없이 동작함을 검증한다.
- 전체 Go test, vet, AWS browser race, skip-free, release size gate를 통과해야 한다.

## 후속 범위

B4-D1은 읽기 depth를 줄이지만 Overview preview 자체를 선택 대상으로 만들지는 않는다. 다음 단계에서는 넓은 화면의 Resource List 옆 Quick Preview 또는 Overview 안의 direct target selection 가운데 실제 사용 빈도가 높은 방식을 검증한다.
