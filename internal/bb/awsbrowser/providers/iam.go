package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
)

const iamMaxItems int32 = 1000

var ErrInvalidIAMExecutor = errors.New("IAM query executor requires an API and clock")

var (
	iamNameRE    = regexp.MustCompile(`^[A-Za-z0-9_+=,.@-]+$`)
	iamVersionRE = regexp.MustCompile(`^v[1-9][0-9]*(?:\.[A-Za-z0-9-]*)?$`)
)

// IAMQueryExecutor maps the deliberately narrowed, read-only IAMAPI surface
// into credential-free query pages. Operation selection remains lazy: Execute
// invokes only the method named by the query key.
type IAMQueryExecutor struct {
	api   awsbrowser.IAMAPI
	clock func() time.Time
}

func NewIAM(api awsbrowser.IAMAPI, clock func() time.Time) (awsbrowser.QueryExecutor, error) {
	if api == nil || clock == nil {
		return nil, ErrInvalidIAMExecutor
	}
	return &IAMQueryExecutor{api: api, clock: clock}, nil
}

func (executor *IAMQueryExecutor) Execute(ctx context.Context, key awsbrowser.QueryKey, sink awsbrowser.QueryPageSink) error {
	if executor == nil || executor.api == nil || executor.clock == nil || sink == nil || ctx == nil {
		return ErrInvalidIAMExecutor
	}
	if err := key.Validate(); err != nil {
		return err
	}
	if key.Provider != awsbrowser.ProviderIAM || awsbrowser.ValidateProviderOperation(key.Provider, key.Operation) != nil {
		return awsbrowser.ErrInvalidProviderOperation
	}
	params, err := iamParams(key)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	switch key.Operation {
	case awsbrowser.OperationListRoles:
		return executor.listRoles(ctx, key, params, sink)
	case awsbrowser.OperationGetInstanceProfile:
		return executor.getInstanceProfile(ctx, key, params, sink)
	case awsbrowser.OperationGetRole:
		return executor.getRole(ctx, key, params, sink)
	case awsbrowser.OperationListAttachedRolePolicies:
		return executor.listAttachedRolePolicies(ctx, key, params, sink)
	case awsbrowser.OperationListRolePolicies:
		return executor.listRolePolicies(ctx, key, params, sink)
	case awsbrowser.OperationGetPolicy:
		return executor.getPolicy(ctx, key, params, sink)
	case awsbrowser.OperationGetPolicyVersion:
		return executor.getPolicyVersion(ctx, key, params, sink)
	case awsbrowser.OperationGetRolePolicy:
		return executor.getRolePolicy(ctx, key, params, sink)
	default:
		return awsbrowser.ErrInvalidProviderOperation
	}
}

func iamParams(key awsbrowser.QueryKey) (map[string]string, error) {
	values, err := url.ParseQuery(key.ParamsKey)
	if err != nil {
		return nil, awsbrowser.ErrInvalidQueryKey
	}
	params := make(map[string]string, len(values))
	for name, items := range values {
		if len(items) != 1 {
			return nil, awsbrowser.ErrInvalidQueryKey
		}
		params[name] = items[0]
	}
	return params, nil
}

func validateIAMParams(params map[string]string, required []string, optional ...string) error {
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, name := range required {
		allowed[name] = struct{}{}
		if strings.TrimSpace(params[name]) == "" {
			return awsbrowser.ErrInvalidQueryKey
		}
	}
	for _, name := range optional {
		allowed[name] = struct{}{}
	}
	for name := range params {
		if _, ok := allowed[name]; !ok {
			return awsbrowser.ErrInvalidQueryKey
		}
	}
	return nil
}

func validIAMName(value string, limit int) bool {
	return value == strings.TrimSpace(value) && len(value) <= limit && iamNameRE.MatchString(value)
}

func validIAMPath(value string) bool {
	if len(value) == 0 || len(value) > 512 || value[0] != '/' || value[len(value)-1] != '/' {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7f {
			return false
		}
	}
	return true
}

func validIAMPolicyARN(key awsbrowser.QueryKey, value string) bool {
	parsed, err := arn.Parse(value)
	if err != nil || parsed.Partition != key.Context.Partition || parsed.Service != awsbrowser.ProviderIAM ||
		parsed.Region != "" || (parsed.AccountID != key.Context.AccountID && parsed.AccountID != "aws") {
		return false
	}
	return strings.HasPrefix(parsed.Resource, "policy/") && len(parsed.Resource) > len("policy/")
}

func (executor *IAMQueryExecutor) listRoles(ctx context.Context, key awsbrowser.QueryKey, params map[string]string, sink awsbrowser.QueryPageSink) error {
	if err := validateIAMParams(params, nil, "path-prefix"); err != nil {
		return err
	}
	if path := params["path-prefix"]; path != "" && !validIAMPath(path) {
		return awsbrowser.ErrInvalidQueryKey
	}
	input := &iam.ListRolesInput{MaxItems: aws.Int32(iamMaxItems)}
	if path := params["path-prefix"]; path != "" {
		input.PathPrefix = aws.String(path)
	}
	return executor.paginate(ctx, key, sink, func() ([]awsbrowser.ObservedResource, bool, *string, time.Time, error) {
		output, err := executor.api.ListRoles(ctx, input)
		if err != nil {
			return nil, false, nil, time.Time{}, err
		}
		if output == nil {
			return nil, false, nil, time.Time{}, awsbrowser.ErrQueryDecode
		}
		fetchedAt := executor.clock().UTC()
		resources := make([]awsbrowser.ObservedResource, 0, len(output.Roles))
		for _, role := range output.Roles {
			if err := ctx.Err(); err != nil {
				return nil, false, nil, time.Time{}, err
			}
			resource, err := mapIAMRole(key, role, fetchedAt)
			if err != nil {
				return nil, false, nil, time.Time{}, err
			}
			resources = append(resources, resource)
		}
		input.Marker = output.Marker
		return resources, output.IsTruncated, output.Marker, fetchedAt, nil
	})
}

func (executor *IAMQueryExecutor) getInstanceProfile(ctx context.Context, key awsbrowser.QueryKey, params map[string]string, sink awsbrowser.QueryPageSink) error {
	if err := validateIAMParams(params, []string{"instance-profile-name"}); err != nil {
		return err
	}
	if !validIAMName(params["instance-profile-name"], 128) {
		return awsbrowser.ErrInvalidQueryKey
	}
	output, err := executor.api.GetInstanceProfile(ctx, &iam.GetInstanceProfileInput{InstanceProfileName: aws.String(params["instance-profile-name"])})
	if err != nil {
		return awsbrowser.ClassifyProviderError(err, awsbrowser.ProviderIAM, key.Operation)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if output == nil || output.InstanceProfile == nil {
		return awsbrowser.ErrQueryDecode
	}
	if aws.ToString(output.InstanceProfile.InstanceProfileName) != params["instance-profile-name"] {
		return awsbrowser.ErrQueryDecode
	}
	fetchedAt := executor.clock().UTC()
	resources, err := mapIAMInstanceProfile(key, *output.InstanceProfile, fetchedAt)
	if err != nil {
		return err
	}
	return emitIAMPage(ctx, sink, 0, resources, fetchedAt, true)
}

func (executor *IAMQueryExecutor) getRole(ctx context.Context, key awsbrowser.QueryKey, params map[string]string, sink awsbrowser.QueryPageSink) error {
	if err := validateIAMParams(params, []string{"role-name"}); err != nil {
		return err
	}
	if !validIAMName(params["role-name"], 64) {
		return awsbrowser.ErrInvalidQueryKey
	}
	output, err := executor.api.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(params["role-name"])})
	if err != nil {
		if awsbrowser.IsProviderNotFound(err) {
			return completeIAM(ctx, sink, executor.clock().UTC())
		}
		return awsbrowser.ClassifyProviderError(err, awsbrowser.ProviderIAM, key.Operation)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if output == nil || output.Role == nil {
		return awsbrowser.ErrQueryDecode
	}
	if aws.ToString(output.Role.RoleName) != params["role-name"] {
		return awsbrowser.ErrQueryDecode
	}
	fetchedAt := executor.clock().UTC()
	resource, err := mapIAMRole(key, *output.Role, fetchedAt)
	if err != nil {
		return err
	}
	return emitIAMPage(ctx, sink, 0, []awsbrowser.ObservedResource{resource}, fetchedAt, true)
}

func (executor *IAMQueryExecutor) listAttachedRolePolicies(ctx context.Context, key awsbrowser.QueryKey, params map[string]string, sink awsbrowser.QueryPageSink) error {
	if err := validateIAMParams(params, []string{"role-name"}, "path-prefix"); err != nil {
		return err
	}
	if !validIAMName(params["role-name"], 64) || params["path-prefix"] != "" && !validIAMPath(params["path-prefix"]) {
		return awsbrowser.ErrInvalidQueryKey
	}
	input := &iam.ListAttachedRolePoliciesInput{RoleName: aws.String(params["role-name"]), MaxItems: aws.Int32(iamMaxItems)}
	if path := params["path-prefix"]; path != "" {
		input.PathPrefix = aws.String(path)
	}
	return executor.paginate(ctx, key, sink, func() ([]awsbrowser.ObservedResource, bool, *string, time.Time, error) {
		output, err := executor.api.ListAttachedRolePolicies(ctx, input)
		if err != nil {
			return nil, false, nil, time.Time{}, err
		}
		if output == nil {
			return nil, false, nil, time.Time{}, awsbrowser.ErrQueryDecode
		}
		fetchedAt := executor.clock().UTC()
		resources := make([]awsbrowser.ObservedResource, 0, len(output.AttachedPolicies))
		for _, policy := range output.AttachedPolicies {
			if err := ctx.Err(); err != nil {
				return nil, false, nil, time.Time{}, err
			}
			resource, err := mapIAMAttachedPolicy(key, params["role-name"], policy, fetchedAt)
			if err != nil {
				return nil, false, nil, time.Time{}, err
			}
			resources = append(resources, resource)
		}
		input.Marker = output.Marker
		return resources, output.IsTruncated, output.Marker, fetchedAt, nil
	})
}

func (executor *IAMQueryExecutor) listRolePolicies(ctx context.Context, key awsbrowser.QueryKey, params map[string]string, sink awsbrowser.QueryPageSink) error {
	if err := validateIAMParams(params, []string{"role-name"}); err != nil {
		return err
	}
	if !validIAMName(params["role-name"], 64) {
		return awsbrowser.ErrInvalidQueryKey
	}
	input := &iam.ListRolePoliciesInput{RoleName: aws.String(params["role-name"]), MaxItems: aws.Int32(iamMaxItems)}
	return executor.paginate(ctx, key, sink, func() ([]awsbrowser.ObservedResource, bool, *string, time.Time, error) {
		output, err := executor.api.ListRolePolicies(ctx, input)
		if err != nil {
			return nil, false, nil, time.Time{}, err
		}
		if output == nil {
			return nil, false, nil, time.Time{}, awsbrowser.ErrQueryDecode
		}
		fetchedAt := executor.clock().UTC()
		resources := make([]awsbrowser.ObservedResource, 0, len(output.PolicyNames))
		for _, policyName := range output.PolicyNames {
			if err := ctx.Err(); err != nil {
				return nil, false, nil, time.Time{}, err
			}
			resource, err := mapIAMInlinePolicy(key, params["role-name"], policyName, nil, fetchedAt)
			if err != nil {
				return nil, false, nil, time.Time{}, err
			}
			resources = append(resources, resource)
		}
		input.Marker = output.Marker
		return resources, output.IsTruncated, output.Marker, fetchedAt, nil
	})
}

func (executor *IAMQueryExecutor) getPolicy(ctx context.Context, key awsbrowser.QueryKey, params map[string]string, sink awsbrowser.QueryPageSink) error {
	if err := validateIAMParams(params, []string{"policy-arn"}); err != nil {
		return err
	}
	if !validIAMPolicyARN(key, params["policy-arn"]) {
		return awsbrowser.ErrInvalidQueryKey
	}
	output, err := executor.api.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: aws.String(params["policy-arn"])})
	if err != nil {
		return awsbrowser.ClassifyProviderError(err, awsbrowser.ProviderIAM, key.Operation)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if output == nil || output.Policy == nil {
		return awsbrowser.ErrQueryDecode
	}
	if aws.ToString(output.Policy.Arn) != params["policy-arn"] {
		return awsbrowser.ErrQueryDecode
	}
	fetchedAt := executor.clock().UTC()
	resource, err := mapIAMPolicy(key, *output.Policy, fetchedAt)
	if err != nil {
		return err
	}
	return emitIAMPage(ctx, sink, 0, []awsbrowser.ObservedResource{resource}, fetchedAt, true)
}

func (executor *IAMQueryExecutor) getPolicyVersion(ctx context.Context, key awsbrowser.QueryKey, params map[string]string, sink awsbrowser.QueryPageSink) error {
	if err := validateIAMParams(params, []string{"policy-arn", "version-id"}); err != nil {
		return err
	}
	if !validIAMPolicyARN(key, params["policy-arn"]) || !iamVersionRE.MatchString(params["version-id"]) {
		return awsbrowser.ErrInvalidQueryKey
	}
	output, err := executor.api.GetPolicyVersion(ctx, &iam.GetPolicyVersionInput{
		PolicyArn: aws.String(params["policy-arn"]), VersionId: aws.String(params["version-id"]),
	})
	if err != nil {
		return awsbrowser.ClassifyProviderError(err, awsbrowser.ProviderIAM, key.Operation)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if output == nil || output.PolicyVersion == nil {
		return awsbrowser.ErrQueryDecode
	}
	if aws.ToString(output.PolicyVersion.VersionId) != params["version-id"] {
		return awsbrowser.ErrQueryDecode
	}
	fetchedAt := executor.clock().UTC()
	resource, err := mapIAMPolicyVersion(key, params["policy-arn"], params["version-id"], *output.PolicyVersion, fetchedAt)
	if err != nil {
		return err
	}
	return emitIAMPage(ctx, sink, 0, []awsbrowser.ObservedResource{resource}, fetchedAt, true)
}

func (executor *IAMQueryExecutor) getRolePolicy(ctx context.Context, key awsbrowser.QueryKey, params map[string]string, sink awsbrowser.QueryPageSink) error {
	if err := validateIAMParams(params, []string{"role-name", "policy-name"}); err != nil {
		return err
	}
	if !validIAMName(params["role-name"], 64) || !validIAMName(params["policy-name"], 128) {
		return awsbrowser.ErrInvalidQueryKey
	}
	output, err := executor.api.GetRolePolicy(ctx, &iam.GetRolePolicyInput{
		RoleName: aws.String(params["role-name"]), PolicyName: aws.String(params["policy-name"]),
	})
	if err != nil {
		return awsbrowser.ClassifyProviderError(err, awsbrowser.ProviderIAM, key.Operation)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if output == nil || output.RoleName == nil || output.PolicyName == nil || output.PolicyDocument == nil {
		return awsbrowser.ErrQueryDecode
	}
	if *output.RoleName != params["role-name"] || *output.PolicyName != params["policy-name"] {
		return awsbrowser.ErrQueryDecode
	}
	document, err := decodeIAMDocument(*output.PolicyDocument)
	if err != nil {
		return err
	}
	fetchedAt := executor.clock().UTC()
	resource, err := mapIAMInlinePolicy(key, *output.RoleName, *output.PolicyName, document, fetchedAt)
	if err != nil {
		return err
	}
	return emitIAMPage(ctx, sink, 0, []awsbrowser.ObservedResource{resource}, fetchedAt, true)
}

func (executor *IAMQueryExecutor) paginate(ctx context.Context, key awsbrowser.QueryKey, sink awsbrowser.QueryPageSink, fetch func() ([]awsbrowser.ObservedResource, bool, *string, time.Time, error)) error {
	seen := make(map[string]struct{})
	var pageNumber uint64
	var fetchedAt time.Time
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		resources, truncated, marker, pageFetchedAt, err := fetch()
		if err != nil {
			if errors.Is(err, awsbrowser.ErrQueryDecode) || errors.Is(err, awsbrowser.ErrIncompletePage) || errors.Is(err, awsbrowser.ErrInvalidMappedFields) {
				return err
			}
			return awsbrowser.ClassifyProviderError(err, awsbrowser.ProviderIAM, key.Operation)
		}
		fetchedAt = pageFetchedAt
		var cursor string
		if truncated {
			cursor = strings.TrimSpace(aws.ToString(marker))
			if cursor == "" {
				return awsbrowser.ErrQueryDecode
			}
			if _, exists := seen[cursor]; exists {
				return awsbrowser.ErrQueryDecode
			}
			seen[cursor] = struct{}{}
		}
		if err := emitIAMPage(ctx, sink, pageNumber, resources, fetchedAt, false); err != nil {
			return err
		}
		pageNumber++
		if !truncated {
			return completeIAM(ctx, sink, fetchedAt)
		}
	}
}

func emitIAMPage(ctx context.Context, sink awsbrowser.QueryPageSink, number uint64, resources []awsbrowser.ObservedResource, fetchedAt time.Time, complete bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	page, err := awsbrowser.NewQueryPage(number, resources, fetchedAt, true)
	if err != nil {
		return err
	}
	if err := sink.Page(page); err != nil {
		return err
	}
	if complete {
		return completeIAM(ctx, sink, fetchedAt)
	}
	return nil
}

func completeIAM(ctx context.Context, sink awsbrowser.QueryPageSink, fetchedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return sink.Complete(fetchedAt)
}

func mapIAMRole(key awsbrowser.QueryKey, role iamtypes.Role, fetchedAt time.Time) (awsbrowser.ObservedResource, error) {
	roleName := strings.TrimSpace(aws.ToString(role.RoleName))
	if roleName == "" || aws.ToString(role.RoleId) == "" || aws.ToString(role.Arn) == "" || role.Path == nil || role.CreateDate == nil {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	tags, err := mapIAMTags(role.Tags)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	resourceKey, err := awsbrowser.NewGlobalResourceKey(key.Context, "iam.role", roleName)
	if err != nil {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	fields := map[string]any{
		"role_name": roleName, "role_id": aws.ToString(role.RoleId), "arn": aws.ToString(role.Arn),
		"path": aws.ToString(role.Path), "description": aws.ToString(role.Description),
		"max_session_duration": aws.ToInt32(role.MaxSessionDuration), "tags": tags,
	}
	fields["create_date"] = role.CreateDate.UTC()
	if role.AssumeRolePolicyDocument != nil {
		document, err := decodeIAMDocument(*role.AssumeRolePolicyDocument)
		if err != nil {
			return awsbrowser.ObservedResource{}, err
		}
		fields["trust_policy_document"] = document
	}
	observation, err := awsbrowser.NewResourceObservationForOperation(key.Context, key.Operation, fields, fetchedAt, true)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	return awsbrowser.ObservedResource{Key: resourceKey, Observation: observation}, nil
}

func mapIAMInstanceProfile(key awsbrowser.QueryKey, profile iamtypes.InstanceProfile, fetchedAt time.Time) ([]awsbrowser.ObservedResource, error) {
	profileName := strings.TrimSpace(aws.ToString(profile.InstanceProfileName))
	if profileName == "" || aws.ToString(profile.InstanceProfileId) == "" || aws.ToString(profile.Arn) == "" ||
		profile.Path == nil || profile.CreateDate == nil {
		return nil, awsbrowser.ErrQueryDecode
	}
	tags, err := mapIAMTags(profile.Tags)
	if err != nil {
		return nil, err
	}
	profileKey, err := awsbrowser.NewGlobalResourceKey(key.Context, "iam.instance-profile", profileName)
	if err != nil {
		return nil, awsbrowser.ErrQueryDecode
	}
	resources := make([]awsbrowser.ObservedResource, 0, len(profile.Roles)+1)
	roleKeys := make([]awsbrowser.ResourceKey, 0, len(profile.Roles))
	relations := make([]any, 0, len(profile.Roles))
	for _, role := range profile.Roles {
		resource, err := mapIAMRole(key, role, fetchedAt)
		if err != nil {
			return nil, err
		}
		roleKeys = append(roleKeys, resource.Key)
		relation, err := exactIAMRelation(profileKey, resource.Key, key.Operation, "instance-profile-role", fetchedAt)
		if err != nil {
			return nil, err
		}
		relations = append(relations, relation)
		resources = append(resources, resource)
	}
	fields := map[string]any{
		"instance_profile_name": profileName, "instance_profile_id": aws.ToString(profile.InstanceProfileId),
		"arn": aws.ToString(profile.Arn), "path": aws.ToString(profile.Path), "tags": tags,
		"role_keys": roleKeys, "relations": relations,
	}
	fields["create_date"] = profile.CreateDate.UTC()
	observation, err := awsbrowser.NewResourceObservationForOperation(key.Context, key.Operation, fields, fetchedAt, true)
	if err != nil {
		return nil, err
	}
	profileResource := awsbrowser.ObservedResource{Key: profileKey, Observation: observation}
	return append([]awsbrowser.ObservedResource{profileResource}, resources...), nil
}

func mapIAMAttachedPolicy(key awsbrowser.QueryKey, roleName string, policy iamtypes.AttachedPolicy, fetchedAt time.Time) (awsbrowser.ObservedResource, error) {
	policyARN := strings.TrimSpace(aws.ToString(policy.PolicyArn))
	if policyARN == "" {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	policyKey, err := awsbrowser.NewGlobalResourceKey(key.Context, "iam.managed-policy", policyARN)
	if err != nil {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	roleKey, err := awsbrowser.NewGlobalResourceKey(key.Context, "iam.role", roleName)
	if err != nil {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	relation, err := exactIAMRelation(roleKey, policyKey, key.Operation, "role-attached-policy", fetchedAt)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	fields := map[string]any{
		"policy_arn": policyARN, "policy_name": aws.ToString(policy.PolicyName), "role_key": roleKey,
		"relations": []any{relation},
	}
	observation, err := awsbrowser.NewResourceObservationForOperation(key.Context, key.Operation, fields, fetchedAt, true)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	return awsbrowser.ObservedResource{Key: policyKey, Observation: observation}, nil
}

func mapIAMInlinePolicy(key awsbrowser.QueryKey, roleName, policyName string, document any, fetchedAt time.Time) (awsbrowser.ObservedResource, error) {
	roleName, policyName = strings.TrimSpace(roleName), strings.TrimSpace(policyName)
	if roleName == "" || policyName == "" {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	policyKey, err := awsbrowser.NewGlobalResourceKey(key.Context, "iam.inline-policy", roleName+":"+policyName)
	if err != nil {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	roleKey, err := awsbrowser.NewGlobalResourceKey(key.Context, "iam.role", roleName)
	if err != nil {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	relation, err := exactIAMRelation(roleKey, policyKey, key.Operation, "role-inline-policy", fetchedAt)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	fields := map[string]any{
		"policy_name": policyName, "role_name": roleName, "role_key": roleKey,
		"relations": []any{relation},
	}
	if document != nil {
		fields["policy_document"] = document
	}
	observation, err := awsbrowser.NewResourceObservationForOperation(key.Context, key.Operation, fields, fetchedAt, true)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	return awsbrowser.ObservedResource{Key: policyKey, Observation: observation}, nil
}

func mapIAMPolicy(key awsbrowser.QueryKey, policy iamtypes.Policy, fetchedAt time.Time) (awsbrowser.ObservedResource, error) {
	policyARN := strings.TrimSpace(aws.ToString(policy.Arn))
	if policyARN == "" {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	tags, err := mapIAMTags(policy.Tags)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	policyKey, err := awsbrowser.NewGlobalResourceKey(key.Context, "iam.managed-policy", policyARN)
	if err != nil {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	fields := map[string]any{
		"policy_arn": policyARN, "policy_name": aws.ToString(policy.PolicyName), "policy_id": aws.ToString(policy.PolicyId),
		"path": aws.ToString(policy.Path), "description": aws.ToString(policy.Description),
		"default_version_id": aws.ToString(policy.DefaultVersionId), "attachment_count": aws.ToInt32(policy.AttachmentCount),
		"permissions_boundary_usage_count": aws.ToInt32(policy.PermissionsBoundaryUsageCount),
		"is_attachable":                    policy.IsAttachable, "tags": tags,
	}
	if policy.CreateDate != nil {
		fields["create_date"] = policy.CreateDate.UTC()
	}
	if policy.UpdateDate != nil {
		fields["update_date"] = policy.UpdateDate.UTC()
	}
	observation, err := awsbrowser.NewResourceObservationForOperation(key.Context, key.Operation, fields, fetchedAt, true)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	return awsbrowser.ObservedResource{Key: policyKey, Observation: observation}, nil
}

func mapIAMPolicyVersion(key awsbrowser.QueryKey, policyARN, versionID string, version iamtypes.PolicyVersion, fetchedAt time.Time) (awsbrowser.ObservedResource, error) {
	policyKey, err := awsbrowser.NewGlobalResourceKey(key.Context, "iam.managed-policy", policyARN)
	if err != nil || version.Document == nil || aws.ToString(version.VersionId) != versionID {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	versionKey, err := awsbrowser.NewGlobalResourceKey(key.Context, "iam.managed-policy-version", policyARN+":"+versionID)
	if err != nil {
		return awsbrowser.ObservedResource{}, awsbrowser.ErrQueryDecode
	}
	document, err := decodeIAMDocument(*version.Document)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	relation, err := exactIAMRelation(policyKey, versionKey, key.Operation, "managed-policy-version", fetchedAt)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	fields := map[string]any{
		"policy_arn": policyARN, "version_id": versionID, "policy_key": policyKey,
		"is_default_version": version.IsDefaultVersion, "policy_document": document, "relations": []any{relation},
	}
	if version.CreateDate != nil {
		fields["create_date"] = version.CreateDate.UTC()
	}
	observation, err := awsbrowser.NewResourceObservationForOperation(key.Context, key.Operation, fields, fetchedAt, true)
	if err != nil {
		return awsbrowser.ObservedResource{}, err
	}
	return awsbrowser.ObservedResource{Key: versionKey, Observation: observation}, nil
}

func decodeIAMDocument(encoded string) (string, error) {
	decoded, err := url.QueryUnescape(encoded)
	if err != nil {
		return "", awsbrowser.ErrQueryDecode
	}
	decoder := json.NewDecoder(strings.NewReader(decoded))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return "", awsbrowser.ErrQueryDecode
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", awsbrowser.ErrQueryDecode
	}
	switch document.(type) {
	case map[string]any, []any:
	default:
		return "", awsbrowser.ErrQueryDecode
	}
	canonical, err := json.Marshal(document)
	if err != nil {
		return "", awsbrowser.ErrQueryDecode
	}
	return string(canonical), nil
}

func mapIAMTags(tags []iamtypes.Tag) (map[string]string, error) {
	result := make(map[string]string, len(tags))
	for _, tag := range tags {
		if tag.Key == nil || tag.Value == nil || strings.TrimSpace(*tag.Key) == "" {
			return nil, awsbrowser.ErrQueryDecode
		}
		if _, duplicate := result[*tag.Key]; duplicate {
			return nil, awsbrowser.ErrQueryDecode
		}
		result[*tag.Key] = *tag.Value
	}
	return result, nil
}

func exactIAMRelation(source, target awsbrowser.ResourceKey, operation, reason string, observedAt time.Time) (map[string]any, error) {
	evidence, err := awsbrowser.NewRelationEvidence(awsbrowser.RelationAPIExact, reason, operation, awsbrowser.GlobalRegion, observedAt)
	if err != nil {
		return nil, err
	}
	relation, err := awsbrowser.NewRelation(source, target, evidence)
	if err != nil {
		return nil, err
	}
	validated := relation.Evidence()[0]
	return map[string]any{
		"source": relation.Source, "target": relation.Target, "kind": string(validated.Kind),
		"reason": validated.Reason, "operation": validated.Operation, "scope": validated.Scope, "observed_at": validated.ObservedAt,
	}, nil
}

var _ awsbrowser.QueryExecutor = (*IAMQueryExecutor)(nil)
