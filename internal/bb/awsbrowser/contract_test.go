package awsbrowser_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
)

type contractRuntime struct {
	identity awsbrowser.VerifiedIdentity
}

func TestProviderInterfacesAcceptNoSDKOperationOptions(t *testing.T) {
	interfaces := []struct {
		name   string
		typeOf reflect.Type
	}{
		{name: "STS", typeOf: reflect.TypeOf((*awsbrowser.STSAPI)(nil)).Elem()},
		{name: "EC2", typeOf: reflect.TypeOf((*awsbrowser.EC2API)(nil)).Elem()},
		{name: "IAM", typeOf: reflect.TypeOf((*awsbrowser.IAMAPI)(nil)).Elem()},
		{name: "Route53", typeOf: reflect.TypeOf((*awsbrowser.Route53API)(nil)).Elem()},
		{name: "CloudFront", typeOf: reflect.TypeOf((*awsbrowser.CloudFrontAPI)(nil)).Elem()},
		{name: "S3", typeOf: reflect.TypeOf((*awsbrowser.S3API)(nil)).Elem()},
	}

	for _, service := range interfaces {
		t.Run(service.name, func(t *testing.T) {
			for i := 0; i < service.typeOf.NumMethod(); i++ {
				method := service.typeOf.Method(i)
				if method.Type.IsVariadic() {
					t.Errorf("%s is variadic: %v", method.Name, method.Type)
				}
				if method.Type.NumIn() != 2 {
					t.Errorf("%s parameters=%d want context and input only", method.Name, method.Type.NumIn())
				}
				if strings.Contains(method.Type.String(), ".Options") {
					t.Errorf("%s exposes SDK options: %v", method.Name, method.Type)
				}
			}
		})
	}
}

func (r contractRuntime) Identity() awsbrowser.VerifiedIdentity { return r.identity }
func (contractRuntime) STS() awsbrowser.STSAPI                  { return nil }
func (contractRuntime) EC2() awsbrowser.EC2API                  { return nil }
func (contractRuntime) IAM() awsbrowser.IAMAPI                  { return nil }
func (contractRuntime) Route53() awsbrowser.Route53API          { return nil }
func (contractRuntime) CloudFront() awsbrowser.CloudFrontAPI    { return nil }
func (contractRuntime) S3() awsbrowser.S3API                    { return nil }

type contractFactory struct {
	runtime awsbrowser.RuntimeContext
}

func (f contractFactory) Resolve(context.Context, awsbrowser.ContextSpec) (awsbrowser.RuntimeContext, error) {
	return f.runtime, nil
}

var (
	_ awsbrowser.RuntimeContext = contractRuntime{}
	_ awsbrowser.RuntimeFactory = contractFactory{}
)

func TestRuntimeContractCarriesCredentialFreeIdentity(t *testing.T) {
	want := awsbrowser.VerifiedIdentity{
		Partition:            "aws",
		AccountID:            "123456789012",
		PrincipalARN:         "arn:aws:sts::123456789012:assumed-role/ReadOnly/session",
		CredentialGeneration: 7,
	}
	factory := contractFactory{runtime: contractRuntime{identity: want}}

	runtime, err := factory.Resolve(context.Background(), awsbrowser.ContextSpec{
		Mode:    awsbrowser.ContextModeNamedProfile,
		Profile: "dev",
		Region:  "ap-northeast-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := runtime.Identity(); got != want {
		t.Fatalf("identity=%+v want=%+v", got, want)
	}
}

func TestContextModeValuesAreStable(t *testing.T) {
	if got := string(awsbrowser.ContextModeAmbient); got != "ambient" {
		t.Fatalf("ambient mode=%q", got)
	}
	if got := string(awsbrowser.ContextModeNamedProfile); got != "named-profile" {
		t.Fatalf("named-profile mode=%q", got)
	}
}

func TestInterfacesExposeOnlyApprovedOperations(t *testing.T) {
	tests := []struct {
		name   string
		typeOf reflect.Type
		want   []string
	}{
		{
			name:   "RuntimeFactory",
			typeOf: reflect.TypeOf((*awsbrowser.RuntimeFactory)(nil)).Elem(),
			want:   []string{"Resolve"},
		},
		{
			name:   "RuntimeContext",
			typeOf: reflect.TypeOf((*awsbrowser.RuntimeContext)(nil)).Elem(),
			want:   []string{"CloudFront", "EC2", "IAM", "Identity", "Route53", "S3", "STS"},
		},
		{
			name:   "STS",
			typeOf: reflect.TypeOf((*awsbrowser.STSAPI)(nil)).Elem(),
			want:   []string{"GetCallerIdentity"},
		},
		{
			name:   "EC2",
			typeOf: reflect.TypeOf((*awsbrowser.EC2API)(nil)).Elem(),
			want: []string{
				"DescribeInstances",
				"DescribeRouteTables",
				"DescribeSecurityGroupRules",
				"DescribeSecurityGroups",
				"DescribeSubnets",
				"DescribeVolumes",
				"DescribeVpcs",
			},
		},
		{
			name:   "IAM",
			typeOf: reflect.TypeOf((*awsbrowser.IAMAPI)(nil)).Elem(),
			want: []string{
				"GetInstanceProfile",
				"GetPolicy",
				"GetPolicyVersion",
				"GetRole",
				"GetRolePolicy",
				"ListAttachedRolePolicies",
				"ListRolePolicies",
				"ListRoles",
			},
		},
		{
			name:   "Route53",
			typeOf: reflect.TypeOf((*awsbrowser.Route53API)(nil)).Elem(),
			want: []string{
				"ListHostedZones",
				"ListHostedZonesByName",
				"ListResourceRecordSets",
			},
		},
		{
			name:   "CloudFront",
			typeOf: reflect.TypeOf((*awsbrowser.CloudFrontAPI)(nil)).Elem(),
			want:   []string{"ListDistributions"},
		},
		{
			name:   "S3",
			typeOf: reflect.TypeOf((*awsbrowser.S3API)(nil)).Elem(),
			want:   []string{"GetBucketLocation"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := make([]string, test.typeOf.NumMethod())
			for i := range got {
				got[i] = test.typeOf.Method(i).Name
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("methods=%v want=%v", got, test.want)
			}
		})
	}
}
