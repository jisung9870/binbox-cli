package providers

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
)

type fakeEC2 struct {
	instances func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error)
	volumes   func(context.Context, *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error)
	groups    func(context.Context, *ec2.DescribeSecurityGroupsInput) (*ec2.DescribeSecurityGroupsOutput, error)
	rules     func(context.Context, *ec2.DescribeSecurityGroupRulesInput) (*ec2.DescribeSecurityGroupRulesOutput, error)
	vpcs      func(context.Context, *ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error)
	subnets   func(context.Context, *ec2.DescribeSubnetsInput) (*ec2.DescribeSubnetsOutput, error)
	routes    func(context.Context, *ec2.DescribeRouteTablesInput) (*ec2.DescribeRouteTablesOutput, error)
}

func (f *fakeEC2) DescribeInstances(c context.Context, i *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
	if f.instances == nil {
		panic("unexpected DescribeInstances")
	}
	return f.instances(c, i)
}
func (f *fakeEC2) DescribeVolumes(c context.Context, i *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error) {
	if f.volumes == nil {
		panic("unexpected DescribeVolumes")
	}
	return f.volumes(c, i)
}
func (f *fakeEC2) DescribeSecurityGroups(c context.Context, i *ec2.DescribeSecurityGroupsInput) (*ec2.DescribeSecurityGroupsOutput, error) {
	if f.groups == nil {
		panic("unexpected DescribeSecurityGroups")
	}
	return f.groups(c, i)
}
func (f *fakeEC2) DescribeSecurityGroupRules(c context.Context, i *ec2.DescribeSecurityGroupRulesInput) (*ec2.DescribeSecurityGroupRulesOutput, error) {
	if f.rules == nil {
		panic("unexpected DescribeSecurityGroupRules")
	}
	return f.rules(c, i)
}
func (f *fakeEC2) DescribeVpcs(c context.Context, i *ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error) {
	if f.vpcs == nil {
		panic("unexpected DescribeVpcs")
	}
	return f.vpcs(c, i)
}
func (f *fakeEC2) DescribeSubnets(c context.Context, i *ec2.DescribeSubnetsInput) (*ec2.DescribeSubnetsOutput, error) {
	if f.subnets == nil {
		panic("unexpected DescribeSubnets")
	}
	return f.subnets(c, i)
}
func (f *fakeEC2) DescribeRouteTables(c context.Context, i *ec2.DescribeRouteTablesInput) (*ec2.DescribeRouteTablesOutput, error) {
	if f.routes == nil {
		panic("unexpected DescribeRouteTables")
	}
	return f.routes(c, i)
}

type captureSink struct {
	pages     []awsbrowser.QueryPage
	completed int
	at        time.Time
	pageErr   error
}

func (s *captureSink) Page(p awsbrowser.QueryPage) error {
	if s.pageErr != nil {
		return s.pageErr
	}
	s.pages = append(s.pages, p)
	return nil
}
func (s *captureSink) Complete(at time.Time) error { s.completed++; s.at = at; return nil }

func providerContext(t *testing.T) awsbrowser.AWSContext {
	t.Helper()
	c, e := awsbrowser.NewAWSContext(awsbrowser.ContextSpec{Mode: awsbrowser.ContextModeNamedProfile, Profile: "dev", Region: "us-east-1"}, awsbrowser.VerifiedIdentity{Partition: "aws", AccountID: "123456789012", PrincipalARN: "arn:aws:iam::123456789012:user/test", CredentialGeneration: 1}, "")
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func providerKey(t *testing.T, op string, p map[string]string) awsbrowser.QueryKey {
	t.Helper()
	k, e := awsbrowser.NewQueryKey(providerContext(t), awsbrowser.ProviderEC2, op, p)
	if e != nil {
		t.Fatal(e)
	}
	return k
}
func fixedClock() func() time.Time {
	value := time.Date(2026, 8, 28, 1, 2, 3, 0, time.UTC)
	return func() time.Time { return value }
}

func TestEC2ExecutorSelectsOperationBuildsInputAndMapsRelations(t *testing.T) {
	var got *ec2.DescribeInstancesInput
	client := &fakeEC2{instances: func(_ context.Context, in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		got = in
		return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{Instances: []types.Instance{{
			InstanceId: aws.String("i-1"), VpcId: aws.String("vpc-1"), SubnetId: aws.String("subnet-1"), InstanceType: types.InstanceTypeT3Micro,
			State: &types.InstanceState{Name: types.InstanceStateNameRunning}, SecurityGroups: []types.GroupIdentifier{{GroupId: aws.String("sg-1")}},
			BlockDeviceMappings: []types.InstanceBlockDeviceMapping{{Ebs: &types.EbsInstanceBlockDevice{VolumeId: aws.String("vol-1")}}},
			IamInstanceProfile:  &types.IamInstanceProfile{Arn: aws.String("arn:aws:iam::123456789012:instance-profile/app/web-profile")},
			Tags:                []types.Tag{{Key: aws.String("Name"), Value: aws.String("web")}},
		}}}}}, nil
	}}
	executor, e := NewEC2QueryExecutor(client, fixedClock())
	if e != nil {
		t.Fatal(e)
	}
	sink := &captureSink{}
	if e = executor.Execute(context.Background(), providerKey(t, awsbrowser.OperationDescribeInstances, map[string]string{"vpc-id": "vpc-1", "instance-state-name": "running"}), sink); e != nil {
		t.Fatal(e)
	}
	if got == nil || got.MaxResults == nil || *got.MaxResults != 100 || len(got.Filters) != 2 || got.DryRun != nil || sink.completed != 1 || len(sink.pages) != 1 {
		t.Fatalf("input=%+v sink=%+v", got, sink)
	}
	resource := sink.pages[0].Resources()[0]
	if resource.Key.Type != "ec2.instance" || resource.Key.Region != "us-east-1" {
		t.Fatalf("key=%+v", resource.Key)
	}
	fields := resource.Observation.Fields()
	if fields["tags"].(map[string]string)["Name"] != "web" {
		t.Fatalf("fields=%+v", fields)
	}
	if fields["instance_profile_arn"] != "arn:aws:iam::123456789012:instance-profile/app/web-profile" || fields["instance_profile_name"] != "web-profile" {
		t.Fatalf("profile fields=%+v", fields)
	}
	wantTargets := map[string]string{
		"ec2.vpc/vpc-1":                    "us-east-1",
		"ec2.subnet/subnet-1":              "us-east-1",
		"ec2.security-group/sg-1":          "us-east-1",
		"ec2.volume/vol-1":                 "us-east-1",
		"iam.instance-profile/web-profile": awsbrowser.GlobalRegion,
	}
	if gotTargets := exactRelationTargets(t, resource); !reflect.DeepEqual(gotTargets, wantTargets) {
		t.Fatalf("relation targets=%+v want=%+v", gotTargets, wantTargets)
	}
	assertMappedOnly(t, fields)
}

func TestEC2ExecutorSelectivelyCallsAllFrozenOperations(t *testing.T) {
	tests := []struct {
		operation string
		client    *fakeEC2
		wantType  string
	}{
		{awsbrowser.OperationDescribeInstances, &fakeEC2{instances: func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
			return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{Instances: []types.Instance{{InstanceId: aws.String("i-op")}}}}}, nil
		}}, "ec2.instance"},
		{awsbrowser.OperationDescribeVolumes, &fakeEC2{volumes: func(context.Context, *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error) {
			return &ec2.DescribeVolumesOutput{Volumes: []types.Volume{{VolumeId: aws.String("vol-op")}}}, nil
		}}, "ec2.volume"},
		{awsbrowser.OperationDescribeSecurityGroups, &fakeEC2{groups: func(context.Context, *ec2.DescribeSecurityGroupsInput) (*ec2.DescribeSecurityGroupsOutput, error) {
			return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []types.SecurityGroup{{GroupId: aws.String("sg-op")}}}, nil
		}}, "ec2.security-group"},
		{awsbrowser.OperationDescribeSecurityGroupRules, &fakeEC2{rules: func(context.Context, *ec2.DescribeSecurityGroupRulesInput) (*ec2.DescribeSecurityGroupRulesOutput, error) {
			return &ec2.DescribeSecurityGroupRulesOutput{SecurityGroupRules: []types.SecurityGroupRule{{SecurityGroupRuleId: aws.String("sgr-op")}}}, nil
		}}, "ec2.security-group-rule"},
		{awsbrowser.OperationDescribeVpcs, &fakeEC2{vpcs: func(context.Context, *ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error) {
			return &ec2.DescribeVpcsOutput{Vpcs: []types.Vpc{{VpcId: aws.String("vpc-op")}}}, nil
		}}, "ec2.vpc"},
		{awsbrowser.OperationDescribeSubnets, &fakeEC2{subnets: func(context.Context, *ec2.DescribeSubnetsInput) (*ec2.DescribeSubnetsOutput, error) {
			return &ec2.DescribeSubnetsOutput{Subnets: []types.Subnet{{SubnetId: aws.String("subnet-op")}}}, nil
		}}, "ec2.subnet"},
		{awsbrowser.OperationDescribeRouteTables, &fakeEC2{routes: func(context.Context, *ec2.DescribeRouteTablesInput) (*ec2.DescribeRouteTablesOutput, error) {
			return &ec2.DescribeRouteTablesOutput{RouteTables: []types.RouteTable{{RouteTableId: aws.String("rtb-op")}}}, nil
		}}, "ec2.route-table"},
	}
	for _, test := range tests {
		t.Run(test.operation, func(t *testing.T) {
			executor, _ := NewEC2QueryExecutor(test.client, fixedClock())
			sink := &captureSink{}
			if err := executor.Execute(context.Background(), providerKey(t, test.operation, nil), sink); err != nil {
				t.Fatal(err)
			}
			if sink.completed != 1 || len(sink.pages) != 1 || sink.pages[0].Number != 0 || len(sink.pages[0].Resources()) != 1 || sink.pages[0].Resources()[0].Key.Type != test.wantType {
				t.Fatalf("sink=%+v", sink)
			}
		})
	}
}

func TestEC2ExecutorPaginatesSequentiallyAndCoordinatorRetainsSecondPage(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	client := &fakeEC2{instances: func(_ context.Context, in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			if in.NextToken != nil {
				return nil, errors.New("first cursor set")
			}
			return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{Instances: []types.Instance{{InstanceId: aws.String("i-1")}}}}, NextToken: aws.String("next")}, nil
		}
		if aws.ToString(in.NextToken) != "next" {
			return nil, errors.New("cursor missing")
		}
		return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{Instances: []types.Instance{{InstanceId: aws.String("i-2")}}}}}, nil
	}}
	executor, _ := NewEC2QueryExecutor(client, fixedClock())
	key := providerKey(t, awsbrowser.OperationDescribeInstances, nil)
	store := awsbrowser.NewSessionStore()
	coordinator, e := awsbrowser.NewQueryCoordinator(store, executor, 1)
	if e != nil {
		t.Fatal(e)
	}
	sub, e := coordinator.Subscribe(key)
	if e != nil {
		t.Fatal(e)
	}
	var final awsbrowser.QueryUpdate
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case update, ok := <-sub.Updates():
			if !ok {
				if final.Snapshot.State != awsbrowser.LoadReady || final.Snapshot.ResourceCount() != 2 || len(final.Snapshot.Pages()) != 2 {
					t.Fatalf("final=%+v pages=%d", final, len(final.Snapshot.Pages()))
				}
				return
			}
			final = update
		case <-timer.C:
			t.Fatal("coordinator timeout")
		}
	}
}

func TestEC2ExecutorRejectsCursorAndTargetFailuresWithoutCompleting(t *testing.T) {
	tests := []struct {
		name      string
		output    *ec2.DescribeVolumesOutput
		params    map[string]string
		wantPages int
	}{
		{"empty cursor", &ec2.DescribeVolumesOutput{NextToken: aws.String(" ")}, nil, 0},
		{"repeated cursor", &ec2.DescribeVolumesOutput{Volumes: []types.Volume{{VolumeId: aws.String("vol-invalid-current-page")}}, NextToken: aws.String("same")}, nil, 1},
		{"wrong target", &ec2.DescribeVolumesOutput{Volumes: []types.Volume{{VolumeId: aws.String("vol-other")}}}, map[string]string{"volume-id": "vol-want"}, 0},
		{"missing target", &ec2.DescribeVolumesOutput{}, map[string]string{"volume-id": "vol-want"}, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := &fakeEC2{volumes: func(_ context.Context, in *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error) {
				calls++
				if test.name == "repeated cursor" && calls == 1 {
					return &ec2.DescribeVolumesOutput{Volumes: []types.Volume{{VolumeId: aws.String("vol-valid-first-page")}}, NextToken: aws.String("same")}, nil
				}
				return test.output, nil
			}}
			executor, _ := NewEC2QueryExecutor(client, fixedClock())
			sink := &captureSink{}
			e := executor.Execute(context.Background(), providerKey(t, awsbrowser.OperationDescribeVolumes, test.params), sink)
			var provider *awsbrowser.ProviderError
			if !errors.As(e, &provider) || provider.Kind != awsbrowser.ProviderDecode || sink.completed != 0 || len(sink.pages) != test.wantPages {
				t.Fatalf("err=%v sink=%+v", e, sink)
			}
			if test.name == "repeated cursor" {
				resources := sink.pages[0].Resources()
				if len(resources) != 1 || resources[0].Key.ID != "vol-valid-first-page" {
					t.Fatalf("invalid repeated-cursor page escaped: %+v", resources)
				}
			}
		})
	}
}

func TestEC2ExecutorCancellationAndExternalSecurityGroupTargets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &fakeEC2{rules: func(_ context.Context, _ *ec2.DescribeSecurityGroupRulesInput) (*ec2.DescribeSecurityGroupRulesOutput, error) {
		cancel()
		return &ec2.DescribeSecurityGroupRulesOutput{SecurityGroupRules: []types.SecurityGroupRule{{SecurityGroupRuleId: aws.String("sgr-1"), GroupId: aws.String("sg-1"), CidrIpv4: aws.String("0.0.0.0/0"), ReferencedGroupInfo: &types.ReferencedSecurityGroup{GroupId: aws.String("sg-external"), UserId: aws.String("999999999999"), VpcId: aws.String("vpc-external")}}}}, nil
	}}
	executor, _ := NewEC2QueryExecutor(client, fixedClock())
	sink := &captureSink{}
	e := executor.Execute(ctx, providerKey(t, awsbrowser.OperationDescribeSecurityGroupRules, nil), sink)
	if !errors.Is(e, context.Canceled) || len(sink.pages) != 0 || sink.completed != 0 {
		t.Fatalf("err=%v sink=%+v", e, sink)
	}

	client.rules = func(context.Context, *ec2.DescribeSecurityGroupRulesInput) (*ec2.DescribeSecurityGroupRulesOutput, error) {
		return &ec2.DescribeSecurityGroupRulesOutput{SecurityGroupRules: []types.SecurityGroupRule{{SecurityGroupRuleId: aws.String("sgr-1"), GroupId: aws.String("sg-1"), CidrIpv4: aws.String("0.0.0.0/0"), ReferencedGroupInfo: &types.ReferencedSecurityGroup{GroupId: aws.String("sg-external"), UserId: aws.String("999999999999"), VpcId: aws.String("vpc-external")}}}}, nil
	}
	sink = &captureSink{}
	if e = executor.Execute(context.Background(), providerKey(t, awsbrowser.OperationDescribeSecurityGroupRules, nil), sink); e != nil {
		t.Fatal(e)
	}
	fields := sink.pages[0].Resources()[0].Observation.Fields()
	if fields["cidr_ipv4"] != "0.0.0.0/0" || len(fields["relations"].([]any)) != 1 || fields["usage_scope"] != "EC2 only" {
		t.Fatalf("fields=%+v", fields)
	}
}

func TestEC2ExecutorRejectsUnsafeInstanceProfileARNBeforeEmitting(t *testing.T) {
	client := &fakeEC2{instances: func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{Instances: []types.Instance{{
			InstanceId: aws.String("i-1"),
			IamInstanceProfile: &types.IamInstanceProfile{
				Arn: aws.String("arn:aws:iam::999999999999:instance-profile/external-profile"),
			},
		}}}}}, nil
	}}
	executor, _ := NewEC2QueryExecutor(client, fixedClock())
	sink := &captureSink{}
	err := executor.Execute(context.Background(), providerKey(t, awsbrowser.OperationDescribeInstances, nil), sink)
	var provider *awsbrowser.ProviderError
	if !errors.As(err, &provider) || provider.Kind != awsbrowser.ProviderDecode || len(sink.pages) != 0 || sink.completed != 0 {
		t.Fatalf("err=%v sink=%+v", err, sink)
	}
}

func TestEC2ExecutorMapsExpandedRouteTargetsAndExcludesLocalSentinel(t *testing.T) {
	coreARN := "arn:aws:networkmanager::123456789012:core-network/core-network-1"
	client := &fakeEC2{routes: func(context.Context, *ec2.DescribeRouteTablesInput) (*ec2.DescribeRouteTablesOutput, error) {
		return &ec2.DescribeRouteTablesOutput{RouteTables: []types.RouteTable{{
			RouteTableId: aws.String("rtb-1"), VpcId: aws.String("vpc-1"),
			Routes: []types.Route{
				{DestinationCidrBlock: aws.String("10.0.0.0/16"), GatewayId: aws.String("local")},
				{DestinationIpv6CidrBlock: aws.String("::/0"), EgressOnlyInternetGatewayId: aws.String("eigw-1")},
				{DestinationCidrBlock: aws.String("0.0.0.0/0"), CarrierGatewayId: aws.String("cagw-1")},
				{DestinationCidrBlock: aws.String("192.0.2.0/24"), LocalGatewayId: aws.String("lgw-1")},
				{DestinationCidrBlock: aws.String("198.51.100.0/24"), CoreNetworkArn: aws.String(coreARN)},
			},
		}}}, nil
	}}
	executor, _ := NewEC2QueryExecutor(client, fixedClock())
	sink := &captureSink{}
	if err := executor.Execute(context.Background(), providerKey(t, awsbrowser.OperationDescribeRouteTables, nil), sink); err != nil {
		t.Fatal(err)
	}
	resource := sink.pages[0].Resources()[0]
	wantTargets := map[string]string{
		"ec2.vpc/vpc-1": "us-east-1",
		"ec2.egress-only-internet-gateway/eigw-1": "us-east-1",
		"ec2.carrier-gateway/cagw-1":              "us-east-1",
		"ec2.local-gateway/lgw-1":                 "us-east-1",
		"networkmanager.core-network/" + coreARN:  awsbrowser.GlobalRegion,
	}
	if gotTargets := exactRelationTargets(t, resource); !reflect.DeepEqual(gotTargets, wantTargets) {
		t.Fatalf("relation targets=%+v want=%+v", gotTargets, wantTargets)
	}
	routes := resource.Observation.Fields()["routes"].([]any)
	if routes[0].(map[string]any)["gateway_id"] != "local" || routes[1].(map[string]any)["egress_only_internet_gateway_id"] != "eigw-1" ||
		routes[2].(map[string]any)["carrier_gateway_id"] != "cagw-1" || routes[3].(map[string]any)["local_gateway_id"] != "lgw-1" ||
		routes[4].(map[string]any)["core_network_arn"] != coreARN {
		t.Fatalf("routes=%+v", routes)
	}
}

func TestEC2ExecutorRejectsUnknownParamsBeforeCallingSDK(t *testing.T) {
	called := false
	client := &fakeEC2{vpcs: func(context.Context, *ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error) {
		called = true
		return nil, nil
	}}
	executor, _ := NewEC2QueryExecutor(client, fixedClock())
	sink := &captureSink{}
	e := executor.Execute(context.Background(), providerKey(t, awsbrowser.OperationDescribeVpcs, map[string]string{"dry-run": "true"}), sink)
	var provider *awsbrowser.ProviderError
	if !errors.As(e, &provider) || provider.Kind != awsbrowser.ProviderUnsupported || called || sink.completed != 0 {
		t.Fatalf("err=%v called=%v sink=%+v", e, called, sink)
	}
}

func exactRelationTargets(t *testing.T, resource awsbrowser.ObservedResource) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, raw := range resource.Observation.Fields()["relations"].([]any) {
		relation := raw.(map[string]any)
		if relation["kind"] != string(awsbrowser.RelationIDExact) {
			t.Fatalf("relation=%+v", relation)
		}
		if source := relation["source"].(awsbrowser.ResourceKey); source != resource.Key {
			t.Fatalf("relation source=%+v key=%+v", source, resource.Key)
		}
		target := relation["target"].(awsbrowser.ResourceKey)
		result[target.Type+"/"+target.ID] = relation["scope"].(string)
	}
	return result
}

func assertMappedOnly(t *testing.T, value any) {
	t.Helper()
	var walk func(any)
	walk = func(v any) {
		switch item := v.(type) {
		case nil, bool, string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, time.Time, awsbrowser.ResourceKey:
		case []string:
		case []awsbrowser.ResourceKey:
		case map[string]string:
		case []any:
			for _, child := range item {
				walk(child)
			}
		case map[string]any:
			for _, child := range item {
				walk(child)
			}
		default:
			t.Fatalf("raw or unsupported mapped value %T (%v), kind=%s", v, v, reflect.TypeOf(v).Kind())
		}
	}
	walk(value)
}

var _ awsbrowser.EC2API = (*fakeEC2)(nil)
