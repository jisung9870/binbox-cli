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

type ConfigLoader func(context.Context, ...func(*config.LoadOptions) error) (aws.Config, error)

type SDKClients struct {
	Config           aws.Config
	Credentials      *aws.CredentialsCache
	CredentialSource *CredentialProvider
	STS              STSAPI
	EC2              EC2API
	IAM              IAMAPI
	Route53          Route53API
}

func NewSDKClients(ctx context.Context, region string, provider *CredentialProvider) (*SDKClients, error) {
	return newSDKClients(ctx, region, provider, config.LoadDefaultConfig)
}

func newSDKClients(ctx context.Context, region string, provider *CredentialProvider, load ConfigLoader) (*SDKClients, error) {
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
		config.WithRetryer(func() aws.Retryer {
			return retry.NewStandard(func(options *retry.StandardOptions) {
				options.MaxAttempts = 3
			})
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

	return &SDKClients{
		Config:           cfg,
		Credentials:      cache,
		CredentialSource: provider,
		STS:              sts.NewFromConfig(cfg),
		EC2:              ec2.NewFromConfig(cfg),
		IAM:              iam.NewFromConfig(cfg),
		Route53:          route53.NewFromConfig(cfg),
	}, nil
}

type ignoreConfiguredEndpoints struct{}

func (ignoreConfiguredEndpoints) GetIgnoreConfiguredEndpoints(context.Context) (bool, bool, error) {
	return true, true, nil
}
