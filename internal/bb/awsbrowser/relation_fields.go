package awsbrowser

import "time"

// RelationsFromMappedFields decodes the strict provider relation projection
// shared by the live TUI and snapshot collector. A malformed relation is a
// provider-boundary error; callers must not silently drop it before sync.
func RelationsFromMappedFields(fields map[string]any) ([]Relation, error) {
	if fields == nil {
		return nil, nil
	}
	values := make([]any, 0)
	if raw, exists := fields["relations"]; exists {
		relations, ok := raw.([]any)
		if !ok {
			return nil, ErrInvalidMappedFields
		}
		values = append(values, relations...)
	}
	for _, name := range []string{"alias_relation", "zone_relation"} {
		if raw, exists := fields[name]; exists {
			relation, ok := raw.(map[string]any)
			if !ok {
				return nil, ErrInvalidMappedFields
			}
			values = append(values, relation)
		}
	}
	result := make([]Relation, 0, len(values))
	for _, raw := range values {
		mapped, ok := raw.(map[string]any)
		if !ok {
			return nil, ErrInvalidMappedFields
		}
		source, sourceOK := mapped["source"].(ResourceKey)
		target, targetOK := mapped["target"].(ResourceKey)
		relationType, typeOK := mapped["relation_type"].(string)
		direction, directionOK := mapped["direction"].(string)
		condition, conditionOK := mapped["condition"].(string)
		kind, kindOK := mapped["kind"].(string)
		reason, reasonOK := mapped["reason"].(string)
		operation, operationOK := mapped["operation"].(string)
		scope, scopeOK := mapped["scope"].(string)
		observedAt, observedOK := mapped["observed_at"].(time.Time)
		if !sourceOK || !targetOK || !typeOK || !directionOK || !conditionOK || !kindOK || !reasonOK ||
			!operationOK || !scopeOK || !observedOK {
			return nil, ErrInvalidMappedFields
		}
		semantics, err := NewRelationSemantics(RelationType(relationType), RelationDirection(direction), condition)
		if err != nil {
			return nil, ErrInvalidMappedFields
		}
		evidence, err := NewRelationEvidence(RelationKind(kind), reason, operation, scope, observedAt)
		if err != nil {
			return nil, ErrInvalidMappedFields
		}
		relation, err := NewRelation(source, target, semantics, evidence)
		if err != nil {
			return nil, ErrInvalidMappedFields
		}
		result = append(result, relation)
	}
	return result, nil
}
