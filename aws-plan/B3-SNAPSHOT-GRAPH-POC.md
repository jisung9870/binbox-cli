# B3 SG reverse snapshot graph PoC 결과

독자        binbox-cli 개발·운영자와 B4 구현 담당자
목적        optional snapshot graph가 멀티 profile SG reverse query의 성능·불완전 coverage·복구 요구를 만족하는지 판정한다
대상 환경   Go 1.25.11, darwin/arm64, Apple M3, `CGO_ENABLED=0`, local SQLite file
최종 검토   2026-08-28
다음 검토   B4 public `sync`/`refs` command 설계 전 또는 30 account 초과 시
상태        PoC 완료 — optional store로 채택, live browser 기본 경로는 유지

관련 문서   [SQLite ADR](ADR-002-SQLITE-SNAPSHOT-GRAPH.md) · [확장 설계](design-aws-tui-202608.md) · [관계 계약](spec-aws-tui-relations-202608.md)

## SQLite store는 목표를 충족했지만 public 기능은 B4로 남는다

`modernc.org/sqlite v1.57.0` 기반 저장소는 완성된 run만 active pointer로 교체하고, 성공·실패·미관찰 coverage를 같은 transaction에 저장한다. SG reverse는 incoming edge를 복제하지 않고 `(run_id, target_id)` 인덱스를 사용한다. 10만 resource/50만 relation 합성 fixture의 p95는 relation 0.133ms, reverse 0.098ms, bounded path 0.129ms로 200ms 목표를 충족했다.

이번 단계는 `internal/bb/awsbrowser/snapshot`의 저장·sync 계약 PoC다. AWS SDK collector, `bb aws sync`, TUI snapshot source, `refs/whois/diff`는 연결하지 않았다. 기존 `bb aws browse`는 live progressive read를 계속 사용한다.

## 기준과 판정

측정 전에 고정한 기준은 B3 설계의 query p95 200ms, cgo-free release, 40MiB stripped binary hard cap, 중단된 run 비노출, newest-run retention, 손상 파일 보존 후 재생성이다.

| 평가축 | 2026-08-28 실측/fixture | 판정 |
|---|---:|---|
| relation p95 | 133µs, 101 samples | 통과 |
| reverse p95 | 98µs, 101 samples | 통과 |
| bounded path p95 | 129µs, 101 samples | 통과 |
| 100k/500k sync write | 9.300s | 5분 재검토 기준 이내 |
| one-run store size | 229.8MiB | 채택, 디스크 상한 필요 |
| forced-link darwin/arm64 binary | 18,400,274 bytes | 40MiB cap 통과 |
| default retention | newest 2 complete runs | fixture 통과 |
| corruption recovery | original file quarantine + fresh store | fixture 통과 |

합성 fixture는 이름과 condition을 의도적으로 짧게 만들었다. 실제 SG rule description이 길면 store size와 write time은 증가하므로 229.8MiB를 운영 상한으로 해석하면 안 된다.

## 2 profile × 2 region fixture는 빈 결과와 조회 실패를 분리한다

두 profile은 같은 account를 관찰해 canonical SG와 instance는 한 resource로 합쳐지고 profile별 observation은 두 건으로 남는다. 한 region/profile은 `access-denied`로 실패하며, EC2 attachment만 수집한 범위를 분명히 하기 위해 각 scope의 ELBv2 coverage는 `not-observed/ec2-only`로 기록된다.

Cross-account SG target은 대상 account ID가 알려진 exact `ResourceRef`로 저장한다. 대상 account를 실제로 관찰하지 못해도 reverse index에서 source SG의 `references`와 EC2 instance의 `uses`를 찾을 수 있다. 이 결과는 대상 account가 완전히 검색됐다는 뜻이 아니며 coverage와 함께 읽어야 한다.

## 정규화한 edge ID가 store 크기를 58.8% 줄였다

초기 스키마는 canonical text key를 relation row와 source/target 인덱스에 반복 저장해 같은 fixture가 558.3MiB였다. resource table에 key를 한 번 저장하고 relation이 integer `source_id/target_id`를 참조하도록 바꾼 뒤 229.8MiB로 줄었다.

초기 recursive CTE path는 같은 fixture에서 60초 이상 완료되지 않아 폐기했다. 최종 구현은 `(run_id, source_id)` 인덱스로 frontier를 batch 조회하는 breadth-first search이며 depth 32, visited resource 100,000으로 제한한다. 이는 graph 연결 경로일 뿐 protocol/port 기반 packet reachability 판정이 아니다.

## 실패와 복구 계약

- Sync는 AWS 수집 중 transaction을 열지 않는다. scope 결과를 모은 뒤 하나의 immediate transaction으로 run·resource·observation·relation·coverage·active pointer를 commit한다.
- scope 하나가 실패해도 나머지 성공 결과와 failed coverage를 complete run으로 commit한다. context cancellation은 commit 전에 종료되어 이전 active run을 유지한다.
- open 시 `PRAGMA integrity_check`가 실패하면 DB와 WAL/SHM sidecar를 `.corrupt-<UTC timestamp>`로 rename해 보존하고 새 DB를 만든다. 자동 삭제나 SQLite repair는 하지 않는다.
- 완료 검사는 `integrity_check`와 `foreign_key_check`를 함께 실행한다.
- 기본 retention은 complete run 2개다. 229.8MiB one-run fixture 기준 2개 relation/observation의 단순 합은 최대 약 459.6MiB `(추정, shared resource와 SQLite page reuse 제외)`다.

## 검증 환경과 운영 환경의 차이

PoC는 local NVMe, 단일 process, 한 account 모양의 synthetic graph를 사용했다. NFS/SMB, 동시 TUI reader, 30 account fan-out, 긴 SG descriptions, 실제 AWS API latency는 측정하지 않았다. WAL DB 파일만 복사하는 backup도 검증하지 않았으므로 snapshot은 재생성 가능한 local cache로만 취급한다.

EC2 instance attachment와 SG-to-SG rule만 범위에 포함했다. ELBv2, RDS, Lambda, ECS 등 SG를 사용하는 다른 서비스는 검색하지 않았다는 coverage가 반드시 필요하다.

## 재검토 트리거

- one-run store가 500MiB를 넘거나 default retention 저장소가 1GiB를 넘는다.
- 10만 resource/50만 relation query p95가 200ms를 넘거나 sync write가 5분을 넘는다.
- profile/account가 30개를 넘거나 concurrent reader가 필요해진다.
- public B4 query가 unresolved ARN/DNS/CIDR target 또는 snapshot diff evidence를 요구한다.
- SQLite driver가 pinned `modernc.org/libc v1.74.4` 이외 버전을 선택하거나 cgo-free release가 실패한다.
