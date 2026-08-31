package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aws/smithy-go"
	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
)

type searchProfileListerFake struct {
	mu       sync.Mutex
	profiles []string
	err      error
	calls    int
}

type searchProfileListerFunc func(context.Context, []string) ([]string, error)

func (list searchProfileListerFunc) ListProfiles(ctx context.Context, env []string) ([]string, error) {
	return list(ctx, env)
}

func (fake *searchProfileListerFake) ListProfiles(context.Context, []string) ([]string, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls++
	return append([]string(nil), fake.profiles...), fake.err
}

func (fake *searchProfileListerFake) count() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.calls
}

type searchCoreFake struct {
	resolve func(context.Context, ContextRequest) (ContextResult, error)
	query   func(context.Context, Request) (Result, error)
}

func (fake *searchCoreFake) Resolve(ctx context.Context, request ContextRequest) (ContextResult, error) {
	return fake.resolve(ctx, request)
}

func (fake *searchCoreFake) Query(ctx context.Context, request Request) (Result, error) {
	return fake.query(ctx, request)
}

func TestSearchServiceDoesNoWorkBeforeSubmitAndOrdersDeduplicatedScope(t *testing.T) {
	lister := &searchProfileListerFake{profiles: []string{"dev", "prod", "dev", "qa"}}
	var mu sync.Mutex
	var resolves []string
	core := &searchCoreFake{
		resolve: func(_ context.Context, request ContextRequest) (ContextResult, error) {
			mu.Lock()
			resolves = append(resolves, request.Profile)
			mu.Unlock()
			ctx := searchTestContext(t, request.Profile, searchAccount(request.Profile), 1)
			return ContextResult{Context: &ctx, Coverage: Coverage{ContextResolved: true}}, nil
		},
		query: func(_ context.Context, request Request) (Result, error) {
			if request.Operation != awsbrowser.OperationGetRole || request.Params["role-name"] != "worker" {
				t.Fatalf("query=%+v", request)
			}
			return Result{}, &smithy.GenericAPIError{Code: "NoSuchEntity", Message: "SECRET raw role text"}
		},
	}
	service, err := NewSearchService(core, lister, []string{"SAFE=value"})
	if err != nil {
		t.Fatal(err)
	}
	if lister.count() != 0 || len(resolves) != 0 {
		t.Fatal("constructor performed search work")
	}
	result, err := service.Submit(context.Background(), SearchRequest{Kind: SearchRole, Scope: SearchAll, Query: "worker", Profile: "dev", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dev", "prod", "qa"}
	if len(result.Coverage) != len(want) {
		t.Fatalf("coverage=%+v", result.Coverage)
	}
	for index, profile := range want {
		if result.Coverage[index].Profile != profile || result.Coverage[index].Status != ProfileStatusNotFound || result.Coverage[index].Current != (index == 0) {
			t.Fatalf("coverage[%d]=%+v", index, result.Coverage[index])
		}
	}
	mu.Lock()
	if len(resolves) == 0 || resolves[0] != "dev" {
		t.Fatalf("resolve order=%v", resolves)
	}
	mu.Unlock()
	if lister.count() != 1 || len(result.Resources) != 0 {
		t.Fatalf("list calls=%d result=%+v", lister.count(), result)
	}
}

func TestSearchServiceFindsAMIVisibilityAcrossAccounts(t *testing.T) {
	lister := &searchProfileListerFake{profiles: []string{"dev", "prod"}}
	contexts := map[string]awsbrowser.AWSContext{
		"dev":  searchTestContext(t, "dev", "111111111111", 1),
		"prod": searchTestContext(t, "prod", "222222222222", 1),
	}
	core := &searchCoreFake{
		resolve: func(_ context.Context, request ContextRequest) (ContextResult, error) {
			value := contexts[request.Profile]
			return ContextResult{Context: &value}, nil
		},
		query: func(_ context.Context, request Request) (Result, error) {
			if request.Operation != awsbrowser.OperationDescribeImages || request.Params["image-id"] != "ami-0123456789abcdef0" {
				t.Fatalf("query=%+v", request)
			}
			value := contexts[request.Profile]
			resource := searchTestResource(t, value, request.Operation, false, "ec2.image", "ami-0123456789abcdef0", map[string]any{
				"name": "shared-base", "owner_id": "999999999999",
			})
			return searchTestResult(t, value, request.Provider, request.Operation, request.Params, resource), nil
		},
	}
	service, err := NewSearchService(core, lister, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Submit(context.Background(), SearchRequest{
		Kind: SearchAMI, Scope: SearchAll, Query: "ami-0123456789abcdef0", Profile: "dev", Region: "ap-northeast-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Resources) != 2 || len(result.Coverage) != 2 {
		t.Fatalf("result=%+v", result)
	}
	for _, coverage := range result.Coverage {
		if coverage.Status != ProfileStatusMatched || coverage.Matches != 1 {
			t.Fatalf("coverage=%+v", coverage)
		}
	}
	for _, resource := range result.Resources {
		if resource.Key.Type != "ec2.image" || resource.Observations[0].Fields()["owner_id"] != "999999999999" {
			t.Fatalf("resource=%+v", resource)
		}
	}
}

func TestSearchServiceQueriesCurrentWithoutWaitingForSlowProfiles(t *testing.T) {
	lister := &searchProfileListerFake{profiles: []string{"slow"}}
	current := searchTestContext(t, "current", "111111111111", 1)
	slow := searchTestContext(t, "slow", "222222222222", 1)
	slowStarted := make(chan struct{})
	releaseSlow := make(chan struct{})
	currentQueried := make(chan struct{})
	core := &searchCoreFake{
		resolve: func(_ context.Context, request ContextRequest) (ContextResult, error) {
			if request.Profile == "current" {
				return ContextResult{Context: &current}, nil
			}
			close(slowStarted)
			<-releaseSlow
			return ContextResult{Context: &slow}, nil
		},
		query: func(_ context.Context, request Request) (Result, error) {
			if request.Profile == "current" {
				close(currentQueried)
			}
			return Result{}, nil
		},
	}
	service, _ := NewSearchService(core, lister, nil)
	done := make(chan error, 1)
	go func() {
		_, err := service.Submit(context.Background(), SearchRequest{Kind: SearchRole, Scope: SearchAll, Query: "worker", Profile: "current", Region: "us-east-1"})
		done <- err
	}()
	select {
	case <-slowStarted:
	case <-time.After(2 * time.Second):
		close(releaseSlow)
		t.Fatal("slow profile resolution did not start")
	}
	select {
	case <-currentQueried:
	case <-time.After(2 * time.Second):
		close(releaseSlow)
		t.Fatal("current query was delayed by another profile resolution")
	}
	close(releaseSlow)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSearchStreamEmitsCurrentThenIncrementalAndTerminalCoverage(t *testing.T) {
	lister := &searchProfileListerFake{profiles: []string{"p1", "p2"}}
	contexts := map[string]awsbrowser.AWSContext{}
	for index, profile := range []string{"current", "p1", "p2"} {
		contexts[profile] = searchTestContext(t, profile, fmt.Sprintf("%012d", index+1), 1)
	}
	started := make(chan string, 2)
	releases := map[string]chan struct{}{"p1": make(chan struct{}), "p2": make(chan struct{})}
	core := &searchCoreFake{
		resolve: func(_ context.Context, request ContextRequest) (ContextResult, error) {
			value := contexts[request.Profile]
			return ContextResult{Context: &value}, nil
		},
		query: func(_ context.Context, request Request) (Result, error) {
			if release := releases[request.Profile]; release != nil {
				started <- request.Profile
				<-release
			}
			value := contexts[request.Profile]
			resource := searchTestResource(t, value, request.Operation, true, "iam-role", "worker", map[string]any{"name": "worker"})
			return searchTestResult(t, value, request.Provider, request.Operation, request.Params, resource), nil
		},
	}
	service, _ := NewSearchService(core, lister, nil)
	updates, err := service.Stream(context.Background(), SearchRequest{Kind: SearchRole, Scope: SearchAll, Query: "worker", Profile: "current", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	current := <-updates
	if current.Done || len(current.Result.Coverage) != 1 || current.Result.Coverage[0].Profile != "current" || !current.Result.Coverage[0].Current {
		t.Fatalf("current update=%+v", current)
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("secondary profile did not start")
		}
	}
	close(releases["p2"])
	incremental := <-updates
	if incremental.Done || len(incremental.Result.Coverage) != 2 || incremental.Result.Coverage[0].Profile != "current" || incremental.Result.Coverage[1].Profile != "p2" {
		t.Fatalf("incremental update=%+v", incremental)
	}
	close(releases["p1"])
	terminal := <-updates
	if !terminal.Done || len(terminal.Result.Coverage) != 3 || len(terminal.Result.Resources) != 3 {
		t.Fatalf("terminal update=%+v", terminal)
	}
	for index, profile := range []string{"current", "p1", "p2"} {
		if terminal.Result.Coverage[index].Profile != profile {
			t.Fatalf("terminal coverage=%+v", terminal.Result.Coverage)
		}
	}
	if _, open := <-updates; open {
		t.Fatal("stream remained open after terminal update")
	}
}

func TestSearchStreamCancellationReplacesBufferedCurrentWithTerminalWhenNoSecondaryProfiles(t *testing.T) {
	for attempt := 0; attempt < 50; attempt++ {
		discoveryStarted := make(chan struct{})
		lister := searchProfileListerFunc(func(ctx context.Context, _ []string) ([]string, error) {
			close(discoveryStarted)
			<-ctx.Done()
			return nil, ctx.Err()
		})
		current := searchTestContext(t, "current", "111111111111", 1)
		core := &searchCoreFake{
			resolve: func(context.Context, ContextRequest) (ContextResult, error) {
				return ContextResult{Context: &current}, nil
			},
			query: func(context.Context, Request) (Result, error) { return Result{}, nil },
		}
		service, _ := NewSearchService(core, lister, nil)
		ctx, cancel := context.WithCancel(context.Background())
		updates, err := service.Stream(ctx, SearchRequest{Kind: SearchRole, Scope: SearchAll, Query: "worker", Profile: "current", Region: "us-east-1"})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		<-discoveryStarted
		waitForSearchUpdate(t, updates)

		cancel()
		terminal, terminalCount := drainSearchUpdates(t, updates)
		if !terminal.Done || len(terminal.Result.Coverage) != 1 || terminal.Result.Coverage[0].Profile != "current" {
			t.Fatalf("attempt %d terminal=%+v", attempt, terminal)
		}
		if terminalCount != 1 {
			t.Fatalf("attempt %d terminal count=%d", attempt, terminalCount)
		}
	}
}

func TestSearchStreamCancellationReplacesBufferedIncrementalWithSecondaryTerminal(t *testing.T) {
	for attempt := 0; attempt < 50; attempt++ {
		lister := &searchProfileListerFake{profiles: []string{"p1", "p2"}}
		contexts := map[string]awsbrowser.AWSContext{}
		for index, profile := range []string{"current", "p1", "p2"} {
			contexts[profile] = searchTestContext(t, profile, fmt.Sprintf("%012d", index+1), 1)
		}
		started := make(chan string, 2)
		releaseP1 := make(chan struct{})
		core := &searchCoreFake{
			resolve: func(_ context.Context, request ContextRequest) (ContextResult, error) {
				value := contexts[request.Profile]
				return ContextResult{Context: &value}, nil
			},
			query: func(ctx context.Context, request Request) (Result, error) {
				if request.Profile == "current" {
					return Result{}, nil
				}
				started <- request.Profile
				if request.Profile == "p1" {
					select {
					case <-releaseP1:
					case <-ctx.Done():
						return Result{}, ctx.Err()
					}
				} else {
					<-ctx.Done()
					return Result{}, ctx.Err()
				}
				return Result{}, nil
			},
		}
		service, _ := NewSearchService(core, lister, nil)
		ctx, cancel := context.WithCancel(context.Background())
		updates, err := service.Stream(ctx, SearchRequest{Kind: SearchRole, Scope: SearchAll, Query: "worker", Profile: "current", Region: "us-east-1"})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		current := receiveSearchUpdate(t, updates)
		if current.Done || len(current.Result.Coverage) != 1 {
			cancel()
			t.Fatalf("attempt %d current=%+v", attempt, current)
		}
		for range 2 {
			<-started
		}
		close(releaseP1)
		waitForSearchUpdate(t, updates)

		cancel()
		terminal, terminalCount := drainSearchUpdates(t, updates)
		if !terminal.Done || len(terminal.Result.Coverage) != 3 {
			t.Fatalf("attempt %d terminal=%+v", attempt, terminal)
		}
		if terminalCount != 1 {
			t.Fatalf("attempt %d terminal count=%d", attempt, terminalCount)
		}
	}
}

func TestSearchServiceDomainUsesEverySuffixZoneAndKeepsVariants(t *testing.T) {
	lister := &searchProfileListerFake{profiles: []string{"other"}}
	contexts := map[string]awsbrowser.AWSContext{}
	for index, profile := range []string{"current", "other"} {
		contexts[profile] = searchTestContext(t, profile, "111111111111", uint64(index+1))
	}
	var mu sync.Mutex
	zoneCalls := map[string]int{}
	core := &searchCoreFake{
		resolve: func(_ context.Context, request ContextRequest) (ContextResult, error) {
			ctx := contexts[request.Profile]
			return ContextResult{Context: &ctx}, nil
		},
		query: func(_ context.Context, request Request) (Result, error) {
			ctx := contexts[request.Profile]
			switch request.Operation {
			case awsbrowser.OperationListHostedZones:
				return searchTestResult(t, ctx, request.Provider, request.Operation, request.Params,
					searchTestResource(t, ctx, request.Operation, true, "hosted-zone", "ZPUB", map[string]any{"id": "ZPUB", "name": "example.com.", "private": false}),
					searchTestResource(t, ctx, request.Operation, true, "hosted-zone", "ZPRIVATE", map[string]any{"id": "ZPRIVATE", "name": "sub.example.com.", "private": true}),
					searchTestResource(t, ctx, request.Operation, true, "hosted-zone", "ZOTHER", map[string]any{"id": "ZOTHER", "name": "notexample.com.", "private": false}),
				), nil
			case awsbrowser.OperationListResourceRecordSets:
				zone := request.Params["hosted-zone-id"]
				if request.Params["record-name"] != "www.sub.example.com." {
					t.Fatalf("record params=%v", request.Params)
				}
				mu.Lock()
				zoneCalls[zone]++
				mu.Unlock()
				resources := []awsbrowser.ObservedResource{
					searchTestResource(t, ctx, request.Operation, true, "resource-record-set", zone+"-A-blue", map[string]any{"name": "www.sub.example.com.", "type": "A", "set_identifier": "blue"}),
				}
				if zone == "ZPUB" {
					resources = append(resources, searchTestResource(t, ctx, request.Operation, true, "resource-record-set", zone+"-AAAA", map[string]any{"name": "www.sub.example.com.", "type": "AAAA"}))
				}
				return searchTestResult(t, ctx, request.Provider, request.Operation, request.Params, resources...), nil
			default:
				t.Fatalf("unexpected operation %s", request.Operation)
				return Result{}, nil
			}
		},
	}
	service, _ := NewSearchService(core, lister, nil)
	result, err := service.Submit(context.Background(), SearchRequest{Kind: SearchDomain, Scope: SearchAll, Query: "WWW.Sub.Example.Com", Profile: "current", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if zoneCalls["ZPUB"] != 2 || zoneCalls["ZPRIVATE"] != 2 || zoneCalls["ZOTHER"] != 0 {
		t.Fatalf("zone calls=%v", zoneCalls)
	}
	mu.Unlock()
	if len(result.Resources) != 3 {
		t.Fatalf("resources=%d %+v", len(result.Resources), result.Resources)
	}
	for _, resource := range result.Resources {
		if len(resource.AvailableViaProfiles) != 2 || len(resource.Observations) != 2 {
			t.Fatalf("merge=%+v", resource)
		}
	}
	for _, coverage := range result.Coverage {
		if coverage.Status != ProfileStatusMatched {
			t.Fatalf("coverage=%+v", result.Coverage)
		}
	}
}

func TestSearchServiceEnforcesResolutionAndSDKCaps(t *testing.T) {
	profiles := make([]string, 12)
	for index := range profiles {
		profiles[index] = fmt.Sprintf("p%02d", index)
	}
	lister := &searchProfileListerFake{profiles: profiles[1:]}
	resolveRelease := make(chan struct{})
	resolveFour := make(chan struct{})
	queryRelease := make(chan struct{})
	queryFour := make(chan struct{})
	var mu sync.Mutex
	resolveActive, resolveMax := 0, 0
	queryActive, queryMax := 0, 0
	accountActive, accountMax := map[string]int{}, map[string]int{}
	core := &searchCoreFake{
		resolve: func(ctx context.Context, request ContextRequest) (ContextResult, error) {
			if request.Profile != profiles[0] {
				mu.Lock()
				resolveActive++
				if resolveActive > resolveMax {
					resolveMax = resolveActive
				}
				if resolveActive == 4 {
					select {
					case <-resolveFour:
					default:
						close(resolveFour)
					}
				}
				mu.Unlock()
				select {
				case <-resolveRelease:
				case <-ctx.Done():
				}
				mu.Lock()
				resolveActive--
				mu.Unlock()
			}
			ctxValue := searchTestContext(t, request.Profile, searchAccount(request.Profile), 1)
			return ContextResult{Context: &ctxValue}, nil
		},
		query: func(ctx context.Context, request Request) (Result, error) {
			if request.Profile == profiles[0] {
				return Result{}, nil
			}
			account := searchAccount(request.Profile)
			mu.Lock()
			queryActive++
			accountActive[account]++
			if queryActive > queryMax {
				queryMax = queryActive
			}
			if accountActive[account] > accountMax[account] {
				accountMax[account] = accountActive[account]
			}
			if queryActive == 4 {
				select {
				case <-queryFour:
				default:
					close(queryFour)
				}
			}
			mu.Unlock()
			select {
			case <-queryRelease:
			case <-ctx.Done():
			}
			mu.Lock()
			queryActive--
			accountActive[account]--
			mu.Unlock()
			return Result{}, nil
		},
	}
	service, _ := NewSearchService(core, lister, nil)
	done := make(chan error, 1)
	go func() {
		_, err := service.Submit(context.Background(), SearchRequest{Kind: SearchRole, Scope: SearchAll, Query: "worker", Profile: profiles[0], Region: "us-east-1"})
		done <- err
	}()
	select {
	case <-resolveFour:
	case <-time.After(2 * time.Second):
		t.Fatal("four credential resolutions never became active")
	}
	close(resolveRelease)
	select {
	case <-queryFour:
	case <-time.After(2 * time.Second):
		t.Fatal("four SDK queries never became active")
	}
	close(queryRelease)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if resolveMax != 4 || queryMax != 4 {
		t.Fatalf("max resolve=%d SDK=%d", resolveMax, queryMax)
	}
	for account, maximum := range accountMax {
		if maximum > 2 {
			t.Fatalf("account %s max=%d", account, maximum)
		}
	}
}

func TestSearchServiceCapsSpanOverlappingSubmitCalls(t *testing.T) {
	t.Run("credentials", func(t *testing.T) {
		lister := &searchProfileListerFake{}
		started := make(chan struct{}, 8)
		release := make(chan struct{})
		var mu sync.Mutex
		active, maximum := 0, 0
		core := &searchCoreFake{
			resolve: func(_ context.Context, request ContextRequest) (ContextResult, error) {
				mu.Lock()
				active++
				if active > maximum {
					maximum = active
				}
				mu.Unlock()
				started <- struct{}{}
				<-release
				mu.Lock()
				active--
				mu.Unlock()
				value := searchTestContext(t, request.Profile, "111111111111", 1)
				return ContextResult{Context: &value}, nil
			},
			query: func(context.Context, Request) (Result, error) { return Result{}, nil },
		}
		service, _ := NewSearchService(core, lister, nil)
		done := startConcurrentSearches(service, 6, SearchRole)
		for index := 0; index < 4; index++ {
			<-started
		}
		select {
		case <-started:
			close(release)
			t.Fatal("more than four credential resolutions started across Submit calls")
		case <-time.After(100 * time.Millisecond):
		}
		close(release)
		waitConcurrentSearches(t, done, 6)
		mu.Lock()
		defer mu.Unlock()
		if maximum != 4 {
			t.Fatalf("credential max=%d", maximum)
		}
	})

	t.Run("global and account SDK", func(t *testing.T) {
		lister := &searchProfileListerFake{}
		started := make(chan struct{}, 8)
		release := make(chan struct{})
		var mu sync.Mutex
		active, maximum := 0, 0
		accountActive, accountMaximum := map[string]int{}, map[string]int{}
		core := &searchCoreFake{
			resolve: func(_ context.Context, request ContextRequest) (ContextResult, error) {
				account := "111111111111"
				if request.Profile == "p01" || request.Profile == "p03" || request.Profile == "p05" {
					account = "222222222222"
				}
				value := searchTestContext(t, request.Profile, account, 1)
				return ContextResult{Context: &value}, nil
			},
			query: func(_ context.Context, request Request) (Result, error) {
				account := "111111111111"
				if request.Profile == "p01" || request.Profile == "p03" || request.Profile == "p05" {
					account = "222222222222"
				}
				mu.Lock()
				active++
				accountActive[account]++
				if active > maximum {
					maximum = active
				}
				if accountActive[account] > accountMaximum[account] {
					accountMaximum[account] = accountActive[account]
				}
				mu.Unlock()
				started <- struct{}{}
				<-release
				mu.Lock()
				active--
				accountActive[account]--
				mu.Unlock()
				return Result{}, nil
			},
		}
		service, _ := NewSearchService(core, lister, nil)
		done := startConcurrentSearches(service, 6, SearchRole)
		for index := 0; index < 4; index++ {
			<-started
		}
		select {
		case <-started:
			close(release)
			t.Fatal("more than four SDK calls started across Submit calls")
		case <-time.After(100 * time.Millisecond):
		}
		close(release)
		waitConcurrentSearches(t, done, 6)
		mu.Lock()
		defer mu.Unlock()
		if maximum != 4 {
			t.Fatalf("SDK max=%d", maximum)
		}
		for account, value := range accountMaximum {
			if value > 2 {
				t.Fatalf("account %s max=%d", account, value)
			}
		}
	})

	t.Run("Route53 account", func(t *testing.T) {
		lister := &searchProfileListerFake{}
		started := make(chan struct{}, 4)
		release := make(chan struct{})
		core := &searchCoreFake{
			resolve: func(_ context.Context, request ContextRequest) (ContextResult, error) {
				value := searchTestContext(t, request.Profile, "111111111111", 1)
				return ContextResult{Context: &value}, nil
			},
			query: func(context.Context, Request) (Result, error) {
				started <- struct{}{}
				<-release
				return Result{}, nil
			},
		}
		service, _ := NewSearchService(core, lister, nil)
		done := startConcurrentSearches(service, 4, SearchDomain)
		<-started
		select {
		case <-started:
			close(release)
			t.Fatal("more than one Route53 call started for an account across Submit calls")
		case <-time.After(100 * time.Millisecond):
		}
		close(release)
		waitConcurrentSearches(t, done, 4)
	})
}

func TestSearchCancellationStartsNoQueuedWorkAndRetainsCompletedResults(t *testing.T) {
	lister := &searchProfileListerFake{profiles: []string{"p1", "p2", "p3", "p4", "p5"}}
	ctxByProfile := map[string]awsbrowser.AWSContext{}
	for _, profile := range append([]string{"current"}, lister.profiles...) {
		ctxByProfile[profile] = searchTestContext(t, profile, "111111111111", 1)
	}
	started := make(chan string, 16)
	core := &searchCoreFake{
		resolve: func(_ context.Context, request ContextRequest) (ContextResult, error) {
			value := ctxByProfile[request.Profile]
			return ContextResult{Context: &value}, nil
		},
		query: func(ctx context.Context, request Request) (Result, error) {
			started <- request.Profile
			if request.Profile == "current" {
				resource := searchTestResource(t, ctxByProfile[request.Profile], request.Operation, true, "iam-role", "worker", map[string]any{"name": "worker"})
				return searchTestResult(t, ctxByProfile[request.Profile], request.Provider, request.Operation, request.Params, resource), nil
			}
			<-ctx.Done()
			return Result{}, ctx.Err()
		},
	}
	service, _ := NewSearchService(core, lister, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan SearchResult, 1)
	go func() {
		result, _ := service.Submit(ctx, SearchRequest{Kind: SearchRole, Scope: SearchAll, Query: "worker", Profile: "current", Region: "us-east-1"})
		done <- result
	}()
	if profile := <-started; profile != "current" {
		t.Fatalf("first=%s", profile)
	}
	for index := 0; index < 2; index++ {
		<-started
	}
	cancel()
	result := <-done
	if len(started) != 0 {
		t.Fatalf("queued calls started after cancel: %d", len(started))
	}
	if len(result.Resources) != 1 || result.Coverage[0].Status != ProfileStatusMatched {
		t.Fatalf("completed result lost: %+v", result)
	}
	notSearched := 0
	for _, coverage := range result.Coverage[1:] {
		if coverage.Status == ProfileStatusNotSearched {
			notSearched++
		}
	}
	if notSearched == 0 {
		t.Fatalf("coverage=%+v", result.Coverage)
	}
}

func TestSearchServiceLimitsRoute53ToOneCallPerAccount(t *testing.T) {
	lister := &searchProfileListerFake{profiles: []string{"p1", "p2", "p3", "p4"}}
	ctxValue := map[string]awsbrowser.AWSContext{}
	for _, profile := range append([]string{"current"}, lister.profiles...) {
		ctxValue[profile] = searchTestContext(t, profile, "111111111111", 1)
	}
	started := make(chan string, 8)
	core := &searchCoreFake{
		resolve: func(_ context.Context, request ContextRequest) (ContextResult, error) {
			value := ctxValue[request.Profile]
			return ContextResult{Context: &value}, nil
		},
		query: func(ctx context.Context, request Request) (Result, error) {
			started <- request.Profile
			if request.Profile == "current" {
				return Result{}, nil
			}
			<-ctx.Done()
			return Result{}, ctx.Err()
		},
	}
	service, _ := NewSearchService(core, lister, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_, _ = service.Submit(ctx, SearchRequest{Kind: SearchDomain, Scope: SearchAll, Query: "www.example.com", Profile: "current", Region: "us-east-1"})
		close(done)
	}()
	if profile := <-started; profile != "current" {
		t.Fatalf("first=%s", profile)
	}
	<-started
	cancel()
	<-done
	if len(started) != 0 {
		t.Fatalf("more than one Route53 call was active/started for the account: %d", len(started)+1)
	}
}

func TestSearchResultDoesNotRetainRawErrorsOrSecrets(t *testing.T) {
	lister := &searchProfileListerFake{err: errors.New("SECRET list profiles output")}
	ctxValue := searchTestContext(t, "current", "111111111111", 1)
	core := &searchCoreFake{
		resolve: func(context.Context, ContextRequest) (ContextResult, error) {
			return ContextResult{Context: &ctxValue}, nil
		},
		query: func(context.Context, Request) (Result, error) {
			return Result{}, awsbrowser.NewProviderError(awsbrowser.ProviderForbidden, "iam", "GetRole", "AccessDenied", "raw-request-secret")
		},
	}
	service, _ := NewSearchService(core, lister, nil)
	result, err := service.Submit(context.Background(), SearchRequest{Kind: SearchRole, Scope: SearchAll, Query: "worker", Profile: "current", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.DiscoveryStatus != ProfileStatusUnknown || result.Coverage[0].Status != ProfileStatusForbidden {
		t.Fatalf("result=%+v", result)
	}
	text := fmt.Sprintf("%+v", result)
	if containsSecret(text) {
		t.Fatalf("retained raw data: %s", text)
	}
}

func TestSearchServiceDoesNotTreatNonRoleNoSuchEntityAsNotFound(t *testing.T) {
	lister := &searchProfileListerFake{}
	ctxValue := searchTestContext(t, "current", "111111111111", 1)
	core := &searchCoreFake{
		resolve: func(context.Context, ContextRequest) (ContextResult, error) {
			return ContextResult{Context: &ctxValue}, nil
		},
		query: func(context.Context, Request) (Result, error) {
			return Result{}, &smithy.GenericAPIError{Code: "NoSuchEntity", Message: "not an EC2 miss"}
		},
	}
	service, _ := NewSearchService(core, lister, nil)
	result, err := service.Submit(context.Background(), SearchRequest{Kind: SearchEC2Instances, Scope: SearchCurrent, Profile: "current", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Coverage[0].Status != ProfileStatusUnknown {
		t.Fatalf("status=%s", result.Coverage[0].Status)
	}
}

func TestSearchServiceRejectsControlBearingContextInputs(t *testing.T) {
	lister := &searchProfileListerFake{}
	core := &searchCoreFake{
		resolve: func(context.Context, ContextRequest) (ContextResult, error) {
			t.Fatal("invalid request reached context resolution")
			return ContextResult{}, nil
		},
		query: func(context.Context, Request) (Result, error) {
			t.Fatal("invalid request reached query")
			return Result{}, nil
		},
	}
	service, _ := NewSearchService(core, lister, nil)
	for _, request := range []SearchRequest{
		{Kind: SearchRole, Scope: SearchCurrent, Query: "worker", Profile: "dev\nprod", Region: "us-east-1"},
		{Kind: SearchRole, Scope: SearchCurrent, Query: "worker", Profile: "dev", Region: "us-east-1\x1b"},
	} {
		if _, err := service.Submit(context.Background(), request); !errors.Is(err, ErrInvalidSearchRequest) {
			t.Fatalf("request=%+v err=%v", request, err)
		}
	}
	if lister.count() != 0 {
		t.Fatalf("profile discovery calls=%d", lister.count())
	}
}

func startConcurrentSearches(service *SearchService, count int, kind SearchKind) <-chan error {
	done := make(chan error, count)
	for index := 0; index < count; index++ {
		go func(index int) {
			request := SearchRequest{Kind: kind, Scope: SearchCurrent, Profile: fmt.Sprintf("p%02d", index), Region: "us-east-1"}
			switch kind {
			case SearchRole:
				request.Query = "worker"
			case SearchDomain:
				request.Query = "www.example.com"
			}
			_, err := service.Submit(context.Background(), request)
			done <- err
		}(index)
	}
	return done
}

func waitConcurrentSearches(t *testing.T, done <-chan error, count int) {
	t.Helper()
	for index := 0; index < count; index++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func waitForSearchUpdate(t *testing.T, updates <-chan SearchUpdate) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for len(updates) == 0 {
		select {
		case <-deadline:
			t.Fatal("search update buffer was not filled")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func receiveSearchUpdate(t *testing.T, updates <-chan SearchUpdate) SearchUpdate {
	t.Helper()
	select {
	case update, open := <-updates:
		if !open {
			t.Fatal("search stream closed without an update")
		}
		return update
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for search update")
		return SearchUpdate{}
	}
}

func drainSearchUpdates(t *testing.T, updates <-chan SearchUpdate) (SearchUpdate, int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	var terminal SearchUpdate
	terminalCount := 0
	for {
		select {
		case update, open := <-updates:
			if !open {
				return terminal, terminalCount
			}
			if update.Done {
				terminal = update
				terminalCount++
			}
		case <-deadline:
			t.Fatal("timed out waiting for search stream to close")
			return SearchUpdate{}, 0
		}
	}
}

func searchTestContext(t *testing.T, profile, account string, generation uint64) awsbrowser.AWSContext {
	t.Helper()
	mode := awsbrowser.ContextModeNamedProfile
	if profile == "" {
		mode = awsbrowser.ContextModeAmbient
	}
	value, err := awsbrowser.NewAWSContext(awsbrowser.ContextSpec{Mode: mode, Profile: profile, Region: "us-east-1"}, awsbrowser.VerifiedIdentity{
		Partition: "aws", AccountID: account, PrincipalARN: "arn:aws:iam::" + account + ":role/test", CredentialGeneration: generation,
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func searchTestResource(t *testing.T, ctx awsbrowser.AWSContext, operation string, global bool, resourceType, id string, fields map[string]any) awsbrowser.ObservedResource {
	t.Helper()
	var key awsbrowser.ResourceKey
	var err error
	if global {
		key, err = awsbrowser.NewGlobalResourceKey(ctx, resourceType, id)
	} else {
		key, err = awsbrowser.NewRegionalResourceKey(ctx, resourceType, id)
	}
	if err != nil {
		t.Fatal(err)
	}
	observation, err := awsbrowser.NewResourceObservationForOperation(ctx, operation, fields, time.Unix(100, 0), true)
	if err != nil {
		t.Fatal(err)
	}
	return awsbrowser.ObservedResource{Key: key, Observation: observation}
}

func searchTestResult(t *testing.T, ctx awsbrowser.AWSContext, provider, operation string, params map[string]string, resources ...awsbrowser.ObservedResource) Result {
	t.Helper()
	key, err := awsbrowser.NewQueryKey(ctx, provider, operation, params)
	if err != nil {
		t.Fatal(err)
	}
	page, err := awsbrowser.NewQueryPage(0, resources, time.Unix(100, 0), true)
	if err != nil {
		t.Fatal(err)
	}
	store := awsbrowser.NewSessionStore()
	if err = store.BeginLoad(key); err != nil {
		t.Fatal(err)
	}
	if err = store.CommitPage(key, page); err != nil {
		t.Fatal(err)
	}
	if err = store.CompleteQuery(key, time.Unix(101, 0)); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.Snapshot(key)
	return Result{Update: Update{Snapshot: snapshot}}
}

func searchAccount(profile string) string {
	if profile == "p00" || profile == "p01" || profile == "p02" || profile == "p03" {
		return "111111111111"
	}
	if profile == "p04" || profile == "p05" || profile == "p06" || profile == "p07" {
		return "222222222222"
	}
	return "333333333333"
}

func containsSecret(value string) bool {
	for _, secret := range []string{"SECRET", "raw-request-secret", "list profiles output"} {
		if stringsContains(value, secret) {
			return true
		}
	}
	return false
}

func stringsContains(value, fragment string) bool {
	for index := 0; index+len(fragment) <= len(value); index++ {
		if value[index:index+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
