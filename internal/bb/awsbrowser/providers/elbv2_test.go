package providers

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
)

const (
	testLoadBalancerARN = "arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:loadbalancer/app/api-public/111"
	testListenerARN     = "arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:listener/app/api-public/111/222"
	testRuleARN         = "arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:listener-rule/app/api-public/111/222/333"
	testTargetGroupARN  = "arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:targetgroup/api-service/444"
)

type elbv2Fake struct {
	awsbrowser.ELBV2API
	describeLoadBalancers func(context.Context, *elasticloadbalancingv2.DescribeLoadBalancersInput) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error)
	describeListeners     func(context.Context, *elasticloadbalancingv2.DescribeListenersInput) (*elasticloadbalancingv2.DescribeListenersOutput, error)
	describeRules         func(context.Context, *elasticloadbalancingv2.DescribeRulesInput) (*elasticloadbalancingv2.DescribeRulesOutput, error)
	describeTargetGroups  func(context.Context, *elasticloadbalancingv2.DescribeTargetGroupsInput) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error)
	describeTargetHealth  func(context.Context, *elasticloadbalancingv2.DescribeTargetHealthInput) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error)
}

func (fake *elbv2Fake) DescribeLoadBalancers(ctx context.Context, input *elasticloadbalancingv2.DescribeLoadBalancersInput) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error) {
	return fake.describeLoadBalancers(ctx, input)
}
func (fake *elbv2Fake) DescribeListeners(ctx context.Context, input *elasticloadbalancingv2.DescribeListenersInput) (*elasticloadbalancingv2.DescribeListenersOutput, error) {
	return fake.describeListeners(ctx, input)
}
func (fake *elbv2Fake) DescribeRules(ctx context.Context, input *elasticloadbalancingv2.DescribeRulesInput) (*elasticloadbalancingv2.DescribeRulesOutput, error) {
	return fake.describeRules(ctx, input)
}
func (fake *elbv2Fake) DescribeTargetGroups(ctx context.Context, input *elasticloadbalancingv2.DescribeTargetGroupsInput) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error) {
	return fake.describeTargetGroups(ctx, input)
}
func (fake *elbv2Fake) DescribeTargetHealth(ctx context.Context, input *elasticloadbalancingv2.DescribeTargetHealthInput) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error) {
	return fake.describeTargetHealth(ctx, input)
}

func TestELBV2DomainTraceFixturePreservesRuleConditionsAndTargetTypes(t *testing.T) {
	now := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	instanceTargets := true
	fake := &elbv2Fake{}
	fake.describeLoadBalancers = func(_ context.Context, input *elasticloadbalancingv2.DescribeLoadBalancersInput) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error) {
		if len(input.LoadBalancerArns) != 0 || aws.ToInt32(input.PageSize) != elbv2PageSize {
			t.Fatalf("load balancer input=%+v", input)
		}
		return &elasticloadbalancingv2.DescribeLoadBalancersOutput{LoadBalancers: []types.LoadBalancer{{
			LoadBalancerArn: aws.String(testLoadBalancerARN), LoadBalancerName: aws.String("api-public"),
			DNSName: aws.String("api-public-123.elb.ap-northeast-2.amazonaws.com"), Type: types.LoadBalancerTypeEnumApplication,
			Scheme: types.LoadBalancerSchemeEnumInternetFacing, IpAddressType: types.IpAddressTypeIpv4,
			VpcId: aws.String("vpc-123"), SecurityGroups: []string{"sg-123"},
			AvailabilityZones: []types.AvailabilityZone{{ZoneName: aws.String("ap-northeast-2a"), SubnetId: aws.String("subnet-123")}},
			State:             &types.LoadBalancerState{Code: types.LoadBalancerStateEnumActive}, CreatedTime: aws.Time(now.Add(-time.Hour)),
		}}}, nil
	}
	fake.describeListeners = func(_ context.Context, input *elasticloadbalancingv2.DescribeListenersInput) (*elasticloadbalancingv2.DescribeListenersOutput, error) {
		if aws.ToString(input.LoadBalancerArn) != testLoadBalancerARN {
			t.Fatalf("listener input=%+v", input)
		}
		return &elasticloadbalancingv2.DescribeListenersOutput{Listeners: []types.Listener{{
			ListenerArn: aws.String(testListenerARN), LoadBalancerArn: aws.String(testLoadBalancerARN),
			Protocol: types.ProtocolEnumHttps, Port: aws.Int32(443), SslPolicy: aws.String("ELBSecurityPolicy-TLS13-1-2-2021-06"),
			DefaultActions: []types.Action{{Type: types.ActionTypeEnumForward, Order: aws.Int32(1), TargetGroupArn: aws.String(testTargetGroupARN)}},
		}}}, nil
	}
	fake.describeRules = func(_ context.Context, input *elasticloadbalancingv2.DescribeRulesInput) (*elasticloadbalancingv2.DescribeRulesOutput, error) {
		if aws.ToString(input.ListenerArn) != testListenerARN {
			t.Fatalf("rule input=%+v", input)
		}
		return &elasticloadbalancingv2.DescribeRulesOutput{Rules: []types.Rule{{
			RuleArn: aws.String(testRuleARN), Priority: aws.String("10"), IsDefault: aws.Bool(false),
			Conditions: []types.RuleCondition{
				{Field: aws.String("host-header"), HostHeaderConfig: &types.HostHeaderConditionConfig{Values: []string{"api.example.com"}}},
				{Field: aws.String("path-pattern"), PathPatternConfig: &types.PathPatternConditionConfig{Values: []string{"/v1/*"}}},
			},
			Actions: []types.Action{{Type: types.ActionTypeEnumForward, Order: aws.Int32(2), ForwardConfig: &types.ForwardActionConfig{
				TargetGroups: []types.TargetGroupTuple{{TargetGroupArn: aws.String(testTargetGroupARN), Weight: aws.Int32(80)}},
			}}},
		}}}, nil
	}
	fake.describeTargetGroups = func(_ context.Context, input *elasticloadbalancingv2.DescribeTargetGroupsInput) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error) {
		if len(input.TargetGroupArns) != 1 || input.TargetGroupArns[0] != testTargetGroupARN {
			t.Fatalf("target group input=%+v", input)
		}
		targetType := types.TargetTypeEnumInstance
		if !instanceTargets {
			targetType = types.TargetTypeEnumIp
		}
		return &elasticloadbalancingv2.DescribeTargetGroupsOutput{TargetGroups: []types.TargetGroup{{
			TargetGroupArn: aws.String(testTargetGroupARN), TargetGroupName: aws.String("api-service"), TargetType: targetType,
			Protocol: types.ProtocolEnumHttp, Port: aws.Int32(8080), VpcId: aws.String("vpc-123"),
			LoadBalancerArns: []string{testLoadBalancerARN}, HealthCheckEnabled: aws.Bool(true),
			HealthCheckProtocol: types.ProtocolEnumHttp, HealthCheckPort: aws.String("traffic-port"), HealthCheckPath: aws.String("/health"),
		}}}, nil
	}
	fake.describeTargetHealth = func(_ context.Context, input *elasticloadbalancingv2.DescribeTargetHealthInput) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error) {
		if aws.ToString(input.TargetGroupArn) != testTargetGroupARN {
			t.Fatalf("target health input=%+v", input)
		}
		targetID := "i-0123456789abcdef0"
		if !instanceTargets {
			targetID = "10.0.1.42"
		}
		return &elasticloadbalancingv2.DescribeTargetHealthOutput{TargetHealthDescriptions: []types.TargetHealthDescription{{
			Target:          &types.TargetDescription{Id: aws.String(targetID), Port: aws.Int32(8080), AvailabilityZone: aws.String("ap-northeast-2a")},
			HealthCheckPort: aws.String("8080"), TargetHealth: &types.TargetHealth{State: types.TargetHealthStateEnumHealthy},
		}}}, nil
	}

	executor, err := NewELBV2(fake, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	loadBalancer := executeELBV2Fixture(t, executor, awsbrowser.OperationDescribeLoadBalancers, map[string]string{"load-balancer-dns": "dualstack.api-public-123.elb.ap-northeast-2.amazonaws.com"})[0]
	if loadBalancer.Key.Type != "elbv2.load-balancer" || loadBalancer.Key.ID != testLoadBalancerARN {
		t.Fatalf("load balancer=%+v", loadBalancer.Key)
	}
	loadBalancerProjection := awsbrowser.ProjectResourceFields(loadBalancer.Key, loadBalancer.Observation.Fields())
	if loadBalancerProjection.Title != "api-public" || !hasProjectionTarget(loadBalancerProjection, "elbv2.listeners:"+testLoadBalancerARN) {
		t.Fatalf("projection=%+v", loadBalancerProjection)
	}

	listener := executeELBV2Fixture(t, executor, awsbrowser.OperationDescribeListeners, map[string]string{"load-balancer-arn": testLoadBalancerARN})[0]
	listenerProjection := awsbrowser.ProjectResourceFields(listener.Key, listener.Observation.Fields())
	if listenerProjection.Title != "HTTPS 443" || !hasProjectionTarget(listenerProjection, "elbv2.rules:"+testListenerARN) || !hasProjectionTarget(listenerProjection, "elbv2.target-group:"+testTargetGroupARN) {
		t.Fatalf("listener projection=%+v", listenerProjection)
	}

	rule := executeELBV2Fixture(t, executor, awsbrowser.OperationDescribeRules, map[string]string{"listener-arn": testListenerARN})[0]
	rawRuleRelation := rule.Observation.Fields()["relations"].([]any)[0].(map[string]any)
	rawSource, sourceOK := rawRuleRelation["source"].(awsbrowser.ResourceKey)
	rawTarget, targetOK := rawRuleRelation["target"].(awsbrowser.ResourceKey)
	if !sourceOK || !targetOK || rawSource != rule.Key || rawTarget.Type != "elbv2.target-group" || rawTarget.ID != testTargetGroupARN || rawRuleRelation["kind"] != string(awsbrowser.RelationAPIExact) {
		t.Fatalf("reverse evidence cannot be reconstructed: relation=%+v", rawRuleRelation)
	}
	ruleProjection := awsbrowser.ProjectResourceFields(rule.Key, rule.Observation.Fields())
	if len(ruleProjection.Relations) != 1 || ruleProjection.Relations[0].Target != "elbv2.target-group:"+testTargetGroupARN ||
		!strings.Contains(ruleProjection.Relations[0].Condition, "priority=10") || !strings.Contains(ruleProjection.Relations[0].Condition, "host-header=api.example.com") ||
		!strings.Contains(ruleProjection.Relations[0].Condition, "path-pattern=/v1/*") || !strings.Contains(ruleProjection.Relations[0].Condition, "weight=80") {
		t.Fatalf("rule projection=%+v", ruleProjection)
	}

	targetGroup := executeELBV2Fixture(t, executor, awsbrowser.OperationDescribeTargetGroups, map[string]string{"target-group-arn": testTargetGroupARN})[0]
	targetGroupProjection := awsbrowser.ProjectResourceFields(targetGroup.Key, targetGroup.Observation.Fields())
	targetsRelation := projectionTarget(targetGroupProjection, "elbv2.targets:")
	if targetsRelation == "" {
		t.Fatalf("target group projection=%+v", targetGroupProjection)
	}
	_, encodedTargets, _ := strings.Cut(targetsRelation, ":")
	params, err := url.ParseQuery(encodedTargets)
	if err != nil || params.Get("target-group-arn") != testTargetGroupARN || params.Get("target-type") != "instance" {
		t.Fatalf("target relation=%q params=%v error=%v", targetsRelation, params, err)
	}

	instance := executeELBV2Fixture(t, executor, awsbrowser.OperationDescribeTargetHealth, map[string]string{"target-group-arn": testTargetGroupARN, "target-type": "instance"})[0]
	instanceProjection := awsbrowser.ProjectResourceFields(instance.Key, instance.Observation.Fields())
	if instanceProjection.Title != "i-0123456789abcdef0" || !hasProjectionTarget(instanceProjection, "ec2.instance:i-0123456789abcdef0") {
		t.Fatalf("instance target projection=%+v", instanceProjection)
	}

	instanceTargets = false
	ipTarget := executeELBV2Fixture(t, executor, awsbrowser.OperationDescribeTargetHealth, map[string]string{"target-group-arn": testTargetGroupARN, "target-type": "ip"})[0]
	ipProjection := awsbrowser.ProjectResourceFields(ipTarget.Key, ipTarget.Observation.Fields())
	if ipProjection.Title != "10.0.1.42" || len(ipProjection.Relations) != 0 || !projectionFieldContains(ipProjection, "Resolution", "no EC2 instance inference") {
		t.Fatalf("IP target projection=%+v", ipProjection)
	}
}

func TestELBV2ExactLoadBalancerReadOmitsCatalogPagination(t *testing.T) {
	fake := &elbv2Fake{}
	fake.describeLoadBalancers = func(_ context.Context, input *elasticloadbalancingv2.DescribeLoadBalancersInput) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error) {
		if len(input.LoadBalancerArns) != 1 || input.LoadBalancerArns[0] != testLoadBalancerARN || input.PageSize != nil {
			t.Fatalf("exact load balancer input=%+v", input)
		}
		return &elasticloadbalancingv2.DescribeLoadBalancersOutput{LoadBalancers: []types.LoadBalancer{{
			LoadBalancerArn: aws.String(testLoadBalancerARN), LoadBalancerName: aws.String("api-public"),
			DNSName: aws.String("api-public-123.elb.ap-northeast-2.amazonaws.com"), Type: types.LoadBalancerTypeEnumApplication,
			State: &types.LoadBalancerState{Code: types.LoadBalancerStateEnumActive},
		}}}, nil
	}
	executor, err := NewELBV2(fake, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	resources := executeELBV2Fixture(t, executor, awsbrowser.OperationDescribeLoadBalancers, map[string]string{"load-balancer-arn": testLoadBalancerARN})
	if len(resources) != 1 || resources[0].Key.ID != testLoadBalancerARN {
		t.Fatalf("resources=%+v", resources)
	}
}

func TestELBV2CatalogListsAndFiltersALBAndNLB(t *testing.T) {
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	fake := &elbv2Fake{}
	fake.describeLoadBalancers = func(_ context.Context, input *elasticloadbalancingv2.DescribeLoadBalancersInput) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error) {
		if len(input.LoadBalancerArns) != 0 || len(input.Names) != 0 || aws.ToInt32(input.PageSize) != elbv2PageSize {
			t.Fatalf("catalog input=%+v", input)
		}
		loadBalancer := func(kind types.LoadBalancerTypeEnum, resource, name string) types.LoadBalancer {
			return types.LoadBalancer{
				LoadBalancerArn:  aws.String("arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:loadbalancer/" + resource),
				LoadBalancerName: aws.String(name), DNSName: aws.String(name + "-123.elb.ap-northeast-2.amazonaws.com"),
				Type: kind, Scheme: types.LoadBalancerSchemeEnumInternal, IpAddressType: types.IpAddressTypeIpv4,
				VpcId: aws.String("vpc-123"), State: &types.LoadBalancerState{Code: types.LoadBalancerStateEnumActive},
			}
		}
		return &elasticloadbalancingv2.DescribeLoadBalancersOutput{LoadBalancers: []types.LoadBalancer{
			loadBalancer(types.LoadBalancerTypeEnumApplication, "app/api/111", "api-alb"),
			loadBalancer(types.LoadBalancerTypeEnumNetwork, "net/internal/222", "internal-nlb"),
			loadBalancer(types.LoadBalancerTypeEnumGateway, "gwy/appliance/333", "appliance-gwlb"),
		}}, nil
	}
	executor, err := NewELBV2(fake, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		params map[string]string
		want   []string
	}{
		{name: "all ALB and NLB", want: []string{"application", "network"}},
		{name: "ALB only", params: map[string]string{"load-balancer-type": "application"}, want: []string{"application"}},
		{name: "NLB only", params: map[string]string{"load-balancer-type": "network"}, want: []string{"network"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resources := executeELBV2Fixture(t, executor, awsbrowser.OperationDescribeLoadBalancers, test.params)
			got := make([]string, 0, len(resources))
			for _, resource := range resources {
				got = append(got, resource.Observation.Fields()["type"].(string))
			}
			if strings.Join(got, ",") != strings.Join(test.want, ",") {
				t.Fatalf("types=%v want=%v", got, test.want)
			}
		})
	}
}

func TestELBV2RejectsCrossRegionARNAndMalformedTargetQuery(t *testing.T) {
	fake := &elbv2Fake{}
	executor, _ := NewELBV2(fake, time.Now)
	for _, test := range []struct {
		operation string
		params    map[string]string
	}{
		{awsbrowser.OperationDescribeLoadBalancers, map[string]string{"load-balancer-arn": strings.Replace(testLoadBalancerARN, "ap-northeast-2", "us-west-2", 1)}},
		{awsbrowser.OperationDescribeLoadBalancers, map[string]string{"load-balancer-dns": "not-an-elb.example.com"}},
		{awsbrowser.OperationDescribeLoadBalancers, map[string]string{"load-balancer-type": "gateway"}},
		{awsbrowser.OperationDescribeTargetHealth, map[string]string{"target-group-arn": testTargetGroupARN, "target-type": "container"}},
	} {
		key, err := awsbrowser.NewQueryKey(testIAMContext(t), awsbrowser.ProviderELBV2, test.operation, test.params)
		if err != nil {
			t.Fatal(err)
		}
		if err := executor.Execute(context.Background(), key, &collectingSink{}); err != awsbrowser.ErrInvalidQueryKey {
			t.Fatalf("operation=%s params=%v error=%v", test.operation, test.params, err)
		}
	}
}

func executeELBV2Fixture(t *testing.T, executor awsbrowser.QueryExecutor, operation string, params map[string]string) []awsbrowser.ObservedResource {
	t.Helper()
	key, err := awsbrowser.NewQueryKey(testIAMContext(t), awsbrowser.ProviderELBV2, operation, params)
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	if err := executor.Execute(context.Background(), key, sink); err != nil {
		t.Fatal(err)
	}
	if sink.completed != 1 || resourceCount(sink.pages) == 0 {
		t.Fatalf("operation=%s completed=%d resources=%d", operation, sink.completed, resourceCount(sink.pages))
	}
	resources := make([]awsbrowser.ObservedResource, 0, resourceCount(sink.pages))
	for _, page := range sink.pages {
		resources = append(resources, page.Resources()...)
	}
	return resources
}

func hasProjectionTarget(projection awsbrowser.ResourceProjection, target string) bool {
	for _, relation := range projection.Relations {
		if relation.Target == target {
			return true
		}
	}
	return false
}

func projectionTarget(projection awsbrowser.ResourceProjection, prefix string) string {
	for _, relation := range projection.Relations {
		if strings.HasPrefix(relation.Target, prefix) {
			return relation.Target
		}
	}
	return ""
}

func projectionFieldContains(projection awsbrowser.ResourceProjection, label, value string) bool {
	for _, field := range projection.Fields {
		if field.Label == label && strings.Contains(field.Value, value) {
			return true
		}
	}
	return false
}
