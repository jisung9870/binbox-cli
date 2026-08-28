# ADR-002. Optional snapshot graph에 cgo-free SQLite 채택

## Status

Accepted for optional snapshot storage on 2026-08-28. Live progressive browse remains the default source.

## Context

B3는 여러 profile/account/region에서 SG referenced-by와 attached resource를 역방향으로 찾되, 조회하지 못한 scope를 빈 결과와 구분해야 했다. 저장소는 versioned run, profile별 observation, relation evidence, coverage, retention과 corruption recovery를 하나의 local file에서 제공해야 하며 release는 `CGO_ENABLED=0`과 stripped binary 40MiB cap을 지켜야 한다.

평가 후보는 `modernc.org/sqlite v1.57.0`, `github.com/ncruces/go-sqlite3 v0.35.3`, persistent store 미도입이었다. `mattn/go-sqlite3`는 cgo를 요구해 사전 탈락했다. 측정 결과는 [B3 PoC 결과](B3-SNAPSHOT-GRAPH-POC.md)에 기록했다.

## Decision

Optional snapshot graph store에 `modernc.org/sqlite v1.57.0`과 정확히 호환되는 `modernc.org/libc v1.74.4`를 사용한다.

DB는 user state directory의 owner-only local file을 전제로 하며 `_foreign_keys=on`, `_txlock=immediate`, `_busy_timeout=5000`, `_journal_mode=WAL`, `_synchronous=FULL`, `_defensive=1`, `_dqs=0`을 고정한다. AWS 수집은 transaction 밖에서 수행하고 complete run 전체와 active pointer만 한 transaction으로 commit한다.

Canonical resource key는 resource table에 한 번 저장한다. Relation은 integer source/target ID를 참조하고 `(run_id, source_id)`, `(run_id, target_id)`를 인덱싱한다. Reverse 결과는 target index에서 원본 outgoing edge를 읽어 incoming으로 투영하며 별도 reverse edge를 저장하지 않는다.

## Consequences

좋아지는 점:

- cgo 없이 darwin/linux amd64/arm64 release를 유지한다.
- transaction, strict table, foreign key, indexed reverse query와 corruption diagnostics를 표준 SQL로 구현한다.
- 2026-08-28 Apple M3 합성 fixture에서 100k/500k relation·reverse·bounded path p95가 모두 0.2ms 미만이었다.
- DB 손상 시 원본을 보존하고 explicit sync로 재생성할 수 있다.

받아들이는 비용:

- snapshot package를 강제로 link한 darwin/arm64 stripped binary는 14,713,042 bytes에서 18,400,274 bytes로 3,687,232 bytes 증가했다. product binary는 B3에서 package를 아직 연결하지 않아 baseline과 같다.
- one-run 100k/500k fixture가 229.8MiB다. 기본 retention 2라도 실제 field 길이와 free-page 상태에 따라 500MiB에 접근할 수 있다.
- modernc generated source는 module download/cache/SBOM 비용을 늘린다. upstream은 `libc` version mismatch를 지원하지 않으므로 dependency update를 같이 검증해야 한다.
- WAL은 network filesystem에 적합하지 않고 DB 파일 하나만 복사한 backup은 안전하지 않다. 이 store는 재생성 가능한 local cache이며 authoritative inventory가 아니다.
- BSD-3-Clause modernc와 MIT sqlite-vec 고지는 product release에 snapshot package가 연결되기 전에 distribution notice에 포함해야 한다. SQLite 본체는 public domain이다.
- Recursive CTE path는 synthetic graph에서 목표를 만족하지 못했다. 최종 bounded BFS는 storage index를 사용하지만 packet reachability 의미를 제공하지 않는다.

## Alternatives

`ncruces/go-sqlite3`는 cgo-free이고 module footprint가 작아 CI cache에는 유리하다. 2026-08-28 최소 binary 측정에서 modernc보다 약 1.77MB 컸고 B3의 binary·query 요구를 더 잘 충족하지 않아 선택하지 않았다.

Persistent store 미도입은 live browse를 가장 단순하게 유지한다. 그러나 multi-profile reverse lookup이 매번 전체 SG fan-out을 요구하고 run coverage·diff 기반을 제공하지 못하므로 optional B3 경로에는 부적합했다. 기본 live browse에는 이 대안을 계속 유지한다.

## References

- [modernc.org/sqlite v1.57.0 package](https://pkg.go.dev/modernc.org/sqlite@v1.57.0)
- [modernc.org/sqlite changelog](https://gitlab.com/cznic/sqlite/-/blob/v1.57.0/CHANGELOG.md)
- [SQLite transaction](https://www.sqlite.org/lang_transaction.html)
- [SQLite WAL](https://www.sqlite.org/wal.html)
- [SQLite integrity_check](https://www.sqlite.org/pragma.html#pragma_integrity_check)
