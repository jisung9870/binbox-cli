package snapshot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
			"primary/us-east-1":      {},
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
	if _, err := store.db.ExecContext(ctx, `PRAGMA user_version = 2`); err != nil {
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
