package providers

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
)

type fakeS3 struct {
	get func(context.Context, *s3.GetBucketLocationInput) (*s3.GetBucketLocationOutput, error)
}

func (fake *fakeS3) GetBucketLocation(ctx context.Context, input *s3.GetBucketLocationInput) (*s3.GetBucketLocationOutput, error) {
	return fake.get(ctx, input)
}

func TestS3GetsVerifiedBucketRegion(t *testing.T) {
	fake := &fakeS3{get: func(_ context.Context, input *s3.GetBucketLocationInput) (*s3.GetBucketLocationOutput, error) {
		if input.Bucket == nil || *input.Bucket != "udg-kr-game-binary" {
			t.Fatalf("input=%#v", input)
		}
		return &s3.GetBucketLocationOutput{LocationConstraint: s3types.BucketLocationConstraintApNortheast2}, nil
	}}
	executor, err := NewS3(fake, fixedIAMClock)
	if err != nil {
		t.Fatal(err)
	}
	key, err := awsbrowser.NewQueryKey(testIAMContext(t), awsbrowser.ProviderS3, awsbrowser.OperationGetBucketLocation, map[string]string{"bucket": "udg-kr-game-binary"})
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingSink{}
	if err := executor.Execute(context.Background(), key, sink); err != nil {
		t.Fatal(err)
	}
	resource := sink.pages[0].Resources()[0]
	fields := resource.Observation.Fields()
	if resource.Key.Type != "s3.bucket" || resource.Key.ID != "udg-kr-game-binary" || fields["region"] != "ap-northeast-2" {
		t.Fatalf("resource=%+v fields=%v", resource, fields)
	}
}

func TestS3NormalizesLegacyLocationsAndRejectsInvalidBucket(t *testing.T) {
	fake := &fakeS3{get: func(context.Context, *s3.GetBucketLocationInput) (*s3.GetBucketLocationOutput, error) {
		return &s3.GetBucketLocationOutput{}, nil
	}}
	executor, _ := NewS3(fake, fixedIAMClock)
	key, _ := awsbrowser.NewQueryKey(testIAMContext(t), awsbrowser.ProviderS3, awsbrowser.OperationGetBucketLocation, map[string]string{"bucket": "valid-bucket"})
	sink := &recordingSink{}
	if err := executor.Execute(context.Background(), key, sink); err != nil {
		t.Fatal(err)
	}
	if got := sink.pages[0].Resources()[0].Observation.Fields()["region"]; got != "us-east-1" {
		t.Fatalf("region=%v", got)
	}

	bad, _ := awsbrowser.NewQueryKey(testIAMContext(t), awsbrowser.ProviderS3, awsbrowser.OperationGetBucketLocation, map[string]string{"bucket": "192.0.2.1"})
	if err := executor.Execute(context.Background(), bad, &recordingSink{}); !errors.Is(err, awsbrowser.ErrInvalidQueryKey) {
		t.Fatalf("error=%v", err)
	}
}

func TestS3RejectsUnknownLocationConstraint(t *testing.T) {
	fake := &fakeS3{get: func(context.Context, *s3.GetBucketLocationInput) (*s3.GetBucketLocationOutput, error) {
		return &s3.GetBucketLocationOutput{LocationConstraint: s3types.BucketLocationConstraint("not-a-region")}, nil
	}}
	executor, _ := NewS3(fake, fixedIAMClock)
	key, _ := awsbrowser.NewQueryKey(testIAMContext(t), awsbrowser.ProviderS3, awsbrowser.OperationGetBucketLocation, map[string]string{"bucket": "valid-bucket"})
	if err := executor.Execute(context.Background(), key, &recordingSink{}); !errors.Is(err, awsbrowser.ErrQueryDecode) {
		t.Fatalf("error=%v", err)
	}
}
