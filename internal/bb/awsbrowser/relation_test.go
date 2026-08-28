package awsbrowser

import (
	"errors"
	"testing"
	"time"
)

func TestRelationEvidencePreservesKindReasonOperationAndTime(t *testing.T) {
	context := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	source, _ := NewRegionalResourceKey(context, "instance", "i-001")
	target, _ := NewRegionalResourceKey(context, "security-group", "sg-001")
	later := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	earlier := later.Add(-time.Minute)
	correlated, err := NewRelationEvidence(RelationCorrelated, "private DNS value matches", OperationListResourceRecordSets, GlobalRegion, later)
	if err != nil {
		t.Fatal(err)
	}
	exact, err := NewRelationEvidence(RelationIDExact, "group ID returned by instance", OperationDescribeInstances, "us-east-1", earlier)
	if err != nil {
		t.Fatal(err)
	}
	semantics, err := NewRelationSemantics(RelationUses, RelationOutgoing, "primary network interface")
	if err != nil {
		t.Fatal(err)
	}
	relation, err := NewRelation(source, target, semantics, correlated, exact)
	if err != nil {
		t.Fatal(err)
	}
	evidence := relation.Evidence()
	if relation.Semantics != semantics || len(evidence) != 2 || evidence[0] != exact || evidence[1] != correlated ||
		evidence[0].Scope != "us-east-1" || evidence[1].Scope != GlobalRegion {
		t.Fatalf("evidence was not preserved and ordered: %+v", evidence)
	}
	evidence[0].Reason = "caller mutation"
	if relation.Evidence()[0].Reason != exact.Reason {
		t.Fatal("relation exposed mutable evidence storage")
	}
}

func TestRelationSemanticsRejectsUnknownTypeOrDirection(t *testing.T) {
	for _, semantics := range []RelationSemantics{
		{Type: "protected-by", Direction: RelationOutgoing},
		{Type: RelationUses, Direction: "sideways"},
	} {
		if !errors.Is(semantics.Validate(), ErrInvalidRelationSemantics) {
			t.Fatalf("invalid semantics accepted: %+v", semantics)
		}
	}
}

func TestRelationEvidenceRejectsIncompleteValues(t *testing.T) {
	when := time.Now().UTC()
	for _, evidence := range []RelationEvidence{
		{Kind: "exact", Reason: "reason", Operation: OperationDescribeInstances, Scope: "us-east-1", ObservedAt: when},
		{Kind: RelationInferred, Operation: OperationDescribeInstances, Scope: "us-east-1", ObservedAt: when},
		{Kind: RelationInferred, Reason: "reason", Scope: "us-east-1", ObservedAt: when},
		{Kind: RelationInferred, Reason: "reason", Operation: OperationDescribeInstances, ObservedAt: when},
		{Kind: RelationInferred, Reason: "reason", Operation: OperationDescribeInstances, Scope: "all-regions", ObservedAt: when},
		{Kind: RelationInferred, Reason: "reason", Operation: OperationDescribeInstances, Scope: "us-east-1"},
	} {
		if !errors.Is(evidence.Validate(), ErrInvalidRelationEvidence) {
			t.Fatalf("invalid evidence accepted: %+v", evidence)
		}
	}
}
