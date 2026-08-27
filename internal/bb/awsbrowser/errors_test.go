package awsbrowser

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCLIErrorClassificationUsesStructuredCodes(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		kind   CLIErrorKind
		code   string
	}{
		{
			name:   "expired SSO",
			stderr: `{"Code":"SSOTokenLoadError","Message":"Token has expired and refresh failed"}`,
			kind:   CLIAuthRequired,
			code:   "SSOTokenLoadError",
		},
		{
			name:   "nested expired SSO message",
			stderr: `{"Code":"Configuration","Message":"Error when retrieving token from sso: Token has expired and refresh failed"}`,
			kind:   CLIAuthRequired,
			code:   "Configuration",
		},
		{
			name:   "unsupported option",
			stderr: `{"Code":"UnknownOptionsError","Message":"Unknown options: --cli-error-format"}`,
			kind:   CLIUnsupported,
			code:   "UnsupportedCLIErrorFormat",
		},
		{
			name:   "ordinary configuration",
			stderr: `{"Code":"Configuration","Message":"The config profile could not be found"}`,
			kind:   CLIUnknown,
			code:   "Configuration",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := classifyCLIError(context.Background(), cliOperationExportCredentials, []byte(test.stderr), errors.New("exit status 1"))
			var cliError *CLIError
			if !errors.As(err, &cliError) {
				t.Fatalf("error=%v want CLIError", err)
			}
			if cliError.Kind != test.kind || cliError.Code != test.code {
				t.Fatalf("kind=%q code=%q want kind=%q code=%q", cliError.Kind, cliError.Code, test.kind, test.code)
			}
		})
	}
}

func TestCLIErrorClassificationRecognizesUnstructuredCapabilityFailures(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		stderr    string
		code      string
	}{
		{name: "error format", operation: cliOperationExportCredentials, stderr: "Unknown options: --cli-error-format, credential-super-secret", code: "UnsupportedCLIErrorFormat"},
		{name: "auto prompt", operation: cliOperationExportCredentials, stderr: "Unknown options: --no-cli-auto-prompt", code: "UnsupportedCLIAutoPrompt"},
		{name: "pager", operation: cliOperationListProfiles, stderr: "Unknown options: --no-cli-pager", code: "UnsupportedCLIPager"},
		{name: "credential export", operation: cliOperationExportCredentials, stderr: "argument operation: Invalid choice: 'export-credentials'", code: "UnsupportedExportCredentials"},
		{name: "profile list", operation: cliOperationListProfiles, stderr: "argument operation: Invalid choice: 'list-profiles'", code: "UnsupportedListProfiles"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := classifyCLIError(context.Background(), test.operation, []byte(test.stderr), errors.New("exit status 2"))
			var cliError *CLIError
			if !errors.As(err, &cliError) || cliError.Kind != CLIUnsupported || cliError.Code != test.code {
				t.Fatalf("error=%v kind=%q code=%q", err, cliError.Kind, cliError.Code)
			}
			if strings.Contains(err.Error(), "credential-super-secret") {
				t.Fatalf("sanitized error leaked stderr: %q", err)
			}
		})
	}
}

func TestCLIErrorClassificationRecognizesUnstructuredExpiredSSO(t *testing.T) {
	stderr := []byte(`Error when retrieving token from sso: Token has expired and refresh failed`)
	err := classifyCLIError(context.Background(), cliOperationExportCredentials, stderr, errors.New("exit status 1"))
	var cliError *CLIError
	if !errors.As(err, &cliError) || cliError.Kind != CLIAuthRequired || cliError.Code != "SSOTokenLoadError" {
		t.Fatalf("error=%v kind=%q code=%q", err, cliError.Kind, cliError.Code)
	}
}

func TestSanitizedErrorsRejectControlCharactersAndRawMessages(t *testing.T) {
	cliError := &CLIError{
		Kind:      CLIUnknown,
		Operation: "unsafe\x1boperation",
		Code:      "unsafe\x1bcode",
		err:       errors.New("exit status 1"),
	}
	credentialError := classifyCredentialError(context.Background(), cliError)
	for _, err := range []error{cliError, credentialError} {
		if strings.Contains(err.Error(), "\x1b") || strings.Contains(err.Error(), "unsafe") {
			t.Fatalf("error was not sanitized: %q", err)
		}
	}
}

func TestSanitizedErrorsRejectHostileStructuredCodes(t *testing.T) {
	const secret = "credential-super-secret"
	err := classifyCLIError(
		context.Background(),
		cliOperationExportCredentials,
		[]byte(`{"Code":"credential-super-secret","Message":"ordinary failure"}`),
		errors.New("exporter credential-super-secret"),
	)
	credentialErr := classifyCredentialError(context.Background(), err)

	for _, rendered := range []error{err, credentialErr} {
		for current := rendered; current != nil; current = errors.Unwrap(current) {
			if strings.Contains(current.Error(), secret) {
				t.Fatalf("error chain leaked hostile value: %q", current)
			}
		}
	}
	var cliError *CLIError
	if !errors.As(err, &cliError) || cliError.Code != "" {
		t.Fatalf("hostile code was retained: %#v", cliError)
	}
}

func TestCredentialErrorsDoNotUnwrapArbitraryExporterErrors(t *testing.T) {
	const secret = "exporter-credential-super-secret"
	err := classifyCredentialError(context.Background(), errors.New(secret))
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("rendered error leaked exporter value: %q", err)
	}
	if unwrapped := errors.Unwrap(err); unwrapped != nil {
		t.Fatalf("arbitrary exporter error remained in chain: %q", unwrapped)
	}
}
