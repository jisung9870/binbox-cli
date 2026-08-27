package awsbrowser

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

func TestVerifiedIdentityParsesCallerARNPartitions(t *testing.T) {
	tests := []struct {
		name      string
		account   string
		arn       string
		partition string
	}{
		{
			name:      "commercial assumed role",
			account:   "123456789012",
			arn:       "arn:aws:sts::123456789012:assumed-role/ReadOnly/session",
			partition: "aws",
		},
		{
			name:      "govcloud IAM user",
			account:   "210987654321",
			arn:       "arn:aws-us-gov:iam::210987654321:user/reader",
			partition: "aws-us-gov",
		},
		{
			name:      "china root",
			account:   "111122223333",
			arn:       "arn:aws-cn:iam::111122223333:root",
			partition: "aws-cn",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			identity, err := verifiedIdentity(&sts.GetCallerIdentityOutput{
				Account: aws.String(test.account),
				Arn:     aws.String(test.arn),
			}, 9)
			if err != nil {
				t.Fatal(err)
			}
			if identity.Partition != test.partition || identity.AccountID != test.account ||
				identity.PrincipalARN != test.arn || identity.CredentialGeneration != 9 {
				t.Fatalf("identity=%+v", identity)
			}
		})
	}
}

func TestVerifiedIdentityRejectsUnboundOrMalformedResponses(t *testing.T) {
	tests := []struct {
		name       string
		output     *sts.GetCallerIdentityOutput
		generation uint64
	}{
		{name: "nil", output: nil, generation: 1},
		{name: "zero generation", output: callerIdentity("aws", "123456789012", "reader"), generation: 0},
		{name: "missing account", output: &sts.GetCallerIdentityOutput{Arn: aws.String("arn:aws:iam::123456789012:user/reader")}, generation: 1},
		{name: "invalid account", output: &sts.GetCallerIdentityOutput{Account: aws.String("1234"), Arn: aws.String("arn:aws:iam::1234:user/reader")}, generation: 1},
		{name: "mismatched account", output: &sts.GetCallerIdentityOutput{Account: aws.String("123456789012"), Arn: aws.String("arn:aws:iam::210987654321:user/reader")}, generation: 1},
		{name: "invalid partition", output: &sts.GetCallerIdentityOutput{Account: aws.String("123456789012"), Arn: aws.String("arn:AWS:iam::123456789012:user/reader")}, generation: 1},
		{name: "unexpected region", output: &sts.GetCallerIdentityOutput{Account: aws.String("123456789012"), Arn: aws.String("arn:aws:sts:us-east-1:123456789012:assumed-role/ReadOnly/reader")}, generation: 1},
		{name: "unsupported service", output: &sts.GetCallerIdentityOutput{Account: aws.String("123456789012"), Arn: aws.String("arn:aws:s3::123456789012:bucket")}, generation: 1},
		{name: "empty resource", output: &sts.GetCallerIdentityOutput{Account: aws.String("123456789012"), Arn: aws.String("arn:aws:iam::123456789012:")}, generation: 1},
		{name: "control character", output: &sts.GetCallerIdentityOutput{Account: aws.String("123456789012"), Arn: aws.String("arn:aws:iam::123456789012:user/read\ner")}, generation: 1},
		{name: "malformed ARN", output: &sts.GetCallerIdentityOutput{Account: aws.String("123456789012"), Arn: aws.String("not-an-arn")}, generation: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := verifiedIdentity(test.output, test.generation)
			if !errors.Is(err, errInvalidCallerIdentity) {
				t.Fatalf("error=%v want invalid caller identity", err)
			}
		})
	}
}

func callerIdentity(partition, account, principal string) *sts.GetCallerIdentityOutput {
	return &sts.GetCallerIdentityOutput{
		Account: aws.String(account),
		Arn:     aws.String("arn:" + partition + ":sts::" + account + ":assumed-role/ReadOnly/" + principal),
	}
}
