package awsbrowser

import (
	"errors"
	"sort"
	"strings"
	"time"
)

var (
	ErrInvalidRelationEvidence  = errors.New("invalid relation evidence")
	ErrInvalidRelationSemantics = errors.New("invalid relation semantics")
)

// RelationType describes what an edge means. It is deliberately separate from
// RelationKind, which describes how confident the observation is.
type RelationType string

const (
	RelationAttachedTo     RelationType = "attached-to"
	RelationAssociatedWith RelationType = "associated-with"
	RelationContains       RelationType = "contains"
	RelationHasVersion     RelationType = "has-version"
	RelationMemberOf       RelationType = "member-of"
	RelationReferences     RelationType = "references"
	RelationUses           RelationType = "uses"
	RelationAliasTo        RelationType = "alias-to"
	RelationRoutesTo       RelationType = "routes-to"
)

func (relationType RelationType) valid() bool {
	switch relationType {
	case RelationAttachedTo, RelationAssociatedWith, RelationContains, RelationHasVersion,
		RelationMemberOf, RelationReferences, RelationUses, RelationAliasTo, RelationRoutesTo:
		return true
	default:
		return false
	}
}

type RelationDirection string

const (
	RelationOutgoing RelationDirection = "outgoing"
	RelationIncoming RelationDirection = "incoming"
)

// RelationSemantics is the stable, provider-independent meaning of an edge.
// Condition distinguishes edges that share a source and target, such as
// separate CloudFront path patterns routed to the same S3 bucket.
type RelationSemantics struct {
	Type      RelationType
	Direction RelationDirection
	Condition string
}

func NewRelationSemantics(relationType RelationType, direction RelationDirection, condition string) (RelationSemantics, error) {
	semantics := RelationSemantics{Type: relationType, Direction: direction, Condition: strings.TrimSpace(condition)}
	if err := semantics.Validate(); err != nil {
		return RelationSemantics{}, err
	}
	return semantics, nil
}

func (semantics RelationSemantics) Validate() error {
	if !semantics.Type.valid() || semantics.Direction != RelationOutgoing && semantics.Direction != RelationIncoming {
		return ErrInvalidRelationSemantics
	}
	return nil
}

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
	Scope      string
	ObservedAt time.Time
}

func NewRelationEvidence(kind RelationKind, reason, operation, scope string, observedAt time.Time) (RelationEvidence, error) {
	evidence := RelationEvidence{
		Kind:       kind,
		Reason:     strings.TrimSpace(reason),
		Operation:  strings.TrimSpace(operation),
		Scope:      strings.TrimSpace(scope),
		ObservedAt: observedAt.UTC(),
	}
	if err := evidence.Validate(); err != nil {
		return RelationEvidence{}, err
	}
	return evidence, nil
}

func (evidence RelationEvidence) Validate() error {
	if !evidence.Kind.valid() || !validIdentifier(evidence.Reason) || !validIdentifier(evidence.Operation) ||
		(evidence.Scope != GlobalRegion && !regionNameRE.MatchString(evidence.Scope)) || evidence.ObservedAt.IsZero() {
		return ErrInvalidRelationEvidence
	}
	return nil
}

// Relation is an immutable canonical edge. Evidence returns a copied,
// observation-time-ordered slice suitable for model and coordinator fakes.
type Relation struct {
	Source    ResourceKey
	Target    ResourceKey
	Semantics RelationSemantics

	evidence []RelationEvidence
}

func NewRelation(source, target ResourceKey, semantics RelationSemantics, evidence ...RelationEvidence) (Relation, error) {
	if err := semantics.Validate(); err != nil {
		return Relation{}, err
	}
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
	return Relation{Source: source, Target: target, Semantics: semantics, evidence: copy}, nil
}

func (relation Relation) Evidence() []RelationEvidence {
	return append([]RelationEvidence(nil), relation.evidence...)
}
