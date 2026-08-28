package awsbrowser

import (
	"errors"
	"testing"
)

func TestProviderOperationContractAcceptsOnlyOwnedOperations(t *testing.T) {
	for _, pair := range [][2]string{
		{ProviderEC2, OperationDescribeInstances},
		{ProviderEC2, OperationDescribeSecurityGroupRules},
		{ProviderIAM, OperationListRoles},
		{ProviderIAM, OperationGetPolicyVersion},
		{ProviderRoute53, OperationListHostedZones},
		{ProviderRoute53, OperationListResourceRecordSets},
		{ProviderCloudFront, OperationListDistributions},
		{ProviderELBV2, OperationDescribeLoadBalancers},
		{ProviderELBV2, OperationDescribeTargetHealth},
		{ProviderS3, OperationGetBucketLocation},
	} {
		if err := ValidateProviderOperation(pair[0], pair[1]); err != nil {
			t.Fatalf("valid provider operation rejected: %q %q: %v", pair[0], pair[1], err)
		}
	}
	for _, pair := range [][2]string{
		{"", OperationDescribeInstances},
		{ProviderIAM, OperationDescribeInstances},
		{ProviderRoute53, OperationListRoles},
		{ProviderCloudFront, OperationGetBucketLocation},
		{ProviderS3, OperationListDistributions},
		{ProviderELBV2, OperationDescribeInstances},
		{ProviderEC2, "describe-instances"},
		{"unknown", "Unknown"},
	} {
		if err := ValidateProviderOperation(pair[0], pair[1]); !errors.Is(err, ErrInvalidProviderOperation) {
			t.Fatalf("invalid provider operation accepted: %q %q: %v", pair[0], pair[1], err)
		}
	}
}
