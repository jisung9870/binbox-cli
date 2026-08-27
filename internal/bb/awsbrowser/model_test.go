package awsbrowser

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

type blockingDispatcher struct {
	started chan struct{}
	done    chan struct{}
}

func (dispatcher *blockingDispatcher) Dispatch(ctx context.Context, _ Intent) (IntentStream, error) {
	close(dispatcher.started)
	<-ctx.Done()
	close(dispatcher.done)
	return nil, ctx.Err()
}

type recordingDispatcher struct {
	intents []Intent
	streams []*testIntentStream
	err     error
}

func (dispatcher *recordingDispatcher) Dispatch(_ context.Context, intent Intent) (IntentStream, error) {
	dispatcher.intents = append(dispatcher.intents, intent)
	if dispatcher.err != nil {
		return nil, dispatcher.err
	}
	if len(dispatcher.streams) == 0 {
		stream := newTestIntentStream()
		dispatcher.streams = append(dispatcher.streams, stream)
	}
	stream := dispatcher.streams[0]
	dispatcher.streams = dispatcher.streams[1:]
	return stream, nil
}

type testIntentStream struct {
	updates chan IntentUpdate
	cancels int
}

func newTestIntentStream() *testIntentStream {
	return &testIntentStream{updates: make(chan IntentUpdate, 8)}
}

func (stream *testIntentStream) Updates() <-chan IntentUpdate { return stream.updates }
func (stream *testIntentStream) Cancel()                      { stream.cancels++ }

func key(code rune) tea.KeyPressMsg  { return tea.KeyPressMsg{Code: code} }
func ctrl(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code, Mod: tea.ModCtrl} }

func TestModelStartupAndLocalNavigationAreZeroCall(t *testing.T) {
	dispatcher := new(recordingDispatcher)
	m := NewModel(context.Background(), Config{Profile: "dev", Region: "ap-northeast-2"}, dispatcher)
	if cmd := m.Init(); cmd != nil {
		t.Fatalf("Init command=%v", cmd)
	}
	for _, want := range []string{"AWS Browser · READ ONLY", "EC2 Instances", "Route 53 Hosted Zones", "IAM Roles", "VPC & Networking", "Cross-profile search", "Account unresolved", "Principal unresolved"} {
		if !strings.Contains(m.View().Content, want) {
			t.Fatalf("Home missing %q:\n%s", want, m.View().Content)
		}
	}
	var model tea.Model = m
	for _, msg := range []tea.Msg{tea.WindowSizeMsg{Width: 120, Height: 30}, key('j'), key('k'), key(tea.KeyPgDown), key(tea.KeyPgUp), key('?'), key('?')} {
		model, _ = model.Update(msg)
	}
	if len(dispatcher.intents) != 0 {
		t.Fatalf("local navigation dispatched %+v", dispatcher.intents)
	}
}

func TestModelProgressiveListDetailRelationAndHistory(t *testing.T) {
	first, relation := newTestIntentStream(), newTestIntentStream()
	dispatcher := &recordingDispatcher{streams: []*testIntentStream{first, relation}}
	m := NewModel(context.Background(), Config{Profile: "dev", Region: "ap-northeast-2"}, dispatcher)

	model, wait := runModelCommand(t, m, key(tea.KeyEnter))
	resource := resourceProjection("web-api", "running")
	first.updates <- IntentUpdate{Query: QueryUpdate{Snapshot: QuerySnapshot{State: LoadLoading}}, Projection: IntentProjection{Resources: []ResourceProjection{resource}}}
	model, wait = model.Update(wait())
	if !strings.Contains(model.View().Content, "Loaded 1 · loading more") || !strings.Contains(model.View().Content, "web-api") {
		t.Fatalf("progressive view=%s", model.View().Content)
	}
	first.updates <- IntentUpdate{Query: QueryUpdate{Snapshot: QuerySnapshot{State: LoadReady, FetchedAt: time.Now()}}, Projection: IntentProjection{Resources: []ResourceProjection{resource}}, Done: true}
	model, _ = model.Update(wait())
	if first.cancels != 1 {
		t.Fatalf("terminal stream was not released: cancels=%d", first.cancels)
	}
	model, _ = model.Update(key(tea.KeyEnter))
	for _, want := range []string{"Private IP", "10.0.1.24", "sg-web", "enter open"} {
		if !strings.Contains(model.View().Content, want) {
			t.Fatalf("detail missing %q:\n%s", want, model.View().Content)
		}
	}
	model, wait = runModelCommand(t, model, key(tea.KeyEnter))
	if got := dispatcher.intents[len(dispatcher.intents)-1].Target; got != "ec2.security-group:sg-web" {
		t.Fatalf("relation target=%q", got)
	}
	model, _ = model.Update(key(tea.KeyEscape))
	if relation.cancels != 1 || !strings.Contains(model.View().Content, "web-api") || !strings.Contains(model.View().Content, "Private IP") {
		t.Fatalf("relation back did not cancel/restore detail: cancels=%d view=%s", relation.cancels, model.View().Content)
	}
	model, _ = model.Update(key(tea.KeyEscape))
	if !strings.Contains(model.View().Content, "Resources (1)") {
		t.Fatalf("detail back did not restore list: %s", model.View().Content)
	}
}

func TestModelBackCancelsDispatchBeforeStreamAcquisition(t *testing.T) {
	dispatcher := &blockingDispatcher{started: make(chan struct{}), done: make(chan struct{})}
	model, command := NewModel(context.Background(), Config{}, dispatcher).Update(key(tea.KeyEnter))
	result := make(chan tea.Msg, 1)
	go func() { result <- command() }()
	<-dispatcher.started
	model, _ = model.Update(key(tea.KeyEscape))
	select {
	case <-dispatcher.done:
	case <-time.After(time.Second):
		t.Fatal("Back did not cancel the in-flight Dispatch context")
	}
	model, _ = model.Update(<-result)
	if !strings.Contains(model.View().Content, "Services / tasks") {
		t.Fatalf("late dispatch result replaced Home: %s", model.View().Content)
	}
}

func TestModelPrematureStreamClosureIsTypedAndKeepsPartialResult(t *testing.T) {
	stream := newTestIntentStream()
	dispatcher := &recordingDispatcher{streams: []*testIntentStream{stream}}
	model, wait := runModelCommand(t, NewModel(context.Background(), Config{}, dispatcher), key(tea.KeyEnter))
	stream.updates <- IntentUpdate{Query: QueryUpdate{Snapshot: QuerySnapshot{State: LoadLoading}}, Projection: IntentProjection{Resources: []ResourceProjection{resourceProjection("partial", "loading")}}}
	model, wait = model.Update(wait())
	close(stream.updates)
	model, _ = model.Update(wait())
	view := model.View().Content
	if !strings.Contains(view, "partial") || !strings.Contains(view, "query failed") || !strings.Contains(view, "incomplete stream") {
		t.Fatalf("premature closure was not retained as a typed failure: %s", view)
	}
}

func TestModelTerminalUpdateIsStickyAgainstLateMessages(t *testing.T) {
	stream := newTestIntentStream()
	dispatcher := &recordingDispatcher{streams: []*testIntentStream{stream}}
	model, wait := runModelCommand(t, NewModel(context.Background(), Config{}, dispatcher), key(tea.KeyEnter))
	ready := resourceProjection("ready", "running")
	stream.updates <- IntentUpdate{Query: QueryUpdate{Snapshot: QuerySnapshot{State: LoadReady, FetchedAt: time.Now()}}, Projection: IntentProjection{Resources: []ResourceProjection{ready}}}
	model, _ = model.Update(wait())
	concrete := model.(Model)
	frame := concrete.current()
	model, _ = model.Update(intentStreamMsg{generation: frame.generation, open: true, update: IntentUpdate{
		Query: QueryUpdate{Snapshot: QuerySnapshot{State: LoadLoading}}, Projection: IntentProjection{Resources: []ResourceProjection{resourceProjection("late", "loading")}},
	}})
	view := model.View().Content
	if !strings.Contains(view, "ready") || strings.Contains(view, "late") || !strings.Contains(view, "Ready") {
		t.Fatalf("late update replaced terminal state: %s", view)
	}
}

func TestSearchEditorIsLocalUntilExplicitSubmit(t *testing.T) {
	dispatcher := new(recordingDispatcher)
	model, command := NewModel(context.Background(), Config{}, dispatcher).Update(ctrl('g'))
	if command != nil || len(dispatcher.intents) != 0 || !strings.Contains(model.View().Content, "no AWS request until submit") {
		t.Fatalf("opening search dispatched work: intents=%+v", dispatcher.intents)
	}
	for _, character := range "api.qexample.com" {
		model, command = model.Update(key(character))
		if command != nil || len(dispatcher.intents) != 0 {
			t.Fatal("editing search dispatched work")
		}
	}
	model, command = model.Update(key(tea.KeyEnter))
	if command == nil || len(dispatcher.intents) != 0 {
		t.Fatal("submit did not create a deferred dispatch command")
	}
	model, _ = model.Update(command())
	if len(dispatcher.intents) != 1 || dispatcher.intents[0].Kind != IntentSearch || dispatcher.intents[0].SearchKind != "domain" ||
		dispatcher.intents[0].Query != "api.qexample.com" || dispatcher.intents[0].Scope != "all" {
		t.Fatalf("search intent=%+v", dispatcher.intents)
	}
}

func TestModelQQuitsRootHelpListDetailAndSearchControls(t *testing.T) {
	tests := []struct {
		name  string
		model Model
	}{
		{name: "root", model: NewModel(context.Background(), Config{}, nil)},
		{name: "help", model: func() Model {
			model := NewModel(context.Background(), Config{}, nil)
			model.help = true
			return model
		}()},
		{name: "list", model: Model{history: []routeFrame{{mode: routeList, stream: newTestIntentStream()}}}},
		{name: "detail", model: Model{history: []routeFrame{{mode: routeDetail, stream: newTestIntentStream()}}}},
		{name: "search kind", model: Model{history: []routeFrame{{mode: routeSearch, searchFocus: 0, stream: newTestIntentStream()}}}},
		{name: "search scope", model: Model{history: []routeFrame{{mode: routeSearch, searchFocus: 2, stream: newTestIntentStream()}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var active []*testIntentStream
			for index := range test.model.history {
				if stream, ok := test.model.history[index].stream.(*testIntentStream); ok {
					active = append(active, stream)
				}
			}
			updated, command := test.model.Update(key('q'))
			if command == nil || command() != tea.Quit() {
				t.Fatalf("q command=%v", command)
			}
			model := updated.(Model)
			for index := range model.history {
				if model.history[index].stream != nil {
					t.Fatalf("route %d retained an active stream", index)
				}
			}
			for index, stream := range active {
				if stream.cancels != 1 {
					t.Fatalf("stream %d cancellations=%d", index, stream.cancels)
				}
			}
		})
	}
}

func TestModelQQuitsAndCancelsDispatchBeforeStreamAcquisition(t *testing.T) {
	dispatcher := &blockingDispatcher{started: make(chan struct{}), done: make(chan struct{})}
	model, dispatch := NewModel(context.Background(), Config{}, dispatcher).Update(key(tea.KeyEnter))
	result := make(chan tea.Msg, 1)
	go func() { result <- dispatch() }()
	<-dispatcher.started

	model, quit := model.Update(key('q'))
	if quit == nil || quit() != tea.Quit() {
		t.Fatalf("q did not request tea.Quit: %v", quit)
	}
	select {
	case <-dispatcher.done:
	case <-time.After(time.Second):
		t.Fatal("q did not cancel the in-flight Dispatch context")
	}
	<-result
}

func TestModelRefreshPreservesOldProjectionAndRejectsLateStream(t *testing.T) {
	initial, refresh := newTestIntentStream(), newTestIntentStream()
	dispatcher := &recordingDispatcher{streams: []*testIntentStream{initial, refresh}}
	m := NewModel(context.Background(), Config{}, dispatcher)
	model, wait := runModelCommand(t, m, key(tea.KeyEnter))
	old := resourceProjection("old-instance", "running")
	initial.updates <- IntentUpdate{Query: QueryUpdate{Snapshot: QuerySnapshot{State: LoadReady, FetchedAt: time.Now()}}, Projection: IntentProjection{Resources: []ResourceProjection{old}}, Done: true}
	model, _ = model.Update(wait())
	model, wait = runModelCommand(t, model, ctrl('r'))
	refresh.updates <- IntentUpdate{Query: QueryUpdate{Snapshot: QuerySnapshot{State: LoadRefreshing}}, Projection: IntentProjection{Resources: []ResourceProjection{resourceProjection("uncommitted", "pending")}}}
	model, wait = model.Update(wait())
	if !strings.Contains(model.View().Content, "old-instance") || strings.Contains(model.View().Content, "uncommitted") {
		t.Fatalf("refresh exposed staged data: %s", model.View().Content)
	}
	newResource := resourceProjection("new-instance", "running")
	refresh.updates <- IntentUpdate{Query: QueryUpdate{Snapshot: QuerySnapshot{State: LoadReady, FetchedAt: time.Now()}}, Projection: IntentProjection{Resources: []ResourceProjection{newResource}}, Done: true}
	model, _ = model.Update(wait())
	if !strings.Contains(model.View().Content, "new-instance") || strings.Contains(model.View().Content, "old-instance") {
		t.Fatalf("refresh did not atomically replace projection: %s", model.View().Content)
	}
}

func TestModelRefreshEarlyFinalizationPreservesCachedFrame(t *testing.T) {
	stagedContext := testStoreContext(t, "staged", "999999999999", "us-west-2", 2)
	staged := IntentUpdate{
		Context:    &stagedContext,
		Query:      QueryUpdate{Snapshot: QuerySnapshot{State: LoadRefreshing}},
		Projection: IntentProjection{Resources: []ResourceProjection{resourceProjection("staged", "pending")}},
		Coverage:   &SearchCoverage{DiscoveryStatus: "staged-discovery", Profiles: []SearchProfileCoverage{{Profile: "staged", Status: "matched", Matches: 1}}},
	}
	tests := []struct {
		name string
		step func(Model) tea.Model
		want string
	}{
		{
			name: "dispatch error",
			step: func(model Model) tea.Model {
				updated, _ := model.Update(intentStartedMsg{generation: 1, result: IntentResultMsg{Intent: model.current().intent, Err: errors.New("boom\x1b[31m")}})
				return updated
			},
			want: "refresh failed",
		},
		{
			name: "nil stream",
			step: func(model Model) tea.Model {
				updated, _ := model.Update(intentStartedMsg{generation: 1, result: IntentResultMsg{Intent: model.current().intent}})
				return updated
			},
			want: "no update stream",
		},
		{
			name: "premature stream close",
			step: func(model Model) tea.Model {
				model.applyIntentUpdate(model.current(), staged)
				updated, _ := model.Update(intentStreamMsg{generation: 1, open: false})
				return updated
			},
			want: "refresh query failed · incomplete stream",
		},
		{
			name: "nonterminal done",
			step: func(model Model) tea.Model {
				update := staged
				update.Done = true
				updated, _ := model.Update(intentStreamMsg{generation: 1, open: true, update: update})
				return updated
			},
			want: "refresh query failed · incomplete stream",
		},
		{
			name: "terminal cancellation",
			step: func(model Model) tea.Model {
				model.applyIntentUpdate(model.current(), staged)
				update := staged
				update.Query.Snapshot.State = LoadCancelled
				update.Done = true
				updated, _ := model.Update(intentStreamMsg{generation: 1, open: true, update: update})
				return updated
			},
			want: "refresh cancelled",
		},
		{
			name: "terminal error",
			step: func(model Model) tea.Model {
				model.applyIntentUpdate(model.current(), staged)
				update := staged
				update.Query = QueryUpdate{Snapshot: QuerySnapshot{State: LoadThrottled}, Failure: &ProviderFailure{State: LoadThrottled, Service: "ec2\x1b[31m", Operation: "DescribeInstances"}}
				update.Done = true
				updated, _ := model.Update(intentStreamMsg{generation: 1, open: true, update: update})
				return updated
			},
			want: "refresh throttled",
		},
		{
			name: "terminal stale",
			step: func(model Model) tea.Model {
				model.applyIntentUpdate(model.current(), staged)
				update := staged
				update.Query.Snapshot.State = LoadStale
				update.Done = true
				updated, _ := model.Update(intentStreamMsg{generation: 1, open: true, update: update})
				return updated
			},
			want: "Stale · showing cached 1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model, stream := cachedRefreshingModel(t)
			updated := test.step(model).(Model)
			assertCachedModelRefreshTerminal(t, updated, stream, test.want)
		})
	}
}

func TestModelEscapeCancelsRefreshBeforeStreamAcquisition(t *testing.T) {
	dispatcher := &blockingDispatcher{started: make(chan struct{}), done: make(chan struct{})}
	model, _ := cachedRefreshingModel(t)
	model.finishFrame(model.current())
	model.dispatcher = dispatcher
	updated, dispatch := model.Update(ctrl('r'))
	result := make(chan tea.Msg, 1)
	go func() { result <- dispatch() }()
	<-dispatcher.started
	updated, _ = updated.Update(key(tea.KeyEscape))
	select {
	case <-dispatcher.done:
	case <-time.After(time.Second):
		t.Fatal("Esc did not cancel refresh Dispatch context")
	}
	frame := updated.(Model)
	if frame.current().refreshing || !strings.Contains(frame.current().status, "refresh cancelled") {
		t.Fatalf("Esc did not persist cancelled refresh: %+v", frame.current())
	}
	late, _ := updated.Update(<-result)
	lateModel := late.(Model)
	if !strings.Contains(lateModel.current().status, "refresh cancelled") {
		t.Fatalf("late cancelled Dispatch result escaped generation fence: %+v", lateModel.current())
	}
}

func TestModelRefreshCancellationFinalizesBeforeEscapeAndNavigation(t *testing.T) {
	for _, test := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{name: "escape", key: key(tea.KeyEscape)},
		{name: "enter", key: key(tea.KeyEnter)},
		{name: "search navigation", key: ctrl('g')},
	} {
		t.Run(test.name, func(t *testing.T) {
			model, stream := cachedRefreshingModel(t)
			stagedContext := testStoreContext(t, "staged", "999999999999", "us-west-2", 2)
			model.applyIntentUpdate(model.current(), IntentUpdate{
				Context: &stagedContext, Query: QueryUpdate{Snapshot: QuerySnapshot{State: LoadRefreshing}},
				Projection: IntentProjection{Resources: []ResourceProjection{resourceProjection("staged", "pending")}},
				Coverage:   &SearchCoverage{DiscoveryStatus: "staged-discovery"},
			})
			updated, _ := model.Update(test.key)
			result := updated.(Model)
			cached := &result.history[0]
			if cached.refreshing || cached.stream != nil || cached.staged != (refreshStage{}) || cached.context.Profile != "cached" || cached.projection.Resources[0].Title != "cached" || cached.coverage.DiscoveryStatus != "cached-discovery" || !strings.Contains(cached.status, "refresh cancelled") {
				t.Fatalf("navigation left incoherent refresh state: %+v", cached)
			}
			if stream.cancels != 1 {
				t.Fatalf("stream cancellations=%d", stream.cancels)
			}
			if test.name == "escape" && len(result.history) != 1 {
				t.Fatalf("escape during refresh navigated away: history=%d", len(result.history))
			}
		})
	}
}

func TestModelRefreshPromotesContextCoverageAndProjectionAtomically(t *testing.T) {
	model, stream := cachedRefreshingModel(t)
	replacementContext := testStoreContext(t, "replacement", "999999999999", "us-west-2", 2)
	replacementCoverage := &SearchCoverage{DiscoveryStatus: "replacement-discovery", Profiles: []SearchProfileCoverage{{Profile: "replacement", Status: "matched", Matches: 1}}}
	replacementProjection := IntentProjection{Resources: []ResourceProjection{resourceProjection("replacement", "running")}}
	staged := IntentUpdate{
		Context: &replacementContext, Query: QueryUpdate{Snapshot: QuerySnapshot{State: LoadRefreshing}},
		Coverage: replacementCoverage, Projection: replacementProjection,
	}
	updated, _ := model.Update(intentStreamMsg{generation: 1, open: true, update: staged})
	model = updated.(Model)
	if model.current().context.Profile != "cached" || model.current().coverage.DiscoveryStatus != "cached-discovery" || model.current().projection.Resources[0].Title != "cached" {
		t.Fatalf("staged refresh leaked before success: %+v", model.current())
	}
	updated, _ = model.Update(intentStreamMsg{generation: 1, open: true, update: IntentUpdate{Query: QueryUpdate{Snapshot: QuerySnapshot{State: LoadReady, FetchedAt: time.Now()}}, Done: true}})
	model = updated.(Model)
	frame := model.current()
	if frame.context.Profile != "replacement" || frame.coverage.DiscoveryStatus != "replacement-discovery" || frame.projection.Resources[0].Title != "replacement" || frame.staged != (refreshStage{}) || frame.refreshing || stream.cancels != 1 {
		t.Fatalf("successful refresh was not atomically promoted: %+v cancels=%d", frame, stream.cancels)
	}
}

func TestModelRefreshWithoutDispatcherFinalizes(t *testing.T) {
	model, _ := cachedRefreshingModel(t)
	model.finishFrame(model.current())
	model.current().status = "Ready · 1 resources"
	model.dispatcher = nil
	updated, command := model.Update(ctrl('r'))
	if command == nil {
		t.Fatal("nil dispatcher refresh did not produce a finalizing result")
	}
	updated, _ = updated.Update(command())
	result := updated.(Model)
	frame := result.current()
	if frame.refreshing || frame.staged != (refreshStage{}) || !strings.Contains(frame.status, "refresh failed") || !strings.Contains(frame.status, "no dispatcher") {
		t.Fatalf("nil dispatcher refresh did not finalize: %+v", frame)
	}
}

func cachedRefreshingModel(t *testing.T) (Model, *testIntentStream) {
	t.Helper()
	cachedContext := testStoreContext(t, "cached", "123456789012", "us-east-1", 1)
	stream := newTestIntentStream()
	model := NewModel(context.Background(), Config{}, nil)
	model.nextGeneration = 1
	model.history = []routeFrame{{
		mode: routeList, target: "cross-profile-search", label: "Search results · reader",
		intent:     Intent{Kind: IntentSearch, Target: "cross-profile-search", SearchKind: "role", Query: "reader", Scope: "all"},
		projection: IntentProjection{Resources: []ResourceProjection{resourceProjection("cached", "running")}},
		context:    &cachedContext, coverage: &SearchCoverage{DiscoveryStatus: "cached-discovery", Profiles: []SearchProfileCoverage{{Profile: "cached", Status: "matched", Matches: 1}}},
		stream: stream, generation: 1, refreshing: true, status: "Showing cached 1 · refreshing… · Esc cancel",
	}}
	return model, stream
}

func assertCachedModelRefreshTerminal(t *testing.T, model Model, stream *testIntentStream, want string) {
	t.Helper()
	frame := model.current()
	if frame == nil || frame.refreshing || frame.stream != nil || frame.staged != (refreshStage{}) {
		t.Fatalf("refresh did not finalize: %+v", frame)
	}
	if frame.context == nil || frame.context.Profile != "cached" || frame.coverage == nil || frame.coverage.DiscoveryStatus != "cached-discovery" || len(frame.projection.Resources) != 1 || frame.projection.Resources[0].Title != "cached" {
		t.Fatalf("cached tuple changed: %+v", frame)
	}
	if strings.Contains(frame.status, "\x1b") || strings.Contains(frame.status, "refreshing") || !strings.Contains(frame.status, want) {
		t.Fatalf("status=%q want %q", frame.status, want)
	}
	if stream.cancels != 1 {
		t.Fatalf("stream cancellations=%d", stream.cancels)
	}
}

func TestModelNonSearchRefreshPinsResolvedContext(t *testing.T) {
	initial, refresh := newTestIntentStream(), newTestIntentStream()
	dispatcher := &recordingDispatcher{streams: []*testIntentStream{initial, refresh}}
	model, wait := runModelCommand(t, NewModel(context.Background(), Config{}, dispatcher), key(tea.KeyEnter))
	resolved := testStoreContext(t, "dev", "123456789012", "us-west-2", 1)
	initial.updates <- IntentUpdate{
		Context: &resolved,
		Query:   QueryUpdate{Snapshot: QuerySnapshot{State: LoadReady, FetchedAt: time.Now()}},
		Projection: IntentProjection{Resources: []ResourceProjection{
			resourceProjection("old-instance", "running"),
		}},
		Done: true,
	}
	model, _ = model.Update(wait())
	model, dispatch := model.Update(ctrl('r'))
	if dispatch == nil {
		t.Fatal("refresh did not dispatch")
	}
	_, _ = model.Update(dispatch())
	if len(dispatcher.intents) != 2 || dispatcher.intents[0].Profile != "" || dispatcher.intents[0].Region != "" ||
		dispatcher.intents[1].Kind != IntentRefresh || dispatcher.intents[1].Profile != "dev" || dispatcher.intents[1].Region != "us-west-2" {
		t.Fatalf("non-search refresh did not pin resolved context: %+v", dispatcher.intents)
	}
}

func TestModelSearchRefreshRepeatsValidatedSearchIntentAndContext(t *testing.T) {
	initial, refresh := newTestIntentStream(), newTestIntentStream()
	dispatcher := &recordingDispatcher{streams: []*testIntentStream{initial, refresh}}
	config := Config{Profile: "dev", Region: "us-east-1"}
	var model tea.Model = NewModel(context.Background(), config, dispatcher)
	model, _ = model.Update(ctrl('g'))
	for _, character := range "reader" {
		model, _ = model.Update(key(character))
	}
	model, wait := runModelCommand(t, model, key(tea.KeyEnter))
	audit := testStoreContext(t, "audit", "999999999999", "us-west-2", 2)
	old := resourceProjection("old-role", "matched")
	initial.updates <- IntentUpdate{
		Context:    &audit,
		Query:      QueryUpdate{Snapshot: QuerySnapshot{State: LoadReady, FetchedAt: time.Now()}},
		Projection: IntentProjection{Resources: []ResourceProjection{old}},
		Coverage:   &SearchCoverage{DiscoveryStatus: "cached-discovery", Profiles: []SearchProfileCoverage{{Profile: "audit", Status: "matched", Matches: 1}}},
		Done:       true,
	}
	model, _ = model.Update(wait())

	model, dispatch := model.Update(ctrl('r'))
	if dispatch == nil || !strings.Contains(model.View().Content, "Showing cached 1 · refreshing") || !strings.Contains(model.View().Content, "old-role") {
		t.Fatalf("search refresh did not enter cached-refreshing state: %s", model.View().Content)
	}
	model, wait = model.Update(dispatch())
	if len(dispatcher.intents) != 2 {
		t.Fatalf("search refresh intents=%+v", dispatcher.intents)
	}
	want := Intent{Kind: IntentSearch, Target: "cross-profile-search", SearchKind: "domain", Query: "reader", Scope: "all", Profile: "dev", Region: "us-east-1"}
	if dispatcher.intents[0] != want || dispatcher.intents[1] != want {
		t.Fatalf("search refresh did not repeat original intent:\nfirst=%+v\nrefresh=%+v\nwant=%+v", dispatcher.intents[0], dispatcher.intents[1], want)
	}
	newResource := resourceProjection("new-role", "matched")
	refresh.updates <- IntentUpdate{
		Query:      QueryUpdate{Snapshot: QuerySnapshot{State: LoadRefreshing}},
		Projection: IntentProjection{Resources: []ResourceProjection{newResource}},
		Coverage:   &SearchCoverage{DiscoveryStatus: "replacement-discovery", Profiles: []SearchProfileCoverage{{Profile: "dev", Status: "matched", Matches: 1}}},
	}
	model, wait = model.Update(wait())
	refreshingView := model.View().Content
	if !strings.Contains(refreshingView, "Showing cached 1 · refreshing") || !strings.Contains(refreshingView, "old-role") ||
		strings.Contains(refreshingView, "new-role") || !strings.Contains(refreshingView, "cached-discovery") || strings.Contains(refreshingView, "replacement-discovery") {
		t.Fatalf("search refresh exposed staged coverage or results: %s", refreshingView)
	}
	refresh.updates <- IntentUpdate{
		Query:      QueryUpdate{Snapshot: QuerySnapshot{State: LoadReady, FetchedAt: time.Now()}},
		Projection: IntentProjection{Resources: []ResourceProjection{newResource}},
		Done:       true,
	}
	model, _ = model.Update(wait())
	if strings.Contains(model.View().Content, "old-role") || !strings.Contains(model.View().Content, "new-role") ||
		strings.Contains(model.View().Content, "cached-discovery") || !strings.Contains(model.View().Content, "replacement-discovery") || strings.Contains(model.View().Content, "unsupported") {
		t.Fatalf("search refresh did not atomically install results: %s", model.View().Content)
	}
}

func TestModelTypedStaleAndPartialFailuresAreSafe(t *testing.T) {
	stream := newTestIntentStream()
	dispatcher := &recordingDispatcher{streams: []*testIntentStream{stream}}
	m := NewModel(context.Background(), Config{}, dispatcher)
	model, wait := runModelCommand(t, m, key(tea.KeyEnter))
	when := time.Now()
	stream.updates <- IntentUpdate{Query: QueryUpdate{
		Snapshot: QuerySnapshot{State: LoadThrottled},
		Failure:  &ProviderFailure{State: LoadThrottled, Service: "ec2\x1b[31m", Operation: "Describe\nInstances", PartialPages: 2},
	}, Projection: IntentProjection{Resources: []ResourceProjection{resourceProjection("kept", "running")}}, Done: true}
	model, _ = model.Update(wait())
	view := model.View().Content
	if strings.Contains(view, "\x1b") || strings.Contains(view, "\nInstances") || !strings.Contains(view, "throttled") || !strings.Contains(view, "2 complete pages kept") {
		t.Fatalf("unsafe/incorrect partial status=%q", view)
	}

	stale := queryStatus(QueryUpdate{Snapshot: QuerySnapshot{State: LoadStale, FetchedAt: when, RefreshFailure: &RefreshFailure{State: LoadTimedOut, ObservedAt: when}}}, 3)
	if !strings.Contains(stale, "Stale") || !strings.Contains(stale, "timed out") {
		t.Fatalf("stale status=%q", stale)
	}
}

func TestModelResizePreservesRouteAndSelection(t *testing.T) {
	stream := newTestIntentStream()
	dispatcher := &recordingDispatcher{streams: []*testIntentStream{stream}}
	m := NewModel(context.Background(), Config{}, dispatcher)
	model, _ := m.Update(key('j'))
	model, _ = model.Update(key(tea.KeyEnter))
	model, _ = model.Update(tea.WindowSizeMsg{Width: 39, Height: 11})
	if model.View().Content != MinimumSizeMessage {
		t.Fatalf("small view=%q", model.View().Content)
	}
	model, _ = model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if !strings.Contains(model.View().Content, "Route 53 Hosted Zones") || !strings.Contains(model.View().Content, "Loading") {
		t.Fatalf("state not restored:\n%s", model.View().Content)
	}
	model, _ = model.Update(key(tea.KeyEscape))
	if stream.cancels != 0 { // Dispatch command never ran, so no stream was acquired.
		t.Fatalf("unexpected cancellation=%d", stream.cancels)
	}
	if !strings.Contains(model.View().Content, "> Route 53 Hosted Zones") {
		t.Fatalf("selection not restored:\n%s", model.View().Content)
	}
}

func TestIntentDispatchErrorIsSanitized(t *testing.T) {
	dispatcher := &recordingDispatcher{err: errors.New("not\x1b[31m integrated")}
	m := NewModel(context.Background(), Config{}, dispatcher)
	model, _ := runModelCommand(t, m, key(tea.KeyEnter))
	if strings.Contains(model.View().Content, "\x1b") || !strings.Contains(model.View().Content, "! ec2-instances: not [31m integrated") {
		t.Fatalf("view=%s", model.View().Content)
	}
}

func TestProjectQueryUpdateDerivesSafeFieldsAndNavigableRelations(t *testing.T) {
	store := NewSessionStore()
	awsContext := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	query := testQueryKey(t, awsContext)
	instance, _ := NewRegionalResourceKey(awsContext, "ec2.instance", "i-001")
	securityGroup, _ := NewRegionalResourceKey(awsContext, "ec2.security-group", "sg-001")
	when := time.Now().UTC()
	observation := testOperationObservation(t, awsContext, OperationDescribeInstances, map[string]any{
		"name": "web\x1b[31m", "state": "running", "private_ip_address": "10.0.1.24",
		"relations": []any{map[string]any{"target": securityGroup, "kind": "id-exact", "reason": "instance security group id"}},
	}, when)
	commitOneResource(t, store, query, instance, observation, when)
	snapshot, _ := store.Snapshot(query)
	projection := ProjectQueryUpdate(QueryUpdate{Key: query, Snapshot: snapshot})
	if len(projection.Resources) != 1 || len(projection.Resources[0].Relations) != 1 {
		t.Fatalf("projection=%+v", projection)
	}
	resource := projection.Resources[0]
	if strings.Contains(resource.Title, "\x1b") || resource.Relations[0].Target != "ec2.security-group:sg-001" || resource.Relations[0].Reason != "instance security group id" {
		t.Fatalf("unsafe/incomplete projection=%+v", resource)
	}
}

func TestProjectQueryUpdateKeepsUnmappedExactRelationsEvidenceOnly(t *testing.T) {
	store := NewSessionStore()
	awsContext := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	query := testQueryKey(t, awsContext)
	instance, _ := NewRegionalResourceKey(awsContext, "ec2.instance", "i-001")
	natGateway, _ := NewRegionalResourceKey(awsContext, "ec2.nat-gateway", "nat-001")
	when := time.Now().UTC()
	observation := testOperationObservation(t, awsContext, OperationDescribeInstances, map[string]any{
		"relations": []any{map[string]any{"target": natGateway, "kind": "id-exact", "reason": "route target nat gateway id"}},
	}, when)
	commitOneResource(t, store, query, instance, observation, when)
	snapshot, _ := store.Snapshot(query)
	relation := ProjectQueryUpdate(QueryUpdate{Key: query, Snapshot: snapshot}).Resources[0].Relations[0]
	if relation.Target != "" || relation.Label != "nat-001" || relation.Kind != "id-exact" || relation.Reason != "route target nat gateway id" {
		t.Fatalf("unmapped exact relation lost evidence or became navigable: %+v", relation)
	}
}

func TestSearchResultRelationDispatchUsesSelectedResourceContext(t *testing.T) {
	audit := testStoreContext(t, "audit", "999999999999", "us-west-2", 2)
	stream := newTestIntentStream()
	dispatcher := &recordingDispatcher{streams: []*testIntentStream{stream}}
	resource := ResourceProjection{
		Target: "resource-record-set:record", Title: "api.example.com.", Context: &audit,
		Relations: []ProjectionRelation{{Label: "Z1", Target: "hosted-zone:Z1", Kind: "api-exact"}},
	}
	m := NewModel(context.Background(), Config{Profile: "dev", Region: "us-east-1"}, dispatcher)
	m.history = []routeFrame{{mode: routeList, label: "Search results", projection: IntentProjection{Resources: []ResourceProjection{resource}}}}
	model, _ := m.Update(key(tea.KeyEnter))
	model, command := model.Update(key(tea.KeyEnter))
	if command == nil {
		t.Fatal("supported relation did not dispatch")
	}
	_ = command()
	if len(dispatcher.intents) != 1 || dispatcher.intents[0].Profile != "audit" || dispatcher.intents[0].Region != "us-west-2" || dispatcher.intents[0].Target != "hosted-zone:Z1" {
		t.Fatalf("relation intent=%+v", dispatcher.intents)
	}
}

func TestProjectQueryUpdatePreservesStructuredFieldsAndRoute53Evidence(t *testing.T) {
	store := NewSessionStore()
	awsContext := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	query, err := NewQueryKey(awsContext, ProviderRoute53, OperationListResourceRecordSets, map[string]string{"hosted-zone-id": "Z1"})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := NewGlobalResourceKey(awsContext, "route53.record-set", "api.example.com")
	when := time.Now().UTC()
	observation := testOperationObservation(t, awsContext, OperationListResourceRecordSets, map[string]any{
		"name":          "api.example.com.",
		"routing":       map[string]any{"values": []any{"10.0.0.1", "10.0.0.2"}},
		"zone_relation": map[string]any{"kind": "api-exact", "reason": "record-listed-from-hosted-zone", "scope": GlobalRegion, "operation": OperationListResourceRecordSets, "observed_at": when},
	}, when)
	commitOneResource(t, store, query, key, observation, when)
	snapshot, _ := store.Snapshot(query)
	projection := ProjectQueryUpdate(QueryUpdate{Key: query, Snapshot: snapshot})
	resource := projection.Resources[0]
	if len(resource.Relations) != 1 || resource.Relations[0].Label != "Hosted zone" || resource.Relations[0].Kind != "api-exact" || resource.Relations[0].Operation != OperationListResourceRecordSets {
		t.Fatalf("zone evidence=%+v", resource.Relations)
	}
	for _, field := range resource.Fields {
		if field.Label == "Routing" && strings.Contains(field.Value, "10.0.0.1") {
			return
		}
	}
	t.Fatalf("structured routing was collapsed: %+v", resource.Fields)
}

func TestChannelIntentStreamCancellationIsIdempotent(t *testing.T) {
	calls := 0
	stream := &ChannelIntentStream{C: make(chan IntentUpdate), CancelFunc: func() { calls++ }}
	stream.Cancel()
	stream.Cancel()
	if calls != 1 {
		t.Fatalf("cancel calls=%d", calls)
	}
}

func runModelCommand(t *testing.T, model tea.Model, msg tea.Msg) (tea.Model, tea.Cmd) {
	t.Helper()
	updated, cmd := model.Update(msg)
	if cmd == nil {
		return updated, nil
	}
	return updated.Update(cmd())
}

func resourceProjection(title, subtitle string) ResourceProjection {
	return ResourceProjection{
		Target:   "ec2.instance:" + title,
		Title:    title,
		Subtitle: subtitle,
		Fields:   []ProjectionField{{Label: "Private IP", Value: "10.0.1.24"}, {Label: "Long value", Value: strings.Repeat("metadata-", 12)}},
		Relations: []ProjectionRelation{{
			Label: "sg-web", Target: "ec2.security-group:sg-web", Kind: "id-exact", Reason: "instance security group id",
		}},
	}
}
