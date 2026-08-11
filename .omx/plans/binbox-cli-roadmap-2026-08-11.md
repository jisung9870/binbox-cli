# binbox-cli 전환 로드맵

작성일: 2026-08-11

## 목표

`binbox-cli`를 기존 `binbox`의 비Git 일상 기능을 대체하는 설치형 Go CLI로
안정화한다. 릴리스가 macOS/Linux에서 재현 가능해야 하고, Bubble Tea 선택기는
실제 터미널과 자동화 환경 모두에서 stdout 계약을 깨지 않아야 한다. 개인 장비와
회사 장비의 컷오버 증거가 확보된 뒤에만 레거시 저장소 보관 여부를 결정한다.

현재 제품 경계는 설치형 단일 바이너리, XDG 상태, 외부 도구의 명시적 소유권이다
([docs/legacy-comparison.md:3](../../docs/legacy-comparison.md),
[docs/architecture.md:17](../../docs/architecture.md)). 개인 장비 컷오버와 LazyVim
소비자 전환은 이미 완료되었지만, 추가 상태가 있는 장비의 점검은 남아 있다
([docs/migration-plan.md:71](../../docs/migration-plan.md),
[docs/operations.md:110](../../docs/operations.md)).

## 범위와 비범위

범위:

- macOS/Linux 릴리스 생성, 검증, 설치, 업그레이드와 롤백
- Bubble Tea 선택기를 사용하는 `tm`, `wenv`, `sec`의 터미널 UX와 fallback
- `project/tm`, `profile/wenv/sec`, `kx/assm`, `tfx/tvx`, `port`, `doctor`,
  `setup nvim`의 대체 가능성 확인
- 장비별 체크 우선 컷오버와 복구 증거

비범위:

- `bb git`, `bb gx` 신규 기능 또는 UX 개선
- Orca가 소유하는 agent/worktree lifecycle 재구현
  ([docs/legacy-comparison.md:24](../../docs/legacy-comparison.md))
- 레거시 포맷의 호환성 파괴, 자격 증명 저장, MCP 설정 변경
  ([docs/migration-plan.md:83](../../docs/migration-plan.md))
- 사용자 결정 전 setup/workbench 또는 기존 binbox 저장소 삭제·archive

## 우선순위 로드맵

### P0. 릴리스 파이프라인 이식성 복구

목표: macOS와 Ubuntu에서 같은 입력으로 동일한 릴리스 산출물을 만든다.

현재 차단 근거:

- `scripts/release.sh`는 GNU 전용 `tar --sort`, `--mtime`, `--owner` 옵션을
  사용한다 ([scripts/release.sh:49](../../scripts/release.sh)).
- checksum 생성도 macOS 기본 도구가 아닌 `sha256sum`에 고정되어 있다
  ([scripts/release.sh:63](../../scripts/release.sh)).
- 실패 cleanup은 읽기 전용 Go module cache를 그대로 `rm -rf`하여 잔여물을
  남길 수 있다 ([scripts/test-release-guard.sh:8](../../scripts/test-release-guard.sh)).

구현:

1. Go 표준 라이브러리 `archive/tar`, `compress/gzip`, `crypto/sha256` 기반의
   내부 릴리스 패키저를 추가한다. tar header의 이름, mode, uid/gid, mtime과
   gzip header를 명시해 호스트 tar 구현에 의존하지 않게 한다.
2. `scripts/release.sh`는 바이너리 빌드와 패키저 호출만 담당하도록 축소하고,
   네 개 대상 `linux/{amd64,arm64}`, `darwin/{amd64,arm64}` 계약은 유지한다
   ([scripts/release.sh:43](../../scripts/release.sh)).
3. release-guard fixture cleanup 전에 fixture 내부 권한을 복원하고, 성공·실패
   양쪽에서 `.tmp/bb-release-guard-test.*`가 남지 않는 테스트를 추가한다.
4. 동일 commit을 두 번 빌드해 각 archive와 `checksums.txt` SHA-256이 동일한지
   비교한다.

완료 조건:

- macOS와 Ubuntu에서 `scripts/test-release-guard.sh` exit 0
- 두 번 생성한 네 archive의 SHA-256이 전부 동일
- `tar --sort`와 `sha256sum` 실행 의존성 0개
- 성공 및 의도적 실패 후 `.tmp/bb-release-guard-test.*` 잔여물 0개
- `go test ./...`, `go test -race ./internal/bb`, `go vet ./...` 통과

예상 규모: 1~2 작업일

### P1. v0.5.0 릴리스와 설치/복구 증명

목표: Bubble Tea 선택기와 macOS 수정이 포함된 첫 안정 릴리스를 배포한다.

GitHub Actions는 `v*` annotated tag에서 네 플랫폼 asset, checksum, SBOM을
게시하도록 구성되어 있다 ([.github/workflows/release.yml:3](../../.github/workflows/release.yml),
[.github/workflows/release.yml:30](../../.github/workflows/release.yml)). 실제 tag 생성은
외부 배포 변경이므로 P0 완료 후 별도 릴리스 결정 게이트로 둔다.

구현:

1. v0.4.1 이후 변경을 사용자 관점의 release note로 정리한다.
2. clean HEAD를 가리키는 annotated `v0.5.0` tag를 생성하고 push한다.
3. 네 archive, `checksums.txt`, SPDX SBOM 업로드를 확인한다.
4. 격리 HOME에서 신규 설치, v0.4.1→v0.5.0 업그레이드, 동일 버전 no-op,
   검증 실패 시 이전 바이너리 복원 시나리오를 실행한다. 설치기는 현재 checksum,
   버전 확인과 같은 디렉터리 rename을 계약으로 가진다
   ([docs/operations.md:49](../../docs/operations.md)).

완료 조건:

- `v0.5.0` annotated tag와 release target commit 일치
- 네 archive + checksums + SBOM 존재
- 각 archive의 `bb version`이 `0.5.0`, commit이 tag commit과 일치
- 신규 설치/업그레이드/no-op/rollback 네 시나리오 exit 0
- 실제 사용자 HOME, XDG 설정 파일 변경 0개

예상 규모: 0.5~1 작업일

### P2. Bubble Tea 선택기 실사용 안정화

목표: fuzzy selector가 편리하되 자동화 계약을 침범하지 않게 한다.

현재 선택기는 TTY에서 Bubble Tea, 비TTY·dumb terminal·`BB_SELECTOR=plain`에서
번호 선택기로 전환하며 UI를 stderr로 보낸다
([internal/bb/select.go:17](../../internal/bb/select.go),
[internal/bb/select.go:31](../../internal/bb/select.go)).

구현:

1. fuzzy 입력, 결과 없음, Enter, Esc, Ctrl-C, resize, 긴 목록의 model test를
   추가한다.
2. `tm` project/session, `wenv`, `sec copy` 각각에 대해 stable value가 반환되는
   통합 테스트를 추가한다.
3. 비TTY와 `BB_SELECTOR=plain`에서 stdout JSON/shell-eval 출력이 byte 단위로
   기존 fixture와 같은지 검증한다.
4. macOS Terminal/iTerm2와 tmux 안팎에서 수동 smoke matrix를 기록한다.

완료 조건:

- 선택기 키보드/필터/resize 테스트 전부 통과
- 대상 네 흐름에서 화면 label과 내부 value 혼동 0건
- TUI 렌더링이 stdout에 나타나는 테스트 0건
- 취소 시 상태 파일과 외부 프로세스 변경 0건
- 외부 `fzf` 설치 없이 모든 selector 흐름 동작

예상 규모: 1~2 작업일

### P3. 비Git 기능 대체성 감사

목표: 실제 사용 기능을 기준으로 기존 binbox가 필요한 이유를 0개로 줄인다.

현재 비교 문서는 이식된 기능과 의도적으로 제외한 기능을 구분하고 있다
([docs/legacy-comparison.md:28](../../docs/legacy-comparison.md)). 이 단계에서는
문서 주장만 믿지 않고 명령별 smoke evidence를 다시 수집한다.

구현:

1. 비Git 명령을 `retained / migrated / intentionally retired / external owner` 중
   하나로 분류하고 실제 실행 증거 링크를 붙인다.
2. `doctor --json`의 required capability와 recovery 문구가 설치 직후에도
   안정적인지 golden test로 고정한다
   ([docs/migration-plan.md:108](../../docs/migration-plan.md)).
3. 프로젝트 import, wenv import, sec 기존 ciphertext, LazyVim link를 읽기 우선으로
   검사하고 legacy source byte가 변하지 않음을 확인한다.
4. 사용 기록이 없는 기능은 새로 확장하지 않고 유지 또는 의도적 retire로 결정한다.

완료 조건:

- 범위 내 모든 명령에 분류, owner, smoke 결과, recovery 방법 존재
- legacy-only 비Git 일상 사용 사례 0개
- Git CLI 변경 diff 0줄
- credential, secret plaintext, machine-specific path의 commit 0건
- compatibility matrix와 실제 테스트 결과 불일치 0건

예상 규모: 2~3 작업일

### P4. 장비별 컷오버

목표: 개인 장비 외 추가 상태가 있는 장비에서도 binbox-cli가 기본 `bb`가 된다.

문서는 회사 장비 등 추가 상태가 있는 각 장비에서 inventory와 rollback 검사를
반복하도록 요구한다 ([docs/operations.md:110](../../docs/operations.md)).

장비별 순서:

1. `doctor`, sessionizer `--check`, nvim dry-run과 기존 파일 hash를 수집한다.
2. XDG backup과 기존 실행 파일 rollback 위치를 기록한다.
3. v0.5.0을 설치하고 `bb shell init zsh`로 checkout-sourced startup을 교체한다.
4. project import `--apply` 후 중복 0건과 legacy source byte 동일성을 확인한다.
5. 7일 관찰 기간 동안 legacy fallback 호출과 복구 실행 여부를 기록한다.

완료 조건:

- 대상 장비마다 pre/post inventory와 rollback 경로 존재
- 7일간 legacy-only 명령 호출 0건
- project/wenv/sec/nvim 데이터 손실 0건
- rollback drill 1회 성공 또는 격리 fixture로 동일 절차 증명
- machine-specific 값의 저장소 commit 0건

예상 규모: 장비당 1 작업일 + 7일 관찰

### P5. 레거시 저장소 보관 결정

목표: 삭제가 아니라 증거 기반으로 archive/read-only 전환 여부를 결정한다.

setup/workbench archive는 모든 retained data에 owner/export가 있을 때만 가능하다
([docs/migration-plan.md:122](../../docs/migration-plan.md)).

결정 게이트:

- P0~P4 완료 증거가 모두 존재한다.
- rollback snapshot의 위치, 보존 기간, 복원 절차가 문서화되어 있다.
- legacy-only 비Git 사용 사례가 관찰 기간 동안 0개다.
- archive 대상별 retained data owner가 모두 지정되어 있다.

게이트를 통과하면 archive를 별도 승인 작업으로 수행한다. 하나라도 통과하지 못하면
레거시는 read-only rollback/reference 상태로 유지한다.

예상 규모: 0.5 작업일, 사용자 결정 필요

## 마일스톤

| 마일스톤 | 포함 단계 | 통과 증거 |
|---|---|---|
| M1 Release-ready | P0 | macOS/Ubuntu release guard와 재현성 통과 |
| M2 v0.5.0 available | P1 | assets/checksum/SBOM 및 격리 설치/복구 통과 |
| M3 Daily-use ready | P2~P3 | selector matrix와 비Git parity audit 통과 |
| M4 Cutover complete | P4 | 장비별 7일 legacy-only 사용 0건 |
| M5 Legacy decision | P5 | archive 또는 read-only 유지 결정 기록 |

## 위험과 완화

- 재현 가능한 archive가 Go 버전 차이로 달라질 수 있음: tar/gzip header를 모두
  명시하고 CI Go 버전을 `go.mod`로 고정하며 double-build SHA 비교를 수행한다.
- private GitHub release 인증이 로컬 성공을 가릴 수 있음: token을 직접 다루지 않고
  기존 `gh` 인증 경계를 사용한다 ([docs/operations.md:33](../../docs/operations.md)).
- 실제 장비 컷오버에서 상태 손실 가능: 모든 mutation 전에 check/hash/backup을
  수집하고 legacy source는 수정하지 않는다
  ([docs/operations.md:63](../../docs/operations.md)).
- selector 개선이 stdout 계약을 오염할 수 있음: stderr-only test와 non-TTY golden
  test를 release gate에 포함한다.
- 범위가 Git/agent lifecycle로 다시 확장될 수 있음: Git diff 0줄과 Orca external
  ownership을 각 마일스톤 검토 기준으로 둔다.

## 전체 검증 명령

```sh
go test ./...
go test -race ./internal/bb
go vet ./...
scripts/test-install.sh
scripts/test-release-guard.sh
git diff --check
```

P1에서는 위 검증에 double-build SHA 비교와 격리 HOME 설치/rollback 검증을
추가한다. P4는 실제 장비 상태를 저장소에 복사하지 않고 redacted 결과와 hash만
증거로 남긴다.

## 중단 조건

- persisted legacy format/public command 변경이 필요하면 구현을 중단하고 별도 결정
- credential 또는 secret plaintext 저장이 필요하면 구현을 중단
- 실제 release tag push, 장비 설정 mutation, 저장소 archive는 해당 단계의 명시적
  승인 전에는 수행하지 않음
- 각 단계의 완료 조건이 충족되지 않으면 다음 단계로 진행하지 않음

## 권장 착수 순서

즉시 P0를 수행한다. P0 완료 후 `v0.5.0` tag 생성 여부를 결정하고, P1과 P2는
순차 진행한다. P3은 P2 결과를 포함해 감사하고, P4 관찰 기간이 끝나기 전에는
P5 archive 결정을 내리지 않는다.
