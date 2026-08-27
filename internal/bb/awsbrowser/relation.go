package awsbrowser

import (
	"errors"
	"sort"
	"strings"
	"time"
)

var ErrInvalidRelationEvidence = errors.New("invalid relation evidence")

// RelationKind describes confidence without promoting correlation or
// inference to an exact relationship.
type RelationKind string

const (
	RelationIDExact     RelationKind = "id-exact"
	RelationAPIExact    RelationKind = "api-exact"
	RelationCorrelated  RelationKind = "correlated"
	RelationInferred    RelationKind = "inferred"
	RelationAmbiguous   RelationKind = "ambiguous"
	RelationUnsupported RelationKind = "unsupported"
)

func (kind RelationKind) valid() bool {
	switch kind {
	case RelationIDExact, RelationAPIExact, RelationCorrelated, RelationInferred, RelationAmbiguous, RelationUnsupported:
		return true
	default:
		return false
	}
}

// RelationEvidence records why an edge exists, which read operation produced
// it, and when it was observed. It contains no provider payload.
type RelationEvidence struct {
	Kind       RelationKind
	Reason     string
	Operation  string
	ObservedAt time.Time
}

func NewRelationEvidence(kind RelationKind, reason, operation string, observedAt time.Time) (RelationEvidence, error) {
	evidence := RelationEvidence{
		Kind:       kind,
		Reason:     strings.TrimSpace(reason),
		Operation:  strings.TrimSpace(operation),
		ObservedAt: observedAt.UTC(),
	}
	if err := evidence.Validate(); err != nil {
		return RelationEvidence{}, err
	}
	return evidence, nil
}

func (evidence RelationEvidence) Validate() error {
	if !evidence.Kind.valid() || !validIdentifier(evidence.Reason) || !validIdentifier(evidence.Operation) ||
		evidence.ObservedAt.IsZero() {
		return ErrInvalidRelationEvidence
	}
	return nil
}

// Relation is an immutable canonical edge. Evidence returns a copied,
// observation-time-ordered slice suitable for model and coordinator fakes.
type Relation struct {
	Source ResourceKey
	Target ResourceKey

	evidence []RelationEvidence
}

func NewRelation(source, target ResourceKey, evidence ...RelationEvidence) (Relation, error) {
	if source.Validate() != nil || target.Validate() != nil || len(evidence) == 0 {
		return Relation{}, ErrInvalidRelationEvidence
	}
	copy := append([]RelationEvidence(nil), evidence...)
	for _, item := range copy {
		if err := item.Validate(); err != nil {
			return Relation{}, err
		}
	}
	sort.SliceStable(copy, func(left, right int) bool {
		return copy[left].ObservedAt.Before(copy[right].ObservedAt)
	})
	return Relation{Source: source, Target: target, evidence: copy}, nil
}

func (relation Relation) Evidence() []RelationEvidence {
	return append([]RelationEvidence(nil), relation.evidence...)
}
