package providers

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cloudfronttypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
)

const cloudFrontPageSize int32 = 100

var (
	ErrInvalidCloudFrontExecutor = errors.New("CloudFront query executor requires an API and clock")
	cloudFrontDomainRE           = regexp.MustCompile(`^[a-z0-9-]+\.cloudfront\.net$`)
)

type CloudFrontQueryExecutor struct {
	api   awsbrowser.CloudFrontAPI
	clock func() time.Time
}

func NewCloudFront(api awsbrowser.CloudFrontAPI, clock func() time.Time) (awsbrowser.QueryExecutor, error) {
	if api == nil || clock == nil {
		return nil, ErrInvalidCloudFrontExecutor
	}
	return &CloudFrontQueryExecutor{api: api, clock: clock}, nil
}

func (executor *CloudFrontQueryExecutor) Execute(ctx context.Context, key awsbrowser.QueryKey, sink awsbrowser.QueryPageSink) error {
	if executor == nil || executor.api == nil || executor.clock == nil || ctx == nil || sink == nil {
		return ErrInvalidCloudFrontExecutor
	}
	if key.Validate() != nil || key.Provider != awsbrowser.ProviderCloudFront || key.Operation != awsbrowser.OperationListDistributions {
		return awsbrowser.ErrInvalidProviderOperation
	}
	params, err := cloudFrontParams(key)
	if err != nil {
		return err
	}
	domain := params["distribution-domain"]
	if !cloudFrontDomainRE.MatchString(domain) {
		return awsbrowser.ErrInvalidQueryKey
	}

	input := &cloudfront.ListDistributionsInput{MaxItems: aws.Int32(cloudFrontPageSize)}
	seen := make(map[string]struct{})
	var pageNumber uint64
	var fetchedAt time.Time
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		output, err := executor.api.ListDistributions(ctx, input)
		if err != nil {
			return awsbrowser.ClassifyProviderError(err, awsbrowser.ProviderCloudFront, key.Operation)
		}
		if output == nil || output.DistributionList == nil || output.DistributionList.IsTruncated == nil {
			return awsbrowser.ErrQueryDecode
		}
		fetchedAt = executor.clock().UTC()
		resources := make([]awsbrowser.ObservedResource, 0, 1)
		for _, distribution := range output.DistributionList.Items {
			if strings.TrimSuffix(strings.ToLower(aws.ToString(distribution.DomainName)), ".") != domain {
				continue
			}
			resource, err := mapCloudFrontDistribution(key, distribution, fetchedAt)
			if err != nil {
				return err
			}
			resources = append(resources, resource)
		}
		page, err := awsbrowser.NewQueryPage(pageNumber, resources, fetchedAt, true)
		if err != nil {
			return err
		}
		if err := sink.Page(page); err != nil {
			return err
		}
		pageNumber++
		if !*output.DistributionList.IsTruncated {
			return sink.Complete(fetchedAt)
		}
		marker := strings.TrimSpace(aws.ToString(output.DistributionList.NextMarker))
		if marker == "" {
			return awsbrowser.ErrQueryDecode
		}
		if _, duplicate := seen[marker]; duplicate {
			return awsbrowser.ErrQueryDecode
		}
		seen[marker] = struct{}{}
		input.Marker = aws.String(marker)
	}
}

func cloudFrontParams(key awsbrowser.QueryKey) (map[string]string, error) {
	values, err := url.ParseQuery(key.ParamsKey)
	if err != nil || len(values) != 1 || len(values["distribution-domain"]) != 1 {
		return nil, awsbrowser.ErrInvalidQueryKey
	}
	domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(values.Get("distribution-domain"))), ".")
	if domain == "" {
		return nil, awsbrowser.ErrInvalidQueryKey
	}
	return map[string]string{"distribution-domain": domain}, nil
}

func mapCloudFrontDistribution(key awsbrowser.QueryKey, distribution cloudfronttypes.DistributionSummary, fetchedAt time.Time) (awsbrowser.ObservedResource, error) {
	id := strings.TrimSpace(aws.ToString(distribution.Id))
	domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(aws.ToString(distribution.DomainName))), ".")
	if id == "" || domain == "" || distribution.Origins == nil || distribution.DefaultCacheBehavior == nil ||
		distribution.CacheBehaviors == nil || distribution.Enabled == nil || distribution.LastModifiedTime == nil {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	resourceKey, err := awsbrowser.NewGlobalResourceKey(key.Context, "cloudfront.distribution", id)
	if err != nil {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	origins := make(map[string]cloudfronttypes.Origin, len(distribution.Origins.Items))
	originDetails := make([]any, 0, len(distribution.Origins.Items))
	for _, origin := range distribution.Origins.Items {
		originID := strings.TrimSpace(aws.ToString(origin.Id))
		originDomain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(aws.ToString(origin.DomainName))), ".")
		if originID == "" || originDomain == "" {
			return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
		}
		origins[originID] = origin
		originDetails = append(originDetails, map[string]any{
			"id": originID, "domain_name": originDomain, "origin_path": aws.ToString(origin.OriginPath),
		})
	}
	relations := make([]any, 0, 1+len(distribution.CacheBehaviors.Items))
	routing := make([]any, 0, 1+len(distribution.CacheBehaviors.Items))
	if err := appendCloudFrontRoute(key.Context, resourceKey, origins, "Default /*", "*", aws.ToString(distribution.DefaultCacheBehavior.TargetOriginId), key.Operation, fetchedAt, &relations, &routing); err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	if distribution.CacheBehaviors != nil {
		for _, behavior := range distribution.CacheBehaviors.Items {
			path := strings.TrimSpace(aws.ToString(behavior.PathPattern))
			if path == "" {
				return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
			}
			if err := appendCloudFrontRoute(key.Context, resourceKey, origins, path, path, aws.ToString(behavior.TargetOriginId), key.Operation, fetchedAt, &relations, &routing); err != nil {
				return awsbrowser.ObservedResource{}, err
			}
		}
	}
	aliases := []string{}
	if distribution.Aliases != nil {
		aliases = append(aliases, distribution.Aliases.Items...)
	}
	fields := map[string]any{
		"distribution_id": id, "domain_name": domain, "arn": aws.ToString(distribution.ARN),
		"status": aws.ToString(distribution.Status), "enabled": aws.ToBool(distribution.Enabled),
		"last_modified_time": distribution.LastModifiedTime.UTC(), "aliases": aliases,
		"comment": aws.ToString(distribution.Comment), "http_version": string(distribution.HttpVersion),
		"price_class": string(distribution.PriceClass), "ipv6_enabled": aws.ToBool(distribution.IsIPV6Enabled),
		"origins": originDetails, "routing_rules": routing, "relations": relations,
	}
	observation, err := awsbrowser.NewResourceObservationForOperation(key.Context, key.Operation, fields, fetchedAt, true)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	return awsbrowser.ObservedResource{Key: resourceKey, Observation: observation}, nil
}

func appendCloudFrontRoute(context awsbrowser.AWSContext, source awsbrowser.ResourceKey, origins map[string]cloudfronttypes.Origin, label, path, targetOriginID, operation string, observedAt time.Time, relations, routing *[]any) error {
	targetOriginID = strings.TrimSpace(targetOriginID)
	origin, ok := origins[targetOriginID]
	if !ok {
		return awsbrowser.ErrQueryDecode
	}
	domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(aws.ToString(origin.DomainName))), ".")
	if domain == "" {
		return awsbrowser.ErrQueryDecode
	}
	*routing = append(*routing, map[string]any{
		"path_pattern": path, "target_origin_id": targetOriginID, "origin_domain": domain,
	})
	relation := map[string]any{
		"label": label + " → " + domain, "relation_type": string(awsbrowser.RelationRoutesTo),
		"direction": string(awsbrowser.RelationOutgoing), "condition": path, "kind": string(awsbrowser.RelationUnsupported),
		"reason": "cloudfront-origin-domain", "operation": operation, "scope": awsbrowser.GlobalRegion,
		"observed_at": observedAt, "source": source,
	}
	if bucket, ok := s3BucketFromOrigin(origin); ok {
		target, err := awsbrowser.NewGlobalResourceKey(context, "s3.bucket", bucket)
		if err != nil {
			return err
		}
		evidence, err := awsbrowser.NewRelationEvidence(awsbrowser.RelationInferred, "cloudfront-s3-origin-domain", operation, awsbrowser.GlobalRegion, observedAt)
		if err != nil {
			return err
		}
		semantics, err := awsbrowser.NewRelationSemantics(awsbrowser.RelationRoutesTo, awsbrowser.RelationOutgoing, path)
		if err != nil {
			return err
		}
		edge, err := awsbrowser.NewRelation(source, target, semantics, evidence)
		if err != nil {
			return err
		}
		validated := edge.Evidence()[0]
		relation["target"] = edge.Target
		relation["kind"] = string(validated.Kind)
		relation["reason"] = validated.Reason
		relation["relation_type"] = string(edge.Semantics.Type)
		relation["direction"] = string(edge.Semantics.Direction)
		relation["condition"] = edge.Semantics.Condition
	}
	*relations = append(*relations, relation)
	return nil
}

func s3BucketFromOrigin(origin cloudfronttypes.Origin) (string, bool) {
	if origin.S3OriginConfig == nil {
		return "", false
	}
	domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(aws.ToString(origin.DomainName))), ".")
	marker := ".s3."
	index := strings.Index(domain, marker)
	if index < 1 {
		marker = ".s3-"
		index = strings.Index(domain, marker)
	}
	if index < 1 {
		return "", false
	}
	bucket := domain[:index]
	if !validS3BucketName(bucket) {
		return "", false
	}
	return bucket, true
}

var _ awsbrowser.QueryExecutor = (*CloudFrontQueryExecutor)(nil)
