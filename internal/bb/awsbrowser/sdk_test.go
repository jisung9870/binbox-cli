package awsbrowser

import (
	"context"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type staticCredentialCLI struct{}

func (staticCredentialCLI) ListProfiles(context.Context, []string) ([]string, error) {
	return nil, nil
}

func (staticCredentialCLI) ExportCredentials(context.Context, string, []string) ([]byte, error) {
	return []byte(`{"Version":1,"AccessKeyId":"AKIDEXAMPLE","SecretAccessKey":"secret"}`), nil
}

func testCredentialProvider(t *testing.T) *CredentialProvider {
	t.Helper()
	provider, err := NewCredentialProvider(staticCredentialCLI{}, "", []string{})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestSDKRuntimeUsesDefaultEndpointsAndFreshRetryers(t *testing.T) {
	t.Setenv("AWS_MAX_ATTEMPTS", "9")
	t.Setenv("AWS_RETRY_MODE", "adaptive")
	runtime, err := newSDKRuntime(context.Background(), "us-east-1", testCredentialProvider(t), config.LoadDefaultConfig)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.credentials == nil || runtime.credentialSource == nil {
		t.Fatal("private credential state was not retained")
	}

	stsClient, ok := runtime.sts.(*sts.Client)
	if !ok {
		t.Fatalf("STS client type=%T", runtime.sts)
	}
	ec2Client, ok := runtime.ec2.(*ec2.Client)
	if !ok {
		t.Fatalf("EC2 client type=%T", runtime.ec2)
	}
	iamClient, ok := runtime.iam.(*iam.Client)
	if !ok {
		t.Fatalf("IAM client type=%T", runtime.iam)
	}
	route53Client, ok := runtime.route53.(*route53.Client)
	if !ok {
		t.Fatalf("Route53 client type=%T", runtime.route53)
	}

	stsOptions := stsClient.Options()
	ec2Options := ec2Client.Options()
	iamOptions := iamClient.Options()
	route53Options := route53Client.Options()

	services := []struct {
		name            string
		baseEndpointNil bool
		resolver        any
		defaultResolver any
		retryer         any
		maxAttempts     int
	}{
		{"STS", stsOptions.BaseEndpoint == nil, stsOptions.EndpointResolverV2, sts.NewDefaultEndpointResolverV2(), stsOptions.Retryer, stsOptions.Retryer.MaxAttempts()},
		{"EC2", ec2Options.BaseEndpoint == nil, ec2Options.EndpointResolverV2, ec2.NewDefaultEndpointResolverV2(), ec2Options.Retryer, ec2Options.Retryer.MaxAttempts()},
		{"IAM", iamOptions.BaseEndpoint == nil, iamOptions.EndpointResolverV2, iam.NewDefaultEndpointResolverV2(), iamOptions.Retryer, iamOptions.Retryer.MaxAttempts()},
		{"Route53", route53Options.BaseEndpoint == nil, route53Options.EndpointResolverV2, route53.NewDefaultEndpointResolverV2(), route53Options.Retryer, route53Options.Retryer.MaxAttempts()},
	}

	retryerPointers := make(map[uintptr]string, len(services))
	for _, service := range services {
		if !service.baseEndpointNil {
			t.Errorf("%s BaseEndpoint is configured", service.name)
		}
		if got, want := reflect.TypeOf(service.resolver), reflect.TypeOf(service.defaultResolver); got != want {
			t.Errorf("%s resolver type=%v want=%v", service.name, got, want)
		}
		if service.maxAttempts != 3 {
			t.Errorf("%s max attempts=%d", service.name, service.maxAttempts)
		}
		pointer := reflect.ValueOf(service.retryer).Pointer()
		if previous, exists := retryerPointers[pointer]; exists {
			t.Errorf("%s shares retryer with %s", service.name, previous)
		}
		retryerPointers[pointer] = service.name
	}
}
