// Package integration composes the AWS browser's credential-free query core.
//
// It is deliberately a leaf package: awsbrowser never imports this package or
// providers, while integration may import both without creating a cycle.
package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser/providers"
)

var (
	ErrInvalidOptions = errors.New("invalid AWS browser integration options")
	ErrInvalidRequest = errors.New("invalid AWS browser query request")
)

// ProductionOptions contains construction-only dependencies. Construction is
// local and lazy: it starts no process, loads no profile, and makes no AWS call.
type ProductionOptions struct {
	AWSCLIPath  string
	Env         []string
	Clock       func() time.Time
	Concurrency int
}

// Request is the credential-free query surface shared by interactive and
// non-interactive callers. An empty Profile selects the ambient context.
type Request struct {
	Profile   string
	Region    string
	Provider  string
	Operation string
	Params    map[string]string
	Refresh   bool
}

// ContextRequest identifies a context without carrying credential material.
type ContextRequest struct {
	Profile string
	Region  string
}

// Coverage records how far a request progressed without retaining an error,
// SDK response, configuration value, or credential material.
type Coverage struct {
	ContextResolved bool
	QueryStarted    bool
	Completed       bool
}

// Failure is safe to render, serialize, or retain. It never wraps its cause.
type Failure struct {
	State     awsbrowser.LoadState
	Kind      awsbrowser.ProviderErrorKind
	Provider  string
	Operation string
	Code      string
	RequestID string
}

func (*Failure) Error() string { return "AWS browser query failed" }

// Update is the credential-free stream item returned to TUI and CLI callers.
// Key is absent when context resolution failed before a verified QueryKey
// could be constructed.
type Update struct {
	Key      *awsbrowser.QueryKey
	Snapshot awsbrowser.QuerySnapshot
	Coverage Coverage
	Failure  *Failure
}

// Result is the terminal item from a one-shot Query call.
type Result struct {
	Update Update
}

// ContextResult is the sanitized result of an explicit context resolution.
type ContextResult struct {
	Context  *awsbrowser.AWSContext
	Coverage Coverage
	Failure  *Failure
}

// Subscription owns exactly one underlying coordinator subscription.
type Subscription struct {
	updates <-chan Update
	stop    func()
	once    sync.Once
}

func (subscription *Subscription) Updates() <-chan Update {
	if subscription == nil {
		return nil
	}
	return subscription.updates
}

func (subscription *Subscription) Unsubscribe() {
	if subscription != nil {
		subscription.once.Do(subscription.stop)
	}
}

// Core owns one in-memory session store, context registry, runtime-fenced
// provider multiplexer, and query coordinator.
type Core struct {
	registry    *awsbrowser.ContextRegistry
	coordinator *awsbrowser.QueryCoordinator
	contexts    contextResolver
	profiles    awsbrowser.ProfileLister
}

// NewProduction constructs the production core without resolving a context.
func NewProduction(options ProductionOptions) (*Core, error) {
	if strings.TrimSpace(options.AWSCLIPath) == "" || options.Clock == nil || options.Concurrency < 1 {
		return nil, ErrInvalidOptions
	}
	env := options.Env
	if env == nil {
		env = os.Environ()
	}
	factory, err := awsbrowser.NewRuntimeFactory(options.AWSCLIPath, env)
	if err != nil {
		return nil, ErrInvalidOptions
	}
	return newCore(factory, env, options.Clock, options.Concurrency, awsbrowser.NewExecCLI(options.AWSCLIPath))
}

// NewWithRuntimeFactory is the dependency-injection seam for controlled
// runtimes. RuntimeFactory exposes only verified identity and narrowed reads.
func NewWithRuntimeFactory(factory awsbrowser.RuntimeFactory, env []string, clock func() time.Time, concurrency int) (*Core, error) {
	return newCore(factory, env, clock, concurrency, nil)
}

func newCore(factory awsbrowser.RuntimeFactory, env []string, clock func() time.Time, concurrency int, profiles awsbrowser.ProfileLister) (*Core, error) {
	if nilInterface(factory) || clock == nil || concurrency < 1 {
		return nil, ErrInvalidOptions
	}
	registry := awsbrowser.NewContextRegistry(runtimeFactoryGuard{factory: factory})
	multiplexer := &runtimeMultiplexer{registry: registry, clock: clock}
	coordinator, err := awsbrowser.NewQueryCoordinator(awsbrowser.NewSessionStore(), multiplexer, concurrency, registry)
	if err != nil {
		return nil, ErrInvalidOptions
	}
	return &Core{registry: registry, coordinator: coordinator, contexts: newContextResolver(env), profiles: profiles}, nil
}

// ListContexts discovers configured profile names and their non-secret region
// defaults without resolving credentials, accounts, or principals.
func (core *Core) ListContexts(ctx context.Context) ([]awsbrowser.ContextChoice, error) {
	if core == nil || ctx == nil || nilInterface(core.profiles) {
		return nil, ErrInvalidRequest
	}
	profiles, err := core.profiles.ListProfiles(ctx, core.contexts.env)
	if err != nil {
		return nil, err
	}
	choices := make([]awsbrowser.ContextChoice, 0, len(profiles))
	seen := make(map[string]bool, len(profiles))
	for _, profile := range profiles {
		if seen[profile] || awsbrowser.ValidateContextSelection(profile, "") != nil {
			continue
		}
		seen[profile] = true
		region, err := core.contexts.sharedConfigRegion(ctx, profile)
		if err != nil {
			return nil, err
		}
		if region != "" && awsbrowser.ValidateContextSelection("", region) != nil {
			region = ""
		}
		choices = append(choices, awsbrowser.ContextChoice{Profile: profile, Region: region})
	}
	return choices, nil
}

// Resolve explicitly resolves and identity-verifies one context. Like Query,
// it returns typed failure data and never returns or retains the raw cause.
func (core *Core) Resolve(ctx context.Context, request ContextRequest) (ContextResult, error) {
	if core == nil || core.registry == nil || ctx == nil {
		return ContextResult{}, ErrInvalidRequest
	}
	if !validExplicitContextRequest(request.Profile, request.Region) {
		return ContextResult{}, ErrInvalidRequest
	}
	_, awsContext, failure := core.resolveContext(ctx, request.Profile, request.Region, "", "")
	if failure != nil {
		return ContextResult{Failure: failure}, failure
	}
	return ContextResult{Context: &awsContext, Coverage: Coverage{ContextResolved: true}}, nil
}

// Subscribe resolves the requested context only after this explicit call.
// Runtime failures are returned as a terminal sanitized Update, not as raw
// errors. Request validation errors are safe sentinel errors.
func (core *Core) Subscribe(ctx context.Context, request Request) (*Subscription, error) {
	if core == nil || core.registry == nil || core.coordinator == nil || ctx == nil {
		return nil, ErrInvalidRequest
	}
	provider := strings.ToLower(strings.TrimSpace(request.Provider))
	operation := strings.TrimSpace(request.Operation)
	if awsbrowser.ValidateProviderOperation(provider, operation) != nil ||
		!validExplicitContextRequest(request.Profile, request.Region) {
		return nil, ErrInvalidRequest
	}

	_, awsContext, failure := core.resolveContext(ctx, request.Profile, request.Region, provider, operation)
	if failure != nil {
		return immediateFailure(*failure), nil
	}
	key, err := awsbrowser.NewQueryKey(awsContext, provider, operation, cloneParams(request.Params))
	if err != nil {
		return nil, ErrInvalidRequest
	}

	var raw *awsbrowser.QuerySubscription
	if request.Refresh {
		raw, err = core.coordinator.Refresh(key)
	} else {
		raw, err = core.coordinator.Subscribe(key)
	}
	if err != nil {
		return nil, ErrInvalidRequest
	}
	return bridgeSubscription(ctx, key, raw), nil
}

func (core *Core) resolveContext(ctx context.Context, profile, region, provider, operation string) (awsbrowser.ContextSpec, awsbrowser.AWSContext, *Failure) {
	spec, err := core.contexts.Resolve(ctx, profile, region)
	if err != nil {
		failure := runtimeFailure(err, provider, operation)
		return awsbrowser.ContextSpec{}, awsbrowser.AWSContext{}, &failure
	}
	runtime, err := core.registry.Resolve(ctx, spec)
	if err != nil {
		failure := runtimeFailure(err, provider, operation)
		return spec, awsbrowser.AWSContext{}, &failure
	}
	identity := runtime.Identity()
	awsContext, err := awsbrowser.NewAWSContext(spec, identity, roleName(identity.PrincipalARN))
	if err != nil {
		failure := runtimeFailure(awsbrowser.ErrContextChanged, provider, operation)
		return spec, awsbrowser.AWSContext{}, &failure
	}
	return spec, awsContext, nil
}

// Query drains one subscription to its terminal update and always
// unsubscribes, including on caller cancellation.
func (core *Core) Query(ctx context.Context, request Request) (Result, error) {
	subscription, err := core.Subscribe(ctx, request)
	if err != nil {
		return Result{}, err
	}
	defer subscription.Unsubscribe()

	var result Result
	for update := range subscription.Updates() {
		result.Update = update
	}
	if result.Update.Failure != nil {
		return result, result.Update.Failure
	}
	return result, nil
}

type runtimeMultiplexer struct {
	registry *awsbrowser.ContextRegistry
	clock    func() time.Time
}

type runtimeFactoryGuard struct {
	factory awsbrowser.RuntimeFactory
}

func (guard runtimeFactoryGuard) Resolve(ctx context.Context, spec awsbrowser.ContextSpec) (awsbrowser.RuntimeContext, error) {
	runtime, err := guard.factory.Resolve(ctx, spec)
	if err != nil {
		return nil, err
	}
	if nilInterface(runtime) {
		return nil, errors.New("AWS runtime factory returned no runtime")
	}
	return runtime, nil
}

func (multiplexer *runtimeMultiplexer) Execute(ctx context.Context, key awsbrowser.QueryKey, sink awsbrowser.QueryPageSink) error {
	if multiplexer == nil || multiplexer.registry == nil || multiplexer.clock == nil || ctx == nil || sink == nil || key.Validate() != nil {
		return awsbrowser.NewProviderError(awsbrowser.ProviderUnsupported, key.Provider, key.Operation, "InvalidQuery", "")
	}
	spec := awsbrowser.ContextSpec{Mode: key.Context.Mode, Profile: key.Context.Profile, Region: key.Context.Region}
	runtime, err := multiplexer.registry.Resolve(ctx, spec)
	if err != nil {
		return sanitizeExecutorError(err, key)
	}
	if nilInterface(runtime) {
		return awsbrowser.NewProviderError(awsbrowser.ProviderUnsupported, key.Provider, key.Operation, "UnavailableRuntime", "")
	}
	if !identityMatches(key.Context, runtime.Identity()) {
		return awsbrowser.ErrContextChanged
	}
	ctx = awsbrowser.WithReadIdentity(ctx, awsbrowser.VerifiedIdentity{
		Partition:            key.Context.Partition,
		AccountID:            key.Context.AccountID,
		PrincipalARN:         key.Context.PrincipalARN,
		CredentialGeneration: key.Context.CredentialGen,
	})

	var executor awsbrowser.QueryExecutor
	switch key.Provider {
	case awsbrowser.ProviderEC2:
		client := runtime.EC2()
		if nilInterface(client) {
			break
		}
		executor, err = providers.NewEC2(client, multiplexer.clock)
	case awsbrowser.ProviderIAM:
		client := runtime.IAM()
		if nilInterface(client) {
			break
		}
		executor, err = providers.NewIAM(client, multiplexer.clock)
	case awsbrowser.ProviderRoute53:
		client := runtime.Route53()
		if nilInterface(client) {
			break
		}
		executor, err = providers.NewRoute53(client, multiplexer.clock)
	case awsbrowser.ProviderCloudFront:
		client := runtime.CloudFront()
		if nilInterface(client) {
			break
		}
		executor, err = providers.NewCloudFront(client, multiplexer.clock)
	case awsbrowser.ProviderS3:
		client := runtime.S3()
		if nilInterface(client) {
			break
		}
		executor, err = providers.NewS3(client, multiplexer.clock)
	default:
		return awsbrowser.NewProviderError(awsbrowser.ProviderUnsupported, key.Provider, key.Operation, "InvalidProvider", "")
	}
	if err != nil || executor == nil {
		return awsbrowser.NewProviderError(awsbrowser.ProviderUnsupported, key.Provider, key.Operation, "UnavailableProvider", "")
	}
	return executor.Execute(ctx, key, sink)
}

func identityMatches(context awsbrowser.AWSContext, identity awsbrowser.VerifiedIdentity) bool {
	return context.Partition == identity.Partition && context.AccountID == identity.AccountID &&
		context.PrincipalARN == identity.PrincipalARN && context.CredentialGen == identity.CredentialGeneration
}

func sanitizeExecutorError(err error, key awsbrowser.QueryKey) error {
	failure := runtimeFailure(err, key.Provider, key.Operation)
	if failure.Kind == awsbrowser.ProviderContextChanged {
		return awsbrowser.ErrContextChanged
	}
	return awsbrowser.NewProviderError(failure.Kind, failure.Provider, failure.Operation, failure.Code, failure.RequestID)
}

func bridgeSubscription(ctx context.Context, key awsbrowser.QueryKey, raw *awsbrowser.QuerySubscription) *Subscription {
	updates := make(chan Update, 1)
	bridgeCtx, cancel := context.WithCancel(ctx)
	stop := func() {
		cancel()
		raw.Unsubscribe()
	}
	subscription := &Subscription{updates: updates, stop: stop}

	go func() {
		defer close(updates)
		defer subscription.Unsubscribe()
		last := Update{Key: copyKey(key), Coverage: Coverage{ContextResolved: true, QueryStarted: true}}
		terminal := false
		for {
			select {
			case <-bridgeCtx.Done():
				failure := runtimeFailure(bridgeCtx.Err(), key.Provider, key.Operation)
				last.Failure = &failure
				publishUpdate(updates, last)
				return
			case rawUpdate, ok := <-raw.Updates():
				if !ok {
					if err := bridgeCtx.Err(); err != nil {
						failure := runtimeFailure(err, key.Provider, key.Operation)
						last.Failure = &failure
						publishUpdate(updates, last)
					} else if !terminal {
						last.Failure = &Failure{State: awsbrowser.LoadUnknown, Kind: awsbrowser.ProviderIncomplete, Provider: key.Provider, Operation: key.Operation}
						publishUpdate(updates, last)
					}
					return
				}
				update := Update{Key: copyKey(key), Snapshot: rawUpdate.Snapshot, Coverage: Coverage{ContextResolved: true, QueryStarted: true}}
				if rawUpdate.Failure != nil {
					update.Failure = failureFromProvider(*rawUpdate.Failure)
				}
				update.Coverage.Completed = terminalState(rawUpdate.Snapshot.State)
				last, terminal = update, update.Coverage.Completed
				publishUpdate(updates, update)
				if terminal {
					return
				}
			}
		}
	}()
	return subscription
}

func immediateFailure(failure Failure) *Subscription {
	updates := make(chan Update, 1)
	updates <- Update{
		Snapshot: awsbrowser.QuerySnapshot{State: failure.State},
		Coverage: Coverage{Completed: true},
		Failure:  &failure,
	}
	close(updates)
	return &Subscription{updates: updates, stop: func() {}}
}

func publishUpdate(updates chan Update, update Update) {
	select {
	case <-updates:
	default:
	}
	updates <- update
}

func runtimeFailure(err error, provider, operation string) Failure {
	failure := Failure{State: awsbrowser.LoadUnknown, Kind: awsbrowser.ProviderUnknown, Provider: safeToken(provider), Operation: safeToken(operation)}
	switch {
	case errors.Is(err, awsbrowser.ErrContextChanged):
		failure.Kind = awsbrowser.ProviderContextChanged
	case errors.Is(err, errRegionRequired):
		failure.State, failure.Kind = awsbrowser.LoadUnsupported, awsbrowser.ProviderUnsupported
	case errors.Is(err, context.Canceled):
		failure.State, failure.Kind = awsbrowser.LoadCancelled, awsbrowser.ProviderCancelled
	case errors.Is(err, context.DeadlineExceeded):
		failure.State, failure.Kind = awsbrowser.LoadTimedOut, awsbrowser.ProviderTimedOut
	default:
		var providerError *awsbrowser.ProviderError
		if errors.As(err, &providerError) {
			clean := awsbrowser.NewProviderError(providerError.Kind, providerError.Service, providerError.Operation, providerError.Code, providerError.RequestID)
			failure.Kind, failure.Provider, failure.Operation, failure.Code, failure.RequestID = clean.Kind, clean.Service, clean.Operation, clean.Code, clean.RequestID
			failure.State = stateForKind(clean.Kind)
			return failure
		}
		var credential *awsbrowser.CredentialError
		if errors.As(err, &credential) {
			switch credential.Kind {
			case awsbrowser.CredentialAuthRequired:
				failure.State, failure.Kind = awsbrowser.LoadAuthRequired, awsbrowser.ProviderAuthRequired
			case awsbrowser.CredentialUnsupported:
				failure.State, failure.Kind = awsbrowser.LoadUnsupported, awsbrowser.ProviderUnsupported
			case awsbrowser.CredentialCancelled:
				failure.State, failure.Kind = awsbrowser.LoadCancelled, awsbrowser.ProviderCancelled
			case awsbrowser.CredentialInvalid, awsbrowser.CredentialOutputTooLarge:
				failure.Kind = awsbrowser.ProviderDecode
			}
			return failure
		}
		var profileSource *awsbrowser.ProfileSourceError
		if errors.As(err, &profileSource) &&
			(profileSource.Reason == "Environment is not allowed for named profiles" || profileSource.Reason == "unsupported credential source") {
			failure.State, failure.Kind = awsbrowser.LoadUnsupported, awsbrowser.ProviderUnsupported
			return failure
		}
		classified := awsbrowser.ClassifyProviderError(err, "sts", "GetCallerIdentity")
		if errors.As(classified, &providerError) {
			failure.Kind, failure.Provider, failure.Operation, failure.Code, failure.RequestID = providerError.Kind, providerError.Service, providerError.Operation, providerError.Code, providerError.RequestID
			failure.State = stateForKind(providerError.Kind)
		}
	}
	return failure
}

func stateForKind(kind awsbrowser.ProviderErrorKind) awsbrowser.LoadState {
	switch kind {
	case awsbrowser.ProviderForbidden:
		return awsbrowser.LoadForbidden
	case awsbrowser.ProviderAuthRequired:
		return awsbrowser.LoadAuthRequired
	case awsbrowser.ProviderThrottled:
		return awsbrowser.LoadThrottled
	case awsbrowser.ProviderTimedOut:
		return awsbrowser.LoadTimedOut
	case awsbrowser.ProviderCancelled:
		return awsbrowser.LoadCancelled
	case awsbrowser.ProviderUnsupported:
		return awsbrowser.LoadUnsupported
	default:
		return awsbrowser.LoadUnknown
	}
}

func failureFromProvider(provider awsbrowser.ProviderFailure) *Failure {
	return &Failure{State: provider.State, Kind: provider.Kind, Provider: safeToken(provider.Service), Operation: safeToken(provider.Operation), Code: safeToken(provider.Code), RequestID: safeToken(provider.RequestID)}
}

func safeToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return ""
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') &&
			!(character >= '0' && character <= '9') && !strings.ContainsRune("_.:/-", character) {
			return ""
		}
	}
	return value
}

func terminalState(state awsbrowser.LoadState) bool {
	switch state {
	case awsbrowser.LoadReady, awsbrowser.LoadEmpty, awsbrowser.LoadStale, awsbrowser.LoadForbidden,
		awsbrowser.LoadAuthRequired, awsbrowser.LoadThrottled, awsbrowser.LoadTimedOut,
		awsbrowser.LoadCancelled, awsbrowser.LoadUnsupported, awsbrowser.LoadUnknown:
		return true
	default:
		return false
	}
}

func copyKey(key awsbrowser.QueryKey) *awsbrowser.QueryKey {
	copy := key
	return &copy
}

func cloneParams(params map[string]string) map[string]string {
	if params == nil {
		return nil
	}
	copy := make(map[string]string, len(params))
	for name, value := range params {
		copy[name] = value
	}
	return copy
}

func nilInterface(value any) bool {
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

func roleName(principalARN string) string {
	const assumedRole = ":assumed-role/"
	if index := strings.Index(principalARN, assumedRole); index >= 0 {
		parts := strings.Split(strings.TrimPrefix(principalARN[index:], assumedRole), "/")
		if len(parts) >= 2 {
			return strings.Join(parts[:len(parts)-1], "/")
		}
	}
	const iamRole = ":role/"
	if index := strings.Index(principalARN, iamRole); index >= 0 {
		return strings.TrimPrefix(principalARN[index:], iamRole)
	}
	return ""
}

func (failure Failure) String() string {
	return fmt.Sprintf("%s/%s", failure.State, failure.Kind)
}
