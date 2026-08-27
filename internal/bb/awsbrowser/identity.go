package awsbrowser

import (
	"errors"
	"regexp"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

var (
	accountIDRE = regexp.MustCompile(`^[0-9]{12}$`)
	partitionRE = regexp.MustCompile(`^aws(?:-[a-z0-9]+)*$`)
)

var errInvalidCallerIdentity = errors.New("AWS caller identity response is invalid")

func verifiedIdentity(output *sts.GetCallerIdentityOutput, generation uint64) (VerifiedIdentity, error) {
	if output == nil || generation == 0 {
		return VerifiedIdentity{}, errInvalidCallerIdentity
	}

	accountID := aws.ToString(output.Account)
	principalARN := aws.ToString(output.Arn)
	if !accountIDRE.MatchString(accountID) || principalARN == "" {
		return VerifiedIdentity{}, errInvalidCallerIdentity
	}

	parsed, err := arn.Parse(principalARN)
	if err != nil || !partitionRE.MatchString(parsed.Partition) || parsed.AccountID != accountID || parsed.Region != "" ||
		parsed.Resource == "" || identityContainsForbiddenControl(principalARN) {
		return VerifiedIdentity{}, errInvalidCallerIdentity
	}
	if parsed.Service != "iam" && parsed.Service != "sts" {
		return VerifiedIdentity{}, errInvalidCallerIdentity
	}

	return VerifiedIdentity{
		Partition:            parsed.Partition,
		AccountID:            accountID,
		PrincipalARN:         principalARN,
		CredentialGeneration: generation,
	}, nil
}

func identityContainsForbiddenControl(value string) bool {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}
