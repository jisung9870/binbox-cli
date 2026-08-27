package awsbrowser

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidQueryResult = errors.New("invalid query result")
	ErrQueryDecode        = errors.New("query response decode failed")
)

type ProviderErrorKind string

const (
	ProviderForbidden      ProviderErrorKind = "forbidden"
	ProviderAuthRequired   ProviderErrorKind = "auth_required"
	ProviderThrottled      ProviderErrorKind = "throttled"
	ProviderTimedOut       ProviderErrorKind = "timed_out"
	ProviderCancelled      ProviderErrorKind = "cancelled"
	ProviderUnsupported    ProviderErrorKind = "unsupported"
	ProviderDecode         ProviderErrorKind = "decode"
	ProviderIncomplete     ProviderErrorKind = "incomplete"
	ProviderContextChanged ProviderErrorKind = "context_changed"
	ProviderUnknown        ProviderErrorKind = "unknown"
)

var safeProviderMetadataRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]*$`)

// ProviderError deliberately has no raw message, payload, SDK error, client,
// or credential field.
type ProviderError struct {
	Kind      ProviderErrorKind
	Service   string
	Operation string
	Code      string
	RequestID string
}

func NewProviderError(kind ProviderErrorKind, service, operation, code, requestID string) *ProviderError {
	return &ProviderError{
		Kind: normalizeProviderKind(kind), Service: safeProviderMetadata(service, 64),
		Operation: safeProviderMetadata(operation, 128), Code: safeProviderMetadata(code, 128),
		RequestID: safeProviderMetadata(requestID, 256),
	}
}

func normalizeProviderKind(kind ProviderErrorKind) ProviderErrorKind {
	switch kind {
	case ProviderForbidden, ProviderAuthRequired, ProviderThrottled, ProviderTimedOut, ProviderCancelled,
		ProviderUnsupported, ProviderDecode, ProviderIncomplete, ProviderContextChanged, ProviderUnknown:
		return kind
	default:
		return ProviderUnknown
	}
}

func (*ProviderError) Error() string { return "AWS provider query failed" }

func safeProviderMetadata(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > limit || !safeProviderMetadataRE.MatchString(value) {
		return ""
	}
	return value
}

// QueryPageSink is coordinator-owned. Providers may emit only complete,
// fully mapped pages and must call Complete exactly once after the final page.
type QueryPageSink interface {
	Page(QueryPage) error
	Complete(time.Time) error
}

// QueryExecutor receives no credentials or raw SDK clients.
type QueryExecutor interface {
	Execute(context.Context, QueryKey, QueryPageSink) error
}

// ProviderFailure is sanitized metadata safe for models, views, and logs.
type ProviderFailure struct {
	State        LoadState
	Kind         ProviderErrorKind
	Service      string
	Operation    string
	Code         string
	RequestID    string
	PartialPages uint64
}

type QueryUpdate struct {
	Key      QueryKey
	Snapshot QuerySnapshot
	Failure  *ProviderFailure
}

func (update QueryUpdate) Partial() bool {
	return update.Failure != nil && update.Failure.PartialPages != 0
}

type querySubscriber struct {
	updates chan QueryUpdate
	closed  bool
}

type queryFlight struct {
	key          QueryKey
	ctx          context.Context
	cancel       context.CancelFunc
	subscribers  map[uint64]*querySubscriber
	retired      bool
	refresh      bool
	beganStore   bool
	partialPages uint64
}

type querySink struct {
	coordinator *QueryCoordinator
	flight      *queryFlight
	terminal    int
	fetchedAt   time.Time
	protocolErr error
}

type QuerySubscription struct {
	updates     <-chan QueryUpdate
	unsubscribe func()
	once        sync.Once
}

func (subscription *QuerySubscription) Updates() <-chan QueryUpdate {
	if subscription == nil {
		return nil
	}
	return subscription.updates
}

func (subscription *QuerySubscription) Unsubscribe() {
	if subscription != nil {
		subscription.once.Do(subscription.unsubscribe)
	}
}

type QueryCoordinator struct {
	mu       sync.Mutex
	store    *SessionStore
	executor QueryExecutor
	registry *ContextRegistry
	limit    chan struct{}
	flights  map[QueryKey]*queryFlight
	latest   map[queryLineage]uint64
	failures map[QueryKey]ProviderFailure
	nextID   uint64
}

type queryLineage struct {
	mode      ContextMode
	profile   string
	region    string
	provider  string
	operation string
	params    string
}

// At most one optional registry may be supplied.
func NewQueryCoordinator(store *SessionStore, executor QueryExecutor, maxConcurrent int, registries ...*ContextRegistry) (*QueryCoordinator, error) {
	if store == nil || executor == nil || maxConcurrent < 1 || len(registries) > 1 {
		return nil, errors.New("query coordinator requires a store, executor, and positive concurrency limit")
	}
	var registry *ContextRegistry
	if len(registries) == 1 {
		registry = registries[0]
	}
	return &QueryCoordinator{
		store: store, executor: executor, registry: registry, limit: make(chan struct{}, maxConcurrent),
		flights: make(map[QueryKey]*queryFlight), latest: make(map[queryLineage]uint64),
		failures: make(map[QueryKey]ProviderFailure),
	}, nil
}

func (coordinator *QueryCoordinator) Subscribe(key QueryKey) (*QuerySubscription, error) {
	return coordinator.subscribe(key, false)
}

func (coordinator *QueryCoordinator) Refresh(key QueryKey) (*QuerySubscription, error) {
	return coordinator.subscribe(key, true)
}

func (coordinator *QueryCoordinator) subscribe(key QueryKey, refresh bool) (*QuerySubscription, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	if !refresh {
		if snapshot, ok := coordinator.store.Snapshot(key); ok && cacheTerminal(snapshot.State) {
			coordinator.mu.Lock()
			failure, failed := coordinator.failures[key]
			coordinator.mu.Unlock()
			update := QueryUpdate{Key: key, Snapshot: snapshot}
			if failed {
				update.Failure = &failure
			}
			return immediateSubscription(update), nil
		}
	}

	coordinator.mu.Lock()
	if flight := coordinator.flights[key]; flight != nil && !flight.retired {
		subscription := coordinator.addSubscriberLocked(flight)
		coordinator.mu.Unlock()
		return subscription, nil
	}

	lineage := lineageFor(key)
	if generation := coordinator.latest[lineage]; key.Context.CredentialGen > generation {
		coordinator.latest[lineage] = key.Context.CredentialGen
		coordinator.store.InvalidateGeneration(key.Context)
		for oldKey, oldFlight := range coordinator.flights {
			if lineageFor(oldKey) == lineage && oldKey.Context.CredentialGen < key.Context.CredentialGen {
				coordinator.retireLocked(oldFlight)
				delete(coordinator.flights, oldKey)
				oldFlight.cancel()
			}
		}
		for oldKey := range coordinator.failures {
			if lineageFor(oldKey) == lineage && oldKey.Context.CredentialGen < key.Context.CredentialGen {
				delete(coordinator.failures, oldKey)
			}
		}
	} else if generation > key.Context.CredentialGen {
		coordinator.mu.Unlock()
		return immediateSubscription(QueryUpdate{Key: key, Snapshot: QuerySnapshot{State: LoadCancelled}}), nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	flight := &queryFlight{key: key, ctx: ctx, cancel: cancel, refresh: refresh, subscribers: make(map[uint64]*querySubscriber)}
	coordinator.flights[key] = flight
	subscription := coordinator.addSubscriberLocked(flight)
	coordinator.publishLocked(flight, QueryUpdate{Key: key, Snapshot: QuerySnapshot{State: LoadQueued}})
	coordinator.mu.Unlock()
	go coordinator.run(flight)
	return subscription, nil
}

func immediateSubscription(update QueryUpdate) *QuerySubscription {
	updates := make(chan QueryUpdate, 1)
	updates <- update
	close(updates)
	return &QuerySubscription{updates: updates, unsubscribe: func() {}}
}

func (coordinator *QueryCoordinator) addSubscriberLocked(flight *queryFlight) *QuerySubscription {
	coordinator.nextID++
	id := coordinator.nextID
	subscriber := &querySubscriber{updates: make(chan QueryUpdate, 1)}
	flight.subscribers[id] = subscriber
	return &QuerySubscription{updates: subscriber.updates, unsubscribe: func() { coordinator.unsubscribe(flight, id) }}
}

func (coordinator *QueryCoordinator) unsubscribe(flight *queryFlight, id uint64) {
	coordinator.mu.Lock()
	subscriber := flight.subscribers[id]
	if subscriber == nil {
		coordinator.mu.Unlock()
		return
	}
	delete(flight.subscribers, id)
	coordinator.closeSubscriberLocked(subscriber)
	last := len(flight.subscribers) == 0 && !flight.retired
	if last {
		// Remove shared visibility before cancellation begins.
		flight.retired = true
		if coordinator.flights[flight.key] == flight {
			delete(coordinator.flights, flight.key)
		}
		if flight.beganStore {
			_ = coordinator.store.FailQuery(flight.key, LoadCancelled, time.Now().UTC())
			failure, _ := sanitizedProviderFailure(context.Canceled, flight.key, flight.partialPages)
			coordinator.failures[flight.key] = failure
		}
	}
	coordinator.mu.Unlock()
	if last {
		flight.cancel()
	}
}

func (coordinator *QueryCoordinator) run(flight *queryFlight) {
	select {
	case coordinator.limit <- struct{}{}:
	case <-flight.ctx.Done():
		coordinator.finish(flight, QueryUpdate{Key: flight.key, Snapshot: QuerySnapshot{State: LoadCancelled}})
		return
	}
	defer func() { <-coordinator.limit }()

	coordinator.mu.Lock()
	canStart := coordinator.canCommitLocked(flight)
	if canStart && flight.refresh {
		if snapshot, ok := coordinator.store.Snapshot(flight.key); ok &&
			(snapshot.State == LoadReady || snapshot.State == LoadEmpty || snapshot.State == LoadStale) {
			if coordinator.store.BeginRefresh(flight.key) == nil {
				flight.beganStore = true
			}
		}
	}
	if canStart {
		state := LoadLoading
		if flight.beganStore && flight.refresh {
			state = LoadRefreshing
		}
		coordinator.publishLocked(flight, QueryUpdate{Key: flight.key, Snapshot: coordinator.snapshotOr(flight.key, state)})
	}
	coordinator.mu.Unlock()
	if !canStart {
		coordinator.finish(flight, QueryUpdate{Key: flight.key, Snapshot: QuerySnapshot{State: LoadCancelled}})
		return
	}

	sink := &querySink{coordinator: coordinator, flight: flight}
	err := coordinator.executor.Execute(flight.ctx, flight.key, sink)
	coordinator.mu.Lock()
	protocolErr, terminal, fetchedAt := sink.protocolErr, sink.terminal, sink.fetchedAt
	coordinator.mu.Unlock()
	if err != nil {
		coordinator.fail(flight, err)
		return
	}
	if protocolErr != nil || terminal != 1 {
		coordinator.fail(flight, ErrInvalidQueryResult)
		return
	}
	if err := coordinator.complete(flight, fetchedAt); err != nil {
		coordinator.fail(flight, err)
		return
	}
	snapshot, _ := coordinator.store.Snapshot(flight.key)
	coordinator.finish(flight, QueryUpdate{Key: flight.key, Snapshot: snapshot})
}

func (sink *querySink) Page(page QueryPage) error {
	coordinator := sink.coordinator
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if sink.protocolErr != nil {
		return sink.protocolErr
	}
	if sink.terminal != 0 || !coordinator.canCommitLocked(sink.flight) {
		sink.protocolErr = ErrInvalidQueryResult
		return sink.protocolErr
	}
	if !page.Complete || page.FetchedAt.IsZero() {
		sink.protocolErr = ErrIncompletePage
		return sink.protocolErr
	}
	for _, resource := range page.resources {
		if validateObservedResource(sink.flight.key.Context, resource) != nil {
			sink.protocolErr = ErrIncompletePage
			return sink.protocolErr
		}
	}
	if !sink.flight.beganStore {
		if err := coordinator.store.BeginLoad(sink.flight.key); err != nil {
			sink.protocolErr = err
			return err
		}
		sink.flight.beganStore = true
	}
	if err := coordinator.store.CommitPage(sink.flight.key, page); err != nil {
		sink.protocolErr = err
		return err
	}
	sink.flight.partialPages++
	coordinator.publishLocked(sink.flight, QueryUpdate{Key: sink.flight.key, Snapshot: coordinator.snapshotOr(sink.flight.key, LoadLoading)})
	return nil
}

func (sink *querySink) Complete(fetchedAt time.Time) error {
	sink.coordinator.mu.Lock()
	defer sink.coordinator.mu.Unlock()
	if sink.protocolErr != nil {
		return sink.protocolErr
	}
	if sink.terminal != 0 || fetchedAt.IsZero() || !sink.coordinator.canCommitLocked(sink.flight) {
		sink.protocolErr = ErrInvalidQueryResult
		return sink.protocolErr
	}
	sink.terminal = 1
	sink.fetchedAt = fetchedAt.UTC()
	return nil
}

func (coordinator *QueryCoordinator) complete(flight *queryFlight, fetchedAt time.Time) error {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if !coordinator.canCommitLocked(flight) {
		return context.Canceled
	}
	if !flight.beganStore {
		if err := coordinator.store.BeginLoad(flight.key); err != nil {
			return err
		}
		flight.beganStore = true
	}
	if err := coordinator.store.CompleteQuery(flight.key, fetchedAt); err != nil {
		return err
	}
	delete(coordinator.failures, flight.key)
	return nil
}

func (coordinator *QueryCoordinator) fail(flight *queryFlight, err error) {
	failure, cache := sanitizedProviderFailure(err, flight.key, flight.partialPages)
	coordinator.mu.Lock()
	if errors.Is(err, ErrContextChanged) {
		if coordinator.registry != nil {
			coordinator.registry.Invalidate(contextSpecFor(flight.key.Context), ErrContextChanged)
		}
		coordinator.store.InvalidateContext(flight.key.Context)
		delete(coordinator.failures, flight.key)
		flight.beganStore = false
	} else if coordinator.canCommitLocked(flight) {
		if flight.beganStore {
			_ = coordinator.store.FailQuery(flight.key, failure.State, time.Now().UTC())
		} else if cache && coordinator.store.Queue(flight.key) == nil {
			_ = coordinator.store.FailQuery(flight.key, failure.State, time.Now().UTC())
		}
		if flight.beganStore || cache {
			coordinator.failures[flight.key] = failure
		}
	}
	snapshot := coordinator.snapshotOr(flight.key, failure.State)
	coordinator.mu.Unlock()
	coordinator.finish(flight, QueryUpdate{Key: flight.key, Snapshot: snapshot, Failure: &failure})
}

func sanitizedProviderFailure(err error, key QueryKey, partialPages uint64) (ProviderFailure, bool) {
	failure := ProviderFailure{
		State: LoadUnknown, Kind: ProviderUnknown, Service: safeProviderMetadata(key.Provider, 64),
		Operation: safeProviderMetadata(key.Operation, 128), PartialPages: partialPages,
	}
	cache := false
	var providerError *ProviderError
	switch {
	case errors.Is(err, ErrContextChanged):
		failure.Kind, failure.PartialPages, cache = ProviderContextChanged, 0, false
	case errors.Is(err, context.Canceled):
		failure.State, failure.Kind, cache = LoadCancelled, ProviderCancelled, false
	case errors.Is(err, context.DeadlineExceeded):
		failure.State, failure.Kind, cache = LoadTimedOut, ProviderTimedOut, false
	case errors.Is(err, ErrQueryDecode):
		failure.Kind, cache = ProviderDecode, false
	case errors.Is(err, ErrInvalidQueryResult), errors.Is(err, ErrIncompletePage), errors.Is(err, ErrIncompleteObservation):
		failure.Kind, cache = ProviderIncomplete, false
	case errors.As(err, &providerError):
		clean := NewProviderError(providerError.Kind, providerError.Service, providerError.Operation, providerError.Code, providerError.RequestID)
		failure.Kind, failure.Service, failure.Operation = clean.Kind, clean.Service, clean.Operation
		failure.Code, failure.RequestID = clean.Code, clean.RequestID
		failure.State = loadStateForProviderKind(clean.Kind)
		cache = clean.Kind == ProviderForbidden || clean.Kind == ProviderAuthRequired || clean.Kind == ProviderUnsupported
	}
	return failure, cache
}

func loadStateForProviderKind(kind ProviderErrorKind) LoadState {
	switch kind {
	case ProviderForbidden:
		return LoadForbidden
	case ProviderAuthRequired:
		return LoadAuthRequired
	case ProviderThrottled:
		return LoadThrottled
	case ProviderTimedOut:
		return LoadTimedOut
	case ProviderCancelled:
		return LoadCancelled
	case ProviderUnsupported:
		return LoadUnsupported
	default:
		return LoadUnknown
	}
}

func contextSpecFor(awsContext AWSContext) ContextSpec {
	return ContextSpec{Mode: awsContext.Mode, Profile: awsContext.Profile, Region: awsContext.Region}
}

func (coordinator *QueryCoordinator) canCommitLocked(flight *queryFlight) bool {
	return !flight.retired && len(flight.subscribers) != 0 && coordinator.flights[flight.key] == flight &&
		coordinator.latest[lineageFor(flight.key)] <= flight.key.Context.CredentialGen
}

func (coordinator *QueryCoordinator) finish(flight *queryFlight, update QueryUpdate) {
	coordinator.mu.Lock()
	if !flight.retired {
		flight.retired = true
		if coordinator.flights[flight.key] == flight {
			delete(coordinator.flights, flight.key)
		}
		coordinator.publishLocked(flight, update)
	}
	for _, subscriber := range flight.subscribers {
		coordinator.closeSubscriberLocked(subscriber)
	}
	flight.subscribers = nil
	coordinator.mu.Unlock()
	flight.cancel()
}

func (coordinator *QueryCoordinator) publishLocked(flight *queryFlight, update QueryUpdate) {
	for _, subscriber := range flight.subscribers {
		if subscriber.closed {
			continue
		}
		select {
		case <-subscriber.updates:
		default:
		}
		copy := update
		if update.Failure != nil {
			failure := *update.Failure
			copy.Failure = &failure
		}
		subscriber.updates <- copy
	}
}

func (coordinator *QueryCoordinator) closeSubscriberLocked(subscriber *querySubscriber) {
	if !subscriber.closed {
		subscriber.closed = true
		close(subscriber.updates)
	}
}

func (coordinator *QueryCoordinator) retireLocked(flight *queryFlight) {
	flight.retired = true
	for _, subscriber := range flight.subscribers {
		coordinator.closeSubscriberLocked(subscriber)
	}
	flight.subscribers = nil
}

func (coordinator *QueryCoordinator) snapshotOr(key QueryKey, fallback LoadState) QuerySnapshot {
	if snapshot, ok := coordinator.store.Snapshot(key); ok {
		return snapshot
	}
	return QuerySnapshot{State: fallback}
}

func cacheTerminal(state LoadState) bool {
	switch state {
	case LoadReady, LoadEmpty, LoadStale, LoadForbidden, LoadAuthRequired, LoadUnsupported:
		return true
	default:
		return false
	}
}

func lineageFor(key QueryKey) queryLineage {
	return queryLineage{mode: key.Context.Mode, profile: key.Context.Profile, region: key.Context.Region,
		provider: key.Provider, operation: key.Operation, params: key.ParamsKey}
}
