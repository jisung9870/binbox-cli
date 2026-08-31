package awsbrowser

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// STSAPI is the complete STS surface available to an AWS browser provider.
// Calls deliberately accept no SDK operation options: endpoint, transport,
// signing, and credential policy remain owned by the runtime.
type STSAPI interface {
	GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput) (*sts.GetCallerIdentityOutput, error)
}

// EC2API is the complete EC2 surface available to AWS browser providers.
// Adding an operation requires the Phase 0 read-boundary evidence described by
// the AWS browser architecture.
type EC2API interface {
	DescribeImages(context.Context, *ec2.DescribeImagesInput) (*ec2.DescribeImagesOutput, error)
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error)
	DescribeVolumes(context.Context, *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error)
	DescribeSecurityGroups(context.Context, *ec2.DescribeSecurityGroupsInput) (*ec2.DescribeSecurityGroupsOutput, error)
	DescribeSecurityGroupRules(context.Context, *ec2.DescribeSecurityGroupRulesInput) (*ec2.DescribeSecurityGroupRulesOutput, error)
	DescribeVpcs(context.Context, *ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error)
	DescribeSubnets(context.Context, *ec2.DescribeSubnetsInput) (*ec2.DescribeSubnetsOutput, error)
	DescribeRouteTables(context.Context, *ec2.DescribeRouteTablesInput) (*ec2.DescribeRouteTablesOutput, error)
	DescribeVpcPeeringConnections(context.Context, *ec2.DescribeVpcPeeringConnectionsInput) (*ec2.DescribeVpcPeeringConnectionsOutput, error)
	DescribeLaunchTemplates(context.Context, *ec2.DescribeLaunchTemplatesInput) (*ec2.DescribeLaunchTemplatesOutput, error)
	DescribeLaunchTemplateVersions(context.Context, *ec2.DescribeLaunchTemplateVersionsInput) (*ec2.DescribeLaunchTemplateVersionsOutput, error)
}

// IAMAPI is the complete IAM surface available to AWS browser providers.
type IAMAPI interface {
	ListRoles(context.Context, *iam.ListRolesInput) (*iam.ListRolesOutput, error)
	GetInstanceProfile(context.Context, *iam.GetInstanceProfileInput) (*iam.GetInstanceProfileOutput, error)
	GetRole(context.Context, *iam.GetRoleInput) (*iam.GetRoleOutput, error)
	ListAttachedRolePolicies(context.Context, *iam.ListAttachedRolePoliciesInput) (*iam.ListAttachedRolePoliciesOutput, error)
	ListRolePolicies(context.Context, *iam.ListRolePoliciesInput) (*iam.ListRolePoliciesOutput, error)
	GetPolicy(context.Context, *iam.GetPolicyInput) (*iam.GetPolicyOutput, error)
	GetPolicyVersion(context.Context, *iam.GetPolicyVersionInput) (*iam.GetPolicyVersionOutput, error)
	GetRolePolicy(context.Context, *iam.GetRolePolicyInput) (*iam.GetRolePolicyOutput, error)
}

// Route53API is the complete Route 53 surface available to AWS browser
// providers.
type Route53API interface {
	ListHostedZones(context.Context, *route53.ListHostedZonesInput) (*route53.ListHostedZonesOutput, error)
	ListHostedZonesByName(context.Context, *route53.ListHostedZonesByNameInput) (*route53.ListHostedZonesByNameOutput, error)
	ListResourceRecordSets(context.Context, *route53.ListResourceRecordSetsInput) (*route53.ListResourceRecordSetsOutput, error)
}

type CloudFrontAPI interface {
	ListDistributions(context.Context, *cloudfront.ListDistributionsInput) (*cloudfront.ListDistributionsOutput, error)
}

type ELBV2API interface {
	DescribeLoadBalancers(context.Context, *elasticloadbalancingv2.DescribeLoadBalancersInput) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error)
	DescribeListeners(context.Context, *elasticloadbalancingv2.DescribeListenersInput) (*elasticloadbalancingv2.DescribeListenersOutput, error)
	DescribeRules(context.Context, *elasticloadbalancingv2.DescribeRulesInput) (*elasticloadbalancingv2.DescribeRulesOutput, error)
	DescribeTargetGroups(context.Context, *elasticloadbalancingv2.DescribeTargetGroupsInput) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error)
	DescribeTargetHealth(context.Context, *elasticloadbalancingv2.DescribeTargetHealthInput) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error)
}

type S3API interface {
	GetBucketLocation(context.Context, *s3.GetBucketLocationInput) (*s3.GetBucketLocationOutput, error)
}

// The raw SDK call contracts stay private so callers cannot supply operation
// options or assert a RuntimeContext client back to a concrete SDK client.
type rawSTSAPI interface {
	GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

type rawEC2API interface {
	DescribeImages(context.Context, *ec2.DescribeImagesInput, ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error)
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	DescribeVolumes(context.Context, *ec2.DescribeVolumesInput, ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error)
	DescribeSecurityGroups(context.Context, *ec2.DescribeSecurityGroupsInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
	DescribeSecurityGroupRules(context.Context, *ec2.DescribeSecurityGroupRulesInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupRulesOutput, error)
	DescribeVpcs(context.Context, *ec2.DescribeVpcsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
	DescribeSubnets(context.Context, *ec2.DescribeSubnetsInput, ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
	DescribeRouteTables(context.Context, *ec2.DescribeRouteTablesInput, ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error)
	DescribeVpcPeeringConnections(context.Context, *ec2.DescribeVpcPeeringConnectionsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcPeeringConnectionsOutput, error)
	DescribeLaunchTemplates(context.Context, *ec2.DescribeLaunchTemplatesInput, ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplatesOutput, error)
	DescribeLaunchTemplateVersions(context.Context, *ec2.DescribeLaunchTemplateVersionsInput, ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplateVersionsOutput, error)
}

type rawIAMAPI interface {
	ListRoles(context.Context, *iam.ListRolesInput, ...func(*iam.Options)) (*iam.ListRolesOutput, error)
	GetInstanceProfile(context.Context, *iam.GetInstanceProfileInput, ...func(*iam.Options)) (*iam.GetInstanceProfileOutput, error)
	GetRole(context.Context, *iam.GetRoleInput, ...func(*iam.Options)) (*iam.GetRoleOutput, error)
	ListAttachedRolePolicies(context.Context, *iam.ListAttachedRolePoliciesInput, ...func(*iam.Options)) (*iam.ListAttachedRolePoliciesOutput, error)
	ListRolePolicies(context.Context, *iam.ListRolePoliciesInput, ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error)
	GetPolicy(context.Context, *iam.GetPolicyInput, ...func(*iam.Options)) (*iam.GetPolicyOutput, error)
	GetPolicyVersion(context.Context, *iam.GetPolicyVersionInput, ...func(*iam.Options)) (*iam.GetPolicyVersionOutput, error)
	GetRolePolicy(context.Context, *iam.GetRolePolicyInput, ...func(*iam.Options)) (*iam.GetRolePolicyOutput, error)
}

type rawRoute53API interface {
	ListHostedZones(context.Context, *route53.ListHostedZonesInput, ...func(*route53.Options)) (*route53.ListHostedZonesOutput, error)
	ListHostedZonesByName(context.Context, *route53.ListHostedZonesByNameInput, ...func(*route53.Options)) (*route53.ListHostedZonesByNameOutput, error)
	ListResourceRecordSets(context.Context, *route53.ListResourceRecordSetsInput, ...func(*route53.Options)) (*route53.ListResourceRecordSetsOutput, error)
}

type rawCloudFrontAPI interface {
	ListDistributions(context.Context, *cloudfront.ListDistributionsInput, ...func(*cloudfront.Options)) (*cloudfront.ListDistributionsOutput, error)
}

type rawELBV2API interface {
	DescribeLoadBalancers(context.Context, *elasticloadbalancingv2.DescribeLoadBalancersInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error)
	DescribeListeners(context.Context, *elasticloadbalancingv2.DescribeListenersInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeListenersOutput, error)
	DescribeRules(context.Context, *elasticloadbalancingv2.DescribeRulesInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeRulesOutput, error)
	DescribeTargetGroups(context.Context, *elasticloadbalancingv2.DescribeTargetGroupsInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error)
	DescribeTargetHealth(context.Context, *elasticloadbalancingv2.DescribeTargetHealthInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error)
}

type rawS3API interface {
	GetBucketLocation(context.Context, *s3.GetBucketLocationInput, ...func(*s3.Options)) (*s3.GetBucketLocationOutput, error)
}

var (
	_ rawSTSAPI        = (*sts.Client)(nil)
	_ rawEC2API        = (*ec2.Client)(nil)
	_ rawIAMAPI        = (*iam.Client)(nil)
	_ rawRoute53API    = (*route53.Client)(nil)
	_ rawCloudFrontAPI = (*cloudfront.Client)(nil)
	_ rawELBV2API      = (*elasticloadbalancingv2.Client)(nil)
	_ rawS3API         = (*s3.Client)(nil)
)
