package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cloudfronttypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
)

var fixedNow = time.Date(2026, 8, 28, 3, 0, 0, 0, time.UTC)

type fakeFactory struct {
	mu      sync.Mutex
	calls   int
	runtime awsbrowser.RuntimeContext
	err     error
}

type fakeProfileLister struct {
	profiles []string
	calls    int
}

func (lister *fakeProfileLister) ListProfiles(context.Context, []string) ([]string, error) {
	lister.calls++
	return append([]string(nil), lister.profiles...), nil
}

func (factory *fakeFactory) Resolve(ctx context.Context, _ awsbrowser.ContextSpec) (awsbrowser.RuntimeContext, error) {
	factory.mu.Lock()
	factory.calls++
	factory.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return factory.runtime, factory.err
}

func (factory *fakeFactory) count() int {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return factory.calls
}

type fakeRuntime struct {
	mu         sync.RWMutex
	identity   awsbrowser.VerifiedIdentity
	sts        awsbrowser.STSAPI
	ec2        awsbrowser.EC2API
	iam        awsbrowser.IAMAPI
	route53    awsbrowser.Route53API
	cloudfront awsbrowser.CloudFrontAPI
	elbv2      awsbrowser.ELBV2API
	s3         awsbrowser.S3API
}

func (runtime *fakeRuntime) Identity() awsbrowser.VerifiedIdentity {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.identity
}
func (runtime *fakeRuntime) setIdentity(identity awsbrowser.VerifiedIdentity) {
	runtime.mu.Lock()
	runtime.identity = identity
	runtime.mu.Unlock()
}
func (runtime *fakeRuntime) STS() awsbrowser.STSAPI               { return runtime.sts }
func (runtime *fakeRuntime) EC2() awsbrowser.EC2API               { return runtime.ec2 }
func (runtime *fakeRuntime) IAM() awsbrowser.IAMAPI               { return runtime.iam }
func (runtime *fakeRuntime) Route53() awsbrowser.Route53API       { return runtime.route53 }
func (runtime *fakeRuntime) CloudFront() awsbrowser.CloudFrontAPI { return runtime.cloudfront }
func (runtime *fakeRuntime) ELBV2() awsbrowser.ELBV2API           { return runtime.elbv2 }
func (runtime *fakeRuntime) S3() awsbrowser.S3API                 { return runtime.s3 }

type fakeSTS struct{ awsbrowser.STSAPI }

func (fakeSTS) GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput) (*sts.GetCallerIdentityOutput, error) {
	panic("unexpected STS call")
}

type fakeEC2 struct {
	awsbrowser.EC2API
	describeInstances func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error)
}

func (client *fakeEC2) DescribeInstances(ctx context.Context, input *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
	if client.describeInstances == nil {
		panic("unexpected EC2 call")
	}
	return client.describeInstances(ctx, input)
}

type fakeIAM struct {
	awsbrowser.IAMAPI
	listRoles func(context.Context, *iam.ListRolesInput) (*iam.ListRolesOutput, error)
}

func (client *fakeIAM) ListRoles(ctx context.Context, input *iam.ListRolesInput) (*iam.ListRolesOutput, error) {
	if client.listRoles == nil {
		panic("unexpected IAM call")
	}
	return client.listRoles(ctx, input)
}

type fakeRoute53 struct {
	awsbrowser.Route53API
	listHostedZones func(context.Context, *route53.ListHostedZonesInput) (*route53.ListHostedZonesOutput, error)
}

type fakeCloudFront struct {
	awsbrowser.CloudFrontAPI
	listDistributions func(context.Context, *cloudfront.ListDistributionsInput) (*cloudfront.ListDistributionsOutput, error)
}

func (client *fakeCloudFront) ListDistributions(ctx context.Context, input *cloudfront.ListDistributionsInput) (*cloudfront.ListDistributionsOutput, error) {
	if client.listDistributions == nil {
		panic("unexpected CloudFront call")
	}
	return client.listDistributions(ctx, input)
}

type fakeS3 struct {
	awsbrowser.S3API
	getBucketLocation func(context.Context, *s3.GetBucketLocationInput) (*s3.GetBucketLocationOutput, error)
}

type fakeELBV2 struct {
	awsbrowser.ELBV2API
	describeLoadBalancers func(context.Context, *elasticloadbalancingv2.DescribeLoadBalancersInput) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error)
}

func (client *fakeELBV2) DescribeLoadBalancers(ctx context.Context, input *elasticloadbalancingv2.DescribeLoadBalancersInput) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error) {
	if client.describeLoadBalancers == nil {
		panic("unexpected ELBV2 call")
	}
	return client.describeLoadBalancers(ctx, input)
}

func (client *fakeS3) GetBucketLocation(ctx context.Context, input *s3.GetBucketLocationInput) (*s3.GetBucketLocationOutput, error) {
	if client.getBucketLocation == nil {
		panic("unexpected S3 call")
	}
	return client.getBucketLocation(ctx, input)
}

func (client *fakeRoute53) ListHostedZones(ctx context.Context, input *route53.ListHostedZonesInput) (*route53.ListHostedZonesOutput, error) {
	if client.listHostedZones == nil {
		panic("unexpected Route 53 call")
	}
	return client.listHostedZones(ctx, input)
}

func testIdentity(generation uint64) awsbrowser.VerifiedIdentity {
	return awsbrowser.VerifiedIdentity{
		Partition: "aws", AccountID: "123456789012",
		PrincipalARN:         "arn:aws:sts::123456789012:assumed-role/ReadOnly/session",
		CredentialGeneration: generation,
	}
}

func completeRuntime(identity awsbrowser.VerifiedIdentity, ec2Client awsbrowser.EC2API, iamClient awsbrowser.IAMAPI, route53Client awsbrowser.Route53API) *fakeRuntime {
	if ec2Client == nil {
		ec2Client = &fakeEC2{}
	}
	if iamClient == nil {
		iamClient = &fakeIAM{}
	}
	if route53Client == nil {
		route53Client = &fakeRoute53{}
	}
	return &fakeRuntime{identity: identity, sts: fakeSTS{}, ec2: ec2Client, iam: iamClient, route53: route53Client}
}

func TestProductionConstructionIsZeroCall(t *testing.T) {
	core, err := NewProduction(ProductionOptions{
		AWSCLIPath: "/path/that/must/not/be-resolved/or/executed/aws",
		Env:        []string{"AWS_REGION=us-east-1"}, Clock: func() time.Time { return fixedNow }, Concurrency: 2,
	})
	if err != nil || core == nil {
		t.Fatalf("core=%v error=%v", core, err)
	}

	factory := &fakeFactory{runtime: completeRuntime(testIdentity(1), nil, nil, nil)}
	core, err = NewWithRuntimeFactory(factory, []string{"AWS_REGION=us-east-1"}, func() time.Time { return fixedNow }, 2)
	if err != nil || core == nil {
		t.Fatalf("core=%v error=%v", core, err)
	}
	if factory.count() != 0 {
		t.Fatalf("construction resolved runtime %d times", factory.count())
	}
}

func TestContextChoicesReadConfiguredProfilesAndRegionsWithoutResolvingAccounts(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config")
	if err := os.WriteFile(configPath, []byte("[profile dev]\nregion = us-east-1\n[profile prod]\nregion = ap-northeast-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	factory := &fakeFactory{runtime: completeRuntime(testIdentity(1), nil, nil, nil)}
	lister := &fakeProfileLister{profiles: []string{"dev", "prod"}}
	core, err := newCore(
		factory,
		[]string{"AWS_CONFIG_FILE=" + configPath},
		func() time.Time { return fixedNow },
		2,
		lister,
	)
	if err != nil {
		t.Fatal(err)
	}
	choices, err := core.ListContexts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []awsbrowser.ContextChoice{{Profile: "dev", Region: "us-east-1"}, {Profile: "prod", Region: "ap-northeast-2"}}
	if !reflect.DeepEqual(choices, want) || lister.calls != 1 || factory.count() != 0 {
		t.Fatalf("choices=%+v calls=%d runtime_calls=%d", choices, lister.calls, factory.count())
	}
}

func TestNewWithRuntimeFactoryRejectsTypedNilFactory(t *testing.T) {
	var factory *fakeFactory
	core, err := NewWithRuntimeFactory(factory, nil, func() time.Time { return fixedNow }, 1)
	if !errors.Is(err, ErrInvalidOptions) || core != nil {
		t.Fatalf("core=%v error=%v", core, err)
	}
}

func TestTypedNilRuntimeBecomesSanitizedFailure(t *testing.T) {
	var runtime *fakeRuntime
	factory := &fakeFactory{runtime: runtime}
	core, err := NewWithRuntimeFactory(factory, nil, func() time.Time { return fixedNow }, 1)
	if err != nil {
		t.Fatal(err)
	}
	result, resolveErr := core.Resolve(context.Background(), ContextRequest{Region: "us-east-1"})
	if resolveErr == nil || result.Failure == nil || result.Failure.Kind != awsbrowser.ProviderUnknown {
		t.Fatalf("result=%+v error=%v", result, resolveErr)
	}
	if factory.count() != 1 {
		t.Fatalf("factory calls=%d", factory.count())
	}
}

func TestTypedNilNarrowedClientsBecomeSanitizedFailures(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		operation string
		runtime   func() awsbrowser.RuntimeContext
	}{
		{
			name: "EC2", provider: awsbrowser.ProviderEC2, operation: awsbrowser.OperationDescribeInstances,
			runtime: func() awsbrowser.RuntimeContext {
				var client *fakeEC2
				return &fakeRuntime{identity: testIdentity(1), sts: fakeSTS{}, ec2: client, iam: &fakeIAM{}, route53: &fakeRoute53{}}
			},
		},
		{
			name: "IAM", provider: awsbrowser.ProviderIAM, operation: awsbrowser.OperationListRoles,
			runtime: func() awsbrowser.RuntimeContext {
				var client *fakeIAM
				return &fakeRuntime{identity: testIdentity(1), sts: fakeSTS{}, ec2: &fakeEC2{}, iam: client, route53: &fakeRoute53{}}
			},
		},
		{
			name: "Route 53", provider: awsbrowser.ProviderRoute53, operation: awsbrowser.OperationListHostedZones,
			runtime: func() awsbrowser.RuntimeContext {
				var client *fakeRoute53
				return &fakeRuntime{identity: testIdentity(1), sts: fakeSTS{}, ec2: &fakeEC2{}, iam: &fakeIAM{}, route53: client}
			},
		},
		{
			name: "CloudFront", provider: awsbrowser.ProviderCloudFront, operation: awsbrowser.OperationListDistributions,
			runtime: func() awsbrowser.RuntimeContext {
				var client *fakeCloudFront
				return &fakeRuntime{identity: testIdentity(1), sts: fakeSTS{}, cloudfront: client}
			},
		},
		{
			name: "S3", provider: awsbrowser.ProviderS3, operation: awsbrowser.OperationGetBucketLocation,
			runtime: func() awsbrowser.RuntimeContext {
				var client *fakeS3
				return &fakeRuntime{identity: testIdentity(1), sts: fakeSTS{}, s3: client}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			core, err := NewWithRuntimeFactory(&fakeFactory{runtime: test.runtime()}, nil, func() time.Time { return fixedNow }, 1)
			if err != nil {
				t.Fatal(err)
			}
			params := map[string]string(nil)
			if test.provider == awsbrowser.ProviderCloudFront {
				params = map[string]string{"distribution-domain": "example.cloudfront.net"}
			} else if test.provider == awsbrowser.ProviderS3 {
				params = map[string]string{"bucket": "valid-bucket"}
			}
			result, queryErr := core.Query(context.Background(), Request{
				Region: "us-east-1", Provider: test.provider, Operation: test.operation, Params: params,
			})
			if queryErr == nil || result.Update.Failure == nil || result.Update.Failure.Kind != awsbrowser.ProviderUnsupported {
				t.Fatalf("result=%+v error=%v", result, queryErr)
			}
		})
	}
}

func TestInvalidExplicitContextInputMakesNoRuntimeCalls(t *testing.T) {
	for _, request := range []ContextRequest{
		{Profile: "bad profile", Region: "us-east-1"},
		{Profile: "dev", Region: "--region"},
	} {
		t.Run(request.Profile+request.Region, func(t *testing.T) {
			factory := &fakeFactory{runtime: completeRuntime(testIdentity(1), nil, nil, nil)}
			core, err := NewWithRuntimeFactory(factory, nil, func() time.Time { return fixedNow }, 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := core.Resolve(context.Background(), request); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Resolve error=%v", err)
			}
			if _, err := core.Subscribe(context.Background(), Request{
				Profile: request.Profile, Region: request.Region,
				Provider: awsbrowser.ProviderIAM, Operation: awsbrowser.OperationListRoles,
			}); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Subscribe error=%v", err)
			}
			if factory.count() != 0 {
				t.Fatalf("factory calls=%d", factory.count())
			}
		})
	}
}

func TestQuerySelectsOnlyRequestedProvider(t *testing.T) {
	var iamCalls int
	iamClient := &fakeIAM{listRoles: func(_ context.Context, input *iam.ListRolesInput) (*iam.ListRolesOutput, error) {
		iamCalls++
		if aws.ToInt32(input.MaxItems) != 1000 {
			t.Fatalf("MaxItems=%d", aws.ToInt32(input.MaxItems))
		}
		return &iam.ListRolesOutput{Roles: []iamtypes.Role{{
			RoleName: aws.String("ReadOnly"), RoleId: aws.String("AROAEXAMPLE"),
			Arn: aws.String("arn:aws:iam::123456789012:role/ReadOnly"), Path: aws.String("/"),
			CreateDate: aws.Time(fixedNow),
		}}}, nil
	}}
	factory := &fakeFactory{runtime: completeRuntime(testIdentity(1), &fakeEC2{}, iamClient, &fakeRoute53{})}
	core, err := NewWithRuntimeFactory(factory, nil, func() time.Time { return fixedNow }, 2)
	if err != nil {
		t.Fatal(err)
	}

	result, err := core.Query(context.Background(), Request{
		Region: "us-east-1", Provider: awsbrowser.ProviderIAM, Operation: awsbrowser.OperationListRoles,
	})
	if err != nil {
		t.Fatal(err)
	}
	if iamCalls != 1 || result.Update.Snapshot.ResourceCount() != 1 || !result.Update.Coverage.Completed {
		t.Fatalf("iam calls=%d resources=%d coverage=%+v", iamCalls, result.Update.Snapshot.ResourceCount(), result.Update.Coverage)
	}
	if factory.count() != 1 {
		t.Fatalf("registry did not memoize runtime: resolves=%d", factory.count())
	}
}

func TestCloudFrontAndS3QueriesReachNarrowedProviders(t *testing.T) {
	cloudFrontCalls, s3Calls := 0, 0
	runtime := completeRuntime(testIdentity(1), nil, nil, nil)
	runtime.cloudfront = &fakeCloudFront{listDistributions: func(_ context.Context, input *cloudfront.ListDistributionsInput) (*cloudfront.ListDistributionsOutput, error) {
		cloudFrontCalls++
		if aws.ToInt32(input.MaxItems) != 100 {
			t.Fatalf("CloudFront input=%#v", input)
		}
		return &cloudfront.ListDistributionsOutput{DistributionList: &cloudfronttypes.DistributionList{
			IsTruncated: aws.Bool(false), Marker: aws.String(""), MaxItems: aws.Int32(100), Quantity: aws.Int32(0),
		}}, nil
	}}
	runtime.s3 = &fakeS3{getBucketLocation: func(_ context.Context, input *s3.GetBucketLocationInput) (*s3.GetBucketLocationOutput, error) {
		s3Calls++
		if aws.ToString(input.Bucket) != "udg-kr-game-binary" {
			t.Fatalf("S3 input=%#v", input)
		}
		return &s3.GetBucketLocationOutput{LocationConstraint: s3types.BucketLocationConstraintApNortheast2}, nil
	}}
	core, err := NewWithRuntimeFactory(&fakeFactory{runtime: runtime}, nil, func() time.Time { return fixedNow }, 1)
	if err != nil {
		t.Fatal(err)
	}
	cloudFrontResult, err := core.Query(context.Background(), Request{
		Region: "ap-northeast-2", Provider: awsbrowser.ProviderCloudFront, Operation: awsbrowser.OperationListDistributions,
		Params: map[string]string{"distribution-domain": "d24odq2ocbsmjd.cloudfront.net"},
	})
	if err != nil || !cloudFrontResult.Update.Coverage.Completed || cloudFrontResult.Update.Snapshot.ResourceCount() != 0 {
		t.Fatalf("CloudFront result=%+v error=%v", cloudFrontResult, err)
	}
	s3Result, err := core.Query(context.Background(), Request{
		Region: "ap-northeast-2", Provider: awsbrowser.ProviderS3, Operation: awsbrowser.OperationGetBucketLocation,
		Params: map[string]string{"bucket": "udg-kr-game-binary"},
	})
	if err != nil || !s3Result.Update.Coverage.Completed || s3Result.Update.Snapshot.ResourceCount() != 1 || cloudFrontCalls != 1 || s3Calls != 1 {
		t.Fatalf("S3 result=%+v error=%v calls=%d/%d", s3Result, err, cloudFrontCalls, s3Calls)
	}
}

func TestELBV2QueryReachesNarrowedReadProvider(t *testing.T) {
	const loadBalancerARN = "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/api/123"
	calls := 0
	runtime := completeRuntime(testIdentity(1), nil, nil, nil)
	runtime.elbv2 = &fakeELBV2{describeLoadBalancers: func(_ context.Context, input *elasticloadbalancingv2.DescribeLoadBalancersInput) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error) {
		calls++
		if len(input.LoadBalancerArns) != 1 || input.LoadBalancerArns[0] != loadBalancerARN {
			t.Fatalf("input=%+v", input)
		}
		return &elasticloadbalancingv2.DescribeLoadBalancersOutput{LoadBalancers: []elbv2types.LoadBalancer{{
			LoadBalancerArn: aws.String(loadBalancerARN), LoadBalancerName: aws.String("api"),
			DNSName: aws.String("api-123.us-east-1.elb.amazonaws.com"), Type: elbv2types.LoadBalancerTypeEnumApplication,
			Scheme: elbv2types.LoadBalancerSchemeEnumInternetFacing, IpAddressType: elbv2types.IpAddressTypeIpv4,
			State: &elbv2types.LoadBalancerState{Code: elbv2types.LoadBalancerStateEnumActive},
		}}}, nil
	}}
	core, err := NewWithRuntimeFactory(&fakeFactory{runtime: runtime}, nil, func() time.Time { return fixedNow }, 1)
	if err != nil {
		t.Fatal(err)
	}
	result, err := core.Query(context.Background(), Request{
		Region: "us-east-1", Provider: awsbrowser.ProviderELBV2, Operation: awsbrowser.OperationDescribeLoadBalancers,
		Params: map[string]string{"load-balancer-arn": loadBalancerARN},
	})
	if err != nil || calls != 1 || result.Update.Snapshot.ResourceCount() != 1 || !result.Update.Coverage.Completed {
		t.Fatalf("calls=%d resources=%d coverage=%+v error=%v", calls, result.Update.Snapshot.ResourceCount(), result.Update.Coverage, err)
	}
}

type sinkStub struct{}

func (sinkStub) Page(awsbrowser.QueryPage) error { return nil }
func (sinkStub) Complete(time.Time) error        { return nil }

func TestMultiplexerRejectsStaleVerifiedIdentity(t *testing.T) {
	var calls int
	iamClient := &fakeIAM{listRoles: func(context.Context, *iam.ListRolesInput) (*iam.ListRolesOutput, error) {
		calls++
		return &iam.ListRolesOutput{}, nil
	}}
	runtime := completeRuntime(testIdentity(2), nil, iamClient, nil)
	registry := awsbrowser.NewContextRegistry(&fakeFactory{runtime: runtime})
	multiplexer := &runtimeMultiplexer{registry: registry, clock: func() time.Time { return fixedNow }}
	spec := awsbrowser.ContextSpec{Mode: awsbrowser.ContextModeAmbient, Region: "us-east-1"}
	awsContext, err := awsbrowser.NewAWSContext(spec, testIdentity(1), "ReadOnly")
	if err != nil {
		t.Fatal(err)
	}
	key, err := awsbrowser.NewQueryKey(awsContext, awsbrowser.ProviderIAM, awsbrowser.OperationListRoles, nil)
	if err != nil {
		t.Fatal(err)
	}

	err = multiplexer.Execute(context.Background(), key, sinkStub{})
	if !errors.Is(err, awsbrowser.ErrContextChanged) || calls != 0 {
		t.Fatalf("error=%v provider calls=%d", err, calls)
	}
}

func TestQueryRejectsGenerationCrossingBeforeFirstPageCommit(t *testing.T) {
	var runtime *fakeRuntime
	calls := 0
	client := &fakeEC2{describeInstances: func(ctx context.Context, _ *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		calls++
		runtime.setIdentity(testIdentity(2))
		if err := awsbrowser.ValidateReadIdentity(ctx, runtime.Identity()); err != nil {
			return nil, err
		}
		return instancePage("i-stale", ""), nil
	}}
	runtime = completeRuntime(testIdentity(1), client, nil, nil)
	core, err := NewWithRuntimeFactory(&fakeFactory{runtime: runtime}, nil, func() time.Time { return fixedNow }, 1)
	if err != nil {
		t.Fatal(err)
	}

	result, queryErr := core.Query(context.Background(), Request{
		Region: "us-east-1", Provider: awsbrowser.ProviderEC2, Operation: awsbrowser.OperationDescribeInstances,
	})
	if queryErr == nil || result.Update.Failure == nil || result.Update.Failure.Kind != awsbrowser.ProviderContextChanged ||
		result.Update.Snapshot.ResourceCount() != 0 || calls != 1 {
		t.Fatalf("result=%+v error=%v calls=%d", result, queryErr, calls)
	}
}

func TestQueryRejectsGenerationCrossingOnSubsequentPageAndReResolves(t *testing.T) {
	var runtime *fakeRuntime
	calls := 0
	client := &fakeEC2{describeInstances: func(ctx context.Context, _ *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		calls++
		switch calls {
		case 1:
			if err := awsbrowser.ValidateReadIdentity(ctx, runtime.Identity()); err != nil {
				return nil, err
			}
			return instancePage("i-first", "next"), nil
		case 2:
			runtime.setIdentity(testIdentity(2))
			if err := awsbrowser.ValidateReadIdentity(ctx, runtime.Identity()); err != nil {
				return nil, err
			}
			return instancePage("i-stale", ""), nil
		default:
			if err := awsbrowser.ValidateReadIdentity(ctx, runtime.Identity()); err != nil {
				return nil, err
			}
			return instancePage("i-current", ""), nil
		}
	}}
	runtime = completeRuntime(testIdentity(1), client, nil, nil)
	core, err := NewWithRuntimeFactory(&fakeFactory{runtime: runtime}, nil, func() time.Time { return fixedNow }, 1)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Region: "us-east-1", Provider: awsbrowser.ProviderEC2, Operation: awsbrowser.OperationDescribeInstances}

	first, firstErr := core.Query(context.Background(), request)
	if firstErr == nil || first.Update.Failure == nil || first.Update.Failure.Kind != awsbrowser.ProviderContextChanged ||
		first.Update.Snapshot.ResourceCount() != 0 {
		t.Fatalf("crossed result=%+v error=%v", first, firstErr)
	}
	second, secondErr := core.Query(context.Background(), request)
	if secondErr != nil || second.Update.Failure != nil || second.Update.Key == nil ||
		second.Update.Key.Context.CredentialGen != 2 || second.Update.Snapshot.ResourceCount() != 1 || calls != 3 {
		t.Fatalf("retried result=%+v error=%v calls=%d", second, secondErr, calls)
	}
}

func instancePage(id, next string) *ec2.DescribeInstancesOutput {
	output := &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{{InstanceId: aws.String(id)}}}}}
	if next != "" {
		output.NextToken = aws.String(next)
	}
	return output
}

func TestCallerCancellationUnsubscribesAndReachesProvider(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	ec2Client := &fakeEC2{describeInstances: func(ctx context.Context, _ *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	}}
	factory := &fakeFactory{runtime: completeRuntime(testIdentity(1), ec2Client, nil, nil)}
	core, err := NewWithRuntimeFactory(factory, nil, func() time.Time { return fixedNow }, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	subscription, err := core.Subscribe(ctx, Request{
		Region: "us-east-1", Provider: awsbrowser.ProviderEC2, Operation: awsbrowser.OperationDescribeInstances,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}
	cancel()
	terminal := 0
	for update := range subscription.Updates() {
		if update.Failure != nil || update.Coverage.Completed {
			terminal++
		}
	}
	if terminal != 1 {
		t.Fatalf("terminal updates=%d want=1", terminal)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("provider did not observe cancellation")
	}
	// The public method remains idempotent after automatic one-shot cleanup.
	subscription.Unsubscribe()
}

func TestCompletedTerminalIsStickyAgainstLaterCancellation(t *testing.T) {
	ec2Client := &fakeEC2{describeInstances: func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		return &ec2.DescribeInstancesOutput{}, nil
	}}
	core, err := NewWithRuntimeFactory(
		&fakeFactory{runtime: completeRuntime(testIdentity(1), ec2Client, nil, nil)},
		nil, func() time.Time { return fixedNow }, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	for iteration := 0; iteration < 100; iteration++ {
		ctx, cancel := context.WithCancel(context.Background())
		subscription, err := core.Subscribe(ctx, Request{
			Region: "us-east-1", Provider: awsbrowser.ProviderEC2,
			Operation: awsbrowser.OperationDescribeInstances, Refresh: true,
		})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		terminal := 0
		var last Update
		for update := range subscription.Updates() {
			last = update
			if update.Failure != nil || update.Coverage.Completed {
				terminal++
				cancel()
			}
		}
		cancel()
		if terminal != 1 || last.Failure != nil || !last.Coverage.Completed {
			t.Fatalf("iteration=%d terminal=%d last=%+v", iteration, terminal, last)
		}
	}
}

func TestRuntimeFailureRetainsNoRawErrorOrSecret(t *testing.T) {
	const secret = "AKIA-SECRET raw stderr from helper"
	factory := &fakeFactory{err: errors.New(secret)}
	core, err := NewWithRuntimeFactory(factory, nil, func() time.Time { return fixedNow }, 1)
	if err != nil {
		t.Fatal(err)
	}
	result, queryErr := core.Query(context.Background(), Request{
		Region: "us-east-1", Provider: awsbrowser.ProviderIAM, Operation: awsbrowser.OperationListRoles,
	})
	if queryErr == nil || result.Update.Failure == nil {
		t.Fatalf("result=%+v error=%v", result, queryErr)
	}
	retained := fmt.Sprintf("%+v %v", result, queryErr)
	if strings.Contains(retained, secret) || strings.Contains(retained, "stderr") || queryErr.Error() != "AWS browser query failed" {
		t.Fatalf("unsafe retained failure: %s", retained)
	}
	if result.Update.Failure.Kind != awsbrowser.ProviderUnknown || result.Update.Coverage.ContextResolved {
		t.Fatalf("failure=%+v coverage=%+v", result.Update.Failure, result.Update.Coverage)
	}
}

func TestImmediateFailurePublishesTerminalSnapshot(t *testing.T) {
	failure := Failure{
		State:     awsbrowser.LoadUnsupported,
		Kind:      awsbrowser.ProviderUnsupported,
		Provider:  awsbrowser.ProviderEC2,
		Operation: awsbrowser.OperationDescribeInstances,
	}
	subscription := immediateFailure(failure)

	var updates []Update
	for update := range subscription.Updates() {
		updates = append(updates, update)
	}
	if len(updates) != 1 {
		t.Fatalf("updates=%d want=1", len(updates))
	}
	update := updates[0]
	if update.Failure == nil || update.Snapshot.State != awsbrowser.LoadUnsupported || !update.Coverage.Completed {
		t.Fatalf("update=%+v", update)
	}
}

func TestContextResolutionPrecedenceRemainsLazy(t *testing.T) {
	factory := &fakeFactory{runtime: completeRuntime(testIdentity(1), nil, nil, nil)}
	core, err := NewWithRuntimeFactory(factory, []string{"AWS_REGION=us-west-2", "AWS_DEFAULT_REGION=eu-west-1"}, func() time.Time { return fixedNow }, 1)
	if err != nil {
		t.Fatal(err)
	}
	if factory.count() != 0 {
		t.Fatal("constructor resolved context")
	}
	result, err := core.Resolve(context.Background(), ContextRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Context == nil || result.Context.Region != "us-west-2" || !result.Coverage.ContextResolved {
		t.Fatalf("result=%+v", result)
	}
}

func TestSharedConfigRegionUsesAcceptedINIGrammar(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "colon separator", body: "[profile dev]\nregion: us-west-2\n", want: "us-west-2"},
		{name: "double quoted", body: "[profile dev]\nregion = \"ap-northeast-2\"\n", want: "ap-northeast-2"},
		{name: "single quoted and comment", body: "[profile dev] # selected\nregion = 'eu-west-1' ; preferred\n", want: "eu-west-1"},
		{name: "last value wins", body: "[profile dev]\nregion = us-east-1\nregion: us-west-1\n", want: "us-west-1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config")
			if err := os.WriteFile(path, []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}
			resolver := newContextResolver([]string{"AWS_CONFIG_FILE=" + path})
			spec, err := resolver.Resolve(context.Background(), "dev", "")
			if err != nil || spec.Region != test.want || spec.Mode != awsbrowser.ContextModeNamedProfile {
				t.Fatalf("spec=%+v error=%v want region=%s", spec, err, test.want)
			}
		})
	}
}
