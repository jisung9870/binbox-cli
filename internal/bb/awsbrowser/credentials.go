package awsbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

var profileNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

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

type CredentialErrorKind string

const (
	CredentialUnknown        CredentialErrorKind = "unknown"
	CredentialAuthRequired   CredentialErrorKind = "auth_required"
	CredentialUnsupported    CredentialErrorKind = "unsupported"
	CredentialCancelled      CredentialErrorKind = "cancelled"
	CredentialOutputTooLarge CredentialErrorKind = "output_too_large"
	CredentialInvalid        CredentialErrorKind = "invalid"
)

type CredentialError struct {
	Kind CredentialErrorKind
	Code string
	err  error
}

func (e *CredentialError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("AWS credential export failed (%s)", e.Code)
	}
	return "AWS credential export failed"
}

func (e *CredentialError) Unwrap() error { return e.err }

// CredentialProvider adapts AWS CLI v2 export-credentials output to the SDK.
// The provider never exposes raw CLI output in its errors.
type CredentialProvider struct {
	cli        CLI
	profile    string
	baseEnv    []string
	generation atomic.Uint64
}

func NewCredentialProvider(cli CLI, profile string, env []string) (*CredentialProvider, error) {
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
	args := []string{}
	if p.profile != "" {
		args = append(args, "--profile", p.profile)
	}
	args = append(args,
		"configure", "export-credentials",
		"--format", "process",
		"--no-cli-pager",
		"--no-cli-auto-prompt",
		"--cli-error-format", "json",
	)

	stdout, stderr, err := p.cli.Run(ctx, args, credentialEnvironment(p.baseEnv, p.profile != ""))
	if err != nil {
		return aws.Credentials{}, classifyCredentialError(ctx, stderr, err)
	}

	var document struct {
		Version         int    `json:"Version"`
		AccessKeyID     string `json:"AccessKeyId"`
		SecretAccessKey string `json:"SecretAccessKey"`
		SessionToken    string `json:"SessionToken"`
		Expiration      string `json:"Expiration"`
	}
	if err := json.Unmarshal(stdout, &document); err != nil {
		return aws.Credentials{}, &CredentialError{Kind: CredentialInvalid, err: err}
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
			return aws.Credentials{}, &CredentialError{Kind: CredentialInvalid, err: err}
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

func classifyCredentialError(ctx context.Context, stderr []byte, err error) error {
	if ctx.Err() != nil {
		return &CredentialError{Kind: CredentialCancelled, err: ctx.Err()}
	}
	var limitError *OutputLimitError
	if errors.As(err, &limitError) {
		return &CredentialError{Kind: CredentialOutputTooLarge, err: limitError}
	}

	code := structuredErrorCode(stderr)
	kind := CredentialUnknown
	switch code {
	case "UnauthorizedException", "InvalidGrantException", "ExpiredToken", "SSOTokenLoadError":
		kind = CredentialAuthRequired
	case "UnknownOptionsError", "Invalid choice":
		kind = CredentialUnsupported
	}
	return &CredentialError{Kind: kind, Code: code, err: err}
}

func structuredErrorCode(data []byte) string {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return ""
	}
	return findErrorCode(value)
}

func findErrorCode(value any) string {
	switch value := value.(type) {
	case map[string]any:
		for _, key := range []string{"Code", "code", "ErrorCode", "errorCode", "__type"} {
			if code, ok := value[key].(string); ok {
				return code
			}
		}
		for _, child := range value {
			if code := findErrorCode(child); code != "" {
				return code
			}
		}
	case []any:
		for _, child := range value {
			if code := findErrorCode(child); code != "" {
				return code
			}
		}
	}
	return ""
}
