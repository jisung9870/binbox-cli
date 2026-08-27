package awsbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// CLIErrorKind classifies failures from the two approved AWS CLI control-plane
// capabilities. Raw stdout and stderr are deliberately not retained.
type CLIErrorKind string

const (
	CLIUnknown        CLIErrorKind = "unknown"
	CLIAuthRequired   CLIErrorKind = "auth_required"
	CLIUnsupported    CLIErrorKind = "unsupported"
	CLICancelled      CLIErrorKind = "cancelled"
	CLIOutputTooLarge CLIErrorKind = "output_too_large"
	CLIInvalidOutput  CLIErrorKind = "invalid_output"
)

// CLIError is a sanitized AWS CLI failure. Code contains only a bounded error
// identifier, never a CLI message or raw command output.
type CLIError struct {
	Kind      CLIErrorKind
	Operation string
	Code      string
	err       error
}

func (e *CLIError) Error() string {
	operation := "control-plane operation"
	if e != nil && (e.Operation == cliOperationListProfiles || e.Operation == cliOperationExportCredentials) {
		operation = e.Operation
	}
	if e != nil {
		if code := sanitizeErrorCode(e.Code); code != "" {
			return fmt.Sprintf("AWS CLI %s failed (%s)", operation, code)
		}
	}
	return fmt.Sprintf("AWS CLI %s failed", operation)
}

func (e *CLIError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
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

// CredentialError is safe to render or log. It never retains credential
// documents or raw AWS CLI output.
type CredentialError struct {
	Kind CredentialErrorKind
	Code string
	err  error
}

func (e *CredentialError) Error() string {
	if e != nil {
		if code := sanitizeErrorCode(e.Code); code != "" {
			return fmt.Sprintf("AWS credential export failed (%s)", code)
		}
	}
	return "AWS credential export failed"
}

func (e *CredentialError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

type OutputLimitError struct {
	Stream string
	Limit  int
}

func (e *OutputLimitError) Error() string {
	return fmt.Sprintf("AWS CLI %s exceeded %d bytes", e.Stream, e.Limit)
}

func classifyCLIError(ctx context.Context, operation string, stderr []byte, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return &CLIError{Kind: CLICancelled, Operation: operation, err: ctxErr}
	}

	var limitError *OutputLimitError
	if errors.As(err, &limitError) {
		return &CLIError{
			Kind:      CLIOutputTooLarge,
			Operation: operation,
			err:       limitError,
		}
	}

	code, message := structuredError(stderr)
	if code == "" && message == "" {
		message = string(stderr)
	}
	code = sanitizeErrorCode(code)
	kind := CLIUnknown

	if unsupportedCode(code) {
		kind = CLIUnsupported
	}
	if stableCode := unsupportedTextCode(operation, stderr); stableCode != "" {
		kind = CLIUnsupported
		code = stableCode
	}
	if ssoAuthenticationExpired(code, message) {
		kind = CLIAuthRequired
		if code == "" {
			code = "SSOTokenLoadError"
		}
	}

	return &CLIError{
		Kind:      kind,
		Operation: operation,
		Code:      code,
		err:       err,
	}
}

func classifyCredentialError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return &CredentialError{Kind: CredentialCancelled, err: ctxErr}
	}

	var cliError *CLIError
	if !errors.As(err, &cliError) {
		return &CredentialError{Kind: CredentialUnknown, err: err}
	}

	kind := CredentialUnknown
	switch cliError.Kind {
	case CLIAuthRequired:
		kind = CredentialAuthRequired
	case CLIUnsupported:
		kind = CredentialUnsupported
	case CLICancelled:
		kind = CredentialCancelled
	case CLIOutputTooLarge:
		kind = CredentialOutputTooLarge
	case CLIInvalidOutput:
		kind = CredentialInvalid
	}
	return &CredentialError{Kind: kind, Code: sanitizeErrorCode(cliError.Code), err: cliError}
}

func structuredError(data []byte) (code, message string) {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return "", ""
	}
	return findErrorCode(value), findErrorMessage(value)
}

func findErrorCode(value any) string {
	switch value := value.(type) {
	case map[string]any:
		for _, key := range []string{"Code", "code", "ErrorCode", "errorCode", "__type", "Type", "type"} {
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

func findErrorMessage(value any) string {
	switch value := value.(type) {
	case map[string]any:
		for _, key := range []string{"Message", "message", "Error", "error"} {
			if message, ok := value[key].(string); ok {
				return message
			}
		}
		for _, child := range value {
			if message := findErrorMessage(child); message != "" {
				return message
			}
		}
	case []any:
		for _, child := range value {
			if message := findErrorMessage(child); message != "" {
				return message
			}
		}
	}
	return ""
}

func sanitizeErrorCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 128 {
		return ""
	}
	for _, char := range code {
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			strings.ContainsRune(" ._:#/-", char) {
			continue
		}
		return ""
	}
	return code
}

func unsupportedCode(code string) bool {
	switch strings.ToLower(strings.ReplaceAll(code, " ", "")) {
	case "unknownoptionserror", "unknownargumenterror", "invalidchoice", "invalidchoiceerror":
		return true
	default:
		return false
	}
}

func unsupportedTextCode(operation string, stderr []byte) string {
	text := strings.ToLower(string(stderr))
	unknown := strings.Contains(text, "unknown option") ||
		strings.Contains(text, "unknown argument") ||
		strings.Contains(text, "invalid choice")
	if !unknown {
		return ""
	}
	if strings.Contains(text, "--cli-error-format") {
		return "UnsupportedCLIErrorFormat"
	}
	if strings.Contains(text, "--no-cli-auto-prompt") {
		return "UnsupportedCLIAutoPrompt"
	}
	if strings.Contains(text, "--no-cli-pager") {
		return "UnsupportedCLIPager"
	}
	if operation == cliOperationExportCredentials && strings.Contains(text, "export-credentials") {
		return "UnsupportedExportCredentials"
	}
	if operation == cliOperationListProfiles && strings.Contains(text, "list-profiles") {
		return "UnsupportedListProfiles"
	}
	return ""
}

func ssoAuthenticationExpired(code, message string) bool {
	switch code {
	case "SSOTokenLoadError", "InvalidGrantException", "UnauthorizedException", "ExpiredToken":
		return true
	}

	message = strings.ToLower(message)
	ssoEvidence := strings.Contains(message, "sso token") ||
		strings.Contains(message, "token from sso")
	expiryEvidence := strings.Contains(message, "expired") ||
		strings.Contains(message, "does not exist") ||
		strings.Contains(message, "not found")
	return ssoEvidence && expiryEvidence
}
