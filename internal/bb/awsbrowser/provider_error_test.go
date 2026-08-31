package awsbrowser

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/smithy-go"
)

type requestIDWrapper struct {
	err       error
	requestID string
}

func (e requestIDWrapper) Error() string            { return "opaque request failure" }
func (e requestIDWrapper) Unwrap() error            { return e.err }
func (e requestIDWrapper) ServiceRequestID() string { return e.requestID }

func TestClassifyProviderErrorUsesTypedChainWithoutMessages(t *testing.T) {
	for _, test := range []struct {
		code string
		kind ProviderErrorKind
	}{
		{"AccessDenied", ProviderForbidden},
		{"ExpiredToken", ProviderAuthRequired},
		{"ThrottlingException", ProviderThrottled},
		{"Unexpected", ProviderUnknown},
	} {
		t.Run(test.code, func(t *testing.T) {
			api := &smithy.GenericAPIError{Code: test.code, Message: "secret provider message"}
			err := requestIDWrapper{err: &smithy.OperationError{ServiceID: "ec2", OperationName: "DescribeInstances", Err: api}, requestID: "req-123"}
			classified := ClassifyProviderError(err, "fallback", "Fallback")
			var provider *ProviderError
			if !errors.As(classified, &provider) || provider.Kind != test.kind || provider.Service != "ec2" ||
				provider.Operation != "DescribeInstances" || provider.Code != test.code || provider.RequestID != "req-123" {
				t.Fatalf("classified=%+v", provider)
			}
			if provider.Error() != "AWS provider query failed" {
				t.Fatalf("provider error exposed raw text: %q", provider.Error())
			}
		})
	}
}

func TestClassifyProviderErrorPreservesControlFlowAndNotFound(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, ErrContextChanged} {
		if got := ClassifyProviderError(err, ProviderEC2, OperationDescribeInstances); !errors.Is(got, err) {
			t.Fatalf("control error %v became %v", err, got)
		}
	}
	notFound := &smithy.GenericAPIError{Code: "NoSuchEntity", Message: "role missing"}
	missingAMI := &smithy.GenericAPIError{Code: "InvalidAMIID.NotFound", Message: "AMI missing"}
	if !IsProviderNotFound(notFound) || !IsProviderNotFound(missingAMI) || IsProviderNotFound(errors.New("NoSuchEntity")) {
		t.Fatal("not-found classification did not require a typed API code")
	}
}

func TestClassifyProviderErrorDropsUnsafeMetadata(t *testing.T) {
	api := &smithy.GenericAPIError{Code: "AccessDenied\x1b[31m", Message: "secret"}
	err := requestIDWrapper{err: api, requestID: "request id with spaces"}
	var provider *ProviderError
	if !errors.As(ClassifyProviderError(err, "e\nc2", "DescribeInstances\x00"), &provider) {
		t.Fatal("missing provider error")
	}
	if provider.Service != "" || provider.Operation != "" || provider.Code != "" || provider.RequestID != "" {
		t.Fatalf("unsafe metadata survived: service=%q operation=%q code=%q requestID=%q", provider.Service, provider.Operation, provider.Code, provider.RequestID)
	}
}
