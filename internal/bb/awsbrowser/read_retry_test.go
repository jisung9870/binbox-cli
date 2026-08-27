package awsbrowser

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

func testVerifiedRuntime(t *testing.T, exporter CredentialExporter, identity func(int) *sts.GetCallerIdentityOutput, ec2Client rawEC2API) (RuntimeContext, *CredentialProvider, *credentialSTS) {
	t.Helper()
	provider, err := NewCredentialProvider(exporter, "dev", []string{})
	if err != nil {
		t.Fatal(err)
	}
	runtime, stsClient := fakeSDKRuntime(provider, identity, ec2Client)
	verified, err := resolveRuntimeIdentity(context.Background(), runtime)
	if err != nil {
		t.Fatal(err)
	}
	return newVerifiedRuntime(runtime, verified), provider, stsClient
}

func TestReadRetriesExpiredTokenExactlyOnceAfterIdentityRevalidation(t *testing.T) {
	exporter := &recordingCredentialExporter{output: validCredentialDocument()}
	var provider *CredentialProvider
	firstError := &smithy.GenericAPIError{Code: "ExpiredToken", Message: "expired"}
	want := &ec2.DescribeInstancesOutput{}
	calls := 0
	client := &ec2Stub{describeInstances: func(context.Context) (*ec2.DescribeInstancesOutput, error) {
		calls++
		if calls == 1 {
			return nil, firstError
		}
		return want, nil
	}}
	runtime, gotProvider, stsClient := testVerifiedRuntime(t, exporter, func(int) *sts.GetCallerIdentityOutput {
		return callerIdentity("aws", "123456789012", "reader")
	}, client)
	provider = gotProvider

	got, err := runtime.EC2().DescribeInstances(context.Background(), &ec2.DescribeInstancesInput{})
	if err != nil {
		t.Fatal(err)
	}
	if got != want || calls != 2 {
		t.Fatalf("output=%p want=%p calls=%d", got, want, calls)
	}
	if got := provider.Generation(); got != 2 {
		t.Fatalf("generation=%d want=2", got)
	}
	if got := runtime.Identity().CredentialGeneration; got != 2 {
		t.Fatalf("verified generation=%d want=2", got)
	}
	if got := exporter.calls(); len(got) != 2 {
		t.Fatalf("credential exports=%v want two", got)
	}
	if got := stsClient.callCount(); got != 2 {
		t.Fatalf("STS calls=%d want initial plus revalidation", got)
	}
}

func TestReadReturnsRepeatedExpiredTokenWithoutThirdCall(t *testing.T) {
	exporter := &recordingCredentialExporter{output: validCredentialDocument()}
	firstError := &smithy.GenericAPIError{Code: "ExpiredToken", Message: "first"}
	secondError := &smithy.GenericAPIError{Code: "ExpiredToken", Message: "second"}
	calls := 0
	client := &ec2Stub{describeInstances: func(context.Context) (*ec2.DescribeInstancesOutput, error) {
		calls++
		if calls == 1 {
			return nil, firstError
		}
		return nil, secondError
	}}
	runtime, _, _ := testVerifiedRuntime(t, exporter, func(int) *sts.GetCallerIdentityOutput {
		return callerIdentity("aws", "123456789012", "reader")
	}, client)

	_, err := runtime.EC2().DescribeInstances(context.Background(), &ec2.DescribeInstancesInput{})
	if err != secondError || calls != 2 {
		t.Fatalf("error=%v calls=%d want second typed error after two calls", err, calls)
	}
}

func TestReadDoesNotRetryUntypedExpiredTokenText(t *testing.T) {
	exporter := &recordingCredentialExporter{output: validCredentialDocument()}
	wantErr := errors.New("ExpiredToken")
	calls := 0
	client := &ec2Stub{describeInstances: func(context.Context) (*ec2.DescribeInstancesOutput, error) {
		calls++
		return nil, wantErr
	}}
	runtime, _, _ := testVerifiedRuntime(t, exporter, func(int) *sts.GetCallerIdentityOutput {
		return callerIdentity("aws", "123456789012", "reader")
	}, client)

	_, err := runtime.EC2().DescribeInstances(context.Background(), &ec2.DescribeInstancesInput{})
	if err != wantErr || calls != 1 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}

func TestReadDiscardsGenerationCrossingAndRetriesAfterSameAccountRevalidation(t *testing.T) {
	exporter := &recordingCredentialExporter{output: validCredentialDocument()}
	first := &ec2.DescribeInstancesOutput{}
	second := &ec2.DescribeInstancesOutput{}
	var provider *CredentialProvider
	calls := 0
	client := &ec2Stub{describeInstances: func(context.Context) (*ec2.DescribeInstancesOutput, error) {
		calls++
		if calls == 1 {
			provider.generation.Add(1)
			return first, nil
		}
		return second, nil
	}}
	runtime, gotProvider, stsClient := testVerifiedRuntime(t, exporter, func(call int) *sts.GetCallerIdentityOutput {
		return callerIdentity("aws", "123456789012", "reader-generation-"+strconv.Itoa(call))
	}, client)
	provider = gotProvider

	got, err := runtime.EC2().DescribeInstances(context.Background(), &ec2.DescribeInstancesInput{})
	if err != nil {
		t.Fatal(err)
	}
	if got != second || got == first || calls != 2 {
		t.Fatalf("stale response was committed: got=%p first=%p second=%p calls=%d", got, first, second, calls)
	}
	identity := runtime.Identity()
	if identity.CredentialGeneration != 2 || identity.PrincipalARN != "arn:aws:sts::123456789012:assumed-role/ReadOnly/reader-generation-2" {
		t.Fatalf("identity=%+v", identity)
	}
	if stsClient.callCount() != 2 {
		t.Fatalf("STS calls=%d want initial plus revalidation", stsClient.callCount())
	}
}

func TestReadRejectsAccountOrPartitionChangeBeforeCommit(t *testing.T) {
	tests := []struct {
		name      string
		partition string
		account   string
	}{
		{name: "account", partition: "aws", account: "210987654321"},
		{name: "partition", partition: "aws-us-gov", account: "123456789012"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			exporter := &recordingCredentialExporter{output: validCredentialDocument()}
			var provider *CredentialProvider
			calls := 0
			client := &ec2Stub{describeInstances: func(context.Context) (*ec2.DescribeInstancesOutput, error) {
				calls++
				provider.generation.Add(1)
				return &ec2.DescribeInstancesOutput{}, nil
			}}
			runtime, gotProvider, _ := testVerifiedRuntime(t, exporter, func(call int) *sts.GetCallerIdentityOutput {
				if call == 1 {
					return callerIdentity("aws", "123456789012", "reader")
				}
				return callerIdentity(test.partition, test.account, "reader")
			}, client)
			provider = gotProvider

			output, err := runtime.EC2().DescribeInstances(context.Background(), &ec2.DescribeInstancesInput{})
			if output != nil || !errors.Is(err, ErrContextChanged) || calls != 1 {
				t.Fatalf("output=%v error=%v calls=%d", output, err, calls)
			}
			if _, err := runtime.EC2().DescribeInstances(context.Background(), &ec2.DescribeInstancesInput{}); !errors.Is(err, ErrContextChanged) {
				t.Fatalf("poisoned runtime error=%v", err)
			}
			if calls != 1 {
				t.Fatalf("poisoned runtime made another resource call: %d", calls)
			}
		})
	}
}

func TestReadRejectsSecondGenerationCrossing(t *testing.T) {
	exporter := &recordingCredentialExporter{output: validCredentialDocument()}
	var provider *CredentialProvider
	calls := 0
	client := &ec2Stub{describeInstances: func(context.Context) (*ec2.DescribeInstancesOutput, error) {
		calls++
		provider.generation.Add(1)
		return &ec2.DescribeInstancesOutput{}, nil
	}}
	runtime, gotProvider, stsClient := testVerifiedRuntime(t, exporter, func(int) *sts.GetCallerIdentityOutput {
		return callerIdentity("aws", "123456789012", "reader")
	}, client)
	provider = gotProvider

	output, err := runtime.EC2().DescribeInstances(context.Background(), &ec2.DescribeInstancesInput{})
	if output != nil || !errors.Is(err, ErrContextChanged) || calls != 2 {
		t.Fatalf("output=%v error=%v calls=%d", output, err, calls)
	}
	if stsClient.callCount() != 3 {
		t.Fatalf("STS calls=%d want both generation changes revalidated", stsClient.callCount())
	}
}

func TestReadPassesCancellationToBlockingSDKClient(t *testing.T) {
	exporter := &recordingCredentialExporter{output: validCredentialDocument()}
	started := make(chan struct{})
	var once sync.Once
	client := &ec2Stub{describeInstances: func(ctx context.Context) (*ec2.DescribeInstancesOutput, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	runtime, _, _ := testVerifiedRuntime(t, exporter, func(int) *sts.GetCallerIdentityOutput {
		return callerIdentity("aws", "123456789012", "reader")
	}, client)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := runtime.EC2().DescribeInstances(ctx, &ec2.DescribeInstancesInput{})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("SDK client did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v want context cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SDK client did not stop after cancellation")
	}
}
