package awsbrowser

import (
	"errors"
	"strings"
)

var ErrInvalidProviderOperation = errors.New("invalid provider operation")

// Provider and operation names are the stable query/observation vocabulary
// shared by the EC2, IAM, and Route 53 lanes. Operation values intentionally
// match AWS SDK method names.
const (
	ProviderEC2        = "ec2"
	ProviderIAM        = "iam"
	ProviderRoute53    = "route53"
	ProviderCloudFront = "cloudfront"
	ProviderELBV2      = "elbv2"
	ProviderS3         = "s3"

	OperationDescribeImages                 = "DescribeImages"
	OperationDescribeInstances              = "DescribeInstances"
	OperationDescribeVolumes                = "DescribeVolumes"
	OperationDescribeSecurityGroups         = "DescribeSecurityGroups"
	OperationDescribeSecurityGroupRules     = "DescribeSecurityGroupRules"
	OperationDescribeVpcs                   = "DescribeVpcs"
	OperationDescribeSubnets                = "DescribeSubnets"
	OperationDescribeRouteTables            = "DescribeRouteTables"
	OperationDescribeVpcPeeringConnections  = "DescribeVpcPeeringConnections"
	OperationDescribeLaunchTemplates        = "DescribeLaunchTemplates"
	OperationDescribeLaunchTemplateVersions = "DescribeLaunchTemplateVersions"

	OperationListRoles                = "ListRoles"
	OperationGetInstanceProfile       = "GetInstanceProfile"
	OperationGetRole                  = "GetRole"
	OperationListAttachedRolePolicies = "ListAttachedRolePolicies"
	OperationListRolePolicies         = "ListRolePolicies"
	OperationGetPolicy                = "GetPolicy"
	OperationGetPolicyVersion         = "GetPolicyVersion"
	OperationGetRolePolicy            = "GetRolePolicy"

	OperationListHostedZones        = "ListHostedZones"
	OperationListHostedZonesByName  = "ListHostedZonesByName"
	OperationListResourceRecordSets = "ListResourceRecordSets"

	OperationListDistributions     = "ListDistributions"
	OperationDescribeLoadBalancers = "DescribeLoadBalancers"
	OperationDescribeListeners     = "DescribeListeners"
	OperationDescribeRules         = "DescribeRules"
	OperationDescribeTargetGroups  = "DescribeTargetGroups"
	OperationDescribeTargetHealth  = "DescribeTargetHealth"
	OperationGetBucketLocation     = "GetBucketLocation"
)

var providerOperations = map[string]map[string]struct{}{
	ProviderEC2: operationSet(
		OperationDescribeImages, OperationDescribeInstances, OperationDescribeVolumes, OperationDescribeSecurityGroups,
		OperationDescribeSecurityGroupRules, OperationDescribeVpcs, OperationDescribeSubnets,
		OperationDescribeRouteTables,
		OperationDescribeVpcPeeringConnections,
		OperationDescribeLaunchTemplates, OperationDescribeLaunchTemplateVersions,
	),
	ProviderIAM: operationSet(
		OperationListRoles, OperationGetInstanceProfile, OperationGetRole,
		OperationListAttachedRolePolicies, OperationListRolePolicies, OperationGetPolicy,
		OperationGetPolicyVersion, OperationGetRolePolicy,
	),
	ProviderRoute53: operationSet(
		OperationListHostedZones, OperationListHostedZonesByName, OperationListResourceRecordSets,
	),
	ProviderCloudFront: operationSet(OperationListDistributions),
	ProviderELBV2: operationSet(
		OperationDescribeLoadBalancers, OperationDescribeListeners, OperationDescribeRules,
		OperationDescribeTargetGroups, OperationDescribeTargetHealth,
	),
	ProviderS3: operationSet(OperationGetBucketLocation),
}

// ValidateProviderOperation rejects unknown and cross-service combinations.
// A provider owns SDK pagination: it consumes opaque cursors internally,
// maps each complete operation output to safe built-in field values, emits
// complete QueryPages, and calls QueryPageSink.Complete exactly once only
// after the SDK terminal page. Provider cursors never enter QueryPage/store.
func ValidateProviderOperation(provider, operation string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	operation = strings.TrimSpace(operation)
	operations, ok := providerOperations[provider]
	if !ok {
		return ErrInvalidProviderOperation
	}
	if _, ok := operations[operation]; !ok {
		return ErrInvalidProviderOperation
	}
	return nil
}

func operationSet(operations ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(operations))
	for _, operation := range operations {
		set[operation] = struct{}{}
	}
	return set
}
