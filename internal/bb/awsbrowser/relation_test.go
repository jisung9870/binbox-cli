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
	correlated, err := NewRelationEvidence(RelationCorrelated, "private DNS value matches", "ListResourceRecordSets", later)
	if err != nil {
		t.Fatal(err)
	}
	exact, err := NewRelationEvidence(RelationIDExact, "group ID returned by instance", "DescribeInstances", earlier)
	if err != nil {
		t.Fatal(err)
	}
	relation, err := NewRelation(source, target, correlated, exact)
	if err != nil {
		t.Fatal(err)
	}
	evidence := relation.Evidence()
	if len(evidence) != 2 || evidence[0] != exact || evidence[1] != correlated {
		t.Fatalf("evidence was not preserved and ordered: %+v", evidence)
	}
	evidence[0].Reason = "caller mutation"
	if relation.Evidence()[0].Reason != exact.Reason {
		t.Fatal("relation exposed mutable evidence storage")
	}
}

func TestRelationEvidenceRejectsIncompleteValues(t *testing.T) {
	when := time.Now().UTC()
	for _, evidence := range []RelationEvidence{
		{Kind: "exact", Reason: "reason", Operation: "DescribeInstances", ObservedAt: when},
		{Kind: RelationInferred, Operation: "DescribeInstances", ObservedAt: when},
		{Kind: RelationInferred, Reason: "reason", ObservedAt: when},
		{Kind: RelationInferred, Reason: "reason", Operation: "DescribeInstances"},
	} {
		if !errors.Is(evidence.Validate(), ErrInvalidRelationEvidence) {
			t.Fatalf("invalid evidence accepted: %+v", evidence)
		}
	}
}
