package bb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type awsQueryServiceFunc func(context.Context, awsQueryRequest) (awsQueryExecution, error)

func (function awsQueryServiceFunc) Execute(ctx context.Context, request awsQueryRequest) (awsQueryExecution, error) {
	return function(ctx, request)
}

func TestParseAWSQueryGrammarAndDefaults(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		request  awsQueryRequest
		jsonMode bool
	}{
		{
			name:    "EC2 current",
			args:    []string{"ec2", "instances"},
			request: awsQueryRequest{Kind: awsQueryKindEC2Instances, Scope: awsQueryScopeCurrent},
		},
		{
			name:     "EC2 flags",
			args:     []string{"ec2", "instances", "--profile", "dev-1", "--region", "ap-northeast-2", "--json"},
			request:  awsQueryRequest{Kind: awsQueryKindEC2Instances, Scope: awsQueryScopeCurrent, Profile: "dev-1", Region: "ap-northeast-2"},
			jsonMode: true,
		},
		{
			name:    "domain canonical all",
			args:    []string{"domain", "API.Example.COM"},
			request: awsQueryRequest{Kind: awsQueryKindDomainExact, Value: "api.example.com.", Scope: awsQueryScopeAll},
		},
		{
			name:    "domain current",
			args:    []string{"domain", "_acme-challenge.example.com.", "--scope", "current", "--region", "us-east-1"},
			request: awsQueryRequest{Kind: awsQueryKindDomainExact, Value: "_acme-challenge.example.com.", Scope: awsQueryScopeCurrent, Region: "us-east-1"},
		},
		{
			name:    "wildcard domain",
			args:    []string{"domain", "*.example.com"},
			request: awsQueryRequest{Kind: awsQueryKindDomainExact, Value: "*.example.com.", Scope: awsQueryScopeAll},
		},
		{
			name:     "role exact all",
			args:     []string{"role", "deploy-role", "--profile", "prod", "--scope", "all", "--json"},
			request:  awsQueryRequest{Kind: awsQueryKindRoleExact, Value: "deploy-role", Scope: awsQueryScopeAll, Profile: "prod"},
			jsonMode: true,
		},
		{
			name:    "AMI exact all",
			args:    []string{"ami", "ami-0123456789abcdef0", "--region", "ap-northeast-2"},
			request: awsQueryRequest{Kind: awsQueryKindAMIExact, Value: "ami-0123456789abcdef0", Scope: awsQueryScopeAll, Region: "ap-northeast-2"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, jsonMode, err := parseAWSQuery(test.args)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(request, test.request) || jsonMode != test.jsonMode {
				t.Fatalf("request=%+v json=%v, want %+v json=%v", request, jsonMode, test.request, test.jsonMode)
			}
		})
	}
}

func TestAWSQueryInvalidInvocationsNeverConstructBackend(t *testing.T) {
	tests := [][]string{
		{},
		{"unknown"},
		{"ec2"},
		{"ec2", "volumes"},
		{"ec2", "instances", "--scope", "current"},
		{"ec2", "instances", "extra"},
		{"domain"},
		{"domain", "bad domain"},
		{"domain", "two..dots"},
		{"domain", "example.com", "--scope", "some"},
		{"domain", "example.com", "--scope", "all", "--scope", "current"},
		{"domain", "example.com", "--unknown"},
		{"domain", "example.com", "--profile"},
		{"domain", "example.com", "--profile", "--inject"},
		{"domain", "example.com", "--profile", ".hidden"},
		{"domain", "example.com", "--profile", "-hidden"},
		{"domain", "example.com", "--profile", "dev", "--profile", "prod"},
		{"domain", "example.com", "--region", "not-a-region"},
		{"domain", "example.com", "--region", "us-east-1", "--region", "us-west-2"},
		{"domain", "example.com", "--json", "--json"},
		{"role"},
		{"role", "partial/name"},
		{"role", strings.Repeat("a", 65)},
		{"ami"},
		{"ami", "i-0123456789abcdef0"},
		{"ami", "ami-not-hex"},
		{"ami", "ami-123456789"},
	}

	for _, args := range tests {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
			app := New(stdout, stderr, nil)
			calls := 0
			app.awsQueryService = func() (awsQueryService, error) {
				calls++
				return nil, errors.New("must not construct")
			}
			err := app.Run(append([]string{"aws", "query"}, args...))
			if len(args) == 0 {
				if err != nil || !strings.Contains(stdout.String(), "bb aws query ec2 instances") {
					t.Fatalf("empty query should show help: err=%v stdout=%q", err, stdout.String())
				}
			} else if ExitCode(err) != ExitInvalidInvocation {
				t.Fatalf("args=%q err=%v exit=%d", args, err, ExitCode(err))
			}
			if calls != 0 {
				t.Fatalf("args=%q constructed backend %d times", args, calls)
			}
		})
	}
}

func TestAWSQueryInvalidContextDoesNotDiscoverAWSCLI(t *testing.T) {
	for _, args := range [][]string{{"role", "reader", "--profile", ".hidden"}, {"role", "reader", "--profile", "-hidden"}, {"domain", "example.com", "--region", "not-a-region"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			app := New(new(bytes.Buffer), new(bytes.Buffer), nil)
			app.lookPath = func(string) (string, error) { t.Fatal("invalid context discovered AWS CLI"); return "", nil }
			if err := app.Run(append([]string{"aws", "query"}, args...)); ExitCode(err) != ExitInvalidInvocation {
				t.Fatalf("args=%q err=%v exit=%d", args, err, ExitCode(err))
			}
		})
	}
}

func TestAWSQueryJSONEnvelopeAndRequest(t *testing.T) {
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	app := New(stdout, stderr, nil)
	fetchedAt := time.Date(2026, 8, 28, 4, 5, 6, 0, time.UTC)
	wantRequest := awsQueryRequest{
		Kind: awsQueryKindDomainExact, Value: "api.example.com.", Scope: awsQueryScopeCurrent,
		Profile: "dev", Region: "ap-northeast-2",
	}
	app.awsQueryService = func() (awsQueryService, error) {
		return awsQueryServiceFunc(func(_ context.Context, request awsQueryRequest) (awsQueryExecution, error) {
			if request != wantRequest {
				t.Fatalf("request=%+v want=%+v", request, wantRequest)
			}
			return awsQueryExecution{
				Coverage: awsQueryCoverage{
					Total: 2, Completed: 2, Searched: 1, Matched: 1, Forbidden: 1, Partial: true,
					Profiles: []awsQueryProfileCoverage{
						{Profile: "dev", Mode: "named-profile", AccountID: "123456789012", PrincipalARN: "arn:aws:iam::123456789012:role/read", Status: "matched", ResultCount: 1},
						{Profile: "locked", Mode: "named-profile", Status: "forbidden"},
					},
				},
				Results: []awsQueryResult{{
					Resource:  awsQueryResource{Partition: "aws", AccountID: "123456789012", Region: "global", Type: "resource-record-set", ID: "record-key"},
					Context:   awsQueryContext{Profile: "dev", AccountID: "123456789012", PrincipalARN: "arn:aws:iam::123456789012:role/read", RoleName: "read", Region: "ap-northeast-2"},
					FetchedAt: fetchedAt, Fields: map[string]any{
						"name": "api.example.com.", "type": "A",
						"relations": []any{map[string]any{"relation_type": "alias-to", "direction": "outgoing", "condition": "A alias", "kind": "api-exact"}},
					}, AvailableViaProfiles: []string{"dev"},
				}},
				Errors:   []awsQueryFailure{{Profile: "locked", Kind: "forbidden", Service: "route53", Operation: "ListHostedZones", Code: "AccessDenied", RequestID: "req-1"}},
				Warnings: []string{"partial coverage"},
			}, nil
		}), nil
	}

	err := app.Run([]string{"aws", "query", "domain", "API.Example.com", "--profile", "dev", "--region", "ap-northeast-2", "--scope", "current", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q", stderr.String())
	}
	var document struct {
		SchemaVersion int             `json:"schema_version"`
		OK            bool            `json:"ok"`
		Data          awsQueryData    `json:"data"`
		Warnings      []string        `json:"warnings"`
		Error         json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("decode %q: %v", stdout.String(), err)
	}
	if document.SchemaVersion != SchemaVersion || !document.OK || string(document.Error) != "null" || !reflect.DeepEqual(document.Warnings, []string{"partial coverage"}) {
		t.Fatalf("envelope=%+v", document)
	}
	if !reflect.DeepEqual(document.Data.Query, wantRequest) || len(document.Data.Results) != 1 || len(document.Data.Errors) != 1 || !document.Data.Coverage.Partial {
		t.Fatalf("data=%+v", document.Data)
	}
	relation := document.Data.Results[0].Fields["relations"].([]any)[0].(map[string]any)
	if relation["relation_type"] != "alias-to" || relation["direction"] != "outgoing" || relation["condition"] != "A alias" || relation["kind"] != "api-exact" {
		t.Fatalf("query relation semantics=%+v", relation)
	}
}

func TestAWSQueryJSONNormalizesCollections(t *testing.T) {
	stdout := new(bytes.Buffer)
	app := New(stdout, new(bytes.Buffer), nil)
	app.awsQueryService = func() (awsQueryService, error) {
		return awsQueryServiceFunc(func(context.Context, awsQueryRequest) (awsQueryExecution, error) {
			return awsQueryExecution{}, nil
		}), nil
	}
	if err := app.Run([]string{"aws", "query", "role", "deploy-role", "--json"}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"profiles":[]`, `"results":[]`, `"errors":[]`, `"warnings":[]`, `"error":null`} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("JSON %q missing %q", stdout.String(), expected)
		}
	}
}

func TestAWSQueryHumanRenderer(t *testing.T) {
	stdout := new(bytes.Buffer)
	app := New(stdout, new(bytes.Buffer), nil)
	app.awsQueryService = func() (awsQueryService, error) {
		return awsQueryServiceFunc(func(context.Context, awsQueryRequest) (awsQueryExecution, error) {
			return awsQueryExecution{
				Coverage: awsQueryCoverage{Total: 2, Completed: 2, Partial: true},
				Results: []awsQueryResult{{
					Resource: awsQueryResource{Type: "iam.role", ID: "deploy-role", AccountID: "123456789012", Region: "global"},
					Context:  awsQueryContext{Profile: "dev"},
				}},
				Errors:   []awsQueryFailure{{Profile: "locked\nunsafe", Kind: "forbidden", Service: "iam", Operation: "GetRole", Code: "AccessDenied", RequestID: "req-1"}},
				Warnings: []string{"partial\ncoverage"},
			}, nil
		}), nil
	}
	if err := app.Run([]string{"aws", "query", "role", "deploy-role"}); err != nil {
		t.Fatal(err)
	}
	want := "AWS query: role deploy-role (scope: all)\n" +
		"Coverage: 2/2 completed, 1 results (partial)\n" +
		"TYPE\tID\tACCOUNT\tPROFILE\tREGION\n" +
		"iam.role\tdeploy-role\t123456789012\tdev\tglobal\n" +
		"Warning: partial coverage\n" +
		"Error: profile=locked unsafe kind=forbidden service=iam operation=GetRole code=AccessDenied request_id=req-1\n"
	if stdout.String() != want {
		t.Fatalf("stdout=%q want=%q", stdout.String(), want)
	}
}

func TestAWSQueryAMIRendererShowsOwnerAndVisibleAccount(t *testing.T) {
	stdout := new(bytes.Buffer)
	app := New(stdout, new(bytes.Buffer), nil)
	app.awsQueryService = func() (awsQueryService, error) {
		return awsQueryServiceFunc(func(context.Context, awsQueryRequest) (awsQueryExecution, error) {
			return awsQueryExecution{
				Coverage: awsQueryCoverage{Total: 1, Completed: 1, Matched: 1},
				Results: []awsQueryResult{{
					Resource: awsQueryResource{Type: "ec2.image", ID: "ami-0123456789abcdef0", AccountID: "111111111111", Region: "ap-northeast-2"},
					Context:  awsQueryContext{Profile: "dev", AccountID: "111111111111"},
					Fields:   map[string]any{"owner_id": "999999999999", "name": "shared-base"},
				}},
			}, nil
		}), nil
	}
	if err := app.Run([]string{"aws", "query", "ami", "ami-0123456789abcdef0", "--region", "ap-northeast-2"}); err != nil {
		t.Fatal(err)
	}
	want := "AWS query: ami ami-0123456789abcdef0 (scope: all)\n" +
		"Coverage: 1/1 completed, 1 results\n" +
		"AMI\tNAME\tOWNER_ACCOUNT\tVISIBLE_IN_ACCOUNT\tPROFILE\tREGION\n" +
		"ami-0123456789abcdef0\tshared-base\t999999999999\t111111111111\tdev\tap-northeast-2\n"
	if stdout.String() != want {
		t.Fatalf("stdout=%q want=%q", stdout.String(), want)
	}
}

func TestAWSQueryStableExitMapping(t *testing.T) {
	tests := []struct {
		name       string
		factoryErr error
		serviceErr error
		exit       int
		code       string
	}{
		{name: "factory operational", factoryErr: errors.New("factory credential secret"), exit: ExitOperational, code: "operational_error"},
		{name: "factory unavailable", factoryErr: unavailable("AWS CLI missing"), exit: ExitCapabilityUnavailable, code: "capability_unavailable"},
		{name: "service operational", serviceErr: errors.New("query credential secret"), exit: ExitOperational, code: "operational_error"},
		{name: "service unavailable", serviceErr: unavailable("AWS CLI unsupported"), exit: ExitCapabilityUnavailable, code: "capability_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stdout := new(bytes.Buffer)
			app := New(stdout, new(bytes.Buffer), nil)
			app.awsQueryService = func() (awsQueryService, error) {
				if test.factoryErr != nil {
					return nil, test.factoryErr
				}
				return awsQueryServiceFunc(func(context.Context, awsQueryRequest) (awsQueryExecution, error) {
					return awsQueryExecution{}, test.serviceErr
				}), nil
			}
			err := app.Run([]string{"aws", "query", "ec2", "instances", "--json"})
			if ExitCode(err) != test.exit || !Reported(err) {
				t.Fatalf("err=%v exit=%d reported=%v", err, ExitCode(err), Reported(err))
			}
			var document struct {
				OK    bool `json:"ok"`
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if decodeErr := json.Unmarshal(stdout.Bytes(), &document); decodeErr != nil || document.OK || document.Error.Code != test.code {
				t.Fatalf("document=%+v decode=%v raw=%q", document, decodeErr, stdout.String())
			}
			if strings.Contains(stdout.String(), "credential secret") || strings.Contains(err.Error(), "credential secret") {
				t.Fatalf("raw backend failure leaked: err=%q JSON=%q", err, stdout.String())
			}
		})
	}
}

func TestAWSQueryInvalidJSONUsesErrorEnvelopeWithoutBackend(t *testing.T) {
	stdout := new(bytes.Buffer)
	app := New(stdout, new(bytes.Buffer), nil)
	calls := 0
	app.awsQueryService = func() (awsQueryService, error) {
		calls++
		return nil, errors.New("must not construct")
	}
	err := app.Run([]string{"aws", "query", "domain", "not a domain", "--json"})
	if ExitCode(err) != ExitInvalidInvocation || !Reported(err) || calls != 0 {
		t.Fatalf("err=%v exit=%d reported=%v calls=%d", err, ExitCode(err), Reported(err), calls)
	}
	var document struct {
		SchemaVersion int  `json:"schema_version"`
		OK            bool `json:"ok"`
		Data          any  `json:"data"`
		Error         struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if decodeErr := json.Unmarshal(stdout.Bytes(), &document); decodeErr != nil || document.SchemaVersion != SchemaVersion || document.OK || document.Data != nil || document.Error.Code != "invalid_invocation" {
		t.Fatalf("document=%+v decode=%v raw=%q", document, decodeErr, stdout.String())
	}
}

func TestAWSQueryDefaultBackendIsCapabilityUnavailableWithoutAWSCLI(t *testing.T) {
	app := New(new(bytes.Buffer), new(bytes.Buffer), nil)
	app.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	err := app.Run([]string{"aws", "query", "ec2", "instances"})
	if ExitCode(err) != ExitCapabilityUnavailable {
		t.Fatalf("err=%v exit=%d", err, ExitCode(err))
	}
}

func TestAWSQueryHelpAndAWSHelpAreLocalOnly(t *testing.T) {
	for _, args := range [][]string{{"aws", "query", "--help"}, {"aws", "--help"}} {
		stdout := new(bytes.Buffer)
		app := New(stdout, new(bytes.Buffer), nil)
		calls := 0
		app.awsQueryService = func() (awsQueryService, error) {
			calls++
			return nil, errors.New("must not construct")
		}
		if err := app.Run(args); err != nil {
			t.Fatal(err)
		}
		if calls != 0 || !strings.Contains(stdout.String(), "bb aws query ec2 instances") {
			t.Fatalf("args=%q calls=%d stdout=%q", args, calls, stdout.String())
		}
		if args[1] == "--help" && strings.Contains(stdout.String(), "browse [--profile NAME] [--region REGION] [--json]") {
			t.Fatalf("top-level AWS help retained browse --json: %q", stdout.String())
		}
	}
}
