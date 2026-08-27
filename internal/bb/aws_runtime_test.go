package bb

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
	awsintegration "github.com/jisung9870/binbox-cli/internal/bb/awsbrowser/integration"
)

type awsIntentSearchFunc func(context.Context, awsintegration.SearchRequest) (awsintegration.SearchResult, error)

func (function awsIntentSearchFunc) Submit(ctx context.Context, request awsintegration.SearchRequest) (awsintegration.SearchResult, error) {
	return function(ctx, request)
}

type awsIntentSearchStreamFake struct {
	updates <-chan awsintegration.SearchUpdate
}

func (fake awsIntentSearchStreamFake) Submit(context.Context, awsintegration.SearchRequest) (awsintegration.SearchResult, error) {
	return awsintegration.SearchResult{}, nil
}

func (fake awsIntentSearchStreamFake) Stream(context.Context, awsintegration.SearchRequest) (<-chan awsintegration.SearchUpdate, error) {
	return fake.updates, nil
}

func (function awsIntentSearchFunc) Stream(ctx context.Context, request awsintegration.SearchRequest) (<-chan awsintegration.SearchUpdate, error) {
	updates := make(chan awsintegration.SearchUpdate, 1)
	go func() {
		defer close(updates)
		result, err := function(ctx, request)
		if err == nil {
			updates <- awsintegration.SearchUpdate{Result: result, Done: true}
		}
	}()
	return updates, nil
}

func TestAWSIntentCatalogAndRelationRouting(t *testing.T) {
	tests := []struct {
		target    string
		provider  string
		operation string
		params    map[string]string
	}{
		{"ec2-instances", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeInstances, nil},
		{"route53-hosted-zones", awsbrowser.ProviderRoute53, awsbrowser.OperationListHostedZones, nil},
		{"iam-roles", awsbrowser.ProviderIAM, awsbrowser.OperationListRoles, nil},
		{"vpc-networking", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeVpcs, nil},
		{"ec2.instance:i-123", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeInstances, map[string]string{"instance-id": "i-123"}},
		{"ec2.volume:vol-123", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeVolumes, map[string]string{"volume-id": "vol-123"}},
		{"ec2.security-group:sg-123", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeSecurityGroups, map[string]string{"group-id": "sg-123"}},
		{"ec2.security-group-rule:sgr-123", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeSecurityGroupRules, map[string]string{"security-group-rule-id": "sgr-123"}},
		{"ec2.vpc:vpc-123", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeVpcs, map[string]string{"vpc-id": "vpc-123"}},
		{"ec2.subnet:subnet-123", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeSubnets, map[string]string{"subnet-id": "subnet-123"}},
		{"ec2.route-table:rtb-123", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeRouteTables, map[string]string{"route-table-id": "rtb-123"}},
		{"iam.role:reader", awsbrowser.ProviderIAM, awsbrowser.OperationGetRole, map[string]string{"role-name": "reader"}},
		{"iam.instance-profile:worker", awsbrowser.ProviderIAM, awsbrowser.OperationGetInstanceProfile, map[string]string{"instance-profile-name": "worker"}},
		{"iam.managed-policy:arn:aws:iam::123456789012:policy/read", awsbrowser.ProviderIAM, awsbrowser.OperationGetPolicy, map[string]string{"policy-arn": "arn:aws:iam::123456789012:policy/read"}},
		{"iam.inline-policy:reader:inline", awsbrowser.ProviderIAM, awsbrowser.OperationGetRolePolicy, map[string]string{"role-name": "reader", "policy-name": "inline"}},
		{"iam.managed-policy-version:arn:aws:iam::123456789012:policy/read:v7", awsbrowser.ProviderIAM, awsbrowser.OperationGetPolicyVersion, map[string]string{"policy-arn": "arn:aws:iam::123456789012:policy/read", "version-id": "v7"}},
		{"hosted-zone:Z123", awsbrowser.ProviderRoute53, awsbrowser.OperationListResourceRecordSets, map[string]string{"hosted-zone-id": "Z123"}},
	}
	for _, test := range tests {
		t.Run(test.target, func(t *testing.T) {
			request, ok := awsRequestForIntent(awsbrowser.Intent{Target: test.target, Profile: "dev", Region: "us-east-1"})
			if !ok || request.Profile != "dev" || request.Region != "us-east-1" || request.Provider != test.provider || request.Operation != test.operation || !reflect.DeepEqual(request.Params, test.params) {
				t.Fatalf("request=%+v ok=%v", request, ok)
			}
		})
	}
	for _, target := range []string{"", "unknown", "ec2.instance:", "ec2.instance:i-1\nunsafe", "iam.inline-policy:missing", "iam.managed-policy-version:bad"} {
		if _, ok := awsRequestForIntent(awsbrowser.Intent{Target: target}); ok {
			t.Fatalf("unsupported target %q was accepted", target)
		}
	}
}

func TestAWSNavigableRelationContractHasRuntimeMapping(t *testing.T) {
	tests := map[string]string{
		"ec2.instance": "i-1", "ec2.volume": "vol-1", "ec2.security-group": "sg-1",
		"ec2.security-group-rule": "sgr-1", "ec2.vpc": "vpc-1", "ec2.subnet": "subnet-1",
		"ec2.route-table": "rtb-1", "iam.role": "reader", "iam.instance-profile": "worker",
		"iam.managed-policy":         "arn:aws:iam::123456789012:policy/read",
		"iam.inline-policy":          "reader:inline",
		"iam.managed-policy-version": "arn:aws:iam::123456789012:policy/read:v1",
		"hosted-zone":                "Z1",
	}
	for resourceType, id := range tests {
		if !awsbrowser.NavigableRelationTargetType(resourceType) {
			t.Fatalf("runtime-mapped type %q is not navigable", resourceType)
		}
		if _, ok := awsRequestForIntent(awsbrowser.Intent{Target: resourceType + ":" + id}); !ok {
			t.Fatalf("navigable type %q has no runtime mapping", resourceType)
		}
	}
	for _, resourceType := range []string{"ec2.gateway", "ec2.egress-only-internet-gateway", "ec2.carrier-gateway", "ec2.local-gateway", "ec2.nat-gateway", "ec2.network-interface", "ec2.transit-gateway", "ec2.vpc-peering-connection", "networkmanager.core-network"} {
		if awsbrowser.NavigableRelationTargetType(resourceType) {
			t.Fatalf("unmapped emitted type %q is navigable", resourceType)
		}
		if _, ok := awsRequestForIntent(awsbrowser.Intent{Target: resourceType + ":id-1"}); ok {
			t.Fatalf("unmapped emitted type %q reached runtime", resourceType)
		}
	}
}

func TestAWSSearchIntentMapsCurrentAndAll(t *testing.T) {
	tests := []struct {
		intent awsbrowser.Intent
		want   awsintegration.SearchRequest
	}{
		{awsbrowser.Intent{SearchKind: "ec2-instances", Scope: "current", Query: "i-1", Profile: "dev", Region: "us-east-1"}, awsintegration.SearchRequest{Kind: awsintegration.SearchEC2Instances, Scope: awsintegration.SearchCurrent, Query: "i-1", Profile: "dev", Region: "us-east-1"}},
		{awsbrowser.Intent{SearchKind: "domain", Scope: "all", Query: "api.example.com."}, awsintegration.SearchRequest{Kind: awsintegration.SearchDomain, Scope: awsintegration.SearchAll, Query: "api.example.com."}},
		{awsbrowser.Intent{SearchKind: "role", Scope: "all", Query: "reader"}, awsintegration.SearchRequest{Kind: awsintegration.SearchRole, Scope: awsintegration.SearchAll, Query: "reader"}},
	}
	for _, test := range tests {
		got, ok := awsSearchRequestForIntent(test.intent)
		if !ok || got != test.want {
			t.Fatalf("got=%+v ok=%v want=%+v", got, ok, test.want)
		}
	}
	if _, ok := awsSearchRequestForIntent(awsbrowser.Intent{SearchKind: "role", Scope: "wide"}); ok {
		t.Fatal("unsupported scope accepted")
	}
	if _, ok := awsSearchRequestForIntent(awsbrowser.Intent{Target: "unexpected", SearchKind: "role", Scope: "all", Query: "reader"}); ok {
		t.Fatal("unsupported search target accepted")
	}
}

func TestAWSIntentSearchCancellationIsIdempotent(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	dispatcher := &awsIntentDispatcher{search: awsIntentSearchFunc(func(ctx context.Context, _ awsintegration.SearchRequest) (awsintegration.SearchResult, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return awsintegration.SearchResult{}, nil
	})}
	stream, err := dispatcher.Dispatch(context.Background(), awsbrowser.Intent{Kind: awsbrowser.IntentSearch, Target: "cross-profile-search", SearchKind: "role", Scope: "all", Query: "reader"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	stream.Cancel()
	stream.Cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("search did not observe cancellation")
	}
}

func TestAWSIntentDispatcherRepeatedSearchRetainsBoundedRequest(t *testing.T) {
	var requests []awsintegration.SearchRequest
	dispatcher := &awsIntentDispatcher{search: awsIntentSearchFunc(func(_ context.Context, request awsintegration.SearchRequest) (awsintegration.SearchResult, error) {
		requests = append(requests, request)
		return awsintegration.SearchResult{}, nil
	})}
	intent := awsbrowser.Intent{
		Kind: awsbrowser.IntentSearch, Target: "cross-profile-search", SearchKind: "role",
		Scope: "all", Query: "reader", Profile: "dev", Region: "us-east-1",
	}
	for range 2 {
		stream, err := dispatcher.Dispatch(context.Background(), intent)
		if err != nil {
			t.Fatal(err)
		}
		update := <-stream.Updates()
		if !update.Done || update.Query.Snapshot.State != awsbrowser.LoadEmpty {
			t.Fatalf("search update=%+v", update)
		}
	}
	want := awsintegration.SearchRequest{
		Kind: awsintegration.SearchRole, Scope: awsintegration.SearchAll, Query: "reader", Profile: "dev", Region: "us-east-1",
	}
	if len(requests) != 2 || requests[0] != want || requests[1] != want {
		t.Fatalf("repeated requests=%+v want=%+v", requests, want)
	}
}

func TestAWSIntentDispatcherForwardsProgressAndTerminalOwnership(t *testing.T) {
	searchUpdates := make(chan awsintegration.SearchUpdate, 2)
	dispatcher := &awsIntentDispatcher{search: awsIntentSearchStreamFake{updates: searchUpdates}}
	stream, err := dispatcher.Dispatch(context.Background(), awsbrowser.Intent{Kind: awsbrowser.IntentSearch, SearchKind: "role", Scope: "all", Query: "reader"})
	if err != nil {
		t.Fatal(err)
	}
	searchUpdates <- awsintegration.SearchUpdate{Result: awsintegration.SearchResult{Coverage: []awsintegration.ProfileCoverage{{Profile: "current", Current: true, Status: awsintegration.ProfileStatusNotFound}}}}
	searchUpdates <- awsintegration.SearchUpdate{Result: awsintegration.SearchResult{Coverage: []awsintegration.ProfileCoverage{{Profile: "current", Current: true, Status: awsintegration.ProfileStatusNotFound}, {Profile: "audit", Status: awsintegration.ProfileStatusNotFound}}}, Done: true}
	close(searchUpdates)
	progress := <-stream.Updates()
	if progress.Done || progress.Coverage == nil || len(progress.Coverage.Profiles) != 1 {
		t.Fatalf("progress=%+v", progress)
	}
	terminal := <-stream.Updates()
	if !terminal.Done || terminal.Coverage == nil || len(terminal.Coverage.Profiles) != 2 {
		t.Fatalf("terminal=%+v", terminal)
	}
	if _, open := <-stream.Updates(); open {
		t.Fatal("intent stream remained open after terminal update")
	}
}

func TestAWSAppConstructionAndBrowserHomeAreZeroCall(t *testing.T) {
	stderr := new(bytes.Buffer)
	app := New(new(bytes.Buffer), stderr, []string{"BB_SELECTOR=plain"})
	lookups := 0
	app.lookPath = func(string) (string, error) {
		lookups++
		return "", errors.New("must remain lazy")
	}
	app.awsBrowserTerminal = func() awsbrowser.Terminal {
		return awsbrowser.Terminal{In: strings.NewReader("quit\n"), Err: stderr, StdinTTY: true, StderrTTY: true, Width: 80, Height: 24}
	}
	if err := app.Run([]string{"aws", "browse"}); err != nil {
		t.Fatal(err)
	}
	if lookups != 0 {
		t.Fatalf("Home performed %d AWS executable lookups", lookups)
	}
}

func TestProductionAWSQueryConversionIsCanonicalAndSafe(t *testing.T) {
	when := time.Date(2026, 8, 28, 1, 2, 3, 0, time.UTC)
	awsContext, err := awsbrowser.NewAWSContext(
		awsbrowser.ContextSpec{Mode: awsbrowser.ContextModeNamedProfile, Profile: "dev", Region: "us-east-1"},
		awsbrowser.VerifiedIdentity{Partition: "aws", AccountID: "123456789012", PrincipalARN: "arn:aws:iam::123456789012:role/reader", CredentialGeneration: 1}, "reader",
	)
	if err != nil {
		t.Fatal(err)
	}
	key, err := awsbrowser.NewGlobalResourceKey(awsContext, "iam.role", "reader")
	if err != nil {
		t.Fatal(err)
	}
	observation, err := awsbrowser.NewResourceObservationForOperation(awsContext, awsbrowser.OperationGetRole, map[string]any{"role_name": "reader"}, when, true)
	if err != nil {
		t.Fatal(err)
	}
	searchResult := awsintegration.SearchResult{
		Resources: []awsintegration.CanonicalSearchResource{{Key: key, Observations: []awsbrowser.ResourceObservation{observation}, AvailableViaProfiles: []string{"dev", "audit"}}},
		Coverage: []awsintegration.ProfileCoverage{
			{Profile: "dev", Current: true, AccountID: "123456789012", Status: awsintegration.ProfileStatusMatched, Matches: 1},
			{Profile: "locked", Status: awsintegration.ProfileStatusForbidden},
		},
	}
	service := &productionAWSQueryService{search: awsIntentSearchFunc(func(context.Context, awsintegration.SearchRequest) (awsintegration.SearchResult, error) {
		return searchResult, nil
	})}
	execution, err := service.Execute(context.Background(), awsQueryRequest{Kind: awsQueryKindRoleExact, Value: "reader", Scope: awsQueryScopeAll})
	if err != nil {
		t.Fatal(err)
	}
	if len(execution.Results) != 1 || execution.Results[0].Resource.ID != "reader" || !reflect.DeepEqual(execution.Results[0].AvailableViaProfiles, []string{"dev", "audit"}) {
		t.Fatalf("results=%+v", execution.Results)
	}
	if execution.Coverage.Total != 2 || execution.Coverage.Matched != 1 || execution.Coverage.Forbidden != 1 || !execution.Coverage.Partial || len(execution.Errors) != 1 {
		t.Fatalf("execution=%+v", execution)
	}
	failure := execution.Errors[0]
	if failure.Kind != "forbidden" || failure.Service != awsbrowser.ProviderIAM || failure.Operation != awsbrowser.OperationGetRole || failure.Code != "" || failure.RequestID != "" {
		t.Fatalf("failure=%+v", failure)
	}

	unsafe := &productionAWSQueryService{search: awsIntentSearchFunc(func(context.Context, awsintegration.SearchRequest) (awsintegration.SearchResult, error) {
		return awsintegration.SearchResult{}, errors.New("secret raw backend cause")
	})}
	_, err = unsafe.Execute(context.Background(), awsQueryRequest{Kind: awsQueryKindRoleExact, Value: "reader", Scope: awsQueryScopeAll})
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe error escaped: %v", err)
	}
}

func TestInteractiveSearchPreservesPartialCoverageAndPerResourceContext(t *testing.T) {
	when := time.Date(2026, 8, 28, 1, 2, 3, 0, time.UTC)
	awsContext, err := awsbrowser.NewAWSContext(
		awsbrowser.ContextSpec{Mode: awsbrowser.ContextModeNamedProfile, Profile: "audit", Region: "us-west-2"},
		awsbrowser.VerifiedIdentity{Partition: "aws", AccountID: "123456789012", PrincipalARN: "arn:aws:iam::123456789012:role/audit", CredentialGeneration: 1}, "audit",
	)
	if err != nil {
		t.Fatal(err)
	}
	record, _ := awsbrowser.NewGlobalResourceKey(awsContext, "resource-record-set", "name=api.example.com.&zone=Z1")
	zone, _ := awsbrowser.NewGlobalResourceKey(awsContext, "hosted-zone", "Z1")
	observation, err := awsbrowser.NewResourceObservationForOperation(awsContext, awsbrowser.OperationListResourceRecordSets, map[string]any{
		"name":          "api.example.com.",
		"zone_relation": map[string]any{"target": zone, "kind": "api-exact", "reason": "record-listed-from-hosted-zone"},
	}, when, true)
	if err != nil {
		t.Fatal(err)
	}
	update := intentUpdateFromSearch(awsintegration.SearchResult{
		Resources:       []awsintegration.CanonicalSearchResource{{Key: record, Observations: []awsbrowser.ResourceObservation{observation}, AvailableViaProfiles: []string{"audit", "read-only"}}},
		Coverage:        []awsintegration.ProfileCoverage{{Profile: "audit", Region: "us-west-2", AccountID: "123456789012", Current: true, Status: awsintegration.ProfileStatusMatched, Matches: 1}, {Profile: "locked", Status: awsintegration.ProfileStatusForbidden}},
		DiscoveryStatus: awsintegration.ProfileStatusTimedOut,
	})
	if update.Coverage == nil || !update.Coverage.Partial || update.Coverage.DiscoveryStatus != "timed_out" || len(update.Coverage.Profiles) != 2 {
		t.Fatalf("coverage=%+v", update.Coverage)
	}
	resource := update.Projection.Resources[0]
	if resource.Context == nil || resource.Context.Profile != "audit" || !resource.Current || !reflect.DeepEqual(resource.AvailableViaProfiles, []string{"audit", "read-only"}) {
		t.Fatalf("resource provenance=%+v", resource)
	}
	if len(resource.Relations) != 1 || resource.Relations[0].Target != "hosted-zone:Z1" {
		t.Fatalf("resource relations=%+v", resource.Relations)
	}
}
