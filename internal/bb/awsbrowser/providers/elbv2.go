package providers

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsarn "github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
)

const elbv2PageSize int32 = 400

var ErrInvalidELBV2Executor = errors.New("ELBV2 query executor requires an API and clock")

type ELBV2QueryExecutor struct {
	api   awsbrowser.ELBV2API
	clock func() time.Time
}

func NewELBV2(api awsbrowser.ELBV2API, clock func() time.Time) (awsbrowser.QueryExecutor, error) {
	if api == nil || clock == nil {
		return nil, ErrInvalidELBV2Executor
	}
	return &ELBV2QueryExecutor{api: api, clock: clock}, nil
}

func (executor *ELBV2QueryExecutor) Execute(ctx context.Context, key awsbrowser.QueryKey, sink awsbrowser.QueryPageSink) error {
	if executor == nil || executor.api == nil || executor.clock == nil || ctx == nil || sink == nil {
		return ErrInvalidELBV2Executor
	}
	if key.Validate() != nil || key.Provider != awsbrowser.ProviderELBV2 {
		return awsbrowser.ErrInvalidProviderOperation
	}
	switch key.Operation {
	case awsbrowser.OperationDescribeLoadBalancers:
		return executor.describeLoadBalancers(ctx, key, sink)
	case awsbrowser.OperationDescribeListeners:
		return executor.describeListeners(ctx, key, sink)
	case awsbrowser.OperationDescribeRules:
		return executor.describeRules(ctx, key, sink)
	case awsbrowser.OperationDescribeTargetGroups:
		return executor.describeTargetGroups(ctx, key, sink)
	case awsbrowser.OperationDescribeTargetHealth:
		return executor.describeTargetHealth(ctx, key, sink)
	default:
		return awsbrowser.ErrInvalidProviderOperation
	}
}

func (executor *ELBV2QueryExecutor) describeLoadBalancers(ctx context.Context, key awsbrowser.QueryKey, sink awsbrowser.QueryPageSink) error {
	params, err := exactELBV2Params(key, []string{}, []string{"load-balancer-type"}, []string{"load-balancer-dns"}, []string{"load-balancer-arn"})
	if err != nil {
		return err
	}
	dnsName := canonicalELBV2DNS(params.Get("load-balancer-dns"))
	loadBalancerARN := params.Get("load-balancer-arn")
	loadBalancerType := params.Get("load-balancer-type")
	catalogQuery := len(params) == 0 || loadBalancerType != ""
	if loadBalancerType != "" && !validCatalogLoadBalancerType(loadBalancerType) {
		return awsbrowser.ErrInvalidQueryKey
	}
	if dnsName != "" {
		if region, ok := awsbrowser.ELBV2RegionFromDNS(key.Context.Partition, dnsName); !ok || region != key.Context.Region {
			return awsbrowser.ErrInvalidQueryKey
		}
	} else if !catalogQuery && !validELBV2ARN(key.Context, loadBalancerARN, "loadbalancer/") {
		return awsbrowser.ErrInvalidQueryKey
	}
	input := &elasticloadbalancingv2.DescribeLoadBalancersInput{PageSize: aws.Int32(elbv2PageSize)}
	if loadBalancerARN != "" {
		input.LoadBalancerArns = []string{loadBalancerARN}
	}
	seen := map[string]struct{}{}
	var pageNumber uint64
	for {
		output, callErr := executor.api.DescribeLoadBalancers(ctx, input)
		if callErr != nil {
			return awsbrowser.ClassifyProviderError(callErr, awsbrowser.ProviderELBV2, key.Operation)
		}
		if output == nil {
			return awsbrowser.ErrQueryDecode
		}
		fetchedAt := executor.clock().UTC()
		resources := make([]awsbrowser.ObservedResource, 0, len(output.LoadBalancers))
		for _, loadBalancer := range output.LoadBalancers {
			if dnsName != "" && canonicalELBV2DNS(aws.ToString(loadBalancer.DNSName)) != dnsName {
				continue
			}
			observedType := string(loadBalancer.Type)
			if catalogQuery && (!validCatalogLoadBalancerType(observedType) || loadBalancerType != "" && observedType != loadBalancerType) {
				continue
			}
			resource, mapErr := mapELBV2LoadBalancer(key, loadBalancer, fetchedAt)
			if mapErr != nil {
				return mapErr
			}
			resources = append(resources, resource)
		}
		if err := emitELBV2Page(sink, pageNumber, resources, fetchedAt); err != nil {
			return err
		}
		pageNumber++
		marker, more, markerErr := nextELBV2Marker(output.NextMarker, seen)
		if markerErr != nil {
			return markerErr
		}
		if !more {
			return sink.Complete(fetchedAt)
		}
		input.Marker = aws.String(marker)
	}
}

func (executor *ELBV2QueryExecutor) describeListeners(ctx context.Context, key awsbrowser.QueryKey, sink awsbrowser.QueryPageSink) error {
	params, err := exactELBV2Params(key, []string{"load-balancer-arn"})
	if err != nil || !validELBV2ARN(key.Context, params.Get("load-balancer-arn"), "loadbalancer/") {
		return awsbrowser.ErrInvalidQueryKey
	}
	input := &elasticloadbalancingv2.DescribeListenersInput{LoadBalancerArn: aws.String(params.Get("load-balancer-arn")), PageSize: aws.Int32(elbv2PageSize)}
	seen := map[string]struct{}{}
	var pageNumber uint64
	for {
		output, callErr := executor.api.DescribeListeners(ctx, input)
		if callErr != nil {
			return awsbrowser.ClassifyProviderError(callErr, awsbrowser.ProviderELBV2, key.Operation)
		}
		if output == nil {
			return awsbrowser.ErrQueryDecode
		}
		fetchedAt := executor.clock().UTC()
		resources := make([]awsbrowser.ObservedResource, 0, len(output.Listeners))
		for _, listener := range output.Listeners {
			resource, mapErr := mapELBV2Listener(key, listener, fetchedAt)
			if mapErr != nil {
				return mapErr
			}
			resources = append(resources, resource)
		}
		if err := emitELBV2Page(sink, pageNumber, resources, fetchedAt); err != nil {
			return err
		}
		pageNumber++
		marker, more, markerErr := nextELBV2Marker(output.NextMarker, seen)
		if markerErr != nil {
			return markerErr
		}
		if !more {
			return sink.Complete(fetchedAt)
		}
		input.Marker = aws.String(marker)
	}
}

func (executor *ELBV2QueryExecutor) describeRules(ctx context.Context, key awsbrowser.QueryKey, sink awsbrowser.QueryPageSink) error {
	params, err := exactELBV2Params(key, []string{"listener-arn"})
	if err != nil || !validELBV2ARN(key.Context, params.Get("listener-arn"), "listener/") {
		return awsbrowser.ErrInvalidQueryKey
	}
	input := &elasticloadbalancingv2.DescribeRulesInput{ListenerArn: aws.String(params.Get("listener-arn")), PageSize: aws.Int32(elbv2PageSize)}
	seen := map[string]struct{}{}
	var pageNumber uint64
	for {
		output, callErr := executor.api.DescribeRules(ctx, input)
		if callErr != nil {
			return awsbrowser.ClassifyProviderError(callErr, awsbrowser.ProviderELBV2, key.Operation)
		}
		if output == nil {
			return awsbrowser.ErrQueryDecode
		}
		fetchedAt := executor.clock().UTC()
		resources := make([]awsbrowser.ObservedResource, 0, len(output.Rules))
		for _, rule := range output.Rules {
			resource, mapErr := mapELBV2Rule(key, rule, fetchedAt)
			if mapErr != nil {
				return mapErr
			}
			resources = append(resources, resource)
		}
		if err := emitELBV2Page(sink, pageNumber, resources, fetchedAt); err != nil {
			return err
		}
		pageNumber++
		marker, more, markerErr := nextELBV2Marker(output.NextMarker, seen)
		if markerErr != nil {
			return markerErr
		}
		if !more {
			return sink.Complete(fetchedAt)
		}
		input.Marker = aws.String(marker)
	}
}

func (executor *ELBV2QueryExecutor) describeTargetGroups(ctx context.Context, key awsbrowser.QueryKey, sink awsbrowser.QueryPageSink) error {
	params, err := exactELBV2Params(key, []string{"target-group-arn"})
	if err != nil || !validELBV2ARN(key.Context, params.Get("target-group-arn"), "targetgroup/") {
		return awsbrowser.ErrInvalidQueryKey
	}
	input := &elasticloadbalancingv2.DescribeTargetGroupsInput{TargetGroupArns: []string{params.Get("target-group-arn")}, PageSize: aws.Int32(elbv2PageSize)}
	seen := map[string]struct{}{}
	var pageNumber uint64
	for {
		output, callErr := executor.api.DescribeTargetGroups(ctx, input)
		if callErr != nil {
			return awsbrowser.ClassifyProviderError(callErr, awsbrowser.ProviderELBV2, key.Operation)
		}
		if output == nil {
			return awsbrowser.ErrQueryDecode
		}
		fetchedAt := executor.clock().UTC()
		resources := make([]awsbrowser.ObservedResource, 0, len(output.TargetGroups))
		for _, targetGroup := range output.TargetGroups {
			resource, mapErr := mapELBV2TargetGroup(key, targetGroup, fetchedAt)
			if mapErr != nil {
				return mapErr
			}
			resources = append(resources, resource)
		}
		if err := emitELBV2Page(sink, pageNumber, resources, fetchedAt); err != nil {
			return err
		}
		pageNumber++
		marker, more, markerErr := nextELBV2Marker(output.NextMarker, seen)
		if markerErr != nil {
			return markerErr
		}
		if !more {
			return sink.Complete(fetchedAt)
		}
		input.Marker = aws.String(marker)
	}
}

func (executor *ELBV2QueryExecutor) describeTargetHealth(ctx context.Context, key awsbrowser.QueryKey, sink awsbrowser.QueryPageSink) error {
	params, err := exactELBV2Params(key, []string{"target-group-arn", "target-type"})
	if err != nil || !validELBV2ARN(key.Context, params.Get("target-group-arn"), "targetgroup/") || !validTargetType(params.Get("target-type")) {
		return awsbrowser.ErrInvalidQueryKey
	}
	output, callErr := executor.api.DescribeTargetHealth(ctx, &elasticloadbalancingv2.DescribeTargetHealthInput{TargetGroupArn: aws.String(params.Get("target-group-arn"))})
	if callErr != nil {
		return awsbrowser.ClassifyProviderError(callErr, awsbrowser.ProviderELBV2, key.Operation)
	}
	if output == nil {
		return awsbrowser.ErrQueryDecode
	}
	fetchedAt := executor.clock().UTC()
	resources := make([]awsbrowser.ObservedResource, 0, len(output.TargetHealthDescriptions))
	for _, target := range output.TargetHealthDescriptions {
		resource, mapErr := mapELBV2Target(key, params.Get("target-group-arn"), params.Get("target-type"), target, fetchedAt)
		if mapErr != nil {
			return mapErr
		}
		resources = append(resources, resource)
	}
	if err := emitELBV2Page(sink, 0, resources, fetchedAt); err != nil {
		return err
	}
	return sink.Complete(fetchedAt)
}

func mapELBV2LoadBalancer(key awsbrowser.QueryKey, value types.LoadBalancer, fetchedAt time.Time) (awsbrowser.ObservedResource, error) {
	loadBalancerARN := strings.TrimSpace(aws.ToString(value.LoadBalancerArn))
	name := strings.TrimSpace(aws.ToString(value.LoadBalancerName))
	dnsName := canonicalELBV2DNS(aws.ToString(value.DNSName))
	if name == "" || dnsName == "" || !validELBV2ARN(key.Context, loadBalancerARN, "loadbalancer/") || value.State == nil {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	resourceKey, err := awsbrowser.NewRegionalResourceKey(key.Context, "elbv2.load-balancer", loadBalancerARN)
	if err != nil {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	zones := make([]any, 0, len(value.AvailabilityZones))
	relations := make([]any, 0, 1+len(value.SecurityGroups)+len(value.AvailabilityZones))
	for _, zone := range value.AvailabilityZones {
		zoneName, subnetID := aws.ToString(zone.ZoneName), aws.ToString(zone.SubnetId)
		zones = append(zones, map[string]any{"availability_zone": zoneName, "subnet_id": subnetID})
		if subnetID != "" {
			target, targetErr := awsbrowser.NewRegionalResourceKey(key.Context, "ec2.subnet", subnetID)
			if targetErr != nil {
				return awsbrowser.ObservedResource{}, targetErr
			}
			relations = append(relations, exactELBV2Relation(resourceKey, target, awsbrowser.RelationAssociatedWith, zoneName, key.Operation, "load-balancer-availability-zone", fetchedAt))
		}
	}
	if vpcID := strings.TrimSpace(aws.ToString(value.VpcId)); vpcID != "" {
		target, targetErr := awsbrowser.NewRegionalResourceKey(key.Context, "ec2.vpc", vpcID)
		if targetErr != nil {
			return awsbrowser.ObservedResource{}, targetErr
		}
		relations = append(relations, exactELBV2Relation(resourceKey, target, awsbrowser.RelationMemberOf, "", key.Operation, "load-balancer-vpc", fetchedAt))
	}
	for _, groupID := range value.SecurityGroups {
		target, targetErr := awsbrowser.NewRegionalResourceKey(key.Context, "ec2.security-group", groupID)
		if targetErr != nil {
			return awsbrowser.ObservedResource{}, targetErr
		}
		relations = append(relations, exactELBV2Relation(resourceKey, target, awsbrowser.RelationUses, "", key.Operation, "load-balancer-security-group", fetchedAt))
	}
	fields := map[string]any{
		"name": name, "arn": loadBalancerARN, "dns_name": dnsName, "type": string(value.Type),
		"scheme": string(value.Scheme), "ip_address_type": string(value.IpAddressType),
		"state": string(value.State.Code), "vpc_id": aws.ToString(value.VpcId),
		"canonical_hosted_zone_id": aws.ToString(value.CanonicalHostedZoneId),
		"security_groups":          append([]string(nil), value.SecurityGroups...), "availability_zones": zones,
		"relations": relations,
	}
	if value.CreatedTime != nil {
		fields["created_time"] = value.CreatedTime.UTC()
	}
	return observedELBV2(key, resourceKey, fields, fetchedAt)
}

func mapELBV2Listener(key awsbrowser.QueryKey, value types.Listener, fetchedAt time.Time) (awsbrowser.ObservedResource, error) {
	listenerARN := strings.TrimSpace(aws.ToString(value.ListenerArn))
	loadBalancerARN := strings.TrimSpace(aws.ToString(value.LoadBalancerArn))
	if !validELBV2ARN(key.Context, listenerARN, "listener/") || !validELBV2ARN(key.Context, loadBalancerARN, "loadbalancer/") || value.Port == nil {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	resourceKey, err := awsbrowser.NewRegionalResourceKey(key.Context, "elbv2.listener", listenerARN)
	if err != nil {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	loadBalancerKey, err := awsbrowser.NewRegionalResourceKey(key.Context, "elbv2.load-balancer", loadBalancerARN)
	if err != nil {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	relations := []any{exactELBV2Relation(resourceKey, loadBalancerKey, awsbrowser.RelationMemberOf, "", key.Operation, "listener-load-balancer", fetchedAt)}
	actions, actionRelations, err := mapELBV2Actions(key, resourceKey, value.DefaultActions, "default", fetchedAt)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	relations = append(relations, actionRelations...)
	fields := map[string]any{
		"name": fmt.Sprintf("%s %d", value.Protocol, aws.ToInt32(value.Port)), "arn": listenerARN,
		"load_balancer_arn": loadBalancerARN, "protocol": string(value.Protocol), "port": aws.ToInt32(value.Port),
		"ssl_policy": aws.ToString(value.SslPolicy), "alpn_policy": append([]string(nil), value.AlpnPolicy...),
		"default_actions": actions, "relations": relations,
	}
	return observedELBV2(key, resourceKey, fields, fetchedAt)
}

func mapELBV2Rule(key awsbrowser.QueryKey, value types.Rule, fetchedAt time.Time) (awsbrowser.ObservedResource, error) {
	ruleARN := strings.TrimSpace(aws.ToString(value.RuleArn))
	priority := strings.TrimSpace(aws.ToString(value.Priority))
	if !validELBV2ARN(key.Context, ruleARN, "listener-rule/") || priority == "" || value.IsDefault == nil {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	resourceKey, err := awsbrowser.NewRegionalResourceKey(key.Context, "elbv2.listener-rule", ruleARN)
	if err != nil {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	conditions := make([]any, 0, len(value.Conditions))
	conditionLabels := make([]string, 0, len(value.Conditions))
	for _, condition := range value.Conditions {
		mapped, label, mapErr := mapELBV2RuleCondition(condition)
		if mapErr != nil {
			return awsbrowser.ObservedResource{}, mapErr
		}
		conditions = append(conditions, mapped)
		if label != "" {
			conditionLabels = append(conditionLabels, label)
		}
	}
	routingCondition := "priority=" + priority
	if len(conditionLabels) != 0 {
		routingCondition += "; " + strings.Join(conditionLabels, "; ")
	}
	actions, relations, err := mapELBV2Actions(key, resourceKey, value.Actions, routingCondition, fetchedAt)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	fields := map[string]any{
		"name": "Priority " + priority, "arn": ruleARN, "priority": priority, "is_default": aws.ToBool(value.IsDefault),
		"routing_condition": routingCondition, "conditions": conditions, "actions": actions, "relations": relations,
	}
	return observedELBV2(key, resourceKey, fields, fetchedAt)
}

func mapELBV2TargetGroup(key awsbrowser.QueryKey, value types.TargetGroup, fetchedAt time.Time) (awsbrowser.ObservedResource, error) {
	targetGroupARN := strings.TrimSpace(aws.ToString(value.TargetGroupArn))
	name := strings.TrimSpace(aws.ToString(value.TargetGroupName))
	targetType := string(value.TargetType)
	if name == "" || !validELBV2ARN(key.Context, targetGroupARN, "targetgroup/") || !validTargetType(targetType) {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	resourceKey, err := awsbrowser.NewRegionalResourceKey(key.Context, "elbv2.target-group", targetGroupARN)
	if err != nil {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	relations := make([]any, 0, len(value.LoadBalancerArns))
	for _, loadBalancerARN := range value.LoadBalancerArns {
		if !validELBV2ARN(key.Context, loadBalancerARN, "loadbalancer/") {
			return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
		}
		target, targetErr := awsbrowser.NewRegionalResourceKey(key.Context, "elbv2.load-balancer", loadBalancerARN)
		if targetErr != nil {
			return awsbrowser.ObservedResource{}, targetErr
		}
		relations = append(relations, exactELBV2Relation(resourceKey, target, awsbrowser.RelationAssociatedWith, "", key.Operation, "target-group-load-balancer", fetchedAt))
	}
	fields := map[string]any{
		"name": name, "arn": targetGroupARN, "target_type": targetType, "protocol": string(value.Protocol),
		"protocol_version": aws.ToString(value.ProtocolVersion), "vpc_id": aws.ToString(value.VpcId),
		"ip_address_type": string(value.IpAddressType), "health_check_enabled": aws.ToBool(value.HealthCheckEnabled),
		"health_check_protocol": string(value.HealthCheckProtocol), "health_check_port": aws.ToString(value.HealthCheckPort),
		"health_check_path": aws.ToString(value.HealthCheckPath), "relations": relations,
	}
	if value.Port != nil {
		fields["port"] = aws.ToInt32(value.Port)
	}
	return observedELBV2(key, resourceKey, fields, fetchedAt)
}

func mapELBV2Target(key awsbrowser.QueryKey, targetGroupARN, targetType string, value types.TargetHealthDescription, fetchedAt time.Time) (awsbrowser.ObservedResource, error) {
	if value.Target == nil || value.TargetHealth == nil {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	targetID := strings.TrimSpace(aws.ToString(value.Target.Id))
	if targetID == "" {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	identity := url.Values{"target-group-arn": []string{targetGroupARN}, "target-id": []string{targetID}}
	if value.Target.Port != nil {
		identity.Set("port", strconv.FormatInt(int64(aws.ToInt32(value.Target.Port)), 10))
	}
	if availabilityZone := strings.TrimSpace(aws.ToString(value.Target.AvailabilityZone)); availabilityZone != "" {
		identity.Set("availability-zone", availabilityZone)
	}
	resourceKey, err := awsbrowser.NewRegionalResourceKey(key.Context, "elbv2.target", identity.Encode())
	if err != nil {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	fields := map[string]any{
		"target_id": targetID, "target_type": targetType, "target_group_arn": targetGroupARN,
		"availability_zone": aws.ToString(value.Target.AvailabilityZone),
		"health_check_port": aws.ToString(value.HealthCheckPort), "health_state": string(value.TargetHealth.State),
		"health_reason": string(value.TargetHealth.Reason), "health_description": aws.ToString(value.TargetHealth.Description),
	}
	if value.Target.Port != nil {
		fields["port"] = aws.ToInt32(value.Target.Port)
	}
	var targetKey awsbrowser.ResourceKey
	switch targetType {
	case "instance":
		targetKey, err = awsbrowser.NewRegionalResourceKey(key.Context, "ec2.instance", targetID)
	case "alb":
		targetKey, err = awsbrowser.NewRegionalResourceKey(key.Context, "elbv2.load-balancer", targetID)
	case "ip":
		fields["resolution"] = "IP target; no EC2 instance inference"
	case "lambda":
		fields["resolution"] = "Lambda target; Lambda detail provider is not implemented"
	}
	if err != nil {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	if targetKey.Validate() == nil {
		condition := "target-type=" + targetType
		if value.Target.Port != nil {
			condition += "; port=" + strconv.FormatInt(int64(aws.ToInt32(value.Target.Port)), 10)
		}
		fields["relations"] = []any{exactELBV2Relation(resourceKey, targetKey, awsbrowser.RelationRoutesTo, condition, key.Operation, "registered-target-returned-by-api", fetchedAt)}
	}
	return observedELBV2(key, resourceKey, fields, fetchedAt)
}

func mapELBV2Actions(key awsbrowser.QueryKey, source awsbrowser.ResourceKey, actions []types.Action, condition string, fetchedAt time.Time) ([]any, []any, error) {
	mapped := make([]any, 0, len(actions))
	relations := make([]any, 0, len(actions))
	seenTargets := map[string]struct{}{}
	for _, action := range actions {
		order := aws.ToInt32(action.Order)
		detail := map[string]any{"type": string(action.Type), "order": order}
		targets := make([]struct {
			arn    string
			weight int32
		}, 0, 1)
		if targetGroupARN := strings.TrimSpace(aws.ToString(action.TargetGroupArn)); targetGroupARN != "" {
			detail["target_group_arn"] = targetGroupARN
			targets = append(targets, struct {
				arn    string
				weight int32
			}{targetGroupARN, 0})
		}
		if action.ForwardConfig != nil {
			forwardTargets := make([]any, 0, len(action.ForwardConfig.TargetGroups))
			for _, tuple := range action.ForwardConfig.TargetGroups {
				targetGroupARN := strings.TrimSpace(aws.ToString(tuple.TargetGroupArn))
				weight := aws.ToInt32(tuple.Weight)
				forwardTargets = append(forwardTargets, map[string]any{"target_group_arn": targetGroupARN, "weight": weight})
				targets = append(targets, struct {
					arn    string
					weight int32
				}{targetGroupARN, weight})
			}
			detail["forward_targets"] = forwardTargets
		}
		mapped = append(mapped, detail)
		for _, target := range targets {
			if _, duplicate := seenTargets[target.arn]; duplicate {
				continue
			}
			if !validELBV2ARN(key.Context, target.arn, "targetgroup/") {
				return nil, nil, awsbrowser.ErrQueryDecode
			}
			seenTargets[target.arn] = struct{}{}
			targetKey, err := awsbrowser.NewRegionalResourceKey(key.Context, "elbv2.target-group", target.arn)
			if err != nil {
				return nil, nil, err
			}
			actionCondition := condition
			if order != 0 {
				actionCondition += "; action-order=" + strconv.FormatInt(int64(order), 10)
			}
			if target.weight != 0 {
				actionCondition += "; weight=" + strconv.FormatInt(int64(target.weight), 10)
			}
			relation := exactELBV2Relation(source, targetKey, awsbrowser.RelationRoutesTo, actionCondition, key.Operation, "forward-action-target-group", fetchedAt)
			relation["label"] = targetGroupLabel(target.arn)
			relations = append(relations, relation)
		}
	}
	return mapped, relations, nil
}

func mapELBV2RuleCondition(condition types.RuleCondition) (map[string]any, string, error) {
	field := strings.TrimSpace(aws.ToString(condition.Field))
	if field == "" {
		return nil, "", awsbrowser.ErrQueryDecode
	}
	values := append([]string(nil), condition.Values...)
	regexValues := append([]string(nil), condition.RegexValues...)
	detail := map[string]any{"field": field}
	if condition.HostHeaderConfig != nil {
		values = append(values, condition.HostHeaderConfig.Values...)
		regexValues = append(regexValues, condition.HostHeaderConfig.RegexValues...)
	}
	if condition.PathPatternConfig != nil {
		values = append(values, condition.PathPatternConfig.Values...)
		regexValues = append(regexValues, condition.PathPatternConfig.RegexValues...)
	}
	if condition.HttpRequestMethodConfig != nil {
		values = append(values, condition.HttpRequestMethodConfig.Values...)
	}
	if condition.SourceIpConfig != nil {
		values = append(values, condition.SourceIpConfig.Values...)
		if condition.SourceIpConfig.IpAddressType != "" {
			values = append(values, string(condition.SourceIpConfig.IpAddressType))
		}
	}
	if condition.HttpHeaderConfig != nil {
		detail["http_header_name"] = aws.ToString(condition.HttpHeaderConfig.HttpHeaderName)
		values = append(values, condition.HttpHeaderConfig.Values...)
		regexValues = append(regexValues, condition.HttpHeaderConfig.RegexValues...)
	}
	if condition.QueryStringConfig != nil {
		queryValues := make([]any, 0, len(condition.QueryStringConfig.Values))
		for _, pair := range condition.QueryStringConfig.Values {
			key, value := aws.ToString(pair.Key), aws.ToString(pair.Value)
			queryValues = append(queryValues, map[string]any{"key": key, "value": value})
			if key == "" {
				values = append(values, value)
			} else {
				values = append(values, key+"="+value)
			}
		}
		detail["query_values"] = queryValues
	}
	if len(values) != 0 {
		detail["values"] = values
	}
	if len(regexValues) != 0 {
		detail["regex_values"] = regexValues
	}
	labelValues := append(append([]string(nil), values...), regexValues...)
	return detail, field + "=" + strings.Join(labelValues, "|"), nil
}

func observedELBV2(key awsbrowser.QueryKey, resourceKey awsbrowser.ResourceKey, fields map[string]any, fetchedAt time.Time) (awsbrowser.ObservedResource, error) {
	observation, err := awsbrowser.NewResourceObservationForOperation(key.Context, key.Operation, fields, fetchedAt, true)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	return awsbrowser.ObservedResource{Key: resourceKey, Observation: observation}, nil
}

func exactELBV2Relation(source, target awsbrowser.ResourceKey, relationType awsbrowser.RelationType, condition, operation, reason string, observedAt time.Time) map[string]any {
	return map[string]any{
		"source": source, "target": target, "relation_type": string(relationType), "direction": string(awsbrowser.RelationOutgoing),
		"condition": condition, "kind": string(awsbrowser.RelationAPIExact), "reason": reason,
		"operation": operation, "scope": source.Region, "observed_at": observedAt,
	}
}

func exactELBV2Params(key awsbrowser.QueryKey, alternatives ...[]string) (url.Values, error) {
	values, err := url.ParseQuery(key.ParamsKey)
	if err != nil {
		return nil, awsbrowser.ErrInvalidQueryKey
	}
	for _, names := range alternatives {
		if len(values) != len(names) {
			continue
		}
		valid := true
		for _, name := range names {
			if len(values[name]) != 1 || strings.TrimSpace(values.Get(name)) == "" || values.Get(name) != strings.TrimSpace(values.Get(name)) {
				valid = false
				break
			}
		}
		if valid {
			return values, nil
		}
	}
	return nil, awsbrowser.ErrInvalidQueryKey
}

func validELBV2ARN(context awsbrowser.AWSContext, value, resourcePrefix string) bool {
	parsed, err := awsarn.Parse(strings.TrimSpace(value))
	return err == nil && parsed.Partition == context.Partition && parsed.Service == "elasticloadbalancing" &&
		parsed.Region == context.Region && parsed.AccountID == context.AccountID && strings.HasPrefix(parsed.Resource, resourcePrefix)
}

func validTargetType(value string) bool {
	switch value {
	case "instance", "ip", "lambda", "alb":
		return true
	default:
		return false
	}
}

func validCatalogLoadBalancerType(value string) bool {
	return value == string(types.LoadBalancerTypeEnumApplication) || value == string(types.LoadBalancerTypeEnumNetwork)
}

func canonicalELBV2DNS(value string) string {
	value = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	return strings.TrimPrefix(value, "dualstack.")
}

func targetGroupLabel(targetGroupARN string) string {
	if parsed, err := awsarn.Parse(targetGroupARN); err == nil {
		parts := strings.Split(parsed.Resource, "/")
		if len(parts) >= 2 && parts[1] != "" {
			return parts[1]
		}
	}
	return targetGroupARN
}

func emitELBV2Page(sink awsbrowser.QueryPageSink, pageNumber uint64, resources []awsbrowser.ObservedResource, fetchedAt time.Time) error {
	page, err := awsbrowser.NewQueryPage(pageNumber, resources, fetchedAt, true)
	if err != nil {
		return err
	}
	return sink.Page(page)
}

func nextELBV2Marker(raw *string, seen map[string]struct{}) (string, bool, error) {
	marker := strings.TrimSpace(aws.ToString(raw))
	if marker == "" {
		return "", false, nil
	}
	if _, duplicate := seen[marker]; duplicate {
		return "", false, awsbrowser.ErrQueryDecode
	}
	seen[marker] = struct{}{}
	return marker, true, nil
}
