package awsbrowser

import (
	"context"
	"errors"
	"os"
	"regexp"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

var regionNameRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)+-[0-9]+$`)

type sdkRuntimeBuilder func(context.Context, string, *CredentialProvider) (*sdkRuntime, error)

type profileSourceValidator func(context.Context, string, []string) error

type runtimeFactory struct {
	exporter              CredentialExporter
	env                   []string
	build                 sdkRuntimeBuilder
	validateProfileSource profileSourceValidator
}

var _ RuntimeFactory = (*runtimeFactory)(nil)

// NewRuntimeFactory creates the production identity-verifying AWS runtime
// factory. The returned interface does not expose the AWS CLI bridge, SDK
// configuration, credential provider, or credential cache.
func NewRuntimeFactory(awsCLIPath string, env []string) (RuntimeFactory, error) {
	if awsCLIPath == "" {
		return nil, errors.New("AWS CLI path is required")
	}
	return newRuntimeFactory(NewExecCLI(awsCLIPath), env, func(ctx context.Context, region string, provider *CredentialProvider) (*sdkRuntime, error) {
		return newSDKRuntime(ctx, region, provider, config.LoadDefaultConfig)
	}, validateNamedProfileSource)
}

func newRuntimeFactory(exporter CredentialExporter, env []string, build sdkRuntimeBuilder, validateSource profileSourceValidator) (*runtimeFactory, error) {
	if exporter == nil {
		return nil, errors.New("AWS credential exporter is required")
	}
	if build == nil {
		return nil, errors.New("AWS SDK runtime builder is required")
	}
	if validateSource == nil {
		return nil, errors.New("AWS profile source validator is required")
	}
	if env == nil {
		env = os.Environ()
	}
	return &runtimeFactory{
		exporter:              exporter,
		env:                   append([]string(nil), env...),
		build:                 build,
		validateProfileSource: validateSource,
	}, nil
}

func (f *runtimeFactory) Resolve(ctx context.Context, spec ContextSpec) (RuntimeContext, error) {
	profile, err := validateContextSpec(spec)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if spec.Mode == ContextModeNamedProfile {
		if err := f.validateProfileSource(ctx, profile, f.env); err != nil {
			return nil, err
		}
	}

	provider, err := NewCredentialProvider(f.exporter, profile, f.env)
	if err != nil {
		return nil, err
	}
	runtime, err := f.build(ctx, spec.Region, provider)
	if err != nil {
		return nil, err
	}
	if runtime == nil || runtime.credentials == nil || runtime.credentialSource != provider || runtime.sts == nil ||
		runtime.ec2 == nil || runtime.iam == nil || runtime.route53 == nil || runtime.cloudfront == nil || runtime.s3 == nil {
		return nil, errors.New("AWS SDK runtime is incomplete")
	}

	identity, err := resolveRuntimeIdentity(ctx, runtime)
	if err != nil {
		return nil, err
	}
	return newVerifiedRuntime(runtime, identity), nil
}

func validateContextSpec(spec ContextSpec) (string, error) {
	if spec.Region == "" || len(spec.Region) > 64 || !regionNameRE.MatchString(spec.Region) {
		return "", errors.New("invalid AWS region")
	}

	switch spec.Mode {
	case ContextModeAmbient:
		if spec.Profile != "" {
			return "", errors.New("ambient AWS context cannot name a profile")
		}
		return "", nil
	case ContextModeNamedProfile:
		if !profileNameRE.MatchString(spec.Profile) {
			return "", errors.New("invalid AWS profile name")
		}
		return spec.Profile, nil
	default:
		return "", errors.New("invalid AWS context mode")
	}
}

func resolveRuntimeIdentity(ctx context.Context, runtime *sdkRuntime) (VerifiedIdentity, error) {
	output, err := runtime.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil && isExpiredToken(err) {
		runtime.credentials.Invalidate()
		output, err = runtime.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	}
	if err != nil {
		return VerifiedIdentity{}, err
	}
	return verifiedIdentity(output, runtime.credentialSource.Generation())
}

type verifiedRuntime struct {
	guard      *readGuard
	sts        STSAPI
	ec2        EC2API
	iam        IAMAPI
	route53    Route53API
	cloudfront CloudFrontAPI
	s3         S3API
}

var _ RuntimeContext = (*verifiedRuntime)(nil)

func newVerifiedRuntime(runtime *sdkRuntime, identity VerifiedIdentity) *verifiedRuntime {
	guard := newReadGuard(runtime, identity)
	return &verifiedRuntime{
		guard:      guard,
		sts:        guardedSTS{guard: guard, client: runtime.sts},
		ec2:        guardedEC2{guard: guard, client: runtime.ec2},
		iam:        guardedIAM{guard: guard, client: runtime.iam},
		route53:    guardedRoute53{guard: guard, client: runtime.route53},
		cloudfront: guardedCloudFront{guard: guard, client: runtime.cloudfront},
		s3:         guardedS3{guard: guard, client: runtime.s3},
	}
}

func (r *verifiedRuntime) Identity() VerifiedIdentity { return r.guard.Identity() }
func (r *verifiedRuntime) STS() STSAPI                { return r.sts }
func (r *verifiedRuntime) EC2() EC2API                { return r.ec2 }
func (r *verifiedRuntime) IAM() IAMAPI                { return r.iam }
func (r *verifiedRuntime) Route53() Route53API        { return r.route53 }
func (r *verifiedRuntime) CloudFront() CloudFrontAPI  { return r.cloudfront }
func (r *verifiedRuntime) S3() S3API                  { return r.s3 }
