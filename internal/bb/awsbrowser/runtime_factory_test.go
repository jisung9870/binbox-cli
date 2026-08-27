package awsbrowser

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

type recordingCredentialExporter struct {
	mu       sync.Mutex
	profiles []string
	output   []byte
}

func (e *recordingCredentialExporter) ExportCredentials(_ context.Context, profile string, _ []string) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.profiles = append(e.profiles, profile)
	return append([]byte(nil), e.output...), nil
}

func (e *recordingCredentialExporter) calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.profiles...)
}

type blockingCredentialExporter struct {
	started chan struct{}
	once    sync.Once
}

func (e *blockingCredentialExporter) ExportCredentials(ctx context.Context, _ string, _ []string) ([]byte, error) {
	e.once.Do(func() { close(e.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

type credentialSTS struct {
	cache    *aws.CredentialsCache
	mu       sync.Mutex
	calls    int
	identity func(int) *sts.GetCallerIdentityOutput
	failure  func(int) error
}

func (s *credentialSTS) GetCallerIdentity(ctx context.Context, _ *sts.GetCallerIdentityInput, _ ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	if _, err := s.cache.Retrieve(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.failure != nil {
		if err := s.failure(s.calls); err != nil {
			return nil, err
		}
	}
	return s.identity(s.calls), nil
}

func (s *credentialSTS) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type ec2Stub struct {
	describeInstances func(context.Context) (*ec2.DescribeInstancesOutput, error)
}

func (s *ec2Stub) DescribeInstances(ctx context.Context, _ *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	if s.describeInstances != nil {
		return s.describeInstances(ctx)
	}
	return &ec2.DescribeInstancesOutput{}, nil
}

func (*ec2Stub) DescribeVolumes(context.Context, *ec2.DescribeVolumesInput, ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	return &ec2.DescribeVolumesOutput{}, nil
}

func (*ec2Stub) DescribeSecurityGroups(context.Context, *ec2.DescribeSecurityGroupsInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	return &ec2.DescribeSecurityGroupsOutput{}, nil
}

func (*ec2Stub) DescribeSecurityGroupRules(context.Context, *ec2.DescribeSecurityGroupRulesInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupRulesOutput, error) {
	return &ec2.DescribeSecurityGroupRulesOutput{}, nil
}

func (*ec2Stub) DescribeVpcs(context.Context, *ec2.DescribeVpcsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	return &ec2.DescribeVpcsOutput{}, nil
}

func (*ec2Stub) DescribeSubnets(context.Context, *ec2.DescribeSubnetsInput, ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	return &ec2.DescribeSubnetsOutput{}, nil
}

func (*ec2Stub) DescribeRouteTables(context.Context, *ec2.DescribeRouteTablesInput, ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error) {
	return &ec2.DescribeRouteTablesOutput{}, nil
}

type iamStub struct{}

func (iamStub) ListRoles(context.Context, *iam.ListRolesInput, ...func(*iam.Options)) (*iam.ListRolesOutput, error) {
	return &iam.ListRolesOutput{}, nil
}

func (iamStub) GetInstanceProfile(context.Context, *iam.GetInstanceProfileInput, ...func(*iam.Options)) (*iam.GetInstanceProfileOutput, error) {
	return &iam.GetInstanceProfileOutput{}, nil
}

func (iamStub) GetRole(context.Context, *iam.GetRoleInput, ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
	return &iam.GetRoleOutput{}, nil
}

func (iamStub) ListAttachedRolePolicies(context.Context, *iam.ListAttachedRolePoliciesInput, ...func(*iam.Options)) (*iam.ListAttachedRolePoliciesOutput, error) {
	return &iam.ListAttachedRolePoliciesOutput{}, nil
}

func (iamStub) ListRolePolicies(context.Context, *iam.ListRolePoliciesInput, ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error) {
	return &iam.ListRolePoliciesOutput{}, nil
}

func (iamStub) GetPolicy(context.Context, *iam.GetPolicyInput, ...func(*iam.Options)) (*iam.GetPolicyOutput, error) {
	return &iam.GetPolicyOutput{}, nil
}

func (iamStub) GetPolicyVersion(context.Context, *iam.GetPolicyVersionInput, ...func(*iam.Options)) (*iam.GetPolicyVersionOutput, error) {
	return &iam.GetPolicyVersionOutput{}, nil
}

func (iamStub) GetRolePolicy(context.Context, *iam.GetRolePolicyInput, ...func(*iam.Options)) (*iam.GetRolePolicyOutput, error) {
	return &iam.GetRolePolicyOutput{}, nil
}

type route53Stub struct{}

func (route53Stub) ListHostedZones(context.Context, *route53.ListHostedZonesInput, ...func(*route53.Options)) (*route53.ListHostedZonesOutput, error) {
	return &route53.ListHostedZonesOutput{}, nil
}

func (route53Stub) ListHostedZonesByName(context.Context, *route53.ListHostedZonesByNameInput, ...func(*route53.Options)) (*route53.ListHostedZonesByNameOutput, error) {
	return &route53.ListHostedZonesByNameOutput{}, nil
}

func (route53Stub) ListResourceRecordSets(context.Context, *route53.ListResourceRecordSetsInput, ...func(*route53.Options)) (*route53.ListResourceRecordSetsOutput, error) {
	return &route53.ListResourceRecordSetsOutput{}, nil
}

func fakeSDKRuntime(provider *CredentialProvider, identity func(int) *sts.GetCallerIdentityOutput, ec2Client rawEC2API) (*sdkRuntime, *credentialSTS) {
	cache := aws.NewCredentialsCache(provider)
	stsClient := &credentialSTS{cache: cache, identity: identity}
	if ec2Client == nil {
		ec2Client = &ec2Stub{}
	}
	return &sdkRuntime{
		credentials:      cache,
		credentialSource: provider,
		sts:              stsClient,
		ec2:              ec2Client,
		iam:              iamStub{},
		route53:          route53Stub{},
	}, stsClient
}

func TestRuntimeFactoryValidatesContextBeforeBuilding(t *testing.T) {
	exporter := &recordingCredentialExporter{output: validCredentialDocument()}
	builds := 0
	factory, err := newRuntimeFactory(exporter, []string{}, func(context.Context, string, *CredentialProvider) (*sdkRuntime, error) {
		builds++
		return nil, errors.New("unexpected build")
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []ContextSpec{
		{Mode: ContextModeAmbient, Profile: "dev", Region: "us-east-1"},
		{Mode: ContextModeNamedProfile, Region: "us-east-1"},
		{Mode: ContextModeNamedProfile, Profile: "--dev", Region: "us-east-1"},
		{Mode: "unknown", Region: "us-east-1"},
		{Mode: ContextModeAmbient},
		{Mode: ContextModeAmbient, Region: "us_east_1"},
		{Mode: ContextModeAmbient, Region: "--region"},
	}
	for _, spec := range tests {
		if _, err := factory.Resolve(context.Background(), spec); err == nil {
			t.Errorf("spec=%+v was accepted", spec)
		}
	}
	if builds != 0 || len(exporter.calls()) != 0 {
		t.Fatalf("invalid contexts executed work: builds=%d exports=%v", builds, exporter.calls())
	}
}

func TestRuntimeFactoryResolvesAmbientAndIsolatesNamedProfiles(t *testing.T) {
	exporter := &recordingCredentialExporter{output: validCredentialDocument()}
	var providers []*CredentialProvider
	var caches []*aws.CredentialsCache
	var regions []string
	factory, err := newRuntimeFactory(exporter, []string{"SAFE=original"}, func(_ context.Context, region string, provider *CredentialProvider) (*sdkRuntime, error) {
		runtime, _ := fakeSDKRuntime(provider, func(int) *sts.GetCallerIdentityOutput {
			return callerIdentity("aws", "123456789012", provider.profile+"-session")
		}, nil)
		providers = append(providers, provider)
		caches = append(caches, runtime.credentials)
		regions = append(regions, region)
		return runtime, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	specs := []ContextSpec{
		{Mode: ContextModeAmbient, Region: "us-east-1"},
		{Mode: ContextModeNamedProfile, Profile: "dev", Region: "ap-northeast-2"},
		{Mode: ContextModeNamedProfile, Profile: "prod", Region: "us-gov-west-1"},
	}
	for _, spec := range specs {
		runtime, err := factory.Resolve(context.Background(), spec)
		if err != nil {
			t.Fatal(err)
		}
		if identity := runtime.Identity(); identity.AccountID != "123456789012" || identity.CredentialGeneration != 1 {
			t.Fatalf("identity=%+v", identity)
		}
	}

	if got, want := exporter.calls(), []string{"", "dev", "prod"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("export profiles=%v want=%v", got, want)
	}
	if got, want := regions, []string{"us-east-1", "ap-northeast-2", "us-gov-west-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("regions=%v want=%v", got, want)
	}
	if providers[0] == providers[1] || providers[1] == providers[2] || caches[0] == caches[1] || caches[1] == caches[2] {
		t.Fatal("contexts shared credential providers or caches")
	}
}

func TestRuntimeFactoryCredentialChildUsesResolveCancellation(t *testing.T) {
	exporter := &blockingCredentialExporter{started: make(chan struct{})}
	factory, err := newRuntimeFactory(exporter, []string{}, func(_ context.Context, _ string, provider *CredentialProvider) (*sdkRuntime, error) {
		runtime, _ := fakeSDKRuntime(provider, func(int) *sts.GetCallerIdentityOutput {
			return callerIdentity("aws", "123456789012", "reader")
		}, nil)
		return runtime, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := factory.Resolve(ctx, ContextSpec{Mode: ContextModeNamedProfile, Profile: "dev", Region: "us-east-1"})
		done <- err
	}()
	select {
	case <-exporter.started:
	case <-time.After(2 * time.Second):
		t.Fatal("credential child did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v want context cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("credential child did not stop after cancellation")
	}
}

func TestRuntimeIdentityRetriesTypedExpiredTokenOnlyOnce(t *testing.T) {
	exporter := &recordingCredentialExporter{output: validCredentialDocument()}
	provider, err := NewCredentialProvider(exporter, "dev", []string{})
	if err != nil {
		t.Fatal(err)
	}
	runtime, stsClient := fakeSDKRuntime(provider, func(int) *sts.GetCallerIdentityOutput {
		return callerIdentity("aws", "123456789012", "reader")
	}, nil)
	first := &smithy.GenericAPIError{Code: "ExpiredToken", Message: "first"}
	stsClient.failure = func(call int) error {
		if call == 1 {
			return first
		}
		return nil
	}

	identity, err := resolveRuntimeIdentity(context.Background(), runtime)
	if err != nil {
		t.Fatal(err)
	}
	if identity.CredentialGeneration != 2 || stsClient.callCount() != 2 || len(exporter.calls()) != 2 {
		t.Fatalf("identity=%+v STS calls=%d exports=%v", identity, stsClient.callCount(), exporter.calls())
	}

	provider, err = NewCredentialProvider(exporter, "dev", []string{})
	if err != nil {
		t.Fatal(err)
	}
	runtime, stsClient = fakeSDKRuntime(provider, func(int) *sts.GetCallerIdentityOutput {
		return callerIdentity("aws", "123456789012", "reader")
	}, nil)
	second := &smithy.GenericAPIError{Code: "ExpiredToken", Message: "second"}
	stsClient.failure = func(call int) error {
		if call == 1 {
			return first
		}
		return second
	}
	_, err = resolveRuntimeIdentity(context.Background(), runtime)
	if err != second || stsClient.callCount() != 2 {
		t.Fatalf("error=%v STS calls=%d want second typed error after two calls", err, stsClient.callCount())
	}
}

func TestNewRuntimeFactoryRejectsMissingCLIPath(t *testing.T) {
	if _, err := NewRuntimeFactory("", []string{}); err == nil {
		t.Fatal("empty AWS CLI path was accepted")
	}
}

func validCredentialDocument() []byte {
	return []byte(`{"Version":1,"AccessKeyId":"AKIDEXAMPLE","SecretAccessKey":"secret"}`)
}
