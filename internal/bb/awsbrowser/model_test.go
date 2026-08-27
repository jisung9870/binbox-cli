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
	for _, character := range "api.example.com" {
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
		dispatcher.intents[0].Query != "api.example.com" || dispatcher.intents[0].Scope != "all" {
		t.Fatalf("search intent=%+v", dispatcher.intents)
	}
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
