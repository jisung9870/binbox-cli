# B1.5 AWS TUI k9s형 테이블 기준선

독자        저장소 소유자와 B2 이후 TUI/provider 구현 담당자
목적        전체 화면뿐 아니라 selectable content 자체를 k9s형 고밀도 column table로 고정한다
대상 환경   Bubble Tea AWS browser, 40열 이상 character terminal
최종 검토   2026-08-28
다음 검토   B2 ELBv2 relation column 추가 시
상태        완료

관련 문서   [루트 디자인 계약](../DESIGN.md) · [확장 설계](design-aws-tui-202608.md) · [B1 관계 계약](B1-RELATION-CONTRACT.md)

## 결론 — 전체 화면 안의 목록도 table로 전환했다

기존 화면은 full viewport와 k9s key convention을 사용했지만 resource와 relation이 prose row였다. B1.5는 Home, profile/context, cross-profile coverage, resource list, Summary category/field, relation, Tags에 공통 uppercase header, 두 칸 gutter, `>` marker와 full-row selection highlight를 적용했다. 리소스 목록의 inline Preview는 제거해 table이 남은 높이를 사용하고 상세 정보는 Enter 후 Summary에서 읽도록 역할을 분리했다.

```text
  NAME          TYPE          ID          STATUS   ACCOUNT       REGION
> web-api       ec2.instance  i-012345    running  123456789012  ap-northeast-2

  DIR  RELATION   TARGET                    CONDITION  CONFIDENCE
> →    routes-to  s3.bucket:game-binaries   report/*   inferred
```

## 화면별 column 계약

| 화면 | 넓은 terminal | 축소 우선순위 |
|---|---|---|
| Home | `RESOURCE · ALIAS · STATUS` | `RESOURCE · STATUS` |
| Profile/context | `PROFILE · REGION · GROUP · SCOPE` | GROUP, SCOPE를 오른쪽부터 제거 |
| Search coverage | `PROFILE · ACCOUNT · REGION · STATUS · SCOPE · MATCHES` | PROFILE과 STATUS 유지 |
| Resource | `NAME · TYPE · ID · STATUS · ACCOUNT · REGION` | ACCOUNT, REGION, STATUS를 제거하고 50열에서 NAME·TYPE·ID 유지 |
| Summary category | `CATEGORY · COUNT · ACTION` | 40열까지 세 column 유지 |
| Summary field | `FIELD · VALUE` | VALUE를 말줄임하고 Detail에서 전체 내용 제공 |
| Relation | `DIR · RELATION · TARGET · CONDITION · CONFIDENCE · SCOPE` | SCOPE, CONFIDENCE를 제거하되 40열까지 CONDITION 유지 |
| Tags | `KEY · VALUE` | VALUE를 말줄임하고 선택 preview에서 전체 값 제공 |

Detail과 policy JSON은 row 단위 table보다 문서형 scrolling이 정확하므로 기존 key/value viewer를 유지한다. Plain fallback도 numbered command loop를 유지한다.

## 구현·회귀 기준

- 모든 cell은 control-safe text를 ANSI display width로 truncate한다.
- 선택 상태는 marker와 full-row style을 함께 사용하고 `NO_COLOR`에서도 marker가 남는다.
- column header는 dark/light theme에서 cyan 계열 강조를 사용하지만 의미를 색상에만 맡기지 않는다.
- relation direction은 `outgoing=→`, `incoming=←`, 미지정=`·`로 표시한다.
- filter, cursor, Right/Enter open, Left back, `Ctrl-o`/`Ctrl-i`, context 변경, refresh 동작은 layout 변경 전 계약을 유지한다.

## 2026-08-28 검증 결과

| 검증 | 결과 |
|---|---|
| 120x30, 80x24, 50x16, 40x12 golden | 통과 |
| 120/80/50 relation responsive column test | 통과 |
| context/Tags table header test | 통과 |
| `go test ./... -count=1` | 통과, 모든 Go package 성공 |
| `go test -race ./internal/bb/awsbrowser/... -count=1` | 통과 |
| `go vet ./...` | 통과 |
| `go build -trimpath -ldflags='-s -w' ./cmd/bb` | 통과, 14,189,922 bytes |
| stripped binary hard cap | 통과, 40 MiB 미만 |

실제 AWS credential과 production account는 사용하지 않았다. 렌더링 검증은 deterministic model/golden과 기존 PTY lifecycle regression으로 수행했다.
