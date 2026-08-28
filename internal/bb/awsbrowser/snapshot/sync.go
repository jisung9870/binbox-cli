package snapshot

import (
	"context"
	"errors"
	"strings"
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
	if !validText(scope.Profile, 256) || !accountPattern.MatchString(scope.AccountID) ||
		!validRegion(scope.Region) || !validText(scope.Service, 128) ||
		!validOptionalText(scope.NotObservedReason, 256) || scope.NotObserved != (scope.NotObservedReason != "") {
		return ErrInvalidInput
	}
	return nil
}

type Collection struct {
	Resources []Resource
	Relations []Relation
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
	Store     *Store
	Collector Collector
	Now       func() time.Time
}

// Sync is the only collection entry point. Nothing in this package starts a
// timer, goroutine, daemon, or background refresh.
func (coordinator Coordinator) Sync(ctx context.Context, scopes []Scope) (Run, error) {
	if coordinator.Store == nil || coordinator.Collector == nil || len(scopes) == 0 {
		return Run{}, ErrInvalidInput
	}
	for _, scope := range scopes {
		if err := scope.validate(); err != nil {
			return Run{}, err
		}
	}
	now := coordinator.Now
	if now == nil {
		now = time.Now
	}
	startedAt := now().UTC()
	input := RunInput{StartedAt: startedAt}
	for _, scope := range scopes {
		if err := ctx.Err(); err != nil {
			return Run{}, err
		}
		if scope.NotObserved {
			input.Coverage = append(input.Coverage, Coverage{
				Profile: scope.Profile, AccountID: scope.AccountID, Region: scope.Region, Service: scope.Service,
				Status: CoverageNotObserved, ErrorKind: scope.NotObservedReason,
			})
			continue
		}
		collection, err := coordinator.Collector.Collect(ctx, scope)
		coverage := Coverage{
			Profile:   scope.Profile,
			AccountID: scope.AccountID,
			Region:    scope.Region,
			Service:   scope.Service,
			Status:    CoverageSucceeded,
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return Run{}, ctxErr
			}
			coverage.Status = CoverageFailed
			coverage.ErrorKind = collectionErrorKind(err)
			input.Coverage = append(input.Coverage, coverage)
			continue
		}
		input.Coverage = append(input.Coverage, coverage)
		input.Resources = append(input.Resources, collection.Resources...)
		input.Relations = append(input.Relations, collection.Relations...)
		observedAt := now().UTC()
		for _, resource := range collection.Resources {
			input.Observations = append(input.Observations, Observation{
				Resource:   resource.Ref,
				Profile:    scope.Profile,
				AccountID:  scope.AccountID,
				Region:     scope.Region,
				ObservedAt: observedAt,
			})
		}
	}
	input.CompletedAt = now().UTC()
	return coordinator.Store.CommitRun(ctx, input)
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
