package awsbrowser

import (
	"context"
	"go/token"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type optionBearingSTS interface {
	GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

type mutatingSTS interface {
	AssumeRole(context.Context, *sts.AssumeRoleInput, ...func(*sts.Options)) (*sts.AssumeRoleOutput, error)
}

type optionBearingEC2 interface {
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

type mutatingEC2 interface {
	StartInstances(context.Context, *ec2.StartInstancesInput, ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error)
}

type optionBearingIAM interface {
	ListRoles(context.Context, *iam.ListRolesInput, ...func(*iam.Options)) (*iam.ListRolesOutput, error)
}

type mutatingIAM interface {
	CreateRole(context.Context, *iam.CreateRoleInput, ...func(*iam.Options)) (*iam.CreateRoleOutput, error)
}

type optionBearingRoute53 interface {
	ListHostedZones(context.Context, *route53.ListHostedZonesInput, ...func(*route53.Options)) (*route53.ListHostedZonesOutput, error)
}

type mutatingRoute53 interface {
	ChangeResourceRecordSets(context.Context, *route53.ChangeResourceRecordSetsInput, ...func(*route53.Options)) (*route53.ChangeResourceRecordSetsOutput, error)
}

type optionBearingCloudFront interface {
	ListDistributions(context.Context, *cloudfront.ListDistributionsInput, ...func(*cloudfront.Options)) (*cloudfront.ListDistributionsOutput, error)
}

type mutatingCloudFront interface {
	CreateDistribution(context.Context, *cloudfront.CreateDistributionInput, ...func(*cloudfront.Options)) (*cloudfront.CreateDistributionOutput, error)
}

type optionBearingELBV2 interface {
	DescribeLoadBalancers(context.Context, *elasticloadbalancingv2.DescribeLoadBalancersInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error)
}

type mutatingELBV2 interface {
	CreateLoadBalancer(context.Context, *elasticloadbalancingv2.CreateLoadBalancerInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.CreateLoadBalancerOutput, error)
}

type optionBearingS3 interface {
	GetBucketLocation(context.Context, *s3.GetBucketLocationInput, ...func(*s3.Options)) (*s3.GetBucketLocationOutput, error)
}

type mutatingS3 interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

func TestSDKRuntimeHasNoPublicEscapeHatches(t *testing.T) {
	typeOf := reflect.TypeOf(sdkRuntime{})
	if reflect.PointerTo(typeOf).Implements(reflect.TypeOf((*RuntimeContext)(nil)).Elem()) {
		t.Fatal("raw SDK runtime implements the public RuntimeContext")
	}
	for i := 0; i < typeOf.NumField(); i++ {
		field := typeOf.Field(i)
		if field.IsExported() {
			t.Errorf("exported sdkRuntime field %s exposes %v", field.Name, field.Type)
		}
	}

	approved := map[string]bool{
		"EC2":        true,
		"ELBV2":      true,
		"CloudFront": true,
		"IAM":        true,
		"Identity":   true,
		"Route53":    true,
		"S3":         true,
		"STS":        true,
	}
	runtimeContract := reflect.TypeOf((*RuntimeContext)(nil)).Elem()
	for i := 0; i < runtimeContract.NumMethod(); i++ {
		method := runtimeContract.Method(i)
		if !approved[method.Name] {
			t.Errorf("unapproved RuntimeContext method %s", method.Name)
		}
		delete(approved, method.Name)
	}
	for missing := range approved {
		t.Errorf("missing RuntimeContext method %s", missing)
	}
}

func TestRuntimeContextReturnsOnlyNarrowPrivateAdapters(t *testing.T) {
	runtime, _, _ := testVerifiedRuntime(t,
		&recordingCredentialExporter{output: validCredentialDocument()},
		func(int) *sts.GetCallerIdentityOutput {
			return callerIdentity("aws", "123456789012", "reader")
		},
		nil,
	)

	clients := []struct {
		name          string
		value         any
		concrete      reflect.Type
		contract      reflect.Type
		optionBearing func(any) bool
		mutating      func(any) bool
	}{
		{
			name:          "STS",
			value:         runtime.STS(),
			concrete:      reflect.TypeOf((*sts.Client)(nil)),
			contract:      reflect.TypeOf((*STSAPI)(nil)).Elem(),
			optionBearing: func(value any) bool { _, ok := value.(optionBearingSTS); return ok },
			mutating:      func(value any) bool { _, ok := value.(mutatingSTS); return ok },
		},
		{
			name:          "EC2",
			value:         runtime.EC2(),
			concrete:      reflect.TypeOf((*ec2.Client)(nil)),
			contract:      reflect.TypeOf((*EC2API)(nil)).Elem(),
			optionBearing: func(value any) bool { _, ok := value.(optionBearingEC2); return ok },
			mutating:      func(value any) bool { _, ok := value.(mutatingEC2); return ok },
		},
		{
			name:          "IAM",
			value:         runtime.IAM(),
			concrete:      reflect.TypeOf((*iam.Client)(nil)),
			contract:      reflect.TypeOf((*IAMAPI)(nil)).Elem(),
			optionBearing: func(value any) bool { _, ok := value.(optionBearingIAM); return ok },
			mutating:      func(value any) bool { _, ok := value.(mutatingIAM); return ok },
		},
		{
			name:          "Route53",
			value:         runtime.Route53(),
			concrete:      reflect.TypeOf((*route53.Client)(nil)),
			contract:      reflect.TypeOf((*Route53API)(nil)).Elem(),
			optionBearing: func(value any) bool { _, ok := value.(optionBearingRoute53); return ok },
			mutating:      func(value any) bool { _, ok := value.(mutatingRoute53); return ok },
		},
		{
			name:          "CloudFront",
			value:         runtime.CloudFront(),
			concrete:      reflect.TypeOf((*cloudfront.Client)(nil)),
			contract:      reflect.TypeOf((*CloudFrontAPI)(nil)).Elem(),
			optionBearing: func(value any) bool { _, ok := value.(optionBearingCloudFront); return ok },
			mutating:      func(value any) bool { _, ok := value.(mutatingCloudFront); return ok },
		},
		{
			name:          "ELBV2",
			value:         runtime.ELBV2(),
			concrete:      reflect.TypeOf((*elasticloadbalancingv2.Client)(nil)),
			contract:      reflect.TypeOf((*ELBV2API)(nil)).Elem(),
			optionBearing: func(value any) bool { _, ok := value.(optionBearingELBV2); return ok },
			mutating:      func(value any) bool { _, ok := value.(mutatingELBV2); return ok },
		},
		{
			name:          "S3",
			value:         runtime.S3(),
			concrete:      reflect.TypeOf((*s3.Client)(nil)),
			contract:      reflect.TypeOf((*S3API)(nil)).Elem(),
			optionBearing: func(value any) bool { _, ok := value.(optionBearingS3); return ok },
			mutating:      func(value any) bool { _, ok := value.(mutatingS3); return ok },
		},
	}

	for _, client := range clients {
		t.Run(client.name, func(t *testing.T) {
			dynamic := reflect.TypeOf(client.value)
			if dynamic == client.concrete {
				t.Fatalf("RuntimeContext returned concrete SDK client %v", dynamic)
			}
			if dynamic.PkgPath() == "" || dynamic.Name() == "" || token.IsExported(dynamic.Name()) {
				t.Fatalf("RuntimeContext returned non-private adapter %v", dynamic)
			}
			if dynamic.NumMethod() != client.contract.NumMethod() {
				t.Fatalf("adapter methods=%d allowlisted methods=%d", dynamic.NumMethod(), client.contract.NumMethod())
			}
			if client.optionBearing(client.value) {
				t.Fatal("adapter satisfies option-bearing SDK call interface")
			}
			if client.mutating(client.value) {
				t.Fatal("adapter satisfies representative mutation interface")
			}
		})
	}
}
