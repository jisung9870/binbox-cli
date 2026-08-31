package bb

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"reflect"
	"strconv"
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

type traceIntentCoreFake struct {
	context       awsbrowser.AWSContext
	requests      []awsintegration.Request
	failOperation string
}

func (*traceIntentCoreFake) Subscribe(context.Context, awsintegration.Request) (*awsintegration.Subscription, error) {
	return nil, errors.New("unexpected Subscribe")
}

func (fake *traceIntentCoreFake) Resolve(context.Context, awsintegration.ContextRequest) (awsintegration.ContextResult, error) {
	copy := fake.context
	return awsintegration.ContextResult{Context: &copy}, nil
}

func (*traceIntentCoreFake) ListContexts(context.Context) ([]awsbrowser.ContextChoice, error) {
	return nil, nil
}

func (fake *traceIntentCoreFake) Query(_ context.Context, request awsintegration.Request) (awsintegration.Result, error) {
	fake.requests = append(fake.requests, request)
	key, err := awsbrowser.NewQueryKey(fake.context, request.Provider, request.Operation, request.Params)
	if err != nil {
		return awsintegration.Result{}, err
	}
	if request.Operation == fake.failOperation {
		failure := &awsintegration.Failure{
			State: awsbrowser.LoadForbidden, Kind: awsbrowser.ProviderForbidden,
			Provider: request.Provider, Operation: request.Operation, Code: "AccessDenied",
		}
		return awsintegration.Result{Update: awsintegration.Update{
			Key: &key, Snapshot: awsbrowser.QuerySnapshot{State: awsbrowser.LoadForbidden},
			Coverage: awsintegration.Coverage{ContextResolved: true, QueryStarted: true, Completed: true}, Failure: failure,
		}}, failure
	}
	store := awsbrowser.NewSessionStore()
	if err := store.Queue(key); err != nil {
		return awsintegration.Result{}, err
	}
	if err := store.BeginLoad(key); err != nil {
		return awsintegration.Result{}, err
	}
	when := time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC)
	var resourceType, resourceID string
	fields := map[string]any{}
	switch request.Operation {
	case awsbrowser.OperationDescribeLoadBalancers:
		resourceType = "elbv2.load-balancer"
		resourceID = "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/api/111"
		fields = map[string]any{"name": "api-alb", "state": "active"}
	case awsbrowser.OperationDescribeListeners:
		resourceType = "elbv2.listener"
		resourceID = "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener/app/api/111/222"
		fields = map[string]any{"name": "HTTPS 443"}
	}
	if resourceType != "" {
		resourceKey, keyErr := awsbrowser.NewRegionalResourceKey(fake.context, resourceType, resourceID)
		if keyErr != nil {
			return awsintegration.Result{}, keyErr
		}
		if request.Operation == awsbrowser.OperationDescribeListeners {
			loadBalancerKey, keyErr := awsbrowser.NewRegionalResourceKey(fake.context, "elbv2.load-balancer", "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/api/111")
			if keyErr != nil {
				return awsintegration.Result{}, keyErr
			}
			fields["relations"] = []any{map[string]any{
				"target": loadBalancerKey, "relation_type": "member-of", "direction": "outgoing", "kind": "api-exact",
			}}
		}
		observation, observationErr := awsbrowser.NewResourceObservationForOperation(fake.context, request.Operation, fields, when, true)
		if observationErr != nil {
			return awsintegration.Result{}, observationErr
		}
		page, pageErr := awsbrowser.NewQueryPage(0, []awsbrowser.ObservedResource{{Key: resourceKey, Observation: observation}}, when, true)
		if pageErr != nil {
			return awsintegration.Result{}, pageErr
		}
		if err := store.CommitPage(key, page); err != nil {
			return awsintegration.Result{}, err
		}
	}
	if err := store.CompleteQuery(key, when); err != nil {
		return awsintegration.Result{}, err
	}
	snapshot, _ := store.Snapshot(key)
	return awsintegration.Result{Update: awsintegration.Update{
		Key: &key, Snapshot: snapshot, Coverage: awsintegration.Coverage{ContextResolved: true, QueryStarted: true, Completed: true},
	}}, nil
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
	lbARN := "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/api/123"
	listenerARN := "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener/app/api/123/456"
	targetGroupARN := "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/api/789"
	targetsID := url.Values{"target-group-arn": []string{targetGroupARN}, "target-type": []string{"instance"}}.Encode()
	tests := []struct {
		target    string
		provider  string
		operation string
		params    map[string]string
	}{
		{"ec2-instances", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeInstances, nil},
		{"ec2-launch-templates", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeLaunchTemplates, nil},
		{"route53-hosted-zones", awsbrowser.ProviderRoute53, awsbrowser.OperationListHostedZones, nil},
		{"iam-roles", awsbrowser.ProviderIAM, awsbrowser.OperationListRoles, nil},
		{"vpc-networking", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeVpcs, nil},
		{"elbv2-load-balancers", awsbrowser.ProviderELBV2, awsbrowser.OperationDescribeLoadBalancers, nil},
		{"elbv2-application-load-balancers", awsbrowser.ProviderELBV2, awsbrowser.OperationDescribeLoadBalancers, map[string]string{"load-balancer-type": "application"}},
		{"elbv2-network-load-balancers", awsbrowser.ProviderELBV2, awsbrowser.OperationDescribeLoadBalancers, map[string]string{"load-balancer-type": "network"}},
		{"ec2.instance:i-123", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeInstances, map[string]string{"instance-id": "i-123"}},
		{"ec2.image:ami-123", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeImages, map[string]string{"image-id": "ami-123"}},
		{"ec2.volume:vol-123", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeVolumes, map[string]string{"volume-id": "vol-123"}},
		{"ec2.security-group:sg-123", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeSecurityGroups, map[string]string{"group-id": "sg-123"}},
		{"ec2.security-group-rule:sgr-123", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeSecurityGroupRules, map[string]string{"security-group-rule-id": "sgr-123"}},
		{"ec2.security-group-rules-inbound:sg-123", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeSecurityGroupRules, map[string]string{"direction": "ingress", "group-id": "sg-123"}},
		{"ec2.security-group-rules-outbound:sg-123", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeSecurityGroupRules, map[string]string{"direction": "egress", "group-id": "sg-123"}},
		{"ec2.vpc:vpc-123", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeVpcs, map[string]string{"vpc-id": "vpc-123"}},
		{"ec2.subnet:subnet-123", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeSubnets, map[string]string{"subnet-id": "subnet-123"}},
		{"ec2.route-table:rtb-123", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeRouteTables, map[string]string{"route-table-id": "rtb-123"}},
		{"ec2.vpc-peering-connection:pcx-123", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeVpcPeeringConnections, map[string]string{"vpc-peering-connection-id": "pcx-123"}},
		{"ec2.launch-template:lt-123", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeLaunchTemplates, map[string]string{"launch-template-id": "lt-123"}},
		{"ec2.launch-template-versions:lt-123", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeLaunchTemplateVersions, map[string]string{"launch-template-id": "lt-123"}},
		{"ec2.launch-template-version:lt-123/3", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeLaunchTemplateVersions, map[string]string{"launch-template-id": "lt-123", "version": "3"}},
		{"ec2.launch-template-user-data:lt-123/3", awsbrowser.ProviderEC2, awsbrowser.OperationDescribeLaunchTemplateVersions, map[string]string{"launch-template-id": "lt-123", "version": "3", "view": "user-data"}},
		{"iam.role:reader", awsbrowser.ProviderIAM, awsbrowser.OperationGetRole, map[string]string{"role-name": "reader"}},
		{"iam.role-attached-policies:reader", awsbrowser.ProviderIAM, awsbrowser.OperationListAttachedRolePolicies, map[string]string{"role-name": "reader"}},
		{"iam.role-inline-policies:reader", awsbrowser.ProviderIAM, awsbrowser.OperationListRolePolicies, map[string]string{"role-name": "reader"}},
		{"iam.instance-profile:worker", awsbrowser.ProviderIAM, awsbrowser.OperationGetInstanceProfile, map[string]string{"instance-profile-name": "worker"}},
		{"iam.managed-policy:arn:aws:iam::123456789012:policy/read", awsbrowser.ProviderIAM, awsbrowser.OperationGetPolicy, map[string]string{"policy-arn": "arn:aws:iam::123456789012:policy/read"}},
		{"iam.inline-policy:reader:inline", awsbrowser.ProviderIAM, awsbrowser.OperationGetRolePolicy, map[string]string{"role-name": "reader", "policy-name": "inline"}},
		{"iam.managed-policy-version:arn:aws:iam::123456789012:policy/read:v7", awsbrowser.ProviderIAM, awsbrowser.OperationGetPolicyVersion, map[string]string{"policy-arn": "arn:aws:iam::123456789012:policy/read", "version-id": "v7"}},
		{"hosted-zone:Z123", awsbrowser.ProviderRoute53, awsbrowser.OperationListResourceRecordSets, map[string]string{"hosted-zone-id": "Z123"}},
		{"route53.records:Z123", awsbrowser.ProviderRoute53, awsbrowser.OperationListResourceRecordSets, map[string]string{"hosted-zone-id": "Z123"}},
		{"cloudfront.distribution-domain:d24odq2ocbsmjd.cloudfront.net", awsbrowser.ProviderCloudFront, awsbrowser.OperationListDistributions, map[string]string{"distribution-domain": "d24odq2ocbsmjd.cloudfront.net"}},
		{"elbv2.load-balancer-dns:api-123.elb.us-east-1.amazonaws.com", awsbrowser.ProviderELBV2, awsbrowser.OperationDescribeLoadBalancers, map[string]string{"load-balancer-dns": "api-123.elb.us-east-1.amazonaws.com"}},
		{"elbv2.load-balancer:" + lbARN, awsbrowser.ProviderELBV2, awsbrowser.OperationDescribeLoadBalancers, map[string]string{"load-balancer-arn": lbARN}},
		{"elbv2.listeners:" + lbARN, awsbrowser.ProviderELBV2, awsbrowser.OperationDescribeListeners, map[string]string{"load-balancer-arn": lbARN}},
		{"elbv2.rules:" + listenerARN, awsbrowser.ProviderELBV2, awsbrowser.OperationDescribeRules, map[string]string{"listener-arn": listenerARN}},
		{"elbv2.target-group:" + targetGroupARN, awsbrowser.ProviderELBV2, awsbrowser.OperationDescribeTargetGroups, map[string]string{"target-group-arn": targetGroupARN}},
		{"elbv2.targets:" + targetsID, awsbrowser.ProviderELBV2, awsbrowser.OperationDescribeTargetHealth, map[string]string{"target-group-arn": targetGroupARN, "target-type": "instance"}},
		{"s3.bucket:udg-kr-game-binary", awsbrowser.ProviderS3, awsbrowser.OperationGetBucketLocation, map[string]string{"bucket": "udg-kr-game-binary"}},
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

func TestAWSTargetTraceCollectsLinearPathInOneStream(t *testing.T) {
	awsContext, err := awsbrowser.NewAWSContext(
		awsbrowser.ContextSpec{Mode: awsbrowser.ContextModeNamedProfile, Profile: "dev", Region: "us-east-1"},
		awsbrowser.VerifiedIdentity{Partition: "aws", AccountID: "123456789012", PrincipalARN: "arn:aws:sts::123456789012:assumed-role/ReadOnly/session", CredentialGeneration: 1},
		"ReadOnly",
	)
	if err != nil {
		t.Fatal(err)
	}
	core := &traceIntentCoreFake{context: awsContext}
	dispatcher := &awsIntentDispatcher{core: core}
	stream, err := dispatcher.Dispatch(context.Background(), awsbrowser.Intent{
		Kind: awsbrowser.IntentTrace, Target: "elbv2.load-balancer-dns:api-123.elb.us-east-1.amazonaws.com",
		Profile: "dev", Region: "us-east-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var final awsbrowser.IntentUpdate
	for update := range stream.Updates() {
		final = update
	}
	if !final.Done || final.Query.Snapshot.State != awsbrowser.LoadReady || len(final.Projection.Resources) != 3 {
		t.Fatalf("trace final=%+v", final)
	}
	if got := []string{core.requests[0].Operation, core.requests[1].Operation, core.requests[2].Operation}; !reflect.DeepEqual(got, []string{
		awsbrowser.OperationDescribeLoadBalancers, awsbrowser.OperationDescribeListeners, awsbrowser.OperationDescribeRules,
	}) {
		t.Fatalf("trace requests=%+v", core.requests)
	}
	for index, resource := range final.Projection.Resources {
		depth := ""
		for _, field := range resource.Fields {
			if field.Label == "Trace Depth" {
				depth = field.Value
			}
		}
		if depth != strconv.Itoa(index) {
			t.Fatalf("resource %d trace depth=%q projection=%+v", index, depth, resource)
		}
	}
}

func TestAWSTargetTraceKeepsAliasEvidenceWhenLiveReadIsDenied(t *testing.T) {
	awsContext, err := awsbrowser.NewAWSContext(
		awsbrowser.ContextSpec{Mode: awsbrowser.ContextModeNamedProfile, Profile: "lg-udg-ops", Region: "ap-northeast-2"},
		awsbrowser.VerifiedIdentity{Partition: "aws", AccountID: "306612189751", PrincipalARN: "arn:aws:sts::306612189751:assumed-role/common-ops/session", CredentialGeneration: 1},
		"common-ops",
	)
	if err != nil {
		t.Fatal(err)
	}
	core := &traceIntentCoreFake{context: awsContext, failOperation: awsbrowser.OperationDescribeLoadBalancers}
	dispatcher := &awsIntentDispatcher{core: core}
	target := "elbv2.load-balancer-dns:m-alb-udg-kr-pmm.elb.ap-northeast-2.amazonaws.com"
	stream, err := dispatcher.Dispatch(context.Background(), awsbrowser.Intent{
		Kind: awsbrowser.IntentTrace, Target: target, Profile: "lg-udg-ops", Region: "ap-northeast-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	var final awsbrowser.IntentUpdate
	for update := range stream.Updates() {
		final = update
	}
	if !final.Done || final.Query.Snapshot.State != awsbrowser.LoadForbidden || final.Query.Failure == nil || final.Query.Failure.Code != "AccessDenied" ||
		len(final.Projection.Resources) != 1 || final.Projection.Resources[0].Target != target {
		t.Fatalf("denied trace did not preserve root evidence: %+v", final)
	}
}

func TestAWSNavigableRelationContractHasRuntimeMapping(t *testing.T) {
	lbARN := "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/api/123"
	listenerARN := "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener/app/api/123/456"
	targetGroupARN := "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/api/789"
	tests := map[string]string{
		"ec2.instance": "i-1", "ec2.image": "ami-1", "ec2.volume": "vol-1", "ec2.security-group": "sg-1",
		"ec2.security-group-rule": "sgr-1", "ec2.security-group-rules-inbound": "sg-1",
		"ec2.security-group-rules-outbound": "sg-1", "ec2.vpc": "vpc-1", "ec2.subnet": "subnet-1",
		"ec2.route-table": "rtb-1", "ec2.vpc-peering-connection": "pcx-1", "iam.role": "reader", "iam.instance-profile": "worker",
		"iam.role-attached-policies": "reader", "iam.role-inline-policies": "reader",
		"iam.managed-policy":             "arn:aws:iam::123456789012:policy/read",
		"iam.inline-policy":              "reader:inline",
		"iam.managed-policy-version":     "arn:aws:iam::123456789012:policy/read:v1",
		"hosted-zone":                    "Z1",
		"route53.records":                "Z1",
		"cloudfront.distribution-domain": "d24odq2ocbsmjd.cloudfront.net",
		"elbv2.load-balancer-dns":        "api-123.elb.us-east-1.amazonaws.com",
		"elbv2.load-balancer":            lbARN,
		"elbv2.listeners":                lbARN,
		"elbv2.rules":                    listenerARN,
		"elbv2.target-group":             targetGroupARN,
		"elbv2.targets":                  url.Values{"target-group-arn": []string{targetGroupARN}, "target-type": []string{"instance"}}.Encode(),
		"s3.bucket":                      "udg-kr-game-binary",
	}
	for resourceType, id := range tests {
		if !awsbrowser.NavigableRelationTargetType(resourceType) {
			t.Fatalf("runtime-mapped type %q is not navigable", resourceType)
		}
		if _, ok := awsRequestForIntent(awsbrowser.Intent{Target: resourceType + ":" + id}); !ok {
			t.Fatalf("navigable type %q has no runtime mapping", resourceType)
		}
	}
	for _, resourceType := range []string{"ec2.gateway", "ec2.egress-only-internet-gateway", "ec2.carrier-gateway", "ec2.local-gateway", "ec2.nat-gateway", "ec2.network-interface", "ec2.transit-gateway", "networkmanager.core-network"} {
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
		{awsbrowser.Intent{SearchKind: "ami", Scope: "all", Query: "ami-0123456789abcdef0", Profile: "dev", Region: "us-east-1"}, awsintegration.SearchRequest{Kind: awsintegration.SearchAMI, Scope: awsintegration.SearchAll, Query: "ami-0123456789abcdef0", Profile: "dev", Region: "us-east-1"}},
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

func TestAWSMultiRegionRoutingAndAggregation(t *testing.T) {
	regions := []string{"ap-northeast-2", "ap-southeast-1", "us-east-1", "eu-central-1"}
	regionSet := strings.Join(regions, ",")
	for _, target := range []string{"ec2-instances", "ec2-launch-templates", "elbv2-load-balancers", "elbv2-application-load-balancers", "elbv2-network-load-balancers"} {
		regional, err := multiRegionIntentRegions(awsbrowser.Intent{Target: target, Region: regions[0], Regions: regionSet})
		if err != nil || !reflect.DeepEqual(regional, regions) {
			t.Fatalf("target=%s regional=%+v error=%v", target, regional, err)
		}
	}
	for _, target := range []string{"route53-hosted-zones", "iam-roles", "cloudfront.distribution-domain:example.cloudfront.net", "ec2.instance:i-1"} {
		got, err := multiRegionIntentRegions(awsbrowser.Intent{Target: target, Region: regions[0], Regions: regionSet})
		if err != nil || got != nil {
			t.Fatalf("global/exact target %q regions=%+v error=%v", target, got, err)
		}
	}

	newContext := func(region string) awsbrowser.AWSContext {
		awsContext, contextErr := awsbrowser.NewAWSContext(
			awsbrowser.ContextSpec{Mode: awsbrowser.ContextModeNamedProfile, Profile: "lg-udg-ops", Region: region},
			awsbrowser.VerifiedIdentity{Partition: "aws", AccountID: "123456789012", PrincipalARN: "arn:aws:sts::123456789012:assumed-role/ReadOnly/session", CredentialGeneration: 1},
			"ReadOnly",
		)
		if contextErr != nil {
			t.Fatal(contextErr)
		}
		return awsContext
	}
	kr, eu := newContext(regions[0]), newContext(regions[3])
	states := map[string]*regionIntentState{}
	for _, region := range regions {
		states[region] = &regionIntentState{done: true, query: awsbrowser.QueryUpdate{Snapshot: awsbrowser.QuerySnapshot{State: awsbrowser.LoadEmpty}}}
	}
	states[regions[0]].context = &kr
	states[regions[0]].projection = awsbrowser.IntentProjection{Resources: []awsbrowser.ResourceProjection{{Target: "ec2.instance:i-kr", Title: "kr-api", Context: &kr}}}
	states[regions[0]].query.Snapshot.State = awsbrowser.LoadReady
	states[regions[3]].context = &eu
	states[regions[3]].projection = awsbrowser.IntentProjection{Resources: []awsbrowser.ResourceProjection{{Target: "ec2.instance:i-eu", Title: "eu-api", Context: &eu}}}
	states[regions[3]].query = awsbrowser.QueryUpdate{
		Snapshot: awsbrowser.QuerySnapshot{State: awsbrowser.LoadForbidden},
		Failure:  &awsbrowser.ProviderFailure{State: awsbrowser.LoadForbidden, Kind: awsbrowser.ProviderForbidden},
	}
	aggregate := aggregateRegionIntent("lg-udg-ops", regions[0], regions, states)
	if !aggregate.Done || aggregate.Query.Snapshot.State != awsbrowser.LoadReady || aggregate.Context == nil || aggregate.Context.Region != regions[0] ||
		len(aggregate.Projection.Resources) != 2 || aggregate.Projection.Resources[1].Context.Region != regions[3] || aggregate.Coverage == nil || !aggregate.Coverage.Partial {
		t.Fatalf("aggregate=%+v", aggregate)
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

func TestAWSAppConstructionAndExplicitProfileBrowserHomeAreZeroCall(t *testing.T) {
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
	if err := app.Run([]string{"aws", "browse", "--profile", "dev"}); err != nil {
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
