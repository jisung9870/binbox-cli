package providers

import (
	"context"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
)

var (
	ErrInvalidS3Executor = errors.New("S3 query executor requires an API and clock")
	s3BucketNameRE       = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	s3RegionRE           = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)+-[0-9]+$`)
)

type S3QueryExecutor struct {
	api   awsbrowser.S3API
	clock func() time.Time
}

func NewS3(api awsbrowser.S3API, clock func() time.Time) (awsbrowser.QueryExecutor, error) {
	if api == nil || clock == nil {
		return nil, ErrInvalidS3Executor
	}
	return &S3QueryExecutor{api: api, clock: clock}, nil
}

func (executor *S3QueryExecutor) Execute(ctx context.Context, key awsbrowser.QueryKey, sink awsbrowser.QueryPageSink) error {
	if executor == nil || executor.api == nil || executor.clock == nil || ctx == nil || sink == nil {
		return ErrInvalidS3Executor
	}
	if key.Validate() != nil || key.Provider != awsbrowser.ProviderS3 || key.Operation != awsbrowser.OperationGetBucketLocation {
		return awsbrowser.ErrInvalidProviderOperation
	}
	bucket, err := s3BucketParam(key)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	output, err := executor.api.GetBucketLocation(ctx, &s3.GetBucketLocationInput{Bucket: &bucket})
	if err != nil {
		return awsbrowser.ClassifyProviderError(err, awsbrowser.ProviderS3, key.Operation)
	}
	if output == nil {
		return awsbrowser.ErrQueryDecode
	}
	region := string(output.LocationConstraint)
	switch region {
	case "":
		region = "us-east-1"
	case "EU":
		region = "eu-west-1"
	}
	if !s3RegionRE.MatchString(region) {
		return awsbrowser.ErrQueryDecode
	}
	fetchedAt := executor.clock().UTC()
	resourceKey, err := awsbrowser.NewGlobalResourceKey(key.Context, "s3.bucket", bucket)
	if err != nil {
		return awsbrowser.ErrQueryDecode
	}
	observation, err := awsbrowser.NewResourceObservationForOperation(key.Context, key.Operation, map[string]any{
		"bucket_name": bucket,
		"region":      region,
	}, fetchedAt, true)
	if err != nil {
		return err
	}
	page, err := awsbrowser.NewQueryPage(0, []awsbrowser.ObservedResource{{Key: resourceKey, Observation: observation}}, fetchedAt, true)
	if err != nil {
		return err
	}
	if err := sink.Page(page); err != nil {
		return err
	}
	return sink.Complete(fetchedAt)
}

func s3BucketParam(key awsbrowser.QueryKey) (string, error) {
	values, err := url.ParseQuery(key.ParamsKey)
	if err != nil || len(values) != 1 || len(values["bucket"]) != 1 {
		return "", awsbrowser.ErrInvalidQueryKey
	}
	bucket := strings.TrimSpace(values.Get("bucket"))
	if !validS3BucketName(bucket) {
		return "", awsbrowser.ErrInvalidQueryKey
	}
	return bucket, nil
}

func validS3BucketName(bucket string) bool {
	return bucket == strings.ToLower(bucket) && s3BucketNameRE.MatchString(bucket) &&
		!strings.Contains(bucket, "..") && net.ParseIP(bucket) == nil
}

var _ awsbrowser.QueryExecutor = (*S3QueryExecutor)(nil)
