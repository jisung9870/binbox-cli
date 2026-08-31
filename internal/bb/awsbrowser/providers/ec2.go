// Package providers contains credential-free mappings from the narrowed AWS
// read clients into the AWS browser domain.
package providers

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsarn "github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
)

const ec2PageSize int32 = 100

var errInvalidEC2Query = errors.New("invalid EC2 provider query")

var iamInstanceProfileNameRE = regexp.MustCompile(`^[A-Za-z0-9_+=,.@-]+$`)

// EC2QueryExecutor owns construction, pagination, and mapping for the allowed
// frozen EC2 reads. It never accepts SDK inputs or operation options.
type EC2QueryExecutor struct {
	client awsbrowser.EC2API
	now    func() time.Time
}

// NewEC2 is the provider registry surface.
func NewEC2(client awsbrowser.EC2API, clock func() time.Time) (awsbrowser.QueryExecutor, error) {
	return NewEC2QueryExecutor(client, clock)
}

func NewEC2QueryExecutor(client awsbrowser.EC2API, clock func() time.Time) (*EC2QueryExecutor, error) {
	if client == nil || clock == nil {
		return nil, errInvalidEC2Query
	}
	return &EC2QueryExecutor{client: client, now: clock}, nil
}

func (executor *EC2QueryExecutor) Execute(ctx context.Context, key awsbrowser.QueryKey, sink awsbrowser.QueryPageSink) error {
	if executor == nil || executor.client == nil || executor.now == nil || sink == nil || ctx == nil || key.Validate() != nil ||
		key.Provider != awsbrowser.ProviderEC2 || awsbrowser.ValidateProviderOperation(key.Provider, key.Operation) != nil {
		return awsbrowser.NewProviderError(awsbrowser.ProviderUnsupported, awsbrowser.ProviderEC2, key.Operation, "InvalidQuery", "")
	}
	if err := ctx.Err(); err != nil {
		return awsbrowser.ClassifyProviderError(err, awsbrowser.ProviderEC2, key.Operation)
	}
	params, err := decodeEC2Params(key)
	if err != nil {
		return awsbrowser.NewProviderError(awsbrowser.ProviderUnsupported, awsbrowser.ProviderEC2, key.Operation, "InvalidParameters", "")
	}
	var page uint64
	switch key.Operation {
	case awsbrowser.OperationDescribeImages:
		err = executor.images(ctx, key, params, sink, &page)
	case awsbrowser.OperationDescribeInstances:
		err = executor.instances(ctx, key, params, sink, &page)
	case awsbrowser.OperationDescribeVolumes:
		err = executor.volumes(ctx, key, params, sink, &page)
	case awsbrowser.OperationDescribeSecurityGroups:
		err = executor.securityGroups(ctx, key, params, sink, &page)
	case awsbrowser.OperationDescribeSecurityGroupRules:
		err = executor.securityGroupRules(ctx, key, params, sink, &page)
	case awsbrowser.OperationDescribeVpcs:
		err = executor.vpcs(ctx, key, params, sink, &page)
	case awsbrowser.OperationDescribeSubnets:
		err = executor.subnets(ctx, key, params, sink, &page)
	case awsbrowser.OperationDescribeRouteTables:
		err = executor.routeTables(ctx, key, params, sink, &page)
	case awsbrowser.OperationDescribeVpcPeeringConnections:
		err = executor.vpcPeeringConnections(ctx, key, params, sink, &page)
	case awsbrowser.OperationDescribeLaunchTemplates:
		err = executor.launchTemplates(ctx, key, params, sink, &page)
	case awsbrowser.OperationDescribeLaunchTemplateVersions:
		err = executor.launchTemplateVersions(ctx, key, params, sink, &page)
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return awsbrowser.ClassifyProviderError(err, awsbrowser.ProviderEC2, key.Operation)
	}
	completedAt := executor.now().UTC()
	if completedAt.IsZero() {
		return providerFailure(errInvalidEC2Query, key.Operation)
	}
	return sink.Complete(completedAt)
}

type ec2Params struct {
	ids       []string
	filters   []types.Filter
	direction string
	versions  []string
	view      string
}

var ec2Selectors = map[string]string{
	awsbrowser.OperationDescribeImages:                 "image-id",
	awsbrowser.OperationDescribeInstances:              "instance-id",
	awsbrowser.OperationDescribeVolumes:                "volume-id",
	awsbrowser.OperationDescribeSecurityGroups:         "group-id",
	awsbrowser.OperationDescribeSecurityGroupRules:     "security-group-rule-id",
	awsbrowser.OperationDescribeVpcs:                   "vpc-id",
	awsbrowser.OperationDescribeSubnets:                "subnet-id",
	awsbrowser.OperationDescribeRouteTables:            "route-table-id",
	awsbrowser.OperationDescribeVpcPeeringConnections:  "vpc-peering-connection-id",
	awsbrowser.OperationDescribeLaunchTemplates:        "launch-template-id",
	awsbrowser.OperationDescribeLaunchTemplateVersions: "launch-template-id",
}

var ec2FilterAllowlist = map[string]map[string]struct{}{
	awsbrowser.OperationDescribeInstances:              allow("availability-zone", "architecture", "image-id", "instance-state-name", "instance-type", "instance.group-id", "private-ip-address", "subnet-id", "tag-key", "vpc-id"),
	awsbrowser.OperationDescribeVolumes:                allow("attachment.instance-id", "availability-zone", "encrypted", "snapshot-id", "status", "tag-key", "volume-type"),
	awsbrowser.OperationDescribeSecurityGroups:         allow("group-name", "owner-id", "tag-key", "vpc-id"),
	awsbrowser.OperationDescribeSecurityGroupRules:     allow("group-id", "tag-key"),
	awsbrowser.OperationDescribeVpcs:                   allow("cidr", "is-default", "owner-id", "state", "tag-key"),
	awsbrowser.OperationDescribeSubnets:                allow("availability-zone", "cidr-block", "default-for-az", "state", "tag-key", "vpc-id"),
	awsbrowser.OperationDescribeRouteTables:            allow("association.main", "association.subnet-id", "route.destination-cidr-block", "route.state", "tag-key", "vpc-id"),
	awsbrowser.OperationDescribeVpcPeeringConnections:  allow("accepter-vpc-info.cidr-block", "accepter-vpc-info.owner-id", "accepter-vpc-info.vpc-id", "requester-vpc-info.cidr-block", "requester-vpc-info.owner-id", "requester-vpc-info.vpc-id", "status-code", "status-message", "tag-key"),
	awsbrowser.OperationDescribeLaunchTemplates:        allow("create-time", "launch-template-name", "tag-key"),
	awsbrowser.OperationDescribeLaunchTemplateVersions: allow("create-time", "ebs-optimized", "http-endpoint", "http-protocol-ipv4", "http-tokens", "iam-instance-profile", "image-id", "instance-type", "is-default-version", "kernel-id", "license-configuration-arn", "network-card-index", "ram-disk-id"),
}

func allow(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func decodeEC2Params(key awsbrowser.QueryKey) (ec2Params, error) {
	values, err := url.ParseQuery(key.ParamsKey)
	if err != nil {
		return ec2Params{}, errInvalidEC2Query
	}
	selector := ec2Selectors[key.Operation]
	allowed := ec2FilterAllowlist[key.Operation]
	result := ec2Params{}
	for name, encoded := range values {
		if len(encoded) != 1 {
			return ec2Params{}, errInvalidEC2Query
		}
		items := splitValues(encoded[0])
		if len(items) == 0 {
			return ec2Params{}, errInvalidEC2Query
		}
		if name == selector {
			result.ids = items
			continue
		}
		if name == "version" {
			if key.Operation != awsbrowser.OperationDescribeLaunchTemplateVersions {
				return ec2Params{}, errInvalidEC2Query
			}
			for _, version := range items {
				if !validLaunchTemplateVersion(version) {
					return ec2Params{}, errInvalidEC2Query
				}
			}
			result.versions = items
			continue
		}
		if name == "view" {
			if key.Operation != awsbrowser.OperationDescribeLaunchTemplateVersions || len(items) != 1 || items[0] != "user-data" {
				return ec2Params{}, errInvalidEC2Query
			}
			result.view = items[0]
			continue
		}
		if name == "direction" {
			if key.Operation != awsbrowser.OperationDescribeSecurityGroupRules || len(items) != 1 ||
				(items[0] != "ingress" && items[0] != "egress") {
				return ec2Params{}, errInvalidEC2Query
			}
			result.direction = items[0]
			continue
		}
		filterName := strings.TrimPrefix(name, "filter.")
		if filterName == name {
			filterName = name
		}
		if strings.HasPrefix(filterName, "tag:") {
			if len(filterName) <= len("tag:") {
				return ec2Params{}, errInvalidEC2Query
			}
		} else if _, ok := allowed[filterName]; !ok {
			return ec2Params{}, errInvalidEC2Query
		}
		result.filters = append(result.filters, types.Filter{Name: aws.String(filterName), Values: items})
	}
	if len(result.ids) != 0 && len(result.filters) != 0 {
		return ec2Params{}, errInvalidEC2Query
	}
	if key.Operation == awsbrowser.OperationDescribeLaunchTemplateVersions && len(result.ids) != 1 {
		return ec2Params{}, errInvalidEC2Query
	}
	if key.Operation == awsbrowser.OperationDescribeImages && len(result.ids) != 1 {
		return ec2Params{}, errInvalidEC2Query
	}
	if result.view == "user-data" && len(result.versions) != 1 {
		return ec2Params{}, errInvalidEC2Query
	}
	sort.Slice(result.filters, func(i, j int) bool {
		return aws.ToString(result.filters[i].Name) < aws.ToString(result.filters[j].Name)
	})
	return result, nil
}

func validLaunchTemplateVersion(value string) bool {
	if value == "$Default" || value == "$Latest" {
		return true
	}
	version, err := strconv.ParseInt(value, 10, 64)
	return err == nil && version > 0
}

func splitValues(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || len(part) > 1024 {
			return nil
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		result = append(result, part)
	}
	sort.Strings(result)
	return result
}

type tokenGuard struct{ seen map[string]struct{} }

func (guard *tokenGuard) next(current, returned *string) (*string, bool, error) {
	if returned == nil {
		return nil, true, nil
	}
	next := strings.TrimSpace(aws.ToString(returned))
	if next == "" || next == aws.ToString(current) {
		return nil, false, errInvalidEC2Query
	}
	if guard.seen == nil {
		guard.seen = map[string]struct{}{}
	}
	if _, exists := guard.seen[next]; exists {
		return nil, false, errInvalidEC2Query
	}
	guard.seen[next] = struct{}{}
	return aws.String(next), false, nil
}

func validateEC2Page(guard *tokenGuard, current, returned *string, ids []string, found map[string]bool, operation string) (*string, bool, error) {
	next, done, err := guard.next(current, returned)
	if err != nil {
		return nil, false, providerFailure(err, operation)
	}
	if done {
		if err := requireTargets(ids, found, operation); err != nil {
			return nil, false, err
		}
	}
	return next, done, nil
}

func providerFailure(err error, operation string) error {
	if errors.Is(err, errInvalidEC2Query) || errors.Is(err, awsbrowser.ErrInvalidMappedFields) || errors.Is(err, awsbrowser.ErrIncompleteObservation) || errors.Is(err, awsbrowser.ErrIncompletePage) || errors.Is(err, awsbrowser.ErrInvalidResourceKey) {
		return awsbrowser.NewProviderError(awsbrowser.ProviderDecode, awsbrowser.ProviderEC2, operation, "InvalidResult", "")
	}
	return awsbrowser.ClassifyProviderError(err, awsbrowser.ProviderEC2, operation)
}

func (executor *EC2QueryExecutor) emit(ctx context.Context, key awsbrowser.QueryKey, sink awsbrowser.QueryPageSink, page *uint64, resources []awsbrowser.ObservedResource) error {
	if err := ctx.Err(); err != nil {
		return providerFailure(err, key.Operation)
	}
	when := executor.now().UTC()
	mapped, err := awsbrowser.NewQueryPage(*page, resources, when, true)
	if err != nil {
		return providerFailure(err, key.Operation)
	}
	if err := ctx.Err(); err != nil {
		return providerFailure(err, key.Operation)
	}
	if err := sink.Page(mapped); err != nil {
		return err
	}
	*page++
	return nil
}

func maxFor(ids []string) *int32 {
	if len(ids) != 0 {
		return nil
	}
	return aws.Int32(ec2PageSize)
}

func (executor *EC2QueryExecutor) images(ctx context.Context, key awsbrowser.QueryKey, params ec2Params, sink awsbrowser.QueryPageSink, page *uint64) error {
	var token *string
	guard := tokenGuard{}
	found := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return providerFailure(err, key.Operation)
		}
		out, err := executor.client.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: params.ids, NextToken: token})
		if err != nil {
			if awsbrowser.IsProviderNotFound(err) {
				return nil
			}
			return providerFailure(err, key.Operation)
		}
		if out == nil {
			return providerFailure(errInvalidEC2Query, key.Operation)
		}
		resources := make([]awsbrowser.ObservedResource, 0, len(out.Images))
		for _, value := range out.Images {
			resource, mapErr := mapImage(key.Context, key.Operation, executor.now(), value)
			if mapErr != nil || !targetMatch(params.ids, resource.Key.ID, found) {
				return providerFailure(errInvalidEC2Query, key.Operation)
			}
			resources = append(resources, resource)
		}
		next, done, pageErr := guard.next(token, out.NextToken)
		if pageErr != nil {
			return providerFailure(pageErr, key.Operation)
		}
		if done && len(found) == 0 {
			return nil
		}
		if len(resources) != 0 {
			if err = executor.emit(ctx, key, sink, page, resources); err != nil {
				return err
			}
		}
		if done {
			if err = requireTargets(params.ids, found, key.Operation); err != nil {
				return err
			}
			return nil
		}
		token = next
	}
}

func (executor *EC2QueryExecutor) instances(ctx context.Context, key awsbrowser.QueryKey, params ec2Params, sink awsbrowser.QueryPageSink, page *uint64) error {
	var token *string
	guard := tokenGuard{}
	found := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return providerFailure(err, key.Operation)
		}
		out, err := executor.client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: params.ids, Filters: params.filters, MaxResults: maxFor(params.ids), NextToken: token})
		if err != nil {
			return providerFailure(err, key.Operation)
		}
		if out == nil {
			return providerFailure(errInvalidEC2Query, key.Operation)
		}
		resources := []awsbrowser.ObservedResource{}
		for _, reservation := range out.Reservations {
			for _, item := range reservation.Instances {
				if err := ctx.Err(); err != nil {
					return providerFailure(err, key.Operation)
				}
				resource, err := mapInstance(key.Context, key.Operation, executor.now(), item)
				if err != nil || !targetMatch(params.ids, resource.Key.ID, found) {
					return providerFailure(errInvalidEC2Query, key.Operation)
				}
				resources = append(resources, resource)
			}
		}
		next, done, err := validateEC2Page(&guard, token, out.NextToken, params.ids, found, key.Operation)
		if err != nil {
			return err
		}
		if err := executor.emit(ctx, key, sink, page, resources); err != nil {
			return err
		}
		if done {
			return nil
		}
		token = next
	}
}

func (executor *EC2QueryExecutor) volumes(ctx context.Context, key awsbrowser.QueryKey, params ec2Params, sink awsbrowser.QueryPageSink, page *uint64) error {
	var token *string
	guard := tokenGuard{}
	found := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return providerFailure(err, key.Operation)
		}
		out, err := executor.client.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: params.ids, Filters: params.filters, MaxResults: maxFor(params.ids), NextToken: token})
		if err != nil {
			return providerFailure(err, key.Operation)
		}
		if out == nil {
			return providerFailure(errInvalidEC2Query, key.Operation)
		}
		resources := make([]awsbrowser.ObservedResource, 0, len(out.Volumes))
		for _, item := range out.Volumes {
			if err := ctx.Err(); err != nil {
				return providerFailure(err, key.Operation)
			}
			resource, e := mapVolume(key.Context, key.Operation, executor.now(), item)
			if e != nil || !targetMatch(params.ids, resource.Key.ID, found) {
				return providerFailure(errInvalidEC2Query, key.Operation)
			}
			resources = append(resources, resource)
		}
		next, done, e := validateEC2Page(&guard, token, out.NextToken, params.ids, found, key.Operation)
		if e != nil {
			return e
		}
		if err = executor.emit(ctx, key, sink, page, resources); err != nil {
			return err
		}
		if done {
			return nil
		}
		token = next
	}
}

func (executor *EC2QueryExecutor) securityGroups(ctx context.Context, key awsbrowser.QueryKey, params ec2Params, sink awsbrowser.QueryPageSink, page *uint64) error {
	var token *string
	guard := tokenGuard{}
	found := map[string]bool{}
	for {
		if e := ctx.Err(); e != nil {
			return providerFailure(e, key.Operation)
		}
		out, e := executor.client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: params.ids, Filters: params.filters, MaxResults: maxFor(params.ids), NextToken: token})
		if e != nil {
			return providerFailure(e, key.Operation)
		}
		if out == nil {
			return providerFailure(errInvalidEC2Query, key.Operation)
		}
		rs := make([]awsbrowser.ObservedResource, 0, len(out.SecurityGroups))
		for _, v := range out.SecurityGroups {
			if e := ctx.Err(); e != nil {
				return providerFailure(e, key.Operation)
			}
			r, m := mapSecurityGroup(key.Context, key.Operation, executor.now(), v)
			if m != nil || !targetMatch(params.ids, r.Key.ID, found) {
				return providerFailure(errInvalidEC2Query, key.Operation)
			}
			rs = append(rs, r)
		}
		next, done, m := validateEC2Page(&guard, token, out.NextToken, params.ids, found, key.Operation)
		if m != nil {
			return m
		}
		if e = executor.emit(ctx, key, sink, page, rs); e != nil {
			return e
		}
		if done {
			return nil
		}
		token = next
	}
}

func (executor *EC2QueryExecutor) securityGroupRules(ctx context.Context, key awsbrowser.QueryKey, params ec2Params, sink awsbrowser.QueryPageSink, page *uint64) error {
	var token *string
	guard := tokenGuard{}
	found := map[string]bool{}
	for {
		if e := ctx.Err(); e != nil {
			return providerFailure(e, key.Operation)
		}
		out, e := executor.client.DescribeSecurityGroupRules(ctx, &ec2.DescribeSecurityGroupRulesInput{SecurityGroupRuleIds: params.ids, Filters: params.filters, MaxResults: maxFor(params.ids), NextToken: token})
		if e != nil {
			return providerFailure(e, key.Operation)
		}
		if out == nil {
			return providerFailure(errInvalidEC2Query, key.Operation)
		}
		rs := make([]awsbrowser.ObservedResource, 0, len(out.SecurityGroupRules))
		for _, v := range out.SecurityGroupRules {
			if e := ctx.Err(); e != nil {
				return providerFailure(e, key.Operation)
			}
			if (params.direction == "ingress" && b(v.IsEgress)) || (params.direction == "egress" && !b(v.IsEgress)) {
				continue
			}
			r, m := mapSecurityGroupRule(key.Context, key.Operation, executor.now(), v)
			if m != nil || !targetMatch(params.ids, r.Key.ID, found) {
				return providerFailure(errInvalidEC2Query, key.Operation)
			}
			rs = append(rs, r)
		}
		next, done, m := validateEC2Page(&guard, token, out.NextToken, params.ids, found, key.Operation)
		if m != nil {
			return m
		}
		if e = executor.emit(ctx, key, sink, page, rs); e != nil {
			return e
		}
		if done {
			return nil
		}
		token = next
	}
}

func (executor *EC2QueryExecutor) vpcs(ctx context.Context, key awsbrowser.QueryKey, params ec2Params, sink awsbrowser.QueryPageSink, page *uint64) error {
	var token *string
	guard := tokenGuard{}
	found := map[string]bool{}
	for {
		if e := ctx.Err(); e != nil {
			return providerFailure(e, key.Operation)
		}
		out, e := executor.client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: params.ids, Filters: params.filters, MaxResults: maxFor(params.ids), NextToken: token})
		if e != nil {
			return providerFailure(e, key.Operation)
		}
		if out == nil {
			return providerFailure(errInvalidEC2Query, key.Operation)
		}
		rs := make([]awsbrowser.ObservedResource, 0, len(out.Vpcs))
		for _, v := range out.Vpcs {
			if e := ctx.Err(); e != nil {
				return providerFailure(e, key.Operation)
			}
			r, m := mapVpc(key.Context, key.Operation, executor.now(), v)
			if m != nil || !targetMatch(params.ids, r.Key.ID, found) {
				return providerFailure(errInvalidEC2Query, key.Operation)
			}
			rs = append(rs, r)
		}
		next, done, m := validateEC2Page(&guard, token, out.NextToken, params.ids, found, key.Operation)
		if m != nil {
			return m
		}
		if e = executor.emit(ctx, key, sink, page, rs); e != nil {
			return e
		}
		if done {
			return nil
		}
		token = next
	}
}

func (executor *EC2QueryExecutor) subnets(ctx context.Context, key awsbrowser.QueryKey, params ec2Params, sink awsbrowser.QueryPageSink, page *uint64) error {
	var token *string
	guard := tokenGuard{}
	found := map[string]bool{}
	for {
		if e := ctx.Err(); e != nil {
			return providerFailure(e, key.Operation)
		}
		out, e := executor.client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: params.ids, Filters: params.filters, MaxResults: maxFor(params.ids), NextToken: token})
		if e != nil {
			return providerFailure(e, key.Operation)
		}
		if out == nil {
			return providerFailure(errInvalidEC2Query, key.Operation)
		}
		rs := make([]awsbrowser.ObservedResource, 0, len(out.Subnets))
		for _, v := range out.Subnets {
			if e := ctx.Err(); e != nil {
				return providerFailure(e, key.Operation)
			}
			r, m := mapSubnet(key.Context, key.Operation, executor.now(), v)
			if m != nil || !targetMatch(params.ids, r.Key.ID, found) {
				return providerFailure(errInvalidEC2Query, key.Operation)
			}
			rs = append(rs, r)
		}
		next, done, m := validateEC2Page(&guard, token, out.NextToken, params.ids, found, key.Operation)
		if m != nil {
			return m
		}
		if e = executor.emit(ctx, key, sink, page, rs); e != nil {
			return e
		}
		if done {
			return nil
		}
		token = next
	}
}

func (executor *EC2QueryExecutor) routeTables(ctx context.Context, key awsbrowser.QueryKey, params ec2Params, sink awsbrowser.QueryPageSink, page *uint64) error {
	var token *string
	guard := tokenGuard{}
	found := map[string]bool{}
	for {
		if e := ctx.Err(); e != nil {
			return providerFailure(e, key.Operation)
		}
		out, e := executor.client.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{RouteTableIds: params.ids, Filters: params.filters, MaxResults: maxFor(params.ids), NextToken: token})
		if e != nil {
			return providerFailure(e, key.Operation)
		}
		if out == nil {
			return providerFailure(errInvalidEC2Query, key.Operation)
		}
		rs := make([]awsbrowser.ObservedResource, 0, len(out.RouteTables))
		for _, v := range out.RouteTables {
			if e := ctx.Err(); e != nil {
				return providerFailure(e, key.Operation)
			}
			r, m := mapRouteTable(key.Context, key.Operation, executor.now(), v)
			if m != nil || !targetMatch(params.ids, r.Key.ID, found) {
				return providerFailure(errInvalidEC2Query, key.Operation)
			}
			rs = append(rs, r)
		}
		next, done, m := validateEC2Page(&guard, token, out.NextToken, params.ids, found, key.Operation)
		if m != nil {
			return m
		}
		if e = executor.emit(ctx, key, sink, page, rs); e != nil {
			return e
		}
		if done {
			return nil
		}
		token = next
	}
}

func (executor *EC2QueryExecutor) vpcPeeringConnections(ctx context.Context, key awsbrowser.QueryKey, params ec2Params, sink awsbrowser.QueryPageSink, page *uint64) error {
	var token *string
	guard := tokenGuard{}
	found := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return providerFailure(err, key.Operation)
		}
		out, err := executor.client.DescribeVpcPeeringConnections(ctx, &ec2.DescribeVpcPeeringConnectionsInput{
			VpcPeeringConnectionIds: params.ids, Filters: params.filters, MaxResults: maxFor(params.ids), NextToken: token,
		})
		if err != nil {
			return providerFailure(err, key.Operation)
		}
		if out == nil {
			return providerFailure(errInvalidEC2Query, key.Operation)
		}
		resources := make([]awsbrowser.ObservedResource, 0, len(out.VpcPeeringConnections))
		for _, value := range out.VpcPeeringConnections {
			if err := ctx.Err(); err != nil {
				return providerFailure(err, key.Operation)
			}
			resource, mapErr := mapVpcPeeringConnection(key.Context, key.Operation, executor.now(), value)
			if mapErr != nil || !targetMatch(params.ids, resource.Key.ID, found) {
				return providerFailure(errInvalidEC2Query, key.Operation)
			}
			resources = append(resources, resource)
		}
		next, done, validateErr := validateEC2Page(&guard, token, out.NextToken, params.ids, found, key.Operation)
		if validateErr != nil {
			return validateErr
		}
		if err = executor.emit(ctx, key, sink, page, resources); err != nil {
			return err
		}
		if done {
			return nil
		}
		token = next
	}
}

func (executor *EC2QueryExecutor) launchTemplates(ctx context.Context, key awsbrowser.QueryKey, params ec2Params, sink awsbrowser.QueryPageSink, page *uint64) error {
	var token *string
	guard := tokenGuard{}
	found := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return providerFailure(err, key.Operation)
		}
		out, err := executor.client.DescribeLaunchTemplates(ctx, &ec2.DescribeLaunchTemplatesInput{
			LaunchTemplateIds: params.ids, Filters: params.filters, MaxResults: maxFor(params.ids), NextToken: token,
		})
		if err != nil {
			return providerFailure(err, key.Operation)
		}
		if out == nil {
			return providerFailure(errInvalidEC2Query, key.Operation)
		}
		resources := make([]awsbrowser.ObservedResource, 0, len(out.LaunchTemplates))
		for _, value := range out.LaunchTemplates {
			resource, mapErr := mapLaunchTemplate(key.Context, key.Operation, executor.now(), value)
			if mapErr != nil || !targetMatch(params.ids, resource.Key.ID, found) {
				return providerFailure(errInvalidEC2Query, key.Operation)
			}
			resources = append(resources, resource)
		}
		next, done, validateErr := validateEC2Page(&guard, token, out.NextToken, params.ids, found, key.Operation)
		if validateErr != nil {
			return validateErr
		}
		if err = executor.emit(ctx, key, sink, page, resources); err != nil {
			return err
		}
		if done {
			return nil
		}
		token = next
	}
}

func (executor *EC2QueryExecutor) launchTemplateVersions(ctx context.Context, key awsbrowser.QueryKey, params ec2Params, sink awsbrowser.QueryPageSink, page *uint64) error {
	var token *string
	guard := tokenGuard{}
	for {
		if err := ctx.Err(); err != nil {
			return providerFailure(err, key.Operation)
		}
		out, err := executor.client.DescribeLaunchTemplateVersions(ctx, &ec2.DescribeLaunchTemplateVersionsInput{
			LaunchTemplateId: aws.String(params.ids[0]), Versions: params.versions, MaxResults: maxFor(params.versions), NextToken: token,
		})
		if err != nil {
			return providerFailure(err, key.Operation)
		}
		if out == nil {
			return providerFailure(errInvalidEC2Query, key.Operation)
		}
		resources := make([]awsbrowser.ObservedResource, 0, len(out.LaunchTemplateVersions))
		for _, value := range out.LaunchTemplateVersions {
			if s(value.LaunchTemplateId) != params.ids[0] {
				return providerFailure(errInvalidEC2Query, key.Operation)
			}
			resource, mapErr := mapLaunchTemplateVersion(key.Context, key.Operation, executor.now(), value, params.view == "user-data")
			if mapErr != nil {
				return providerFailure(errInvalidEC2Query, key.Operation)
			}
			resources = append(resources, resource)
		}
		next, done, validateErr := guard.next(token, out.NextToken)
		if validateErr != nil {
			return providerFailure(validateErr, key.Operation)
		}
		if err = executor.emit(ctx, key, sink, page, resources); err != nil {
			return err
		}
		if done {
			return nil
		}
		token = next
	}
}

func targetMatch(ids []string, id string, found map[string]bool) bool {
	if len(ids) == 0 {
		return true
	}
	for _, want := range ids {
		if id == want {
			found[id] = true
			return true
		}
	}
	return false
}
func requireTargets(ids []string, found map[string]bool, operation string) error {
	for _, id := range ids {
		if !found[id] {
			return providerFailure(errInvalidEC2Query, operation)
		}
	}
	return nil
}

func observed(context awsbrowser.AWSContext, operation, kind, id string, at time.Time, fields map[string]any) (awsbrowser.ObservedResource, error) {
	key, e := awsbrowser.NewRegionalResourceKey(context, "ec2."+kind, id)
	if e != nil {
		return awsbrowser.ObservedResource{}, e
	}
	fields["id"] = id
	observation, e := awsbrowser.NewResourceObservationForOperation(context, operation, fields, at.UTC(), true)
	if e != nil {
		return awsbrowser.ObservedResource{}, e
	}
	return awsbrowser.ObservedResource{Key: key, Observation: observation}, nil
}
func tags(values []types.Tag) (map[string]string, error) {
	result := map[string]string{}
	for _, tag := range values {
		if tag.Key == nil || tag.Value == nil {
			return nil, errInvalidEC2Query
		}
		if _, duplicate := result[*tag.Key]; duplicate {
			return nil, errInvalidEC2Query
		}
		result[*tag.Key] = *tag.Value
	}
	return result, nil
}
func s(v *string) string { return aws.ToString(v) }
func b(v *bool) bool     { return aws.ToBool(v) }
func i32(v *int32) int32 { return aws.ToInt32(v) }
func i64(v *int64) int64 { return aws.ToInt64(v) }

func relation(context awsbrowser.AWSContext, operation, reason, targetType, targetID string, relationType awsbrowser.RelationType, condition string, at time.Time) (map[string]any, error) {
	target, e := awsbrowser.NewRegionalResourceKey(context, "ec2."+targetType, targetID)
	if e != nil {
		return nil, e
	}
	return relationTo(context, operation, reason, target, relationType, condition, at)
}

func relationTo(context awsbrowser.AWSContext, operation, reason string, target awsbrowser.ResourceKey, relationType awsbrowser.RelationType, condition string, at time.Time) (map[string]any, error) {
	if target.Validate() != nil {
		return nil, errInvalidEC2Query
	}
	evidence, e := awsbrowser.NewRelationEvidence(awsbrowser.RelationIDExact, reason, operation, context.Region, at)
	if e != nil {
		return nil, e
	}
	semantics, e := awsbrowser.NewRelationSemantics(relationType, awsbrowser.RelationOutgoing, condition)
	if e != nil {
		return nil, e
	}
	return map[string]any{"target": target, "relation_type": string(semantics.Type), "direction": string(semantics.Direction), "condition": semantics.Condition, "kind": string(evidence.Kind), "reason": evidence.Reason, "operation": evidence.Operation, "scope": evidence.Scope, "observed_at": evidence.ObservedAt}, nil
}

func relationBetween(context awsbrowser.AWSContext, operation, reason string, source, target awsbrowser.ResourceKey, relationType awsbrowser.RelationType, condition string, at time.Time) (map[string]any, error) {
	relation, err := relationTo(context, operation, reason, target, relationType, condition, at)
	if err != nil || source.Validate() != nil {
		return nil, errInvalidEC2Query
	}
	relation["source"] = source
	return relation, nil
}

func globalRelation(context awsbrowser.AWSContext, operation, reason, targetType, targetID string, relationType awsbrowser.RelationType, condition string, at time.Time) (map[string]any, error) {
	target, err := awsbrowser.NewGlobalResourceKey(context, targetType, targetID)
	if err != nil {
		return nil, err
	}
	evidence, err := awsbrowser.NewRelationEvidence(awsbrowser.RelationIDExact, reason, operation, awsbrowser.GlobalRegion, at)
	if err != nil {
		return nil, err
	}
	semantics, err := awsbrowser.NewRelationSemantics(relationType, awsbrowser.RelationOutgoing, condition)
	if err != nil {
		return nil, err
	}
	return map[string]any{"target": target, "relation_type": string(semantics.Type), "direction": string(semantics.Direction), "condition": semantics.Condition, "kind": string(evidence.Kind), "reason": evidence.Reason, "operation": evidence.Operation, "scope": evidence.Scope, "observed_at": evidence.ObservedAt}, nil
}

func parseGlobalARN(context awsbrowser.AWSContext, value, service, resourcePrefix string) (awsarn.ARN, error) {
	value = strings.TrimSpace(value)
	parsed, err := awsarn.Parse(value)
	if err != nil || parsed.Partition != context.Partition || parsed.AccountID != context.AccountID || parsed.Service != service || parsed.Region != "" ||
		!strings.HasPrefix(parsed.Resource, resourcePrefix) || len(parsed.Resource) == len(resourcePrefix) {
		return awsarn.ARN{}, errInvalidEC2Query
	}
	return parsed, nil
}

func instanceProfileRelation(context awsbrowser.AWSContext, operation string, profile *types.IamInstanceProfile, at time.Time) (string, string, map[string]any, error) {
	if profile == nil {
		return "", "", nil, nil
	}
	parsed, err := parseGlobalARN(context, s(profile.Arn), "iam", "instance-profile/")
	if err != nil {
		return "", "", nil, err
	}
	pathAndName := strings.TrimPrefix(parsed.Resource, "instance-profile/")
	name := pathAndName[strings.LastIndex(pathAndName, "/")+1:]
	if !iamInstanceProfileNameRE.MatchString(name) || len(name) > 128 {
		return "", "", nil, errInvalidEC2Query
	}
	relation, err := globalRelation(context, operation, "instance iam profile arn", "iam.instance-profile", name, awsbrowser.RelationUses, "", at)
	return parsed.String(), name, relation, err
}

func addRelation(context awsbrowser.AWSContext, operation, reason, targetType, targetID string, relationType awsbrowser.RelationType, condition string, at time.Time, relations *[]any) error {
	if targetID == "" {
		return nil
	}
	r, e := relation(context, operation, reason, targetType, targetID, relationType, condition, at)
	if e == nil {
		*relations = append(*relations, r)
	}
	return e
}

func bindRelationSources(context awsbrowser.AWSContext, sourceType, sourceID string, relations []any) error {
	source, err := awsbrowser.NewRegionalResourceKey(context, "ec2."+sourceType, sourceID)
	if err != nil {
		return err
	}
	for _, raw := range relations {
		relation, ok := raw.(map[string]any)
		if !ok {
			return errInvalidEC2Query
		}
		relation["source"] = source
	}
	return nil
}

func mapImage(c awsbrowser.AWSContext, op string, at time.Time, value types.Image) (awsbrowser.ObservedResource, error) {
	mappedTags, err := tags(value.Tags)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	name := s(value.Name)
	if name == "" {
		name = mappedTags["Name"]
	}
	fields := map[string]any{
		"name":                name,
		"description":         s(value.Description),
		"state":               string(value.State),
		"architecture":        string(value.Architecture),
		"image_type":          string(value.ImageType),
		"platform_details":    s(value.PlatformDetails),
		"owner_id":            s(value.OwnerId),
		"creation_date":       s(value.CreationDate),
		"root_device_name":    s(value.RootDeviceName),
		"root_device_type":    string(value.RootDeviceType),
		"virtualization_type": string(value.VirtualizationType),
		"ena_support":         b(value.EnaSupport),
		"public":              b(value.Public),
		"tags":                mappedTags,
	}
	return observed(c, op, "image", s(value.ImageId), at, fields)
}

func mapInstance(c awsbrowser.AWSContext, op string, at time.Time, v types.Instance) (awsbrowser.ObservedResource, error) {
	id := s(v.InstanceId)
	mappedTags, err := tags(v.Tags)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	rel := []any{}
	if e := addRelation(c, op, "instance image id", "image", s(v.ImageId), awsbrowser.RelationUses, "", at, &rel); e != nil {
		return awsbrowser.ObservedResource{}, e
	}
	if e := addRelation(c, op, "instance vpc id", "vpc", s(v.VpcId), awsbrowser.RelationMemberOf, "", at, &rel); e != nil {
		return awsbrowser.ObservedResource{}, e
	}
	if e := addRelation(c, op, "instance subnet id", "subnet", s(v.SubnetId), awsbrowser.RelationMemberOf, "", at, &rel); e != nil {
		return awsbrowser.ObservedResource{}, e
	}
	groups := []awsbrowser.ResourceKey{}
	seenGroups := map[string]bool{}
	addGroup := func(groupID, condition string) error {
		if groupID == "" {
			return nil
		}
		if !seenGroups[groupID] {
			key, err := awsbrowser.NewRegionalResourceKey(c, "ec2.security-group", groupID)
			if err != nil {
				return err
			}
			groups = append(groups, key)
			seenGroups[groupID] = true
		}
		return addRelation(c, op, "instance network interface security group id", "security-group", groupID, awsbrowser.RelationUses, condition, at, &rel)
	}
	if len(v.NetworkInterfaces) != 0 {
		for _, networkInterface := range v.NetworkInterfaces {
			condition := "network-interface=" + s(networkInterface.NetworkInterfaceId)
			for _, group := range networkInterface.Groups {
				if err := addGroup(s(group.GroupId), condition); err != nil {
					return awsbrowser.ObservedResource{}, err
				}
			}
		}
	} else {
		for _, group := range v.SecurityGroups {
			if err := addGroup(s(group.GroupId), "network-interface=unknown"); err != nil {
				return awsbrowser.ObservedResource{}, err
			}
		}
	}
	volumes := []awsbrowser.ResourceKey{}
	for _, m := range v.BlockDeviceMappings {
		if m.Ebs != nil && s(m.Ebs.VolumeId) != "" {
			k, e := awsbrowser.NewRegionalResourceKey(c, "ec2.volume", s(m.Ebs.VolumeId))
			if e != nil {
				return awsbrowser.ObservedResource{}, e
			}
			volumes = append(volumes, k)
			if e = addRelation(c, op, "instance block device volume id", "volume", s(m.Ebs.VolumeId), awsbrowser.RelationAttachedTo, s(m.DeviceName), at, &rel); e != nil {
				return awsbrowser.ObservedResource{}, e
			}
		}
	}
	profileARN, profileName, profileRelation, err := instanceProfileRelation(c, op, v.IamInstanceProfile, at)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	if profileRelation != nil {
		rel = append(rel, profileRelation)
	}
	fields := map[string]any{"name": mappedTags["Name"], "image_id": s(v.ImageId), "instance_type": string(v.InstanceType), "state": "", "availability_zone": "", "private_ip_address": s(v.PrivateIpAddress), "public_ip_address": s(v.PublicIpAddress), "private_dns_name": s(v.PrivateDnsName), "public_dns_name": s(v.PublicDnsName), "vpc_id": s(v.VpcId), "subnet_id": s(v.SubnetId), "instance_profile_arn": profileARN, "instance_profile_name": profileName, "security_groups": groups, "volumes": volumes, "tags": mappedTags, "relations": rel}
	if v.State != nil {
		fields["state"] = string(v.State.Name)
	}
	if v.Placement != nil {
		fields["availability_zone"] = s(v.Placement.AvailabilityZone)
	}
	if v.LaunchTime != nil {
		fields["launch_time"] = v.LaunchTime.UTC()
	}
	if err = bindRelationSources(c, "instance", id, rel); err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	return observed(c, op, "instance", id, at, fields)
}

func mapVolume(c awsbrowser.AWSContext, op string, at time.Time, v types.Volume) (awsbrowser.ObservedResource, error) {
	mappedTags, err := tags(v.Tags)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	rel := []any{}
	attachments := make([]any, 0, len(v.Attachments))
	for _, a := range v.Attachments {
		item := map[string]any{"instance_id": s(a.InstanceId), "device": s(a.Device), "state": string(a.State), "delete_on_termination": b(a.DeleteOnTermination)}
		if a.AttachTime != nil {
			item["attach_time"] = a.AttachTime.UTC()
		}
		attachments = append(attachments, item)
		if e := addRelation(c, op, "volume attachment instance id", "instance", s(a.InstanceId), awsbrowser.RelationAttachedTo, s(a.Device), at, &rel); e != nil {
			return awsbrowser.ObservedResource{}, e
		}
	}
	fields := map[string]any{"availability_zone": s(v.AvailabilityZone), "size_gib": i32(v.Size), "state": string(v.State), "volume_type": string(v.VolumeType), "encrypted": b(v.Encrypted), "iops": i32(v.Iops), "throughput_mibps": i32(v.Throughput), "snapshot_id": s(v.SnapshotId), "attachments": attachments, "tags": mappedTags, "relations": rel}
	if v.CreateTime != nil {
		fields["create_time"] = v.CreateTime.UTC()
	}
	if err = bindRelationSources(c, "volume", s(v.VolumeId), rel); err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	return observed(c, op, "volume", s(v.VolumeId), at, fields)
}

func mapLaunchTemplate(c awsbrowser.AWSContext, op string, at time.Time, value types.LaunchTemplate) (awsbrowser.ObservedResource, error) {
	mappedTags, err := tags(value.Tags)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	fields := map[string]any{
		"name":                   s(value.LaunchTemplateName),
		"created_by":             s(value.CreatedBy),
		"default_version_number": i64(value.DefaultVersionNumber),
		"latest_version_number":  i64(value.LatestVersionNumber),
		"tags":                   mappedTags,
	}
	if value.CreateTime != nil {
		fields["create_time"] = value.CreateTime.UTC()
	}
	return observed(c, op, "launch-template", s(value.LaunchTemplateId), at, fields)
}

func mapLaunchTemplateVersion(c awsbrowser.AWSContext, op string, at time.Time, value types.LaunchTemplateVersion, includeUserData bool) (awsbrowser.ObservedResource, error) {
	templateID := s(value.LaunchTemplateId)
	version := i64(value.VersionNumber)
	if templateID == "" || version < 1 || value.LaunchTemplateData == nil {
		return awsbrowser.ObservedResource{}, errInvalidEC2Query
	}
	data := value.LaunchTemplateData
	if includeUserData {
		return mapLaunchTemplateUserData(c, op, at, value)
	}
	relations := []any{}
	if err := addRelation(c, op, "launch template version parent", "launch-template", templateID, awsbrowser.RelationMemberOf, "", at, &relations); err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	if err := addRelation(c, op, "launch template image id", "image", s(data.ImageId), awsbrowser.RelationUses, "", at, &relations); err != nil {
		return awsbrowser.ObservedResource{}, err
	}

	groups := append([]string(nil), data.SecurityGroupIds...)
	networkInterfaces := make([]any, 0, len(data.NetworkInterfaces))
	for _, network := range data.NetworkInterfaces {
		groups = append(groups, network.Groups...)
		networkInterfaces = append(networkInterfaces, map[string]any{
			"device_index": i32(network.DeviceIndex), "network_interface_id": s(network.NetworkInterfaceId),
			"subnet_id": s(network.SubnetId), "security_group_ids": append([]string(nil), network.Groups...),
			"associate_public_ip": b(network.AssociatePublicIpAddress), "delete_on_termination": b(network.DeleteOnTermination),
		})
		if err := addRelation(c, op, "launch template network interface subnet id", "subnet", s(network.SubnetId), awsbrowser.RelationUses, "", at, &relations); err != nil {
			return awsbrowser.ObservedResource{}, err
		}
	}
	sort.Strings(groups)
	groups = uniqueStrings(groups)
	for _, groupID := range groups {
		if err := addRelation(c, op, "launch template security group id", "security-group", groupID, awsbrowser.RelationUses, "", at, &relations); err != nil {
			return awsbrowser.ObservedResource{}, err
		}
	}

	profileARN, profileName := "", ""
	if data.IamInstanceProfile != nil {
		profileARN, profileName = s(data.IamInstanceProfile.Arn), s(data.IamInstanceProfile.Name)
		if profileARN != "" {
			parsed, err := parseGlobalARN(c, profileARN, "iam", "instance-profile/")
			if err != nil {
				return awsbrowser.ObservedResource{}, err
			}
			pathAndName := strings.TrimPrefix(parsed.Resource, "instance-profile/")
			profileName = pathAndName[strings.LastIndex(pathAndName, "/")+1:]
		}
		if profileName != "" {
			if !iamInstanceProfileNameRE.MatchString(profileName) || len(profileName) > 128 {
				return awsbrowser.ObservedResource{}, errInvalidEC2Query
			}
			relation, err := globalRelation(c, op, "launch template iam instance profile", "iam.instance-profile", profileName, awsbrowser.RelationUses, "", at)
			if err != nil {
				return awsbrowser.ObservedResource{}, err
			}
			relations = append(relations, relation)
		}
	}

	blockDevices := make([]any, 0, len(data.BlockDeviceMappings))
	for _, block := range data.BlockDeviceMappings {
		mapped := map[string]any{"device_name": s(block.DeviceName), "virtual_name": s(block.VirtualName), "no_device": s(block.NoDevice)}
		if block.Ebs != nil {
			mapped["ebs"] = map[string]any{
				"volume_type": string(block.Ebs.VolumeType), "volume_size_gib": i32(block.Ebs.VolumeSize),
				"iops": i32(block.Ebs.Iops), "throughput_mibps": i32(block.Ebs.Throughput),
				"encrypted": b(block.Ebs.Encrypted), "snapshot_id": s(block.Ebs.SnapshotId),
				"delete_on_termination": b(block.Ebs.DeleteOnTermination),
			}
		}
		blockDevices = append(blockDevices, mapped)
	}

	metadata := map[string]any{}
	if data.MetadataOptions != nil {
		metadata = map[string]any{
			"http_endpoint": string(data.MetadataOptions.HttpEndpoint), "http_tokens": string(data.MetadataOptions.HttpTokens),
			"http_protocol_ipv6":          string(data.MetadataOptions.HttpProtocolIpv6),
			"http_put_response_hop_limit": i32(data.MetadataOptions.HttpPutResponseHopLimit),
			"instance_metadata_tags":      string(data.MetadataOptions.InstanceMetadataTags),
		}
	}
	fields := map[string]any{
		"name": s(value.LaunchTemplateName), "launch_template_id": templateID,
		"version_number": version, "default_version": b(value.DefaultVersion),
		"description": s(value.VersionDescription), "created_by": s(value.CreatedBy),
		"image_id": s(data.ImageId), "instance_type": string(data.InstanceType), "key_name": s(data.KeyName),
		"ebs_optimized": b(data.EbsOptimized), "user_data_present": strings.TrimSpace(s(data.UserData)) != "",
		"instance_profile_arn": profileARN, "instance_profile_name": profileName,
		"security_group_ids": groups, "network_interfaces": networkInterfaces,
		"block_device_mappings": blockDevices, "metadata_options": metadata, "relations": relations,
	}
	if value.CreateTime != nil {
		fields["create_time"] = value.CreateTime.UTC()
	}
	sourceID := templateID + "/" + strconv.FormatInt(version, 10)
	if err := bindRelationSources(c, "launch-template-version", sourceID, relations); err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	return observed(c, op, "launch-template-version", sourceID, at, fields)
}

func mapLaunchTemplateUserData(c awsbrowser.AWSContext, op string, at time.Time, value types.LaunchTemplateVersion) (awsbrowser.ObservedResource, error) {
	templateID := s(value.LaunchTemplateId)
	version := i64(value.VersionNumber)
	if templateID == "" || version < 1 || value.LaunchTemplateData == nil || strings.TrimSpace(s(value.LaunchTemplateData.UserData)) == "" {
		return awsbrowser.ObservedResource{}, errInvalidEC2Query
	}
	decoded, err := base64.StdEncoding.DecodeString(s(value.LaunchTemplateData.UserData))
	if err != nil || len(decoded) > 64*1024 {
		return awsbrowser.ObservedResource{}, errInvalidEC2Query
	}
	script := "(binary user data; decoded text is not valid UTF-8)"
	if utf8.Valid(decoded) {
		script = string(decoded)
	}
	sourceID := templateID + "/" + strconv.FormatInt(version, 10)
	return observed(c, op, "launch-template-user-data", sourceID, at, map[string]any{
		"name": s(value.LaunchTemplateName), "launch_template_id": templateID,
		"version_number": version, "script": script, "decoded_bytes": len(decoded),
	})
}

func uniqueStrings(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if value == "" || len(result) != 0 && result[len(result)-1] == value {
			continue
		}
		result = append(result, value)
	}
	return result
}

func permission(v types.IpPermission, direction string) map[string]any {
	cidr4 := make([]any, 0, len(v.IpRanges))
	for _, r := range v.IpRanges {
		cidr4 = append(cidr4, map[string]any{"cidr": s(r.CidrIp), "description": s(r.Description)})
	}
	cidr6 := make([]any, 0, len(v.Ipv6Ranges))
	for _, r := range v.Ipv6Ranges {
		cidr6 = append(cidr6, map[string]any{"cidr": s(r.CidrIpv6), "description": s(r.Description)})
	}
	prefixes := make([]any, 0, len(v.PrefixListIds))
	for _, r := range v.PrefixListIds {
		prefixes = append(prefixes, map[string]any{"prefix_list_id": s(r.PrefixListId), "description": s(r.Description)})
	}
	groups := make([]any, 0, len(v.UserIdGroupPairs))
	for _, r := range v.UserIdGroupPairs {
		groups = append(groups, map[string]any{"group_id": s(r.GroupId), "account_id": s(r.UserId), "vpc_id": s(r.VpcId), "description": s(r.Description)})
	}
	return map[string]any{"direction": direction, "protocol": s(v.IpProtocol), "from_port": i32(v.FromPort), "to_port": i32(v.ToPort), "ipv4_ranges": cidr4, "ipv6_ranges": cidr6, "prefix_lists": prefixes, "referenced_groups": groups}
}
func mapSecurityGroup(c awsbrowser.AWSContext, op string, at time.Time, v types.SecurityGroup) (awsbrowser.ObservedResource, error) {
	mappedTags, err := tags(v.Tags)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	rel := []any{}
	if err = addRelation(c, op, "security group vpc id", "vpc", s(v.VpcId), awsbrowser.RelationMemberOf, "", at, &rel); err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	rules := make([]any, 0, len(v.IpPermissions)+len(v.IpPermissionsEgress))
	for _, r := range v.IpPermissions {
		rules = append(rules, permission(r, "ingress"))
		for _, g := range r.UserIdGroupPairs {
			if (s(g.UserId) == "" || s(g.UserId) == c.AccountID) && s(g.VpcId) == s(v.VpcId) {
				if err = addRelation(c, op, "security group rule referenced group id", "security-group", s(g.GroupId), awsbrowser.RelationReferences, "ingress", at, &rel); err != nil {
					return awsbrowser.ObservedResource{}, err
				}
			}
		}
	}
	for _, r := range v.IpPermissionsEgress {
		rules = append(rules, permission(r, "egress"))
		for _, g := range r.UserIdGroupPairs {
			if (s(g.UserId) == "" || s(g.UserId) == c.AccountID) && s(g.VpcId) == s(v.VpcId) {
				if err = addRelation(c, op, "security group rule referenced group id", "security-group", s(g.GroupId), awsbrowser.RelationReferences, "egress", at, &rel); err != nil {
					return awsbrowser.ObservedResource{}, err
				}
			}
		}
	}
	if err = bindRelationSources(c, "security-group", s(v.GroupId), rel); err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	return observed(c, op, "security-group", s(v.GroupId), at, map[string]any{"name": s(v.GroupName), "description": s(v.Description), "owner_id": s(v.OwnerId), "vpc_id": s(v.VpcId), "rules": rules, "tags": mappedTags, "usage_scope": "EC2 only", "relations": rel})
}

func mapSecurityGroupRule(c awsbrowser.AWSContext, op string, at time.Time, v types.SecurityGroupRule) (awsbrowser.ObservedResource, error) {
	mappedTags, err := tags(v.Tags)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	rel := []any{}
	if err = addRelation(c, op, "security group rule group id", "security-group", s(v.GroupId), awsbrowser.RelationMemberOf, "", at, &rel); err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	if err = bindRelationSources(c, "security-group-rule", s(v.SecurityGroupRuleId), rel); err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	reference := map[string]any{}
	if v.ReferencedGroupInfo != nil {
		reference = map[string]any{"group_id": s(v.ReferencedGroupInfo.GroupId), "account_id": s(v.ReferencedGroupInfo.UserId), "vpc_id": s(v.ReferencedGroupInfo.VpcId)}
		accountID := s(v.ReferencedGroupInfo.UserId)
		if accountID == "" {
			accountID = c.AccountID
		}
		target, targetErr := awsbrowser.NewCanonicalResourceKey(c.Partition, accountID, c.Region, "ec2.security-group", s(v.ReferencedGroupInfo.GroupId))
		source, sourceErr := awsbrowser.NewRegionalResourceKey(c, "ec2.security-group", s(v.GroupId))
		if targetErr == nil && sourceErr == nil {
			condition := securityGroupRuleCondition(v, accountID)
			referenceRelation, relationErr := relationBetween(c, op, "security group rule referenced group id", source, target, awsbrowser.RelationReferences, condition, at)
			if relationErr != nil {
				return awsbrowser.ObservedResource{}, relationErr
			}
			rel = append(rel, referenceRelation)
		}
	}
	return observed(c, op, "security-group-rule", s(v.SecurityGroupRuleId), at, map[string]any{"group_id": s(v.GroupId), "is_egress": b(v.IsEgress), "protocol": s(v.IpProtocol), "from_port": i32(v.FromPort), "to_port": i32(v.ToPort), "cidr_ipv4": s(v.CidrIpv4), "cidr_ipv6": s(v.CidrIpv6), "prefix_list_id": s(v.PrefixListId), "referenced_group": reference, "description": s(v.Description), "tags": mappedTags, "usage_scope": "EC2 only", "relations": rel})
}

func securityGroupRuleCondition(rule types.SecurityGroupRule, accountID string) string {
	values := url.Values{}
	values.Set("direction", "ingress")
	if b(rule.IsEgress) {
		values.Set("direction", "egress")
	}
	values.Set("protocol", s(rule.IpProtocol))
	values.Set("rule-id", s(rule.SecurityGroupRuleId))
	values.Set("source-account", accountID)
	if rule.ReferencedGroupInfo != nil {
		values.Set("source-group", s(rule.ReferencedGroupInfo.GroupId))
	}
	if rule.FromPort != nil {
		values.Set("from-port", strconv.FormatInt(int64(i32(rule.FromPort)), 10))
	}
	if rule.ToPort != nil {
		values.Set("to-port", strconv.FormatInt(int64(i32(rule.ToPort)), 10))
	}
	if description := s(rule.Description); description != "" {
		values.Set("description", description)
	}
	return values.Encode()
}

func mapVpc(c awsbrowser.AWSContext, op string, at time.Time, v types.Vpc) (awsbrowser.ObservedResource, error) {
	mappedTags, err := tags(v.Tags)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	return observed(c, op, "vpc", s(v.VpcId), at, map[string]any{"cidr_block": s(v.CidrBlock), "dhcp_options_id": s(v.DhcpOptionsId), "state": string(v.State), "is_default": b(v.IsDefault), "instance_tenancy": string(v.InstanceTenancy), "owner_id": s(v.OwnerId), "tags": mappedTags})
}

func mapVpcPeeringConnection(c awsbrowser.AWSContext, op string, at time.Time, value types.VpcPeeringConnection) (awsbrowser.ObservedResource, error) {
	mappedTags, err := tags(value.Tags)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	requester, requesterResolution, err := vpcPeeringEndpoint(c, value.RequesterVpcInfo, "requester")
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	accepter, accepterResolution, err := vpcPeeringEndpoint(c, value.AccepterVpcInfo, "accepter")
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	ownerAccount, ownerRegion := c.AccountID, c.Region
	if value.RequesterVpcInfo != nil {
		if account := strings.TrimSpace(s(value.RequesterVpcInfo.OwnerId)); account != "" {
			ownerAccount = account
		}
		if region := strings.TrimSpace(s(value.RequesterVpcInfo.Region)); region != "" {
			ownerRegion = region
		}
	}
	source, err := awsbrowser.NewCanonicalResourceKey(c.Partition, ownerAccount, ownerRegion, "ec2.vpc-peering-connection", s(value.VpcPeeringConnectionId))
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	relations := []any{}
	for _, endpoint := range []struct {
		role       string
		resolution string
		key        *awsbrowser.ResourceKey
	}{{"requester", requesterResolution, requester}, {"accepter", accepterResolution, accepter}} {
		if endpoint.key == nil {
			continue
		}
		relation, relationErr := relationBetween(c, op, "vpc peering "+endpoint.role+" vpc", source, *endpoint.key, awsbrowser.RelationAssociatedWith, "role="+endpoint.role, at)
		if relationErr != nil {
			return awsbrowser.ObservedResource{}, relationErr
		}
		relations = append(relations, relation)
	}
	statusCode, statusMessage := "", ""
	if value.Status != nil {
		statusCode = string(value.Status.Code)
		statusMessage = s(value.Status.Message)
	}
	fields := map[string]any{
		"name": mappedTags["Name"], "status": statusCode, "status_message": statusMessage,
		"requester": vpcPeeringInfoFields(value.RequesterVpcInfo), "requester_resolution": requesterResolution,
		"accepter": vpcPeeringInfoFields(value.AccepterVpcInfo), "accepter_resolution": accepterResolution,
		"tags": mappedTags, "relations": relations,
	}
	observation, err := awsbrowser.NewResourceObservationForOperation(c, op, fields, at, true)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	return awsbrowser.ObservedResource{Key: source, Observation: observation}, nil
}

func vpcPeeringEndpoint(c awsbrowser.AWSContext, info *types.VpcPeeringConnectionVpcInfo, role string) (*awsbrowser.ResourceKey, string, error) {
	if info == nil {
		return nil, "unresolved:missing-" + role + "-vpc-info", nil
	}
	account, region, id := strings.TrimSpace(s(info.OwnerId)), strings.TrimSpace(s(info.Region)), strings.TrimSpace(s(info.VpcId))
	missing := make([]string, 0, 3)
	if account == "" {
		missing = append(missing, "owner-id")
	}
	if region == "" {
		missing = append(missing, "region")
	}
	if id == "" {
		missing = append(missing, "vpc-id")
	}
	if len(missing) != 0 {
		return nil, "unresolved:missing-" + strings.Join(missing, "+"), nil
	}
	key, err := awsbrowser.NewCanonicalResourceKey(c.Partition, account, region, "ec2.vpc", id)
	if err != nil {
		return nil, "", err
	}
	return &key, "exact", nil
}

func vpcPeeringInfoFields(info *types.VpcPeeringConnectionVpcInfo) map[string]any {
	if info == nil {
		return map[string]any{}
	}
	return map[string]any{"owner_id": s(info.OwnerId), "region": s(info.Region), "vpc_id": s(info.VpcId), "cidr_block": s(info.CidrBlock)}
}
func mapSubnet(c awsbrowser.AWSContext, op string, at time.Time, v types.Subnet) (awsbrowser.ObservedResource, error) {
	mappedTags, err := tags(v.Tags)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	rel := []any{}
	if err = addRelation(c, op, "subnet vpc id", "vpc", s(v.VpcId), awsbrowser.RelationMemberOf, "", at, &rel); err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	if err = bindRelationSources(c, "subnet", s(v.SubnetId), rel); err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	return observed(c, op, "subnet", s(v.SubnetId), at, map[string]any{"vpc_id": s(v.VpcId), "availability_zone": s(v.AvailabilityZone), "availability_zone_id": s(v.AvailabilityZoneId), "cidr_block": s(v.CidrBlock), "state": string(v.State), "available_ip_address_count": i32(v.AvailableIpAddressCount), "default_for_az": b(v.DefaultForAz), "map_public_ip_on_launch": b(v.MapPublicIpOnLaunch), "ipv6_native": b(v.Ipv6Native), "owner_id": s(v.OwnerId), "tags": mappedTags, "relations": rel})
}

func mapRouteTable(c awsbrowser.AWSContext, op string, at time.Time, v types.RouteTable) (awsbrowser.ObservedResource, error) {
	mappedTags, err := tags(v.Tags)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	rel := []any{}
	if err = addRelation(c, op, "route table vpc id", "vpc", s(v.VpcId), awsbrowser.RelationMemberOf, "", at, &rel); err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	associations := make([]any, 0, len(v.Associations))
	for _, a := range v.Associations {
		associations = append(associations, map[string]any{"association_id": s(a.RouteTableAssociationId), "subnet_id": s(a.SubnetId), "gateway_id": s(a.GatewayId), "main": b(a.Main)})
		if err = addRelation(c, op, "route table association subnet id", "subnet", s(a.SubnetId), awsbrowser.RelationAssociatedWith, "", at, &rel); err != nil {
			return awsbrowser.ObservedResource{}, err
		}
	}
	routes := make([]any, 0, len(v.Routes))
	for _, r := range v.Routes {
		routes = append(routes, map[string]any{"destination_cidr_block": s(r.DestinationCidrBlock), "destination_ipv6_cidr_block": s(r.DestinationIpv6CidrBlock), "destination_prefix_list_id": s(r.DestinationPrefixListId), "gateway_id": s(r.GatewayId), "egress_only_internet_gateway_id": s(r.EgressOnlyInternetGatewayId), "carrier_gateway_id": s(r.CarrierGatewayId), "local_gateway_id": s(r.LocalGatewayId), "core_network_arn": s(r.CoreNetworkArn), "instance_id": s(r.InstanceId), "nat_gateway_id": s(r.NatGatewayId), "network_interface_id": s(r.NetworkInterfaceId), "transit_gateway_id": s(r.TransitGatewayId), "vpc_peering_connection_id": s(r.VpcPeeringConnectionId), "state": string(r.State), "origin": string(r.Origin)})
		gatewayID := s(r.GatewayId)
		if strings.EqualFold(strings.TrimSpace(gatewayID), "local") {
			gatewayID = ""
		}
		condition := routeCondition(r)
		for _, target := range []struct{ reason, kind, id string }{
			{"route target instance id", "instance", s(r.InstanceId)},
			{"route target gateway id", "gateway", gatewayID},
			{"route target egress-only internet gateway id", "egress-only-internet-gateway", s(r.EgressOnlyInternetGatewayId)},
			{"route target carrier gateway id", "carrier-gateway", s(r.CarrierGatewayId)},
			{"route target local gateway id", "local-gateway", s(r.LocalGatewayId)},
			{"route target nat gateway id", "nat-gateway", s(r.NatGatewayId)},
			{"route target network interface id", "network-interface", s(r.NetworkInterfaceId)},
			{"route target transit gateway id", "transit-gateway", s(r.TransitGatewayId)},
			{"route target vpc peering connection id", "vpc-peering-connection", s(r.VpcPeeringConnectionId)},
		} {
			if err = addRelation(c, op, target.reason, target.kind, target.id, awsbrowser.RelationRoutesTo, condition, at, &rel); err != nil {
				return awsbrowser.ObservedResource{}, err
			}
		}
		if s(r.CoreNetworkArn) != "" {
			parsed, parseErr := parseGlobalARN(c, s(r.CoreNetworkArn), "networkmanager", "core-network/")
			if parseErr != nil {
				return awsbrowser.ObservedResource{}, parseErr
			}
			coreRelation, relationErr := globalRelation(c, op, "route target core network arn", "networkmanager.core-network", parsed.String(), awsbrowser.RelationRoutesTo, condition, at)
			if relationErr != nil {
				return awsbrowser.ObservedResource{}, relationErr
			}
			rel = append(rel, coreRelation)
		}
	}
	if err = bindRelationSources(c, "route-table", s(v.RouteTableId), rel); err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	return observed(c, op, "route-table", s(v.RouteTableId), at, map[string]any{"vpc_id": s(v.VpcId), "owner_id": s(v.OwnerId), "associations": associations, "routes": routes, "tags": mappedTags, "relations": rel})
}

func routeCondition(route types.Route) string {
	return stringsJoinNonEmpty(s(route.DestinationCidrBlock), s(route.DestinationIpv6CidrBlock), s(route.DestinationPrefixListId))
}

func stringsJoinNonEmpty(values ...string) string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return strings.Join(result, " ")
}

var _ awsbrowser.QueryExecutor = (*EC2QueryExecutor)(nil)
