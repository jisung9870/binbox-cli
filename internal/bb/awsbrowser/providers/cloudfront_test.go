package providers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cloudfronttypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
)

type fakeCloudFront struct {
	list func(context.Context, *cloudfront.ListDistributionsInput) (*cloudfront.ListDistributionsOutput, error)
}

func (fake *fakeCloudFront) ListDistributions(ctx context.Context, input *cloudfront.ListDistributionsInput) (*cloudfront.ListDistributionsOutput, error) {
	return fake.list(ctx, input)
}

func cloudFrontKey(t *testing.T, domain string) awsbrowser.QueryKey {
	t.Helper()
	key, err := awsbrowser.NewQueryKey(testIAMContext(t), awsbrowser.ProviderCloudFront, awsbrowser.OperationListDistributions, map[string]string{"distribution-domain": domain})
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestCloudFrontMapsPathAwareS3Origins(t *testing.T) {
	now := fixedIAMClock()
	fake := &fakeCloudFront{list: func(_ context.Context, input *cloudfront.ListDistributionsInput) (*cloudfront.ListDistributionsOutput, error) {
		if input.Marker != nil || aws.ToInt32(input.MaxItems) != cloudFrontPageSize {
			t.Fatalf("input=%#v", input)
		}
		return &cloudfront.ListDistributionsOutput{DistributionList: &cloudfronttypes.DistributionList{
			IsTruncated: aws.Bool(false), Marker: aws.String(""), MaxItems: aws.Int32(100), Quantity: aws.Int32(1),
			Items: []cloudfronttypes.DistributionSummary{cloudFrontDistribution(now)},
		}}, nil
	}}
	executor, err := NewCloudFront(fake, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingSink{}
	if err := executor.Execute(context.Background(), cloudFrontKey(t, "D24ODQ2OCBSMJD.CLOUDFRONT.NET."), sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.pages) != 1 || len(sink.completed) != 1 || len(sink.pages[0].Resources()) != 1 {
		t.Fatalf("pages=%d complete=%d", len(sink.pages), len(sink.completed))
	}
	resource := sink.pages[0].Resources()[0]
	if resource.Key.Type != "cloudfront.distribution" || resource.Key.ID != "E3M80I51D1TQ9P" {
		t.Fatalf("key=%+v", resource.Key)
	}
	projection := awsbrowser.ProjectResourceFields(resource.Key, resource.Observation.Fields())
	if projection.Title != "d24odq2ocbsmjd.cloudfront.net" || len(projection.Relations) != 3 {
		t.Fatalf("projection=%+v", projection)
	}
	want := []struct{ label, target string }{
		{"Default /* → udg-kr-game-binary.s3.ap-northeast-2.amazonaws.com", "s3.bucket:udg-kr-game-binary"},
		{"report/* → udg-us-game-dump.s3.us-east-1.amazonaws.com", "s3.bucket:udg-us-game-dump"},
		{"character/* → udg-us-game-dump.s3.us-east-1.amazonaws.com", "s3.bucket:udg-us-game-dump"},
	}
	for index, relation := range projection.Relations {
		if relation.Label != want[index].label || relation.Target != want[index].target || relation.Kind != string(awsbrowser.RelationInferred) {
			t.Fatalf("relation[%d]=%+v", index, relation)
		}
	}
}

func TestCloudFrontReturnsEmptyForDifferentDomainAndRejectsBrokenCursor(t *testing.T) {
	now := fixedIAMClock()
	fake := &fakeCloudFront{list: func(context.Context, *cloudfront.ListDistributionsInput) (*cloudfront.ListDistributionsOutput, error) {
		return &cloudfront.ListDistributionsOutput{DistributionList: &cloudfronttypes.DistributionList{
			IsTruncated: aws.Bool(false), Marker: aws.String(""), MaxItems: aws.Int32(100), Quantity: aws.Int32(1),
			Items: []cloudfronttypes.DistributionSummary{cloudFrontDistribution(now)},
		}}, nil
	}}
	executor, _ := NewCloudFront(fake, func() time.Time { return now })
	sink := &recordingSink{}
	if err := executor.Execute(context.Background(), cloudFrontKey(t, "different.cloudfront.net"), sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.pages) != 1 || len(sink.pages[0].Resources()) != 0 {
		t.Fatalf("unexpected resources: %+v", sink.pages)
	}

	fake.list = func(context.Context, *cloudfront.ListDistributionsInput) (*cloudfront.ListDistributionsOutput, error) {
		return &cloudfront.ListDistributionsOutput{DistributionList: &cloudfronttypes.DistributionList{
			IsTruncated: aws.Bool(true), Marker: aws.String(""), MaxItems: aws.Int32(100), Quantity: aws.Int32(0),
		}}, nil
	}
	if err := executor.Execute(context.Background(), cloudFrontKey(t, "different.cloudfront.net"), &recordingSink{}); !errors.Is(err, awsbrowser.ErrQueryDecode) {
		t.Fatalf("error=%v", err)
	}
}

func TestCloudFrontCustomOriginRemainsEvidenceOnly(t *testing.T) {
	now := fixedIAMClock()
	distribution := cloudFrontDistribution(now)
	distribution.Origins = &cloudfronttypes.Origins{Quantity: aws.Int32(1), Items: []cloudfronttypes.Origin{{
		Id: aws.String("custom"), DomainName: aws.String("origin.example.com"),
		CustomOriginConfig: &cloudfronttypes.CustomOriginConfig{},
	}}}
	distribution.DefaultCacheBehavior.TargetOriginId = aws.String("custom")
	distribution.CacheBehaviors = &cloudfronttypes.CacheBehaviors{Quantity: aws.Int32(0)}
	fake := &fakeCloudFront{list: func(context.Context, *cloudfront.ListDistributionsInput) (*cloudfront.ListDistributionsOutput, error) {
		return &cloudfront.ListDistributionsOutput{DistributionList: &cloudfronttypes.DistributionList{
			IsTruncated: aws.Bool(false), Marker: aws.String(""), MaxItems: aws.Int32(100), Quantity: aws.Int32(1),
			Items: []cloudfronttypes.DistributionSummary{distribution},
		}}, nil
	}}
	executor, _ := NewCloudFront(fake, func() time.Time { return now })
	sink := &recordingSink{}
	if err := executor.Execute(context.Background(), cloudFrontKey(t, "d24odq2ocbsmjd.cloudfront.net"), sink); err != nil {
		t.Fatal(err)
	}
	resource := sink.pages[0].Resources()[0]
	projection := awsbrowser.ProjectResourceFields(resource.Key, resource.Observation.Fields())
	if len(projection.Relations) != 1 || projection.Relations[0].Target != "" || projection.Relations[0].Label != "Default /* → origin.example.com" || projection.Relations[0].Kind != string(awsbrowser.RelationUnsupported) {
		t.Fatalf("custom origin relation=%+v", projection.Relations)
	}
}

func cloudFrontDistribution(now time.Time) cloudfronttypes.DistributionSummary {
	return cloudfronttypes.DistributionSummary{
		Id: aws.String("E3M80I51D1TQ9P"), ARN: aws.String("arn:aws:cloudfront::123456789012:distribution/E3M80I51D1TQ9P"),
		DomainName: aws.String("d24odq2ocbsmjd.cloudfront.net"), Status: aws.String("Deployed"), Enabled: aws.Bool(true),
		LastModifiedTime: aws.Time(now), Comment: aws.String("UDG binary"), IsIPV6Enabled: aws.Bool(true), Staging: aws.Bool(false),
		Aliases: &cloudfronttypes.Aliases{Quantity: aws.Int32(1), Items: []string{"binary.udg.line.games"}},
		Origins: &cloudfronttypes.Origins{Quantity: aws.Int32(2), Items: []cloudfronttypes.Origin{
			{Id: aws.String("kr"), DomainName: aws.String("udg-kr-game-binary.s3.ap-northeast-2.amazonaws.com"), OriginPath: aws.String(""), S3OriginConfig: &cloudfronttypes.S3OriginConfig{OriginAccessIdentity: aws.String("")}},
			{Id: aws.String("us"), DomainName: aws.String("udg-us-game-dump.s3.us-east-1.amazonaws.com"), OriginPath: aws.String(""), S3OriginConfig: &cloudfronttypes.S3OriginConfig{OriginAccessIdentity: aws.String("")}},
		}},
		DefaultCacheBehavior: &cloudfronttypes.DefaultCacheBehavior{TargetOriginId: aws.String("kr")},
		CacheBehaviors: &cloudfronttypes.CacheBehaviors{Quantity: aws.Int32(2), Items: []cloudfronttypes.CacheBehavior{
			{PathPattern: aws.String("report/*"), TargetOriginId: aws.String("us")},
			{PathPattern: aws.String("character/*"), TargetOriginId: aws.String("us")},
		}},
	}
}
