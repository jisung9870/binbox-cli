package awsbrowser

import (
	"context"
	"sync"
	"testing"
	"time"
)

type queryExecutorFunc func(context.Context, QueryKey, QueryPageSink) error

func (function queryExecutorFunc) Execute(ctx context.Context, key QueryKey, sink QueryPageSink) error {
	return function(ctx, key, sink)
}

func TestQueryCoordinatorIsLazyAndDeduplicatesSubscribers(t *testing.T) {
	key := testCoordinatorKey(t, 1)
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	executor := queryExecutorFunc(func(ctx context.Context, got QueryKey, sink QueryPageSink) error {
		mu.Lock()
		calls++
		mu.Unlock()
		close(started)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return emitSuccessfulQuery(t, sink, got, "i-one")
		}
	})
	coordinator, err := NewQueryCoordinator(NewSessionStore(), executor, 1)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("provider called before subscribe: %d", calls)
	}

	first, _ := coordinator.Subscribe(key)
	second, _ := coordinator.Subscribe(key)
	waitClosed(t, started)
	first.Unsubscribe()
	close(release)
	if update := terminalUpdate(t, second.Updates()); update.Snapshot.State != LoadReady {
		t.Fatalf("terminal state=%s", update.Snapshot.State)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("two subscribers caused %d provider calls", calls)
	}
}

func TestLastUnsubscribeCancelsAndQueuedWorkCannotStart(t *testing.T) {
	firstKey := testCoordinatorKeyWithParam(t, 1, "first")
	queuedKey := testCoordinatorKeyWithParam(t, 1, "queued")
	firstStarted := make(chan struct{})
	firstCancelled := make(chan struct{})
	var mu sync.Mutex
	calls := make(map[QueryKey]int)
	executor := queryExecutorFunc(func(ctx context.Context, key QueryKey, sink QueryPageSink) error {
		mu.Lock()
		calls[key]++
		mu.Unlock()
		if key == firstKey {
			close(firstStarted)
			<-ctx.Done()
			close(firstCancelled)
			return ctx.Err()
		}
		return emitSuccessfulQuery(t, sink, key, "i-unexpected")
	})
	store := NewSessionStore()
	coordinator, _ := NewQueryCoordinator(store, executor, 1)
	first, _ := coordinator.Subscribe(firstKey)
	waitClosed(t, firstStarted)
	queued, _ := coordinator.Subscribe(queuedKey)
	queued.Unsubscribe()
	first.Unsubscribe()
	waitClosed(t, firstCancelled)
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if calls[firstKey] != 1 || calls[queuedKey] != 0 {
		t.Fatalf("provider calls=%v", calls)
	}
	if _, ok := store.Snapshot(queuedKey); ok {
		t.Fatal("queued cancelled query entered store")
	}
}

func TestCompletionAndUnsubscribeRaceIsSafe(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		key := testCoordinatorKeyWithParam(t, 1, time.Now().Format("150405.000000000"))
		release := make(chan struct{})
		executor := queryExecutorFunc(func(_ context.Context, got QueryKey, sink QueryPageSink) error {
			<-release
			return emitSuccessfulQuery(t, sink, got, "i-race")
		})
		coordinator, _ := NewQueryCoordinator(NewSessionStore(), executor, 1)
		subscription, _ := coordinator.Subscribe(key)
		var wait sync.WaitGroup
		wait.Add(2)
		go func() { defer wait.Done(); subscription.Unsubscribe() }()
		go func() { defer wait.Done(); close(release) }()
		wait.Wait()
		for range subscription.Updates() {
		}
	}
}

func TestOldGenerationResultCannotCommit(t *testing.T) {
	oldKey := testCoordinatorKey(t, 1)
	newKey := testCoordinatorKey(t, 2)
	oldStarted := make(chan struct{})
	oldFinished := make(chan struct{})
	releaseOld := make(chan struct{})
	executor := queryExecutorFunc(func(_ context.Context, key QueryKey, sink QueryPageSink) error {
		if key == oldKey {
			defer close(oldFinished)
			close(oldStarted)
			<-releaseOld // Deliberately ignores cancellation.
			_ = emitSuccessfulQuery(t, sink, key, "i-old")
			return nil
		}
		return emitSuccessfulQuery(t, sink, key, "i-new")
	})
	store := NewSessionStore()
	coordinator, _ := NewQueryCoordinator(store, executor, 2)
	oldSubscription, _ := coordinator.Subscribe(oldKey)
	waitClosed(t, oldStarted)
	newSubscription, _ := coordinator.Subscribe(newKey)
	if update := terminalUpdate(t, newSubscription.Updates()); update.Snapshot.State != LoadReady {
		t.Fatalf("new generation state=%s", update.Snapshot.State)
	}
	close(releaseOld)
	waitClosed(t, oldFinished)
	for range oldSubscription.Updates() {
	}
	if _, ok := store.Snapshot(oldKey); ok {
		t.Fatal("old generation result committed")
	}
}

func TestSecondPageFailureOrCancellationRetainsFirstPage(t *testing.T) {
	for _, test := range []struct {
		name  string
		err   error
		state LoadState
		kind  ProviderErrorKind
	}{
		{name: "failure", err: NewProviderError(ProviderForbidden, "ec2", "DescribeInstances", "AccessDenied", "req-1"), state: LoadForbidden, kind: ProviderForbidden},
		{name: "cancel", err: context.Canceled, state: LoadCancelled, kind: ProviderCancelled},
	} {
		t.Run(test.name, func(t *testing.T) {
			key := testCoordinatorKeyWithParam(t, 1, test.name)
			store := NewSessionStore()
			executor := queryExecutorFunc(func(_ context.Context, got QueryKey, sink QueryPageSink) error {
				if err := sink.Page(successfulQueryPage(t, got, "i-first", 0, true)); err != nil {
					return err
				}
				// A current page that has not become a complete mapped value is
				// rejected and cannot disturb the earlier complete page.
				if err := sink.Page(successfulQueryPage(t, got, "i-failing", 1, false)); err == nil {
					return ErrInvalidQueryResult
				}
				return test.err
			})
			coordinator, _ := NewQueryCoordinator(store, executor, 1)
			subscription, _ := coordinator.Subscribe(key)
			update := terminalUpdate(t, subscription.Updates())
			if update.Snapshot.State != test.state || update.Snapshot.ResourceCount() != 1 ||
				update.Failure == nil || update.Failure.Kind != test.kind || !update.Partial() {
				t.Fatalf("unexpected partial failure: %+v", update)
			}
			resources := update.Snapshot.Pages()[0].Resources()
			if len(resources) != 1 || resources[0].Key.ID != "i-first" {
				t.Fatalf("failing page entered cache: %+v", resources)
			}
		})
	}
}

func TestMissingAndDoubleTerminalCompletionAreRejected(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(QueryPageSink)
	}{
		{name: "missing", run: func(QueryPageSink) {}},
		{name: "double", run: func(sink QueryPageSink) {
			_ = sink.Complete(time.Now().UTC())
			_ = sink.Complete(time.Now().UTC())
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			key := testCoordinatorKeyWithParam(t, 1, test.name)
			executor := queryExecutorFunc(func(_ context.Context, got QueryKey, sink QueryPageSink) error {
				if err := sink.Page(successfulQueryPage(t, got, "i-first", 0, true)); err != nil {
					return err
				}
				test.run(sink)
				return nil
			})
			coordinator, _ := NewQueryCoordinator(NewSessionStore(), executor, 1)
			subscription, _ := coordinator.Subscribe(key)
			update := terminalUpdate(t, subscription.Updates())
			if update.Snapshot.State != LoadUnknown || update.Snapshot.ResourceCount() != 1 ||
				update.Failure == nil || update.Failure.Kind != ProviderIncomplete || !update.Partial() {
				t.Fatalf("invalid terminal contract accepted: %+v", update)
			}
		})
	}
}

func TestProviderFailureMetadataIsTypedAndSanitized(t *testing.T) {
	key := testCoordinatorKey(t, 1)
	executor := queryExecutorFunc(func(context.Context, QueryKey, QueryPageSink) error {
		return &ProviderError{
			Kind: ProviderForbidden, Service: "ec2", Operation: "DescribeInstances",
			Code: "AccessDenied\x1b[31m secret", RequestID: "req-123",
		}
	})
	coordinator, _ := NewQueryCoordinator(NewSessionStore(), executor, 1)
	subscription, _ := coordinator.Subscribe(key)
	update := terminalUpdate(t, subscription.Updates())
	if update.Failure == nil || update.Failure.State != LoadForbidden || update.Failure.Kind != ProviderForbidden ||
		update.Failure.Service != "ec2" || update.Failure.Operation != "DescribeInstances" ||
		update.Failure.Code != "" || update.Failure.RequestID != "req-123" {
		t.Fatalf("unsafe typed metadata: %+v", update.Failure)
	}
	second, _ := coordinator.Subscribe(key)
	cached := terminalUpdate(t, second.Updates())
	if cached.Failure == nil || cached.Failure.Code != "" || cached.Failure.RequestID != "req-123" {
		t.Fatalf("cached failure metadata was lost or unsafe: %+v", cached.Failure)
	}
	if (&ProviderError{}).Error() != "AWS provider query failed" {
		t.Fatal("provider error rendered raw metadata")
	}
}

func TestContextChangedInvalidatesRegistry(t *testing.T) {
	key := testCoordinatorKey(t, 1)
	spec := contextSpecFor(key.Context)
	runtime := &registryRuntimeFake{identity: testRegistryIdentity(1)}
	factory := &registryFactoryFake{runtime: runtime}
	registry := NewContextRegistry(factory)
	if _, err := registry.Resolve(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	executor := queryExecutorFunc(func(context.Context, QueryKey, QueryPageSink) error { return ErrContextChanged })
	coordinator, _ := NewQueryCoordinator(NewSessionStore(), executor, 1, registry)
	subscription, _ := coordinator.Subscribe(key)
	update := terminalUpdate(t, subscription.Updates())
	if update.Failure == nil || update.Failure.Kind != ProviderContextChanged || registry.View(spec).Resolved {
		t.Fatalf("context change was not invalidated: update=%+v view=%+v", update, registry.View(spec))
	}
	if _, err := registry.Resolve(context.Background(), spec); err != nil || factory.callCount() != 2 {
		t.Fatalf("registry was not re-resolved: calls=%d err=%v", factory.callCount(), err)
	}
}

func TestContextChangedInvalidatesCommittedPagesWithoutPartialClaim(t *testing.T) {
	key := testCoordinatorKey(t, 1)
	store := NewSessionStore()
	executor := queryExecutorFunc(func(_ context.Context, got QueryKey, sink QueryPageSink) error {
		if err := sink.Page(successfulQueryPage(t, got, "i-first", 0, true)); err != nil {
			return err
		}
		return ErrContextChanged
	})
	coordinator, _ := NewQueryCoordinator(store, executor, 1)
	subscription, _ := coordinator.Subscribe(key)
	update := terminalUpdate(t, subscription.Updates())
	if update.Failure == nil || update.Failure.Kind != ProviderContextChanged || update.Partial() ||
		update.Snapshot.ResourceCount() != 0 {
		t.Fatalf("context-change update=%+v", update)
	}
	if _, ok := store.Snapshot(key); ok {
		t.Fatal("context-changed page remained in cache")
	}
}

func TestNoPageFailureDoesNotEnterCache(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, ErrQueryDecode} {
		key := testCoordinatorKeyWithParam(t, 1, err.Error())
		store := NewSessionStore()
		executor := queryExecutorFunc(func(context.Context, QueryKey, QueryPageSink) error { return err })
		coordinator, _ := NewQueryCoordinator(store, executor, 1)
		subscription, _ := coordinator.Subscribe(key)
		_ = terminalUpdate(t, subscription.Updates())
		if snapshot, ok := store.Snapshot(key); ok {
			t.Fatalf("no-page failure cached: %+v", snapshot)
		}
	}
}

func TestOnlyStableNegativeFailuresAreReplayedFromCache(t *testing.T) {
	for _, test := range []struct {
		name   string
		kind   ProviderErrorKind
		cached bool
	}{
		{name: "forbidden", kind: ProviderForbidden, cached: true},
		{name: "auth-required", kind: ProviderAuthRequired, cached: true},
		{name: "unsupported", kind: ProviderUnsupported, cached: true},
		{name: "throttled", kind: ProviderThrottled},
		{name: "unknown", kind: ProviderUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			key := testCoordinatorKeyWithParam(t, 1, test.name)
			var mu sync.Mutex
			calls := 0
			executor := queryExecutorFunc(func(context.Context, QueryKey, QueryPageSink) error {
				mu.Lock()
				calls++
				mu.Unlock()
				return NewProviderError(test.kind, "ec2", "DescribeInstances", "SafeCode", "req-1")
			})
			coordinator, _ := NewQueryCoordinator(NewSessionStore(), executor, 1)
			first, _ := coordinator.Subscribe(key)
			_ = terminalUpdate(t, first.Updates())
			second, _ := coordinator.Subscribe(key)
			_ = terminalUpdate(t, second.Updates())
			mu.Lock()
			defer mu.Unlock()
			wantCalls := 2
			if test.cached {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("provider calls=%d, want %d", calls, wantCalls)
			}
		})
	}
}

func TestRefreshPagesReplaceAtomicallyOnlyAfterCompletion(t *testing.T) {
	key := testCoordinatorKey(t, 1)
	store := NewSessionStore()
	installCompletedPage(t, store, key, "i-old")
	newPageEmitted := make(chan struct{})
	release := make(chan struct{})
	executor := queryExecutorFunc(func(_ context.Context, got QueryKey, sink QueryPageSink) error {
		if err := sink.Page(successfulQueryPage(t, got, "i-new", 0, true)); err != nil {
			return err
		}
		close(newPageEmitted)
		<-release
		return sink.Complete(time.Now().UTC())
	})
	coordinator, _ := NewQueryCoordinator(store, executor, 1)
	subscription, _ := coordinator.Refresh(key)
	waitClosed(t, newPageEmitted)
	snapshot, _ := store.Snapshot(key)
	if snapshot.Pages()[0].Resources()[0].Key.ID != "i-old" || snapshot.State != LoadRefreshing {
		t.Fatalf("refresh page became visible early: %+v", snapshot)
	}
	close(release)
	update := terminalUpdate(t, subscription.Updates())
	if update.Snapshot.State != LoadReady || update.Snapshot.Pages()[0].Resources()[0].Key.ID != "i-new" {
		t.Fatalf("refresh did not atomically replace: %+v", update.Snapshot)
	}
}

func emitSuccessfulQuery(t *testing.T, sink QueryPageSink, key QueryKey, resourceID string) error {
	t.Helper()
	if err := sink.Page(successfulQueryPage(t, key, resourceID, 0, true)); err != nil {
		return err
	}
	return sink.Complete(time.Now().UTC())
}

func successfulQueryPage(t *testing.T, key QueryKey, resourceID string, number uint64, complete bool) QueryPage {
	t.Helper()
	when := time.Now().UTC()
	resourceKey, err := NewRegionalResourceKey(key.Context, "instance", resourceID)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := NewResourceObservation(key.Context, map[string]any{"name": resourceID}, when, true)
	if err != nil {
		t.Fatal(err)
	}
	page, err := NewQueryPage(number, []ObservedResource{{Key: resourceKey, Observation: observation}}, when, complete)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func installCompletedPage(t *testing.T, store *SessionStore, key QueryKey, resourceID string) {
	t.Helper()
	page := successfulQueryPage(t, key, resourceID, 0, true)
	if err := store.BeginLoad(key); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitPage(key, page); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteQuery(key, page.FetchedAt); err != nil {
		t.Fatal(err)
	}
}

func testCoordinatorKey(t *testing.T, generation uint64) QueryKey {
	t.Helper()
	return testCoordinatorKeyWithParam(t, generation, "all")
}

func testCoordinatorKeyWithParam(t *testing.T, generation uint64, value string) QueryKey {
	t.Helper()
	awsContext := testStoreContext(t, "dev", "123456789012", "us-east-1", generation)
	key, err := NewQueryKey(awsContext, "ec2", "DescribeInstances", map[string]string{"scope": value})
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func terminalUpdate(t *testing.T, updates <-chan QueryUpdate) QueryUpdate {
	t.Helper()
	var last QueryUpdate
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case update, ok := <-updates:
			if !ok {
				return last
			}
			last = update
		case <-timer.C:
			t.Fatal("timed out waiting for terminal query update")
		}
	}
}

func waitClosed(t *testing.T, channel <-chan struct{}) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for signal")
	}
}
