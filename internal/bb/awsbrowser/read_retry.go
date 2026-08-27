package awsbrowser

import (
	"context"
	"errors"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

// ErrContextChanged means a read response was discarded because its
// credential generation could not be committed to the runtime's verified
// account and partition.
var ErrContextChanged = errors.New("AWS runtime context changed")

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

	value, err := call(ctx)
	if err != nil {
		if !isExpiredToken(err) {
			return value, err
		}
		if err := guard.refreshAndRevalidate(ctx, expected); err != nil {
			return zero, err
		}
		expected = guard.Identity()
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

var (
	_ STSAPI     = guardedSTS{}
	_ EC2API     = guardedEC2{}
	_ IAMAPI     = guardedIAM{}
	_ Route53API = guardedRoute53{}
)
