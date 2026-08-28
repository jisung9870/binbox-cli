# B4-C Browse 자동 Incoming relations

독자        저장소 소유자와 이후 AWS TUI 구현·검토 담당자  
목적        일반 사용자가 별도 `sync` 없이 SG/VPC 멀티계정 역방향 관계를 조회하게 한다  
대상 환경   `bb aws browse`, configured Context Group, read-only AWS SDK runtime, local SQLite snapshot  
최종 검토   2026-08-28  
다음 검토   실제 UDG Context Group에서 자동 수집 지연과 SSO 실패 상태를 측정한 뒤  
상태        B4-C 첫 vertical slice 구현 완료

## 결론

Security Group과 VPC Summary의 `Incoming relations · AUTO`가 자동 수집의 유일한 일반 사용자 진입점이다. 진입 시 5분 이내이며 선택된 Context Group의 필수 profile×region coverage가 모두 성공한 snapshot은 재사용하고, 그렇지 않으면 같은 화면에서 `graph` 수집 후 refs를 읽는다. `bb aws sync graph`는 CI·prewarm·진단용으로 유지하되 Browse의 선행 절차가 아니다.

## 요구사항과 제약

- 요구사항: 한 번의 category 진입으로 여러 계정·리전의 역방향 관계를 조회한다.
- 요구사항: 결과가 `LIVE`인지 `SNAPSHOT`인지, age와 succeeded/failed/not-observed coverage를 화면에 표시한다.
- 요구사항: `Ctrl+R`은 cache를 무시하고 강제 수집하며 Escape는 진행 중 수집을 취소한다.
- 제약: Home이나 일반 live resource 진입은 전체 inventory 수집을 시작하지 않는다.
- 제약: profile이 Context Group에 속하지 않으면 임의로 모든 로컬 profile을 조회하지 않는다.
- 제약: snapshot 수집은 read-only provider operation만 사용하며 credential을 저장하지 않는다.

## 가정

- Context Group이 운영자가 허용한 multi-account 조회 범위다. 이 가정이 틀리면 자동 fan-out 범위가 과도하거나 누락되므로 group 설정 계약을 먼저 바꿔야 한다.
- 5분 TTL은 반복 진입 시 조작과 API 호출을 줄이기 위한 초기값이다. 실제 수집 시간이 10초를 넘거나 운영자가 더 짧은 신선도를 요구하면 설정 가능 TTL과 background prewarm을 재검토한다.

## 현재에서 목표로 바뀐 점

- 이전: `bb aws sync graph --group <group>` 실행 후 `bb aws refs ...`를 별도로 실행해야 했다.
- 목표: Browse Summary에서 `Incoming relations`를 열면 in-process Go service가 cache 판정, 필요 시 sync, refs 조회를 연속 실행한다.
- 남은 차이: 동일 account/region source는 exact live read가 가능하지만 cross-account/cross-region source는 snapshot Summary/Detail에 머문다. observed profile을 다시 검증한 뒤 live node로 전환하는 후속 slice가 필요하다.

## 컴포넌트 책임

- TUI Model: SG/VPC에 자동 category를 노출하고 target의 verified partition/account를 intent에 고정한다.
- Lazy AWS runtime: active profile이 속한 Context Group을 선택하고 cancellable 자동 조회 stream을 만든다.
- Auto snapshot service: TTL·coverage를 판정하고 필요한 경우 atomic graph sync 후 refs를 읽는다.
- Snapshot store: 수집 성공 후에만 active run을 교체하고 이전 complete run을 reader에게 계속 제공한다.
- Projection: Name 우선, ID fallback으로 source row를 만들고 cross-account target에는 잘못된 live context를 재사용하지 않는다.

## 실패 모드

- profile이 group에 없음: 자동 조회를 시작하지 않고 group 설정 필요 상태를 표시한다.
- SSO/권한/timeout: 전체 화면을 멈추지 않고 자동 수집 실패 상태를 표시하며 기존 active snapshot을 교체하지 않는다.
- 일부 scope 실패: 성공 결과와 failed/not-observed coverage를 함께 표시한다. 빈 결과를 완전한 0건으로 단정하지 않는다.
- 사용자 취소: collector context를 취소하고 이전 complete snapshot과 화면의 기존 결과를 유지한다.

## 재실행과 동시 실행

- 5분 이내의 필수 coverage가 완전하면 재실행은 read-only cache 조회다.
- `Ctrl+R`은 강제 수집이지만 snapshot sync-owner lock이 중복 writer를 직렬화한다.
- Commit은 atomic run 교체이므로 실패·취소된 run은 active pointer가 되지 않는다.

## 확장 한계와 재검토 트리거

- 한 scope timeout은 3분, fan-out 동시성은 4, refs 상한은 10,000개다.
- 실제 Context Group 수집이 반복해서 10초를 넘으면 per-scope progressive coverage update와 background prewarm을 다음 구조로 검토한다.
- TGW·PrivateLink collector가 추가되면 VPC cache completeness 판정에 해당 service coverage를 포함해야 한다.

## 검증과 운영 인계

- model test가 category, exact target identity fencing, `Ctrl+R` force를 검증한다.
- service test가 fresh complete cache 재사용과 stale cache 자동 graph sync를 검증한다.
- projection test가 Name 우선과 cross-account live-navigation 차단을 검증한다.
- 운영자는 `$XDG_CONFIG_HOME/bb/aws-contexts.json`의 group/profile/region 범위만 관리한다. 일반 Browse 사용자에게 수동 sync 절차를 요구하지 않는다.
