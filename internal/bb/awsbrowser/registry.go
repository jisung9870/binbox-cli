package awsbrowser

import (
	"context"
	"errors"
	"sync"
)

// ContextRegistryView is a credential-free description of a memoized runtime.
// Looking at a registry never resolves credentials or performs network I/O.
type ContextRegistryView struct {
	Resolved bool
	Identity VerifiedIdentity
}

type contextRegistryEntry struct {
	runtime    RuntimeContext
	generation uint64
	resolving  bool
	done       chan struct{}
}

// ContextRegistry lazily resolves and memoizes one runtime per ContextSpec.
// Failed and invalidated resolutions are never retained.
type ContextRegistry struct {
	mu      sync.Mutex
	factory RuntimeFactory
	entries map[ContextSpec]*contextRegistryEntry
}

func NewContextRegistry(factory RuntimeFactory) *ContextRegistry {
	return &ContextRegistry{factory: factory, entries: make(map[ContextSpec]*contextRegistryEntry)}
}

// View returns metadata already held by the registry. It never calls Resolve.
func (registry *ContextRegistry) View(spec ContextSpec) ContextRegistryView {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	entry := registry.entries[spec]
	if entry == nil || entry.runtime == nil {
		return ContextRegistryView{}
	}
	return ContextRegistryView{Resolved: true, Identity: entry.runtime.Identity()}
}

// Resolve coalesces concurrent resolutions. If a memoized runtime reports a
// different credential generation, it is discarded and resolved again.
func (registry *ContextRegistry) Resolve(ctx context.Context, spec ContextSpec) (RuntimeContext, error) {
	if registry == nil || registry.factory == nil {
		return nil, errors.New("AWS runtime factory is required")
	}
	if _, err := validateContextSpec(spec); err != nil {
		return nil, err
	}

	for {
		registry.mu.Lock()
		entry := registry.entries[spec]
		if entry != nil && entry.runtime != nil {
			identity := entry.runtime.Identity()
			if identity.CredentialGeneration == entry.generation {
				runtime := entry.runtime
				registry.mu.Unlock()
				return runtime, nil
			}
			delete(registry.entries, spec)
			entry = nil
		}
		if entry != nil && entry.resolving {
			done := entry.done
			registry.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-done:
				continue
			}
		}

		entry = &contextRegistryEntry{resolving: true, done: make(chan struct{})}
		registry.entries[spec] = entry
		registry.mu.Unlock()

		runtime, err := registry.factory.Resolve(ctx, spec)
		if err == nil && runtime == nil {
			err = errors.New("AWS runtime factory returned no runtime")
		}

		registry.mu.Lock()
		current := registry.entries[spec]
		if current == entry {
			if err == nil {
				entry.runtime = runtime
				entry.generation = runtime.Identity().CredentialGeneration
				entry.resolving = false
			} else {
				delete(registry.entries, spec)
			}
			close(entry.done)
		}
		registry.mu.Unlock()
		return runtime, err
	}
}

// Invalidate removes a memoized runtime only for ErrContextChanged. It returns
// whether a resolved runtime was removed.
func (registry *ContextRegistry) Invalidate(spec ContextSpec, cause error) bool {
	if registry == nil || !errors.Is(cause, ErrContextChanged) {
		return false
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	entry := registry.entries[spec]
	if entry == nil || entry.runtime == nil {
		return false
	}
	delete(registry.entries, spec)
	return true
}

// InvalidateGeneration removes a runtime when its verified generation differs
// from currentGeneration.
func (registry *ContextRegistry) InvalidateGeneration(spec ContextSpec, currentGeneration uint64) bool {
	if registry == nil || currentGeneration == 0 {
		return false
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	entry := registry.entries[spec]
	if entry == nil || entry.runtime == nil || entry.generation == currentGeneration {
		return false
	}
	delete(registry.entries, spec)
	return true
}
