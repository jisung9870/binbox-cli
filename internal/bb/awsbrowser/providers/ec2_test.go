package providers

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
)

type fakeEC2 struct {
	instances func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error)
	images    func(context.Context, *ec2.DescribeImagesInput) (*ec2.DescribeImagesOutput, error)
	volumes   func(context.Context, *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error)
	groups    func(context.Context, *ec2.DescribeSecurityGroupsInput) (*ec2.DescribeSecurityGroupsOutput, error)
	rules     func(context.Context, *ec2.DescribeSecurityGroupRulesInput) (*ec2.DescribeSecurityGroupRulesOutput, error)
	vpcs      func(context.Context, *ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error)
	subnets   func(context.Context, *ec2.DescribeSubnetsInput) (*ec2.DescribeSubnetsOutput, error)
	routes    func(context.Context, *ec2.DescribeRouteTablesInput) (*ec2.DescribeRouteTablesOutput, error)
	peerings  func(context.Context, *ec2.DescribeVpcPeeringConnectionsInput) (*ec2.DescribeVpcPeeringConnectionsOutput, error)
	templates func(context.Context, *ec2.DescribeLaunchTemplatesInput) (*ec2.DescribeLaunchTemplatesOutput, error)
	versions  func(context.Context, *ec2.DescribeLaunchTemplateVersionsInput) (*ec2.DescribeLaunchTemplateVersionsOutput, error)
}

func (f *fakeEC2) DescribeImages(c context.Context, i *ec2.DescribeImagesInput) (*ec2.DescribeImagesOutput, error) {
	if f.images == nil {
		panic("unexpected DescribeImages")
	}
	return f.images(c, i)
}

func (f *fakeEC2) DescribeInstances(c context.Context, i *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
	if f.instances == nil {
		panic("unexpected DescribeInstances")
	}
	return f.instances(c, i)
}
func (f *fakeEC2) DescribeVolumes(c context.Context, i *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error) {
	if f.volumes == nil {
		panic("unexpected DescribeVolumes")
	}
	return f.volumes(c, i)
}
func (f *fakeEC2) DescribeSecurityGroups(c context.Context, i *ec2.DescribeSecurityGroupsInput) (*ec2.DescribeSecurityGroupsOutput, error) {
	if f.groups == nil {
		panic("unexpected DescribeSecurityGroups")
	}
	return f.groups(c, i)
}
func (f *fakeEC2) DescribeSecurityGroupRules(c context.Context, i *ec2.DescribeSecurityGroupRulesInput) (*ec2.DescribeSecurityGroupRulesOutput, error) {
	if f.rules == nil {
		panic("unexpected DescribeSecurityGroupRules")
	}
	return f.rules(c, i)
}
func (f *fakeEC2) DescribeVpcs(c context.Context, i *ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error) {
	if f.vpcs == nil {
		panic("unexpected DescribeVpcs")
	}
	return f.vpcs(c, i)
}
func (f *fakeEC2) DescribeSubnets(c context.Context, i *ec2.DescribeSubnetsInput) (*ec2.DescribeSubnetsOutput, error) {
	if f.subnets == nil {
		panic("unexpected DescribeSubnets")
	}
	return f.subnets(c, i)
}
func (f *fakeEC2) DescribeRouteTables(c context.Context, i *ec2.DescribeRouteTablesInput) (*ec2.DescribeRouteTablesOutput, error) {
	if f.routes == nil {
		panic("unexpected DescribeRouteTables")
	}
	return f.routes(c, i)
}
func (f *fakeEC2) DescribeVpcPeeringConnections(c context.Context, i *ec2.DescribeVpcPeeringConnectionsInput) (*ec2.DescribeVpcPeeringConnectionsOutput, error) {
	if f.peerings == nil {
		panic("unexpected DescribeVpcPeeringConnections")
	}
	return f.peerings(c, i)
}
func (f *fakeEC2) DescribeLaunchTemplates(c context.Context, i *ec2.DescribeLaunchTemplatesInput) (*ec2.DescribeLaunchTemplatesOutput, error) {
	if f.templates == nil {
		panic("unexpected DescribeLaunchTemplates")
	}
	return f.templates(c, i)
}
func (f *fakeEC2) DescribeLaunchTemplateVersions(c context.Context, i *ec2.DescribeLaunchTemplateVersionsInput) (*ec2.DescribeLaunchTemplateVersionsOutput, error) {
	if f.versions == nil {
		panic("unexpected DescribeLaunchTemplateVersions")
	}
	return f.versions(c, i)
}

type captureSink struct {
	pages     []awsbrowser.QueryPage
	completed int
	at        time.Time
	pageErr   error
}

func (s *captureSink) Page(p awsbrowser.QueryPage) error {
	if s.pageErr != nil {
		return s.pageErr
	}
	s.pages = append(s.pages, p)
	return nil
}
func (s *captureSink) Complete(at time.Time) error { s.completed++; s.at = at; return nil }

func providerContext(t *testing.T) awsbrowser.AWSContext {
	t.Helper()
	c, e := awsbrowser.NewAWSContext(awsbrowser.ContextSpec{Mode: awsbrowser.ContextModeNamedProfile, Profile: "dev", Region: "us-east-1"}, awsbrowser.VerifiedIdentity{Partition: "aws", AccountID: "123456789012", PrincipalARN: "arn:aws:iam::123456789012:user/test", CredentialGeneration: 1}, "")
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func providerKey(t *testing.T, op string, p map[string]string) awsbrowser.QueryKey {
	t.Helper()
	k, e := awsbrowser.NewQueryKey(providerContext(t), awsbrowser.ProviderEC2, op, p)
	if e != nil {
		t.Fatal(e)
	}
	return k
}
func fixedClock() func() time.Time {
	value := time.Date(2026, 8, 28, 1, 2, 3, 0, time.UTC)
	return func() time.Time { return value }
}

func TestEC2ExecutorSelectsOperationBuildsInputAndMapsRelations(t *testing.T) {
	var got *ec2.DescribeInstancesInput
	client := &fakeEC2{instances: func(_ context.Context, in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		got = in
		return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{Instances: []types.Instance{{
			InstanceId: aws.String("i-1"), ImageId: aws.String("ami-1"), VpcId: aws.String("vpc-1"), SubnetId: aws.String("subnet-1"), InstanceType: types.InstanceTypeT3Micro,
			State: &types.InstanceState{Name: types.InstanceStateNameRunning}, SecurityGroups: []types.GroupIdentifier{{GroupId: aws.String("sg-1")}},
			BlockDeviceMappings: []types.InstanceBlockDeviceMapping{{DeviceName: aws.String("/dev/xvda"), Ebs: &types.EbsInstanceBlockDevice{VolumeId: aws.String("vol-1")}}},
			IamInstanceProfile:  &types.IamInstanceProfile{Arn: aws.String("arn:aws:iam::123456789012:instance-profile/app/web-profile")},
			Tags:                []types.Tag{{Key: aws.String("Name"), Value: aws.String("web")}},
		}}}}}, nil
	}}
	executor, e := NewEC2QueryExecutor(client, fixedClock())
	if e != nil {
		t.Fatal(e)
	}
	sink := &captureSink{}
	if e = executor.Execute(context.Background(), providerKey(t, awsbrowser.OperationDescribeInstances, map[string]string{"vpc-id": "vpc-1", "instance-state-name": "running"}), sink); e != nil {
		t.Fatal(e)
	}
	if got == nil || got.MaxResults == nil || *got.MaxResults != 100 || len(got.Filters) != 2 || got.DryRun != nil || sink.completed != 1 || len(sink.pages) != 1 {
		t.Fatalf("input=%+v sink=%+v", got, sink)
	}
	resource := sink.pages[0].Resources()[0]
	if resource.Key.Type != "ec2.instance" || resource.Key.Region != "us-east-1" {
		t.Fatalf("key=%+v", resource.Key)
	}
	fields := resource.Observation.Fields()
	if fields["tags"].(map[string]string)["Name"] != "web" {
		t.Fatalf("fields=%+v", fields)
	}
	if fields["name"] != "web" {
		t.Fatalf("instance Name tag was not promoted for display: fields=%+v", fields)
	}
	if fields["instance_profile_arn"] != "arn:aws:iam::123456789012:instance-profile/app/web-profile" || fields["instance_profile_name"] != "web-profile" {
		t.Fatalf("profile fields=%+v", fields)
	}
	wantTargets := map[string]string{
		"ec2.image/ami-1":                  "us-east-1",
		"ec2.vpc/vpc-1":                    "us-east-1",
		"ec2.subnet/subnet-1":              "us-east-1",
		"ec2.security-group/sg-1":          "us-east-1",
		"ec2.volume/vol-1":                 "us-east-1",
		"iam.instance-profile/web-profile": awsbrowser.GlobalRegion,
	}
	if gotTargets := exactRelationTargets(t, resource); !reflect.DeepEqual(gotTargets, wantTargets) {
		t.Fatalf("relation targets=%+v want=%+v", gotTargets, wantTargets)
	}
	wantSemantics := map[string]string{
		"ec2.image/ami-1":                  "uses|",
		"ec2.vpc/vpc-1":                    "member-of|",
		"ec2.subnet/subnet-1":              "member-of|",
		"ec2.security-group/sg-1":          "uses|network-interface=unknown",
		"ec2.volume/vol-1":                 "attached-to|/dev/xvda",
		"iam.instance-profile/web-profile": "uses|",
	}
	if gotSemantics := relationSemantics(t, resource); !reflect.DeepEqual(gotSemantics, wantSemantics) {
		t.Fatalf("relation semantics=%+v want=%+v", gotSemantics, wantSemantics)
	}
	assertMappedOnly(t, fields)
}

func TestEC2LaunchTemplatesAndVersionsAreLazyMappedAndRedactUserData(t *testing.T) {
	created := fixedClock()().Add(-time.Hour)
	var templateInput *ec2.DescribeLaunchTemplatesInput
	client := &fakeEC2{templates: func(_ context.Context, input *ec2.DescribeLaunchTemplatesInput) (*ec2.DescribeLaunchTemplatesOutput, error) {
		templateInput = input
		return &ec2.DescribeLaunchTemplatesOutput{LaunchTemplates: []types.LaunchTemplate{{
			LaunchTemplateId: aws.String("lt-123"), LaunchTemplateName: aws.String("web"),
			DefaultVersionNumber: aws.Int64(2), LatestVersionNumber: aws.Int64(3), CreateTime: &created,
			Tags: []types.Tag{{Key: aws.String("Name"), Value: aws.String("web-template")}},
		}}}, nil
	}}
	executor, err := NewEC2QueryExecutor(client, fixedClock())
	if err != nil {
		t.Fatal(err)
	}
	templateSink := &captureSink{}
	if err = executor.Execute(context.Background(), providerKey(t, awsbrowser.OperationDescribeLaunchTemplates, nil), templateSink); err != nil {
		t.Fatal(err)
	}
	if templateInput == nil || templateInput.MaxResults == nil || len(templateSink.pages) != 1 {
		t.Fatalf("input=%+v pages=%d", templateInput, len(templateSink.pages))
	}
	template := templateSink.pages[0].Resources()[0]
	if template.Key.Type != "ec2.launch-template" || template.Key.ID != "lt-123" || template.Observation.Fields()["latest_version_number"] != int64(3) {
		t.Fatalf("template=%+v fields=%+v", template.Key, template.Observation.Fields())
	}

	const script = "#!/bin/bash\necho ready\n"
	secretUserData := base64.StdEncoding.EncodeToString([]byte(script))
	var versionInput *ec2.DescribeLaunchTemplateVersionsInput
	client = &fakeEC2{versions: func(_ context.Context, input *ec2.DescribeLaunchTemplateVersionsInput) (*ec2.DescribeLaunchTemplateVersionsOutput, error) {
		versionInput = input
		return &ec2.DescribeLaunchTemplateVersionsOutput{LaunchTemplateVersions: []types.LaunchTemplateVersion{{
			LaunchTemplateId: aws.String("lt-123"), LaunchTemplateName: aws.String("web"), VersionNumber: aws.Int64(3),
			DefaultVersion: aws.Bool(true), VersionDescription: aws.String("production"), CreateTime: &created,
			LaunchTemplateData: &types.ResponseLaunchTemplateData{
				ImageId: aws.String("ami-123"), InstanceType: types.InstanceTypeT3Micro, UserData: aws.String(secretUserData),
				IamInstanceProfile: &types.LaunchTemplateIamInstanceProfileSpecification{Name: aws.String("web-profile")},
				SecurityGroupIds:   []string{"sg-1"},
				NetworkInterfaces:  []types.LaunchTemplateInstanceNetworkInterfaceSpecification{{SubnetId: aws.String("subnet-1"), Groups: []string{"sg-2"}}},
			},
		}}}, nil
	}}
	executor, _ = NewEC2QueryExecutor(client, fixedClock())
	versionSink := &captureSink{}
	if err = executor.Execute(context.Background(), providerKey(t, awsbrowser.OperationDescribeLaunchTemplateVersions, map[string]string{"launch-template-id": "lt-123", "version": "3"}), versionSink); err != nil {
		t.Fatal(err)
	}
	if versionInput == nil || aws.ToString(versionInput.LaunchTemplateId) != "lt-123" || !reflect.DeepEqual(versionInput.Versions, []string{"3"}) || versionInput.MaxResults != nil {
		t.Fatalf("version input=%+v", versionInput)
	}
	versionResource := versionSink.pages[0].Resources()[0]
	fields := versionResource.Observation.Fields()
	if versionResource.Key.Type != "ec2.launch-template-version" || versionResource.Key.ID != "lt-123/3" || fields["user_data_present"] != true {
		t.Fatalf("version=%+v fields=%+v", versionResource.Key, fields)
	}
	if strings.Contains(fmt.Sprintf("%v", fields), secretUserData) {
		t.Fatalf("mapped fields leaked launch template user data: %v", fields)
	}
	projection := awsbrowser.ProjectResourceFields(versionResource.Key, fields)
	for _, target := range []string{"ec2.launch-template:lt-123", "ec2.launch-template-user-data:lt-123/3", "ec2.image:ami-123", "ec2.security-group:sg-1", "ec2.security-group:sg-2", "ec2.subnet:subnet-1", "iam.instance-profile:web-profile"} {
		found := false
		for _, relation := range projection.Relations {
			found = found || relation.Target == target
		}
		if !found {
			t.Fatalf("missing relation %q in %+v", target, projection.Relations)
		}
	}
	if strings.Contains(fmt.Sprintf("%v", fields), script) {
		t.Fatalf("normal version fields leaked decoded launch template user data: %v", fields)
	}

	userDataSink := &captureSink{}
	if err = executor.Execute(context.Background(), providerKey(t, awsbrowser.OperationDescribeLaunchTemplateVersions, map[string]string{
		"launch-template-id": "lt-123", "version": "3", "view": "user-data",
	}), userDataSink); err != nil {
		t.Fatal(err)
	}
	userData := userDataSink.pages[0].Resources()[0]
	userFields := userData.Observation.Fields()
	if userData.Key.Type != "ec2.launch-template-user-data" || userFields["script"] != script || userFields["decoded_bytes"] != len(script) {
		t.Fatalf("user data=%+v fields=%+v", userData.Key, userFields)
	}
	projected := awsbrowser.ProjectResourceFields(userData.Key, userFields)
	if projected.Title != "User Data · web · v3" || len(projected.Fields) == 0 {
		t.Fatalf("user data projection=%+v", projected)
	}
}

func TestEC2AMIExactReadMapsSafeFields(t *testing.T) {
	var input *ec2.DescribeImagesInput
	client := &fakeEC2{images: func(_ context.Context, value *ec2.DescribeImagesInput) (*ec2.DescribeImagesOutput, error) {
		input = value
		return &ec2.DescribeImagesOutput{Images: []types.Image{{
			ImageId: aws.String("ami-123"), Name: aws.String("al2023-app"), Description: aws.String("application base image"),
			OwnerId: aws.String("123456789012"), CreationDate: aws.String("2026-08-28T01:02:03.000Z"),
			Architecture: types.ArchitectureValues("x86_64"), State: types.ImageState("available"),
			ImageType: types.ImageTypeValues("machine"), RootDeviceName: aws.String("/dev/xvda"),
			RootDeviceType: types.DeviceType("ebs"), VirtualizationType: types.VirtualizationType("hvm"),
			PlatformDetails: aws.String("Linux/UNIX"), EnaSupport: aws.Bool(true), Public: aws.Bool(false),
			Tags: []types.Tag{{Key: aws.String("Name"), Value: aws.String("app-ami")}},
		}}}, nil
	}}
	executor, err := NewEC2QueryExecutor(client, fixedClock())
	if err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	if err = executor.Execute(context.Background(), providerKey(t, awsbrowser.OperationDescribeImages, map[string]string{"image-id": "ami-123"}), sink); err != nil {
		t.Fatal(err)
	}
	if input == nil || !reflect.DeepEqual(input.ImageIds, []string{"ami-123"}) || input.MaxResults != nil {
		t.Fatalf("input=%+v", input)
	}
	resource := sink.pages[0].Resources()[0]
	fields := resource.Observation.Fields()
	if resource.Key.Type != "ec2.image" || resource.Key.ID != "ami-123" || fields["name"] != "al2023-app" || fields["state"] != "available" || fields["owner_id"] != "123456789012" {
		t.Fatalf("resource=%+v fields=%+v", resource.Key, fields)
	}
	assertMappedOnly(t, fields)
}

func TestEC2AMIExactReadTreatsMissingImageAsEmpty(t *testing.T) {
	client := &fakeEC2{images: func(context.Context, *ec2.DescribeImagesInput) (*ec2.DescribeImagesOutput, error) {
		return &ec2.DescribeImagesOutput{}, nil
	}}
	executor, _ := NewEC2QueryExecutor(client, fixedClock())
	sink := &captureSink{}
	if err := executor.Execute(context.Background(), providerKey(t, awsbrowser.OperationDescribeImages, map[string]string{"image-id": "ami-0123456789abcdef0"}), sink); err != nil {
		t.Fatal(err)
	}
	if sink.completed != 1 || len(sink.pages) != 0 {
		t.Fatalf("sink=%+v", sink)
	}
}

func TestEC2LaunchTemplateVersionSelectorRejectsNonVersionValues(t *testing.T) {
	for _, value := range []string{"latest", "0", "-1", "$default"} {
		key := providerKey(t, awsbrowser.OperationDescribeLaunchTemplateVersions, map[string]string{"launch-template-id": "lt-123", "version": value})
		if _, err := decodeEC2Params(key); !errors.Is(err, errInvalidEC2Query) {
			t.Fatalf("version %q accepted: %v", value, err)
		}
	}
}

func TestEC2LaunchTemplateUserDataRejectsInvalidBase64WithoutCommitting(t *testing.T) {
	client := &fakeEC2{versions: func(context.Context, *ec2.DescribeLaunchTemplateVersionsInput) (*ec2.DescribeLaunchTemplateVersionsOutput, error) {
		return &ec2.DescribeLaunchTemplateVersionsOutput{LaunchTemplateVersions: []types.LaunchTemplateVersion{{
			LaunchTemplateId: aws.String("lt-123"), LaunchTemplateName: aws.String("web"), VersionNumber: aws.Int64(3),
			LaunchTemplateData: &types.ResponseLaunchTemplateData{UserData: aws.String("not base64")},
		}}}, nil
	}}
	executor, _ := NewEC2QueryExecutor(client, fixedClock())
	sink := &captureSink{}
	err := executor.Execute(context.Background(), providerKey(t, awsbrowser.OperationDescribeLaunchTemplateVersions, map[string]string{
		"launch-template-id": "lt-123", "version": "3", "view": "user-data",
	}), sink)
	if err == nil || len(sink.pages) != 0 || sink.completed != 0 {
		t.Fatalf("error=%v pages=%d completed=%d", err, len(sink.pages), sink.completed)
	}
}

func TestEC2ExecutorSelectivelyCallsAllFrozenOperations(t *testing.T) {
	tests := []struct {
		operation string
		client    *fakeEC2
		wantType  string
	}{
		{awsbrowser.OperationDescribeInstances, &fakeEC2{instances: func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
			return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{Instances: []types.Instance{{InstanceId: aws.String("i-op")}}}}}, nil
		}}, "ec2.instance"},
		{awsbrowser.OperationDescribeVolumes, &fakeEC2{volumes: func(context.Context, *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error) {
			return &ec2.DescribeVolumesOutput{Volumes: []types.Volume{{VolumeId: aws.String("vol-op")}}}, nil
		}}, "ec2.volume"},
		{awsbrowser.OperationDescribeSecurityGroups, &fakeEC2{groups: func(context.Context, *ec2.DescribeSecurityGroupsInput) (*ec2.DescribeSecurityGroupsOutput, error) {
			return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []types.SecurityGroup{{GroupId: aws.String("sg-op")}}}, nil
		}}, "ec2.security-group"},
		{awsbrowser.OperationDescribeSecurityGroupRules, &fakeEC2{rules: func(context.Context, *ec2.DescribeSecurityGroupRulesInput) (*ec2.DescribeSecurityGroupRulesOutput, error) {
			return &ec2.DescribeSecurityGroupRulesOutput{SecurityGroupRules: []types.SecurityGroupRule{{SecurityGroupRuleId: aws.String("sgr-op")}}}, nil
		}}, "ec2.security-group-rule"},
		{awsbrowser.OperationDescribeVpcs, &fakeEC2{vpcs: func(context.Context, *ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error) {
			return &ec2.DescribeVpcsOutput{Vpcs: []types.Vpc{{VpcId: aws.String("vpc-op")}}}, nil
		}}, "ec2.vpc"},
		{awsbrowser.OperationDescribeSubnets, &fakeEC2{subnets: func(context.Context, *ec2.DescribeSubnetsInput) (*ec2.DescribeSubnetsOutput, error) {
			return &ec2.DescribeSubnetsOutput{Subnets: []types.Subnet{{SubnetId: aws.String("subnet-op")}}}, nil
		}}, "ec2.subnet"},
		{awsbrowser.OperationDescribeRouteTables, &fakeEC2{routes: func(context.Context, *ec2.DescribeRouteTablesInput) (*ec2.DescribeRouteTablesOutput, error) {
			return &ec2.DescribeRouteTablesOutput{RouteTables: []types.RouteTable{{RouteTableId: aws.String("rtb-op")}}}, nil
		}}, "ec2.route-table"},
		{awsbrowser.OperationDescribeVpcPeeringConnections, &fakeEC2{peerings: func(context.Context, *ec2.DescribeVpcPeeringConnectionsInput) (*ec2.DescribeVpcPeeringConnectionsOutput, error) {
			return &ec2.DescribeVpcPeeringConnectionsOutput{VpcPeeringConnections: []types.VpcPeeringConnection{{
				VpcPeeringConnectionId: aws.String("pcx-op"),
				RequesterVpcInfo:       &types.VpcPeeringConnectionVpcInfo{OwnerId: aws.String("123456789012"), Region: aws.String("us-east-1"), VpcId: aws.String("vpc-requester")},
				AccepterVpcInfo:        &types.VpcPeeringConnectionVpcInfo{OwnerId: aws.String("210987654321"), Region: aws.String("ap-northeast-2"), VpcId: aws.String("vpc-accepter")},
			}}}, nil
		}}, "ec2.vpc-peering-connection"},
	}
	for _, test := range tests {
		t.Run(test.operation, func(t *testing.T) {
			executor, _ := NewEC2QueryExecutor(test.client, fixedClock())
			sink := &captureSink{}
			if err := executor.Execute(context.Background(), providerKey(t, test.operation, nil), sink); err != nil {
				t.Fatal(err)
			}
			if sink.completed != 1 || len(sink.pages) != 1 || sink.pages[0].Number != 0 || len(sink.pages[0].Resources()) != 1 || sink.pages[0].Resources()[0].Key.Type != test.wantType {
				t.Fatalf("sink=%+v", sink)
			}
		})
	}
}

func TestEC2ExecutorMapsCrossAccountVpcPeeringAndUnresolvedEndpoint(t *testing.T) {
	client := &fakeEC2{peerings: func(_ context.Context, input *ec2.DescribeVpcPeeringConnectionsInput) (*ec2.DescribeVpcPeeringConnectionsOutput, error) {
		if input.MaxResults == nil || input.NextToken != nil || len(input.VpcPeeringConnectionIds) != 0 {
			t.Fatalf("input=%+v", input)
		}
		return &ec2.DescribeVpcPeeringConnectionsOutput{VpcPeeringConnections: []types.VpcPeeringConnection{
			{
				VpcPeeringConnectionId: aws.String("pcx-exact"),
				RequesterVpcInfo:       &types.VpcPeeringConnectionVpcInfo{OwnerId: aws.String("123456789012"), Region: aws.String("us-east-1"), VpcId: aws.String("vpc-a"), CidrBlock: aws.String("10.0.0.0/16")},
				AccepterVpcInfo:        &types.VpcPeeringConnectionVpcInfo{OwnerId: aws.String("210987654321"), Region: aws.String("ap-northeast-2"), VpcId: aws.String("vpc-b"), CidrBlock: aws.String("10.1.0.0/16")},
				Status:                 &types.VpcPeeringConnectionStateReason{Code: types.VpcPeeringConnectionStateReasonCodeActive},
				Tags:                   []types.Tag{{Key: aws.String("Name"), Value: aws.String("inter-region")}},
			},
			{
				VpcPeeringConnectionId: aws.String("pcx-unresolved"),
				RequesterVpcInfo:       &types.VpcPeeringConnectionVpcInfo{OwnerId: aws.String("123456789012"), Region: aws.String("us-east-1"), VpcId: aws.String("vpc-c")},
				AccepterVpcInfo:        &types.VpcPeeringConnectionVpcInfo{OwnerId: aws.String("210987654321"), VpcId: aws.String("vpc-d")},
			},
		}}, nil
	}}
	executor, _ := NewEC2QueryExecutor(client, fixedClock())
	sink := &captureSink{}
	if err := executor.Execute(context.Background(), providerKey(t, awsbrowser.OperationDescribeVpcPeeringConnections, nil), sink); err != nil {
		t.Fatal(err)
	}
	resources := sink.pages[0].Resources()
	if len(resources) != 2 || resources[0].Key.AccountID != "123456789012" || resources[0].Key.Region != "us-east-1" {
		t.Fatalf("resources=%+v", resources)
	}
	fields := resources[0].Observation.Fields()
	if fields["name"] != "inter-region" || fields["status"] != "active" || fields["requester_resolution"] != "exact" || fields["accepter_resolution"] != "exact" {
		t.Fatalf("fields=%+v", fields)
	}
	wantTargets := map[string]string{"ec2.vpc/vpc-a": "us-east-1", "ec2.vpc/vpc-b": "us-east-1"}
	if got := exactRelationTargets(t, resources[0]); !reflect.DeepEqual(got, wantTargets) {
		t.Fatalf("targets=%+v want=%+v", got, wantTargets)
	}
	unresolved := resources[1].Observation.Fields()
	if unresolved["accepter_resolution"] != "unresolved:missing-region" || len(unresolved["relations"].([]any)) != 1 {
		t.Fatalf("unresolved=%+v", unresolved)
	}
	assertMappedOnly(t, fields)
	assertMappedOnly(t, unresolved)
}

func TestEC2SecurityGroupRulesDirectionIsFilteredLocally(t *testing.T) {
	for _, test := range []struct {
		name, direction string
		wantEgress      bool
		wantID          string
	}{
		{name: "inbound", direction: "ingress", wantID: "sgr-in"},
		{name: "outbound", direction: "egress", wantEgress: true, wantID: "sgr-out"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeEC2{rules: func(_ context.Context, input *ec2.DescribeSecurityGroupRulesInput) (*ec2.DescribeSecurityGroupRulesOutput, error) {
				if len(input.Filters) != 1 || aws.ToString(input.Filters[0].Name) != "group-id" || !reflect.DeepEqual(input.Filters[0].Values, []string{"sg-1"}) {
					t.Fatalf("AWS input leaked synthetic direction or lost group scope: %+v", input.Filters)
				}
				return &ec2.DescribeSecurityGroupRulesOutput{SecurityGroupRules: []types.SecurityGroupRule{
					{SecurityGroupRuleId: aws.String("sgr-in"), GroupId: aws.String("sg-1"), IsEgress: aws.Bool(false), IpProtocol: aws.String("tcp"), FromPort: aws.Int32(443), ToPort: aws.Int32(443)},
					{SecurityGroupRuleId: aws.String("sgr-out"), GroupId: aws.String("sg-1"), IsEgress: aws.Bool(true), IpProtocol: aws.String("-1")},
				}}, nil
			}}
			executor, _ := NewEC2QueryExecutor(client, fixedClock())
			sink := &captureSink{}
			if err := executor.Execute(context.Background(), providerKey(t, awsbrowser.OperationDescribeSecurityGroupRules, map[string]string{
				"group-id": "sg-1", "direction": test.direction,
			}), sink); err != nil {
				t.Fatal(err)
			}
			if len(sink.pages) != 1 || len(sink.pages[0].Resources()) != 1 {
				t.Fatalf("direction result pages=%+v", sink.pages)
			}
			resource := sink.pages[0].Resources()[0]
			if resource.Key.ID != test.wantID || resource.Observation.Fields()["is_egress"] != test.wantEgress {
				t.Fatalf("direction result=%+v fields=%+v", resource.Key, resource.Observation.Fields())
			}
		})
	}
}

func TestEC2ExecutorPaginatesSequentiallyAndCoordinatorRetainsSecondPage(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	client := &fakeEC2{instances: func(_ context.Context, in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			if in.NextToken != nil {
				return nil, errors.New("first cursor set")
			}
			return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{Instances: []types.Instance{{InstanceId: aws.String("i-1")}}}}, NextToken: aws.String("next")}, nil
		}
		if aws.ToString(in.NextToken) != "next" {
			return nil, errors.New("cursor missing")
		}
		return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{Instances: []types.Instance{{InstanceId: aws.String("i-2")}}}}}, nil
	}}
	executor, _ := NewEC2QueryExecutor(client, fixedClock())
	key := providerKey(t, awsbrowser.OperationDescribeInstances, nil)
	store := awsbrowser.NewSessionStore()
	coordinator, e := awsbrowser.NewQueryCoordinator(store, executor, 1)
	if e != nil {
		t.Fatal(e)
	}
	sub, e := coordinator.Subscribe(key)
	if e != nil {
		t.Fatal(e)
	}
	var final awsbrowser.QueryUpdate
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case update, ok := <-sub.Updates():
			if !ok {
				if final.Snapshot.State != awsbrowser.LoadReady || final.Snapshot.ResourceCount() != 2 || len(final.Snapshot.Pages()) != 2 {
					t.Fatalf("final=%+v pages=%d", final, len(final.Snapshot.Pages()))
				}
				return
			}
			final = update
		case <-timer.C:
			t.Fatal("coordinator timeout")
		}
	}
}

func TestEC2ExecutorRejectsCursorAndTargetFailuresWithoutCompleting(t *testing.T) {
	tests := []struct {
		name      string
		output    *ec2.DescribeVolumesOutput
		params    map[string]string
		wantPages int
	}{
		{"empty cursor", &ec2.DescribeVolumesOutput{NextToken: aws.String(" ")}, nil, 0},
		{"repeated cursor", &ec2.DescribeVolumesOutput{Volumes: []types.Volume{{VolumeId: aws.String("vol-invalid-current-page")}}, NextToken: aws.String("same")}, nil, 1},
		{"wrong target", &ec2.DescribeVolumesOutput{Volumes: []types.Volume{{VolumeId: aws.String("vol-other")}}}, map[string]string{"volume-id": "vol-want"}, 0},
		{"missing target", &ec2.DescribeVolumesOutput{}, map[string]string{"volume-id": "vol-want"}, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := &fakeEC2{volumes: func(_ context.Context, in *ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error) {
				calls++
				if test.name == "repeated cursor" && calls == 1 {
					return &ec2.DescribeVolumesOutput{Volumes: []types.Volume{{VolumeId: aws.String("vol-valid-first-page")}}, NextToken: aws.String("same")}, nil
				}
				return test.output, nil
			}}
			executor, _ := NewEC2QueryExecutor(client, fixedClock())
			sink := &captureSink{}
			e := executor.Execute(context.Background(), providerKey(t, awsbrowser.OperationDescribeVolumes, test.params), sink)
			var provider *awsbrowser.ProviderError
			if !errors.As(e, &provider) || provider.Kind != awsbrowser.ProviderDecode || sink.completed != 0 || len(sink.pages) != test.wantPages {
				t.Fatalf("err=%v sink=%+v", e, sink)
			}
			if test.name == "repeated cursor" {
				resources := sink.pages[0].Resources()
				if len(resources) != 1 || resources[0].Key.ID != "vol-valid-first-page" {
					t.Fatalf("invalid repeated-cursor page escaped: %+v", resources)
				}
			}
		})
	}
}

func TestEC2ExecutorCancellationAndExternalSecurityGroupTargets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &fakeEC2{rules: func(_ context.Context, _ *ec2.DescribeSecurityGroupRulesInput) (*ec2.DescribeSecurityGroupRulesOutput, error) {
		cancel()
		return &ec2.DescribeSecurityGroupRulesOutput{SecurityGroupRules: []types.SecurityGroupRule{{SecurityGroupRuleId: aws.String("sgr-1"), GroupId: aws.String("sg-1"), CidrIpv4: aws.String("0.0.0.0/0"), ReferencedGroupInfo: &types.ReferencedSecurityGroup{GroupId: aws.String("sg-external"), UserId: aws.String("999999999999"), VpcId: aws.String("vpc-external")}}}}, nil
	}}
	executor, _ := NewEC2QueryExecutor(client, fixedClock())
	sink := &captureSink{}
	e := executor.Execute(ctx, providerKey(t, awsbrowser.OperationDescribeSecurityGroupRules, nil), sink)
	if !errors.Is(e, context.Canceled) || len(sink.pages) != 0 || sink.completed != 0 {
		t.Fatalf("err=%v sink=%+v", e, sink)
	}

	client.rules = func(context.Context, *ec2.DescribeSecurityGroupRulesInput) (*ec2.DescribeSecurityGroupRulesOutput, error) {
		return &ec2.DescribeSecurityGroupRulesOutput{SecurityGroupRules: []types.SecurityGroupRule{{
			SecurityGroupRuleId: aws.String("sgr-1"), GroupId: aws.String("sg-1"), IpProtocol: aws.String("tcp"),
			FromPort: aws.Int32(443), ToPort: aws.Int32(443), Description: aws.String("partner api"),
			ReferencedGroupInfo: &types.ReferencedSecurityGroup{GroupId: aws.String("sg-external"), UserId: aws.String("999999999999"), VpcId: aws.String("vpc-external")},
		}}}, nil
	}
	sink = &captureSink{}
	if e = executor.Execute(context.Background(), providerKey(t, awsbrowser.OperationDescribeSecurityGroupRules, nil), sink); e != nil {
		t.Fatal(e)
	}
	fields := sink.pages[0].Resources()[0].Observation.Fields()
	if len(fields["relations"].([]any)) != 2 || fields["usage_scope"] != "EC2 only" {
		t.Fatalf("fields=%+v", fields)
	}
	relations, err := awsbrowser.RelationsFromMappedFields(fields)
	if err != nil || len(relations) != 2 {
		t.Fatalf("relations=%#v error=%v", relations, err)
	}
	reference := relations[1]
	if reference.Source.Type != "ec2.security-group" || reference.Source.ID != "sg-1" ||
		reference.Target.AccountID != "999999999999" || reference.Target.ID != "sg-external" ||
		reference.Semantics.Type != awsbrowser.RelationReferences ||
		reference.Semantics.Condition != "description=partner+api&direction=ingress&from-port=443&protocol=tcp&rule-id=sgr-1&source-account=999999999999&source-group=sg-external&to-port=443" {
		t.Fatalf("reference=%#v", reference)
	}
}

func TestEC2InstanceSecurityGroupRelationPreservesNetworkInterfaceID(t *testing.T) {
	client := &fakeEC2{instances: func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{Instances: []types.Instance{{
			InstanceId: aws.String("i-eni"),
			NetworkInterfaces: []types.InstanceNetworkInterface{{
				NetworkInterfaceId: aws.String("eni-123"),
				Groups:             []types.GroupIdentifier{{GroupId: aws.String("sg-eni")}},
			}},
		}}}}}, nil
	}}
	executor, _ := NewEC2QueryExecutor(client, fixedClock())
	sink := &captureSink{}
	if err := executor.Execute(context.Background(), providerKey(t, awsbrowser.OperationDescribeInstances, nil), sink); err != nil {
		t.Fatal(err)
	}
	relations, err := awsbrowser.RelationsFromMappedFields(sink.pages[0].Resources()[0].Observation.Fields())
	if err != nil || len(relations) != 1 || relations[0].Target.ID != "sg-eni" || relations[0].Semantics.Condition != "network-interface=eni-123" {
		t.Fatalf("relations=%#v error=%v", relations, err)
	}
}

func TestEC2ExecutorRejectsUnsafeInstanceProfileARNBeforeEmitting(t *testing.T) {
	client := &fakeEC2{instances: func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{Instances: []types.Instance{{
			InstanceId: aws.String("i-1"),
			IamInstanceProfile: &types.IamInstanceProfile{
				Arn: aws.String("arn:aws:iam::999999999999:instance-profile/external-profile"),
			},
		}}}}}, nil
	}}
	executor, _ := NewEC2QueryExecutor(client, fixedClock())
	sink := &captureSink{}
	err := executor.Execute(context.Background(), providerKey(t, awsbrowser.OperationDescribeInstances, nil), sink)
	var provider *awsbrowser.ProviderError
	if !errors.As(err, &provider) || provider.Kind != awsbrowser.ProviderDecode || len(sink.pages) != 0 || sink.completed != 0 {
		t.Fatalf("err=%v sink=%+v", err, sink)
	}
}

func TestEC2ExecutorMapsExpandedRouteTargetsAndExcludesLocalSentinel(t *testing.T) {
	coreARN := "arn:aws:networkmanager::123456789012:core-network/core-network-1"
	client := &fakeEC2{routes: func(context.Context, *ec2.DescribeRouteTablesInput) (*ec2.DescribeRouteTablesOutput, error) {
		return &ec2.DescribeRouteTablesOutput{RouteTables: []types.RouteTable{{
			RouteTableId: aws.String("rtb-1"), VpcId: aws.String("vpc-1"),
			Routes: []types.Route{
				{DestinationCidrBlock: aws.String("10.0.0.0/16"), GatewayId: aws.String("local")},
				{DestinationIpv6CidrBlock: aws.String("::/0"), EgressOnlyInternetGatewayId: aws.String("eigw-1")},
				{DestinationCidrBlock: aws.String("0.0.0.0/0"), CarrierGatewayId: aws.String("cagw-1")},
				{DestinationCidrBlock: aws.String("192.0.2.0/24"), LocalGatewayId: aws.String("lgw-1")},
				{DestinationCidrBlock: aws.String("198.51.100.0/24"), CoreNetworkArn: aws.String(coreARN)},
			},
		}}}, nil
	}}
	executor, _ := NewEC2QueryExecutor(client, fixedClock())
	sink := &captureSink{}
	if err := executor.Execute(context.Background(), providerKey(t, awsbrowser.OperationDescribeRouteTables, nil), sink); err != nil {
		t.Fatal(err)
	}
	resource := sink.pages[0].Resources()[0]
	wantTargets := map[string]string{
		"ec2.vpc/vpc-1": "us-east-1",
		"ec2.egress-only-internet-gateway/eigw-1": "us-east-1",
		"ec2.carrier-gateway/cagw-1":              "us-east-1",
		"ec2.local-gateway/lgw-1":                 "us-east-1",
		"networkmanager.core-network/" + coreARN:  awsbrowser.GlobalRegion,
	}
	if gotTargets := exactRelationTargets(t, resource); !reflect.DeepEqual(gotTargets, wantTargets) {
		t.Fatalf("relation targets=%+v want=%+v", gotTargets, wantTargets)
	}
	routes := resource.Observation.Fields()["routes"].([]any)
	if routes[0].(map[string]any)["gateway_id"] != "local" || routes[1].(map[string]any)["egress_only_internet_gateway_id"] != "eigw-1" ||
		routes[2].(map[string]any)["carrier_gateway_id"] != "cagw-1" || routes[3].(map[string]any)["local_gateway_id"] != "lgw-1" ||
		routes[4].(map[string]any)["core_network_arn"] != coreARN {
		t.Fatalf("routes=%+v", routes)
	}
}

func TestEC2ExecutorRejectsUnknownParamsBeforeCallingSDK(t *testing.T) {
	called := false
	client := &fakeEC2{vpcs: func(context.Context, *ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error) {
		called = true
		return nil, nil
	}}
	executor, _ := NewEC2QueryExecutor(client, fixedClock())
	sink := &captureSink{}
	e := executor.Execute(context.Background(), providerKey(t, awsbrowser.OperationDescribeVpcs, map[string]string{"dry-run": "true"}), sink)
	var provider *awsbrowser.ProviderError
	if !errors.As(e, &provider) || provider.Kind != awsbrowser.ProviderUnsupported || called || sink.completed != 0 {
		t.Fatalf("err=%v called=%v sink=%+v", e, called, sink)
	}
}

func exactRelationTargets(t *testing.T, resource awsbrowser.ObservedResource) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, raw := range resource.Observation.Fields()["relations"].([]any) {
		relation := raw.(map[string]any)
		if relation["kind"] != string(awsbrowser.RelationIDExact) {
			t.Fatalf("relation=%+v", relation)
		}
		if source := relation["source"].(awsbrowser.ResourceKey); source != resource.Key {
			t.Fatalf("relation source=%+v key=%+v", source, resource.Key)
		}
		target := relation["target"].(awsbrowser.ResourceKey)
		result[target.Type+"/"+target.ID] = relation["scope"].(string)
	}
	return result
}

func relationSemantics(t *testing.T, resource awsbrowser.ObservedResource) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, raw := range resource.Observation.Fields()["relations"].([]any) {
		relation := raw.(map[string]any)
		if relation["direction"] != string(awsbrowser.RelationOutgoing) {
			t.Fatalf("relation direction=%+v", relation)
		}
		target := relation["target"].(awsbrowser.ResourceKey)
		result[target.Type+"/"+target.ID] = relation["relation_type"].(string) + "|" + relation["condition"].(string)
	}
	return result
}

func assertMappedOnly(t *testing.T, value any) {
	t.Helper()
	var walk func(any)
	walk = func(v any) {
		switch item := v.(type) {
		case nil, bool, string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, time.Time, awsbrowser.ResourceKey:
		case []string:
		case []awsbrowser.ResourceKey:
		case map[string]string:
		case []any:
			for _, child := range item {
				walk(child)
			}
		case map[string]any:
			for _, child := range item {
				walk(child)
			}
		default:
			t.Fatalf("raw or unsupported mapped value %T (%v), kind=%s", v, v, reflect.TypeOf(v).Kind())
		}
	}
	walk(value)
}

var _ awsbrowser.EC2API = (*fakeEC2)(nil)
