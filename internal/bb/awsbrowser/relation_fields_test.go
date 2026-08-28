package awsbrowser

import (
	"errors"
	"testing"
	"time"
)

func TestRelationsFromMappedFieldsPreservesExactEdge(t *testing.T) {
	context := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	source, _ := NewRegionalResourceKey(context, "ec2.security-group", "sg-source")
	target, _ := NewCanonicalResourceKey("aws", "210987654321", "us-east-1", "ec2.security-group", "sg-target")
	at := time.Date(2026, 8, 28, 5, 0, 0, 0, time.UTC)
	fields := map[string]any{"relations": []any{map[string]any{
		"source": source, "target": target, "relation_type": "references", "direction": "outgoing",
		"condition": "rule=sgr-1&protocol=tcp", "kind": "id-exact", "reason": "security group rule referenced group id",
		"operation": OperationDescribeSecurityGroupRules, "scope": "us-east-1", "observed_at": at,
	}}}
	relations, err := RelationsFromMappedFields(fields)
	if err != nil || len(relations) != 1 {
		t.Fatalf("relations=%#v error=%v", relations, err)
	}
	if relations[0].Source != source || relations[0].Target != target || relations[0].Semantics.Condition != "rule=sgr-1&protocol=tcp" {
		t.Fatalf("relation=%#v", relations[0])
	}
}

func TestRelationsFromMappedFieldsRejectsMalformedProjection(t *testing.T) {
	_, err := RelationsFromMappedFields(map[string]any{"relations": []any{map[string]any{"target": "sg-unsafe"}}})
	if !errors.Is(err, ErrInvalidMappedFields) {
		t.Fatalf("error=%v", err)
	}
}
