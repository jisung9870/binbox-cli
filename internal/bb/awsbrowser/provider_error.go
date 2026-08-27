package awsbrowser

import (
	"context"
	"errors"

	"github.com/aws/smithy-go"
)

type serviceRequestIDError interface {
	ServiceRequestID() string
}

// ClassifyProviderError converts an SDK error chain into a credential-free,
// message-free failure. Raw SDK error text and payloads are never retained.
func ClassifyProviderError(err error, service, operation string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, ErrContextChanged) {
		return ErrContextChanged
	}

	var operationError *smithy.OperationError
	if errors.As(err, &operationError) {
		if operationError.Service() != "" {
			service = operationError.Service()
		}
		if operationError.Operation() != "" {
			operation = operationError.Operation()
		}
	}
	code := ""
	var apiError smithy.APIError
	if errors.As(err, &apiError) {
		code = apiError.ErrorCode()
	}
	requestID := ""
	var requestError serviceRequestIDError
	if errors.As(err, &requestError) {
		requestID = requestError.ServiceRequestID()
	}
	return NewProviderError(providerKindForCode(code), service, operation, code, requestID)
}

func providerKindForCode(code string) ProviderErrorKind {
	switch code {
	case "AccessDenied", "AccessDeniedException", "UnauthorizedOperation":
		return ProviderForbidden
	case "ExpiredToken", "ExpiredTokenException", "InvalidClientTokenId", "UnrecognizedClientException", "RequestExpired":
		return ProviderAuthRequired
	case "Throttling", "ThrottlingException", "RequestLimitExceeded", "TooManyRequestsException", "SlowDown":
		return ProviderThrottled
	default:
		return ProviderUnknown
	}
}

// IsProviderNotFound is intentionally separate because only an exact lookup
// may translate a modeled absence into an Empty result. List operations must
// not silently convert the same code into success.
func IsProviderNotFound(err error) bool {
	var apiError smithy.APIError
	return errors.As(err, &apiError) && apiError.ErrorCode() == "NoSuchEntity"
}
