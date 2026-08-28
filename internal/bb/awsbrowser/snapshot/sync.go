package snapshot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

type Scope struct {
	Profile           string
	AccountID         string
	Region            string
	Service           string
	NotObserved       bool
	NotObservedReason string
}

func (scope Scope) validate() error {
	if !validText(scope.Profile, 256) || scope.AccountID != "" && !accountPattern.MatchString(scope.AccountID) ||
		!validRegion(scope.Region) || !validText(scope.Service, 128) ||
		!validOptionalText(scope.NotObservedReason, 256) || scope.NotObserved != (scope.NotObservedReason != "") {
		return ErrInvalidInput
	}
	return nil
}

type Collection struct {
	AccountID string
	Resources []Resource
	Relations []Relation
	Coverage  []Coverage
}

type Collector interface {
	Collect(context.Context, Scope) (Collection, error)
}

type CollectionError struct {
	Kind string
	Err  error
}

func (err *CollectionError) Error() string {
	if err == nil || err.Err == nil {
		return "collection failed"
	}
	return err.Err.Error()
}

func (err *CollectionError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

type Coordinator struct {
	Store       *Store
	Collector   Collector
	Now         func() time.Time
	Concurrency int
}

// Sync is the only collection entry point. Nothing in this package starts a
// timer, goroutine, daemon, or background refresh.
func (coordinator Coordinator) Sync(ctx context.Context, scopes []Scope) (Run, error) {
	if coordinator.Store == nil {
		return Run{}, ErrInvalidInput
	}
	input, err := coordinator.Collect(ctx, scopes)
	if err != nil {
		return Run{}, err
	}
	return coordinator.Store.CommitRun(ctx, input)
}

// Collect performs bounded external reads without opening a database
// transaction. Callers may keep the previous active run readable and acquire
// the snapshot write lock only for CommitRun.
func (coordinator Coordinator) Collect(ctx context.Context, scopes []Scope) (RunInput, error) {
	if coordinator.Collector == nil || len(scopes) == 0 {
		return RunInput{}, ErrInvalidInput
	}
	seenScopes := make(map[Scope]struct{}, len(scopes))
	for _, scope := range scopes {
		if err := scope.validate(); err != nil {
			return RunInput{}, err
		}
		if _, exists := seenScopes[scope]; exists {
			return RunInput{}, ErrInvalidInput
		}
		seenScopes[scope] = struct{}{}
	}
	now := coordinator.Now
	if now == nil {
		now = time.Now
	}
	startedAt := now().UTC()
	input := RunInput{StartedAt: startedAt}
	type collectionResult struct {
		collection Collection
		err        error
	}
	results := make([]collectionResult, len(scopes))
	concurrency := coordinator.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	semaphore := make(chan struct{}, concurrency)
	var wait sync.WaitGroup
	for index, scope := range scopes {
		if scope.NotObserved {
			continue
		}
		wait.Add(1)
		go func(index int, scope Scope) {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results[index].err = ctx.Err()
				return
			}
			results[index].collection, results[index].err = coordinator.Collector.Collect(ctx, scope)
		}(index, scope)
	}
	wait.Wait()
	if err := ctx.Err(); err != nil {
		return RunInput{}, err
	}
	for index, scope := range scopes {
		if scope.NotObserved {
			input.Coverage = append(input.Coverage, Coverage{
				Profile: scope.Profile, AccountID: scope.AccountID, Region: scope.Region, Service: scope.Service,
				Status: CoverageNotObserved, ErrorKind: scope.NotObservedReason,
			})
			continue
		}
		collection, err := results[index].collection, results[index].err
		coverage := Coverage{
			Profile:   scope.Profile,
			AccountID: scope.AccountID,
			Region:    scope.Region,
			Service:   scope.Service,
			Status:    CoverageSucceeded,
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return RunInput{}, ctxErr
			}
			if collection.AccountID != "" {
				if !accountPattern.MatchString(collection.AccountID) || scope.AccountID != "" && scope.AccountID != collection.AccountID {
					return RunInput{}, ErrInvalidInput
				}
				coverage.AccountID = collection.AccountID
			}
			coverage.Status = CoverageFailed
			coverage.ErrorKind = collectionErrorKind(err)
			input.Coverage = append(input.Coverage, coverage)
			continue
		}
		if !accountPattern.MatchString(collection.AccountID) || scope.AccountID != "" && scope.AccountID != collection.AccountID {
			return RunInput{}, ErrInvalidInput
		}
		coverage.AccountID = collection.AccountID
		input.Coverage = append(input.Coverage, coverage)
		for _, supplemental := range collection.Coverage {
			if supplemental.Profile != scope.Profile || supplemental.Service == scope.Service || supplemental.validate() != nil {
				return RunInput{}, ErrInvalidInput
			}
		}
		input.Coverage = append(input.Coverage, collection.Coverage...)
		input.Resources = append(input.Resources, collection.Resources...)
		observedAt := now().UTC()
		for _, relation := range collection.Relations {
			relation.Profile = scope.Profile
			relation.AccountID = collection.AccountID
			relation.Region = scope.Region
			input.Relations = append(input.Relations, relation)
		}
		for _, resource := range collection.Resources {
			input.Observations = append(input.Observations, Observation{
				Resource:   resource.Ref,
				Profile:    scope.Profile,
				AccountID:  collection.AccountID,
				Region:     scope.Region,
				ObservedAt: observedAt,
			})
		}
	}
	coverageSeen := make(map[string]Coverage, len(input.Coverage))
	coverage := make([]Coverage, 0, len(input.Coverage))
	for _, item := range input.Coverage {
		key := strings.Join([]string{item.Profile, item.AccountID, item.Region, item.Service}, "\x00")
		if previous, exists := coverageSeen[key]; exists {
			if previous != item {
				return RunInput{}, ErrInvalidInput
			}
			continue
		}
		coverageSeen[key] = item
		coverage = append(coverage, item)
	}
	input.Coverage = coverage
	input.CompletedAt = now().UTC()
	return input, nil
}

func collectionErrorKind(err error) string {
	var collectionError *CollectionError
	if errors.As(err, &collectionError) && validOptionalText(strings.TrimSpace(collectionError.Kind), 256) {
		if kind := strings.TrimSpace(collectionError.Kind); kind != "" {
			return kind
		}
	}
	return "unknown"
}
