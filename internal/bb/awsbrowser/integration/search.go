package integration

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
)

var (
	ErrInvalidSearchOptions = errors.New("invalid AWS browser search options")
	ErrInvalidSearchRequest = errors.New("invalid AWS browser search request")
)

// SearchKind is one of the deliberately small P0 search strategies.
type SearchKind string

const (
	SearchEC2Instances SearchKind = "ec2-instances"
	SearchDomain       SearchKind = "domain"
	SearchRole         SearchKind = "role"
)

// SearchScope controls whether Submit searches just the selected context or
// discovers all AWS CLI profiles. EC2 instance search is current-only.
type SearchScope string

const (
	SearchCurrent SearchScope = "current"
	SearchAll     SearchScope = "all"
)

// ProfileStatus is safe to retain and display. It contains no provider error
// message, SDK response, CLI output, or credential value.
type ProfileStatus string

const (
	ProfileStatusMatched      ProfileStatus = "matched"
	ProfileStatusNotFound     ProfileStatus = "not_found"
	ProfileStatusForbidden    ProfileStatus = "forbidden"
	ProfileStatusAuthRequired ProfileStatus = "auth_required"
	ProfileStatusThrottled    ProfileStatus = "throttled"
	ProfileStatusTimedOut     ProfileStatus = "timed_out"
	ProfileStatusCancelled    ProfileStatus = "cancelled"
	ProfileStatusUnsupported  ProfileStatus = "unsupported"
	ProfileStatusUnknown      ProfileStatus = "unknown"
	ProfileStatusNotSearched  ProfileStatus = "not_searched"
)

// SearchRequest contains only profile selection and an exact search value.
// Query is unused for an unfiltered EC2 instance list.
type SearchRequest struct {
	Kind    SearchKind
	Scope   SearchScope
	Query   string
	Profile string
	Region  string
}

// ProfileCoverage is emitted in scope order: current first, followed by the
// exact AWS CLI profile order after exact named-spec deduplication.
type ProfileCoverage struct {
	Profile   string
	Region    string
	Current   bool
	AccountID string
	Status    ProfileStatus
	Matches   int
}

// CanonicalSearchResource merges equal credential-free ResourceKeys while
// retaining every profile-scoped observation and every profile through which
// the canonical resource was available.
type CanonicalSearchResource struct {
	Key                  awsbrowser.ResourceKey
	Observations         []awsbrowser.ResourceObservation
	AvailableViaProfiles []string
}

// SearchResource is a concise compatibility name for the canonical result.
type SearchResource = CanonicalSearchResource

// SearchResult is successful data even when empty, partial, or cancelled.
// DiscoveryStatus is set only when all-scope profile discovery failed.
type SearchResult struct {
	Resources       []CanonicalSearchResource
	Coverage        []ProfileCoverage
	DiscoveryStatus ProfileStatus
}

// SearchUpdate is a cumulative, credential-free search snapshot. Done marks
// the final projection and coverage for the request.
type SearchUpdate struct {
	Result SearchResult
	Done   bool
}

// SearchCore is the narrow, credential-free seam used by SearchService.
// *Core satisfies it without exposing runtime or credential objects.
type SearchCore interface {
	Resolve(context.Context, ContextRequest) (ContextResult, error)
	Query(context.Context, Request) (Result, error)
}

// SearchService performs no discovery, credential resolution, or SDK work
// until Submit is called.
type SearchService struct {
	core        SearchCore
	profiles    awsbrowser.ProfileLister
	env         []string
	credentials chan struct{}
	sdk         chan struct{}
	limitersMu  sync.Mutex
	accounts    map[string]*searchAccountLimiters
}

func NewSearchService(core SearchCore, profiles awsbrowser.ProfileLister, env []string) (*SearchService, error) {
	if nilSearchInterface(core) || nilSearchInterface(profiles) {
		return nil, ErrInvalidSearchOptions
	}
	return &SearchService{
		core: core, profiles: profiles, env: append([]string(nil), env...),
		credentials: make(chan struct{}, 4), sdk: make(chan struct{}, 4),
		accounts: make(map[string]*searchAccountLimiters),
	}, nil
}

type searchAccountLimiters struct {
	sdk     chan struct{}
	route53 chan struct{}
}

type searchProfile struct {
	profile string
	region  string
	current bool
	context *awsbrowser.AWSContext
	status  ProfileStatus
	matches int
	started bool
	items   []awsbrowser.ObservedResource
}

// Submit consumes the progressive stream for callers that need only the final
// result. Context cancellation is represented in coverage, rather than
// returned as an error.
func (service *SearchService) Submit(ctx context.Context, request SearchRequest) (SearchResult, error) {
	updates, err := service.Stream(ctx, request)
	if err != nil {
		return SearchResult{}, err
	}
	var result SearchResult
	for update := range updates {
		result = update.Result
	}
	return result, nil
}

// Stream emits the current profile before profile discovery or any secondary
// profile can delay delivery. Later cumulative updates are emitted as bounded
// per-profile work completes, with exactly one terminal update.
func (service *SearchService) Stream(ctx context.Context, request SearchRequest) (<-chan SearchUpdate, error) {
	if service == nil || nilSearchInterface(service.core) || nilSearchInterface(service.profiles) || ctx == nil || !validSearchRequest(request) {
		return nil, ErrInvalidSearchRequest
	}
	updates := make(chan SearchUpdate, 1)
	go func() {
		defer close(updates)
		current := &searchProfile{profile: request.Profile, region: request.Region, current: true}
		type discoveryResult struct {
			profiles []*searchProfile
			status   ProfileStatus
		}
		discovered := make(chan discoveryResult, 1)
		if request.Scope == SearchAll {
			go func() {
				profiles, status := service.secondaryScope(ctx, request)
				discovered <- discoveryResult{profiles: profiles, status: status}
			}()
		}

		service.processProfile(ctx, request, current)
		profiles := []*searchProfile{current}
		if request.Scope == SearchCurrent {
			updates <- SearchUpdate{Result: buildSearchResult(profiles, ""), Done: true}
			return
		}
		select {
		case updates <- SearchUpdate{Result: buildSearchResult(profiles, "")}:
		case <-ctx.Done():
			// Submit still receives the buffered terminal snapshot below.
		}

		discovery := <-discovered
		profiles = append(profiles, discovery.profiles...)
		if len(discovery.profiles) == 0 {
			terminal := SearchUpdate{Result: buildSearchResult(profiles, discovery.status), Done: true}
			if ctx.Err() == nil {
				updates <- terminal
			} else {
				select {
				case updates <- terminal:
				default:
				}
			}
			return
		}

		completed := make(chan *searchProfile, len(discovery.profiles))
		for _, profile := range discovery.profiles {
			go func(profile *searchProfile) {
				service.processProfile(ctx, request, profile)
				completed <- profile
			}(profile)
		}
		done := map[*searchProfile]bool{current: true}
		for count := 0; count < len(discovery.profiles); count++ {
			done[<-completed] = true
			result := buildCompletedSearchResult(profiles, done, discovery.status)
			terminal := count == len(discovery.profiles)-1
			update := SearchUpdate{Result: result, Done: terminal}
			if terminal {
				if ctx.Err() == nil {
					updates <- update
				} else {
					select {
					case updates <- update:
					default:
					}
				}
				continue
			}
			select {
			case updates <- update:
			case <-ctx.Done():
			}
		}
	}()
	return updates, nil
}

func (service *SearchService) secondaryScope(ctx context.Context, request SearchRequest) ([]*searchProfile, ProfileStatus) {
	var result []*searchProfile
	names, err := service.profiles.ListProfiles(ctx, append([]string(nil), service.env...))
	if err != nil {
		return result, statusFromError(err, nil)
	}
	seen := make(map[string]struct{}, len(names)+1)
	if request.Profile != "" {
		seen[request.Profile] = struct{}{}
	}
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, &searchProfile{profile: name, region: request.Region})
	}
	return result, ""
}

func (service *SearchService) processProfile(ctx context.Context, request SearchRequest, profile *searchProfile) {
	release, ok := acquireSearch(ctx, service.credentials)
	if !ok {
		profile.status = ProfileStatusNotSearched
		return
	}
	service.resolveOne(ctx, profile)
	release()
	if profile.context != nil {
		service.searchProfile(ctx, request, profile)
	}
	if profile.context != nil && !profile.started && profile.status == "" {
		profile.status = ProfileStatusNotSearched
	}
}

func (service *SearchService) resolveOne(ctx context.Context, profile *searchProfile) {
	if ctx.Err() != nil {
		profile.status = ProfileStatusNotSearched
		return
	}
	result, err := service.core.Resolve(ctx, ContextRequest{Profile: profile.profile, Region: profile.region})
	if result.Context != nil && result.Failure == nil && err == nil {
		copy := *result.Context
		profile.context = &copy
		return
	}
	profile.status = statusFromError(err, result.Failure)
}

func (service *SearchService) searchProfile(ctx context.Context, request SearchRequest, profile *searchProfile) {
	if ctx.Err() != nil {
		profile.status = ProfileStatusNotSearched
		return
	}
	profile.started = true
	account := profile.context.Partition + ":" + profile.context.AccountID
	limiters := service.accountLimiters(account)
	query := func(provider, operation string, params map[string]string) (Result, error, bool) {
		semaphores := []chan struct{}{limiters.sdk}
		if provider == awsbrowser.ProviderRoute53 {
			semaphores = []chan struct{}{limiters.route53, limiters.sdk}
		}
		semaphores = append(semaphores, service.sdk)
		release, ok := acquireSearch(ctx, semaphores...)
		if !ok {
			return Result{}, nil, false
		}
		defer release()
		if ctx.Err() != nil {
			return Result{}, nil, false
		}
		result, err := service.core.Query(ctx, Request{
			Profile: profile.profile, Region: profile.region, Provider: provider,
			Operation: operation, Params: params,
		})
		return result, err, true
	}

	var status ProfileStatus
	switch request.Kind {
	case SearchEC2Instances:
		result, err, started := query(awsbrowser.ProviderEC2, awsbrowser.OperationDescribeInstances, searchEC2Params(request.Query))
		if !started {
			status = ProfileStatusNotSearched
			break
		}
		profile.items = append(profile.items, resourcesFromResult(result)...)
		status = queryStatus(err, result.Update.Failure, len(profile.items), false)
	case SearchRole:
		result, err, started := query(awsbrowser.ProviderIAM, awsbrowser.OperationGetRole, map[string]string{"role-name": request.Query})
		if !started {
			status = ProfileStatusNotSearched
			break
		}
		profile.items = append(profile.items, resourcesFromResult(result)...)
		status = queryStatus(err, result.Update.Failure, len(profile.items), true)
	case SearchDomain:
		result, err, started := query(awsbrowser.ProviderRoute53, awsbrowser.OperationListHostedZones, nil)
		if !started {
			status = ProfileStatusNotSearched
			break
		}
		if err != nil || result.Update.Failure != nil {
			status = queryStatus(err, result.Update.Failure, 0, false)
			break
		}
		zones := matchingZones(resourcesFromResult(result), canonicalDomain(request.Query))
		status = ProfileStatusNotFound
		for _, zone := range zones {
			zoneID, _ := zone.Observation.Fields()["id"].(string)
			records, recordErr, recordStarted := query(awsbrowser.ProviderRoute53, awsbrowser.OperationListResourceRecordSets, map[string]string{
				"hosted-zone-id": zoneID,
				"record-name":    canonicalDomain(request.Query),
			})
			if !recordStarted {
				status = ProfileStatusNotSearched
				break
			}
			profile.items = append(profile.items, exactDomainResources(resourcesFromResult(records), canonicalDomain(request.Query))...)
			if recordErr != nil || records.Update.Failure != nil {
				status = queryStatus(recordErr, records.Update.Failure, len(profile.items), false)
				break
			}
		}
		if len(profile.items) != 0 && (status == ProfileStatusNotFound || status == "") {
			status = ProfileStatusMatched
		}
	}
	profile.matches = len(profile.items)
	profile.status = status
}

func (service *SearchService) accountLimiters(account string) *searchAccountLimiters {
	service.limitersMu.Lock()
	defer service.limitersMu.Unlock()
	limiters := service.accounts[account]
	if limiters == nil {
		limiters = &searchAccountLimiters{sdk: make(chan struct{}, 2), route53: make(chan struct{}, 1)}
		service.accounts[account] = limiters
	}
	return limiters
}

func acquireSearch(ctx context.Context, semaphores ...chan struct{}) (func(), bool) {
	acquired := make([]chan struct{}, 0, len(semaphores))
	for _, semaphore := range semaphores {
		select {
		case semaphore <- struct{}{}:
			acquired = append(acquired, semaphore)
			if ctx.Err() != nil {
				for index := len(acquired) - 1; index >= 0; index-- {
					<-acquired[index]
				}
				return nil, false
			}
		case <-ctx.Done():
			for index := len(acquired) - 1; index >= 0; index-- {
				<-acquired[index]
			}
			return nil, false
		}
	}
	return func() {
		for index := len(acquired) - 1; index >= 0; index-- {
			<-acquired[index]
		}
	}, true
}

func buildSearchResult(profiles []*searchProfile, discovery ProfileStatus) SearchResult {
	result := SearchResult{Coverage: make([]ProfileCoverage, len(profiles)), DiscoveryStatus: discovery}
	profileOrder := make(map[string]int, len(profiles))
	merged := make(map[awsbrowser.ResourceKey]*CanonicalSearchResource)
	for index, profile := range profiles {
		result.Coverage[index] = ProfileCoverage{
			Profile: profile.profile, Region: profile.region, Current: profile.current,
			Status: profile.status, Matches: profile.matches,
		}
		if profile.context != nil {
			result.Coverage[index].AccountID = profile.context.AccountID
		}
		profileOrder[profile.profile] = index
		for _, item := range profile.items {
			resource := merged[item.Key]
			if resource == nil {
				resource = &CanonicalSearchResource{Key: item.Key}
				merged[item.Key] = resource
			}
			resource.Observations = append(resource.Observations, item.Observation)
			if !containsString(resource.AvailableViaProfiles, profile.profile) {
				resource.AvailableViaProfiles = append(resource.AvailableViaProfiles, profile.profile)
			}
		}
	}
	result.Resources = make([]CanonicalSearchResource, 0, len(merged))
	for _, resource := range merged {
		sort.SliceStable(resource.Observations, func(left, right int) bool {
			return profileOrder[resource.Observations[left].Context.Profile] < profileOrder[resource.Observations[right].Context.Profile]
		})
		result.Resources = append(result.Resources, *resource)
	}
	sort.Slice(result.Resources, func(left, right int) bool {
		return searchResourceSortKey(result.Resources[left].Key) < searchResourceSortKey(result.Resources[right].Key)
	})
	return result
}

func buildCompletedSearchResult(profiles []*searchProfile, completed map[*searchProfile]bool, discovery ProfileStatus) SearchResult {
	ready := make([]*searchProfile, 0, len(completed))
	for _, profile := range profiles {
		if completed[profile] {
			ready = append(ready, profile)
		}
	}
	return buildSearchResult(ready, discovery)
}

func resourcesFromResult(result Result) []awsbrowser.ObservedResource {
	var resources []awsbrowser.ObservedResource
	for _, page := range result.Update.Snapshot.Pages() {
		resources = append(resources, page.Resources()...)
	}
	return resources
}

func matchingZones(resources []awsbrowser.ObservedResource, domain string) []awsbrowser.ObservedResource {
	result := make([]awsbrowser.ObservedResource, 0)
	for _, resource := range resources {
		fields := resource.Observation.Fields()
		name, _ := fields["name"].(string)
		name = canonicalDomain(name)
		if domain == name || strings.HasSuffix(domain, "."+strings.TrimSuffix(name, ".")+".") {
			result = append(result, resource)
		}
	}
	sort.SliceStable(result, func(left, right int) bool {
		leftName, _ := result[left].Observation.Fields()["name"].(string)
		rightName, _ := result[right].Observation.Fields()["name"].(string)
		return len(leftName) > len(rightName)
	})
	return result
}

func exactDomainResources(resources []awsbrowser.ObservedResource, domain string) []awsbrowser.ObservedResource {
	result := make([]awsbrowser.ObservedResource, 0, len(resources))
	for _, resource := range resources {
		name, _ := resource.Observation.Fields()["name"].(string)
		if canonicalDomain(name) == domain {
			result = append(result, resource)
		}
	}
	return result
}

func queryStatus(err error, failure *Failure, matches int, roleNotFound bool) ProfileStatus {
	if err == nil && failure == nil {
		if matches != 0 {
			return ProfileStatusMatched
		}
		return ProfileStatusNotFound
	}
	if roleNotFound && awsbrowser.IsProviderNotFound(err) {
		return ProfileStatusNotFound
	}
	return statusFromError(err, failure)
}

func statusFromError(err error, failure *Failure) ProfileStatus {
	if failure != nil {
		switch failure.State {
		case awsbrowser.LoadForbidden:
			return ProfileStatusForbidden
		case awsbrowser.LoadAuthRequired:
			return ProfileStatusAuthRequired
		case awsbrowser.LoadThrottled:
			return ProfileStatusThrottled
		case awsbrowser.LoadTimedOut:
			return ProfileStatusTimedOut
		case awsbrowser.LoadCancelled:
			return ProfileStatusCancelled
		case awsbrowser.LoadUnsupported:
			return ProfileStatusUnsupported
		}
	}
	if errors.Is(err, context.Canceled) {
		return ProfileStatusCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ProfileStatusTimedOut
	}
	var provider *awsbrowser.ProviderError
	if errors.As(err, &provider) {
		switch provider.Kind {
		case awsbrowser.ProviderForbidden:
			return ProfileStatusForbidden
		case awsbrowser.ProviderAuthRequired:
			return ProfileStatusAuthRequired
		case awsbrowser.ProviderThrottled:
			return ProfileStatusThrottled
		case awsbrowser.ProviderTimedOut:
			return ProfileStatusTimedOut
		case awsbrowser.ProviderCancelled:
			return ProfileStatusCancelled
		case awsbrowser.ProviderUnsupported:
			return ProfileStatusUnsupported
		}
	}
	var credential *awsbrowser.CredentialError
	if errors.As(err, &credential) {
		switch credential.Kind {
		case awsbrowser.CredentialAuthRequired:
			return ProfileStatusAuthRequired
		case awsbrowser.CredentialUnsupported:
			return ProfileStatusUnsupported
		case awsbrowser.CredentialCancelled:
			return ProfileStatusCancelled
		}
	}
	var cli *awsbrowser.CLIError
	if errors.As(err, &cli) {
		switch cli.Kind {
		case awsbrowser.CLIAuthRequired:
			return ProfileStatusAuthRequired
		case awsbrowser.CLIUnsupported:
			return ProfileStatusUnsupported
		case awsbrowser.CLICancelled:
			return ProfileStatusCancelled
		}
	}
	if err != nil {
		classified := awsbrowser.ClassifyProviderError(err, "", "")
		if errors.As(classified, &provider) {
			switch provider.Kind {
			case awsbrowser.ProviderForbidden:
				return ProfileStatusForbidden
			case awsbrowser.ProviderAuthRequired:
				return ProfileStatusAuthRequired
			case awsbrowser.ProviderThrottled:
				return ProfileStatusThrottled
			}
		}
	}
	return ProfileStatusUnknown
}

func validSearchRequest(request SearchRequest) bool {
	if request.Scope != SearchCurrent && request.Scope != SearchAll || strings.TrimSpace(request.Profile) != request.Profile || strings.TrimSpace(request.Region) != request.Region ||
		!validExplicitContextRequest(request.Profile, request.Region) {
		return false
	}
	switch request.Kind {
	case SearchEC2Instances:
		return request.Scope == SearchCurrent && (request.Query == "" || strings.TrimSpace(request.Query) == request.Query)
	case SearchRole:
		return request.Query == strings.TrimSpace(request.Query) && request.Query != "" && len(request.Query) <= 64 && safeSearchName(request.Query)
	case SearchDomain:
		return validSearchDomain(request.Query)
	default:
		return false
	}
}

func validSearchDomain(value string) bool {
	if value != strings.TrimSpace(value) || value == "" || len(value) > 254 || strings.Contains(value, "..") {
		return false
	}
	value = strings.TrimSuffix(value, ".")
	for index, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		if label == "*" && index == 0 {
			continue
		}
		for _, character := range label {
			if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-' || character == '_') {
				return false
			}
		}
	}
	return true
}

func safeSearchName(value string) bool {
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("_+=,.@-", character)) {
			return false
		}
	}
	return true
}

func canonicalDomain(value string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), ".")) + "."
}

func searchEC2Params(query string) map[string]string {
	if query == "" {
		return nil
	}
	return map[string]string{"instance-id": query}
}

func containsString(values []string, value string) bool {
	for _, existing := range values {
		if existing == value {
			return true
		}
	}
	return false
}

func searchResourceSortKey(key awsbrowser.ResourceKey) string {
	return strings.Join([]string{key.Partition, key.AccountID, key.Region, key.Type, key.ID}, "\x00")
}

func nilSearchInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
