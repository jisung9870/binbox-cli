package awsbrowser

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeCredentialExporter struct {
	profile string
	env     []string
	output  []byte
	err     error
}

func (f *fakeCredentialExporter) ExportCredentials(_ context.Context, profile string, env []string) ([]byte, error) {
	f.profile = profile
	f.env = append([]string(nil), env...)
	return append([]byte(nil), f.output...), f.err
}

func TestCredentialProviderRetrievesNamedCredentialsAndTracksGeneration(t *testing.T) {
	exporter := &fakeCredentialExporter{output: []byte(`{
		"Version": 1,
		"AccessKeyId": "AKIAFIXTURE",
		"SecretAccessKey": "secret-fixture",
		"SessionToken": "token-fixture",
		"Expiration": "2030-01-02T03:04:05Z"
	}`)}
	baseEnv := []string{
		"SAFE=value",
		"AWS_PROFILE=ambient",
		"AWS_DEFAULT_PROFILE=ambient-default",
		"AWS_ACCESS_KEY_ID=ambient-key",
		"AWS_SECRET_ACCESS_KEY=ambient-secret",
		"AWS_SESSION_TOKEN=ambient-token",
		"AWS_ROLE_ARN=arn:aws:iam::123456789012:role/ambient",
		"AWS_ENDPOINT_URL=http://127.0.0.1:1",
		"AWS_ENDPOINT_URL_STS=http://127.0.0.1:2",
		"AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=false",
	}
	provider, err := NewCredentialProvider(exporter, "dev", baseEnv)
	if err != nil {
		t.Fatal(err)
	}
	baseEnv[0] = "SAFE=mutated-after-construction"

	credentials, err := provider.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if exporter.profile != "dev" {
		t.Fatalf("profile=%q want dev", exporter.profile)
	}
	env := environmentMap(exporter.env)
	if got := env["SAFE"]; got != "value" {
		t.Fatalf("copied SAFE=%q want value", got)
	}
	for name := range namedProfileIdentityEnv {
		if _, ok := env[name]; ok {
			t.Fatalf("named export retained identity variable %s", name)
		}
	}
	assertEndpointEnvironmentStripped(t, env)

	if credentials.AccessKeyID != "AKIAFIXTURE" ||
		credentials.SecretAccessKey != "secret-fixture" ||
		credentials.SessionToken != "token-fixture" {
		t.Fatalf("credentials did not match fixture")
	}
	if !credentials.CanExpire || !credentials.Expires.Equal(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("expiration=%s canExpire=%v", credentials.Expires, credentials.CanExpire)
	}
	if credentials.Source != "AWS CLI export-credentials" {
		t.Fatalf("source=%q", credentials.Source)
	}
	if got := provider.Generation(); got != 1 {
		t.Fatalf("generation=%d want=1", got)
	}
}

func TestCredentialProviderPreservesAmbientIdentityEnvironment(t *testing.T) {
	exporter := &fakeCredentialExporter{output: []byte(`{
		"Version":1,
		"AccessKeyId":"AKIAAMBIENT",
		"SecretAccessKey":"ambient-secret"
	}`)}
	provider, err := NewCredentialProvider(exporter, "", []string{
		"AWS_PROFILE=ambient",
		"AWS_ACCESS_KEY_ID=ambient-key",
		"AWS_SECRET_ACCESS_KEY=ambient-secret",
		"AWS_ENDPOINT_URL=http://127.0.0.1:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Retrieve(context.Background()); err != nil {
		t.Fatal(err)
	}

	env := environmentMap(exporter.env)
	if env["AWS_PROFILE"] != "ambient" || env["AWS_ACCESS_KEY_ID"] != "ambient-key" {
		t.Fatalf("ambient environment not preserved: %v", env)
	}
	assertEndpointEnvironmentStripped(t, env)
}

func TestCredentialProviderRejectsInvalidProfileBeforeExecution(t *testing.T) {
	exporter := &fakeCredentialExporter{}
	for _, profile := range []string{"contains space", "--inject", "line\nbreak", "" + string(rune(0x1b))} {
		if _, err := NewCredentialProvider(exporter, profile, []string{}); err == nil {
			t.Fatalf("profile %q was accepted", profile)
		}
	}
	if exporter.profile != "" {
		t.Fatalf("export unexpectedly called for profile %q", exporter.profile)
	}
}

func TestCredentialProviderRejectsMalformedDocumentsWithoutLeaks(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{name: "not JSON", output: `credential-super-secret`},
		{name: "wrong version", output: `{"Version":2,"AccessKeyId":"AKIA","SecretAccessKey":"credential-super-secret"}`},
		{name: "missing secret", output: `{"Version":1,"AccessKeyId":"AKIA"}`},
		{name: "bad expiration", output: `{"Version":1,"AccessKeyId":"AKIA","SecretAccessKey":"credential-super-secret","Expiration":"credential-super-secret"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			exporter := &fakeCredentialExporter{output: []byte(test.output)}
			provider, err := NewCredentialProvider(exporter, "", []string{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = provider.Retrieve(context.Background())
			var credentialError *CredentialError
			if !errors.As(err, &credentialError) || credentialError.Kind != CredentialInvalid {
				t.Fatalf("error=%v want CredentialInvalid", err)
			}
			for current := err; current != nil; current = errors.Unwrap(current) {
				if strings.Contains(current.Error(), "credential-super-secret") {
					t.Fatalf("error chain leaked credential document: %q", current)
				}
			}
			if got := provider.Generation(); got != 0 {
				t.Fatalf("generation=%d want=0", got)
			}
		})
	}
}

func TestCredentialErrorClassification(t *testing.T) {
	tests := []struct {
		name string
		cli  *CLIError
		want CredentialErrorKind
	}{
		{name: "auth", cli: &CLIError{Kind: CLIAuthRequired, Code: "SSOTokenLoadError"}, want: CredentialAuthRequired},
		{name: "unsupported", cli: &CLIError{Kind: CLIUnsupported, Code: "UnsupportedCLIErrorFormat"}, want: CredentialUnsupported},
		{name: "cancelled", cli: &CLIError{Kind: CLICancelled}, want: CredentialCancelled},
		{name: "output", cli: &CLIError{Kind: CLIOutputTooLarge}, want: CredentialOutputTooLarge},
		{name: "invalid", cli: &CLIError{Kind: CLIInvalidOutput}, want: CredentialInvalid},
		{name: "unknown", cli: &CLIError{Kind: CLIUnknown}, want: CredentialUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := classifyCredentialError(context.Background(), test.cli)
			var credentialError *CredentialError
			if !errors.As(err, &credentialError) || credentialError.Kind != test.want {
				t.Fatalf("error=%v kind=%q want=%q", err, credentialError.Kind, test.want)
			}
		})
	}
}

func TestCredentialCancellationRetainsContextCause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := classifyCredentialError(ctx, errors.New("process still exiting"))
	var credentialError *CredentialError
	if !errors.As(err, &credentialError) || credentialError.Kind != CredentialCancelled {
		t.Fatalf("error=%v want CredentialCancelled", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v does not retain cancellation", err)
	}
}

func TestCredentialEnvironmentIsDeterministic(t *testing.T) {
	base := []string{"Z=last", "A=first", "INVALID", "=empty-name", "A=replaced"}
	want := []string{"A=replaced", "AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true", "Z=last"}
	if got := credentialEnvironment(base, false); !reflect.DeepEqual(got, want) {
		t.Fatalf("environment=%q want=%q", got, want)
	}
}
