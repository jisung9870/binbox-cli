package bb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

const (
	awsQueryKindEC2Instances = "ec2_instances"
	awsQueryKindDomainExact  = "domain_exact"
	awsQueryKindRoleExact    = "role_exact"

	awsQueryScopeCurrent = "current"
	awsQueryScopeAll     = "all"
)

var (
	awsQueryRegionRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)+-[0-9]+$`)
	awsQueryRoleRE   = regexp.MustCompile(`^[A-Za-z0-9_+=,.@-]+$`)
)

type awsQueryRequest struct {
	Kind    string `json:"kind"`
	Value   string `json:"value"`
	Scope   string `json:"scope"`
	Profile string `json:"profile"`
	Region  string `json:"region"`
}

type awsQueryCoverage struct {
	Total        int                       `json:"total"`
	Completed    int                       `json:"completed"`
	Searched     int                       `json:"searched"`
	Matched      int                       `json:"matched"`
	NotFound     int                       `json:"not_found"`
	Forbidden    int                       `json:"forbidden"`
	AuthRequired int                       `json:"auth_required"`
	Throttled    int                       `json:"throttled"`
	TimedOut     int                       `json:"timed_out"`
	Cancelled    int                       `json:"cancelled"`
	Unsupported  int                       `json:"unsupported"`
	Unknown      int                       `json:"unknown"`
	NotSearched  int                       `json:"not_searched"`
	Partial      bool                      `json:"partial"`
	Profiles     []awsQueryProfileCoverage `json:"profiles"`
}

type awsQueryProfileCoverage struct {
	Profile      string `json:"profile"`
	Mode         string `json:"mode"`
	AccountID    string `json:"account_id"`
	PrincipalARN string `json:"principal_arn"`
	Status       string `json:"status"`
	ResultCount  int    `json:"result_count"`
}

type awsQueryResource struct {
	Partition string `json:"partition"`
	AccountID string `json:"account_id"`
	Region    string `json:"region"`
	Type      string `json:"type"`
	ID        string `json:"id"`
}

type awsQueryContext struct {
	Profile      string `json:"profile"`
	AccountID    string `json:"account_id"`
	PrincipalARN string `json:"principal_arn"`
	RoleName     string `json:"role_name"`
	Region       string `json:"region"`
}

type awsQueryResult struct {
	Resource             awsQueryResource `json:"resource"`
	Context              awsQueryContext  `json:"context"`
	FetchedAt            time.Time        `json:"fetched_at"`
	Fields               map[string]any   `json:"fields"`
	AvailableViaProfiles []string         `json:"available_via_profiles"`
}

type awsQueryFailure struct {
	Profile   string `json:"profile"`
	Kind      string `json:"kind"`
	Service   string `json:"service"`
	Operation string `json:"operation"`
	Code      string `json:"code"`
	RequestID string `json:"request_id"`
}

type awsQueryExecution struct {
	Coverage awsQueryCoverage
	Results  []awsQueryResult
	Errors   []awsQueryFailure
	Warnings []string
}

type awsQueryData struct {
	Query    awsQueryRequest   `json:"query"`
	Coverage awsQueryCoverage  `json:"coverage"`
	Results  []awsQueryResult  `json:"results"`
	Errors   []awsQueryFailure `json:"errors"`
}

type awsQueryService interface {
	Execute(context.Context, awsQueryRequest) (awsQueryExecution, error)
}

type awsQueryServiceFactory func() (awsQueryService, error)

func unavailableAWSQueryService() (awsQueryService, error) {
	return nil, unavailable("AWS query backend is not available in this build")
}

func (a *App) awsQuery(args []string) error {
	if len(args) == 0 || helpRequested(args) {
		_, err := fmt.Fprint(a.out, awsQueryHelp)
		return err
	}

	request, jsonMode, err := parseAWSQuery(args)
	if err != nil {
		return err
	}
	if a.awsQueryService == nil {
		return unavailable("AWS query backend is not available in this build")
	}
	service, err := a.awsQueryService()
	if err != nil {
		return mapAWSQueryFailure(err, "initialize AWS query")
	}
	if service == nil {
		return unavailable("AWS query backend is not available in this build")
	}
	execution, err := service.Execute(context.Background(), request)
	if err != nil {
		return mapAWSQueryFailure(err, "run AWS query")
	}
	data := normalizeAWSQueryData(request, execution)
	if jsonMode {
		return printEnvelope(a.out, data, normalizedStrings(execution.Warnings))
	}
	return renderAWSQuery(a.out, data, normalizedStrings(execution.Warnings))
}

func mapAWSQueryFailure(err error, message string) error {
	var commandErr *CommandError
	if errors.As(err, &commandErr) {
		return commandErr
	}
	return &CommandError{Code: "operational_error", Message: message, Exit: ExitOperational}
}

const awsQueryHelp = `Usage:
  bb aws query ec2 instances [--profile NAME] [--region REGION] [--json]
  bb aws query domain <fqdn> [--profile NAME] [--region REGION] [--scope current|all] [--json]
  bb aws query role <exact-name> [--profile NAME] [--region REGION] [--scope current|all] [--json]

EC2 instances queries the current context only. Exact domain and role queries
default to all configured AWS profiles; use --scope current to restrict them.
`

func parseAWSQuery(args []string) (awsQueryRequest, bool, error) {
	var request awsQueryRequest
	if len(args) == 0 {
		return request, false, usage("aws query", "ec2 instances|domain <fqdn>|role <exact-name> [options]")
	}

	var tail []string
	switch args[0] {
	case "ec2":
		if len(args) < 2 || args[1] != "instances" {
			return request, false, usage("aws query ec2", "instances [--profile NAME] [--region REGION] [--json]")
		}
		request.Kind, request.Scope = awsQueryKindEC2Instances, awsQueryScopeCurrent
		tail = args[2:]
	case "domain":
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return request, false, usage("aws query domain", "<fqdn> [--profile NAME] [--region REGION] [--scope current|all] [--json]")
		}
		value, err := canonicalAWSQueryFQDN(args[1])
		if err != nil {
			return request, false, invalid("invalid exact domain FQDN")
		}
		request.Kind, request.Value, request.Scope = awsQueryKindDomainExact, value, awsQueryScopeAll
		tail = args[2:]
	case "role":
		if len(args) < 2 || !validAWSQueryRole(args[1]) {
			return request, false, invalid("invalid exact IAM role name")
		}
		request.Kind, request.Value, request.Scope = awsQueryKindRoleExact, args[1], awsQueryScopeAll
		tail = args[2:]
	default:
		return request, false, invalid(fmt.Sprintf("unknown aws query %q", args[0]))
	}

	jsonMode, profileSet, regionSet, scopeSet := false, false, false, false
	for len(tail) > 0 {
		flag := tail[0]
		switch flag {
		case "--json":
			if jsonMode {
				return request, false, invalid("--json may be specified only once")
			}
			jsonMode = true
			tail = tail[1:]
		case "--profile", "--region", "--scope":
			if len(tail) < 2 || !validExplicitName(tail[1]) {
				return request, jsonMode, invalid("missing or invalid value for " + flag)
			}
			value := tail[1]
			switch flag {
			case "--profile":
				if profileSet {
					return request, jsonMode, invalid("AWS profile may be specified only once")
				}
				if !awsProfileNameRE.MatchString(value) {
					return request, jsonMode, invalid("invalid AWS profile name")
				}
				request.Profile, profileSet = value, true
			case "--region":
				if regionSet {
					return request, jsonMode, invalid("AWS region may be specified only once")
				}
				if len(value) > 64 || !awsQueryRegionRE.MatchString(value) {
					return request, jsonMode, invalid("invalid AWS region")
				}
				request.Region, regionSet = value, true
			case "--scope":
				if scopeSet {
					return request, jsonMode, invalid("AWS query scope may be specified only once")
				}
				if request.Kind == awsQueryKindEC2Instances {
					return request, jsonMode, invalid("--scope is not supported for ec2 instances; the query uses current scope")
				}
				if value != awsQueryScopeCurrent && value != awsQueryScopeAll {
					return request, jsonMode, invalid("AWS query scope must be current or all")
				}
				request.Scope, scopeSet = value, true
			}
			tail = tail[2:]
		default:
			return request, jsonMode, invalid(fmt.Sprintf("unknown aws query argument %q", flag))
		}
	}
	return request, jsonMode, nil
}

func canonicalAWSQueryFQDN(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || strings.ContainsAny(value, " \t\r\n") {
		return "", errors.New("invalid FQDN")
	}
	if strings.HasSuffix(value, ".") {
		value = strings.TrimSuffix(value, ".")
	}
	if value == "" || strings.HasSuffix(value, ".") || len(value) > 253 {
		return "", errors.New("invalid FQDN")
	}
	for index, label := range strings.Split(value, ".") {
		if !validAWSQueryDNSLabel(label, index == 0) {
			return "", errors.New("invalid FQDN")
		}
	}
	return value + ".", nil
}

func validAWSQueryDNSLabel(label string, first bool) bool {
	if label == "" || len(label) > 63 {
		return false
	}
	if label == "*" {
		return first
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for index := 0; index < len(label); index++ {
		character := label[index]
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9', character == '-', character == '_':
		case character == '\\' && index+3 < len(label) && asciiAWSQueryDigit(label[index+1]) && asciiAWSQueryDigit(label[index+2]) && asciiAWSQueryDigit(label[index+3]):
			index += 3
		default:
			return false
		}
	}
	return true
}

func asciiAWSQueryDigit(value byte) bool { return value >= '0' && value <= '9' }

func validAWSQueryRole(value string) bool {
	return value == strings.TrimSpace(value) && len(value) >= 1 && len(value) <= 64 && awsQueryRoleRE.MatchString(value)
}

func normalizeAWSQueryData(request awsQueryRequest, execution awsQueryExecution) awsQueryData {
	data := awsQueryData{Query: request, Coverage: execution.Coverage, Results: execution.Results, Errors: execution.Errors}
	if data.Coverage.Profiles == nil {
		data.Coverage.Profiles = []awsQueryProfileCoverage{}
	}
	if data.Results == nil {
		data.Results = []awsQueryResult{}
	}
	if data.Errors == nil {
		data.Errors = []awsQueryFailure{}
	}
	for index := range data.Results {
		if data.Results[index].Fields == nil {
			data.Results[index].Fields = map[string]any{}
		}
		if data.Results[index].AvailableViaProfiles == nil {
			data.Results[index].AvailableViaProfiles = []string{}
		}
	}
	return data
}

func normalizedStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func renderAWSQuery(out io.Writer, data awsQueryData, warnings []string) error {
	label := data.Query.Kind
	switch data.Query.Kind {
	case awsQueryKindEC2Instances:
		label = "ec2 instances"
	case awsQueryKindDomainExact:
		label = "domain " + data.Query.Value
	case awsQueryKindRoleExact:
		label = "role " + data.Query.Value
	}
	if _, err := fmt.Fprintf(out, "AWS query: %s (scope: %s)\n", label, data.Query.Scope); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Coverage: %d/%d completed, %d results", data.Coverage.Completed, data.Coverage.Total, len(data.Results)); err != nil {
		return err
	}
	if data.Coverage.Partial {
		if _, err := fmt.Fprint(out, " (partial)"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(out); err != nil {
		return err
	}
	if len(data.Results) == 0 {
		if _, err := fmt.Fprintln(out, "No results."); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(out, "TYPE\tID\tACCOUNT\tPROFILE\tREGION"); err != nil {
			return err
		}
		for _, result := range data.Results {
			if _, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\n", safeAWSQueryText(result.Resource.Type), safeAWSQueryText(result.Resource.ID),
				safeAWSQueryText(result.Resource.AccountID), safeAWSQueryText(result.Context.Profile), safeAWSQueryText(result.Resource.Region)); err != nil {
				return err
			}
		}
	}
	for _, warning := range warnings {
		if _, err := fmt.Fprintln(out, "Warning:", safeAWSQueryText(warning)); err != nil {
			return err
		}
	}
	for _, failure := range data.Errors {
		if _, err := fmt.Fprintf(out, "Error: profile=%s kind=%s service=%s operation=%s code=%s request_id=%s\n",
			safeAWSQueryText(failure.Profile), safeAWSQueryText(failure.Kind), safeAWSQueryText(failure.Service),
			safeAWSQueryText(failure.Operation), safeAWSQueryText(failure.Code), safeAWSQueryText(failure.RequestID)); err != nil {
			return err
		}
	}
	return nil
}

func safeAWSQueryText(value string) string {
	return strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, value)
}
