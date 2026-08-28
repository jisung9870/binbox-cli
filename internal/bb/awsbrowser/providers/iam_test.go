package providers

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/smithy-go"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
)

type fakeIAM struct {
	mu    sync.Mutex
	calls []string

	listRoles                func(context.Context, *iam.ListRolesInput) (*iam.ListRolesOutput, error)
	getInstanceProfile       func(context.Context, *iam.GetInstanceProfileInput) (*iam.GetInstanceProfileOutput, error)
	getRole                  func(context.Context, *iam.GetRoleInput) (*iam.GetRoleOutput, error)
	listAttachedRolePolicies func(context.Context, *iam.ListAttachedRolePoliciesInput) (*iam.ListAttachedRolePoliciesOutput, error)
	listRolePolicies         func(context.Context, *iam.ListRolePoliciesInput) (*iam.ListRolePoliciesOutput, error)
	getPolicy                func(context.Context, *iam.GetPolicyInput) (*iam.GetPolicyOutput, error)
	getPolicyVersion         func(context.Context, *iam.GetPolicyVersionInput) (*iam.GetPolicyVersionOutput, error)
	getRolePolicy            func(context.Context, *iam.GetRolePolicyInput) (*iam.GetRolePolicyOutput, error)
}

func (fake *fakeIAM) called(operation string) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, operation)
}

func (fake *fakeIAM) ListRoles(ctx context.Context, input *iam.ListRolesInput) (*iam.ListRolesOutput, error) {
	fake.called(awsbrowser.OperationListRoles)
	return fake.listRoles(ctx, input)
}

func (fake *fakeIAM) GetInstanceProfile(ctx context.Context, input *iam.GetInstanceProfileInput) (*iam.GetInstanceProfileOutput, error) {
	fake.called(awsbrowser.OperationGetInstanceProfile)
	return fake.getInstanceProfile(ctx, input)
}

func (fake *fakeIAM) GetRole(ctx context.Context, input *iam.GetRoleInput) (*iam.GetRoleOutput, error) {
	fake.called(awsbrowser.OperationGetRole)
	return fake.getRole(ctx, input)
}

func (fake *fakeIAM) ListAttachedRolePolicies(ctx context.Context, input *iam.ListAttachedRolePoliciesInput) (*iam.ListAttachedRolePoliciesOutput, error) {
	fake.called(awsbrowser.OperationListAttachedRolePolicies)
	return fake.listAttachedRolePolicies(ctx, input)
}

func (fake *fakeIAM) ListRolePolicies(ctx context.Context, input *iam.ListRolePoliciesInput) (*iam.ListRolePoliciesOutput, error) {
	fake.called(awsbrowser.OperationListRolePolicies)
	return fake.listRolePolicies(ctx, input)
}

func (fake *fakeIAM) GetPolicy(ctx context.Context, input *iam.GetPolicyInput) (*iam.GetPolicyOutput, error) {
	fake.called(awsbrowser.OperationGetPolicy)
	return fake.getPolicy(ctx, input)
}

func (fake *fakeIAM) GetPolicyVersion(ctx context.Context, input *iam.GetPolicyVersionInput) (*iam.GetPolicyVersionOutput, error) {
	fake.called(awsbrowser.OperationGetPolicyVersion)
	return fake.getPolicyVersion(ctx, input)
}

func (fake *fakeIAM) GetRolePolicy(ctx context.Context, input *iam.GetRolePolicyInput) (*iam.GetRolePolicyOutput, error) {
	fake.called(awsbrowser.OperationGetRolePolicy)
	return fake.getRolePolicy(ctx, input)
}

type recordingSink struct {
	pages     []awsbrowser.QueryPage
	completed []time.Time
}

func (sink *recordingSink) Page(page awsbrowser.QueryPage) error {
	sink.pages = append(sink.pages, page)
	return nil
}

func (sink *recordingSink) Complete(at time.Time) error {
	sink.completed = append(sink.completed, at)
	return nil
}

func testIAMContext(t *testing.T) awsbrowser.AWSContext {
	t.Helper()
	identity := awsbrowser.VerifiedIdentity{
		Partition: "aws", AccountID: "123456789012", PrincipalARN: "arn:aws:iam::123456789012:role/Admin",
		CredentialGeneration: 1,
	}
	ctx, err := awsbrowser.NewAWSContext(awsbrowser.ContextSpec{
		Mode: awsbrowser.ContextModeNamedProfile, Profile: "dev", Region: "ap-northeast-2",
	}, identity, "Admin")
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func testIAMKey(t *testing.T, operation string, params map[string]string) awsbrowser.QueryKey {
	t.Helper()
	key, err := awsbrowser.NewQueryKey(testIAMContext(t), awsbrowser.ProviderIAM, operation, params)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func fixedIAMClock() time.Time { return time.Date(2026, 8, 28, 3, 4, 5, 0, time.UTC) }

func completeRole(name string) iamtypes.Role {
	return iamtypes.Role{
		RoleName: aws.String(name), RoleId: aws.String("AROA" + name),
		Arn: aws.String("arn:aws:iam::123456789012:role/" + name), Path: aws.String("/"),
		CreateDate: aws.Time(fixedIAMClock()),
	}
}

func TestIAMConstructorAndExecuteRejectNilDependencies(t *testing.T) {
	for _, test := range []struct {
		name  string
		api   awsbrowser.IAMAPI
		clock func() time.Time
	}{
		{name: "api", clock: fixedIAMClock},
		{name: "clock", api: &fakeIAM{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if executor, err := NewIAM(test.api, test.clock); executor != nil || !errors.Is(err, ErrInvalidIAMExecutor) {
				t.Fatalf("executor=%#v error=%v", executor, err)
			}
		})
	}

	executor, err := NewIAM(&fakeIAM{}, fixedIAMClock)
	if err != nil {
		t.Fatal(err)
	}
	key := testIAMKey(t, awsbrowser.OperationListRoles, nil)
	if err := executor.Execute(nil, key, &recordingSink{}); !errors.Is(err, ErrInvalidIAMExecutor) {
		t.Fatalf("nil context error=%v", err)
	}
	if err := executor.Execute(context.Background(), key, nil); !errors.Is(err, ErrInvalidIAMExecutor) {
		t.Fatalf("nil sink error=%v", err)
	}
	var nilExecutor *IAMQueryExecutor
	if err := nilExecutor.Execute(context.Background(), key, &recordingSink{}); !errors.Is(err, ErrInvalidIAMExecutor) {
		t.Fatalf("nil executor error=%v", err)
	}
}

func TestIAMExecutorSelectsOnlyRequestedOperationAndBuildsTypedInput(t *testing.T) {
	fake := &fakeIAM{getRole: func(_ context.Context, input *iam.GetRoleInput) (*iam.GetRoleOutput, error) {
		if aws.ToString(input.RoleName) != "worker" {
			t.Fatalf("unexpected role input: %#v", input)
		}
		return &iam.GetRoleOutput{Role: ptrIAMRole(completeRole("worker"))}, nil
	}}
	executor, err := NewIAM(fake, fixedIAMClock)
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingSink{}
	if err := executor.Execute(context.Background(), testIAMKey(t, awsbrowser.OperationGetRole, map[string]string{"role-name": "worker"}), sink); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fake.calls, []string{awsbrowser.OperationGetRole}) {
		t.Fatalf("operation was not lazy: %v", fake.calls)
	}
	if len(sink.pages) != 1 || len(sink.completed) != 1 {
		t.Fatalf("pages=%d complete=%d", len(sink.pages), len(sink.completed))
	}
}

func TestIAMExecutorBuildsEveryFrozenOperationInput(t *testing.T) {
	policyARN := "arn:aws:iam::123456789012:policy/ReadOnly"
	profile := iamtypes.InstanceProfile{
		InstanceProfileName: aws.String("profile"), InstanceProfileId: aws.String("AIPAprofile"),
		Arn: aws.String("arn:aws:iam::123456789012:instance-profile/profile"), Path: aws.String("/"),
		CreateDate: aws.Time(fixedIAMClock()), Roles: []iamtypes.Role{completeRole("worker")},
	}
	fake := &fakeIAM{
		getInstanceProfile: func(_ context.Context, input *iam.GetInstanceProfileInput) (*iam.GetInstanceProfileOutput, error) {
			if aws.ToString(input.InstanceProfileName) != "profile" {
				t.Fatalf("profile input=%#v", input)
			}
			return &iam.GetInstanceProfileOutput{InstanceProfile: &profile}, nil
		},
		listAttachedRolePolicies: func(_ context.Context, input *iam.ListAttachedRolePoliciesInput) (*iam.ListAttachedRolePoliciesOutput, error) {
			if aws.ToString(input.RoleName) != "worker" || aws.ToString(input.PathPrefix) != "/app/" || aws.ToInt32(input.MaxItems) != iamMaxItems {
				t.Fatalf("attached input=%#v", input)
			}
			return &iam.ListAttachedRolePoliciesOutput{}, nil
		},
		listRolePolicies: func(_ context.Context, input *iam.ListRolePoliciesInput) (*iam.ListRolePoliciesOutput, error) {
			if aws.ToString(input.RoleName) != "worker" || aws.ToInt32(input.MaxItems) != iamMaxItems {
				t.Fatalf("inline list input=%#v", input)
			}
			return &iam.ListRolePoliciesOutput{}, nil
		},
		getPolicy: func(_ context.Context, input *iam.GetPolicyInput) (*iam.GetPolicyOutput, error) {
			if aws.ToString(input.PolicyArn) != policyARN {
				t.Fatalf("policy input=%#v", input)
			}
			return &iam.GetPolicyOutput{Policy: &iamtypes.Policy{Arn: aws.String(policyARN)}}, nil
		},
		getPolicyVersion: func(_ context.Context, input *iam.GetPolicyVersionInput) (*iam.GetPolicyVersionOutput, error) {
			if aws.ToString(input.PolicyArn) != policyARN || aws.ToString(input.VersionId) != "v3" {
				t.Fatalf("version input=%#v", input)
			}
			return &iam.GetPolicyVersionOutput{PolicyVersion: &iamtypes.PolicyVersion{VersionId: aws.String("v3"), Document: aws.String(`{"Statement":[]}`)}}, nil
		},
		getRolePolicy: func(_ context.Context, input *iam.GetRolePolicyInput) (*iam.GetRolePolicyOutput, error) {
			if aws.ToString(input.RoleName) != "worker" || aws.ToString(input.PolicyName) != "inline" {
				t.Fatalf("role policy input=%#v", input)
			}
			return &iam.GetRolePolicyOutput{RoleName: aws.String("worker"), PolicyName: aws.String("inline"), PolicyDocument: aws.String(`{"Statement":[]}`)}, nil
		},
	}
	tests := []struct {
		operation string
		params    map[string]string
	}{
		{awsbrowser.OperationGetInstanceProfile, map[string]string{"instance-profile-name": "profile"}},
		{awsbrowser.OperationListAttachedRolePolicies, map[string]string{"role-name": "worker", "path-prefix": "/app/"}},
		{awsbrowser.OperationListRolePolicies, map[string]string{"role-name": "worker"}},
		{awsbrowser.OperationGetPolicy, map[string]string{"policy-arn": policyARN}},
		{awsbrowser.OperationGetPolicyVersion, map[string]string{"policy-arn": policyARN, "version-id": "v3"}},
		{awsbrowser.OperationGetRolePolicy, map[string]string{"role-name": "worker", "policy-name": "inline"}},
	}
	executor, _ := NewIAM(fake, fixedIAMClock)
	for _, test := range tests {
		t.Run(test.operation, func(t *testing.T) {
			fake.mu.Lock()
			before := len(fake.calls)
			fake.mu.Unlock()
			if err := executor.Execute(context.Background(), testIAMKey(t, test.operation, test.params), &recordingSink{}); err != nil {
				t.Fatal(err)
			}
			fake.mu.Lock()
			called := append([]string(nil), fake.calls[before:]...)
			fake.mu.Unlock()
			if !reflect.DeepEqual(called, []string{test.operation}) {
				t.Fatalf("calls=%v", called)
			}
		})
	}
}

func TestIAMListRolesPaginatesWithFixedMaxAndRejectsBadCursor(t *testing.T) {
	markers := make([]string, 0, 2)
	fake := &fakeIAM{}
	fake.listRoles = func(_ context.Context, input *iam.ListRolesInput) (*iam.ListRolesOutput, error) {
		if aws.ToInt32(input.MaxItems) != iamMaxItems {
			t.Fatalf("MaxItems=%d", aws.ToInt32(input.MaxItems))
		}
		markers = append(markers, aws.ToString(input.Marker))
		if input.Marker == nil {
			return &iam.ListRolesOutput{Roles: []iamtypes.Role{completeRole("one")}, IsTruncated: true, Marker: aws.String("next")}, nil
		}
		return &iam.ListRolesOutput{Roles: []iamtypes.Role{completeRole("two")}}, nil
	}
	executor, _ := NewIAM(fake, fixedIAMClock)
	sink := &recordingSink{}
	if err := executor.Execute(context.Background(), testIAMKey(t, awsbrowser.OperationListRoles, nil), sink); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(markers, []string{"", "next"}) || len(sink.pages) != 2 || len(sink.completed) != 1 {
		t.Fatalf("markers=%v pages=%d complete=%d", markers, len(sink.pages), len(sink.completed))
	}

	for _, test := range []struct {
		name      string
		marker    *string
		wantPages int
	}{
		{name: "missing"},
		{name: "repeated", marker: aws.String("same"), wantPages: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			bad := &fakeIAM{listRoles: func(_ context.Context, input *iam.ListRolesInput) (*iam.ListRolesOutput, error) {
				calls++
				if test.name == "repeated" && calls == 1 {
					return &iam.ListRolesOutput{Roles: []iamtypes.Role{completeRole("valid")}, IsTruncated: true, Marker: test.marker}, nil
				}
				return &iam.ListRolesOutput{Roles: []iamtypes.Role{completeRole("invalid")}, IsTruncated: true, Marker: test.marker}, nil
			}}
			executor, _ := NewIAM(bad, fixedIAMClock)
			sink := &recordingSink{}
			err := executor.Execute(context.Background(), testIAMKey(t, awsbrowser.OperationListRoles, nil), sink)
			if !errors.Is(err, awsbrowser.ErrQueryDecode) {
				t.Fatalf("error=%v", err)
			}
			if len(sink.pages) != test.wantPages || len(sink.completed) != 0 {
				t.Fatalf("pages=%d complete=%d", len(sink.pages), len(sink.completed))
			}
			if test.name == "repeated" && sink.pages[0].Resources()[0].Key.ID != "valid" {
				t.Fatalf("repeated-marker page was emitted: %#v", sink.pages)
			}
		})
	}
}

func TestIAMExactGetRoleNotFoundIsEmptyButDeniedAndListErrorsFail(t *testing.T) {
	notFound := &smithy.GenericAPIError{Code: "NoSuchEntity", Message: "not found"}
	denied := &smithy.GenericAPIError{Code: "AccessDenied", Message: "secret details"}
	for _, test := range []struct {
		name         string
		operation    string
		err          error
		wantComplete int
		wantKind     awsbrowser.ProviderErrorKind
	}{
		{name: "exact missing", operation: awsbrowser.OperationGetRole, err: notFound, wantComplete: 1},
		{name: "exact denied", operation: awsbrowser.OperationGetRole, err: denied, wantKind: awsbrowser.ProviderForbidden},
		{name: "list missing", operation: awsbrowser.OperationListRoles, err: notFound, wantKind: awsbrowser.ProviderUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeIAM{
				getRole:   func(context.Context, *iam.GetRoleInput) (*iam.GetRoleOutput, error) { return nil, test.err },
				listRoles: func(context.Context, *iam.ListRolesInput) (*iam.ListRolesOutput, error) { return nil, test.err },
			}
			executor, _ := NewIAM(fake, fixedIAMClock)
			sink := &recordingSink{}
			params := map[string]string(nil)
			if test.operation == awsbrowser.OperationGetRole {
				params = map[string]string{"role-name": "missing"}
			}
			err := executor.Execute(context.Background(), testIAMKey(t, test.operation, params), sink)
			if len(sink.completed) != test.wantComplete {
				t.Fatalf("complete=%d error=%v", len(sink.completed), err)
			}
			if test.wantKind != "" {
				var providerErr *awsbrowser.ProviderError
				if !errors.As(err, &providerErr) || providerErr.Kind != test.wantKind {
					t.Fatalf("error=%#v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIAMDocumentsAreDecodedToSafeValuesAndRelationsAreExactGlobal(t *testing.T) {
	encodedTrust := url.QueryEscape(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow"}]}`)
	role := completeRole("worker")
	role.AssumeRolePolicyDocument = aws.String(encodedTrust)
	profile := iamtypes.InstanceProfile{
		InstanceProfileName: aws.String("worker-profile"), InstanceProfileId: aws.String("AIPA1"),
		Arn: aws.String("arn:aws:iam::123456789012:instance-profile/worker-profile"), Path: aws.String("/"),
		CreateDate: aws.Time(fixedIAMClock()), Roles: []iamtypes.Role{role},
	}
	fake := &fakeIAM{getInstanceProfile: func(context.Context, *iam.GetInstanceProfileInput) (*iam.GetInstanceProfileOutput, error) {
		return &iam.GetInstanceProfileOutput{InstanceProfile: &profile}, nil
	}}
	executor, _ := NewIAM(fake, fixedIAMClock)
	sink := &recordingSink{}
	key := testIAMKey(t, awsbrowser.OperationGetInstanceProfile, map[string]string{"instance-profile-name": "worker-profile"})
	if err := executor.Execute(context.Background(), key, sink); err != nil {
		t.Fatal(err)
	}
	resources := sink.pages[0].Resources()
	if len(resources) != 2 || resources[0].Key.Region != awsbrowser.GlobalRegion || resources[1].Key.Region != awsbrowser.GlobalRegion {
		t.Fatalf("resources=%#v", resources)
	}
	profileFields := resources[0].Observation.Fields()
	relations, ok := profileFields["relations"].([]any)
	if !ok || len(relations) != 1 {
		t.Fatalf("relations=%#v", profileFields["relations"])
	}
	evidence := relations[0].(map[string]any)
	source := evidence["source"].(awsbrowser.ResourceKey)
	target := evidence["target"].(awsbrowser.ResourceKey)
	if evidence["relation_type"] != string(awsbrowser.RelationUses) || evidence["direction"] != string(awsbrowser.RelationOutgoing) || evidence["condition"] != "" ||
		evidence["kind"] != string(awsbrowser.RelationAPIExact) || evidence["scope"] != awsbrowser.GlobalRegion ||
		evidence["operation"] != awsbrowser.OperationGetInstanceProfile || evidence["reason"] != "instance-profile-role" ||
		source != resources[0].Key || source.Type != "iam.instance-profile" || source.ID != "worker-profile" ||
		target != resources[1].Key || target.Type != "iam.role" || target.ID != "worker" {
		t.Fatalf("evidence=%#v", evidence)
	}
	roleFields := resources[1].Observation.Fields()
	document, ok := roleFields["trust_policy_document"].(string)
	if !ok || document != `{"Statement":[{"Effect":"Allow"}],"Version":"2012-10-17"}` {
		t.Fatalf("trust policy=%#v", roleFields["trust_policy_document"])
	}
	if _, misleading := roleFields["effective_permissions"]; misleading {
		t.Fatal("policy document was mislabeled as effective permissions")
	}
}

func TestIAMRolePolicyRelationsAreAPIExactAndGlobal(t *testing.T) {
	policyARN := "arn:aws:iam::123456789012:policy/ReadOnly"
	fake := &fakeIAM{
		listAttachedRolePolicies: func(context.Context, *iam.ListAttachedRolePoliciesInput) (*iam.ListAttachedRolePoliciesOutput, error) {
			return &iam.ListAttachedRolePoliciesOutput{AttachedPolicies: []iamtypes.AttachedPolicy{{
				PolicyArn: aws.String(policyARN), PolicyName: aws.String("ReadOnly"),
			}}}, nil
		},
		getRolePolicy: func(context.Context, *iam.GetRolePolicyInput) (*iam.GetRolePolicyOutput, error) {
			return &iam.GetRolePolicyOutput{
				RoleName: aws.String("worker"), PolicyName: aws.String("inline"),
				PolicyDocument: aws.String(url.QueryEscape(`{"Statement":[]}`)),
			}, nil
		},
	}
	executor, _ := NewIAM(fake, fixedIAMClock)
	queries := []struct {
		key        awsbrowser.QueryKey
		targetType string
		targetID   string
		reason     string
		condition  string
	}{
		{
			key:        testIAMKey(t, awsbrowser.OperationListAttachedRolePolicies, map[string]string{"role-name": "worker"}),
			targetType: "iam.managed-policy", targetID: policyARN, reason: "role-attached-policy", condition: "attached",
		},
		{
			key:        testIAMKey(t, awsbrowser.OperationGetRolePolicy, map[string]string{"role-name": "worker", "policy-name": "inline"}),
			targetType: "iam.inline-policy", targetID: "worker:inline", reason: "role-inline-policy", condition: "inline",
		},
	}
	for _, query := range queries {
		sink := &recordingSink{}
		if err := executor.Execute(context.Background(), query.key, sink); err != nil {
			t.Fatal(err)
		}
		resource := sink.pages[0].Resources()[0]
		if resource.Key.Region != awsbrowser.GlobalRegion {
			t.Fatalf("key=%#v", resource.Key)
		}
		relations := resource.Observation.Fields()["relations"].([]any)
		relation := relations[0].(map[string]any)
		source := relation["source"].(awsbrowser.ResourceKey)
		target := relation["target"].(awsbrowser.ResourceKey)
		if relation["relation_type"] != string(awsbrowser.RelationUses) || relation["direction"] != string(awsbrowser.RelationOutgoing) || relation["condition"] != query.condition ||
			relation["kind"] != string(awsbrowser.RelationAPIExact) || relation["scope"] != awsbrowser.GlobalRegion ||
			relation["operation"] != query.key.Operation || relation["reason"] != query.reason ||
			source.Type != "iam.role" || source.ID != "worker" || target.Type != query.targetType || target.ID != query.targetID ||
			resource.Key != target {
			t.Fatalf("relation=%#v", relation)
		}
	}
}

func TestIAMPolicyVersionsUseDistinctGlobalKeysAndExactRelations(t *testing.T) {
	policyARN := "arn:aws:iam::123456789012:policy/ReadOnly"
	fake := &fakeIAM{getPolicyVersion: func(_ context.Context, input *iam.GetPolicyVersionInput) (*iam.GetPolicyVersionOutput, error) {
		versionID := aws.ToString(input.VersionId)
		return &iam.GetPolicyVersionOutput{PolicyVersion: &iamtypes.PolicyVersion{
			VersionId: aws.String(versionID), Document: aws.String(`{"Statement":[]}`),
		}}, nil
	}}
	executor, _ := NewIAM(fake, fixedIAMClock)
	store := awsbrowser.NewSessionStore()
	seen := make(map[awsbrowser.ResourceKey]struct{})
	for _, versionID := range []string{"v1", "v2"} {
		key := testIAMKey(t, awsbrowser.OperationGetPolicyVersion, map[string]string{
			"policy-arn": policyARN, "version-id": versionID,
		})
		sink := &recordingSink{}
		if err := executor.Execute(context.Background(), key, sink); err != nil {
			t.Fatal(err)
		}
		resource := sink.pages[0].Resources()[0]
		if resource.Key.Region != awsbrowser.GlobalRegion || resource.Key.Type != "iam.managed-policy-version" ||
			resource.Key.ID != policyARN+":"+versionID || resource.Observation.Operation != awsbrowser.OperationGetPolicyVersion {
			t.Fatalf("resource=%#v", resource)
		}
		if _, duplicate := seen[resource.Key]; duplicate {
			t.Fatalf("version key collided: %#v", resource.Key)
		}
		seen[resource.Key] = struct{}{}
		if err := store.StoreObservation(resource.Key, resource.Observation); err != nil {
			t.Fatal(err)
		}
		canonical, ok := store.Canonical(resource.Key)
		if !ok || canonical.ObservationCount() != 1 {
			t.Fatalf("canonical=%#v ok=%v", canonical, ok)
		}
		relation := resource.Observation.Fields()["relations"].([]any)[0].(map[string]any)
		source := relation["source"].(awsbrowser.ResourceKey)
		target := relation["target"].(awsbrowser.ResourceKey)
		if source.Type != "iam.managed-policy" || source.ID != policyARN || target != resource.Key ||
			relation["relation_type"] != string(awsbrowser.RelationHasVersion) || relation["direction"] != string(awsbrowser.RelationOutgoing) || relation["condition"] != versionID ||
			relation["kind"] != string(awsbrowser.RelationAPIExact) || relation["scope"] != awsbrowser.GlobalRegion ||
			relation["operation"] != awsbrowser.OperationGetPolicyVersion || relation["reason"] != "managed-policy-version" {
			t.Fatalf("relation=%#v", relation)
		}
	}
}

func TestIAMPolicyDocumentsPreserveArbitraryJSONKeysAsCanonicalStrings(t *testing.T) {
	document := `{"Statement":[{"Condition":{"StringEquals":{"aws:RequestTag/password":"not-a-credential"}},"Effect":"Allow"}]}`
	fake := &fakeIAM{getRolePolicy: func(context.Context, *iam.GetRolePolicyInput) (*iam.GetRolePolicyOutput, error) {
		return &iam.GetRolePolicyOutput{
			RoleName: aws.String("worker"), PolicyName: aws.String("inline"), PolicyDocument: aws.String(url.QueryEscape(document)),
		}, nil
	}}
	executor, _ := NewIAM(fake, fixedIAMClock)
	sink := &recordingSink{}
	if err := executor.Execute(context.Background(), testIAMKey(t, awsbrowser.OperationGetRolePolicy, map[string]string{
		"role-name": "worker", "policy-name": "inline",
	}), sink); err != nil {
		t.Fatal(err)
	}
	got, ok := sink.pages[0].Resources()[0].Observation.Fields()["policy_document"].(string)
	if !ok || got != document {
		t.Fatalf("policy document=%#v", got)
	}
}

func TestIAMAcceptsAWSVersionIDWithEmptyDottedSuffix(t *testing.T) {
	policyARN := "arn:aws:iam::123456789012:policy/ReadOnly"
	fake := &fakeIAM{getPolicyVersion: func(context.Context, *iam.GetPolicyVersionInput) (*iam.GetPolicyVersionOutput, error) {
		return &iam.GetPolicyVersionOutput{PolicyVersion: &iamtypes.PolicyVersion{
			VersionId: aws.String("v1."), Document: aws.String(`{"Statement":[]}`),
		}}, nil
	}}
	executor, _ := NewIAM(fake, fixedIAMClock)
	if err := executor.Execute(context.Background(), testIAMKey(t, awsbrowser.OperationGetPolicyVersion, map[string]string{
		"policy-arn": policyARN, "version-id": "v1.",
	}), &recordingSink{}); err != nil {
		t.Fatal(err)
	}
}

func TestIAMExactOperationsRejectMismatchedResponseIdentifiers(t *testing.T) {
	policyARN := "arn:aws:iam::123456789012:policy/ReadOnly"
	for _, test := range []struct {
		name      string
		operation string
		params    map[string]string
		fake      *fakeIAM
	}{
		{
			name: "instance profile", operation: awsbrowser.OperationGetInstanceProfile,
			params: map[string]string{"instance-profile-name": "requested"},
			fake: &fakeIAM{getInstanceProfile: func(context.Context, *iam.GetInstanceProfileInput) (*iam.GetInstanceProfileOutput, error) {
				return &iam.GetInstanceProfileOutput{InstanceProfile: &iamtypes.InstanceProfile{InstanceProfileName: aws.String("other")}}, nil
			}},
		},
		{
			name: "role", operation: awsbrowser.OperationGetRole, params: map[string]string{"role-name": "requested"},
			fake: &fakeIAM{getRole: func(context.Context, *iam.GetRoleInput) (*iam.GetRoleOutput, error) {
				return &iam.GetRoleOutput{Role: &iamtypes.Role{RoleName: aws.String("other")}}, nil
			}},
		},
		{
			name: "policy", operation: awsbrowser.OperationGetPolicy, params: map[string]string{"policy-arn": policyARN},
			fake: &fakeIAM{getPolicy: func(context.Context, *iam.GetPolicyInput) (*iam.GetPolicyOutput, error) {
				return &iam.GetPolicyOutput{Policy: &iamtypes.Policy{Arn: aws.String("arn:aws:iam::123456789012:policy/Other")}}, nil
			}},
		},
		{
			name: "policy version", operation: awsbrowser.OperationGetPolicyVersion,
			params: map[string]string{"policy-arn": policyARN, "version-id": "v1"},
			fake: &fakeIAM{getPolicyVersion: func(context.Context, *iam.GetPolicyVersionInput) (*iam.GetPolicyVersionOutput, error) {
				return &iam.GetPolicyVersionOutput{PolicyVersion: &iamtypes.PolicyVersion{
					VersionId: aws.String("v2"), Document: aws.String(`{"Statement":[]}`),
				}}, nil
			}},
		},
		{
			name: "inline policy", operation: awsbrowser.OperationGetRolePolicy,
			params: map[string]string{"role-name": "requested", "policy-name": "inline"},
			fake: &fakeIAM{getRolePolicy: func(context.Context, *iam.GetRolePolicyInput) (*iam.GetRolePolicyOutput, error) {
				return &iam.GetRolePolicyOutput{
					RoleName: aws.String("other"), PolicyName: aws.String("inline"), PolicyDocument: aws.String(`{"Statement":[]}`),
				}, nil
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor, _ := NewIAM(test.fake, fixedIAMClock)
			sink := &recordingSink{}
			err := executor.Execute(context.Background(), testIAMKey(t, test.operation, test.params), sink)
			if !errors.Is(err, awsbrowser.ErrQueryDecode) || len(sink.pages) != 0 || len(sink.completed) != 0 {
				t.Fatalf("error=%v pages=%d complete=%d", err, len(sink.pages), len(sink.completed))
			}
		})
	}
}

func TestIAMMalformedDocumentDoesNotEmitIncompletePage(t *testing.T) {
	fake := &fakeIAM{getPolicyVersion: func(context.Context, *iam.GetPolicyVersionInput) (*iam.GetPolicyVersionOutput, error) {
		return &iam.GetPolicyVersionOutput{PolicyVersion: &iamtypes.PolicyVersion{
			VersionId: aws.String("v1"), Document: aws.String("%zz"),
		}}, nil
	}}
	executor, _ := NewIAM(fake, fixedIAMClock)
	sink := &recordingSink{}
	err := executor.Execute(context.Background(), testIAMKey(t, awsbrowser.OperationGetPolicyVersion, map[string]string{
		"policy-arn": "arn:aws:iam::123456789012:policy/ReadOnly", "version-id": "v1",
	}), sink)
	if !errors.Is(err, awsbrowser.ErrQueryDecode) || len(sink.pages) != 0 || len(sink.completed) != 0 {
		t.Fatalf("error=%v pages=%d complete=%d", err, len(sink.pages), len(sink.completed))
	}
}

func TestIAMCancellationStopsBeforePageOrCompletion(t *testing.T) {
	started := make(chan struct{})
	fake := &fakeIAM{listRoles: func(ctx context.Context, _ *iam.ListRolesInput) (*iam.ListRolesOutput, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	executor, _ := NewIAM(fake, fixedIAMClock)
	ctx, cancel := context.WithCancel(context.Background())
	sink := &recordingSink{}
	done := make(chan error, 1)
	go func() { done <- executor.Execute(ctx, testIAMKey(t, awsbrowser.OperationListRoles, nil), sink) }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if len(sink.pages) != 0 || len(sink.completed) != 0 {
		t.Fatalf("pages=%d complete=%d", len(sink.pages), len(sink.completed))
	}
}

func TestIAMQueryCoordinatorRetainsCompletePagesOnLaterDecodeFailure(t *testing.T) {
	calls := 0
	fake := &fakeIAM{listRoles: func(context.Context, *iam.ListRolesInput) (*iam.ListRolesOutput, error) {
		calls++
		if calls == 1 {
			return &iam.ListRolesOutput{Roles: []iamtypes.Role{completeRole("good")}, IsTruncated: true, Marker: aws.String("next")}, nil
		}
		return &iam.ListRolesOutput{Roles: []iamtypes.Role{{RoleName: nil}}}, nil
	}}
	executor, _ := NewIAM(fake, fixedIAMClock)
	store := awsbrowser.NewSessionStore()
	coordinator, err := awsbrowser.NewQueryCoordinator(store, executor, 1)
	if err != nil {
		t.Fatal(err)
	}
	key := testIAMKey(t, awsbrowser.OperationListRoles, nil)
	subscription, err := coordinator.Subscribe(key)
	if err != nil {
		t.Fatal(err)
	}
	var final awsbrowser.QueryUpdate
	for update := range subscription.Updates() {
		final = update
	}
	if final.Failure == nil || final.Failure.Kind != awsbrowser.ProviderDecode || final.Failure.PartialPages != 1 {
		t.Fatalf("failure=%#v", final.Failure)
	}
	if final.Snapshot.ResourceCount() != 1 || len(final.Snapshot.Pages()) != 1 {
		t.Fatalf("snapshot state=%s resources=%d pages=%d", final.Snapshot.State, final.Snapshot.ResourceCount(), len(final.Snapshot.Pages()))
	}
}

func TestIAMInputValidationPreventsSDKCalls(t *testing.T) {
	fake := &fakeIAM{}
	executor, _ := NewIAM(fake, fixedIAMClock)
	err := executor.Execute(context.Background(), testIAMKey(t, awsbrowser.OperationGetRole, map[string]string{"unexpected": "value"}), &recordingSink{})
	if !errors.Is(err, awsbrowser.ErrInvalidQueryKey) || len(fake.calls) != 0 {
		t.Fatalf("error=%v calls=%v", err, fake.calls)
	}
}

func ptrIAMRole(role iamtypes.Role) *iamtypes.Role { return &role }

var _ awsbrowser.IAMAPI = (*fakeIAM)(nil)
