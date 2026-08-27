package awsbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

var profileNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

var namedProfileIdentityEnv = map[string]struct{}{
	"AWS_ACCESS_KEY_ID":           {},
	"AWS_ACCESS_KEY":              {},
	"AWS_SECRET_ACCESS_KEY":       {},
	"AWS_SECRET_KEY":              {},
	"AWS_SESSION_TOKEN":           {},
	"AWS_SECURITY_TOKEN":          {},
	"AWS_PROFILE":                 {},
	"AWS_DEFAULT_PROFILE":         {},
	"AWS_ROLE_ARN":                {},
	"AWS_ROLE_SESSION_NAME":       {},
	"AWS_WEB_IDENTITY_TOKEN_FILE": {},
	"AWS_SESSION_EXPIRATION":      {},
	"AWS_CREDENTIAL_EXPIRATION":   {},
	"BINBOX_ASSUME_PROFILE":       {},
}

// CredentialProvider adapts AWS CLI v2 export-credentials output to the SDK.
// The provider never exposes raw CLI output in its errors.
type CredentialProvider struct {
	cli        CredentialExporter
	profile    string
	baseEnv    []string
	generation atomic.Uint64
}

func NewCredentialProvider(cli CredentialExporter, profile string, env []string) (*CredentialProvider, error) {
	if cli == nil {
		return nil, errors.New("AWS CLI runner is required")
	}
	if profile != "" && !profileNameRE.MatchString(profile) {
		return nil, errors.New("invalid AWS profile name")
	}
	if env == nil {
		env = os.Environ()
	}
	return &CredentialProvider{
		cli:     cli,
		profile: profile,
		baseEnv: append([]string(nil), env...),
	}, nil
}

func (p *CredentialProvider) Generation() uint64 {
	return p.generation.Load()
}

func (p *CredentialProvider) Retrieve(ctx context.Context) (aws.Credentials, error) {
	stdout, err := p.cli.ExportCredentials(ctx, p.profile, credentialEnvironment(p.baseEnv, p.profile != ""))
	if err != nil {
		return aws.Credentials{}, classifyCredentialError(ctx, err)
	}

	var document struct {
		Version         int    `json:"Version"`
		AccessKeyID     string `json:"AccessKeyId"`
		SecretAccessKey string `json:"SecretAccessKey"`
		SessionToken    string `json:"SessionToken"`
		Expiration      string `json:"Expiration"`
	}
	if err := json.Unmarshal(stdout, &document); err != nil {
		return aws.Credentials{}, &CredentialError{Kind: CredentialInvalid}
	}
	if document.Version != 1 || document.AccessKeyID == "" || document.SecretAccessKey == "" {
		return aws.Credentials{}, &CredentialError{Kind: CredentialInvalid}
	}

	credentials := aws.Credentials{
		AccessKeyID:     document.AccessKeyID,
		SecretAccessKey: document.SecretAccessKey,
		SessionToken:    document.SessionToken,
		Source:          "AWS CLI export-credentials",
	}
	if document.Expiration != "" {
		expires, err := time.Parse(time.RFC3339, document.Expiration)
		if err != nil {
			return aws.Credentials{}, &CredentialError{Kind: CredentialInvalid}
		}
		credentials.CanExpire = true
		credentials.Expires = expires
	}
	p.generation.Add(1)
	return credentials, nil
}

func credentialEnvironment(base []string, named bool) []string {
	values := make(map[string]string, len(base)+1)
	for _, entry := range base {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			continue
		}
		if name == "AWS_ENDPOINT_URL" || strings.HasPrefix(name, "AWS_ENDPOINT_URL_") {
			continue
		}
		if named {
			if _, remove := namedProfileIdentityEnv[name]; remove {
				continue
			}
		}
		if name == "AWS_IGNORE_CONFIGURED_ENDPOINT_URLS" {
			continue
		}
		values[name] = value
	}
	values["AWS_IGNORE_CONFIGURED_ENDPOINT_URLS"] = "true"

	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	env := make([]string, 0, len(names))
	for _, name := range names {
		env = append(env, name+"="+values[name])
	}
	return env
}
