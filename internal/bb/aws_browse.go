package bb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// awsBrowseOptions deliberately mirrors AWS CLI global options instead of
// resolving or storing credentials inside bb.
type awsBrowseOptions struct {
	Profile string `json:"profile,omitempty"`
	Region  string `json:"region,omitempty"`
	JSON    bool   `json:"-"`
}

type awsBrowseContext struct {
	Account string `json:"account,omitempty"`
	ARN     string `json:"arn,omitempty"`
	Profile string `json:"profile,omitempty"`
	Region  string `json:"region,omitempty"`
}

type awsBrowseField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type awsBrowseRelation struct {
	Name       string   `json:"name"`
	Confidence string   `json:"confidence"`
	Targets    []string `json:"targets"`
}

type awsBrowseResource struct {
	Key       string              `json:"key"`
	Service   string              `json:"service"`
	Type      string              `json:"type"`
	ID        string              `json:"id"`
	Name      string              `json:"name,omitempty"`
	Scope     string              `json:"scope"`
	Summary   string              `json:"summary,omitempty"`
	Fields    []awsBrowseField    `json:"fields"`
	Relations []awsBrowseRelation `json:"relations"`
}

type awsBrowseCategory struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Resources []string `json:"resources"`
}

type awsBrowseGraph struct {
	Context    awsBrowseContext    `json:"context"`
	Categories []awsBrowseCategory `json:"categories"`
	Resources  []awsBrowseResource `json:"resources"`
	Warnings   []string            `json:"warnings"`
}

type awsBrowseSummaryRow struct {
	Service   string `json:"service"`
	Resources int    `json:"resources"`
}

type awsTag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

type awsEC2Inventory struct {
	Reservations []struct {
		Instances []struct {
			InstanceID       string `json:"InstanceId"`
			ImageID          string `json:"ImageId"`
			InstanceType     string `json:"InstanceType"`
			LaunchTime       string `json:"LaunchTime"`
			PrivateIPAddress string `json:"PrivateIpAddress"`
			PublicIPAddress  string `json:"PublicIpAddress"`
			PrivateDNSName   string `json:"PrivateDnsName"`
			PublicDNSName    string `json:"PublicDnsName"`
			VpcID            string `json:"VpcId"`
			SubnetID         string `json:"SubnetId"`
			State            struct {
				Name string `json:"Name"`
			} `json:"State"`
			Placement struct {
				AvailabilityZone string `json:"AvailabilityZone"`
			} `json:"Placement"`
			IAMInstanceProfile struct {
				ARN string `json:"Arn"`
			} `json:"IamInstanceProfile"`
			SecurityGroups []struct {
				GroupID   string `json:"GroupId"`
				GroupName string `json:"GroupName"`
			} `json:"SecurityGroups"`
			BlockDeviceMappings []struct {
				DeviceName string `json:"DeviceName"`
				EBS        struct {
					VolumeID string `json:"VolumeId"`
				} `json:"Ebs"`
			} `json:"BlockDeviceMappings"`
			Tags []awsTag `json:"Tags"`
		} `json:"Instances"`
	} `json:"Reservations"`
}

type awsVolumeInventory struct {
	Volumes []struct {
		VolumeID         string `json:"VolumeId"`
		AvailabilityZone string `json:"AvailabilityZone"`
		CreateTime       string `json:"CreateTime"`
		Size             int    `json:"Size"`
		VolumeType       string `json:"VolumeType"`
		State            string `json:"State"`
		IOPS             int    `json:"Iops"`
		Throughput       int    `json:"Throughput"`
		Encrypted        bool   `json:"Encrypted"`
		SnapshotID       string `json:"SnapshotId"`
		KMSKeyID         string `json:"KmsKeyId"`
		Attachments      []struct {
			InstanceID string `json:"InstanceId"`
			Device     string `json:"Device"`
			State      string `json:"State"`
		} `json:"Attachments"`
		Tags []awsTag `json:"Tags"`
	} `json:"Volumes"`
}

type awsSecurityGroupInventory struct {
	SecurityGroups []struct {
		GroupID             string            `json:"GroupId"`
		GroupName           string            `json:"GroupName"`
		Description         string            `json:"Description"`
		OwnerID             string            `json:"OwnerId"`
		VpcID               string            `json:"VpcId"`
		IPPermissions       []awsIPPermission `json:"IpPermissions"`
		IPPermissionsEgress []awsIPPermission `json:"IpPermissionsEgress"`
		Tags                []awsTag          `json:"Tags"`
	} `json:"SecurityGroups"`
}

type awsIPPermission struct {
	IPProtocol string `json:"IpProtocol"`
	FromPort   *int   `json:"FromPort"`
	ToPort     *int   `json:"ToPort"`
	IPRanges   []struct {
		CIDR string `json:"CidrIp"`
	} `json:"IpRanges"`
	IPv6Ranges []struct {
		CIDR string `json:"CidrIpv6"`
	} `json:"Ipv6Ranges"`
	UserIDGroupPairs []struct {
		GroupID string `json:"GroupId"`
		UserID  string `json:"UserId"`
	} `json:"UserIdGroupPairs"`
}

type awsVPCInventory struct {
	Vpcs []struct {
		VpcID     string   `json:"VpcId"`
		CIDRBlock string   `json:"CidrBlock"`
		State     string   `json:"State"`
		IsDefault bool     `json:"IsDefault"`
		OwnerID   string   `json:"OwnerId"`
		Tags      []awsTag `json:"Tags"`
	} `json:"Vpcs"`
}

type awsSubnetInventory struct {
	Subnets []struct {
		SubnetID                string   `json:"SubnetId"`
		VpcID                   string   `json:"VpcId"`
		CIDRBlock               string   `json:"CidrBlock"`
		AvailabilityZone        string   `json:"AvailabilityZone"`
		AvailableIPAddressCount int      `json:"AvailableIpAddressCount"`
		MapPublicIPOnLaunch     bool     `json:"MapPublicIpOnLaunch"`
		State                   string   `json:"State"`
		Tags                    []awsTag `json:"Tags"`
	} `json:"Subnets"`
}

type awsRouteTableInventory struct {
	RouteTables []struct {
		RouteTableID string `json:"RouteTableId"`
		VpcID        string `json:"VpcId"`
		Routes       []struct {
			DestinationCIDRBlock     string `json:"DestinationCidrBlock"`
			DestinationIPv6CIDRBlock string `json:"DestinationIpv6CidrBlock"`
			GatewayID                string `json:"GatewayId"`
			NatGatewayID             string `json:"NatGatewayId"`
			TransitGatewayID         string `json:"TransitGatewayId"`
			VpcPeeringConnectionID   string `json:"VpcPeeringConnectionId"`
			NetworkInterfaceID       string `json:"NetworkInterfaceId"`
			InstanceID               string `json:"InstanceId"`
			State                    string `json:"State"`
		} `json:"Routes"`
		Associations []struct {
			Main     bool   `json:"Main"`
			SubnetID string `json:"SubnetId"`
		} `json:"Associations"`
		Tags []awsTag `json:"Tags"`
	} `json:"RouteTables"`
}

type awsRoute53Zones struct {
	HostedZones []struct {
		ID                     string `json:"Id"`
		Name                   string `json:"Name"`
		ResourceRecordSetCount int    `json:"ResourceRecordSetCount"`
		Config                 struct {
			PrivateZone bool `json:"PrivateZone"`
		} `json:"Config"`
	} `json:"HostedZones"`
}

type awsRoute53Records struct {
	ResourceRecordSets []struct {
		Name            string `json:"Name"`
		Type            string `json:"Type"`
		TTL             *int64 `json:"TTL"`
		SetIdentifier   string `json:"SetIdentifier"`
		ResourceRecords []struct {
			Value string `json:"Value"`
		} `json:"ResourceRecords"`
		AliasTarget *struct {
			DNSName              string `json:"DNSName"`
			HostedZoneID         string `json:"HostedZoneId"`
			EvaluateTargetHealth bool   `json:"EvaluateTargetHealth"`
		} `json:"AliasTarget"`
	} `json:"ResourceRecordSets"`
}

type awsIAMUsers struct {
	Users []struct {
		Path             string `json:"Path"`
		UserName         string `json:"UserName"`
		UserID           string `json:"UserId"`
		ARN              string `json:"Arn"`
		CreateDate       string `json:"CreateDate"`
		PasswordLastUsed string `json:"PasswordLastUsed"`
	} `json:"Users"`
}

type awsIAMRoles struct {
	Roles []struct {
		Path               string `json:"Path"`
		RoleName           string `json:"RoleName"`
		RoleID             string `json:"RoleId"`
		ARN                string `json:"Arn"`
		CreateDate         string `json:"CreateDate"`
		Description        string `json:"Description"`
		MaxSessionDuration int    `json:"MaxSessionDuration"`
	} `json:"Roles"`
}

type awsCallerIdentity struct {
	Account string `json:"Account"`
	ARN     string `json:"Arn"`
}

type awsBrowseInventory struct {
	Identity    awsCallerIdentity
	EC2         awsEC2Inventory
	Volumes     awsVolumeInventory
	Groups      awsSecurityGroupInventory
	VPCs        awsVPCInventory
	Subnets     awsSubnetInventory
	RouteTables awsRouteTableInventory
	Zones       awsRoute53Zones
	Records     map[string]awsRoute53Records
	Users       awsIAMUsers
	Roles       awsIAMRoles
}

func parseAWSBrowseOptions(args []string) (awsBrowseOptions, error) {
	opts := awsBrowseOptions{}
	for len(args) > 0 {
		switch args[0] {
		case "--json":
			opts.JSON = true
			args = args[1:]
		case "--profile", "--region":
			if len(args) < 2 || !validExplicitName(args[1]) {
				return opts, usage("aws browse", "[--profile NAME] [--region REGION] [--json]")
			}
			if args[0] == "--profile" {
				if !awsProfileNameRE.MatchString(args[1]) {
					return opts, invalid("invalid AWS profile name")
				}
				opts.Profile = args[1]
			} else {
				opts.Region = args[1]
			}
			args = args[2:]
		default:
			return opts, usage("aws browse", "[--profile NAME] [--region REGION] [--json]")
		}
	}
	return opts, nil
}

func (a *App) awsBrowse(args []string) error {
	if helpRequested(args) {
		_, err := fmt.Fprint(a.out, `Usage:
  bb aws browse [--profile NAME] [--region REGION] [--json]

Reads EC2, EBS, security group, VPC, Route 53, and IAM inventory through the
AWS CLI. It never calls create, update, delete, attach, detach, start, or stop.
The selected region scopes EC2/VPC data; IAM and Route 53 remain global.
`)
		return err
	}
	opts, err := parseAWSBrowseOptions(args)
	if err != nil {
		return err
	}
	if _, err := a.lookPath("aws"); err != nil {
		return unavailable("aws is not installed; install AWS CLI v2 to use bb aws browse")
	}

	graph, err := a.collectAWSBrowseGraph(opts)
	if err != nil {
		return err
	}
	if opts.JSON {
		return printEnvelope(a.out, graph, graph.Warnings)
	}
	if !a.useBubbleSelector() {
		rows := make([]awsBrowseSummaryRow, 0, len(graph.Categories))
		for _, category := range graph.Categories {
			rows = append(rows, awsBrowseSummaryRow{Service: category.Name, Resources: len(category.Resources)})
		}
		for _, warning := range graph.Warnings {
			fmt.Fprintln(a.err, "bb aws browse:", safeTerminalText(warning))
		}
		return printHuman(a.out, rows)
	}
	return a.browseAWSGraph(graph)
}

func (a *App) collectAWSBrowseGraph(opts awsBrowseOptions) (awsBrowseGraph, error) {
	inv := awsBrowseInventory{Records: map[string]awsRoute53Records{}}
	var warnings []string
	succeeded := 0
	read := func(regional bool, service, operation string, output any, operationArgs ...string) bool {
		if err := a.readAWSBrowseJSON(opts, regional, service, operation, output, operationArgs...); err != nil {
			warnings = append(warnings, err.Error())
			return false
		}
		succeeded++
		return true
	}

	read(true, "sts", "get-caller-identity", &inv.Identity)
	read(true, "ec2", "describe-instances", &inv.EC2)
	read(true, "ec2", "describe-volumes", &inv.Volumes)
	read(true, "ec2", "describe-security-groups", &inv.Groups)
	read(true, "ec2", "describe-vpcs", &inv.VPCs)
	read(true, "ec2", "describe-subnets", &inv.Subnets)
	read(true, "ec2", "describe-route-tables", &inv.RouteTables)
	if read(false, "route53", "list-hosted-zones", &inv.Zones) {
		for _, zone := range inv.Zones.HostedZones {
			var records awsRoute53Records
			if read(false, "route53", "list-resource-record-sets", &records, "--hosted-zone-id", zone.ID) {
				inv.Records[zone.ID] = records
			}
		}
	}
	read(false, "iam", "list-users", &inv.Users)
	read(false, "iam", "list-roles", &inv.Roles)
	if succeeded == 0 {
		return awsBrowseGraph{}, unavailable("AWS inventory could not be read; run 'bb aws sso' or inspect the active profile and read-only permissions")
	}

	graph := buildAWSBrowseGraph(inv, opts)
	graph.Warnings = warnings
	return graph, nil
}

func (a *App) readAWSBrowseJSON(opts awsBrowseOptions, regional bool, service, operation string, output any, operationArgs ...string) error {
	argv := []string{service, operation}
	argv = append(argv, operationArgs...)
	argv = append(argv, "--output", "json", "--no-cli-pager")
	if opts.Profile != "" {
		argv = append(argv, "--profile", opts.Profile)
	}
	if regional && opts.Region != "" {
		argv = append(argv, "--region", opts.Region)
	}
	cmd := a.command("aws", argv...)
	var stdout, stderr bytes.Buffer
	cmd.Env, cmd.Stdout, cmd.Stderr = a.env, &stdout, &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(safeTerminalText(stderr.String()))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("%s %s: %s", service, operation, message)
	}
	if err := json.Unmarshal(stdout.Bytes(), output); err != nil {
		return fmt.Errorf("decode aws %s %s output: %w", service, operation, err)
	}
	return nil
}

func awsResourceKey(kind, id string) string { return kind + ":" + id }

func awsTagName(tags []awsTag, fallback string) string {
	for _, tag := range tags {
		if tag.Key == "Name" && strings.TrimSpace(tag.Value) != "" {
			return safeTerminalText(tag.Value)
		}
	}
	return safeTerminalText(fallback)
}

func awsTagsText(tags []awsTag) string {
	values := make([]string, 0, len(tags))
	for _, tag := range tags {
		values = append(values, safeTerminalText(tag.Key)+"="+safeTerminalText(tag.Value))
	}
	sort.Strings(values)
	return strings.Join(values, ", ")
}

func awsField(name, value string) awsBrowseField {
	if value == "" {
		value = "-"
	}
	return awsBrowseField{Name: safeTerminalText(name), Value: safeTerminalText(value)}
}

func awsBool(value bool) string { return strconv.FormatBool(value) }

func awsAddRelation(resources map[string]*awsBrowseResource, source, name, confidence string, targets ...string) {
	node := resources[source]
	if node == nil {
		return
	}
	seen := map[string]bool{}
	valid := make([]string, 0, len(targets))
	for _, target := range targets {
		if target == "" || resources[target] == nil || seen[target] {
			continue
		}
		seen[target] = true
		valid = append(valid, target)
	}
	if len(valid) == 0 {
		return
	}
	sort.Strings(valid)
	node.Relations = append(node.Relations, awsBrowseRelation{Name: name, Confidence: confidence, Targets: valid})
}

func buildAWSBrowseGraph(inv awsBrowseInventory, opts awsBrowseOptions) awsBrowseGraph {
	resources := map[string]*awsBrowseResource{}
	add := func(resource awsBrowseResource) {
		resource.Key = awsResourceKey(resource.Type, resource.ID)
		resource.Name = safeTerminalText(resource.Name)
		resource.Summary = safeTerminalText(resource.Summary)
		resources[resource.Key] = &resource
	}
	region := opts.Region
	if region == "" {
		region = "AWS CLI default"
	}

	for _, reservation := range inv.EC2.Reservations {
		for _, instance := range reservation.Instances {
			name := awsTagName(instance.Tags, instance.InstanceID)
			add(awsBrowseResource{
				Service: "EC2", Type: "instance", ID: instance.InstanceID, Name: name, Scope: region,
				Summary: strings.TrimSpace(instance.State.Name + " · " + instance.InstanceType + " · " + instance.PrivateIPAddress),
				Fields: []awsBrowseField{
					awsField("Instance ID", instance.InstanceID), awsField("Name", name), awsField("State", instance.State.Name),
					awsField("Instance type", instance.InstanceType), awsField("AMI", instance.ImageID),
					awsField("Availability zone", instance.Placement.AvailabilityZone), awsField("Private IP", instance.PrivateIPAddress),
					awsField("Public IP", instance.PublicIPAddress), awsField("Private DNS", instance.PrivateDNSName),
					awsField("Public DNS", instance.PublicDNSName), awsField("VPC", instance.VpcID), awsField("Subnet", instance.SubnetID),
					awsField("IAM instance profile", instance.IAMInstanceProfile.ARN), awsField("Launch time", instance.LaunchTime),
					awsField("Tags", awsTagsText(instance.Tags)),
				},
			})
		}
	}
	for _, volume := range inv.Volumes.Volumes {
		name := awsTagName(volume.Tags, volume.VolumeID)
		add(awsBrowseResource{
			Service: "EC2", Type: "volume", ID: volume.VolumeID, Name: name, Scope: region,
			Summary: fmt.Sprintf("%s · %d GiB · %s", volume.State, volume.Size, volume.VolumeType),
			Fields: []awsBrowseField{
				awsField("Volume ID", volume.VolumeID), awsField("Name", name), awsField("State", volume.State),
				awsField("Size", fmt.Sprintf("%d GiB", volume.Size)), awsField("Type", volume.VolumeType),
				awsField("IOPS", strconv.Itoa(volume.IOPS)), awsField("Throughput", fmt.Sprintf("%d MiB/s", volume.Throughput)),
				awsField("Encrypted", awsBool(volume.Encrypted)), awsField("KMS key", volume.KMSKeyID),
				awsField("Snapshot", volume.SnapshotID), awsField("Availability zone", volume.AvailabilityZone),
				awsField("Create time", volume.CreateTime), awsField("Tags", awsTagsText(volume.Tags)),
			},
		})
	}
	for _, group := range inv.Groups.SecurityGroups {
		name := awsTagName(group.Tags, group.GroupName)
		add(awsBrowseResource{
			Service: "EC2", Type: "security-group", ID: group.GroupID, Name: name, Scope: region,
			Summary: group.Description,
			Fields: []awsBrowseField{
				awsField("Security group ID", group.GroupID), awsField("Name", group.GroupName), awsField("Description", group.Description),
				awsField("Owner", group.OwnerID), awsField("VPC", group.VpcID),
				awsField("Ingress", awsSecurityRules(group.IPPermissions)), awsField("Egress", awsSecurityRules(group.IPPermissionsEgress)),
				awsField("Tags", awsTagsText(group.Tags)),
			},
		})
	}
	for _, vpc := range inv.VPCs.Vpcs {
		name := awsTagName(vpc.Tags, vpc.VpcID)
		add(awsBrowseResource{
			Service: "VPC", Type: "vpc", ID: vpc.VpcID, Name: name, Scope: region,
			Summary: vpc.CIDRBlock + " · " + vpc.State,
			Fields: []awsBrowseField{
				awsField("VPC ID", vpc.VpcID), awsField("Name", name), awsField("CIDR", vpc.CIDRBlock),
				awsField("State", vpc.State), awsField("Default", awsBool(vpc.IsDefault)), awsField("Owner", vpc.OwnerID),
				awsField("Tags", awsTagsText(vpc.Tags)),
			},
		})
	}
	for _, subnet := range inv.Subnets.Subnets {
		name := awsTagName(subnet.Tags, subnet.SubnetID)
		add(awsBrowseResource{
			Service: "VPC", Type: "subnet", ID: subnet.SubnetID, Name: name, Scope: region,
			Summary: subnet.CIDRBlock + " · " + subnet.AvailabilityZone,
			Fields: []awsBrowseField{
				awsField("Subnet ID", subnet.SubnetID), awsField("Name", name), awsField("VPC", subnet.VpcID),
				awsField("CIDR", subnet.CIDRBlock), awsField("Availability zone", subnet.AvailabilityZone),
				awsField("Available IPs", strconv.Itoa(subnet.AvailableIPAddressCount)),
				awsField("Auto-assign public IP", awsBool(subnet.MapPublicIPOnLaunch)), awsField("State", subnet.State),
				awsField("Tags", awsTagsText(subnet.Tags)),
			},
		})
	}
	for _, table := range inv.RouteTables.RouteTables {
		name := awsTagName(table.Tags, table.RouteTableID)
		add(awsBrowseResource{
			Service: "VPC", Type: "route-table", ID: table.RouteTableID, Name: name, Scope: region,
			Summary: fmt.Sprintf("%d routes", len(table.Routes)),
			Fields: []awsBrowseField{
				awsField("Route table ID", table.RouteTableID), awsField("Name", name), awsField("VPC", table.VpcID),
				awsField("Routes", awsRoutesText(table.Routes)), awsField("Tags", awsTagsText(table.Tags)),
			},
		})
	}
	for _, user := range inv.Users.Users {
		add(awsBrowseResource{
			Service: "IAM", Type: "iam-user", ID: user.ARN, Name: user.UserName, Scope: "global",
			Summary: user.Path,
			Fields: []awsBrowseField{
				awsField("User name", user.UserName), awsField("User ID", user.UserID), awsField("ARN", user.ARN),
				awsField("Path", user.Path), awsField("Create date", user.CreateDate), awsField("Password last used", user.PasswordLastUsed),
			},
		})
	}
	for _, role := range inv.Roles.Roles {
		add(awsBrowseResource{
			Service: "IAM", Type: "iam-role", ID: role.ARN, Name: role.RoleName, Scope: "global",
			Summary: role.Description,
			Fields: []awsBrowseField{
				awsField("Role name", role.RoleName), awsField("Role ID", role.RoleID), awsField("ARN", role.ARN),
				awsField("Path", role.Path), awsField("Description", role.Description), awsField("Create date", role.CreateDate),
				awsField("Max session", fmt.Sprintf("%d seconds", role.MaxSessionDuration)),
			},
		})
	}

	instanceTargetsByIP := map[string][]string{}
	instanceTargetsByDNS := map[string][]string{}
	for _, reservation := range inv.EC2.Reservations {
		for _, instance := range reservation.Instances {
			key := awsResourceKey("instance", instance.InstanceID)
			for _, value := range []string{instance.PrivateIPAddress, instance.PublicIPAddress} {
				if value != "" {
					instanceTargetsByIP[value] = append(instanceTargetsByIP[value], key)
				}
			}
			for _, value := range []string{instance.PrivateDNSName, instance.PublicDNSName} {
				if value != "" {
					instanceTargetsByDNS[strings.TrimSuffix(strings.ToLower(value), ".")] = append(instanceTargetsByDNS[strings.TrimSuffix(strings.ToLower(value), ".")], key)
				}
			}
			volumes := make([]string, 0, len(instance.BlockDeviceMappings))
			for _, block := range instance.BlockDeviceMappings {
				volumes = append(volumes, awsResourceKey("volume", block.EBS.VolumeID))
			}
			groups := make([]string, 0, len(instance.SecurityGroups))
			for _, group := range instance.SecurityGroups {
				groups = append(groups, awsResourceKey("security-group", group.GroupID))
			}
			awsAddRelation(resources, key, "EBS volumes", "exact", volumes...)
			awsAddRelation(resources, key, "Security groups", "exact", groups...)
			awsAddRelation(resources, key, "VPC", "exact", awsResourceKey("vpc", instance.VpcID))
			awsAddRelation(resources, key, "Subnet", "exact", awsResourceKey("subnet", instance.SubnetID))
		}
	}
	for _, volume := range inv.Volumes.Volumes {
		instances := make([]string, 0, len(volume.Attachments))
		for _, attachment := range volume.Attachments {
			instances = append(instances, awsResourceKey("instance", attachment.InstanceID))
		}
		awsAddRelation(resources, awsResourceKey("volume", volume.VolumeID), "Attached instances", "exact", instances...)
	}
	for _, group := range inv.Groups.SecurityGroups {
		groupKey := awsResourceKey("security-group", group.GroupID)
		var instances []string
		for _, reservation := range inv.EC2.Reservations {
			for _, instance := range reservation.Instances {
				for _, attached := range instance.SecurityGroups {
					if attached.GroupID == group.GroupID {
						instances = append(instances, awsResourceKey("instance", instance.InstanceID))
					}
				}
			}
		}
		var referenced []string
		for _, permission := range append(append([]awsIPPermission{}, group.IPPermissions...), group.IPPermissionsEgress...) {
			for _, pair := range permission.UserIDGroupPairs {
				referenced = append(referenced, awsResourceKey("security-group", pair.GroupID))
			}
		}
		awsAddRelation(resources, groupKey, "Attached EC2 instances", "exact", instances...)
		awsAddRelation(resources, groupKey, "Referenced security groups", "exact", referenced...)
		awsAddRelation(resources, groupKey, "VPC", "exact", awsResourceKey("vpc", group.VpcID))
	}
	for _, subnet := range inv.Subnets.Subnets {
		awsAddRelation(resources, awsResourceKey("subnet", subnet.SubnetID), "VPC", "exact", awsResourceKey("vpc", subnet.VpcID))
	}
	for _, table := range inv.RouteTables.RouteTables {
		tableKey := awsResourceKey("route-table", table.RouteTableID)
		awsAddRelation(resources, tableKey, "VPC", "exact", awsResourceKey("vpc", table.VpcID))
		for _, association := range table.Associations {
			if association.SubnetID != "" {
				awsAddRelation(resources, tableKey, "Associated subnets", "exact", awsResourceKey("subnet", association.SubnetID))
				awsAddRelation(resources, awsResourceKey("subnet", association.SubnetID), "Route tables", "exact", tableKey)
			}
		}
	}
	for _, vpc := range inv.VPCs.Vpcs {
		key := awsResourceKey("vpc", vpc.VpcID)
		var instances, subnets, groups, tables []string
		for _, reservation := range inv.EC2.Reservations {
			for _, instance := range reservation.Instances {
				if instance.VpcID == vpc.VpcID {
					instances = append(instances, awsResourceKey("instance", instance.InstanceID))
				}
			}
		}
		for _, subnet := range inv.Subnets.Subnets {
			if subnet.VpcID == vpc.VpcID {
				subnets = append(subnets, awsResourceKey("subnet", subnet.SubnetID))
			}
		}
		for _, group := range inv.Groups.SecurityGroups {
			if group.VpcID == vpc.VpcID {
				groups = append(groups, awsResourceKey("security-group", group.GroupID))
			}
		}
		for _, table := range inv.RouteTables.RouteTables {
			if table.VpcID == vpc.VpcID {
				tables = append(tables, awsResourceKey("route-table", table.RouteTableID))
			}
		}
		awsAddRelation(resources, key, "EC2 instances", "exact", instances...)
		awsAddRelation(resources, key, "Subnets", "exact", subnets...)
		awsAddRelation(resources, key, "Security groups", "exact", groups...)
		awsAddRelation(resources, key, "Route tables", "exact", tables...)
	}

	var route53ZoneKeys []string
	for _, zone := range inv.Zones.HostedZones {
		zoneID := strings.TrimPrefix(zone.ID, "/hostedzone/")
		zoneKey := awsResourceKey("hosted-zone", zoneID)
		route53ZoneKeys = append(route53ZoneKeys, zoneKey)
		add(awsBrowseResource{
			Service: "Route 53", Type: "hosted-zone", ID: zoneID, Name: strings.TrimSuffix(zone.Name, "."), Scope: "global",
			Summary: fmt.Sprintf("%d records · private=%t", zone.ResourceRecordSetCount, zone.Config.PrivateZone),
			Fields: []awsBrowseField{
				awsField("Hosted zone ID", zoneID), awsField("Domain", zone.Name),
				awsField("Private zone", awsBool(zone.Config.PrivateZone)), awsField("Record count", strconv.Itoa(zone.ResourceRecordSetCount)),
			},
		})
		var recordKeys []string
		for _, record := range inv.Records[zone.ID].ResourceRecordSets {
			recordID := zoneID + "|" + record.Name + "|" + record.Type + "|" + record.SetIdentifier
			recordKey := awsResourceKey("dns-record", recordID)
			values := make([]string, 0, len(record.ResourceRecords)+1)
			for _, value := range record.ResourceRecords {
				values = append(values, value.Value)
			}
			if record.AliasTarget != nil {
				values = append(values, record.AliasTarget.DNSName)
			}
			ttl := "alias"
			if record.TTL != nil {
				ttl = strconv.FormatInt(*record.TTL, 10)
			}
			fields := []awsBrowseField{
				awsField("Name", record.Name), awsField("Type", record.Type), awsField("TTL", ttl),
				awsField("Set identifier", record.SetIdentifier), awsField("Values", strings.Join(values, ", ")),
			}
			if record.AliasTarget != nil {
				fields = append(fields,
					awsField("Alias hosted zone", record.AliasTarget.HostedZoneID),
					awsField("Evaluate target health", awsBool(record.AliasTarget.EvaluateTargetHealth)),
				)
			}
			add(awsBrowseResource{
				Service: "Route 53", Type: "dns-record", ID: recordID,
				Name: strings.TrimSuffix(record.Name, ".") + " " + record.Type, Scope: "global",
				Summary: strings.Join(values, ", "), Fields: fields,
			})
			recordKeys = append(recordKeys, recordKey)

			var exactTargets []string
			for _, value := range values {
				trimmed := strings.Trim(value, "\"")
				exactTargets = append(exactTargets, instanceTargetsByIP[trimmed]...)
				exactTargets = append(exactTargets, instanceTargetsByDNS[strings.TrimSuffix(strings.ToLower(trimmed), ".")]...)
			}
			awsAddRelation(resources, recordKey, "Matched EC2 resources", "heuristic", exactTargets...)
			if record.AliasTarget != nil && len(exactTargets) == 0 {
				aliasID := record.AliasTarget.HostedZoneID + "|" + record.AliasTarget.DNSName
				aliasKey := awsResourceKey("alias-target", aliasID)
				if resources[aliasKey] == nil {
					add(awsBrowseResource{
						Service: "Route 53", Type: "alias-target", ID: aliasID,
						Name: strings.TrimSuffix(record.AliasTarget.DNSName, "."), Scope: "global",
						Summary: "inferred " + awsAliasService(record.AliasTarget.DNSName),
						Fields: []awsBrowseField{
							awsField("DNS name", record.AliasTarget.DNSName), awsField("Canonical hosted zone", record.AliasTarget.HostedZoneID),
							awsField("Inferred service", awsAliasService(record.AliasTarget.DNSName)),
							awsField("Resolution", "target service adapter is not enabled; DNS metadata only"),
						},
					})
				}
				awsAddRelation(resources, recordKey, "Alias target", "heuristic", aliasKey)
			}
		}
		awsAddRelation(resources, zoneKey, "DNS records", "exact", recordKeys...)
	}

	all := make([]awsBrowseResource, 0, len(resources))
	for _, resource := range resources {
		sort.Slice(resource.Relations, func(i, j int) bool { return resource.Relations[i].Name < resource.Relations[j].Name })
		all = append(all, *resource)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Service != all[j].Service {
			return all[i].Service < all[j].Service
		}
		if all[i].Type != all[j].Type {
			return all[i].Type < all[j].Type
		}
		if all[i].Name != all[j].Name {
			return all[i].Name < all[j].Name
		}
		return all[i].ID < all[j].ID
	})

	categoryKeys := func(service string, types ...string) []string {
		allowed := map[string]bool{}
		for _, kind := range types {
			allowed[kind] = true
		}
		var keys []string
		for _, resource := range all {
			if resource.Service == service && allowed[resource.Type] {
				keys = append(keys, resource.Key)
			}
		}
		return keys
	}
	return awsBrowseGraph{
		Context: awsBrowseContext{Account: inv.Identity.Account, ARN: inv.Identity.ARN, Profile: opts.Profile, Region: region},
		Categories: []awsBrowseCategory{
			{ID: "ec2", Name: "EC2", Resources: categoryKeys("EC2", "instance")},
			{ID: "route53", Name: "Route 53", Resources: route53ZoneKeys},
			{ID: "iam", Name: "IAM", Resources: categoryKeys("IAM", "iam-user", "iam-role")},
			{ID: "vpc", Name: "VPC", Resources: categoryKeys("VPC", "vpc")},
		},
		Resources: all,
	}
}

func awsSecurityRules(rules []awsIPPermission) string {
	if len(rules) == 0 {
		return "none"
	}
	values := make([]string, 0, len(rules))
	for _, rule := range rules {
		ports := "all"
		if rule.FromPort != nil && rule.ToPort != nil {
			ports = strconv.Itoa(*rule.FromPort)
			if *rule.ToPort != *rule.FromPort {
				ports += "-" + strconv.Itoa(*rule.ToPort)
			}
		}
		var sources []string
		for _, source := range rule.IPRanges {
			sources = append(sources, source.CIDR)
		}
		for _, source := range rule.IPv6Ranges {
			sources = append(sources, source.CIDR)
		}
		for _, source := range rule.UserIDGroupPairs {
			sources = append(sources, source.GroupID)
		}
		values = append(values, rule.IPProtocol+":"+ports+" from "+strings.Join(sources, ","))
	}
	return strings.Join(values, "; ")
}

func awsRoutesText(routes []struct {
	DestinationCIDRBlock     string `json:"DestinationCidrBlock"`
	DestinationIPv6CIDRBlock string `json:"DestinationIpv6CidrBlock"`
	GatewayID                string `json:"GatewayId"`
	NatGatewayID             string `json:"NatGatewayId"`
	TransitGatewayID         string `json:"TransitGatewayId"`
	VpcPeeringConnectionID   string `json:"VpcPeeringConnectionId"`
	NetworkInterfaceID       string `json:"NetworkInterfaceId"`
	InstanceID               string `json:"InstanceId"`
	State                    string `json:"State"`
}) string {
	values := make([]string, 0, len(routes))
	for _, route := range routes {
		destination := route.DestinationCIDRBlock
		if destination == "" {
			destination = route.DestinationIPv6CIDRBlock
		}
		target := firstAWSRouteTarget(route.GatewayID, route.NatGatewayID, route.TransitGatewayID, route.VpcPeeringConnectionID, route.NetworkInterfaceID, route.InstanceID)
		values = append(values, destination+" -> "+target+" ("+route.State+")")
	}
	return strings.Join(values, "; ")
}

func firstAWSRouteTarget(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "-"
}

func awsAliasService(dnsName string) string {
	name := strings.ToLower(strings.TrimSuffix(dnsName, "."))
	switch {
	case strings.Contains(name, ".elb.amazonaws.com") || strings.Contains(name, ".elb."):
		return "Elastic Load Balancing"
	case strings.HasSuffix(name, ".cloudfront.net"):
		return "CloudFront"
	case strings.Contains(name, ".execute-api."):
		return "API Gateway"
	case strings.Contains(name, ".s3-website-") || strings.Contains(name, ".s3-website."):
		return "S3 website"
	case strings.Contains(name, ".elasticbeanstalk.com"):
		return "Elastic Beanstalk"
	case strings.HasSuffix(name, ".awsglobalaccelerator.com"):
		return "Global Accelerator"
	default:
		return "AWS or external DNS target"
	}
}

func (a *App) browseAWSGraph(graph awsBrowseGraph) error {
	byKey := make(map[string]awsBrowseResource, len(graph.Resources))
	indexByKey := make(map[string]int, len(graph.Resources))
	for i, resource := range graph.Resources {
		byKey[resource.Key] = resource
		indexByKey[resource.Key] = i
	}
	categoryChoices := make([]selectChoice, 0, len(graph.Categories)+1)
	for i, category := range graph.Categories {
		categoryChoices = append(categoryChoices, selectChoice{
			Value: "category:" + strconv.Itoa(i), Label: category.Name,
			Description: fmt.Sprintf("%d resources", len(category.Resources)), SearchText: category.ID,
		})
	}
	if len(graph.Warnings) > 0 {
		categoryChoices = append(categoryChoices, selectChoice{
			Value: "warnings", Label: "Collection warnings", Description: fmt.Sprintf("%d partial failures", len(graph.Warnings)),
		})
	}
	title := "AWS resources"
	context := []string{}
	if graph.Context.Account != "" {
		context = append(context, graph.Context.Account)
	}
	if graph.Context.Profile != "" {
		context = append(context, graph.Context.Profile)
	}
	if graph.Context.Region != "" {
		context = append(context, graph.Context.Region)
	}
	if len(context) > 0 {
		title += " · " + strings.Join(context, " · ")
	}
	root := selectStage{Prompt: "AWS service", Title: title, Choices: categoryChoices}

	resourceList := func(prompt, title string, keys []string) *selectStage {
		if len(keys) == 0 {
			return &selectStage{Prompt: prompt, Title: title, ReadOnly: true, Choices: []selectChoice{{Value: "empty", Label: "No resources found", Description: "check the selected region and permissions"}}}
		}
		choices := make([]selectChoice, 0, len(keys))
		for _, key := range keys {
			resource, ok := byKey[key]
			if !ok {
				continue
			}
			choices = append(choices, selectChoice{
				Value: "resource:" + strconv.Itoa(indexByKey[key]), Label: resource.Name,
				Description: resource.Type + " · " + resource.ID + " · " + resource.Summary,
				SearchText:  resource.Service + " " + resource.Type + " " + resource.ID + " " + resource.Scope,
			})
		}
		return &selectStage{Prompt: prompt, Title: title, Choices: choices}
	}

	next := func(path []string) *selectStage {
		if len(path) == 0 {
			return nil
		}
		last := path[len(path)-1]
		if last == "warnings" {
			choices := make([]selectChoice, 0, len(graph.Warnings))
			for i, warning := range graph.Warnings {
				choices = append(choices, selectChoice{Value: strconv.Itoa(i), Label: warning})
			}
			return &selectStage{Prompt: "warning", Title: "Partial AWS inventory", ReadOnly: true, Choices: choices}
		}
		parts := strings.Split(last, ":")
		if len(parts) < 2 {
			return nil
		}
		index, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil
		}
		switch parts[0] {
		case "category":
			if index < 0 || index >= len(graph.Categories) {
				return nil
			}
			category := graph.Categories[index]
			return resourceList(category.Name+" resource", category.Name, category.Resources)
		case "resource":
			if index < 0 || index >= len(graph.Resources) {
				return nil
			}
			resource := graph.Resources[index]
			choices := []selectChoice{{Value: "details:" + strconv.Itoa(index), Label: "Details", Description: fmt.Sprintf("%d fields", len(resource.Fields))}}
			for relationIndex, relation := range resource.Relations {
				choices = append(choices, selectChoice{
					Value: fmt.Sprintf("relation:%d:%d", index, relationIndex), Label: relation.Name,
					Description: fmt.Sprintf("%d resources · %s", len(relation.Targets), relation.Confidence),
				})
			}
			return &selectStage{Prompt: "resource section", Title: resource.Name + " · " + resource.Type, Choices: choices}
		case "details":
			if index < 0 || index >= len(graph.Resources) {
				return nil
			}
			resource := graph.Resources[index]
			choices := make([]selectChoice, 0, len(resource.Fields))
			for _, field := range resource.Fields {
				choices = append(choices, selectChoice{Value: field.Name, Label: field.Name, Description: field.Value, SearchText: field.Value})
			}
			return &selectStage{Prompt: "field", Title: resource.Name + " · details", ReadOnly: true, Choices: choices}
		case "relation":
			if len(parts) != 3 || index < 0 || index >= len(graph.Resources) {
				return nil
			}
			relationIndex, err := strconv.Atoi(parts[2])
			if err != nil || relationIndex < 0 || relationIndex >= len(graph.Resources[index].Relations) {
				return nil
			}
			resource := graph.Resources[index]
			relation := resource.Relations[relationIndex]
			return resourceList("linked resource", resource.Name+" · "+relation.Name+" · "+relation.Confidence, relation.Targets)
		}
		return nil
	}

	_, err := a.selectStages(root, next)
	return err
}
