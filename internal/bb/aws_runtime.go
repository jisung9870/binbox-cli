package bb

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
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
	Resolve(context.Context, awsintegration.ContextRequest) (awsintegration.ContextResult, error)
	ListContexts(context.Context) ([]awsbrowser.ContextChoice, error)
}

type awsIntentSearch interface {
	Submit(context.Context, awsintegration.SearchRequest) (awsintegration.SearchResult, error)
	Stream(context.Context, awsintegration.SearchRequest) (<-chan awsintegration.SearchUpdate, error)
}

type awsSnapshotQueryCore interface {
	Query(context.Context, awsintegration.Request) (awsintegration.Result, error)
}

type awsRuntime struct {
	core         awsIntentCore
	search       awsIntentSearch
	snapshotCore awsSnapshotQueryCore
}

// lazyAWSRuntime is shared by browse and query. Merely constructing App,
// Runner, or the browser Home screen performs no PATH lookup, CLI invocation,
// profile resolution, credential resolution, or SDK call.
type lazyAWSRuntime struct {
	app        *App
	once       sync.Once
	runtime    *awsRuntime
	err        error
	groupsOnce sync.Once
	groups     []awsbrowser.ContextGroup
	groupsErr  error
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
		runtime.runtime = &awsRuntime{core: core, search: search, snapshotCore: core}
	})
	return runtime.runtime, runtime.err
}

func (runtime *lazyAWSRuntime) Dispatch(ctx context.Context, intent awsbrowser.Intent) (awsbrowser.IntentStream, error) {
	if intent.Kind == awsbrowser.IntentIncoming {
		return runtime.dispatchIncomingRelations(ctx, intent)
	}
	binding, err := runtime.initialize()
	if err != nil {
		return nil, err
	}
	return (&awsIntentDispatcher{core: binding.core, search: binding.search}).Dispatch(ctx, intent)
}

func (runtime *lazyAWSRuntime) dispatchIncomingRelations(ctx context.Context, intent awsbrowser.Intent) (awsbrowser.IntentStream, error) {
	request, ok := awsIncomingSnapshotRequestForIntent(intent)
	if !ok || ctx == nil {
		return nil, errUnsupportedAWSIntent
	}
	groups, err := runtime.contextGroups()
	if err != nil {
		return nil, unavailable("AWS context groups are invalid")
	}
	group, ok := awsContextGroupForProfile(groups, intent.Profile)
	if !ok {
		return nil, unavailable("automatic incoming relations require the selected profile in an AWS context group")
	}
	syncService, err := runtime.SnapshotSyncService()
	if err != nil {
		return nil, err
	}
	readService, err := runtime.app.localSnapshotReadService()
	if err != nil {
		return nil, err
	}
	service := &awsAutoSnapshotService{
		sync: syncService, read: readService, groups: groups, now: runtime.app.now,
	}
	streamCtx, cancel := context.WithCancel(ctx)
	updates := make(chan awsbrowser.IntentUpdate, 2)
	go func() {
		defer close(updates)
		updates <- awsbrowser.IntentUpdate{
			Query: awsbrowser.QueryUpdate{Snapshot: awsbrowser.QuerySnapshot{State: awsbrowser.LoadLoading}},
			Graph: &awsbrowser.GraphSnapshot{Group: group.Name, Collecting: true},
		}
		execution, graph, resolveErr := service.Resolve(streamCtx, request)
		if resolveErr != nil {
			state := awsbrowser.LoadUnknown
			if errors.Is(resolveErr, context.Canceled) {
				state = awsbrowser.LoadCancelled
			}
			select {
			case updates <- awsbrowser.IntentUpdate{
				Query: awsbrowser.QueryUpdate{Snapshot: awsbrowser.QuerySnapshot{State: state}},
				Graph: &awsbrowser.GraphSnapshot{Group: group.Name, Error: true}, Done: true,
			}:
			case <-streamCtx.Done():
			}
			return
		}
		projection, projectErr := projectAWSIncomingSnapshot(execution, request)
		if projectErr != nil {
			select {
			case updates <- awsbrowser.IntentUpdate{
				Query: awsbrowser.QueryUpdate{Snapshot: awsbrowser.QuerySnapshot{State: awsbrowser.LoadUnknown}},
				Graph: &awsbrowser.GraphSnapshot{Group: group.Name, Error: true}, Done: true,
			}:
			case <-streamCtx.Done():
			}
			return
		}
		state := awsbrowser.LoadReady
		if len(projection.Resources) == 0 {
			state = awsbrowser.LoadEmpty
		}
		select {
		case updates <- awsbrowser.IntentUpdate{
			Query:      awsbrowser.QueryUpdate{Snapshot: awsbrowser.QuerySnapshot{State: state, FetchedAt: graph.CompletedAt}},
			Projection: projection, Graph: &graph, Done: true,
		}:
		case <-streamCtx.Done():
		}
	}()
	return &awsbrowser.ChannelIntentStream{C: updates, CancelFunc: cancel}, nil
}

func (runtime *lazyAWSRuntime) ListContexts(ctx context.Context) ([]awsbrowser.ContextChoice, error) {
	binding, err := runtime.initialize()
	if err != nil {
		return nil, err
	}
	choices, err := (&awsIntentDispatcher{core: binding.core, search: binding.search}).ListContexts(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := runtime.contextGroups()
	if err != nil {
		return nil, err
	}
	return awsbrowser.ApplyContextGroups(choices, groups), nil
}

func (runtime *lazyAWSRuntime) contextGroups() ([]awsbrowser.ContextGroup, error) {
	runtime.groupsOnce.Do(func() {
		configRoot, _, err := runtime.app.paths()
		if err != nil {
			runtime.groupsErr = awsbrowser.ErrInvalidContextGroups
			return
		}
		runtime.groups, runtime.groupsErr = awsbrowser.LoadContextGroups(filepath.Join(configRoot, awsbrowser.AWSContextGroupsFilename))
	})
	return runtime.groups, runtime.groupsErr
}

func (runtime *lazyAWSRuntime) ResolveContext(ctx context.Context, profile, region string) (awsbrowser.ContextResolution, error) {
	binding, err := runtime.initialize()
	if err != nil {
		return awsbrowser.ContextResolution{}, err
	}
	return (&awsIntentDispatcher{core: binding.core, search: binding.search}).ResolveContext(ctx, profile, region)
}

func (runtime *lazyAWSRuntime) QueryService() (awsQueryService, error) {
	binding, err := runtime.initialize()
	if err != nil {
		return nil, err
	}
	return &productionAWSQueryService{search: binding.search}, nil
}

func (runtime *lazyAWSRuntime) SnapshotSyncService() (awsSnapshotSyncService, error) {
	groups, err := runtime.contextGroups()
	if err != nil {
		return nil, unavailable("AWS context groups are invalid")
	}
	_, stateRoot, err := runtime.app.paths()
	if err != nil {
		return nil, err
	}
	return &productionAWSSnapshotSyncService{
		coreFactory: func() (awsSnapshotQueryCore, error) {
			binding, err := runtime.initialize()
			if err != nil {
				return nil, err
			}
			return binding.snapshotCore, nil
		},
		groups: groups, path: awsSnapshotPath(stateRoot), now: runtime.app.now,
	}, nil
}

type awsIntentDispatcher struct {
	core   awsIntentCore
	search awsIntentSearch
}

func (dispatcher *awsIntentDispatcher) ListContexts(ctx context.Context) ([]awsbrowser.ContextChoice, error) {
	if dispatcher == nil || dispatcher.core == nil || ctx == nil {
		return nil, errUnsupportedAWSIntent
	}
	choices, err := dispatcher.core.ListContexts(ctx)
	if err != nil {
		return nil, errUnsupportedAWSIntent
	}
	return choices, nil
}

func (dispatcher *awsIntentDispatcher) ResolveContext(ctx context.Context, profile, region string) (awsbrowser.ContextResolution, error) {
	if dispatcher == nil || dispatcher.core == nil || ctx == nil || awsbrowser.ValidateContextSelection(profile, region) != nil || profile == "" || region == "" {
		return awsbrowser.ContextResolution{}, errUnsupportedAWSIntent
	}
	result, err := dispatcher.core.Resolve(ctx, awsintegration.ContextRequest{Profile: profile, Region: region})
	if result.Failure != nil {
		failure := &awsbrowser.ProviderFailure{
			State: result.Failure.State, Kind: result.Failure.Kind, Service: result.Failure.Provider,
			Operation: result.Failure.Operation, Code: result.Failure.Code, RequestID: result.Failure.RequestID,
		}
		return awsbrowser.ContextResolution{Failure: failure}, nil
	}
	if err != nil || result.Context == nil || result.Context.Validate() != nil || result.Context.Profile != profile || result.Context.Region != region {
		return awsbrowser.ContextResolution{}, errUnsupportedAWSIntent
	}
	contextCopy := *result.Context
	return awsbrowser.ContextResolution{Context: &contextCopy}, nil
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
		if regions, err := multiRegionIntentRegions(intent); err != nil {
			return nil, errUnsupportedAWSIntent
		} else if len(regions) > 1 {
			return dispatcher.dispatchMultiRegion(ctx, request, regions), nil
		}
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

func multiRegionIntentRegions(intent awsbrowser.Intent) ([]string, error) {
	regions, err := awsbrowser.ParseRegionSet(intent.Regions, intent.Region)
	if err != nil {
		return nil, err
	}
	if len(regions) < 2 {
		return nil, nil
	}
	switch intent.Target {
	case "ec2-instances", "vpc-networking", "elbv2-load-balancers",
		"elbv2-application-load-balancers", "elbv2-network-load-balancers":
		return regions, nil
	default:
		return nil, nil
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
	case "elbv2-load-balancers":
		request.Provider, request.Operation = awsbrowser.ProviderELBV2, awsbrowser.OperationDescribeLoadBalancers
	case "elbv2-application-load-balancers":
		request.Provider, request.Operation = awsbrowser.ProviderELBV2, awsbrowser.OperationDescribeLoadBalancers
		request.Params = map[string]string{"load-balancer-type": "application"}
	case "elbv2-network-load-balancers":
		request.Provider, request.Operation = awsbrowser.ProviderELBV2, awsbrowser.OperationDescribeLoadBalancers
		request.Params = map[string]string{"load-balancer-type": "network"}
	default:
		resourceType, id, ok := strings.Cut(intent.Target, ":")
		if !ok || !awsbrowser.NavigableRelationTargetType(resourceType) || !safeAWSIntentParameter(id) {
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
		case "ec2.security-group-rules-inbound":
			request.Provider, request.Operation = awsbrowser.ProviderEC2, awsbrowser.OperationDescribeSecurityGroupRules
			request.Params["group-id"], request.Params["direction"] = id, "ingress"
		case "ec2.security-group-rules-outbound":
			request.Provider, request.Operation = awsbrowser.ProviderEC2, awsbrowser.OperationDescribeSecurityGroupRules
			request.Params["group-id"], request.Params["direction"] = id, "egress"
		case "ec2.vpc":
			request.Provider, request.Operation, request.Params["vpc-id"] = awsbrowser.ProviderEC2, awsbrowser.OperationDescribeVpcs, id
		case "ec2.subnet":
			request.Provider, request.Operation, request.Params["subnet-id"] = awsbrowser.ProviderEC2, awsbrowser.OperationDescribeSubnets, id
		case "ec2.route-table":
			request.Provider, request.Operation, request.Params["route-table-id"] = awsbrowser.ProviderEC2, awsbrowser.OperationDescribeRouteTables, id
		case "iam.role":
			request.Provider, request.Operation, request.Params["role-name"] = awsbrowser.ProviderIAM, awsbrowser.OperationGetRole, id
		case "iam.role-attached-policies":
			request.Provider, request.Operation, request.Params["role-name"] = awsbrowser.ProviderIAM, awsbrowser.OperationListAttachedRolePolicies, id
		case "iam.role-inline-policies":
			request.Provider, request.Operation, request.Params["role-name"] = awsbrowser.ProviderIAM, awsbrowser.OperationListRolePolicies, id
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
		case "route53.records":
			request.Provider, request.Operation, request.Params["hosted-zone-id"] = awsbrowser.ProviderRoute53, awsbrowser.OperationListResourceRecordSets, id
		case "cloudfront.distribution-domain":
			request.Provider, request.Operation, request.Params["distribution-domain"] = awsbrowser.ProviderCloudFront, awsbrowser.OperationListDistributions, id
		case "elbv2.load-balancer-dns":
			region, ok := awsbrowser.ELBV2RegionFromDNS("aws", id)
			if !ok {
				region, ok = awsbrowser.ELBV2RegionFromDNS("aws-cn", id)
			}
			if !ok {
				return awsintegration.Request{}, false
			}
			request.Region = region
			request.Provider, request.Operation, request.Params["load-balancer-dns"] = awsbrowser.ProviderELBV2, awsbrowser.OperationDescribeLoadBalancers, id
		case "elbv2.load-balancer":
			request.Provider, request.Operation, request.Params["load-balancer-arn"] = awsbrowser.ProviderELBV2, awsbrowser.OperationDescribeLoadBalancers, id
		case "elbv2.listeners":
			request.Provider, request.Operation, request.Params["load-balancer-arn"] = awsbrowser.ProviderELBV2, awsbrowser.OperationDescribeListeners, id
		case "elbv2.rules":
			request.Provider, request.Operation, request.Params["listener-arn"] = awsbrowser.ProviderELBV2, awsbrowser.OperationDescribeRules, id
		case "elbv2.target-group":
			request.Provider, request.Operation, request.Params["target-group-arn"] = awsbrowser.ProviderELBV2, awsbrowser.OperationDescribeTargetGroups, id
		case "elbv2.targets":
			values, err := url.ParseQuery(id)
			if err != nil || len(values["target-group-arn"]) != 1 || len(values["target-type"]) != 1 || len(values) != 2 {
				return awsintegration.Request{}, false
			}
			request.Provider, request.Operation = awsbrowser.ProviderELBV2, awsbrowser.OperationDescribeTargetHealth
			request.Params["target-group-arn"], request.Params["target-type"] = values.Get("target-group-arn"), values.Get("target-type")
		case "s3.bucket":
			request.Provider, request.Operation, request.Params["bucket"] = awsbrowser.ProviderS3, awsbrowser.OperationGetBucketLocation, id
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

type regionIntentEvent struct {
	region string
	update awsbrowser.IntentUpdate
}

type regionIntentState struct {
	projection awsbrowser.IntentProjection
	context    *awsbrowser.AWSContext
	query      awsbrowser.QueryUpdate
	done       bool
}

func (dispatcher *awsIntentDispatcher) dispatchMultiRegion(ctx context.Context, request awsintegration.Request, regions []string) awsbrowser.IntentStream {
	updates := make(chan awsbrowser.IntentUpdate, 1)
	queryCtx, cancel := context.WithCancel(ctx)
	stream := &awsbrowser.ChannelIntentStream{C: updates, CancelFunc: cancel}
	go func() {
		defer close(updates)
		defer cancel()
		events := make(chan regionIntentEvent, len(regions)*2)
		semaphore := make(chan struct{}, 2)
		currentReady := make(chan struct{})
		var currentReadyOnce sync.Once
		var workers sync.WaitGroup
		for regionIndex, region := range regions {
			regionIndex := regionIndex
			region := region
			workers.Add(1)
			go func() {
				defer workers.Done()
				if regionIndex != 0 {
					select {
					case <-currentReady:
					case <-queryCtx.Done():
						return
					}
				}
				select {
				case semaphore <- struct{}{}:
				case <-queryCtx.Done():
					return
				}
				defer func() { <-semaphore }()
				regional := request
				regional.Region = region
				subscription, err := dispatcher.core.Subscribe(queryCtx, regional)
				if regionIndex == 0 {
					currentReadyOnce.Do(func() { close(currentReady) })
				}
				if err != nil {
					emitRegionIntentEvent(queryCtx, events, regionIntentEvent{region: region, update: unsupportedIntentUpdate()})
					return
				}
				defer subscription.Unsubscribe()
				terminal := false
				for raw := range subscription.Updates() {
					converted := intentUpdateFromIntegration(raw)
					terminal = terminal || converted.Done || multiRegionTerminal(converted.Query.Snapshot.State)
					if !emitRegionIntentEvent(queryCtx, events, regionIntentEvent{region: region, update: converted}) {
						return
					}
					if terminal {
						return
					}
				}
				if !terminal {
					failure := unsupportedIntentUpdate()
					failure.Query.Snapshot.State = awsbrowser.LoadUnknown
					failure.Query.Failure.State = awsbrowser.LoadUnknown
					emitRegionIntentEvent(queryCtx, events, regionIntentEvent{region: region, update: failure})
				}
			}()
		}
		go func() {
			workers.Wait()
			close(events)
		}()

		states := make(map[string]*regionIntentState, len(regions))
		for _, region := range regions {
			states[region] = &regionIntentState{}
		}
		for event := range events {
			state := states[event.region]
			state.query = event.update.Query
			if event.update.Context != nil && event.update.Context.Validate() == nil {
				copy := *event.update.Context
				state.context = &copy
			}
			projection := projectionWithContext(event.update.Projection, state.context)
			if projection.Resources != nil || event.update.Query.Snapshot.State == awsbrowser.LoadEmpty {
				state.projection = projection
			}
			state.done = state.done || event.update.Done || multiRegionTerminal(event.update.Query.Snapshot.State)
			aggregate := aggregateRegionIntent(request.Profile, request.Region, regions, states)
			select {
			case updates <- aggregate:
			case <-queryCtx.Done():
				return
			}
			if aggregate.Done {
				return
			}
		}
	}()
	return stream
}

func emitRegionIntentEvent(ctx context.Context, events chan<- regionIntentEvent, event regionIntentEvent) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func multiRegionTerminal(state awsbrowser.LoadState) bool {
	switch state {
	case awsbrowser.LoadReady, awsbrowser.LoadEmpty, awsbrowser.LoadStale, awsbrowser.LoadForbidden,
		awsbrowser.LoadAuthRequired, awsbrowser.LoadThrottled, awsbrowser.LoadTimedOut,
		awsbrowser.LoadCancelled, awsbrowser.LoadUnsupported, awsbrowser.LoadUnknown:
		return true
	default:
		return false
	}
}

func projectionWithContext(projection awsbrowser.IntentProjection, awsContext *awsbrowser.AWSContext) awsbrowser.IntentProjection {
	result := awsbrowser.IntentProjection{Resources: append([]awsbrowser.ResourceProjection(nil), projection.Resources...)}
	if awsContext == nil || awsContext.Validate() != nil {
		return result
	}
	for index := range result.Resources {
		copy := *awsContext
		result.Resources[index].Context = &copy
	}
	return result
}

func aggregateRegionIntent(profile, current string, regions []string, states map[string]*regionIntentState) awsbrowser.IntentUpdate {
	result := awsbrowser.IntentUpdate{
		Query:    awsbrowser.QueryUpdate{Snapshot: awsbrowser.QuerySnapshot{State: awsbrowser.LoadLoading}},
		Coverage: &awsbrowser.SearchCoverage{Profiles: make([]awsbrowser.SearchProfileCoverage, 0, len(regions))},
	}
	regionOrder := make(map[string]int, len(regions))
	completed, successful := 0, 0
	var firstFailure *awsbrowser.ProviderFailure
	for index, region := range regions {
		regionOrder[region] = index
		state := states[region]
		coverage := awsbrowser.SearchProfileCoverage{Profile: profile, Region: region, Current: region == current, Status: "not_searched"}
		if state.context != nil {
			coverage.AccountID = state.context.AccountID
			if region == current {
				copy := *state.context
				result.Context = &copy
				result.Query.Key = state.query.Key
			}
		}
		if len(state.projection.Resources) != 0 {
			coverage.Status = "matched"
			coverage.Matches = len(state.projection.Resources)
			resources := append([]awsbrowser.ResourceProjection(nil), state.projection.Resources...)
			for index := range resources {
				if resources[index].Subtitle == "" {
					resources[index].Subtitle = region
				} else {
					resources[index].Subtitle += " · " + region
				}
			}
			result.Projection.Resources = append(result.Projection.Resources, resources...)
		} else if state.done && state.query.Failure == nil {
			coverage.Status = "not_found"
		} else if state.query.Failure != nil {
			coverage.Status = string(state.query.Failure.State)
		}
		if state.query.Failure != nil && firstFailure == nil {
			copy := *state.query.Failure
			firstFailure = &copy
		}
		if state.done {
			completed++
			if state.query.Failure == nil {
				successful++
			}
		}
		result.Coverage.Profiles = append(result.Coverage.Profiles, coverage)
	}
	sort.SliceStable(result.Projection.Resources, func(left, right int) bool {
		leftRegion, rightRegion := "", ""
		if result.Projection.Resources[left].Context != nil {
			leftRegion = result.Projection.Resources[left].Context.Region
		}
		if result.Projection.Resources[right].Context != nil {
			rightRegion = result.Projection.Resources[right].Context.Region
		}
		if regionOrder[leftRegion] != regionOrder[rightRegion] {
			return regionOrder[leftRegion] < regionOrder[rightRegion]
		}
		if result.Projection.Resources[left].Title != result.Projection.Resources[right].Title {
			return result.Projection.Resources[left].Title < result.Projection.Resources[right].Title
		}
		return result.Projection.Resources[left].Target < result.Projection.Resources[right].Target
	})
	result.Coverage.Partial = firstFailure != nil
	if completed != len(regions) {
		return result
	}
	result.Done = true
	if len(result.Projection.Resources) != 0 {
		result.Query.Snapshot.State = awsbrowser.LoadReady
	} else if successful != 0 {
		result.Query.Snapshot.State = awsbrowser.LoadEmpty
	} else if firstFailure != nil {
		result.Query.Snapshot.State = firstFailure.State
		result.Query.Failure = firstFailure
	} else {
		result.Query.Snapshot.State = awsbrowser.LoadUnknown
	}
	return result
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
	converted.Projection = projectionWithContext(converted.Projection, converted.Context)
	return converted
}

func (dispatcher *awsIntentDispatcher) dispatchSearch(ctx context.Context, request awsintegration.SearchRequest) awsbrowser.IntentStream {
	updates := make(chan awsbrowser.IntentUpdate, 1)
	searchCtx, cancel := context.WithCancel(ctx)
	stream := &awsbrowser.ChannelIntentStream{C: updates, CancelFunc: cancel}
	go func() {
		defer close(updates)
		defer cancel()
		searchUpdates, err := dispatcher.search.Stream(searchCtx, request)
		if err != nil {
			update := unsupportedIntentUpdate()
			select {
			case updates <- update:
			case <-searchCtx.Done():
			}
			return
		}
		for searchUpdate := range searchUpdates {
			update := intentUpdateFromSearch(searchUpdate.Result)
			update.Done = searchUpdate.Done
			select {
			case updates <- update:
			case <-searchCtx.Done():
				return
			}
			if update.Done {
				return
			}
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
		Projection: projectSearchResources(result), Coverage: searchCoverageProjection(result),
	}
}

func searchCoverageProjection(result awsintegration.SearchResult) *awsbrowser.SearchCoverage {
	coverage := &awsbrowser.SearchCoverage{DiscoveryStatus: safeAWSQueryText(string(result.DiscoveryStatus))}
	coverage.Profiles = make([]awsbrowser.SearchProfileCoverage, 0, len(result.Coverage))
	coverage.Partial = result.DiscoveryStatus != ""
	for _, profile := range result.Coverage {
		coverage.Profiles = append(coverage.Profiles, awsbrowser.SearchProfileCoverage{
			Profile: safeAWSQueryText(profile.Profile), Region: safeAWSQueryText(profile.Region), AccountID: safeAWSQueryText(profile.AccountID),
			Status: safeAWSQueryText(string(profile.Status)), Current: profile.Current, Matches: profile.Matches,
		})
		if profile.Status != awsintegration.ProfileStatusMatched && profile.Status != awsintegration.ProfileStatusNotFound && profile.Status != "" {
			coverage.Partial = true
		}
	}
	return coverage
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

func projectSearchResources(result awsintegration.SearchResult) awsbrowser.IntentProjection {
	projection := awsbrowser.IntentProjection{Resources: make([]awsbrowser.ResourceProjection, 0, len(result.Resources))}
	currentProfiles := make(map[string]bool, len(result.Coverage))
	for _, coverage := range result.Coverage {
		currentProfiles[coverage.Profile] = coverage.Current
	}
	for _, resource := range result.Resources {
		fields := map[string]any{}
		var resourceContext *awsbrowser.AWSContext
		if len(resource.Observations) != 0 {
			observation := resource.Observations[0]
			fields = observation.Fields()
			if observation.Context.Validate() == nil {
				copy := observation.Context
				resourceContext = &copy
			}
		}
		item := awsbrowser.ProjectResourceFields(resource.Key, fields)
		item.Context = resourceContext
		item.AvailableViaProfiles = append([]string(nil), resource.AvailableViaProfiles...)
		for index := range item.AvailableViaProfiles {
			item.AvailableViaProfiles[index] = safeAWSQueryText(item.AvailableViaProfiles[index])
		}
		if resourceContext != nil {
			item.Current = currentProfiles[resourceContext.Profile]
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
