package bb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAWSBrowseHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_BB_AWS_BROWSE_HELPER") != "1" {
		return
	}
	separator := 0
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i + 1
			break
		}
	}
	name, args := os.Args[separator], os.Args[separator+1:]
	if logPath := os.Getenv("GO_WANT_BB_AWS_BROWSE_LOG"); logPath != "" {
		file, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(91)
		}
		_, _ = fmt.Fprintln(file, strings.Join(append([]string{name}, args...), " "))
		_ = file.Close()
	}
	if name != "aws" || len(args) < 2 {
		os.Exit(90)
	}
	service, operation := args[0], args[1]
	if os.Getenv("GO_WANT_BB_AWS_BROWSE_FAIL_IAM") == "1" && service == "iam" {
		fmt.Fprint(os.Stderr, "AccessDenied: iam inventory is not allowed")
		os.Exit(1)
	}
	responses := map[string]string{
		"sts get-caller-identity": `{"Account":"123456789012","Arn":"arn:aws:sts::123456789012:assumed-role/ReadOnly/test"}`,
		"ec2 describe-instances": `{"Reservations":[{"Instances":[{
			"InstanceId":"i-001","ImageId":"ami-001","InstanceType":"t3.small","LaunchTime":"2026-08-27T01:00:00Z",
			"PrivateIpAddress":"10.0.1.10","PublicIpAddress":"203.0.113.10","PrivateDnsName":"ip-10-0-1-10.internal",
			"PublicDnsName":"ec2-203-0-113-10.compute.amazonaws.com","VpcId":"vpc-001","SubnetId":"subnet-001",
			"State":{"Name":"running"},"Placement":{"AvailabilityZone":"ap-northeast-2a"},
			"SecurityGroups":[{"GroupId":"sg-001","GroupName":"web"}],
			"BlockDeviceMappings":[{"DeviceName":"/dev/xvda","Ebs":{"VolumeId":"vol-001"}}],
			"Tags":[{"Key":"Name","Value":"web-01"}]
		}]}]}`,
		"ec2 describe-volumes": `{"Volumes":[{
			"VolumeId":"vol-001","AvailabilityZone":"ap-northeast-2a","CreateTime":"2026-08-27T01:00:00Z",
			"Size":20,"VolumeType":"gp3","State":"in-use","Iops":3000,"Throughput":125,"Encrypted":true,
			"Attachments":[{"InstanceId":"i-001","Device":"/dev/xvda","State":"attached"}],
			"Tags":[{"Key":"Name","Value":"web-root"}]
		}]}`,
		"ec2 describe-security-groups": `{"SecurityGroups":[{
			"GroupId":"sg-001","GroupName":"web","Description":"web ingress","OwnerId":"123456789012","VpcId":"vpc-001",
			"IpPermissions":[{"IpProtocol":"tcp","FromPort":443,"ToPort":443,"IpRanges":[{"CidrIp":"0.0.0.0/0"}]}],
			"IpPermissionsEgress":[{"IpProtocol":"-1","IpRanges":[{"CidrIp":"0.0.0.0/0"}]}]
		}]}`,
		"ec2 describe-vpcs":         `{"Vpcs":[{"VpcId":"vpc-001","CidrBlock":"10.0.0.0/16","State":"available","IsDefault":false,"OwnerId":"123456789012","Tags":[{"Key":"Name","Value":"main"}]}]}`,
		"ec2 describe-subnets":      `{"Subnets":[{"SubnetId":"subnet-001","VpcId":"vpc-001","CidrBlock":"10.0.1.0/24","AvailabilityZone":"ap-northeast-2a","AvailableIpAddressCount":240,"MapPublicIpOnLaunch":false,"State":"available","Tags":[{"Key":"Name","Value":"private-a"}]}]}`,
		"ec2 describe-route-tables": `{"RouteTables":[{"RouteTableId":"rtb-001","VpcId":"vpc-001","Routes":[{"DestinationCidrBlock":"10.0.0.0/16","GatewayId":"local","State":"active"}],"Associations":[{"SubnetId":"subnet-001","Main":false}],"Tags":[{"Key":"Name","Value":"private"}]}]}`,
		"route53 list-hosted-zones": `{"HostedZones":[{"Id":"/hostedzone/Z001","Name":"example.com.","ResourceRecordSetCount":2,"Config":{"PrivateZone":false}}]}`,
		"route53 list-resource-record-sets": `{"ResourceRecordSets":[
			{"Name":"app.example.com.","Type":"A","TTL":60,"ResourceRecords":[{"Value":"203.0.113.10"}]},
			{"Name":"edge.example.com.","Type":"A","AliasTarget":{"DNSName":"dualstack.edge.elb.amazonaws.com.","HostedZoneId":"ZELB","EvaluateTargetHealth":true}}
		]}`,
		"iam list-users": `{"Users":[{"Path":"/","UserName":"operator","UserId":"AIDA001","Arn":"arn:aws:iam::123456789012:user/operator","CreateDate":"2026-01-01T00:00:00Z"}]}`,
		"iam list-roles": `{"Roles":[{"Path":"/","RoleName":"ReadOnly","RoleId":"AROA001","Arn":"arn:aws:iam::123456789012:role/ReadOnly","CreateDate":"2026-01-01T00:00:00Z","Description":"viewer","MaxSessionDuration":3600}]}`,
	}
	response, ok := responses[service+" "+operation]
	if !ok {
		os.Exit(90)
	}
	fmt.Fprint(os.Stdout, response)
	os.Exit(0)
}

func awsBrowseTestApp(t *testing.T) (*App, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	logPath := filepath.Join(t.TempDir(), "aws.log")
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"GO_WANT_BB_AWS_BROWSE_HELPER=1",
		"GO_WANT_BB_AWS_BROWSE_LOG=" + logPath,
	}
	a := New(stdout, stderr, env)
	a.in = strings.NewReader("")
	a.lookPath = func(name string) (string, error) {
		if name == "aws" {
			return "helper", nil
		}
		return exec.LookPath(name)
	}
	a.command = func(name string, args ...string) *exec.Cmd {
		return exec.Command(os.Args[0], append([]string{"-test.run=TestAWSBrowseHelperProcess", "--", name}, args...)...)
	}
	return a, stdout, stderr, logPath
}

func TestAWSBrowseBuildsLinkedReadOnlyGraph(t *testing.T) {
	a, stdout, _, _ := awsBrowseTestApp(t)
	if err := a.Run([]string{"aws", "browse", "--profile", "dev", "--region", "ap-northeast-2", "--json"}); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		SchemaVersion int            `json:"schema_version"`
		OK            bool           `json:"ok"`
		Data          awsBrowseGraph `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, stdout.String())
	}
	if decoded.SchemaVersion != SchemaVersion || !decoded.OK {
		t.Fatalf("envelope=%+v", decoded)
	}
	if decoded.Data.Context.Account != "123456789012" || decoded.Data.Context.Region != "ap-northeast-2" {
		t.Fatalf("context=%+v", decoded.Data.Context)
	}
	byKey := map[string]awsBrowseResource{}
	for _, resource := range decoded.Data.Resources {
		byKey[resource.Key] = resource
	}
	assertRelationTargets(t, byKey[awsResourceKey("instance", "i-001")], "EBS volumes", awsResourceKey("volume", "vol-001"))
	assertRelationTargets(t, byKey[awsResourceKey("instance", "i-001")], "Security groups", awsResourceKey("security-group", "sg-001"))
	assertRelationTargets(t, byKey[awsResourceKey("instance", "i-001")], "VPC", awsResourceKey("vpc", "vpc-001"))
	assertRelationTargets(t, byKey[awsResourceKey("volume", "vol-001")], "Attached instances", awsResourceKey("instance", "i-001"))
	assertRelationTargets(t, byKey[awsResourceKey("security-group", "sg-001")], "Attached EC2 instances", awsResourceKey("instance", "i-001"))

	var matched, alias bool
	for _, resource := range decoded.Data.Resources {
		if resource.Type != "dns-record" {
			continue
		}
		for _, relation := range resource.Relations {
			matched = matched || relation.Name == "Matched EC2 resources" && relation.Confidence == "heuristic"
			alias = alias || relation.Name == "Alias target" && relation.Confidence == "heuristic"
		}
	}
	if !matched || !alias {
		t.Fatalf("Route 53 links matched=%v alias=%v resources=%+v", matched, alias, decoded.Data.Resources)
	}
}

func assertRelationTargets(t *testing.T, resource awsBrowseResource, relationName, target string) {
	t.Helper()
	for _, relation := range resource.Relations {
		if relation.Name != relationName {
			continue
		}
		for _, got := range relation.Targets {
			if got == target {
				return
			}
		}
		t.Fatalf("%s relation %q targets=%v, want %s", resource.Key, relationName, relation.Targets, target)
	}
	t.Fatalf("%s missing relation %q", resource.Key, relationName)
}

func TestAWSBrowseUsesOnlyReadOperationsAndScopesRegionalCalls(t *testing.T) {
	a, _, _, logPath := awsBrowseTestApp(t)
	if err := a.Run([]string{"aws", "browse", "--profile", "dev", "--region", "ap-northeast-2", "--json"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 11 {
		t.Fatalf("calls=%d\n%s", len(lines), data)
	}
	for _, line := range lines {
		for _, forbidden := range []string{" create-", " delete-", " update-", " attach-", " detach-", " start-", " stop-", " terminate-"} {
			if strings.Contains(line, forbidden) {
				t.Fatalf("mutation call found: %s", line)
			}
		}
		if !strings.Contains(line, "--profile dev") || !strings.Contains(line, "--output json") || !strings.Contains(line, "--no-cli-pager") {
			t.Fatalf("missing stable AWS CLI options: %s", line)
		}
		regional := strings.Contains(line, "aws ec2 ") || strings.Contains(line, "aws sts ")
		if regional != strings.Contains(line, "--region ap-northeast-2") {
			t.Fatalf("regional flag mismatch: %s", line)
		}
	}
}

func TestAWSBrowseKeepsPartialResultsWhenIAMIsDenied(t *testing.T) {
	a, stdout, stderr, _ := awsBrowseTestApp(t)
	a.env = append(a.env, "GO_WANT_BB_AWS_BROWSE_FAIL_IAM=1")
	if err := a.Run([]string{"aws", "browse", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "AccessDenied") || !strings.Contains(stdout.String(), `"ok":true`) {
		t.Fatalf("partial envelope=%s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("JSON mode wrote stderr: %q", stderr.String())
	}
}

func TestAWSBrowseNonTTYPrintsSummaryWithoutPrompting(t *testing.T) {
	a, stdout, stderr, _ := awsBrowseTestApp(t)
	if err := a.Run([]string{"aws", "browse"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"EC2", "Route 53", "IAM", "VPC"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("summary missing %q:\n%s", want, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "[1-") || strings.Contains(stderr.String(), "[1-") {
		t.Fatalf("non-TTY browse prompted:\nstdout=%s\nstderr=%s", stdout.String(), stderr.String())
	}
}

func TestParseAWSBrowseOptionsRejectsUnknownOrMalformedValues(t *testing.T) {
	for _, args := range [][]string{{"extra"}, {"--profile"}, {"--region", "bad\nregion"}, {"--profile", "bad profile"}} {
		if _, err := parseAWSBrowseOptions(args); err == nil {
			t.Fatalf("args=%q should fail", args)
		}
	}
	opts, err := parseAWSBrowseOptions([]string{"--json", "--profile", "dev", "--region", "ap-northeast-2"})
	if err != nil || !opts.JSON || opts.Profile != "dev" || opts.Region != "ap-northeast-2" {
		t.Fatalf("opts=%+v err=%v", opts, err)
	}
}

func TestAWSBrowseSubcommandHelpDoesNotCollectInventory(t *testing.T) {
	a, stdout, _, logPath := awsBrowseTestApp(t)
	if err := a.Run([]string{"aws", "browse", "--help"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Reads EC2, EBS") {
		t.Fatalf("help=%q", stdout.String())
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("help invoked AWS: err=%v", err)
	}
}
