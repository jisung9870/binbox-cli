package bb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
	awsintegration "github.com/jisung9870/binbox-cli/internal/bb/awsbrowser/integration"
	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser/snapshot"
)

type fakeAWSSnapshotSyncService struct {
	request  awsSnapshotSyncRequest
	run      snapshot.Run
	coverage []snapshot.Coverage
	err      error
	calls    int
}

func (service *fakeAWSSnapshotSyncService) Sync(_ context.Context, request awsSnapshotSyncRequest) (snapshot.Run, []snapshot.Coverage, error) {
	service.calls++
	service.request = request
	return service.run, service.coverage, service.err
}

type fakeAWSSnapshotReadService struct {
	request   awsSnapshotRefsRequest
	execution awsSnapshotRefsExecution
	err       error
}

func (service *fakeAWSSnapshotReadService) Refs(_ context.Context, request awsSnapshotRefsRequest) (awsSnapshotRefsExecution, error) {
	service.request = request
	return service.execution, service.err
}

type sequenceAWSSnapshotReadService struct {
	executions []awsSnapshotRefsExecution
	calls      int
}

func (service *sequenceAWSSnapshotReadService) Refs(_ context.Context, _ awsSnapshotRefsRequest) (awsSnapshotRefsExecution, error) {
	index := service.calls
	service.calls++
	if index >= len(service.executions) {
		return awsSnapshotRefsExecution{}, errors.New("unexpected snapshot read")
	}
	return service.executions[index], nil
}

func TestAutoSnapshotReusesFreshCompleteGroupCoverage(t *testing.T) {
	now := time.Date(2026, 8, 28, 15, 0, 0, 0, time.UTC)
	group := awsbrowser.ContextGroup{Name: "udg", Profiles: []string{"dev", "prod"}, Regions: []string{"ap-northeast-2", "us-east-1"}}
	coverage := make([]snapshot.Coverage, 0, 4)
	for _, profile := range group.Profiles {
		for _, region := range group.Regions {
			coverage = append(coverage, snapshot.Coverage{Profile: profile, AccountID: "123456789012", Region: region, Service: "ec2-sg", Status: snapshot.CoverageSucceeded})
		}
	}
	execution := awsSnapshotRefsExecution{Run: snapshot.Run{CompletedAt: now.Add(-time.Minute)}, Coverage: coverage}
	syncService := new(fakeAWSSnapshotSyncService)
	readService := &fakeAWSSnapshotReadService{execution: execution}
	service := &awsAutoSnapshotService{sync: syncService, read: readService, groups: []awsbrowser.ContextGroup{group}, now: func() time.Time { return now }}

	_, graph, err := service.Resolve(context.Background(), awsAutoSnapshotRequest{
		Profile: "dev", Partition: "aws", AccountID: "123456789012", Region: "ap-northeast-2", Kind: "sg", ResourceID: "sg-abc123",
	})
	if err != nil || !graph.Reused || graph.AgeSeconds != 60 || syncService.calls != 0 {
		t.Fatalf("graph=%+v sync calls=%d error=%v", graph, syncService.calls, err)
	}
}

func TestAutoSnapshotRefreshesStaleCoverageWithoutManualSync(t *testing.T) {
	now := time.Date(2026, 8, 28, 15, 0, 0, 0, time.UTC)
	group := awsbrowser.ContextGroup{Name: "udg", Profiles: []string{"dev"}, Regions: []string{"ap-northeast-2"}}
	stale := awsSnapshotRefsExecution{Run: snapshot.Run{CompletedAt: now.Add(-10 * time.Minute)}}
	fresh := awsSnapshotRefsExecution{
		Run:      snapshot.Run{CompletedAt: now},
		Coverage: []snapshot.Coverage{{Profile: "dev", AccountID: "123456789012", Region: "ap-northeast-2", Service: "ec2-vpc-peering", Status: snapshot.CoverageSucceeded}},
	}
	readService := &sequenceAWSSnapshotReadService{executions: []awsSnapshotRefsExecution{stale, fresh}}
	syncService := new(fakeAWSSnapshotSyncService)
	service := &awsAutoSnapshotService{sync: syncService, read: readService, groups: []awsbrowser.ContextGroup{group}, now: func() time.Time { return now }}

	_, graph, err := service.Resolve(context.Background(), awsAutoSnapshotRequest{
		Profile: "dev", Partition: "aws", AccountID: "123456789012", Region: "ap-northeast-2", Kind: "vpc", ResourceID: "vpc-abc123",
	})
	if err != nil || graph.Reused || syncService.calls != 1 || syncService.request != (awsSnapshotSyncRequest{Collection: "graph", Group: "udg"}) || readService.calls != 2 {
		t.Fatalf("graph=%+v sync=%+v read calls=%d error=%v", graph, syncService, readService.calls, err)
	}
}

func TestIncomingSnapshotProjectionPrefersNameAndCarriesVerifiedNavigationHints(t *testing.T) {
	at := time.Date(2026, 8, 28, 15, 0, 0, 0, time.UTC)
	target := snapshot.ResourceRef{Partition: "aws", AccountID: "123456789012", Region: "ap-northeast-2", Type: "ec2.security-group", ID: "sg-abc123"}
	sameAccount := snapshot.ResourceRef{Partition: "aws", AccountID: target.AccountID, Region: target.Region, Type: "ec2.instance", ID: "i-abc123"}
	crossAccount := snapshot.ResourceRef{Partition: "aws", AccountID: "210987654321", Region: target.Region, Type: "ec2.instance", ID: "i-def456"}
	targetKey, _ := target.Key()
	sameKey, _ := sameAccount.Key()
	crossKey, _ := crossAccount.Key()
	execution := awsSnapshotRefsExecution{Run: snapshot.Run{CompletedAt: at}, Target: target, Edges: []snapshot.Edge{
		{SourceKey: sameKey, TargetKey: targetKey, SourceName: "web", Relation: snapshot.Relation{Type: awsbrowser.RelationUses, Direction: awsbrowser.RelationOutgoing, Confidence: awsbrowser.RelationIDExact, Reason: "fixture", Operation: "DescribeInstances", Scope: target.Region, ObservedAt: at}, Observers: []snapshot.Observer{{Profile: "dev", AccountID: sameAccount.AccountID, Region: sameAccount.Region, ObservedAt: at}}},
		{SourceKey: crossKey, TargetKey: targetKey, SourceName: "batch", Relation: snapshot.Relation{Type: awsbrowser.RelationUses, Direction: awsbrowser.RelationOutgoing, Confidence: awsbrowser.RelationIDExact, Reason: "fixture", Operation: "DescribeInstances", Scope: target.Region, ObservedAt: at}, Observers: []snapshot.Observer{{Profile: "prod", AccountID: crossAccount.AccountID, Region: crossAccount.Region, ObservedAt: at}}},
	}}
	projection, err := projectAWSIncomingSnapshot(execution, awsAutoSnapshotRequest{Profile: "dev", AccountID: target.AccountID, Region: target.Region})
	if err != nil || len(projection.Resources) != 2 || projection.Resources[0].Title != "web" || projection.Resources[0].Target != "ec2.instance:i-abc123" ||
		projection.Resources[0].Navigation == nil || projection.Resources[0].Navigation.Profile != "dev" ||
		projection.Resources[1].Target != "ec2.instance:i-def456" || projection.Resources[1].Navigation == nil ||
		projection.Resources[1].Navigation.Profile != "prod" || projection.Resources[1].Navigation.ExpectedAccountID != crossAccount.AccountID {
		t.Fatalf("projection=%+v error=%v", projection, err)
	}
}

type awsSnapshotQueryCoreFunc func(context.Context, awsintegration.Request) (awsintegration.Result, error)

func (function awsSnapshotQueryCoreFunc) Query(ctx context.Context, request awsintegration.Request) (awsintegration.Result, error) {
	return function(ctx, request)
}

func TestParseAWSSnapshotCommands(t *testing.T) {
	syncRequest, jsonMode, err := parseAWSSnapshotSync([]string{"sg", "--group", "udg-prod", "--json"})
	if err != nil || !jsonMode || syncRequest.Group != "udg-prod" || syncRequest.Collection != "sg" {
		t.Fatalf("sync request=%#v json=%v error=%v", syncRequest, jsonMode, err)
	}
	graphRequest, graphJSON, err := parseAWSSnapshotSync([]string{"graph", "--group", "udg-prod"})
	if err != nil || graphJSON || graphRequest.Collection != "graph" || graphRequest.Group != "udg-prod" {
		t.Fatalf("graph request=%#v json=%v error=%v", graphRequest, graphJSON, err)
	}
	refsRequest, jsonMode, err := parseAWSSnapshotRefs([]string{"sg", "sg-123abc", "--account", "123456789012", "--region", "ap-northeast-2", "--partition", "aws", "--json"})
	want := awsSnapshotRefsRequest{Partition: "aws", AccountID: "123456789012", Region: "ap-northeast-2", Kind: "sg", ResourceID: "sg-123abc"}
	if err != nil || !jsonMode || refsRequest != want {
		t.Fatalf("refs request=%#v json=%v error=%v", refsRequest, jsonMode, err)
	}
	vpcRequest, _, err := parseAWSSnapshotRefs([]string{"vpc", "vpc-abc123", "--account", "210987654321", "--region", "ap-northeast-2"})
	if err != nil || vpcRequest.Kind != "vpc" || vpcRequest.ResourceID != "vpc-abc123" {
		t.Fatalf("vpc request=%#v error=%v", vpcRequest, err)
	}
}

func TestProductionAWSGraphSnapshotCollectsVpcPeeringAndRemoteCoverage(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC)
	awsContext, err := awsbrowser.NewAWSContext(
		awsbrowser.ContextSpec{Mode: awsbrowser.ContextModeNamedProfile, Profile: "dev", Region: "us-east-1"},
		awsbrowser.VerifiedIdentity{Partition: "aws", AccountID: "123456789012", PrincipalARN: "arn:aws:iam::123456789012:role/read", CredentialGeneration: 1}, "read",
	)
	if err != nil {
		t.Fatal(err)
	}
	peering, _ := awsbrowser.NewRegionalResourceKey(awsContext, "ec2.vpc-peering-connection", "pcx-123")
	requester, _ := awsbrowser.NewRegionalResourceKey(awsContext, "ec2.vpc", "vpc-requester")
	accepter, _ := awsbrowser.NewCanonicalResourceKey("aws", "210987654321", "ap-northeast-2", "ec2.vpc", "vpc-accepter")
	requesterRelation := testAWSSnapshotRelation(t, awsContext, peering, requester, awsbrowser.RelationAssociatedWith, "role=requester", awsbrowser.OperationDescribeVpcPeeringConnections, at)
	accepterRelation := testAWSSnapshotRelation(t, awsContext, peering, accepter, awsbrowser.RelationAssociatedWith, "role=accepter", awsbrowser.OperationDescribeVpcPeeringConnections, at)
	resources := map[string][]awsbrowser.ObservedResource{
		awsbrowser.OperationDescribeSecurityGroups:     {},
		awsbrowser.OperationDescribeSecurityGroupRules: {},
		awsbrowser.OperationDescribeInstances:          {},
		awsbrowser.OperationDescribeVpcPeeringConnections: {
			testAWSSnapshotObserved(t, awsContext, peering, awsbrowser.OperationDescribeVpcPeeringConnections, map[string]any{
				"name": "cross-account", "relations": []any{testAWSSnapshotMappedRelation(requesterRelation), testAWSSnapshotMappedRelation(accepterRelation)},
			}, at),
		},
	}
	core := awsSnapshotQueryCoreFunc(func(_ context.Context, request awsintegration.Request) (awsintegration.Result, error) {
		values, ok := resources[request.Operation]
		if !ok || request.Provider != awsbrowser.ProviderEC2 || request.Profile != "dev" || request.Region != "us-east-1" {
			t.Fatalf("unexpected request=%#v", request)
		}
		return testAWSSnapshotQueryResult(t, awsContext, request.Operation, values, at), nil
	})
	path := filepath.Join(t.TempDir(), "aws", "snapshot.db")
	syncService := &productionAWSSnapshotSyncService{
		core: core, groups: []awsbrowser.ContextGroup{{Name: "udg", Profiles: []string{"dev"}, Regions: []string{"us-east-1"}}},
		path: path, now: func() time.Time { return at },
	}
	run, coverage, err := syncService.Sync(ctx, awsSnapshotSyncRequest{Collection: "graph", Group: "udg"})
	if err != nil || run.ID == "" || len(coverage) != 10 {
		t.Fatalf("run=%#v coverage=%#v error=%v", run, coverage, err)
	}
	var participantCoverage bool
	for _, item := range coverage {
		if item.AccountID == "210987654321" && item.Region == "ap-northeast-2" && item.Service == "ec2-vpc-peering-participant" && item.Status == snapshot.CoverageNotObserved {
			participantCoverage = item.ErrorKind == "participant-account-not-searched"
		}
	}
	if !participantCoverage {
		t.Fatalf("remote participant coverage missing: %#v", coverage)
	}
	execution, err := (&productionAWSSnapshotReadService{path: path}).Refs(ctx, awsSnapshotRefsRequest{
		Partition: "aws", AccountID: "210987654321", Region: "ap-northeast-2", Kind: "vpc", ResourceID: "vpc-accepter",
	})
	if err != nil || len(execution.Edges) != 1 || execution.Edges[0].Relation.Type != awsbrowser.RelationAssociatedWith ||
		execution.Edges[0].Relation.Confidence != awsbrowser.RelationIDExact || execution.Edges[0].Relation.Condition != "role=accepter" {
		t.Fatalf("execution=%#v error=%v", execution, err)
	}
	normalized, err := normalizeAWSSnapshotRefs(execution, at.Add(time.Minute))
	if err != nil || len(normalized.References) != 1 || normalized.References[0].RelationType != "associated-with" || normalized.Coverage.VpcPeeringComplete {
		t.Fatalf("normalized=%#v error=%v", normalized, err)
	}
}

func TestAWSSnapshotInvalidAndHelpDoNotConstructServices(t *testing.T) {
	for _, args := range [][]string{
		{"aws", "sync", "sg"},
		{"aws", "sync", "sg", "--group", "-bad"},
		{"aws", "refs", "sg", "sg-1", "--account", "bad", "--region", "ap-northeast-2"},
		{"aws", "refs", "sg", "sg-1", "--account", "123456789012"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			app := New(new(bytes.Buffer), new(bytes.Buffer), nil)
			app.awsSnapshotSync = func() (awsSnapshotSyncService, error) { t.Fatal("sync service constructed"); return nil, nil }
			app.awsSnapshotRead = func() (awsSnapshotReadService, error) { t.Fatal("read service constructed"); return nil, nil }
			if err := app.Run(args); ExitCode(err) != ExitInvalidInvocation {
				t.Fatalf("args=%q error=%v exit=%d", args, err, ExitCode(err))
			}
		})
	}
	for _, args := range [][]string{{"aws", "sync", "--help"}, {"aws", "refs", "--help"}} {
		app := New(new(bytes.Buffer), new(bytes.Buffer), nil)
		app.awsSnapshotSync = func() (awsSnapshotSyncService, error) { t.Fatal("sync help constructed service"); return nil, nil }
		app.awsSnapshotRead = func() (awsSnapshotReadService, error) { t.Fatal("refs help constructed service"); return nil, nil }
		if err := app.Run(args); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAWSSnapshotSyncJSONEnvelope(t *testing.T) {
	out := new(bytes.Buffer)
	app := New(out, new(bytes.Buffer), nil)
	completed := time.Date(2026, 8, 28, 5, 0, 0, 0, time.UTC)
	app.now = func() time.Time { return completed.Add(10 * time.Second) }
	service := &fakeAWSSnapshotSyncService{
		run: snapshot.Run{ID: "run-1", CompletedAt: completed, SchemaVersion: snapshot.SchemaVersion},
		coverage: []snapshot.Coverage{
			{Profile: "dev", AccountID: "123456789012", Region: "ap-northeast-2", Service: "ec2-sg", Status: snapshot.CoverageSucceeded},
			{Profile: "dev", AccountID: "123456789012", Region: "ap-northeast-2", Service: "rds", Status: snapshot.CoverageNotObserved, ErrorKind: "ec2-only"},
		},
	}
	app.awsSnapshotSync = func() (awsSnapshotSyncService, error) { return service, nil }
	if err := app.Run([]string{"aws", "sync", "sg", "--group", "udg", "--json"}); err != nil {
		t.Fatal(err)
	}
	var document struct {
		OK       bool                `json:"ok"`
		Data     awsSnapshotSyncData `json:"data"`
		Warnings []string            `json:"warnings"`
	}
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if !document.OK || service.request.Collection != "sg" || service.request.Group != "udg" || document.Data.Collection != "sg" || document.Data.Run.AgeSeconds != 10 || document.Data.Coverage.Complete ||
		!document.Data.Coverage.RuleReferencesComplete || document.Data.Coverage.AttachmentsComplete || len(document.Warnings) != 1 {
		t.Fatalf("document=%#v request=%#v", document, service.request)
	}
}

func TestAWSSnapshotRefsHumanDistinguishesIncompleteEmptyAndSanitizes(t *testing.T) {
	out := new(bytes.Buffer)
	app := New(out, new(bytes.Buffer), nil)
	at := time.Date(2026, 8, 28, 5, 0, 0, 0, time.UTC)
	service := &fakeAWSSnapshotReadService{execution: awsSnapshotRefsExecution{
		Run:      snapshot.Run{ID: "run\nunsafe", CompletedAt: at, SchemaVersion: snapshot.SchemaVersion},
		Target:   snapshot.ResourceRef{Partition: "aws", AccountID: "123456789012", Region: "ap-northeast-2", Type: "ec2.security-group", ID: "sg-123"},
		Coverage: []snapshot.Coverage{{Profile: "dev", AccountID: "123456789012", Region: "ap-northeast-2", Service: "rds", Status: snapshot.CoverageNotObserved, ErrorKind: "ec2-only"}},
	}}
	app.awsSnapshotRead = func() (awsSnapshotReadService, error) { return service, nil }
	if err := app.Run([]string{"aws", "refs", "sg", "sg-123", "--account", "123456789012", "--region", "ap-northeast-2"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "Resource not observed in active snapshot.") || !strings.Contains(got, "0 observed references; result incomplete") || strings.Contains(got, "run\nunsafe") {
		t.Fatalf("output=%q", got)
	}
}

func TestAWSSnapshotRefsJSONUsesStableRelationFieldsAndCollections(t *testing.T) {
	out := new(bytes.Buffer)
	app := New(out, new(bytes.Buffer), nil)
	at := time.Date(2026, 8, 28, 5, 30, 0, 0, time.UTC)
	source := snapshot.ResourceRef{Partition: "aws", AccountID: "123456789012", Region: "ap-northeast-2", Type: "ec2.instance", ID: "i-web"}
	target := snapshot.ResourceRef{Partition: "aws", AccountID: "123456789012", Region: "ap-northeast-2", Type: "ec2.security-group", ID: "sg-123"}
	sourceKey, _ := source.Key()
	targetKey, _ := target.Key()
	service := &fakeAWSSnapshotReadService{execution: awsSnapshotRefsExecution{
		Run: snapshot.Run{ID: "run-json", CompletedAt: at, SchemaVersion: snapshot.SchemaVersion}, Target: target, ResourceObserved: true,
		Edges: []snapshot.Edge{{
			SourceKey: sourceKey, TargetKey: targetKey, SourceName: "web",
			Relation: snapshot.Relation{Type: awsbrowser.RelationUses, Direction: awsbrowser.RelationIncoming, Confidence: awsbrowser.RelationIDExact, Condition: "network-interface=eni-1", Reason: "instance network interface security group id", Operation: awsbrowser.OperationDescribeInstances, Scope: target.Region, ObservedAt: at},
		}},
		Coverage: []snapshot.Coverage{{Profile: "dev", AccountID: target.AccountID, Region: target.Region, Service: "ec2-sg", Status: snapshot.CoverageSucceeded}},
	}}
	app.awsSnapshotRead = func() (awsSnapshotReadService, error) { return service, nil }
	if err := app.Run([]string{"aws", "refs", "sg", "sg-123", "--account", target.AccountID, "--region", target.Region, "--json"}); err != nil {
		t.Fatal(err)
	}
	var document struct {
		OK   bool                `json:"ok"`
		Data awsSnapshotRefsData `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if !document.OK || len(document.Data.References) != 1 || document.Data.References[0].RelationType != "uses" || document.Data.References[0].Observers == nil || !document.Data.Coverage.Complete {
		t.Fatalf("document=%#v", document)
	}
	if strings.Contains(out.String(), `"relation":`) || !strings.Contains(out.String(), `"relation_type":"uses"`) {
		t.Fatalf("json=%s", out.String())
	}
}

func TestAWSSnapshotRefsMissingStoreDoesNotDiscoverAWSCLI(t *testing.T) {
	app, _, _, _ := testApp(t)
	app.lookPath = func(string) (string, error) { t.Fatal("refs discovered AWS CLI"); return "", nil }
	err := app.Run([]string{"aws", "refs", "sg", "sg-123", "--account", "123456789012", "--region", "ap-northeast-2"})
	if ExitCode(err) != ExitCapabilityUnavailable || !strings.Contains(err.Error(), "bb aws sync sg") {
		t.Fatalf("error=%v exit=%d", err, ExitCode(err))
	}
}

func TestAWSSnapshotUnknownGroupDoesNotDiscoverAWSCLI(t *testing.T) {
	app, _, config, _ := testApp(t)
	configRoot := filepath.Join(config, "bb")
	if err := os.MkdirAll(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, awsbrowser.AWSContextGroupsFilename), []byte(`{"version":1,"groups":[{"name":"known","profiles":["dev"],"regions":["ap-northeast-2"]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	discoveries := 0
	app.lookPath = func(string) (string, error) {
		discoveries++
		return "", errors.New("not found")
	}
	err := app.Run([]string{"aws", "sync", "sg", "--group", "missing"})
	if ExitCode(err) != ExitInvalidInvocation || !strings.Contains(err.Error(), "context group not found") || discoveries != 0 {
		t.Fatalf("error=%v exit=%d AWS CLI discoveries=%d", err, ExitCode(err), discoveries)
	}
}

func TestAWSSnapshotRefsReportsTruncation(t *testing.T) {
	out := new(bytes.Buffer)
	app := New(out, new(bytes.Buffer), nil)
	at := time.Date(2026, 8, 28, 5, 45, 0, 0, time.UTC)
	service := &fakeAWSSnapshotReadService{execution: awsSnapshotRefsExecution{
		Run:       snapshot.Run{ID: "run-truncated", CompletedAt: at, SchemaVersion: snapshot.SchemaVersion},
		Target:    snapshot.ResourceRef{Partition: "aws", AccountID: "123456789012", Region: "ap-northeast-2", Type: "ec2.security-group", ID: "sg-123"},
		Coverage:  []snapshot.Coverage{{Profile: "dev", AccountID: "123456789012", Region: "ap-northeast-2", Service: "ec2-sg", Status: snapshot.CoverageSucceeded}},
		Truncated: true,
	}}
	app.awsSnapshotRead = func() (awsSnapshotReadService, error) { return service, nil }
	if err := app.Run([]string{"aws", "refs", "sg", "sg-123", "--account", "123456789012", "--region", "ap-northeast-2", "--json"}); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Data     awsSnapshotRefsData `json:"data"`
		Warnings []string            `json:"warnings"`
	}
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if !document.Data.Truncated || document.Data.Limit != awsSnapshotRefsLimit || len(document.Warnings) != 1 || !strings.Contains(document.Warnings[0], "truncated") {
		t.Fatalf("document=%#v", document)
	}
}

func TestProductionAWSSnapshotSyncAndRefsVerticalSlice(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 8, 28, 6, 0, 0, 0, time.UTC)
	awsContext, err := awsbrowser.NewAWSContext(
		awsbrowser.ContextSpec{Mode: awsbrowser.ContextModeNamedProfile, Profile: "dev", Region: "ap-northeast-2"},
		awsbrowser.VerifiedIdentity{Partition: "aws", AccountID: "123456789012", PrincipalARN: "arn:aws:iam::123456789012:role/read", CredentialGeneration: 1}, "read",
	)
	if err != nil {
		t.Fatal(err)
	}
	sourceSG, _ := awsbrowser.NewRegionalResourceKey(awsContext, "ec2.security-group", "sg-source")
	targetSG, _ := awsbrowser.NewRegionalResourceKey(awsContext, "ec2.security-group", "sg-target")
	ruleKey, _ := awsbrowser.NewRegionalResourceKey(awsContext, "ec2.security-group-rule", "sgr-1")
	instanceKey, _ := awsbrowser.NewRegionalResourceKey(awsContext, "ec2.instance", "i-web")
	reference := testAWSSnapshotRelation(t, awsContext, sourceSG, targetSG, awsbrowser.RelationReferences, "rule-id=sgr-1", awsbrowser.OperationDescribeSecurityGroupRules, at)
	uses := testAWSSnapshotRelation(t, awsContext, instanceKey, targetSG, awsbrowser.RelationUses, "network-interface=eni-1", awsbrowser.OperationDescribeInstances, at)
	resources := map[string][]awsbrowser.ObservedResource{
		awsbrowser.OperationDescribeSecurityGroups: {
			testAWSSnapshotObserved(t, awsContext, sourceSG, awsbrowser.OperationDescribeSecurityGroups, map[string]any{"name": "source"}, at),
			testAWSSnapshotObserved(t, awsContext, targetSG, awsbrowser.OperationDescribeSecurityGroups, map[string]any{"name": "target"}, at),
		},
		awsbrowser.OperationDescribeSecurityGroupRules: {
			testAWSSnapshotObserved(t, awsContext, ruleKey, awsbrowser.OperationDescribeSecurityGroupRules, map[string]any{"relations": []any{testAWSSnapshotMappedRelation(reference)}}, at),
		},
		awsbrowser.OperationDescribeInstances: {
			testAWSSnapshotObserved(t, awsContext, instanceKey, awsbrowser.OperationDescribeInstances, map[string]any{"name": "web", "relations": []any{testAWSSnapshotMappedRelation(uses)}}, at),
		},
	}
	core := awsSnapshotQueryCoreFunc(func(_ context.Context, request awsintegration.Request) (awsintegration.Result, error) {
		values, ok := resources[request.Operation]
		if !ok || request.Provider != awsbrowser.ProviderEC2 || request.Profile != "dev" || request.Region != "ap-northeast-2" {
			t.Fatalf("unexpected request=%#v", request)
		}
		return testAWSSnapshotQueryResult(t, awsContext, request.Operation, values, at), nil
	})
	path := filepath.Join(t.TempDir(), "aws", "snapshot.db")
	syncService := &productionAWSSnapshotSyncService{
		core: core, groups: []awsbrowser.ContextGroup{{Name: "udg", Profiles: []string{"dev"}, Regions: []string{"ap-northeast-2"}}},
		path: path, now: func() time.Time { return at },
	}
	run, coverage, err := syncService.Sync(ctx, awsSnapshotSyncRequest{Group: "udg"})
	if err != nil || run.ID == "" || len(coverage) != 6 {
		t.Fatalf("run=%#v coverage=%#v error=%v", run, coverage, err)
	}
	readService := &productionAWSSnapshotReadService{path: path}
	execution, err := readService.Refs(ctx, awsSnapshotRefsRequest{Partition: "aws", AccountID: "123456789012", Region: "ap-northeast-2", Kind: "sg", ResourceID: "sg-target"})
	if err != nil || !execution.ResourceObserved || len(execution.Edges) != 2 || execution.Run.ID != run.ID {
		t.Fatalf("execution=%#v error=%v", execution, err)
	}
	types := []awsbrowser.RelationType{execution.Edges[0].Relation.Type, execution.Edges[1].Relation.Type}
	if !reflect.DeepEqual(types, []awsbrowser.RelationType{awsbrowser.RelationReferences, awsbrowser.RelationUses}) {
		t.Fatalf("relation types=%v", types)
	}
}

func TestAWSSnapshotRefsReadsPreviousRunWhileSyncCollects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aws", "snapshot.db")
	ctx := context.Background()
	store, _, err := snapshot.Open(ctx, path, 2)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 28, 6, 30, 0, 0, time.UTC)
	target := snapshot.ResourceRef{Partition: "aws", AccountID: "123456789012", Region: "ap-northeast-2", Type: "ec2.security-group", ID: "sg-123"}
	initial, err := store.CommitRun(ctx, snapshot.RunInput{
		StartedAt: at, CompletedAt: at.Add(time.Second),
		Resources:    []snapshot.Resource{{Ref: target, Name: "existing"}},
		Observations: []snapshot.Observation{{Resource: target, Profile: "dev", AccountID: target.AccountID, Region: target.Region, ObservedAt: at}},
		Coverage:     []snapshot.Coverage{{Profile: "dev", AccountID: target.AccountID, Region: target.Region, Service: "ec2-sg", Status: snapshot.CoverageSucceeded}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	core := awsSnapshotQueryCoreFunc(func(queryContext context.Context, _ awsintegration.Request) (awsintegration.Result, error) {
		started <- struct{}{}
		<-queryContext.Done()
		return awsintegration.Result{}, queryContext.Err()
	})
	syncService := &productionAWSSnapshotSyncService{
		core: core, groups: []awsbrowser.ContextGroup{{Name: "udg", Profiles: []string{"dev"}, Regions: []string{target.Region}}},
		path: path, now: func() time.Time { return at.Add(time.Minute) },
	}
	syncContext, cancelSync := context.WithCancel(ctx)
	syncDone := make(chan error, 1)
	go func() {
		_, _, syncErr := syncService.Sync(syncContext, awsSnapshotSyncRequest{Group: "udg"})
		syncDone <- syncErr
	}()
	<-started
	readContext, cancelRead := context.WithTimeout(ctx, time.Second)
	defer cancelRead()
	execution, err := (&productionAWSSnapshotReadService{path: path}).Refs(readContext, awsSnapshotRefsRequest{
		Partition: target.Partition, AccountID: target.AccountID, Region: target.Region, Kind: "sg", ResourceID: target.ID,
	})
	if err != nil || execution.Run.ID != initial.ID || !execution.ResourceObserved {
		t.Fatalf("execution=%#v error=%v", execution, err)
	}
	cancelSync()
	if err := <-syncDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("sync cancellation error=%v", err)
	}
}

func TestAWSSnapshotResourceNamePrefersNameTag(t *testing.T) {
	fields := map[string]any{"name": "default", "tags": map[string]string{"Name": "production-web"}}
	if got := awsSnapshotResourceName(fields); got != "production-web" {
		t.Fatalf("name=%q", got)
	}
}

func testAWSSnapshotObserved(t *testing.T, awsContext awsbrowser.AWSContext, key awsbrowser.ResourceKey, operation string, fields map[string]any, at time.Time) awsbrowser.ObservedResource {
	t.Helper()
	observation, err := awsbrowser.NewResourceObservationForOperation(awsContext, operation, fields, at, true)
	if err != nil {
		t.Fatal(err)
	}
	return awsbrowser.ObservedResource{Key: key, Observation: observation}
}

func testAWSSnapshotRelation(t *testing.T, awsContext awsbrowser.AWSContext, source, target awsbrowser.ResourceKey, relationType awsbrowser.RelationType, condition, operation string, at time.Time) awsbrowser.Relation {
	t.Helper()
	semantics, err := awsbrowser.NewRelationSemantics(relationType, awsbrowser.RelationOutgoing, condition)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := awsbrowser.NewRelationEvidence(awsbrowser.RelationIDExact, "fixture exact id", operation, awsContext.Region, at)
	if err != nil {
		t.Fatal(err)
	}
	relation, err := awsbrowser.NewRelation(source, target, semantics, evidence)
	if err != nil {
		t.Fatal(err)
	}
	return relation
}

func testAWSSnapshotMappedRelation(relation awsbrowser.Relation) map[string]any {
	evidence := relation.Evidence()[0]
	return map[string]any{
		"source": relation.Source, "target": relation.Target, "relation_type": string(relation.Semantics.Type),
		"direction": string(relation.Semantics.Direction), "condition": relation.Semantics.Condition,
		"kind": string(evidence.Kind), "reason": evidence.Reason, "operation": evidence.Operation,
		"scope": evidence.Scope, "observed_at": evidence.ObservedAt,
	}
}

func testAWSSnapshotQueryResult(t *testing.T, awsContext awsbrowser.AWSContext, operation string, resources []awsbrowser.ObservedResource, at time.Time) awsintegration.Result {
	t.Helper()
	key, err := awsbrowser.NewQueryKey(awsContext, awsbrowser.ProviderEC2, operation, nil)
	if err != nil {
		t.Fatal(err)
	}
	page, err := awsbrowser.NewQueryPage(0, resources, at, true)
	if err != nil {
		t.Fatal(err)
	}
	store := awsbrowser.NewSessionStore()
	if err := store.BeginLoad(key); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitPage(key, page); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteQuery(key, at); err != nil {
		t.Fatal(err)
	}
	querySnapshot, ok := store.Snapshot(key)
	if !ok {
		t.Fatal(errors.New("query snapshot missing"))
	}
	return awsintegration.Result{Update: awsintegration.Update{Key: &key, Snapshot: querySnapshot, Coverage: awsintegration.Coverage{ContextResolved: true, QueryStarted: true, Completed: true}}}
}
