package awsbrowser

import (
	"context"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type configLoader func(context.Context, ...func(*config.LoadOptions) error) (aws.Config, error)

// sdkRuntime is intentionally private: callers receive only RuntimeContext,
// never the SDK config, credential cache, provider, or concrete clients.
type sdkRuntime struct {
	identity         VerifiedIdentity
	credentials      *aws.CredentialsCache
	credentialSource *CredentialProvider
	sts              STSAPI
	ec2              EC2API
	iam              IAMAPI
	route53          Route53API
}

var _ RuntimeContext = (*sdkRuntime)(nil)

func newSDKRuntime(ctx context.Context, region string, provider *CredentialProvider, load configLoader) (*sdkRuntime, error) {
	if provider == nil {
		return nil, errors.New("AWS credential provider is required")
	}
	if load == nil {
		return nil, errors.New("AWS SDK config loader is required")
	}

	cfg, err := load(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(provider),
		config.WithCredentialsCacheOptions(func(options *aws.CredentialsCacheOptions) {
			options.ExpiryWindow = 5 * time.Minute
			options.ExpiryWindowJitterFrac = 0.2
		}),
	)
	if err != nil {
		return nil, err
	}

	cache, ok := cfg.Credentials.(*aws.CredentialsCache)
	if !ok {
		return nil, errors.New("AWS SDK did not install the credential cache")
	}
	cfg.BaseEndpoint = nil
	cfg.EndpointResolver = nil
	cfg.EndpointResolverWithOptions = nil
	cfg.ConfigSources = append([]any{ignoreConfiguredEndpoints{}}, cfg.ConfigSources...)

	return &sdkRuntime{
		credentials:      cache,
		credentialSource: provider,
		sts: sts.NewFromConfig(cfg, func(options *sts.Options) {
			options.BaseEndpoint = nil
			options.EndpointResolverV2 = sts.NewDefaultEndpointResolverV2()
			options.RetryMaxAttempts = 0
			options.Retryer = newStandardRetryer()
		}),
		ec2: ec2.NewFromConfig(cfg, func(options *ec2.Options) {
			options.BaseEndpoint = nil
			options.EndpointResolverV2 = ec2.NewDefaultEndpointResolverV2()
			options.RetryMaxAttempts = 0
			options.Retryer = newStandardRetryer()
		}),
		iam: iam.NewFromConfig(cfg, func(options *iam.Options) {
			options.BaseEndpoint = nil
			options.EndpointResolverV2 = iam.NewDefaultEndpointResolverV2()
			options.RetryMaxAttempts = 0
			options.Retryer = newStandardRetryer()
		}),
		route53: route53.NewFromConfig(cfg, func(options *route53.Options) {
			options.BaseEndpoint = nil
			options.EndpointResolverV2 = route53.NewDefaultEndpointResolverV2()
			options.RetryMaxAttempts = 0
			options.Retryer = newStandardRetryer()
		}),
	}, nil
}

func (r *sdkRuntime) Identity() VerifiedIdentity { return r.identity }
func (r *sdkRuntime) STS() STSAPI                { return r.sts }
func (r *sdkRuntime) EC2() EC2API                { return r.ec2 }
func (r *sdkRuntime) IAM() IAMAPI                { return r.iam }
func (r *sdkRuntime) Route53() Route53API        { return r.route53 }

func newStandardRetryer() aws.Retryer {
	return retry.NewStandard(func(options *retry.StandardOptions) {
		options.MaxAttempts = 3
	})
}

type ignoreConfiguredEndpoints struct{}

func (ignoreConfiguredEndpoints) GetIgnoreConfiguredEndpoints(context.Context) (bool, bool, error) {
	return true, true, nil
}
