package awsbrowser

import (
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"
)

var (
	ErrInvalidLoadState      = errors.New("invalid query load state")
	ErrInvalidLoadTransition = errors.New("invalid query load transition")
	ErrObservationScope      = errors.New("resource observation is outside query scope")
)

// LoadState is the typed query state rendered by the model. Only terminal
// negative states may be passed to FailQuery.
type LoadState string

const (
	LoadNotLoaded    LoadState = "not_loaded"
	LoadQueued       LoadState = "queued"
	LoadLoading      LoadState = "loading"
	LoadRefreshing   LoadState = "refreshing"
	LoadReady        LoadState = "ready"
	LoadEmpty        LoadState = "empty"
	LoadStale        LoadState = "stale"
	LoadForbidden    LoadState = "forbidden"
	LoadAuthRequired LoadState = "auth_required"
	LoadThrottled    LoadState = "throttled"
	LoadTimedOut     LoadState = "timed_out"
	LoadCancelled    LoadState = "cancelled"
	LoadUnsupported  LoadState = "unsupported"
	LoadUnknown      LoadState = "unknown"
)

func (state LoadState) isFailure() bool {
	switch state {
	case LoadForbidden, LoadAuthRequired, LoadThrottled, LoadTimedOut, LoadCancelled, LoadUnsupported, LoadUnknown:
		return true
	default:
		return false
	}
}

// RefreshFailure records only a typed state and timestamp. It intentionally
// does not retain raw provider or SDK error text.
type RefreshFailure struct {
	State      LoadState
	ObservedAt time.Time
}

// QuerySnapshot is an immutable view of one cached query. Pages returns a
// fresh deep copy on every call.
type QuerySnapshot struct {
	State          LoadState
	FetchedAt      time.Time
	RefreshFailure *RefreshFailure

	pages []QueryPage
}

func (snapshot QuerySnapshot) Pages() []QueryPage {
	if snapshot.pages == nil {
		return nil
	}
	pages := make([]QueryPage, len(snapshot.pages))
	for index, page := range snapshot.pages {
		pages[index] = page.clone()
	}
	return pages
}

func (snapshot QuerySnapshot) ResourceCount() int {
	count := 0
	for _, page := range snapshot.pages {
		count += len(page.resources)
	}
	return count
}

// CanonicalResource preserves independent observations keyed by their full
// context and operation provenance. Observations returns a deterministic
// copied slice.
type CanonicalResource struct {
	Key ResourceKey

	observations map[ObservationProvenance]ResourceObservation
}

// Observation is the compatibility lookup for a context. If several
// operations exist, it deterministically returns the newest observation,
// breaking timestamp ties by operation name.
func (resource CanonicalResource) Observation(provenance ContextProvenance) (ResourceObservation, bool) {
	var selected ResourceObservation
	found := false
	for identity, observation := range resource.observations {
		if identity.ContextProvenance != provenance {
			continue
		}
		if !found || observation.FetchedAt.After(selected.FetchedAt) ||
			(observation.FetchedAt.Equal(selected.FetchedAt) && observation.Operation < selected.Operation) {
			selected = observation
			found = true
		}
	}
	return selected.clone(), found
}

func (resource CanonicalResource) ObservationForOperation(provenance ObservationProvenance) (ResourceObservation, bool) {
	observation, ok := resource.observations[provenance]
	return observation.clone(), ok
}

func (resource CanonicalResource) ObservationCount() int {
	return len(resource.observations)
}

func (resource CanonicalResource) Observations() []ResourceObservation {
	provenances := make([]ObservationProvenance, 0, len(resource.observations))
	for provenance := range resource.observations {
		provenances = append(provenances, provenance)
	}
	sort.Slice(provenances, func(left, right int) bool {
		return observationProvenanceSortKey(provenances[left]) < observationProvenanceSortKey(provenances[right])
	})
	result := make([]ResourceObservation, len(provenances))
	for index, provenance := range provenances {
		result[index] = resource.observations[provenance].clone()
	}
	return result
}

func observationProvenanceSortKey(provenance ObservationProvenance) string {
	return joinIdentityParts(provenanceSortKey(provenance.ContextProvenance), provenance.Operation)
}

func provenanceSortKey(provenance ContextProvenance) string {
	return joinIdentityParts(
		string(provenance.Mode), provenance.Profile, provenance.Partition,
		provenance.AccountID, provenance.PrincipalARN, provenance.RoleName,
		provenance.Region, strconv.FormatUint(provenance.CredentialGen, 10),
	)
}

type queryEntry struct {
	state          LoadState
	fetchedAt      time.Time
	refreshFailure *RefreshFailure
	pages          map[uint64]QueryPage
	stagedPages    map[uint64]QueryPage
}

// SessionStore is a credential-free, in-memory cache. It stores only complete
// decoded pages and complete mapped observations; there is no persistence.
type SessionStore struct {
	mu        sync.RWMutex
	queries   map[QueryKey]*queryEntry
	resources map[ResourceKey]map[ObservationProvenance]ResourceObservation
}

func NewSessionStore() *SessionStore {
	return &SessionStore{
		queries:   make(map[QueryKey]*queryEntry),
		resources: make(map[ResourceKey]map[ObservationProvenance]ResourceObservation),
	}
}

// Queue records scheduling state without storing a result.
func (store *SessionStore) Queue(key QueryKey) error {
	if err := key.Validate(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	entry := store.entryLocked(key)
	if entry.state == LoadLoading || entry.state == LoadRefreshing {
		return ErrInvalidLoadTransition
	}
	entry.state = LoadQueued
	entry.refreshFailure = nil
	return nil
}

// BeginLoad starts a fresh load and clears any non-complete query attempt.
// Use BeginRefresh when a completed value should remain visible.
func (store *SessionStore) BeginLoad(key QueryKey) error {
	if err := key.Validate(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	entry := store.entryLocked(key)
	if entry.state == LoadLoading || entry.state == LoadRefreshing {
		return ErrInvalidLoadTransition
	}
	entry.state = LoadLoading
	entry.fetchedAt = time.Time{}
	entry.refreshFailure = nil
	entry.pages = make(map[uint64]QueryPage)
	entry.stagedPages = nil
	return nil
}

// BeginRefresh keeps the last completed pages readable while new pages are
// staged. CompleteQuery atomically replaces them; FailQuery marks them stale.
func (store *SessionStore) BeginRefresh(key QueryKey) error {
	if err := key.Validate(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	entry := store.entryLocked(key)
	if entry.state == LoadLoading || entry.state == LoadRefreshing {
		return ErrInvalidLoadTransition
	}
	if entry.fetchedAt.IsZero() || (entry.state != LoadReady && entry.state != LoadEmpty && entry.state != LoadStale) {
		entry.state = LoadLoading
		entry.pages = make(map[uint64]QueryPage)
		entry.fetchedAt = time.Time{}
		entry.stagedPages = nil
	} else {
		entry.state = LoadRefreshing
		entry.stagedPages = make(map[uint64]QueryPage)
	}
	entry.refreshFailure = nil
	return nil
}

// CommitPage accepts only a complete, successful page whose observations are
// complete and scoped to the query's verified context. Refresh pages remain
// staged until CompleteQuery succeeds.
func (store *SessionStore) CommitPage(key QueryKey, page QueryPage) error {
	if err := key.Validate(); err != nil {
		return err
	}
	if !page.Complete || page.FetchedAt.IsZero() {
		return ErrIncompletePage
	}
	for _, resource := range page.resources {
		if err := validateObservedResource(key.Context, resource); err != nil {
			return err
		}
	}
	page = page.clone()

	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.queries[key]
	if !ok || (entry.state != LoadLoading && entry.state != LoadRefreshing) {
		return ErrInvalidLoadTransition
	}
	if entry.state == LoadRefreshing {
		entry.stagedPages[page.Number] = page
		return nil
	}
	entry.pages[page.Number] = page
	for _, resource := range page.resources {
		store.upsertObservationLocked(resource.Key, resource.Observation)
	}
	return nil
}

func validateObservedResource(context AWSContext, resource ObservedResource) error {
	if err := resource.Key.Validate(); err != nil {
		return err
	}
	if err := resource.Observation.validateComplete(); err != nil {
		return err
	}
	if resource.Observation.Context != context || resource.Key.Partition != context.Partition ||
		resource.Key.AccountID != context.AccountID ||
		(resource.Key.Region != GlobalRegion && resource.Key.Region != context.Region) {
		return ErrObservationScope
	}
	return nil
}

// CompleteQuery makes all staged refresh pages and their observations visible
// in one critical section.
func (store *SessionStore) CompleteQuery(key QueryKey, fetchedAt time.Time) error {
	if err := key.Validate(); err != nil {
		return err
	}
	if fetchedAt.IsZero() {
		return ErrInvalidLoadTransition
	}
	fetchedAt = fetchedAt.UTC()
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.queries[key]
	if !ok || (entry.state != LoadLoading && entry.state != LoadRefreshing) {
		return ErrInvalidLoadTransition
	}
	if entry.state == LoadRefreshing {
		entry.pages = entry.stagedPages
		entry.stagedPages = nil
		for _, page := range entry.pages {
			for _, resource := range page.resources {
				store.upsertObservationLocked(resource.Key, resource.Observation)
			}
		}
	}
	entry.fetchedAt = fetchedAt
	entry.refreshFailure = nil
	if queryResourceCount(entry.pages) == 0 {
		entry.state = LoadEmpty
	} else {
		entry.state = LoadReady
	}
	return nil
}

// FailQuery records a sanitized terminal state. A failed refresh discards its
// staged pages and retains the previous result as stale.
func (store *SessionStore) FailQuery(key QueryKey, state LoadState, observedAt time.Time) error {
	if err := key.Validate(); err != nil {
		return err
	}
	if !state.isFailure() || observedAt.IsZero() {
		return ErrInvalidLoadState
	}
	observedAt = observedAt.UTC()
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.queries[key]
	if !ok || (entry.state != LoadQueued && entry.state != LoadLoading && entry.state != LoadRefreshing) {
		return ErrInvalidLoadTransition
	}
	if entry.state == LoadRefreshing {
		entry.state = LoadStale
		entry.stagedPages = nil
		entry.refreshFailure = &RefreshFailure{State: state, ObservedAt: observedAt}
		return nil
	}
	entry.state = state
	entry.stagedPages = nil
	entry.refreshFailure = nil
	return nil
}

func (store *SessionStore) Snapshot(key QueryKey) (QuerySnapshot, bool) {
	if key.Validate() != nil {
		return QuerySnapshot{}, false
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	entry, ok := store.queries[key]
	if !ok {
		return QuerySnapshot{State: LoadNotLoaded}, false
	}
	snapshot := QuerySnapshot{
		State:     entry.state,
		FetchedAt: entry.fetchedAt,
		pages:     sortedPages(entry.pages),
	}
	if entry.refreshFailure != nil {
		failure := *entry.refreshFailure
		snapshot.RefreshFailure = &failure
	}
	return snapshot, true
}

// StoreObservation inserts one completed mapped observation independently of
// a list query, for targeted detail operations.
func (store *SessionStore) StoreObservation(key ResourceKey, observation ResourceObservation) error {
	if err := key.Validate(); err != nil {
		return err
	}
	if err := observation.validateComplete(); err != nil {
		return err
	}
	context := observation.Context
	if key.Partition != context.Partition || key.AccountID != context.AccountID ||
		(key.Region != GlobalRegion && key.Region != context.Region) {
		return ErrObservationScope
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.upsertObservationLocked(key, observation.clone())
	return nil
}

func (store *SessionStore) Canonical(key ResourceKey) (CanonicalResource, bool) {
	if key.Validate() != nil {
		return CanonicalResource{}, false
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	observations, ok := store.resources[key]
	if !ok {
		return CanonicalResource{}, false
	}
	copy := make(map[ObservationProvenance]ResourceObservation, len(observations))
	for provenance, observation := range observations {
		copy[provenance] = observation.clone()
	}
	return CanonicalResource{Key: key, observations: copy}, true
}

// InvalidateContext removes the exact profile/identity/generation scope.
func (store *SessionStore) InvalidateContext(context AWSContext) int {
	if context.Validate() != nil {
		return 0
	}
	provenance, _ := context.Provenance()
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.invalidateLocked(
		func(key QueryKey) bool { return key.Context == context },
		func(candidate ObservationProvenance) bool { return candidate.ContextProvenance == provenance },
		func(ResourceKey) bool { return false },
	)
}

// InvalidateAccount removes every query and canonical observation for an
// account within a partition, across profiles, regions, and generations.
func (store *SessionStore) InvalidateAccount(partition, accountID string) int {
	if !partitionRE.MatchString(partition) || !accountIDRE.MatchString(accountID) {
		return 0
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.invalidateLocked(
		func(key QueryKey) bool {
			return key.Context.Partition == partition && key.Context.AccountID == accountID
		},
		func(candidate ObservationProvenance) bool {
			return candidate.ContextProvenance.Partition == partition && candidate.ContextProvenance.AccountID == accountID
		},
		func(key ResourceKey) bool { return key.Partition == partition && key.AccountID == accountID },
	)
}

// InvalidateGeneration removes older generations of the same profile and
// region while preserving the supplied current generation. Account and
// principal deliberately do not participate in the lineage comparison: an
// identity change must not leave the previous generation cached.
func (store *SessionStore) InvalidateGeneration(current AWSContext) int {
	if current.Validate() != nil {
		return 0
	}
	lineageMatches := func(candidate AWSContext) bool {
		return candidate.Mode == current.Mode && candidate.Profile == current.Profile &&
			candidate.Region == current.Region && candidate.CredentialGen != current.CredentialGen
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.invalidateLocked(
		func(key QueryKey) bool { return lineageMatches(key.Context) },
		func(candidate ObservationProvenance) bool {
			contextProvenance := candidate.ContextProvenance
			return lineageMatches(AWSContext{
				Mode: contextProvenance.Mode, Profile: contextProvenance.Profile, Partition: contextProvenance.Partition,
				AccountID: contextProvenance.AccountID, PrincipalARN: contextProvenance.PrincipalARN,
				RoleName: contextProvenance.RoleName, Region: contextProvenance.Region, CredentialGen: contextProvenance.CredentialGen,
			})
		},
		func(ResourceKey) bool { return false },
	)
}

func (store *SessionStore) entryLocked(key QueryKey) *queryEntry {
	entry := store.queries[key]
	if entry == nil {
		entry = &queryEntry{state: LoadNotLoaded, pages: make(map[uint64]QueryPage)}
		store.queries[key] = entry
	}
	return entry
}

func (store *SessionStore) upsertObservationLocked(key ResourceKey, observation ResourceObservation) {
	provenance, _ := observation.Provenance()
	observations := store.resources[key]
	if observations == nil {
		observations = make(map[ObservationProvenance]ResourceObservation)
		store.resources[key] = observations
	}
	existing, ok := observations[provenance]
	if !ok || !observation.FetchedAt.Before(existing.FetchedAt) {
		observations[provenance] = observation.clone()
	}
}

func (store *SessionStore) invalidateLocked(
	queryMatches func(QueryKey) bool,
	observationMatches func(ObservationProvenance) bool,
	resourceMatches func(ResourceKey) bool,
) int {
	removed := 0
	for key := range store.queries {
		if queryMatches(key) {
			delete(store.queries, key)
			removed++
		}
	}
	for key, observations := range store.resources {
		if resourceMatches(key) {
			removed += len(observations)
			delete(store.resources, key)
			continue
		}
		for provenance := range observations {
			if observationMatches(provenance) {
				delete(observations, provenance)
				removed++
			}
		}
		if len(observations) == 0 {
			delete(store.resources, key)
		}
	}
	return removed
}

func sortedPages(pages map[uint64]QueryPage) []QueryPage {
	numbers := make([]uint64, 0, len(pages))
	for number := range pages {
		numbers = append(numbers, number)
	}
	sort.Slice(numbers, func(left, right int) bool { return numbers[left] < numbers[right] })
	result := make([]QueryPage, len(numbers))
	for index, number := range numbers {
		result[index] = pages[number].clone()
	}
	return result
}

func queryResourceCount(pages map[uint64]QueryPage) int {
	count := 0
	for _, page := range pages {
		count += len(page.resources)
	}
	return count
}
