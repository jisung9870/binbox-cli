# B4-F Wide Quick Preview와 Overview 직접 이동

독자        저장소 소유자와 이후 AWS TUI 구현·검토 담당자
목적        리소스 내용을 확인하거나 첫 두 관계 target을 열기 위해 발생하는 화면 이동을 줄인다
대상 환경   `bb aws browse`, 40열 이상 terminal, 이미 로드된 resource projection
최종 검토   2026-08-31
다음 검토   preview 정보 우선순위 또는 120열 breakpoint 변경 시
상태        구현 및 responsive golden 검증 완료

## 결정

120열 이상 resource list는 왼쪽 약 2/3의 고밀도 table과 오른쪽 약 1/3의 `Quick Preview`를 함께 표시한다. Preview는 선택 행의 이름·type·state·ID, 최대 네 개의 핵심 field, 두 relation, 두 tag를 사용한다. 네트워크 요청은 하지 않는다. 120열 미만은 기존 full-width table을 유지한다.

Overview의 relation preview는 첫 두 값에 `1:`과 `2:`를 붙인다. 선택한 category에서 `1` 또는 `2`를 누르면 해당 target의 exact read를 바로 실행한다. Enter/Right는 기존처럼 singleton이면 직접 열고, 여러 값이면 전체 relation list를 연다. `Target trace`는 실수로 개별 alias target을 여는 일을 피하기 위해 숫자 shortcut 대상에서 제외한다.

## 현재에서 목표로 바뀐 점

- 변경 전: list에서 행을 열어야 핵심 field, relation, tag를 확인할 수 있었다.
- 변경 후: 넓은 terminal에서는 행 선택만으로 같은 projection의 요약을 오른쪽에서 읽는다.
- 변경 전: 다중 relation category는 Overview→relation list→target으로 이동했다.
- 변경 후: 첫 두 preview target은 Overview에서 숫자 한 번으로 열 수 있고, 세 번째 이후나 비교가 필요할 때만 relation list를 연다.

## 제약과 실패 동작

- Preview는 credential, raw provider payload, 새로운 AWS 조회를 포함하지 않는다.
- 긴 값은 pane 폭에서 줄임표로 자르고 Detail의 원문은 변경하지 않는다.
- 선택 category에 해당 번호의 relation이 없으면 화면을 이동하지 않고 `No preview target for that shortcut.` 상태를 표시한다.
- navigation target이 없는 evidence row를 숫자로 열면 기존 Relationship evidence viewer를 사용한다.
- 좁은 terminal에서 숫자 shortcut은 동작하지만 PREVIEW column이 숨겨지므로 footer hint만 보인다.

## 검증

- 120x30 golden은 table/Quick Preview 분할, 전체 viewport, 고정 footer를 검증한다.
- 50x16 golden은 preview pane 없이 full-width table과 선택/footer가 유지되는지 검증한다.
- model test는 Overview의 `2`가 두 번째 relation target exact read를 직접 dispatch하는지 검증한다.
- view test는 Quick Preview에 field, relation, tag가 모두 이미 로드된 projection에서 표시되는지 검증한다.
