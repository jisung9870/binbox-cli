package snapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
)

const testRegion = "ap-northeast-2"

func TestCoordinatorSyncCommitsPartialCoverageDuplicateObservationsAndReverseEdges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, err := Open(ctx, filepath.Join(t.TempDir(), "snapshot.db"), 3)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	target := ref("222222222222", testRegion, "ec2.security-group", "sg-target")
	source := ref("111111111111", testRegion, "ec2.security-group", "sg-source")
	instance := ref("111111111111", testRegion, "ec2.instance", "i-web")
	now := time.Date(2026, 8, 28, 1, 2, 3, 0, time.UTC)
	collection := Collection{
		AccountID: "111111111111",
		Resources: []Resource{{Ref: source, Name: "source-sg"}, {Ref: instance, Name: "web"}},
		Relations: []Relation{
			relation(source, target, awsbrowser.RelationReferences, "ingress tcp 443 from 222222222222/sg-target", now),
			relation(instance, target, awsbrowser.RelationUses, "network interface eni-123", now),
		},
	}
	collector := fixtureCollector{
		results: map[string]Collection{
			"primary/" + testRegion:  collection,
			"readonly/" + testRegion: collection,
			"primary/us-east-1":      {AccountID: "111111111111"},
		},
		errors: map[string]error{
			"readonly/us-east-1": &CollectionError{Kind: "access-denied", Err: errors.New("denied")},
		},
	}
	clock := &stepClock{next: now}
	coordinator := Coordinator{Store: store, Collector: collector, Now: clock.Now}
	scopes := []Scope{
		{Profile: "primary", AccountID: "111111111111", Region: testRegion, Service: "ec2"},
		{Profile: "primary", AccountID: "111111111111", Region: "us-east-1", Service: "ec2"},
		{Profile: "readonly", AccountID: "111111111111", Region: testRegion, Service: "ec2"},
		{Profile: "readonly", AccountID: "111111111111", Region: "us-east-1", Service: "ec2"},
		{Profile: "primary", AccountID: "111111111111", Region: testRegion, Service: "elbv2", NotObserved: true, NotObservedReason: "ec2-only"},
		{Profile: "primary", AccountID: "111111111111", Region: "us-east-1", Service: "elbv2", NotObserved: true, NotObservedReason: "ec2-only"},
		{Profile: "readonly", AccountID: "111111111111", Region: testRegion, Service: "elbv2", NotObserved: true, NotObservedReason: "ec2-only"},
		{Profile: "readonly", AccountID: "111111111111", Region: "us-east-1", Service: "elbv2", NotObserved: true, NotObservedReason: "ec2-only"},
	}
	run, err := coordinator.Sync(ctx, scopes)
	if err != nil {
		t.Fatal(err)
	}
	if run.SchemaVersion != SchemaVersion {
		t.Fatalf("schema version = %d", run.SchemaVersion)
	}

	coverage, err := store.Coverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(coverage) != 8 {
		t.Fatalf("coverage = %#v", coverage)
	}
	failed, notObserved := 0, 0
	for _, item := range coverage {
		if item.Status == CoverageFailed && item.ErrorKind == "access-denied" {
			failed++
		}
		if item.Status == CoverageNotObserved && item.ErrorKind == "ec2-only" {
			notObserved++
		}
	}
	if failed != 1 || notObserved != 4 {
		t.Fatalf("failed coverage = %#v", coverage)
	}

	reverse, err := store.Reverse(ctx, target, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(reverse) != 2 {
		t.Fatalf("reverse edges = %#v", reverse)
	}
	if reverse[0].Relation.Direction != awsbrowser.RelationIncoming || reverse[1].Relation.Direction != awsbrowser.RelationIncoming {
		t.Fatalf("reverse direction = %#v", reverse)
	}
	if len(reverse[0].Observers) != 2 || len(reverse[1].Observers) != 2 {
		t.Fatalf("relation observer provenance was collapsed: %#v", reverse)
	}
	if reverse[0].TargetKey != mustKey(t, target) || reverse[1].TargetKey != mustKey(t, target) {
		t.Fatalf("cross-account target was not preserved: %#v", reverse)
	}
	var observationCount int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM observation o JOIN resource r ON r.id=o.resource_id WHERE o.run_id=? AND r.resource_key=?`, run.ID, mustKey(t, source)).Scan(&observationCount); err != nil {
		t.Fatal(err)
	}
	if observationCount != 2 {
		t.Fatalf("duplicate profile observations collapsed: %d", observationCount)
	}
	var relationCount int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM relation WHERE run_id=?`, run.ID).Scan(&relationCount); err != nil {
		t.Fatal(err)
	}
	if relationCount != 2 {
		t.Fatalf("duplicate relation observations were not canonicalized: %d", relationCount)
	}

	path, err := store.FindPath(ctx, source, target, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(path) != 2 || path[0] != mustKey(t, source) || path[1] != mustKey(t, target) {
		t.Fatalf("path = %#v", path)
	}
}

func TestResourceRefKeyRoundTripRejectsNonCanonicalInput(t *testing.T) {
	t.Parallel()
	want := ref("111111111111", testRegion, "ec2.security-group", "sg-123")
	key := mustKey(t, want)
	got, err := ParseResourceRefKey(key)
	if err != nil || got != want {
		t.Fatalf("ref=%#v error=%v", got, err)
	}
	for _, value := range []string{
		key + "&account=111111111111",
		"partition=aws&account=111111111111&region=" + testRegion + "&type=ec2.security-group&id=sg-123",
		"account=bad&id=sg-123&partition=aws&region=" + testRegion + "&type=ec2.security-group",
	} {
		if _, err := ParseResourceRefKey(value); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("non-canonical key %q error=%v", value, err)
		}
	}
}

func TestCommitRunKeepsActivePointerAtomicAndRetainsNewestRuns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, err := Open(ctx, filepath.Join(t.TempDir(), "snapshot.db"), 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	resource := Resource{Ref: ref("111111111111", testRegion, "ec2.security-group", "sg-one"), Name: "one"}
	var latest Run
	for index := 0; index < 3; index++ {
		at := time.Date(2026, 8, 28, 2, index, 0, 0, time.UTC)
		latest, err = store.CommitRun(ctx, RunInput{
			StartedAt: at, CompletedAt: at.Add(time.Second),
			Resources:    []Resource{resource},
			Observations: []Observation{{Resource: resource.Ref, Profile: "primary", AccountID: "111111111111", Region: testRegion, ObservedAt: at}},
			Coverage:     []Coverage{{Profile: "primary", AccountID: "111111111111", Region: testRegion, Service: "ec2", Status: CoverageSucceeded}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	olderCompletion := time.Date(2026, 8, 27, 23, 0, 0, 0, time.UTC)
	latest, err = store.CommitRun(ctx, RunInput{
		StartedAt: olderCompletion, CompletedAt: olderCompletion.Add(time.Second),
		Resources:    []Resource{resource},
		Observations: []Observation{{Resource: resource.Ref, Profile: "primary", AccountID: "111111111111", Region: testRegion, ObservedAt: olderCompletion}},
		Coverage:     []Coverage{{Profile: "primary", AccountID: "111111111111", Region: testRegion, Service: "ec2", Status: CoverageSucceeded}},
	})
	if err != nil {
		t.Fatal(err)
	}
	activeBefore, err := store.ActiveRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if activeBefore.ID != latest.ID {
		t.Fatalf("active = %s, latest = %s", activeBefore.ID, latest.ID)
	}
	_, err = store.CommitRun(ctx, RunInput{})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid commit error = %v", err)
	}
	activeAfter, err := store.ActiveRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if activeAfter.ID != activeBefore.ID {
		t.Fatalf("failed commit changed active run: %s -> %s", activeBefore.ID, activeAfter.ID)
	}
	var runCount int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM snapshot_run`).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 2 {
		t.Fatalf("retained runs = %d", runCount)
	}
}

func TestOpenQuarantinesCorruptDatabaseAndCreatesFreshStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "snapshot.db")
	if err := os.WriteFile(path, []byte("this is not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, result, err := Open(ctx, path, 3)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if result.RecoveredFrom == "" {
		t.Fatal("corrupt database was not quarantined")
	}
	if _, err := os.Stat(result.RecoveredFrom); err != nil {
		t.Fatalf("quarantine file: %v", err)
	}
	if err := store.IntegrityCheck(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActiveRun(ctx); !errors.Is(err, ErrNoActiveRun) {
		t.Fatalf("fresh store active run error = %v", err)
	}
}

func TestOpenRejectsUnknownSchemaWithoutQuarantining(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "snapshot.db")
	store, _, err := Open(ctx, path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, result, err := Open(ctx, path, 2)
	if !errors.Is(err, ErrSchemaVersion) {
		t.Fatalf("schema error = %v", err)
	}
	if result.RecoveredFrom != "" {
		t.Fatalf("schema mismatch was quarantined as corruption: %s", result.RecoveredFrom)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("schema mismatch database was moved: %v", err)
	}
}

func TestOpenMigratesV1RelationsWithExplicitUnknownObserver(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "snapshot.db")
	store, _, err := Open(ctx, path, 2)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 28, 2, 15, 0, 0, time.UTC)
	source := ref("111111111111", testRegion, "ec2.security-group", "sg-source")
	target := ref("111111111111", testRegion, "ec2.security-group", "sg-target")
	if _, err := store.CommitRun(ctx, RunInput{
		StartedAt: at, CompletedAt: at.Add(time.Second),
		Resources: []Resource{{Ref: source}, {Ref: target}},
		Observations: []Observation{
			{Resource: source, Profile: "profile-a", AccountID: source.AccountID, Region: source.Region, ObservedAt: at},
			{Resource: source, Profile: "profile-b", AccountID: source.AccountID, Region: source.Region, ObservedAt: at},
		},
		Relations: []Relation{relation(source, target, awsbrowser.RelationReferences, "rule-id=sgr-1", at)},
		Coverage:  []Coverage{{Profile: "profile-a", AccountID: source.AccountID, Region: source.Region, Service: "ec2-sg", Status: CoverageSucceeded}},
	}); err != nil {
		t.Fatal(err)
	}
	const downgradeToActualV1DDL = `
DROP TABLE relation_observer;
DROP INDEX relation_source_idx;
DROP INDEX relation_target_idx;
ALTER TABLE relation RENAME TO relation_v2_fixture;
CREATE TABLE relation (
  id INTEGER PRIMARY KEY,
  run_id TEXT NOT NULL REFERENCES snapshot_run(id) ON DELETE CASCADE,
  source_id INTEGER NOT NULL REFERENCES resource(id),
  target_id INTEGER NOT NULL REFERENCES resource(id),
  relation_type TEXT NOT NULL,
  direction TEXT NOT NULL CHECK (direction = 'outgoing'),
  confidence TEXT NOT NULL,
  condition TEXT NOT NULL,
  reason TEXT NOT NULL,
  operation TEXT NOT NULL,
  scope TEXT NOT NULL,
  observed_at TEXT NOT NULL,
  UNIQUE (run_id, source_id, target_id, relation_type, condition, operation)
) STRICT;
INSERT INTO relation(id,run_id,source_id,target_id,relation_type,direction,confidence,condition,reason,operation,scope,observed_at)
SELECT id,run_id,source_id,target_id,relation_type,direction,confidence,condition,reason,operation,scope,observed_at
FROM relation_v2_fixture;
DROP TABLE relation_v2_fixture;
CREATE INDEX relation_source_idx ON relation(run_id, source_id);
CREATE INDEX relation_target_idx ON relation(run_id, target_id);
PRAGMA user_version = 1;
`
	if _, err := store.db.ExecContext(ctx, downgradeToActualV1DDL); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, result, err := Open(ctx, path, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if result.RecoveredFrom != "" {
		t.Fatalf("migration was treated as corruption: %s", result.RecoveredFrom)
	}
	edges, err := store.Reverse(ctx, target, 10)
	if err != nil || len(edges) != 1 || len(edges[0].Observers) != 1 {
		t.Fatalf("edges=%#v error=%v", edges, err)
	}
	observer := edges[0].Observers[0]
	if observer.Profile != "legacy-unknown" || observer.AccountID != source.AccountID || observer.Region != testRegion {
		t.Fatalf("legacy observer=%#v", observer)
	}
}

func TestCommitRunRejectsDuplicateCoverageBeforeSQL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, err := Open(ctx, filepath.Join(t.TempDir(), "snapshot.db"), 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	at := time.Date(2026, 8, 28, 2, 20, 0, 0, time.UTC)
	coverage := Coverage{Profile: "primary", AccountID: "111111111111", Region: testRegion, Service: "ec2-sg", Status: CoverageSucceeded}
	if _, err := store.CommitRun(ctx, RunInput{StartedAt: at, CompletedAt: at.Add(time.Second), Coverage: []Coverage{coverage, coverage}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("duplicate coverage error=%v", err)
	}
}

func TestCommitRunDoesNotMixDistinctRelationEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, err := Open(ctx, filepath.Join(t.TempDir(), "snapshot.db"), 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	at := time.Date(2026, 8, 28, 2, 25, 0, 0, time.UTC)
	source := ref("111111111111", testRegion, "ec2.security-group", "sg-source")
	target := ref("111111111111", testRegion, "ec2.security-group", "sg-target")
	first := relation(source, target, awsbrowser.RelationReferences, "rule-id=sgr-1", at)
	second := first
	second.Profile = "secondary"
	second.Reason = "second exact fixture id"
	second.ObservedAt = at.Add(time.Second)
	if _, err := store.CommitRun(ctx, RunInput{
		StartedAt: at, CompletedAt: at.Add(2 * time.Second),
		Relations: []Relation{first, second},
		Coverage:  []Coverage{{Profile: "primary", AccountID: source.AccountID, Region: source.Region, Service: "ec2-sg", Status: CoverageSucceeded}},
	}); err != nil {
		t.Fatal(err)
	}
	edges, err := store.Reverse(ctx, target, 10)
	if err != nil || len(edges) != 2 {
		t.Fatalf("edges=%#v error=%v", edges, err)
	}
	if edges[0].Relation.Reason == edges[1].Relation.Reason {
		t.Fatalf("distinct evidence collapsed: %#v", edges)
	}
}

func TestOpenReadOnlyDoesNotCreateOrRecoverSnapshotState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	directory := t.TempDir()
	missing := filepath.Join(directory, "missing.db")
	if _, err := OpenReadOnly(ctx, missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing snapshot error = %v", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only open created missing snapshot: %v", err)
	}

	corrupt := filepath.Join(directory, "corrupt.db")
	want := []byte("not a sqlite database")
	if err := os.WriteFile(corrupt, want, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReadOnly(ctx, corrupt); err == nil {
		t.Fatal("corrupt read-only snapshot unexpectedly opened")
	}
	got, err := os.ReadFile(corrupt)
	if err != nil || string(got) != string(want) {
		t.Fatalf("corrupt snapshot changed: bytes=%q error=%v", got, err)
	}
	matches, err := filepath.Glob(corrupt + ".corrupt-*")
	if err != nil || len(matches) != 0 {
		t.Fatalf("read-only open quarantined snapshot: matches=%v error=%v", matches, err)
	}
}

func TestOpenReadOnlyLeavesHealthySnapshotBytesAndDirectoryUnchanged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	directory := t.TempDir()
	path := filepath.Join(directory, "snapshot.db")
	store, _, err := Open(ctx, path, 2)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 28, 2, 10, 0, 0, time.UTC)
	if _, err := store.CommitRun(ctx, RunInput{
		StartedAt: at, CompletedAt: at.Add(time.Second),
		Coverage: []Coverage{{Profile: "primary", AccountID: "111111111111", Region: testRegion, Service: "ec2-sg", Status: CoverageSucceeded}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeEntries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ActiveRun(ctx); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	afterEntries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) || afterInfo.Mode() != beforeInfo.Mode() || !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Fatalf("healthy read-only snapshot changed")
	}
	if len(afterEntries) != len(beforeEntries) {
		t.Fatalf("directory entries changed: before=%v after=%v", beforeEntries, afterEntries)
	}
	for index := range beforeEntries {
		if beforeEntries[index].Name() != afterEntries[index].Name() {
			t.Fatalf("directory entries changed: before=%v after=%v", beforeEntries, afterEntries)
		}
	}
}

func TestOpenReadOnlyReadsResidualWALWithoutCreatingSidecarsOrChangingPersistentData(t *testing.T) {
	ctx := context.Background()
	sourceDirectory := t.TempDir()
	sourcePath := filepath.Join(sourceDirectory, "snapshot.db")
	store, _, err := Open(ctx, sourcePath, 2)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 28, 2, 12, 0, 0, time.UTC)
	want, err := store.CommitRun(ctx, RunInput{
		StartedAt: at, CompletedAt: at.Add(time.Second),
		Coverage: []Coverage{{Profile: "primary", AccountID: "111111111111", Region: testRegion, Service: "ec2-sg", Status: CoverageSucceeded}},
	})
	if err != nil {
		t.Fatal(err)
	}
	crashDirectory := t.TempDir()
	crashPath := filepath.Join(crashDirectory, "snapshot.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		bytes, readErr := os.ReadFile(sourcePath + suffix)
		if readErr != nil {
			t.Fatalf("read source sidecar %q: %v", suffix, readErr)
		}
		if writeErr := os.WriteFile(crashPath+suffix, bytes, 0o600); writeErr != nil {
			t.Fatalf("copy source sidecar %q: %v", suffix, writeErr)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before := make(map[string][]byte, 2)
	beforeModTime := make(map[string]time.Time, 2)
	for _, suffix := range []string{"", "-wal"} {
		before[suffix], err = os.ReadFile(crashPath + suffix)
		if err != nil {
			t.Fatal(err)
		}
		info, statErr := os.Stat(crashPath + suffix)
		if statErr != nil {
			t.Fatal(statErr)
		}
		beforeModTime[suffix] = info.ModTime()
	}
	beforeEntries, err := os.ReadDir(crashDirectory)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnly(ctx, crashPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reader.ActiveRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID {
		t.Fatalf("active run=%q, want residual WAL run %q", got.ID, want.ID)
	}
	for _, suffix := range []string{"", "-wal"} {
		after, readErr := os.ReadFile(crashPath + suffix)
		if readErr != nil {
			t.Fatal(readErr)
		}
		info, statErr := os.Stat(crashPath + suffix)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if !bytes.Equal(before[suffix], after) || !beforeModTime[suffix].Equal(info.ModTime()) {
			t.Fatalf("read-only WAL open changed persistent snapshot data %q", suffix)
		}
	}
	afterEntries, err := os.ReadDir(crashDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(beforeEntries) != len(afterEntries) {
		t.Fatalf("read-only WAL open changed directory entries: before=%v after=%v", beforeEntries, afterEntries)
	}

	withoutSHM := filepath.Join(t.TempDir(), "snapshot.db")
	for _, suffix := range []string{"", "-wal"} {
		bytes, readErr := os.ReadFile(crashPath + suffix)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if writeErr := os.WriteFile(withoutSHM+suffix, bytes, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if _, err := OpenReadOnly(ctx, withoutSHM); err == nil {
		t.Fatal("residual WAL without SHM unexpectedly opened")
	}
	if _, err := os.Stat(withoutSHM + "-shm"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only open created SHM: %v", err)
	}
}

func TestCommitRunKeepsLatestCanonicalAndObserverTimestamps(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, err := Open(ctx, filepath.Join(t.TempDir(), "snapshot.db"), 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	at := time.Date(2026, 8, 28, 2, 14, 0, 0, time.UTC)
	source := ref("111111111111", testRegion, "ec2.security-group", "sg-source")
	target := ref("111111111111", testRegion, "ec2.security-group", "sg-target")
	newer := relation(source, target, awsbrowser.RelationReferences, "rule-id=sgr-1", at.Add(time.Minute))
	newer.Profile = "profile-newer"
	older := newer
	older.Profile = "profile-older"
	older.ObservedAt = at
	if _, err := store.CommitRun(ctx, RunInput{
		StartedAt: at, CompletedAt: at.Add(2 * time.Minute), Relations: []Relation{newer, older},
		Coverage: []Coverage{{Profile: "profile-newer", AccountID: source.AccountID, Region: source.Region, Service: "ec2-sg", Status: CoverageSucceeded}},
	}); err != nil {
		t.Fatal(err)
	}
	edges, err := store.Reverse(ctx, target, 10)
	if err != nil || len(edges) != 1 || len(edges[0].Observers) != 2 {
		t.Fatalf("edges=%#v error=%v", edges, err)
	}
	if !edges[0].Relation.ObservedAt.Equal(newer.ObservedAt) {
		t.Fatalf("canonical observed_at=%s, want %s", edges[0].Relation.ObservedAt, newer.ObservedAt)
	}
}

func TestActiveViewReadsCapturedRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "snapshot.db")
	store, _, err := Open(ctx, path, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	at := time.Date(2026, 8, 28, 2, 30, 0, 0, time.UTC)
	source := ref("111111111111", testRegion, "ec2.instance", "i-pinned")
	target := ref("111111111111", testRegion, "ec2.security-group", "sg-pinned")
	first, err := store.CommitRun(ctx, RunInput{
		StartedAt: at, CompletedAt: at.Add(time.Second),
		Resources:    []Resource{{Ref: source}, {Ref: target}},
		Observations: []Observation{{Resource: target, Profile: "primary", AccountID: target.AccountID, Region: target.Region, ObservedAt: at}},
		Relations:    []Relation{relation(source, target, awsbrowser.RelationUses, "network-interface=eni-pinned", at)},
		Coverage:     []Coverage{{Profile: "primary", AccountID: target.AccountID, Region: target.Region, Service: "ec2-sg", Status: CoverageSucceeded}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	view, err := reader.ActiveView(ctx)
	if err != nil || view.Run().ID != first.ID {
		t.Fatalf("view run=%#v error=%v", view.Run(), err)
	}
	edges, err := view.Reverse(ctx, target, 10)
	if err != nil || len(edges) != 1 || edges[0].RunID != first.ID {
		t.Fatalf("pinned edges=%#v error=%v", edges, err)
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := view.Reverse(ctx, target, 10); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("closed view error=%v", err)
	}
}

func TestCoordinatorBoundsCollectionConcurrency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, err := Open(ctx, filepath.Join(t.TempDir(), "snapshot.db"), 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	collector := &concurrencyCollector{release: make(chan struct{}), started: make(chan struct{}, 3)}
	done := make(chan error, 1)
	go func() {
		_, syncErr := (Coordinator{Store: store, Collector: collector, Concurrency: 2}).Sync(ctx, []Scope{
			{Profile: "one", Region: testRegion, Service: "ec2-sg"},
			{Profile: "two", Region: testRegion, Service: "ec2-sg"},
			{Profile: "three", Region: testRegion, Service: "ec2-sg"},
		})
		done <- syncErr
	}()
	<-collector.started
	<-collector.started
	if active := collector.active.Load(); active != 2 {
		t.Fatalf("active collectors = %d", active)
	}
	close(collector.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if maximum := collector.maximum.Load(); maximum != 2 {
		t.Fatalf("maximum collectors = %d", maximum)
	}
}

func TestCancelledSyncDoesNotReplaceActiveRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, err := Open(ctx, filepath.Join(t.TempDir(), "snapshot.db"), 3)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	at := time.Date(2026, 8, 28, 3, 0, 0, 0, time.UTC)
	initial, err := store.CommitRun(ctx, RunInput{
		StartedAt: at, CompletedAt: at.Add(time.Second),
		Coverage: []Coverage{{Profile: "primary", AccountID: "111111111111", Region: testRegion, Service: "ec2", Status: CoverageSucceeded}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = (Coordinator{Store: store, Collector: fixtureCollector{}, Now: (&stepClock{next: at}).Now}).Sync(cancelled, []Scope{{Profile: "primary", AccountID: "111111111111", Region: testRegion, Service: "ec2"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled sync error = %v", err)
	}
	active, err := store.ActiveRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != initial.ID {
		t.Fatalf("cancelled sync changed active run: %s -> %s", initial.ID, active.ID)
	}
}

type fixtureCollector struct {
	results map[string]Collection
	errors  map[string]error
}

func (collector fixtureCollector) Collect(_ context.Context, scope Scope) (Collection, error) {
	key := scope.Profile + "/" + scope.Region
	if err := collector.errors[key]; err != nil {
		return Collection{}, err
	}
	return collector.results[key], nil
}

type stepClock struct {
	next time.Time
}

type concurrencyCollector struct {
	release chan struct{}
	started chan struct{}
	active  atomic.Int32
	maximum atomic.Int32
}

func (collector *concurrencyCollector) Collect(ctx context.Context, _ Scope) (Collection, error) {
	active := collector.active.Add(1)
	defer collector.active.Add(-1)
	for {
		maximum := collector.maximum.Load()
		if active <= maximum || collector.maximum.CompareAndSwap(maximum, active) {
			break
		}
	}
	collector.started <- struct{}{}
	select {
	case <-collector.release:
		return Collection{AccountID: "111111111111"}, nil
	case <-ctx.Done():
		return Collection{}, ctx.Err()
	}
}

func (clock *stepClock) Now() time.Time {
	result := clock.next
	clock.next = clock.next.Add(time.Millisecond)
	return result
}

func ref(account, region, resourceType, id string) ResourceRef {
	return ResourceRef{Partition: "aws", AccountID: account, Region: region, Type: resourceType, ID: id}
}

func relation(source, target ResourceRef, relationType awsbrowser.RelationType, condition string, at time.Time) Relation {
	return Relation{
		Source: source, Target: target, Type: relationType, Direction: awsbrowser.RelationOutgoing,
		Confidence: awsbrowser.RelationAPIExact, Condition: condition, Reason: "fixture exact id",
		Operation: awsbrowser.OperationDescribeSecurityGroupRules, Scope: source.Region, ObservedAt: at,
		Profile: "fixture", AccountID: source.AccountID, Region: source.Region,
	}
}

func mustKey(t *testing.T, value ResourceRef) string {
	t.Helper()
	key, err := value.Key()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func BenchmarkSnapshotGraph100kNodes500kEdges(b *testing.B) {
	ctx := context.Background()
	store, _, err := Open(ctx, filepath.Join(b.TempDir(), "snapshot.db"), 1)
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()

	const nodeCount = 100_000
	const edgeCount = 500_000
	at := time.Date(2026, 8, 28, 4, 0, 0, 0, time.UTC)
	resources := make([]Resource, nodeCount)
	observations := make([]Observation, nodeCount)
	for index := range nodeCount {
		resourceRef := ref("111111111111", testRegion, "ec2.security-group", fmt.Sprintf("sg-%06d", index))
		resources[index] = Resource{Ref: resourceRef, Name: fmt.Sprintf("sg-%06d", index)}
		observations[index] = Observation{Resource: resourceRef, Profile: "benchmark", AccountID: "111111111111", Region: testRegion, ObservedAt: at}
	}
	relations := make([]Relation, edgeCount)
	for index := range edgeCount {
		sourceIndex := index % nodeCount
		targetIndex := (sourceIndex + 1 + (index/nodeCount)*997) % nodeCount
		relations[index] = relation(resources[sourceIndex].Ref, resources[targetIndex].Ref, awsbrowser.RelationReferences, fmt.Sprintf("edge-%d", index/nodeCount), at)
	}
	writeStarted := time.Now()
	if _, err := store.CommitRun(ctx, RunInput{
		StartedAt: at, CompletedAt: at.Add(time.Second), Resources: resources, Observations: observations, Relations: relations,
		Coverage: []Coverage{{Profile: "benchmark", AccountID: "111111111111", Region: testRegion, Service: "ec2", Status: CoverageSucceeded}},
	}); err != nil {
		b.Fatal(err)
	}
	writeDuration := time.Since(writeStarted)
	databaseInfo, err := os.Stat(store.path)
	if err != nil {
		b.Fatal(err)
	}

	type query struct {
		name string
		fn   func() error
	}
	queries := []query{
		{name: "relation", fn: func() error { _, err := store.Outgoing(ctx, resources[50_000].Ref, 100); return err }},
		{name: "reverse", fn: func() error { _, err := store.Reverse(ctx, resources[50_001].Ref, 100); return err }},
		{name: "path", fn: func() error {
			_, err := store.FindPath(ctx, resources[50_000].Ref, resources[50_003].Ref, 4)
			return err
		}},
	}
	for _, item := range queries {
		b.Run(item.name, func(b *testing.B) {
			const samples = 101
			durations := make([]time.Duration, samples)
			for index := range samples {
				started := time.Now()
				if err := item.fn(); err != nil {
					b.Fatal(err)
				}
				durations[index] = time.Since(started)
			}
			sortDurations(durations)
			p95 := durations[(samples*95+99)/100-1]
			b.ReportMetric(float64(p95.Microseconds()), "p95_us")
			b.ReportMetric(writeDuration.Seconds(), "sync_s")
			b.ReportMetric(float64(databaseInfo.Size())/(1024*1024), "store_MiB")
		})
	}
}

func sortDurations(values []time.Duration) {
	for index := 1; index < len(values); index++ {
		for current := index; current > 0 && values[current] < values[current-1]; current-- {
			values[current], values[current-1] = values[current-1], values[current]
		}
	}
}
