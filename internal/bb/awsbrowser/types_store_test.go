package awsbrowser

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestAWSContextAndResourceKeysRequireVerifiedConstruction(t *testing.T) {
	context := testStoreContext(t, "dev", "123456789012", "ap-northeast-2", 1)
	if err := context.Validate(); err != nil {
		t.Fatal(err)
	}

	mutated := context
	mutated.Profile = "other"
	if !errors.Is(mutated.Validate(), ErrInvalidAWSContext) {
		t.Fatalf("mutated context was accepted: %+v", mutated)
	}
	if _, err := NewRegionalResourceKey(AWSContext{}, "instance", "i-001"); !errors.Is(err, ErrInvalidResourceKey) {
		t.Fatalf("unverified context resource key error=%v", err)
	}

	regional, err := NewRegionalResourceKey(context, "instance", "i-001")
	if err != nil {
		t.Fatal(err)
	}
	global, err := NewGlobalResourceKey(context, "role", "arn:aws:iam::123456789012:role/ReadOnly")
	if err != nil {
		t.Fatal(err)
	}
	if regional.Region != "ap-northeast-2" || global.Region != GlobalRegion || regional == global {
		t.Fatalf("regional=%+v global=%+v", regional, global)
	}
	otherRegion := testStoreContext(t, "dev", "123456789012", "us-west-2", 1)
	otherRegional, _ := NewRegionalResourceKey(otherRegion, "instance", "i-001")
	otherGlobal, _ := NewGlobalResourceKey(otherRegion, "role", "arn:aws:iam::123456789012:role/ReadOnly")
	if otherRegional == regional || otherGlobal != global {
		t.Fatalf("global/regional scope mismatch: regional=%+v other=%+v global=%+v other=%+v", regional, otherRegional, global, otherGlobal)
	}
	otherAccount := testStoreContext(t, "audit", "210987654321", "ap-northeast-2", 1)
	otherAccountKey, _ := NewRegionalResourceKey(otherAccount, "instance", "i-001")
	if otherAccountKey == regional {
		t.Fatal("resource IDs collided across accounts")
	}

	mutatedKey := regional
	mutatedKey.AccountID = "210987654321"
	if !errors.Is(mutatedKey.Validate(), ErrInvalidResourceKey) {
		t.Fatalf("mutated resource key was accepted: %+v", mutatedKey)
	}
}

func TestQueryKeyNormalizesParametersAndRejectsCredentialNames(t *testing.T) {
	context := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	first, err := NewQueryKey(context, "EC2", "DescribeInstances", map[string]string{
		"vpc-id": "vpc-1",
		"state":  "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewQueryKey(context, "ec2", "DescribeInstances", map[string]string{
		"state":  "running",
		"vpc-id": "vpc-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.ParamsKey != "state=running&vpc-id=vpc-1" {
		t.Fatalf("query keys are not normalized: %+v %+v", first, second)
	}
	if _, err := NewQueryKey(context, "ec2", "DescribeInstances", map[string]string{"session_token": "secret"}); !errors.Is(err, ErrInvalidQueryKey) {
		t.Fatalf("credential-shaped query param was accepted: %v", err)
	}
}

func TestGlobalResourceKeyCanonicalizesWhileQueryAndObservationRemainSelectedRegionScoped(t *testing.T) {
	east := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	west := testStoreContext(t, "dev", "123456789012", "us-west-2", 1)
	eastGlobal, _ := NewGlobalResourceKey(east, "role", "arn:aws:iam::123456789012:role/ReadOnly")
	westGlobal, _ := NewGlobalResourceKey(west, "role", "arn:aws:iam::123456789012:role/ReadOnly")
	eastQuery, _ := NewQueryKey(east, ProviderIAM, OperationListRoles, nil)
	westQuery, _ := NewQueryKey(west, ProviderIAM, OperationListRoles, nil)
	eastObservation := testOperationObservation(t, east, OperationListRoles, map[string]any{"name": "ReadOnly"}, time.Now())
	westObservation := testOperationObservation(t, west, OperationListRoles, map[string]any{"name": "ReadOnly"}, time.Now())
	eastProvenance, _ := eastObservation.Provenance()
	westProvenance, _ := westObservation.Provenance()
	if eastGlobal != westGlobal {
		t.Fatalf("global resource key varied by selected region: east=%+v west=%+v", eastGlobal, westGlobal)
	}
	if eastQuery == westQuery || eastProvenance == westProvenance {
		t.Fatal("query/observation provenance lost selected-region scope")
	}
}

func TestMappedFieldsRejectCredentialShapesAndRawObjects(t *testing.T) {
	context := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	when := time.Now().UTC()
	for _, fields := range []map[string]any{
		{"session_token": "must-not-be-retained"},
		{"raw": &struct{ Value string }{Value: "provider-payload"}},
		{"related": ResourceKey{}},
	} {
		if _, err := NewResourceObservation(context, fields, when, true); !errors.Is(err, ErrInvalidMappedFields) {
			t.Fatalf("unsafe mapped fields accepted: fields=%+v err=%v", fields, err)
		}
	}
}

func TestMappedFieldsRequireSDKValuesToBeNormalizedAndKeepExternalTargetsStructured(t *testing.T) {
	type sdkEnum string
	type sdkStruct struct{ Value string }
	context := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	when := time.Now().UTC()
	for _, value := range []any{sdkEnum("running"), &sdkStruct{Value: "raw"}, sdkStruct{Value: "raw"}, new(string)} {
		if _, err := NewResourceObservationForOperation(context, OperationDescribeInstances, map[string]any{"value": value}, when, true); !errors.Is(err, ErrInvalidMappedFields) {
			t.Fatalf("un-normalized SDK-shaped value %T accepted: %v", value, err)
		}
	}
	observation, err := NewResourceObservationForOperation(context, OperationDescribeSecurityGroupRules, map[string]any{
		"external-targets": []any{
			map[string]any{"kind": "cidr", "value": "10.0.0.0/8"},
			map[string]any{"kind": "dns", "value": "example.test"},
			map[string]any{"kind": "principal", "value": "*"},
		},
	}, when, true)
	if err != nil || len(observation.Fields()["external-targets"].([]any)) != 3 {
		t.Fatalf("structured external targets were not preserved: fields=%+v err=%v", observation.Fields(), err)
	}
}

func TestSessionStoreCrossProfileIsolationAndDuplicateProvenance(t *testing.T) {
	store := NewSessionStore()
	fetchedAt := time.Date(2026, 8, 28, 10, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	dev := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	audit := testStoreContext(t, "audit", "123456789012", "us-east-1", 1)
	devQuery := testQueryKey(t, dev)
	auditQuery := testQueryKey(t, audit)
	if devQuery == auditQuery {
		t.Fatal("profile provenance did not isolate query keys")
	}
	resourceKey, err := NewRegionalResourceKey(dev, "instance", "i-shared")
	if err != nil {
		t.Fatal(err)
	}
	auditResourceKey, err := NewRegionalResourceKey(audit, "instance", "i-shared")
	if err != nil {
		t.Fatal(err)
	}
	if resourceKey != auditResourceKey {
		t.Fatalf("same account resource failed to canonicalize: dev=%+v audit=%+v", resourceKey, auditResourceKey)
	}

	commitOneResource(t, store, devQuery, resourceKey, testObservation(t, dev, map[string]any{"name": "dev-view"}, fetchedAt), fetchedAt)
	commitOneResource(t, store, auditQuery, resourceKey, testObservation(t, audit, map[string]any{"name": "audit-view"}, fetchedAt), fetchedAt)
	canonical, ok := store.Canonical(resourceKey)
	if !ok || canonical.ObservationCount() != 2 {
		t.Fatalf("canonical observations=%d ok=%v", canonical.ObservationCount(), ok)
	}

	newer := testObservation(t, dev, map[string]any{"name": "newer-dev-view"}, fetchedAt.Add(time.Minute))
	if err := store.StoreObservation(resourceKey, newer); err != nil {
		t.Fatal(err)
	}
	canonical, _ = store.Canonical(resourceKey)
	if canonical.ObservationCount() != 2 {
		t.Fatalf("duplicate provenance created another observation: %d", canonical.ObservationCount())
	}
	provenance, _ := dev.Provenance()
	observation, ok := canonical.Observation(provenance)
	if !ok || observation.Fields()["name"] != "newer-dev-view" {
		t.Fatalf("newest duplicate observation not retained: %+v ok=%v", observation.Fields(), ok)
	}
}

func TestSessionStorePreservesIndependentOperationObservations(t *testing.T) {
	store := NewSessionStore()
	context := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	key, _ := NewRegionalResourceKey(context, "instance", "i-operations")
	when := time.Now().UTC()
	list := testOperationObservation(t, context, OperationDescribeInstances, map[string]any{"view": "list"}, when)
	detail := testOperationObservation(t, context, OperationDescribeVolumes, map[string]any{"view": "detail"}, when.Add(time.Minute))
	for _, observation := range []ResourceObservation{list, detail} {
		if err := store.StoreObservation(key, observation); err != nil {
			t.Fatal(err)
		}
	}
	canonical, _ := store.Canonical(key)
	if canonical.ObservationCount() != 2 {
		t.Fatalf("operation observations overwrote each other: %d", canonical.ObservationCount())
	}
	listProvenance, _ := list.Provenance()
	gotList, ok := canonical.ObservationForOperation(listProvenance)
	if !ok || gotList.Fields()["view"] != "list" {
		t.Fatalf("list observation missing: %+v ok=%v", gotList, ok)
	}
	contextProvenance, _ := context.Provenance()
	newest, ok := canonical.Observation(contextProvenance)
	if !ok || newest.Operation != OperationDescribeVolumes || newest.Fields()["view"] != "detail" {
		t.Fatalf("compatibility lookup did not return newest operation: %+v ok=%v", newest, ok)
	}

	mutated := detail
	mutated.Operation = OperationDescribeInstances
	if !errors.Is(store.StoreObservation(key, mutated), ErrIncompleteObservation) {
		t.Fatal("mutated operation identity was accepted")
	}
	copy := canonical.Observations()
	copy[0].fields["view"] = "caller mutation"
	again, _ := store.Canonical(key)
	gotList, _ = again.ObservationForOperation(listProvenance)
	if gotList.Fields()["view"] != "list" {
		t.Fatal("operation observation exposed store memory")
	}

	tieStore := NewSessionStore()
	for _, observation := range []ResourceObservation{
		testOperationObservation(t, context, OperationDescribeVolumes, map[string]any{"view": "volume"}, when),
		testOperationObservation(t, context, OperationDescribeInstances, map[string]any{"view": "instance"}, when),
	} {
		if err := tieStore.StoreObservation(key, observation); err != nil {
			t.Fatal(err)
		}
	}
	tied, _ := tieStore.Canonical(key)
	stable, _ := tied.Observation(contextProvenance)
	if stable.Operation != OperationDescribeInstances {
		t.Fatalf("operation tie-break was not stable: %s", stable.Operation)
	}
}

func TestSessionStoreRefreshIsAtomicAndFailureIsStale(t *testing.T) {
	store := NewSessionStore()
	context := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	query := testQueryKey(t, context)
	resource, _ := NewRegionalResourceKey(context, "instance", "i-old")
	oldTime := time.Date(2026, 8, 28, 1, 0, 0, 0, time.UTC)
	commitOneResource(t, store, query, resource, testObservation(t, context, map[string]any{"name": "old"}, oldTime), oldTime)

	if err := store.BeginRefresh(query); err != nil {
		t.Fatal(err)
	}
	newResource, _ := NewRegionalResourceKey(context, "instance", "i-new")
	newTime := oldTime.Add(time.Hour)
	page, _ := NewQueryPage(0, []ObservedResource{{
		Key: newResource, Observation: testObservation(t, context, map[string]any{"name": "new"}, newTime),
	}}, newTime, true)
	if err := store.CommitPage(query, page); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.Snapshot(query)
	if snapshot.State != LoadRefreshing || snapshot.ResourceCount() != 1 || snapshot.Pages()[0].Resources()[0].Key != resource {
		t.Fatalf("refresh exposed staged page: state=%s pages=%+v", snapshot.State, snapshot.Pages())
	}
	if _, ok := store.Canonical(newResource); ok {
		t.Fatal("refresh exposed staged canonical observation")
	}
	if err := store.FailQuery(query, LoadTimedOut, newTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = store.Snapshot(query)
	if snapshot.State != LoadStale || snapshot.FetchedAt != oldTime || snapshot.RefreshFailure == nil ||
		snapshot.RefreshFailure.State != LoadTimedOut || snapshot.Pages()[0].Resources()[0].Key != resource {
		t.Fatalf("failed refresh did not retain stale value: %+v", snapshot)
	}

	if err := store.BeginRefresh(query); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitPage(query, page); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteQuery(query, newTime); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = store.Snapshot(query)
	if snapshot.State != LoadReady || snapshot.FetchedAt != newTime || snapshot.Pages()[0].Resources()[0].Key != newResource {
		t.Fatalf("successful refresh was not atomically installed: %+v", snapshot)
	}
}

func TestSessionStoreDiscardsIncompleteAndCancelledPages(t *testing.T) {
	store := NewSessionStore()
	context := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	query := testQueryKey(t, context)
	resource, _ := NewRegionalResourceKey(context, "instance", "i-partial")
	fetchedAt := time.Now().UTC()
	observation := testObservation(t, context, map[string]any{"name": "partial"}, fetchedAt)
	page, err := NewQueryPage(0, []ObservedResource{{Key: resource, Observation: observation}}, fetchedAt, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginLoad(query); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitPage(query, page); !errors.Is(err, ErrIncompletePage) {
		t.Fatalf("incomplete page commit error=%v", err)
	}
	if err := store.FailQuery(query, LoadCancelled, fetchedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.Snapshot(query)
	if snapshot.State != LoadCancelled || len(snapshot.Pages()) != 0 {
		t.Fatalf("cancelled page retained: %+v", snapshot)
	}
	if _, ok := store.Canonical(resource); ok {
		t.Fatal("incomplete page entered canonical resource store")
	}
}

func TestSessionStoreGenerationAndAccountInvalidation(t *testing.T) {
	store := NewSessionStore()
	oldContext := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	newContext := testStoreContext(t, "dev", "123456789012", "us-east-1", 2)
	otherAccount := testStoreContext(t, "audit", "210987654321", "us-east-1", 1)
	when := time.Now().UTC()

	for index, context := range []AWSContext{oldContext, newContext, otherAccount} {
		query := testQueryKey(t, context)
		resource, _ := NewRegionalResourceKey(context, "instance", fmt.Sprintf("i-%d", index))
		commitOneResource(t, store, query, resource, testObservation(t, context, map[string]any{"index": index}, when), when)
	}
	if removed := store.InvalidateGeneration(newContext); removed == 0 {
		t.Fatal("older generation was not invalidated")
	}
	if _, ok := store.Snapshot(testQueryKey(t, oldContext)); ok {
		t.Fatal("old generation query remains")
	}
	if _, ok := store.Snapshot(testQueryKey(t, newContext)); !ok {
		t.Fatal("current generation query was removed")
	}
	if removed := store.InvalidateContext(newContext); removed == 0 {
		t.Fatal("exact context invalidation removed nothing")
	}
	if _, ok := store.Snapshot(testQueryKey(t, newContext)); ok {
		t.Fatal("exact context query remains")
	}
	commitOneResource(t, store, testQueryKey(t, newContext), func() ResourceKey {
		key, _ := NewRegionalResourceKey(newContext, "instance", "i-current")
		return key
	}(), testObservation(t, newContext, map[string]any{"index": 2}, when), when)
	if removed := store.InvalidateAccount("aws", "123456789012"); removed == 0 {
		t.Fatal("account invalidation removed nothing")
	}
	if _, ok := store.Snapshot(testQueryKey(t, newContext)); ok {
		t.Fatal("account query remains")
	}
	if _, ok := store.Snapshot(testQueryKey(t, otherAccount)); !ok {
		t.Fatal("other account query was removed")
	}
}

func TestSessionStoreDefensiveCopies(t *testing.T) {
	store := NewSessionStore()
	context := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	query := testQueryKey(t, context)
	resource, _ := NewRegionalResourceKey(context, "instance", "i-copy")
	fetchedAt := time.Now().UTC()
	inputNames := []string{"one", "two"}
	inputNested := map[string]any{"owner": "platform"}
	inputFields := map[string]any{"names": inputNames, "metadata": inputNested}
	observation := testObservation(t, context, inputFields, fetchedAt)
	inputNames[0] = "mutated"
	inputNested["owner"] = "mutated"
	inputFields["new"] = true
	commitOneResource(t, store, query, resource, observation, fetchedAt)

	snapshot, _ := store.Snapshot(query)
	pages := snapshot.Pages()
	fields := pages[0].Resources()[0].Observation.Fields()
	fields["metadata"].(map[string]any)["owner"] = "caller mutation"
	pages[0].resources[0].Observation.fields["names"].([]string)[0] = "caller mutation"

	again, _ := store.Snapshot(query)
	stored := again.Pages()[0].Resources()[0].Observation.Fields()
	if stored["names"].([]string)[0] != "one" || stored["metadata"].(map[string]any)["owner"] != "platform" {
		t.Fatalf("snapshot exposed store memory: %+v", stored)
	}
	canonical, _ := store.Canonical(resource)
	canonicalFields := canonical.Observations()[0].Fields()
	canonicalFields["names"].([]string)[0] = "canonical mutation"
	canonicalAgain, _ := store.Canonical(resource)
	if canonicalAgain.Observations()[0].Fields()["names"].([]string)[0] != "one" {
		t.Fatal("canonical observation exposed store memory")
	}
}

func TestSessionStoreConcurrentReadsAndWrites(t *testing.T) {
	store := NewSessionStore()
	when := time.Now().UTC()
	const workers = 24
	contexts := make([]AWSContext, workers)
	queries := make([]QueryKey, workers)
	resources := make([]ResourceKey, workers)
	observations := make([]ResourceObservation, workers)
	for index := 0; index < workers; index++ {
		contexts[index] = testStoreContext(t, fmt.Sprintf("profile-%02d", index), "123456789012", "us-east-1", 1)
		queries[index] = testQueryKey(t, contexts[index])
		resources[index], _ = NewRegionalResourceKey(contexts[index], "instance", "i-shared")
		observations[index] = testObservation(t, contexts[index], map[string]any{"index": index}, when)
	}

	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		index := index
		wait.Add(2)
		go func() {
			defer wait.Done()
			commitOneResource(t, store, queries[index], resources[index], observations[index], when)
		}()
		go func() {
			defer wait.Done()
			for attempt := 0; attempt < 100; attempt++ {
				store.Snapshot(queries[index])
				store.Canonical(resources[index])
			}
		}()
	}
	wait.Wait()
	for index := range queries {
		snapshot, ok := store.Snapshot(queries[index])
		if !ok || snapshot.State != LoadReady || snapshot.ResourceCount() != 1 {
			t.Fatalf("query %d snapshot=%+v ok=%v", index, snapshot, ok)
		}
	}
	canonical, ok := store.Canonical(resources[0])
	if !ok || canonical.ObservationCount() != workers {
		t.Fatalf("concurrent canonical observations=%d ok=%v", canonical.ObservationCount(), ok)
	}
}

func testStoreContext(t *testing.T, profile, account, region string, generation uint64) AWSContext {
	t.Helper()
	context, err := NewAWSContext(ContextSpec{Mode: ContextModeNamedProfile, Profile: profile, Region: region}, VerifiedIdentity{
		Partition:            "aws",
		AccountID:            account,
		PrincipalARN:         "arn:aws:sts::" + account + ":assumed-role/ReadOnly/session",
		CredentialGeneration: generation,
	}, "ReadOnly")
	if err != nil {
		t.Fatal(err)
	}
	return context
}

func testQueryKey(t *testing.T, context AWSContext) QueryKey {
	t.Helper()
	query, err := NewQueryKey(context, "ec2", "DescribeInstances", map[string]string{"state": "running"})
	if err != nil {
		t.Fatal(err)
	}
	return query
}

func testObservation(t *testing.T, context AWSContext, fields map[string]any, fetchedAt time.Time) ResourceObservation {
	t.Helper()
	observation, err := NewResourceObservation(context, fields, fetchedAt, true)
	if err != nil {
		t.Fatal(err)
	}
	return observation
}

func testOperationObservation(t *testing.T, context AWSContext, operation string, fields map[string]any, fetchedAt time.Time) ResourceObservation {
	t.Helper()
	observation, err := NewResourceObservationForOperation(context, operation, fields, fetchedAt, true)
	if err != nil {
		t.Fatal(err)
	}
	return observation
}

func commitOneResource(t *testing.T, store *SessionStore, query QueryKey, key ResourceKey, observation ResourceObservation, fetchedAt time.Time) {
	t.Helper()
	if err := store.BeginLoad(query); err != nil {
		t.Errorf("begin load: %v", err)
		return
	}
	page, err := NewQueryPage(0, []ObservedResource{{Key: key, Observation: observation}}, fetchedAt, true)
	if err != nil {
		t.Errorf("new query page: %v", err)
		return
	}
	if err := store.CommitPage(query, page); err != nil {
		t.Errorf("commit page: %v", err)
		return
	}
	if err := store.CompleteQuery(query, fetchedAt); err != nil {
		t.Errorf("complete query: %v", err)
	}
}
