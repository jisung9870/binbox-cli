package awsbrowser

import (
	"context"
	"errors"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

// ErrContextChanged means a read response was discarded because its verified
// credential generation or principal cannot be committed under the query's
// original identity.
var ErrContextChanged = errors.New("AWS runtime context changed")

type readIdentityContextKey struct{}

// WithReadIdentity binds guarded SDK reads to the verified identity carried by
// a QueryKey. A read that revalidates to any other generation or principal is
// rejected before its response can be mapped or committed under that key.
func WithReadIdentity(ctx context.Context, identity VerifiedIdentity) context.Context {
	return context.WithValue(ctx, readIdentityContextKey{}, identity)
}

// ValidateReadIdentity lets RuntimeContext test doubles honor the same bound
// read contract as the production guarded clients.
func ValidateReadIdentity(ctx context.Context, identity VerifiedIdentity) error {
	expected, ok := ctx.Value(readIdentityContextKey{}).(VerifiedIdentity)
	if ok && expected != identity {
		return ErrContextChanged
	}
	return nil
}

type readGuard struct {
	runtime *sdkRuntime
	gate    chan struct{}

	mu       sync.RWMutex
	identity VerifiedIdentity
	changed  bool
}

func newReadGuard(runtime *sdkRuntime, identity VerifiedIdentity) *readGuard {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &readGuard{runtime: runtime, gate: gate, identity: identity}
}

func (g *readGuard) Identity() VerifiedIdentity {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.identity
}

func (g *readGuard) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.gate:
		return nil
	}
}

func (g *readGuard) release() {
	g.gate <- struct{}{}
}

func (g *readGuard) ensureCurrentIdentity(ctx context.Context) error {
	current, changed := g.state()
	if changed {
		return ErrContextChanged
	}
	if g.runtime.credentialSource.Generation() == current.CredentialGeneration {
		return nil
	}
	return g.revalidate(ctx, current)
}

func (g *readGuard) state() (VerifiedIdentity, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.identity, g.changed
}

func (g *readGuard) revalidate(ctx context.Context, expected VerifiedIdentity) error {
	output, err := g.runtime.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return err
	}
	actual, err := verifiedIdentity(output, g.runtime.credentialSource.Generation())
	if err != nil {
		return err
	}
	if actual.AccountID != expected.AccountID || actual.Partition != expected.Partition {
		g.mu.Lock()
		g.changed = true
		g.mu.Unlock()
		return ErrContextChanged
	}

	g.mu.Lock()
	g.identity = actual
	g.mu.Unlock()
	return nil
}

func (g *readGuard) refreshAndRevalidate(ctx context.Context, expected VerifiedIdentity) error {
	g.runtime.credentials.Invalidate()
	if _, err := g.runtime.credentials.Retrieve(ctx); err != nil {
		return err
	}
	return g.revalidate(ctx, expected)
}

func guardedRead[T any](ctx context.Context, guard *readGuard, call func(context.Context) (T, error)) (T, error) {
	var zero T
	if err := guard.acquire(ctx); err != nil {
		return zero, err
	}
	defer guard.release()

	if err := guard.ensureCurrentIdentity(ctx); err != nil {
		return zero, err
	}
	expected := guard.Identity()
	if err := ValidateReadIdentity(ctx, expected); err != nil {
		return zero, err
	}

	value, err := call(ctx)
	if err != nil {
		if !isExpiredToken(err) {
			return value, err
		}
		if err := guard.refreshAndRevalidate(ctx, expected); err != nil {
			return zero, err
		}
		expected = guard.Identity()
		if err := ValidateReadIdentity(ctx, expected); err != nil {
			return zero, err
		}
		value, err = call(ctx)
		if err != nil {
			return value, err
		}
		return commitStableResponse(ctx, guard, expected, value)
	}

	if guard.runtime.credentialSource.Generation() == expected.CredentialGeneration {
		return value, nil
	}
	if err := guard.revalidate(ctx, expected); err != nil {
		return zero, err
	}
	if err := ValidateReadIdentity(ctx, guard.Identity()); err != nil {
		return zero, err
	}

	// The first successful response crossed a credential generation. Discard
	// it, retry once using the revalidated generation, and commit only if that
	// generation remains stable for the complete second operation.
	expected = guard.Identity()
	value, err = call(ctx)
	if err != nil {
		return value, err
	}
	return commitStableResponse(ctx, guard, expected, value)
}

func commitStableResponse[T any](ctx context.Context, g *readGuard, expected VerifiedIdentity, value T) (T, error) {
	var zero T
	if g.runtime.credentialSource.Generation() == expected.CredentialGeneration {
		return value, nil
	}
	if err := g.revalidate(ctx, expected); err != nil {
		return zero, err
	}
	return zero, ErrContextChanged
}

func isExpiredToken(err error) bool {
	var apiError smithy.APIError
	return errors.As(err, &apiError) && apiError.ErrorCode() == "ExpiredToken"
}

type guardedSTS struct {
	guard  *readGuard
	client rawSTSAPI
}

func (c guardedSTS) GetCallerIdentity(ctx context.Context, input *sts.GetCallerIdentityInput) (*sts.GetCallerIdentityOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*sts.GetCallerIdentityOutput, error) {
		return c.client.GetCallerIdentity(ctx, input)
	})
}

type guardedEC2 struct {
	guard  *readGuard
	client rawEC2API
}

func (c guardedEC2) DescribeImages(ctx context.Context, input *ec2.DescribeImagesInput) (*ec2.DescribeImagesOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*ec2.DescribeImagesOutput, error) {
		return c.client.DescribeImages(ctx, input)
	})
}

func (c guardedEC2) DescribeInstances(ctx context.Context, input *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*ec2.DescribeInstancesOutput, error) {
		return c.client.DescribeInstances(ctx, input)
	})
}

func (c guardedEC2) DescribeVolumes(ctx context.Context, input *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*ec2.DescribeVolumesOutput, error) {
		return c.client.DescribeVolumes(ctx, input)
	})
}

func (c guardedEC2) DescribeSecurityGroups(ctx context.Context, input *ec2.DescribeSecurityGroupsInput) (*ec2.DescribeSecurityGroupsOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*ec2.DescribeSecurityGroupsOutput, error) {
		return c.client.DescribeSecurityGroups(ctx, input)
	})
}

func (c guardedEC2) DescribeSecurityGroupRules(ctx context.Context, input *ec2.DescribeSecurityGroupRulesInput) (*ec2.DescribeSecurityGroupRulesOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*ec2.DescribeSecurityGroupRulesOutput, error) {
		return c.client.DescribeSecurityGroupRules(ctx, input)
	})
}

func (c guardedEC2) DescribeVpcs(ctx context.Context, input *ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*ec2.DescribeVpcsOutput, error) {
		return c.client.DescribeVpcs(ctx, input)
	})
}

func (c guardedEC2) DescribeSubnets(ctx context.Context, input *ec2.DescribeSubnetsInput) (*ec2.DescribeSubnetsOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*ec2.DescribeSubnetsOutput, error) {
		return c.client.DescribeSubnets(ctx, input)
	})
}

func (c guardedEC2) DescribeRouteTables(ctx context.Context, input *ec2.DescribeRouteTablesInput) (*ec2.DescribeRouteTablesOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*ec2.DescribeRouteTablesOutput, error) {
		return c.client.DescribeRouteTables(ctx, input)
	})
}

func (c guardedEC2) DescribeVpcPeeringConnections(ctx context.Context, input *ec2.DescribeVpcPeeringConnectionsInput) (*ec2.DescribeVpcPeeringConnectionsOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*ec2.DescribeVpcPeeringConnectionsOutput, error) {
		return c.client.DescribeVpcPeeringConnections(ctx, input)
	})
}

func (c guardedEC2) DescribeLaunchTemplates(ctx context.Context, input *ec2.DescribeLaunchTemplatesInput) (*ec2.DescribeLaunchTemplatesOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*ec2.DescribeLaunchTemplatesOutput, error) {
		return c.client.DescribeLaunchTemplates(ctx, input)
	})
}

func (c guardedEC2) DescribeLaunchTemplateVersions(ctx context.Context, input *ec2.DescribeLaunchTemplateVersionsInput) (*ec2.DescribeLaunchTemplateVersionsOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*ec2.DescribeLaunchTemplateVersionsOutput, error) {
		return c.client.DescribeLaunchTemplateVersions(ctx, input)
	})
}

type guardedIAM struct {
	guard  *readGuard
	client rawIAMAPI
}

func (c guardedIAM) ListRoles(ctx context.Context, input *iam.ListRolesInput) (*iam.ListRolesOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*iam.ListRolesOutput, error) {
		return c.client.ListRoles(ctx, input)
	})
}

func (c guardedIAM) GetInstanceProfile(ctx context.Context, input *iam.GetInstanceProfileInput) (*iam.GetInstanceProfileOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*iam.GetInstanceProfileOutput, error) {
		return c.client.GetInstanceProfile(ctx, input)
	})
}

func (c guardedIAM) GetRole(ctx context.Context, input *iam.GetRoleInput) (*iam.GetRoleOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*iam.GetRoleOutput, error) {
		return c.client.GetRole(ctx, input)
	})
}

func (c guardedIAM) ListAttachedRolePolicies(ctx context.Context, input *iam.ListAttachedRolePoliciesInput) (*iam.ListAttachedRolePoliciesOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*iam.ListAttachedRolePoliciesOutput, error) {
		return c.client.ListAttachedRolePolicies(ctx, input)
	})
}

func (c guardedIAM) ListRolePolicies(ctx context.Context, input *iam.ListRolePoliciesInput) (*iam.ListRolePoliciesOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*iam.ListRolePoliciesOutput, error) {
		return c.client.ListRolePolicies(ctx, input)
	})
}

func (c guardedIAM) GetPolicy(ctx context.Context, input *iam.GetPolicyInput) (*iam.GetPolicyOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*iam.GetPolicyOutput, error) {
		return c.client.GetPolicy(ctx, input)
	})
}

func (c guardedIAM) GetPolicyVersion(ctx context.Context, input *iam.GetPolicyVersionInput) (*iam.GetPolicyVersionOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*iam.GetPolicyVersionOutput, error) {
		return c.client.GetPolicyVersion(ctx, input)
	})
}

func (c guardedIAM) GetRolePolicy(ctx context.Context, input *iam.GetRolePolicyInput) (*iam.GetRolePolicyOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*iam.GetRolePolicyOutput, error) {
		return c.client.GetRolePolicy(ctx, input)
	})
}

type guardedRoute53 struct {
	guard  *readGuard
	client rawRoute53API
}

func (c guardedRoute53) ListHostedZones(ctx context.Context, input *route53.ListHostedZonesInput) (*route53.ListHostedZonesOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*route53.ListHostedZonesOutput, error) {
		return c.client.ListHostedZones(ctx, input)
	})
}

func (c guardedRoute53) ListHostedZonesByName(ctx context.Context, input *route53.ListHostedZonesByNameInput) (*route53.ListHostedZonesByNameOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*route53.ListHostedZonesByNameOutput, error) {
		return c.client.ListHostedZonesByName(ctx, input)
	})
}

func (c guardedRoute53) ListResourceRecordSets(ctx context.Context, input *route53.ListResourceRecordSetsInput) (*route53.ListResourceRecordSetsOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*route53.ListResourceRecordSetsOutput, error) {
		return c.client.ListResourceRecordSets(ctx, input)
	})
}

type guardedCloudFront struct {
	guard  *readGuard
	client rawCloudFrontAPI
}

func (c guardedCloudFront) ListDistributions(ctx context.Context, input *cloudfront.ListDistributionsInput) (*cloudfront.ListDistributionsOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*cloudfront.ListDistributionsOutput, error) {
		return c.client.ListDistributions(ctx, input)
	})
}

type guardedS3 struct {
	guard  *readGuard
	client rawS3API
}

type guardedELBV2 struct {
	guard  *readGuard
	client rawELBV2API
}

func (c guardedELBV2) DescribeLoadBalancers(ctx context.Context, input *elasticloadbalancingv2.DescribeLoadBalancersInput) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error) {
		return c.client.DescribeLoadBalancers(ctx, input)
	})
}

func (c guardedELBV2) DescribeListeners(ctx context.Context, input *elasticloadbalancingv2.DescribeListenersInput) (*elasticloadbalancingv2.DescribeListenersOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*elasticloadbalancingv2.DescribeListenersOutput, error) {
		return c.client.DescribeListeners(ctx, input)
	})
}

func (c guardedELBV2) DescribeRules(ctx context.Context, input *elasticloadbalancingv2.DescribeRulesInput) (*elasticloadbalancingv2.DescribeRulesOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*elasticloadbalancingv2.DescribeRulesOutput, error) {
		return c.client.DescribeRules(ctx, input)
	})
}

func (c guardedELBV2) DescribeTargetGroups(ctx context.Context, input *elasticloadbalancingv2.DescribeTargetGroupsInput) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error) {
		return c.client.DescribeTargetGroups(ctx, input)
	})
}

func (c guardedELBV2) DescribeTargetHealth(ctx context.Context, input *elasticloadbalancingv2.DescribeTargetHealthInput) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error) {
		return c.client.DescribeTargetHealth(ctx, input)
	})
}

func (c guardedS3) GetBucketLocation(ctx context.Context, input *s3.GetBucketLocationInput) (*s3.GetBucketLocationOutput, error) {
	return guardedRead(ctx, c.guard, func(ctx context.Context) (*s3.GetBucketLocationOutput, error) {
		return c.client.GetBucketLocation(ctx, input)
	})
}

var (
	_ STSAPI        = guardedSTS{}
	_ EC2API        = guardedEC2{}
	_ IAMAPI        = guardedIAM{}
	_ Route53API    = guardedRoute53{}
	_ CloudFrontAPI = guardedCloudFront{}
	_ ELBV2API      = guardedELBV2{}
	_ S3API         = guardedS3{}
)
