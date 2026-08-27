package bb

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
	awsintegration "github.com/jisung9870/binbox-cli/internal/bb/awsbrowser/integration"
)

const awsRuntimeConcurrency = 4

var errUnsupportedAWSIntent = errors.New("unsupported AWS browser target")

type awsIntentCore interface {
	Subscribe(context.Context, awsintegration.Request) (*awsintegration.Subscription, error)
}

type awsIntentSearch interface {
	Submit(context.Context, awsintegration.SearchRequest) (awsintegration.SearchResult, error)
}

type awsRuntime struct {
	core   awsIntentCore
	search awsIntentSearch
}

// lazyAWSRuntime is shared by browse and query. Merely constructing App,
// Runner, or the browser Home screen performs no PATH lookup, CLI invocation,
// profile resolution, credential resolution, or SDK call.
type lazyAWSRuntime struct {
	app     *App
	once    sync.Once
	runtime *awsRuntime
	err     error
}

func newLazyAWSRuntime(app *App) *lazyAWSRuntime { return &lazyAWSRuntime{app: app} }

func (runtime *lazyAWSRuntime) initialize() (*awsRuntime, error) {
	if runtime == nil || runtime.app == nil {
		return nil, unavailable("AWS query backend is not available in this build")
	}
	runtime.once.Do(func() {
		path, err := runtime.app.lookPath("aws")
		if err != nil || strings.TrimSpace(path) == "" {
			runtime.err = unavailable("AWS CLI is required for AWS browse and query")
			return
		}
		env := append([]string(nil), runtime.app.env...)
		if env == nil {
			env = os.Environ()
		}
		core, err := awsintegration.NewProduction(awsintegration.ProductionOptions{
			AWSCLIPath: path, Env: env, Clock: runtime.app.now, Concurrency: awsRuntimeConcurrency,
		})
		if err != nil {
			runtime.err = unavailable("AWS browser runtime is unavailable")
			return
		}
		search, err := awsintegration.NewSearchService(core, awsbrowser.NewExecCLI(path), env)
		if err != nil {
			runtime.err = unavailable("AWS browser search is unavailable")
			return
		}
		runtime.runtime = &awsRuntime{core: core, search: search}
	})
	return runtime.runtime, runtime.err
}

func (runtime *lazyAWSRuntime) Dispatch(ctx context.Context, intent awsbrowser.Intent) (awsbrowser.IntentStream, error) {
	binding, err := runtime.initialize()
	if err != nil {
		return nil, err
	}
	return (&awsIntentDispatcher{core: binding.core, search: binding.search}).Dispatch(ctx, intent)
}

func (runtime *lazyAWSRuntime) QueryService() (awsQueryService, error) {
	binding, err := runtime.initialize()
	if err != nil {
		return nil, err
	}
	return &productionAWSQueryService{search: binding.search}, nil
}

type awsIntentDispatcher struct {
	core   awsIntentCore
	search awsIntentSearch
}

func (dispatcher *awsIntentDispatcher) Dispatch(ctx context.Context, intent awsbrowser.Intent) (awsbrowser.IntentStream, error) {
	if dispatcher == nil || ctx == nil {
		return nil, errUnsupportedAWSIntent
	}
	switch intent.Kind {
	case awsbrowser.IntentOpen, awsbrowser.IntentRefresh:
		request, ok := awsRequestForIntent(intent)
		if !ok || dispatcher.core == nil {
			return nil, errUnsupportedAWSIntent
		}
		request.Refresh = intent.Kind == awsbrowser.IntentRefresh
		return dispatcher.dispatchQuery(ctx, request)
	case awsbrowser.IntentSearch:
		request, ok := awsSearchRequestForIntent(intent)
		if !ok || dispatcher.search == nil {
			return nil, errUnsupportedAWSIntent
		}
		return dispatcher.dispatchSearch(ctx, request), nil
	default:
		return nil, errUnsupportedAWSIntent
	}
}

func awsRequestForIntent(intent awsbrowser.Intent) (awsintegration.Request, bool) {
	request := awsintegration.Request{Profile: intent.Profile, Region: intent.Region}
	switch intent.Target {
	case "ec2-instances":
		request.Provider, request.Operation = awsbrowser.ProviderEC2, awsbrowser.OperationDescribeInstances
	case "route53-hosted-zones":
		request.Provider, request.Operation = awsbrowser.ProviderRoute53, awsbrowser.OperationListHostedZones
	case "iam-roles":
		request.Provider, request.Operation = awsbrowser.ProviderIAM, awsbrowser.OperationListRoles
	case "vpc-networking":
		request.Provider, request.Operation = awsbrowser.ProviderEC2, awsbrowser.OperationDescribeVpcs
	default:
		resourceType, id, ok := strings.Cut(intent.Target, ":")
		if !ok || !safeAWSIntentParameter(id) {
			return awsintegration.Request{}, false
		}
		request.Params = make(map[string]string)
		switch resourceType {
		case "ec2.instance":
			request.Provider, request.Operation, request.Params["instance-id"] = awsbrowser.ProviderEC2, awsbrowser.OperationDescribeInstances, id
		case "ec2.volume":
			request.Provider, request.Operation, request.Params["volume-id"] = awsbrowser.ProviderEC2, awsbrowser.OperationDescribeVolumes, id
		case "ec2.security-group":
			request.Provider, request.Operation, request.Params["group-id"] = awsbrowser.ProviderEC2, awsbrowser.OperationDescribeSecurityGroups, id
		case "ec2.security-group-rule":
			request.Provider, request.Operation, request.Params["security-group-rule-id"] = awsbrowser.ProviderEC2, awsbrowser.OperationDescribeSecurityGroupRules, id
		case "ec2.vpc":
			request.Provider, request.Operation, request.Params["vpc-id"] = awsbrowser.ProviderEC2, awsbrowser.OperationDescribeVpcs, id
		case "ec2.subnet":
			request.Provider, request.Operation, request.Params["subnet-id"] = awsbrowser.ProviderEC2, awsbrowser.OperationDescribeSubnets, id
		case "ec2.route-table":
			request.Provider, request.Operation, request.Params["route-table-id"] = awsbrowser.ProviderEC2, awsbrowser.OperationDescribeRouteTables, id
		case "iam.role":
			request.Provider, request.Operation, request.Params["role-name"] = awsbrowser.ProviderIAM, awsbrowser.OperationGetRole, id
		case "iam.instance-profile":
			request.Provider, request.Operation, request.Params["instance-profile-name"] = awsbrowser.ProviderIAM, awsbrowser.OperationGetInstanceProfile, id
		case "iam.managed-policy":
			request.Provider, request.Operation, request.Params["policy-arn"] = awsbrowser.ProviderIAM, awsbrowser.OperationGetPolicy, id
		case "iam.inline-policy":
			role, policy, found := strings.Cut(id, ":")
			if !found || role == "" || policy == "" {
				return awsintegration.Request{}, false
			}
			request.Provider, request.Operation = awsbrowser.ProviderIAM, awsbrowser.OperationGetRolePolicy
			request.Params["role-name"], request.Params["policy-name"] = role, policy
		case "iam.managed-policy-version":
			index := strings.LastIndex(id, ":")
			if index < 1 || index == len(id)-1 {
				return awsintegration.Request{}, false
			}
			request.Provider, request.Operation = awsbrowser.ProviderIAM, awsbrowser.OperationGetPolicyVersion
			request.Params["policy-arn"], request.Params["version-id"] = id[:index], id[index+1:]
		case "hosted-zone":
			request.Provider, request.Operation, request.Params["hosted-zone-id"] = awsbrowser.ProviderRoute53, awsbrowser.OperationListResourceRecordSets, id
		default:
			return awsintegration.Request{}, false
		}
	}
	if awsbrowser.ValidateProviderOperation(request.Provider, request.Operation) != nil {
		return awsintegration.Request{}, false
	}
	return request, true
}

func awsSearchRequestForIntent(intent awsbrowser.Intent) (awsintegration.SearchRequest, bool) {
	if intent.Target != "" && intent.Target != "cross-profile-search" {
		return awsintegration.SearchRequest{}, false
	}
	request := awsintegration.SearchRequest{Profile: intent.Profile, Region: intent.Region, Query: intent.Query}
	switch intent.Scope {
	case awsQueryScopeCurrent:
		request.Scope = awsintegration.SearchCurrent
	case awsQueryScopeAll:
		request.Scope = awsintegration.SearchAll
	default:
		return awsintegration.SearchRequest{}, false
	}
	switch intent.SearchKind {
	case "ec2-instances":
		request.Kind = awsintegration.SearchEC2Instances
	case "domain":
		request.Kind = awsintegration.SearchDomain
	case "role":
		request.Kind = awsintegration.SearchRole
	default:
		return awsintegration.SearchRequest{}, false
	}
	return request, true
}

func safeAWSIntentParameter(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 4096 {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func (dispatcher *awsIntentDispatcher) dispatchQuery(ctx context.Context, request awsintegration.Request) (awsbrowser.IntentStream, error) {
	subscription, err := dispatcher.core.Subscribe(ctx, request)
	if err != nil {
		return nil, errUnsupportedAWSIntent
	}
	updates := make(chan awsbrowser.IntentUpdate, 1)
	bridgeCtx, cancel := context.WithCancel(ctx)
	stream := &awsbrowser.ChannelIntentStream{C: updates, CancelFunc: func() { cancel(); subscription.Unsubscribe() }}
	go func() {
		defer close(updates)
		defer stream.Cancel()
		for {
			select {
			case <-bridgeCtx.Done():
				return
			case update, ok := <-subscription.Updates():
				if !ok {
					return
				}
				converted := intentUpdateFromIntegration(update)
				select {
				case updates <- converted:
				case <-bridgeCtx.Done():
					return
				}
				if converted.Done {
					return
				}
			}
		}
	}()
	return stream, nil
}

func intentUpdateFromIntegration(update awsintegration.Update) awsbrowser.IntentUpdate {
	converted := awsbrowser.IntentUpdate{Query: awsbrowser.QueryUpdate{Snapshot: update.Snapshot}, Done: update.Coverage.Completed}
	if update.Key != nil {
		converted.Query.Key = *update.Key
		contextCopy := update.Key.Context
		converted.Context = &contextCopy
	}
	if update.Failure != nil {
		converted.Query.Failure = &awsbrowser.ProviderFailure{
			State: update.Failure.State, Kind: update.Failure.Kind, Service: update.Failure.Provider,
			Operation: update.Failure.Operation, Code: update.Failure.Code, RequestID: update.Failure.RequestID,
		}
		converted.Done = true
	}
	converted.Projection = awsbrowser.ProjectQueryUpdate(converted.Query)
	return converted
}

func (dispatcher *awsIntentDispatcher) dispatchSearch(ctx context.Context, request awsintegration.SearchRequest) awsbrowser.IntentStream {
	updates := make(chan awsbrowser.IntentUpdate, 1)
	searchCtx, cancel := context.WithCancel(ctx)
	stream := &awsbrowser.ChannelIntentStream{C: updates, CancelFunc: cancel}
	go func() {
		defer close(updates)
		result, err := dispatcher.search.Submit(searchCtx, request)
		if err != nil {
			update := unsupportedIntentUpdate()
			select {
			case updates <- update:
			case <-searchCtx.Done():
			}
			return
		}
		update := intentUpdateFromSearch(result)
		select {
		case updates <- update:
		case <-searchCtx.Done():
		}
	}()
	return stream
}

func unsupportedIntentUpdate() awsbrowser.IntentUpdate {
	failure := &awsbrowser.ProviderFailure{State: awsbrowser.LoadUnsupported, Kind: awsbrowser.ProviderUnsupported}
	return awsbrowser.IntentUpdate{Query: awsbrowser.QueryUpdate{Snapshot: awsbrowser.QuerySnapshot{State: awsbrowser.LoadUnsupported}, Failure: failure}, Done: true}
}

func intentUpdateFromSearch(result awsintegration.SearchResult) awsbrowser.IntentUpdate {
	state, failure := searchLoadState(result)
	return awsbrowser.IntentUpdate{
		Query:      awsbrowser.QueryUpdate{Snapshot: awsbrowser.QuerySnapshot{State: state}, Failure: failure},
		Projection: projectSearchResources(result.Resources), Done: true,
	}
}

func searchLoadState(result awsintegration.SearchResult) (awsbrowser.LoadState, *awsbrowser.ProviderFailure) {
	if len(result.Resources) != 0 {
		return awsbrowser.LoadReady, nil
	}
	statuses := make([]awsintegration.ProfileStatus, 0, len(result.Coverage)+1)
	if result.DiscoveryStatus != "" {
		statuses = append(statuses, result.DiscoveryStatus)
	}
	for _, coverage := range result.Coverage {
		statuses = append(statuses, coverage.Status)
	}
	for _, status := range statuses {
		state, kind := loadStateForProfileStatus(status)
		if state != awsbrowser.LoadEmpty {
			return state, &awsbrowser.ProviderFailure{State: state, Kind: kind}
		}
	}
	return awsbrowser.LoadEmpty, nil
}

func loadStateForProfileStatus(status awsintegration.ProfileStatus) (awsbrowser.LoadState, awsbrowser.ProviderErrorKind) {
	switch status {
	case awsintegration.ProfileStatusMatched, awsintegration.ProfileStatusNotFound, "":
		return awsbrowser.LoadEmpty, ""
	case awsintegration.ProfileStatusForbidden:
		return awsbrowser.LoadForbidden, awsbrowser.ProviderForbidden
	case awsintegration.ProfileStatusAuthRequired:
		return awsbrowser.LoadAuthRequired, awsbrowser.ProviderAuthRequired
	case awsintegration.ProfileStatusThrottled:
		return awsbrowser.LoadThrottled, awsbrowser.ProviderThrottled
	case awsintegration.ProfileStatusTimedOut:
		return awsbrowser.LoadTimedOut, awsbrowser.ProviderTimedOut
	case awsintegration.ProfileStatusCancelled, awsintegration.ProfileStatusNotSearched:
		return awsbrowser.LoadCancelled, awsbrowser.ProviderCancelled
	case awsintegration.ProfileStatusUnsupported:
		return awsbrowser.LoadUnsupported, awsbrowser.ProviderUnsupported
	default:
		return awsbrowser.LoadUnknown, awsbrowser.ProviderUnknown
	}
}

func projectSearchResources(resources []awsintegration.CanonicalSearchResource) awsbrowser.IntentProjection {
	projection := awsbrowser.IntentProjection{Resources: make([]awsbrowser.ResourceProjection, 0, len(resources))}
	for _, resource := range resources {
		fields := map[string]any{}
		if len(resource.Observations) != 0 {
			fields = resource.Observations[0].Fields()
		}
		title := resource.Key.ID
		for _, name := range []string{"name", "role_name", "dns_name", "record_name"} {
			if value, ok := fields[name].(string); ok && strings.TrimSpace(value) != "" {
				title = value + " · " + resource.Key.ID
				break
			}
		}
		item := awsbrowser.ResourceProjection{Target: resource.Key.Type + ":" + resource.Key.ID, Title: safeAWSQueryText(title)}
		names := make([]string, 0, len(fields))
		for name := range fields {
			if name != "relations" && name != "alias_relation" && name != "zone_relation" {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			encoded, err := json.Marshal(fields[name])
			if err == nil {
				item.Fields = append(item.Fields, awsbrowser.ProjectionField{Label: name, Value: safeAWSQueryText(strings.Trim(string(encoded), `"`))})
			}
		}
		projection.Resources = append(projection.Resources, item)
	}
	return projection
}

type productionAWSQueryService struct{ search awsIntentSearch }

func (service *productionAWSQueryService) Execute(ctx context.Context, request awsQueryRequest) (awsQueryExecution, error) {
	if service == nil || service.search == nil || ctx == nil {
		return awsQueryExecution{}, errUnsupportedAWSIntent
	}
	searchRequest, ok := awsSearchRequestForQuery(request)
	if !ok {
		return awsQueryExecution{}, errUnsupportedAWSIntent
	}
	result, err := service.search.Submit(ctx, searchRequest)
	if err != nil {
		return awsQueryExecution{}, errUnsupportedAWSIntent
	}
	return awsQueryExecutionFromSearch(request, result), nil
}

func awsSearchRequestForQuery(request awsQueryRequest) (awsintegration.SearchRequest, bool) {
	intent := awsbrowser.Intent{Profile: request.Profile, Region: request.Region, Query: request.Value, Scope: request.Scope}
	switch request.Kind {
	case awsQueryKindEC2Instances:
		intent.SearchKind = "ec2-instances"
	case awsQueryKindDomainExact:
		intent.SearchKind = "domain"
	case awsQueryKindRoleExact:
		intent.SearchKind = "role"
	default:
		return awsintegration.SearchRequest{}, false
	}
	return awsSearchRequestForIntent(intent)
}

func awsQueryExecutionFromSearch(request awsQueryRequest, result awsintegration.SearchResult) awsQueryExecution {
	execution := awsQueryExecution{}
	execution.Coverage.Total = len(result.Coverage)
	contexts := make(map[string]awsbrowser.AWSContext)
	for _, resource := range result.Resources {
		for _, observation := range resource.Observations {
			if _, exists := contexts[observation.Context.Profile]; !exists {
				contexts[observation.Context.Profile] = observation.Context
			}
		}
	}
	for _, coverage := range result.Coverage {
		profile := awsQueryProfileCoverage{Profile: coverage.Profile, AccountID: coverage.AccountID, Status: string(coverage.Status), ResultCount: coverage.Matches}
		if coverage.Profile == "" {
			profile.Mode = string(awsbrowser.ContextModeAmbient)
		} else {
			profile.Mode = string(awsbrowser.ContextModeNamedProfile)
		}
		if awsContext, ok := contexts[coverage.Profile]; ok {
			profile.AccountID, profile.PrincipalARN = awsContext.AccountID, awsContext.PrincipalARN
		}
		execution.Coverage.Profiles = append(execution.Coverage.Profiles, profile)
		countAWSQueryStatus(&execution.Coverage, coverage.Status)
		if coverage.Status != awsintegration.ProfileStatusNotSearched {
			execution.Coverage.Completed++
			execution.Coverage.Searched++
		}
		if queryFailureStatus(coverage.Status) {
			provider, operation := searchProviderOperation(request.Kind)
			execution.Errors = append(execution.Errors, awsQueryFailure{Profile: coverage.Profile, Kind: string(coverage.Status), Service: provider, Operation: operation})
		}
	}
	for _, resource := range result.Resources {
		if len(resource.Observations) == 0 {
			continue
		}
		observation := resource.Observations[0]
		execution.Results = append(execution.Results, awsQueryResult{
			Resource:  awsQueryResource{Partition: resource.Key.Partition, AccountID: resource.Key.AccountID, Region: resource.Key.Region, Type: resource.Key.Type, ID: resource.Key.ID},
			Context:   awsQueryContext{Profile: observation.Context.Profile, AccountID: observation.Context.AccountID, PrincipalARN: observation.Context.PrincipalARN, RoleName: observation.Context.RoleName, Region: observation.Context.Region},
			FetchedAt: observation.FetchedAt, Fields: observation.Fields(), AvailableViaProfiles: append([]string(nil), resource.AvailableViaProfiles...),
		})
	}
	execution.Coverage.Partial = execution.Coverage.NotSearched != 0 || len(execution.Errors) != 0 || result.DiscoveryStatus != ""
	if result.DiscoveryStatus != "" {
		execution.Warnings = append(execution.Warnings, "AWS profile discovery was incomplete ("+string(result.DiscoveryStatus)+")")
	}
	if execution.Coverage.Partial {
		execution.Warnings = append(execution.Warnings, "AWS query coverage is partial")
	}
	return execution
}

func countAWSQueryStatus(coverage *awsQueryCoverage, status awsintegration.ProfileStatus) {
	switch status {
	case awsintegration.ProfileStatusMatched:
		coverage.Matched++
	case awsintegration.ProfileStatusNotFound:
		coverage.NotFound++
	case awsintegration.ProfileStatusForbidden:
		coverage.Forbidden++
	case awsintegration.ProfileStatusAuthRequired:
		coverage.AuthRequired++
	case awsintegration.ProfileStatusThrottled:
		coverage.Throttled++
	case awsintegration.ProfileStatusTimedOut:
		coverage.TimedOut++
	case awsintegration.ProfileStatusCancelled:
		coverage.Cancelled++
	case awsintegration.ProfileStatusUnsupported:
		coverage.Unsupported++
	case awsintegration.ProfileStatusNotSearched:
		coverage.NotSearched++
	default:
		coverage.Unknown++
	}
}

func queryFailureStatus(status awsintegration.ProfileStatus) bool {
	return status != awsintegration.ProfileStatusMatched && status != awsintegration.ProfileStatusNotFound && status != awsintegration.ProfileStatusNotSearched && status != ""
}

func searchProviderOperation(kind string) (string, string) {
	switch kind {
	case awsQueryKindEC2Instances:
		return awsbrowser.ProviderEC2, awsbrowser.OperationDescribeInstances
	case awsQueryKindRoleExact:
		return awsbrowser.ProviderIAM, awsbrowser.OperationGetRole
	case awsQueryKindDomainExact:
		return awsbrowser.ProviderRoute53, awsbrowser.OperationListHostedZones
	default:
		return "", ""
	}
}

var _ awsbrowser.IntentDispatcher = (*lazyAWSRuntime)(nil)
var _ awsbrowser.IntentDispatcher = (*awsIntentDispatcher)(nil)
var _ awsQueryService = (*productionAWSQueryService)(nil)
