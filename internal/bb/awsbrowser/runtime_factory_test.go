package awsbrowser

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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

func allowProfileSource(context.Context, string, []string) error { return nil }

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

type cloudFrontStub struct{}

func (cloudFrontStub) ListDistributions(context.Context, *cloudfront.ListDistributionsInput, ...func(*cloudfront.Options)) (*cloudfront.ListDistributionsOutput, error) {
	return &cloudfront.ListDistributionsOutput{}, nil
}

type s3Stub struct{}

func (s3Stub) GetBucketLocation(context.Context, *s3.GetBucketLocationInput, ...func(*s3.Options)) (*s3.GetBucketLocationOutput, error) {
	return &s3.GetBucketLocationOutput{}, nil
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
		cloudfront:       cloudFrontStub{},
		s3:               s3Stub{},
	}, stsClient
}

func TestRuntimeFactoryValidatesContextBeforeBuilding(t *testing.T) {
	exporter := &recordingCredentialExporter{output: validCredentialDocument()}
	builds := 0
	validations := 0
	factory, err := newRuntimeFactory(exporter, []string{}, func(context.Context, string, *CredentialProvider) (*sdkRuntime, error) {
		builds++
		return nil, errors.New("unexpected build")
	}, func(context.Context, string, []string) error {
		validations++
		return errors.New("unexpected validation")
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
	if validations != 0 || builds != 0 || len(exporter.calls()) != 0 {
		t.Fatalf("invalid contexts executed work: validations=%d builds=%d exports=%v", validations, builds, exporter.calls())
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
	}, allowProfileSource)
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
	}, allowProfileSource)
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

func TestRuntimeFactoryProfileValidationUsesResolveCancellationAndDoesNoRejectedWork(t *testing.T) {
	exporter := &recordingCredentialExporter{output: validCredentialDocument()}
	started := make(chan struct{})
	builds := 0
	factory, err := newRuntimeFactory(exporter, []string{"HOME=/snapshot/home"}, func(context.Context, string, *CredentialProvider) (*sdkRuntime, error) {
		builds++
		return nil, errors.New("unexpected build")
	}, func(ctx context.Context, profile string, env []string) error {
		if profile != "dev" || !reflect.DeepEqual(env, []string{"HOME=/snapshot/home"}) {
			t.Errorf("validator profile=%q env=%v", profile, env)
		}
		close(started)
		<-ctx.Done()
		return ctx.Err()
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
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("profile validation did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v want context cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("profile validation did not stop after cancellation")
	}
	if builds != 0 || len(exporter.calls()) != 0 {
		t.Fatalf("rejected profile executed work: builds=%d exports=%v", builds, exporter.calls())
	}
}

func TestRuntimeFactoryCanceledContextDoesNoWork(t *testing.T) {
	exporter := &recordingCredentialExporter{output: validCredentialDocument()}
	validations := 0
	builds := 0
	factory, err := newRuntimeFactory(exporter, []string{}, func(context.Context, string, *CredentialProvider) (*sdkRuntime, error) {
		builds++
		return nil, errors.New("unexpected build")
	}, func(context.Context, string, []string) error {
		validations++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = factory.Resolve(ctx, ContextSpec{Mode: ContextModeNamedProfile, Profile: "dev", Region: "us-east-1"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v want context cancellation", err)
	}
	if validations != 0 || builds != 0 || len(exporter.calls()) != 0 {
		t.Fatalf("canceled resolve executed work: validations=%d builds=%d exports=%v", validations, builds, exporter.calls())
	}
}

func TestRuntimeFactoryAmbientContextDoesNotValidateProfileFiles(t *testing.T) {
	exporter := &recordingCredentialExporter{output: validCredentialDocument()}
	factory, err := newRuntimeFactory(exporter, []string{"AWS_CONFIG_FILE=/must/not/read"}, func(_ context.Context, _ string, provider *CredentialProvider) (*sdkRuntime, error) {
		runtime, _ := fakeSDKRuntime(provider, func(int) *sts.GetCallerIdentityOutput {
			return callerIdentity("aws", "123456789012", "ambient")
		}, nil)
		return runtime, nil
	}, func(context.Context, string, []string) error {
		return errors.New("ambient context read profile files")
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := factory.Resolve(context.Background(), ContextSpec{Mode: ContextModeAmbient, Region: "us-east-1"}); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeFactoryNamedSourcesReachIdentityVerificationWithIsolatedProviders(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config")
	credentialsPath := filepath.Join(directory, "credentials")
	config := "[profile sso]\n" +
		"sso_session = engineering\n" +
		"[profile role]\n" +
		"role_arn = arn:aws:iam::123456789012:role/ReadOnly\n" +
		"source_profile = static\n" +
		"[profile process]\n" +
		"credential_process = private-command --secret value\n"
	credentials := "[static]\n" +
		"aws_access_key_id = AKIAEXAMPLE\n" +
		"aws_secret_access_key = never-retained\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialsPath, []byte(credentials), 0o600); err != nil {
		t.Fatal(err)
	}

	exporter := &recordingCredentialExporter{output: validCredentialDocument()}
	var providers []*CredentialProvider
	var stsClients []*credentialSTS
	env := []string{"AWS_CONFIG_FILE=" + configPath, "AWS_SHARED_CREDENTIALS_FILE=" + credentialsPath}
	factory, err := newRuntimeFactory(exporter, env, func(_ context.Context, _ string, provider *CredentialProvider) (*sdkRuntime, error) {
		runtime, stsClient := fakeSDKRuntime(provider, func(int) *sts.GetCallerIdentityOutput {
			return callerIdentity("aws", "123456789012", provider.profile)
		}, nil)
		providers = append(providers, provider)
		stsClients = append(stsClients, stsClient)
		return runtime, nil
	}, validateNamedProfileSource)
	if err != nil {
		t.Fatal(err)
	}

	profiles := []string{"static", "sso", "role", "process"}
	for _, profile := range profiles {
		runtime, err := factory.Resolve(context.Background(), ContextSpec{Mode: ContextModeNamedProfile, Profile: profile, Region: "us-east-1"})
		if err != nil {
			t.Fatalf("profile %q: %v", profile, err)
		}
		if runtime.Identity().CredentialGeneration != 1 {
			t.Fatalf("profile %q identity=%+v", profile, runtime.Identity())
		}
	}
	if got := exporter.calls(); !reflect.DeepEqual(got, profiles) {
		t.Fatalf("export profiles=%v want=%v", got, profiles)
	}
	for i := range providers {
		if stsClients[i].callCount() != 1 {
			t.Fatalf("profile %q STS calls=%d", profiles[i], stsClients[i].callCount())
		}
		for j := 0; j < i; j++ {
			if providers[i] == providers[j] {
				t.Fatalf("profiles %q and %q shared provider", profiles[i], profiles[j])
			}
		}
	}
}

func TestRuntimeFactoryRejectsUnsafeNamedTopologiesBeforeExporterOrBuild(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   string
	}{
		{
			name:   "environment source",
			config: "[profile dev]\nrole_arn = arn:aws:iam::123456789012:role/SecretRole\ncredential_source = Environment\n",
			want:   "Environment is not allowed",
		},
		{
			name: "cyclic source profiles",
			config: "[profile dev]\nrole_arn = arn:aws:iam::123456789012:role/Dev\nsource_profile = base\n" +
				"[profile base]\nrole_arn = arn:aws:iam::123456789012:role/Base\nsource_profile = dev\n",
			want: "cycle in source_profile chain",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			configPath := filepath.Join(directory, "secret-config-path")
			if err := os.WriteFile(configPath, []byte(test.config), 0o600); err != nil {
				t.Fatal(err)
			}
			exporter := &recordingCredentialExporter{output: validCredentialDocument()}
			builds := 0
			factory, err := newRuntimeFactory(exporter, []string{"AWS_CONFIG_FILE=" + configPath, "AWS_SHARED_CREDENTIALS_FILE=" + filepath.Join(directory, "missing")}, func(context.Context, string, *CredentialProvider) (*sdkRuntime, error) {
				builds++
				return nil, errors.New("unexpected build")
			}, validateNamedProfileSource)
			if err != nil {
				t.Fatal(err)
			}
			_, err = factory.Resolve(context.Background(), ContextSpec{Mode: ContextModeNamedProfile, Profile: "dev", Region: "us-east-1"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want substring %q", err, test.want)
			}
			if strings.Contains(err.Error(), directory) || strings.Contains(err.Error(), "SecretRole") {
				t.Fatalf("error exposed path or value: %q", err)
			}
			if builds != 0 || len(exporter.calls()) != 0 {
				t.Fatalf("rejected profile executed work: builds=%d exports=%v", builds, exporter.calls())
			}
		})
	}
}

func TestNewRuntimeFactoryWiresNamedProfileValidationBeforeCLIOrSDK(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config")
	config := "[profile dev]\nrole_arn = arn:aws:iam::123456789012:role/NeverUsed\ncredential_source = Environment\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	factory, err := NewRuntimeFactory(filepath.Join(directory, "aws-must-not-run"), []string{
		"AWS_CONFIG_FILE=" + configPath,
		"AWS_SHARED_CREDENTIALS_FILE=" + filepath.Join(directory, "missing"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = factory.Resolve(context.Background(), ContextSpec{Mode: ContextModeNamedProfile, Profile: "dev", Region: "us-east-1"})
	if err == nil || !strings.Contains(err.Error(), "Environment is not allowed") {
		t.Fatalf("error=%v want profile source rejection", err)
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
