package awsbrowser

import (
	"context"
	"errors"
	"net/url"
	"reflect"
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

type contextRecordingDispatcher struct {
	recordingDispatcher
	choices         []ContextChoice
	resolution      ContextResolution
	listErr         error
	resolveErr      error
	listCalls       int
	resolvedProfile string
	resolvedRegion  string
}

func (dispatcher *contextRecordingDispatcher) ListContexts(context.Context) ([]ContextChoice, error) {
	dispatcher.listCalls++
	return append([]ContextChoice(nil), dispatcher.choices...), dispatcher.listErr
}

func (dispatcher *contextRecordingDispatcher) ResolveContext(_ context.Context, profile, region string) (ContextResolution, error) {
	dispatcher.resolvedProfile, dispatcher.resolvedRegion = profile, region
	return dispatcher.resolution, dispatcher.resolveErr
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
	dispatcher := new(contextRecordingDispatcher)
	m := NewModel(context.Background(), Config{Profile: "dev", Region: "ap-northeast-2", NoColor: true}, dispatcher)
	if cmd := m.Init(); cmd != nil {
		t.Fatalf("Init command=%v", cmd)
	}
	for _, want := range []string{"AWS Browser · READ ONLY", "EC2 Instances", "Route 53 Hosted Zones", "IAM Roles", "VPC & Networking", "Load Balancers (ALB/NLB)", "Cross-profile search", "Account unresolved", "Principal unresolved"} {
		if !strings.Contains(m.View().Content, want) {
			t.Fatalf("Home missing %q:\n%s", want, m.View().Content)
		}
	}
	var model tea.Model = m
	for _, msg := range []tea.Msg{tea.WindowSizeMsg{Width: 120, Height: 30}, key('j'), key('k'), key(tea.KeyPgDown), key(tea.KeyPgUp), key('?'), key('?')} {
		model, _ = model.Update(msg)
	}
	if len(dispatcher.intents) != 0 || dispatcher.listCalls != 0 {
		t.Fatalf("local navigation dispatched intents=%+v context_lists=%d", dispatcher.intents, dispatcher.listCalls)
	}
}

func TestModelK9sCommandJumpsToResourceAndRejectsUnknownAlias(t *testing.T) {
	dispatcher := new(recordingDispatcher)
	m := NewModel(context.Background(), Config{Profile: "dev", Region: "ap-northeast-2", NoColor: true}, dispatcher)
	model := tea.Model(m)
	for _, character := range ":ec2" {
		model, _ = model.Update(key(character))
	}
	if !strings.Contains(model.View().Content, ":ec2") {
		t.Fatalf("command line is not visible:\n%s", model.View().Content)
	}
	model, command := model.Update(key(tea.KeyEnter))
	commandModel := model.(Model)
	current := commandModel.current()
	if command == nil || current == nil || current.target != "ec2-instances" {
		t.Fatalf("ec2 alias did not open catalog: command=%v frame=%+v", command != nil, current)
	}
	_ = command()
	if len(dispatcher.intents) != 1 || dispatcher.intents[0].Target != "ec2-instances" {
		t.Fatalf("ec2 alias intent=%+v", dispatcher.intents)
	}

	unknown := NewModel(context.Background(), Config{NoColor: true}, nil)
	unknownModel := tea.Model(unknown)
	for _, character := range ":wat" {
		unknownModel, _ = unknownModel.Update(key(character))
	}
	unknownModel, _ = unknownModel.Update(key(tea.KeyEnter))
	if !strings.Contains(unknownModel.View().Content, "Unknown command: wat") {
		t.Fatalf("unknown command feedback is missing:\n%s", unknownModel.View().Content)
	}
}

func TestModelK9sLoadBalancerCommandsOpenAllALBAndNLB(t *testing.T) {
	tests := []struct {
		command string
		target  string
		label   string
	}{
		{command: "elbv2", target: "elbv2-load-balancers", label: "Load Balancers (ALB/NLB)"},
		{command: "alb", target: "elbv2-application-load-balancers", label: "Application Load Balancers"},
		{command: "nlb", target: "elbv2-network-load-balancers", label: "Network Load Balancers"},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			dispatcher := new(recordingDispatcher)
			model := tea.Model(NewModel(context.Background(), Config{Profile: "dev", Region: "ap-northeast-2", NoColor: true}, dispatcher))
			for _, character := range ":" + test.command {
				model, _ = model.Update(key(character))
			}
			model, command := model.Update(key(tea.KeyEnter))
			opened := model.(Model)
			if command == nil || opened.current() == nil || opened.current().target != test.target || opened.current().label != test.label {
				t.Fatalf("command=%v frame=%+v", command != nil, opened.current())
			}
			_ = command()
			if len(dispatcher.intents) != 1 || dispatcher.intents[0].Target != test.target {
				t.Fatalf("intents=%+v", dispatcher.intents)
			}
		})
	}
}

func TestLoadBalancerResourceRowsShowALBAndNLBTypes(t *testing.T) {
	headers := []string{"NAME", "TYPE", "ID", "STATUS"}
	for _, test := range []struct {
		kind string
		want string
	}{
		{kind: "application", want: "ALB"},
		{kind: "network", want: "NLB"},
	} {
		resource := ResourceProjection{
			Target: "elbv2.load-balancer:arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:loadbalancer/" + test.kind + "/api/123",
			Title:  "api-" + test.kind,
			Fields: []ProjectionField{{Label: "Type", Value: test.kind}, {Label: "State", Value: "active"}},
		}
		cells := resourceTableCells(resource, headers)
		if len(cells) != len(headers) || cells[1] != test.want {
			t.Fatalf("kind=%s cells=%v", test.kind, cells)
		}
	}
}

func TestModelK9sFilterAndBrowserHistory(t *testing.T) {
	resource := resourceProjection("web-api", "running")
	m := NewModel(context.Background(), Config{NoColor: true}, nil)
	m.history = []routeFrame{
		{mode: routeList, label: "EC2 Instances", projection: IntentProjection{Resources: []ResourceProjection{resource}}},
		{mode: routeDetail, label: "web-api", detail: resource},
	}

	model, _ := m.Update(ctrl('o'))
	back := model.(Model)
	if back.current() == nil || back.current().mode != routeList || len(back.forwardHistory) != 1 {
		t.Fatalf("ctrl+o did not move back: frame=%+v forward=%d", back.current(), len(back.forwardHistory))
	}
	model, _ = back.Update(ctrl('i'))
	forward := model.(Model)
	if forward.current() == nil || forward.current().mode != routeDetail || len(forward.forwardHistory) != 0 {
		t.Fatalf("ctrl+i did not move forward: frame=%+v forward=%d", forward.current(), len(forward.forwardHistory))
	}

	model, _ = back.Update(key('/'))
	filtered := model.(Model)
	if !filtered.current().filterActive || !strings.Contains(filtered.View().Content, "/  type to filter loaded resources") {
		t.Fatalf("slash did not focus local filter:\n%s", filtered.View().Content)
	}
	model, _ = filtered.Update(key(tea.KeyEscape))
	cleared := model.(Model)
	if cleared.current().filterActive {
		t.Fatalf("escape did not leave explicit filter mode: %+v", cleared.current())
	}
}

func TestModelStartsWithProfileSelectorWhenProfileIsOmitted(t *testing.T) {
	dispatcher := &contextRecordingDispatcher{choices: []ContextChoice{
		{Profile: "dev", Region: "us-east-1"},
		{Profile: "prod", Region: "ap-northeast-2"},
	}}
	m := NewModel(context.Background(), Config{Region: "us-west-2", NoColor: true}, dispatcher)
	if frame := m.current(); frame == nil || !frame.contextStartup || frame.mode != routeContext || !strings.Contains(m.View().Content, "Loading configured profiles") {
		t.Fatalf("profile-less browse did not start at context selector:\n%s", m.View().Content)
	}
	command := m.Init()
	if command == nil {
		t.Fatal("startup profile discovery command is nil")
	}
	model, _ := m.Update(command())
	loaded := model.(Model)
	frame := loaded.current()
	if dispatcher.listCalls != 1 || frame == nil || len(frame.contextChoices) != 2 || frame.contextRegion != "us-west-2" {
		t.Fatalf("startup profiles were not loaded with explicit region: calls=%d frame=%+v", dispatcher.listCalls, frame)
	}
	model, _ = loaded.Update(key(tea.KeyEscape))
	if !strings.Contains(model.View().Content, "Profile ambient") || strings.Contains(model.View().Content, "Select AWS context") {
		t.Fatalf("startup escape did not continue to ambient Home:\n%s", model.View().Content)
	}
}

func TestModelSelectsAndVerifiesContextBeforeApplyingIt(t *testing.T) {
	verified := testStoreContext(t, "prod", "999999999999", "us-west-2", 2)
	dispatcher := &contextRecordingDispatcher{
		choices: []ContextChoice{
			{Profile: "dev", Region: "us-east-1"},
			{Profile: "prod", Region: "ap-northeast-2"},
		},
		resolution: ContextResolution{Context: &verified},
	}
	model := tea.Model(NewModel(context.Background(), Config{Profile: "dev", Region: "us-east-1", NoColor: true}, dispatcher))

	model, _ = runModelCommand(t, model, key('c'))
	if dispatcher.listCalls != 1 || !strings.Contains(model.View().Content, "Select AWS context") ||
		!strings.Contains(model.View().Content, "Account follows verified profile credentials") {
		t.Fatalf("context selector did not load choices:\n%s", model.View().Content)
	}
	model, _ = model.Update(key(tea.KeyDown))
	if !strings.Contains(model.View().Content, "prod") || !strings.Contains(model.View().Content, "ap-northeast-2") {
		t.Fatalf("context choice did not update:\n%s", model.View().Content)
	}
	model, _ = model.Update(key(tea.KeyTab))
	model, _ = model.Update(ctrl('u'))
	for _, character := range "us-west-2" {
		model, _ = model.Update(key(character))
	}
	model, _ = runModelCommand(t, model, key(tea.KeyEnter))
	if dispatcher.resolvedProfile != "prod" || dispatcher.resolvedRegion != "us-west-2" ||
		!strings.Contains(model.View().Content, "999999999999") || !strings.Contains(model.View().Content, "enter apply") {
		t.Fatalf("context was not verified before apply: profile=%q region=%q\n%s", dispatcher.resolvedProfile, dispatcher.resolvedRegion, model.View().Content)
	}
	model, _ = model.Update(key(tea.KeyEnter))
	if !strings.Contains(model.View().Content, "Profile prod") || !strings.Contains(model.View().Content, "Account 999999999999") {
		t.Fatalf("verified context was not applied:\n%s", model.View().Content)
	}

	_, command := model.Update(key(tea.KeyEnter))
	if command == nil {
		t.Fatal("resource open did not dispatch after context apply")
	}
	_ = command()
	if len(dispatcher.intents) != 1 || dispatcher.intents[0].Profile != "prod" || dispatcher.intents[0].Region != "us-west-2" {
		t.Fatalf("resource intent=%+v", dispatcher.intents)
	}
}

func TestModelAppliesConfiguredAllRegionScopeAndPinsResourceRegion(t *testing.T) {
	verified := testStoreContext(t, "lg-udg-ops", "123456789012", "ap-northeast-2", 1)
	dispatcher := &contextRecordingDispatcher{
		choices: []ContextChoice{{
			Profile: "lg-udg-ops", Region: "ap-northeast-2", Group: "UDG",
			Regions: []string{"ap-northeast-2", "ap-southeast-1", "us-east-1", "eu-central-1"},
		}},
		resolution: ContextResolution{Context: &verified},
	}
	model := tea.Model(NewModel(context.Background(), Config{Profile: "lg-udg-ops", Region: "ap-northeast-2", NoColor: true}, dispatcher))
	model, _ = runModelCommand(t, model, key('c'))
	model, _ = model.Update(key(tea.KeyTab))
	model, _ = model.Update(key(tea.KeyTab))
	model, _ = model.Update(key(tea.KeyRight))
	if !strings.Contains(model.View().Content, "All UDG regions (4)") {
		t.Fatalf("all-region scope not rendered:\n%s", model.View().Content)
	}
	model, _ = runModelCommand(t, model, key(tea.KeyEnter))
	model, _ = model.Update(key(tea.KeyEnter))
	model, command := model.Update(key(tea.KeyEnter))
	if command == nil {
		t.Fatal("EC2 open command is nil")
	}
	_ = command()
	wantRegions := "ap-northeast-2,ap-southeast-1,us-east-1,eu-central-1"
	if len(dispatcher.intents) != 1 || dispatcher.intents[0].Regions != wantRegions {
		t.Fatalf("catalog intent=%+v", dispatcher.intents)
	}

	resourceContext := testStoreContext(t, "lg-udg-ops", "123456789012", "eu-central-1", 1)
	pinned := NewModel(context.Background(), Config{Profile: "lg-udg-ops", Region: "ap-northeast-2", Regions: wantRegions}, dispatcher)
	pinned.history = []routeFrame{{
		mode: routeList, label: "EC2 Instances", projection: IntentProjection{Resources: []ResourceProjection{{
			Target: "iam.role:reader", Title: "reader", Context: &resourceContext,
		}}},
	}}
	_, command = pinned.Update(key(tea.KeyEnter))
	if command == nil {
		t.Fatal("resource open command is nil")
	}
	_ = command()
	last := dispatcher.intents[len(dispatcher.intents)-1]
	if last.Region != "eu-central-1" || last.Regions != "" {
		t.Fatalf("resource intent was not pinned: %+v", last)
	}
}

func TestModelContextFailurePreservesPreviousContext(t *testing.T) {
	dispatcher := &contextRecordingDispatcher{
		choices: []ContextChoice{{Profile: "prod", Region: "ap-northeast-2"}},
		resolution: ContextResolution{Failure: &ProviderFailure{
			State: LoadAuthRequired, Kind: ProviderAuthRequired,
		}},
	}
	model := tea.Model(NewModel(context.Background(), Config{Profile: "dev", Region: "us-east-1"}, dispatcher))
	model, _ = runModelCommand(t, model, key('c'))
	model, _ = runModelCommand(t, model, key(tea.KeyEnter))
	if !strings.Contains(model.View().Content, "login required") {
		t.Fatalf("context failure not rendered:\n%s", model.View().Content)
	}
	model, _ = model.Update(key(tea.KeyEscape))
	if !strings.Contains(model.View().Content, "Profile dev") || strings.Contains(model.View().Content, "Profile prod") {
		t.Fatalf("failed context replaced previous selection:\n%s", model.View().Content)
	}
}

func TestModelFiltersContextProfilesLocallyAndEscapeClearsFirst(t *testing.T) {
	dispatcher := &contextRecordingDispatcher{choices: []ContextChoice{
		{Profile: "dev", Region: "us-east-1"},
		{Profile: "prod-readonly", Region: "ap-northeast-2"},
	}}
	model := tea.Model(NewModel(context.Background(), Config{Profile: "dev", Region: "us-east-1", NoColor: true}, dispatcher))
	model, _ = runModelCommand(t, model, key('c'))
	for _, character := range "prod" {
		model, _ = model.Update(key(character))
	}
	view := model.View().Content
	if !strings.Contains(view, "Search  prod") || !strings.Contains(view, "prod-readonly") || strings.Contains(view, "> dev") {
		t.Fatalf("profile filter did not narrow locally:\n%s", view)
	}
	if dispatcher.resolvedProfile != "" || len(dispatcher.intents) != 0 {
		t.Fatalf("profile filtering performed AWS work: resolve=%q intents=%+v", dispatcher.resolvedProfile, dispatcher.intents)
	}
	model, _ = model.Update(key(tea.KeyEscape))
	if !strings.Contains(model.View().Content, "Select AWS context") || !strings.Contains(model.View().Content, "Search  type to filter profiles") {
		t.Fatalf("first escape did not clear profile query:\n%s", model.View().Content)
	}
	model, _ = model.Update(key(tea.KeyEscape))
	if strings.Contains(model.View().Content, "Select AWS context") {
		t.Fatalf("second escape did not leave context selector:\n%s", model.View().Content)
	}
}

func TestModelProgressiveListDetailRelationAndHistory(t *testing.T) {
	first, relation := newTestIntentStream(), newTestIntentStream()
	dispatcher := &recordingDispatcher{streams: []*testIntentStream{first, relation}}
	m := NewModel(context.Background(), Config{Profile: "dev", Region: "ap-northeast-2", NoColor: true}, dispatcher)

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
	for _, want := range []string{"FIELD", "Private IP", "10.0.1.24", "CATEGORY", "Security groups", "ACTION"} {
		if !strings.Contains(model.View().Content, want) {
			t.Fatalf("detail missing %q:\n%s", want, model.View().Content)
		}
	}
	model, command := model.Update(key(tea.KeyEnter))
	if command != nil || !strings.Contains(model.View().Content, "ec2.security-group:sg-web") {
		t.Fatalf("relation category did not open locally: command=%v view=%s", command != nil, model.View().Content)
	}
	model, wait = runModelCommand(t, model, key(tea.KeyEnter))
	if got := dispatcher.intents[len(dispatcher.intents)-1].Target; got != "ec2.security-group:sg-web" {
		t.Fatalf("relation target=%q", got)
	}
	model, _ = model.Update(key(tea.KeyEscape))
	if relation.cancels != 1 || !strings.Contains(model.View().Content, "Security groups") || !strings.Contains(model.View().Content, "sg-web") {
		t.Fatalf("relation back did not cancel/restore category: cancels=%d view=%s", relation.cancels, model.View().Content)
	}
	model, _ = model.Update(key(tea.KeyEscape))
	if !strings.Contains(model.View().Content, "web-api") || !strings.Contains(model.View().Content, "Private IP") {
		t.Fatalf("category back did not restore detail: %s", model.View().Content)
	}
	model, _ = model.Update(key(tea.KeyEscape))
	if !strings.Contains(model.View().Content, "Resources (1)") {
		t.Fatalf("detail back did not restore list: %s", model.View().Content)
	}
}

func TestDetailGroupsRelationsAndOpensEachCategoryLocally(t *testing.T) {
	resource := ResourceProjection{
		Target: "ec2.instance:i-001", Title: "web-api",
		Relations: []ProjectionRelation{
			{Label: "sg-web", Target: "ec2.security-group:sg-web"},
			{Label: "vol-data", Target: "ec2.volume:vol-data"},
			{Label: "sg-ops", Target: "ec2.security-group:sg-ops"},
			{Label: "vpc-main", Target: "ec2.vpc:vpc-main"},
		},
	}
	m := NewModel(context.Background(), Config{NoColor: true}, new(recordingDispatcher))
	m.history = []routeFrame{{mode: routeDetail, detail: resource}}
	view := m.View().Content
	for _, want := range []struct {
		label string
		count string
	}{{"Security groups", "2"}, {"Volumes", "1"}, {"VPCs", "1"}} {
		if !viewLineContainsAll(view, want.label, want.count, "OPEN") {
			t.Fatalf("detail missing category %q count %s:\n%s", want.label, want.count, view)
		}
	}
	for _, hidden := range []string{"sg-web", "vol-data"} {
		if strings.Contains(view, hidden) {
			t.Fatalf("detail leaked relation %q before its category opened:\n%s", hidden, view)
		}
	}

	model, command := m.Update(key(tea.KeyEnter))
	if command != nil {
		t.Fatal("opening a relation category dispatched an AWS request")
	}
	view = model.View().Content
	if !strings.Contains(view, "sg-web") || !strings.Contains(view, "sg-ops") || strings.Contains(view, "vol-data") {
		t.Fatalf("security-group category contents are wrong:\n%s", view)
	}
	for _, character := range "ops" {
		model, _ = model.Update(key(character))
	}
	view = model.View().Content
	if !strings.Contains(view, "/  ops") || !strings.Contains(view, "sg-ops") || strings.Contains(view, "sg-web") {
		t.Fatalf("relation filter did not narrow locally:\n%s", view)
	}

	model, _ = model.Update(key(tea.KeyEscape))
	if !strings.Contains(model.View().Content, "Security groups (2)") {
		t.Fatalf("first escape did not clear relation filter:\n%s", model.View().Content)
	}
	model, _ = model.Update(key(tea.KeyEscape))
	model, _ = model.Update(key(tea.KeyDown))
	model, command = model.Update(key(tea.KeyEnter))
	if command != nil {
		t.Fatal("opening the volume category dispatched an AWS request")
	}
	view = model.View().Content
	if !strings.Contains(view, "vol-data") || strings.Contains(view, "sg-web") {
		t.Fatalf("volume category contents are wrong:\n%s", view)
	}
}

func TestHorizontalArrowsOpenAndReturnOneBrowserScreen(t *testing.T) {
	resource := resourceProjection("web-api", "running")
	m := NewModel(context.Background(), Config{NoColor: true}, new(recordingDispatcher))
	m.history = []routeFrame{{mode: routeList, label: "EC2 Instances", projection: IntentProjection{Resources: []ResourceProjection{resource}}}}

	model, command := m.Update(key(tea.KeyRight))
	currentModel := model.(Model)
	if command != nil || currentModel.current().mode != routeDetail {
		t.Fatalf("right did not open detail locally: command=%v frame=%+v", command != nil, currentModel.current())
	}
	model, command = model.Update(key(tea.KeyLeft))
	currentModel = model.(Model)
	if command != nil || currentModel.current().mode != routeList {
		t.Fatalf("left did not return one screen: command=%v frame=%+v", command != nil, currentModel.current())
	}
}

func TestExactLinkedSingletonOpensSummaryWithoutResourceDetour(t *testing.T) {
	stream := newTestIntentStream()
	resource := ResourceProjection{
		Target: "ec2.security-group:sg-001", Title: "web-sg", Subtitle: "sg-001 · web access",
		Fields: []ProjectionField{{Label: "Description", Value: "web access"}},
	}
	m := NewModel(context.Background(), Config{NoColor: true}, nil)
	m.history = []routeFrame{
		{mode: routeRelations, label: "Security groups"},
		{
			mode: routeList, generation: 1, stream: stream,
			intent: Intent{Kind: IntentOpen, Target: "ec2.security-group:sg-001"},
		},
	}
	model, _ := m.Update(intentStreamMsg{generation: 1, open: true, update: IntentUpdate{
		Query:      QueryUpdate{Snapshot: QuerySnapshot{State: LoadReady, FetchedAt: time.Now()}},
		Projection: IntentProjection{Resources: []ResourceProjection{resource}}, Done: true,
	}})
	current := model.(Model)
	if current.current().mode != routeDetail || !strings.Contains(current.View().Content, "AWS > web-sg > Summary") || strings.Contains(current.View().Content, "Resources (1)") {
		t.Fatalf("singleton did not open Summary directly: frame=%+v\n%s", current.current(), current.View().Content)
	}
	if stream.cancels != 1 {
		t.Fatalf("terminal singleton stream was not released: cancels=%d", stream.cancels)
	}
	back, _ := current.Update(key(tea.KeyLeft))
	backModel := back.(Model)
	if backModel.current().mode != routeRelations {
		t.Fatalf("left did not return directly to relation list: %+v", backModel.current())
	}
}

func TestSingletonPromotionRetainsTrueCollectionsAndAmbiguousResults(t *testing.T) {
	for _, test := range []struct {
		name      string
		target    string
		resources []ResourceProjection
	}{
		{name: "multiple exact results", target: "ec2.vpc:vpc-001", resources: []ResourceProjection{{Title: "one"}, {Title: "two"}}},
		{name: "security group inbound rules", target: "ec2.security-group-rules-inbound:sg-001", resources: []ResourceProjection{{Title: "https"}}},
		{name: "hosted zone record collection", target: "hosted-zone:Z001", resources: []ResourceProjection{{Title: "api.example.com"}}},
		{name: "DNS record collection", target: "route53.records:Z001", resources: []ResourceProjection{{Title: "api.example.com"}}},
		{name: "attached policy collection", target: "iam.role-attached-policies:reader", resources: []ResourceProjection{{Title: "ReadOnly"}}},
		{name: "inline policy collection", target: "iam.role-inline-policies:reader", resources: []ResourceProjection{{Title: "inline"}}},
		{name: "top level catalog", target: "ec2-instances", resources: []ResourceProjection{{Title: "web-api"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := newTestIntentStream()
			m := NewModel(context.Background(), Config{NoColor: true}, nil)
			m.history = []routeFrame{{
				mode: routeList, generation: 1, stream: stream,
				intent: Intent{Kind: IntentOpen, Target: test.target},
			}}
			model, _ := m.Update(intentStreamMsg{generation: 1, open: true, update: IntentUpdate{
				Query:      QueryUpdate{Snapshot: QuerySnapshot{State: LoadReady, FetchedAt: time.Now()}},
				Projection: IntentProjection{Resources: test.resources}, Done: true,
			}})
			current := model.(Model)
			if got := current.current().mode; got != routeList {
				t.Fatalf("collection was promoted to mode %d", got)
			}
		})
	}
}

func TestSummaryOpensFullDetailLocally(t *testing.T) {
	resource := ResourceProjection{
		Target: "ec2.instance:i-001", Title: "web-api", Subtitle: "i-001 · running",
		Fields: []ProjectionField{
			{Label: "State", Value: "running"},
			{Label: "Private IP", Value: "10.0.1.24"},
			{Label: "Alpha", Value: "one"},
			{Label: "Bravo", Value: "two"},
			{Label: "Charlie", Value: "three"},
			{Label: "Delta", Value: "four"},
			{Label: "Full Only", Value: "visible in full detail"},
		},
	}
	m := NewModel(context.Background(), Config{NoColor: true}, nil)
	m.history = []routeFrame{{mode: routeList, projection: IntentProjection{Resources: []ResourceProjection{resource}}}}
	model, command := m.Update(key(tea.KeyRight))
	current := model.(Model)
	if command != nil || current.current().mode != routeDetail || !strings.Contains(current.View().Content, "AWS > web-api > Summary") || strings.Contains(current.View().Content, "visible in full detail") {
		t.Fatalf("resource did not open compact Summary: command=%v\n%s", command != nil, current.View().Content)
	}
	model, command = current.Update(key(tea.KeyRight))
	current = model.(Model)
	if command != nil || current.current().mode != routeFields || !strings.Contains(current.View().Content, "AWS > web-api > Detail") || !strings.Contains(current.View().Content, "visible in full detail") {
		t.Fatalf("Detail did not open locally with all fields: command=%v\n%s", command != nil, current.View().Content)
	}
	model, _ = current.Update(key(tea.KeyLeft))
	current = model.(Model)
	if current.current().mode != routeDetail {
		t.Fatalf("left did not return to Summary: %+v", current.current())
	}
}

func TestSecurityGroupDetailOpensDirectionalRuleLists(t *testing.T) {
	awsContext := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	resourceKey, err := NewRegionalResourceKey(awsContext, "ec2.security-group", "sg-001")
	if err != nil {
		t.Fatal(err)
	}
	resource := ProjectResourceFields(resourceKey, map[string]any{
		"name": "web-sg", "description": "web access", "rules": []any{map[string]any{"direction": "ingress"}},
	})
	groups := relationGroups(resource)
	if len(groups) < 2 || groups[0].Label != "Inbound rules" || groups[1].Label != "Outbound rules" {
		t.Fatalf("security-group categories=%+v", groups)
	}
	for _, field := range resource.Fields {
		if field.Label == "Rules" {
			t.Fatalf("embedded rules blob remained in SG detail: %+v", resource.Fields)
		}
	}

	m := NewModel(context.Background(), Config{Profile: "dev", Region: "us-east-1", NoColor: true}, new(recordingDispatcher))
	m.history = []routeFrame{{mode: routeDetail, detail: resource, context: &awsContext}}
	model, command := m.Update(key(tea.KeyRight))
	currentModel := model.(Model)
	if command == nil || currentModel.current().intent.Target != "ec2.security-group-rules-inbound:sg-001" {
		t.Fatalf("inbound route=%+v command=%v", currentModel.current(), command != nil)
	}
	model, _ = model.Update(key(tea.KeyLeft))
	model, _ = model.Update(key(tea.KeyDown))
	model, command = model.Update(key(tea.KeyRight))
	currentModel = model.(Model)
	if command == nil || currentModel.current().intent.Target != "ec2.security-group-rules-outbound:sg-001" {
		t.Fatalf("outbound route=%+v command=%v", currentModel.current(), command != nil)
	}
}

func TestIAMAndRoute53SummariesExposePolicyAndDNSCategories(t *testing.T) {
	awsContext := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	roleKey, err := NewGlobalResourceKey(awsContext, "iam.role", "reader")
	if err != nil {
		t.Fatal(err)
	}
	role := ProjectResourceFields(roleKey, map[string]any{"role_name": "reader", "role_id": "ARO123"})
	groups := relationGroups(role)
	if len(groups) < 2 || groups[0].Label != "Attached policies" || groups[1].Label != "Inline policies" ||
		groups[0].Relations[0].Target != "iam.role-attached-policies:reader" || groups[1].Relations[0].Target != "iam.role-inline-policies:reader" {
		t.Fatalf("IAM role categories=%+v", groups)
	}

	policyARN := "arn:aws:iam::123456789012:policy/ReadOnly"
	policyKey, err := NewGlobalResourceKey(awsContext, "iam.managed-policy", policyARN)
	if err != nil {
		t.Fatal(err)
	}
	policy := ProjectResourceFields(policyKey, map[string]any{
		"policy_name": "ReadOnly", "default_version_id": "v3",
		"relations": []any{map[string]any{"target": policyKey, "kind": "api-exact", "reason": "role-attached-policy"}},
	})
	groups = relationGroups(policy)
	if policy.Title != "ReadOnly" || len(groups) != 1 || groups[0].Label != "Policy document" ||
		groups[0].Relations[0].Target != "iam.managed-policy-version:"+policyARN+":v3" {
		t.Fatalf("managed policy projection=%+v groups=%+v", policy, groups)
	}
	m := NewModel(context.Background(), Config{NoColor: true}, nil)
	m.history = []routeFrame{{mode: routeDetail, detail: policy}}
	if view := m.View().Content; !viewLineContainsAll(view, "Policy document", "VIEW") {
		t.Fatalf("managed policy document action is unclear:\n%s", view)
	}

	zoneKey, err := NewGlobalResourceKey(awsContext, "hosted-zone", "Z001")
	if err != nil {
		t.Fatal(err)
	}
	zone := ProjectResourceFields(zoneKey, map[string]any{"name": "example.com.", "record_count": int64(12), "private": false})
	groups = relationGroups(zone)
	if zone.Title != "example.com." || len(groups) != 1 || groups[0].Label != "DNS records" || groups[0].Relations[0].Target != "route53.records:Z001" {
		t.Fatalf("hosted zone projection=%+v groups=%+v", zone, groups)
	}
	m.history = []routeFrame{{mode: routeDetail, detail: zone}}
	if view := m.View().Content; !viewLineContainsAll(view, "DNS records", "LIST") {
		t.Fatalf("hosted zone DNS action is unclear:\n%s", view)
	}
}

func TestIAMAndRoute53SummaryCategoriesDispatchDirectly(t *testing.T) {
	for _, test := range []struct {
		name       string
		resource   ResourceProjection
		selection  int
		wantTarget string
	}{
		{
			name: "attached policies",
			resource: ResourceProjection{Title: "reader", Relations: []ProjectionRelation{
				{Label: "Attached policies", Target: "iam.role-attached-policies:reader"},
				{Label: "Inline policies", Target: "iam.role-inline-policies:reader"},
			}},
			wantTarget: "iam.role-attached-policies:reader",
		},
		{
			name: "inline policies",
			resource: ResourceProjection{Title: "reader", Relations: []ProjectionRelation{
				{Label: "Attached policies", Target: "iam.role-attached-policies:reader"},
				{Label: "Inline policies", Target: "iam.role-inline-policies:reader"},
			}},
			selection: 1, wantTarget: "iam.role-inline-policies:reader",
		},
		{
			name: "DNS records",
			resource: ResourceProjection{Title: "example.com.", Relations: []ProjectionRelation{
				{Label: "DNS records", Target: "route53.records:Z001"},
			}},
			wantTarget: "route53.records:Z001",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := new(recordingDispatcher)
			m := NewModel(context.Background(), Config{NoColor: true}, dispatcher)
			m.history = []routeFrame{{mode: routeDetail, detail: test.resource, relationSelected: test.selection}}
			model, command := m.Update(key(tea.KeyRight))
			current := model.(Model)
			if command == nil || current.current().intent.Target != test.wantTarget || len(current.history) != 2 {
				t.Fatalf("category did not dispatch directly: command=%v frame=%+v", command != nil, current.current())
			}
		})
	}
}

func TestRoute53CloudFrontS3TracePreservesPathCategories(t *testing.T) {
	awsContext := testStoreContext(t, "lg-udg-ops", "123456789012", "ap-northeast-2", 1)
	recordKey, err := NewGlobalResourceKey(awsContext, "resource-record-set", "binary-record")
	if err != nil {
		t.Fatal(err)
	}
	distributionTarget, err := NewGlobalResourceKey(awsContext, "cloudfront.distribution-domain", "d24odq2ocbsmjd.cloudfront.net")
	if err != nil {
		t.Fatal(err)
	}
	record := ProjectResourceFields(recordKey, map[string]any{
		"name": "binary.udg.line.games.",
		"alias_relation": map[string]any{
			"target": distributionTarget, "kind": "api-exact", "reason": "alias-target-returned-by-api",
		},
	})
	groups := relationGroups(record)
	if len(groups) != 1 || groups[0].Key != "alias-targets" || !directRelationGroup(groups[0]) {
		t.Fatalf("record groups=%+v", groups)
	}
	dispatcher := new(recordingDispatcher)
	m := NewModel(context.Background(), Config{NoColor: true}, dispatcher)
	m.history = []routeFrame{{mode: routeDetail, detail: record, context: &awsContext}}
	if view := m.View().Content; !viewLineContainsAll(view, "Alias target", "TRACE") {
		t.Fatalf("trace action missing:\n%s", view)
	}
	model, command := m.Update(key(tea.KeyRight))
	aliasModel := model.(Model)
	if command == nil || aliasModel.current().intent.Target != "cloudfront.distribution-domain:d24odq2ocbsmjd.cloudfront.net" {
		t.Fatalf("alias target did not dispatch: frame=%+v", aliasModel.current())
	}

	distributionKey, err := NewGlobalResourceKey(awsContext, "cloudfront.distribution", "E3M80I51D1TQ9P")
	if err != nil {
		t.Fatal(err)
	}
	krBucket, _ := NewGlobalResourceKey(awsContext, "s3.bucket", "udg-kr-game-binary")
	usBucket, _ := NewGlobalResourceKey(awsContext, "s3.bucket", "udg-us-game-dump")
	distribution := ProjectResourceFields(distributionKey, map[string]any{
		"domain_name": "d24odq2ocbsmjd.cloudfront.net",
		"relations": []any{
			map[string]any{"label": "Default /* → kr origin", "target": krBucket, "relation_type": "routes-to", "direction": "outgoing", "condition": "*", "kind": "inferred", "reason": "cloudfront-s3-origin-domain"},
			map[string]any{"label": "report/* → us origin", "target": usBucket, "relation_type": "routes-to", "direction": "outgoing", "condition": "report/*", "kind": "inferred", "reason": "cloudfront-s3-origin-domain"},
			map[string]any{"label": "character/* → us origin", "target": usBucket, "relation_type": "routes-to", "direction": "outgoing", "condition": "character/*", "kind": "inferred", "reason": "cloudfront-s3-origin-domain"},
		},
	})
	groups = relationGroups(distribution)
	if distribution.Title != "d24odq2ocbsmjd.cloudfront.net" || len(groups) != 1 || groups[0].Key != "origins" || len(groups[0].Relations) != 3 ||
		groups[0].Relations[1].Label != "report/* → us origin" || groups[0].Relations[1].Type != "routes-to" || groups[0].Relations[1].Condition != "report/*" {
		t.Fatalf("distribution=%+v groups=%+v", distribution, groups)
	}
	m.history = []routeFrame{{mode: routeDetail, detail: distribution, context: &awsContext}}
	model, command = m.Update(key(tea.KeyRight))
	current := model.(Model)
	if command != nil || current.current().mode != routeRelations || current.current().relationGroup != "origins" {
		t.Fatalf("origins did not open locally: command=%v frame=%+v", command != nil, current.current())
	}
	if view := current.View().Content; !strings.Contains(view, "RELATION") || !strings.Contains(view, "TARGET") || !strings.Contains(view, "CONDITION") ||
		!strings.Contains(view, "report/*") || !strings.Contains(view, "character/*") || !strings.Contains(view, "routes-to") ||
		!strings.Contains(view, "s3.bucket:udg-us-game-dump") || !strings.Contains(view, "condition *") {
		t.Fatalf("path-aware origins missing:\n%s", view)
	}
}

func TestRoute53ELBV2TraceUsesDirectK9sResourceCategories(t *testing.T) {
	awsContext := testStoreContext(t, "lg-udg-ops", "123456789012", "ap-northeast-2", 1)
	const (
		loadBalancerARN = "arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:loadbalancer/app/api/111"
		listenerARN     = "arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:listener/app/api/111/222"
		targetGroupARN  = "arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:targetgroup/api/333"
	)

	recordKey, _ := NewGlobalResourceKey(awsContext, "resource-record-set", "api-record")
	loadBalancerDNS, _ := NewRegionalResourceKey(awsContext, "elbv2.load-balancer-dns", "dualstack.api-123.elb.ap-northeast-2.amazonaws.com")
	record := ProjectResourceFields(recordKey, map[string]any{"name": "api.example.com.", "alias_relation": map[string]any{
		"target": loadBalancerDNS, "relation_type": "alias-to", "direction": "outgoing", "condition": "A alias", "kind": "api-exact",
	}})
	groups := relationGroups(record)
	if len(groups) != 1 || groups[0].Key != "alias-targets" || !directRelationGroup(groups[0]) {
		t.Fatalf("record groups=%+v", groups)
	}

	loadBalancerKey, _ := NewRegionalResourceKey(awsContext, "elbv2.load-balancer", loadBalancerARN)
	loadBalancer := ProjectResourceFields(loadBalancerKey, map[string]any{"name": "api", "state": "active"})
	groups = relationGroups(loadBalancer)
	if len(groups) != 1 || groups[0].Key != "listeners" || !directRelationGroup(groups[0]) || groups[0].Relations[0].Target != "elbv2.listeners:"+loadBalancerARN {
		t.Fatalf("load balancer groups=%+v", groups)
	}

	listenerKey, _ := NewRegionalResourceKey(awsContext, "elbv2.listener", listenerARN)
	targetGroupKey, _ := NewRegionalResourceKey(awsContext, "elbv2.target-group", targetGroupARN)
	listener := ProjectResourceFields(listenerKey, map[string]any{
		"name": "HTTPS 443",
		"relations": []any{map[string]any{
			"target": targetGroupKey, "label": "api", "relation_type": "routes-to", "direction": "outgoing",
			"condition": "default; action-order=1", "kind": "api-exact",
		}},
	})
	groups = relationGroups(listener)
	if len(groups) != 2 || groups[0].Key != "listener-rules" || groups[1].Key != "target-groups" || !directRelationGroup(groups[0]) || !directRelationGroup(groups[1]) {
		t.Fatalf("listener groups=%+v", groups)
	}
	networkListenerARN := "arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:listener/net/internal/444/555"
	networkListenerKey, _ := NewRegionalResourceKey(awsContext, "elbv2.listener", networkListenerARN)
	networkListener := ProjectResourceFields(networkListenerKey, map[string]any{
		"name": "TCP 443",
		"relations": []any{map[string]any{
			"target": targetGroupKey, "label": "api", "relation_type": "routes-to", "direction": "outgoing",
			"condition": "default; action-order=1", "kind": "api-exact",
		}},
	})
	groups = relationGroups(networkListener)
	if len(groups) != 1 || groups[0].Key != "target-groups" || !directRelationGroup(groups[0]) {
		t.Fatalf("NLB listener exposed ALB-only rules or lost its default target group: groups=%+v", groups)
	}

	targetGroup := ProjectResourceFields(targetGroupKey, map[string]any{"name": "api", "target_type": "instance"})
	groups = relationGroups(targetGroup)
	if len(groups) != 1 || groups[0].Key != "targets" || !directRelationGroup(groups[0]) {
		t.Fatalf("target group groups=%+v", groups)
	}
	_, targetQuery, _ := strings.Cut(groups[0].Relations[0].Target, ":")
	values, err := url.ParseQuery(targetQuery)
	if err != nil || values.Get("target-group-arn") != targetGroupARN || values.Get("target-type") != "instance" {
		t.Fatalf("target query=%q values=%v error=%v", targetQuery, values, err)
	}

	targetKey, _ := NewRegionalResourceKey(awsContext, "elbv2.target", "target-id=i-123&target-group=api")
	instanceKey, _ := NewRegionalResourceKey(awsContext, "ec2.instance", "i-123")
	target := ProjectResourceFields(targetKey, map[string]any{"target_id": "i-123", "health_state": "healthy", "relations": []any{map[string]any{
		"target": instanceKey, "relation_type": "routes-to", "direction": "outgoing", "condition": "target-type=instance", "kind": "api-exact",
	}}})
	groups = relationGroups(target)
	if target.Title != "i-123" || len(groups) != 1 || groups[0].Key != "instances" || groups[0].Relations[0].Target != "ec2.instance:i-123" {
		t.Fatalf("target=%+v groups=%+v", target, groups)
	}
}

func TestExactRelationTargetsPromoteToSummary(t *testing.T) {
	for _, target := range []string{
		"cloudfront.distribution-domain:d24odq2ocbsmjd.cloudfront.net",
		"elbv2.load-balancer-dns:api-123.elb.ap-northeast-2.amazonaws.com",
		"elbv2.load-balancer:arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:loadbalancer/app/api/111",
		"elbv2.target-group:arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:targetgroup/api/333",
		"s3.bucket:udg-kr-game-binary",
	} {
		t.Run(target, func(t *testing.T) {
			m := NewModel(context.Background(), Config{NoColor: true}, nil)
			m.history = []routeFrame{{mode: routeList, generation: 1, intent: Intent{Kind: IntentOpen, Target: target}}}
			model, _ := m.Update(intentStreamMsg{generation: 1, open: true, update: IntentUpdate{
				Query:      QueryUpdate{Snapshot: QuerySnapshot{State: LoadReady, FetchedAt: time.Now()}},
				Projection: IntentProjection{Resources: []ResourceProjection{{Target: target, Title: target}}}, Done: true,
			}})
			currentModel := model.(Model)
			if current := currentModel.current(); current.mode != routeDetail {
				t.Fatalf("target did not promote: %+v", current)
			}
		})
	}
}

func TestIAMListRowsHydrateBeforeSummary(t *testing.T) {
	for _, target := range []string{
		"iam.role:reader",
		"iam.managed-policy:arn:aws:iam::123456789012:policy/ReadOnly",
		"iam.inline-policy:reader:inline",
	} {
		t.Run(target, func(t *testing.T) {
			dispatcher := new(recordingDispatcher)
			m := NewModel(context.Background(), Config{NoColor: true}, dispatcher)
			m.history = []routeFrame{{mode: routeList, projection: IntentProjection{Resources: []ResourceProjection{{Target: target, Title: "result"}}}}}
			model, command := m.Update(key(tea.KeyRight))
			current := model.(Model)
			if command == nil || current.current().mode != routeList || current.current().intent.Target != target || len(dispatcher.intents) != 0 {
				t.Fatalf("IAM row was not prepared for exact hydration: command=%v frame=%+v intents=%+v", command != nil, current.current(), dispatcher.intents)
			}
			_ = command()
			if len(dispatcher.intents) != 1 || dispatcher.intents[0].Target != target {
				t.Fatalf("IAM hydration intent=%+v", dispatcher.intents)
			}
		})
	}
}

func TestSecurityGroupRuleProjectionUsesReadableListIdentity(t *testing.T) {
	awsContext := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	resourceKey, err := NewRegionalResourceKey(awsContext, "ec2.security-group-rule", "sgr-001")
	if err != nil {
		t.Fatal(err)
	}
	resource := ProjectResourceFields(resourceKey, map[string]any{
		"protocol": "tcp", "from_port": int32(443), "to_port": int32(443),
		"cidr_ipv4": "0.0.0.0/0", "description": "public https",
	})
	if resource.Title != "TCP 443 · 0.0.0.0/0" || resource.Subtitle != "sgr-001 · public https" {
		t.Fatalf("security-group rule projection=%+v", resource)
	}
}

func TestModelFiltersLoadedResourcesLocallyAndOpensVisibleMatch(t *testing.T) {
	dispatcher := new(recordingDispatcher)
	m := NewModel(context.Background(), Config{Profile: "dev", Region: "ap-northeast-2", NoColor: true}, dispatcher)
	m.history = []routeFrame{{mode: routeList, label: "EC2 Instances", projection: IntentProjection{Resources: []ResourceProjection{
		{Target: "ec2.instance:i-dev", Title: "dev-api", Subtitle: "i-dev · running"},
		{Target: "ec2.instance:i-prod", Title: "prod-api", Subtitle: "i-prod · stopped"},
	}}}}
	model := tea.Model(m)
	for _, character := range "i-prod" {
		model, _ = model.Update(key(character))
	}
	view := model.View().Content
	if !strings.Contains(view, "/  i-prod") || !strings.Contains(view, "prod-api") || strings.Contains(view, "dev-api") || !strings.Contains(view, "Resources (1/2)") {
		t.Fatalf("resource filter did not narrow loaded projection:\n%s", view)
	}
	if len(dispatcher.intents) != 0 {
		t.Fatalf("resource filtering dispatched AWS intent: %+v", dispatcher.intents)
	}
	model, _ = model.Update(key(tea.KeyEnter))
	currentModel := model.(Model)
	current := currentModel.current()
	if current == nil || current.mode != routeDetail || current.detail.Target != "ec2.instance:i-prod" {
		t.Fatalf("filtered selection opened wrong resource: %+v", current)
	}
}

func TestProjectResourceFieldsPrefersEC2NameAndFallsBackToInstanceID(t *testing.T) {
	awsContext := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	key, err := NewRegionalResourceKey(awsContext, "ec2.instance", "i-001")
	if err != nil {
		t.Fatal(err)
	}
	named := ProjectResourceFields(key, map[string]any{"name": "web-api", "state": "running"})
	if named.Title != "web-api" || named.Subtitle != "i-001 · running" {
		t.Fatalf("named projection=%+v", named)
	}
	unnamed := ProjectResourceFields(key, map[string]any{"name": "  ", "state": "stopped"})
	if unnamed.Title != "i-001" || unnamed.Subtitle != "stopped" {
		t.Fatalf("unnamed projection=%+v", unnamed)
	}
}

func TestProjectResourceFieldsPrefersNameTagThenNativeNameThenID(t *testing.T) {
	awsContext := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	vpcKey, err := NewRegionalResourceKey(awsContext, "ec2.vpc", "vpc-001")
	if err != nil {
		t.Fatal(err)
	}
	named := ProjectResourceFields(vpcKey, map[string]any{
		"state": "available", "tags": map[string]string{"Owner": "platform", "Name": "main-vpc"},
	})
	if named.Title != "main-vpc" || named.Subtitle != "vpc-001 · available" ||
		!reflect.DeepEqual(named.Tags, []ProjectionTag{{Key: "Name", Value: "main-vpc"}, {Key: "Owner", Value: "platform"}}) {
		t.Fatalf("Name-tag projection=%+v", named)
	}
	for _, field := range named.Fields {
		if field.Label == "Tags" {
			t.Fatalf("tags remained in detail fields: %+v", named.Fields)
		}
	}

	securityGroupKey, err := NewRegionalResourceKey(awsContext, "ec2.security-group", "sg-001")
	if err != nil {
		t.Fatal(err)
	}
	native := ProjectResourceFields(securityGroupKey, map[string]any{"name": "web-sg", "description": "web access"})
	if native.Title != "web-sg" || native.Subtitle != "sg-001 · web access" {
		t.Fatalf("native-name projection=%+v", native)
	}
	unnamed := ProjectResourceFields(vpcKey, map[string]any{"state": "available", "tags": map[string]string{"Owner": "platform"}})
	if unnamed.Title != "vpc-001" || unnamed.Subtitle != "available" {
		t.Fatalf("ID fallback projection=%+v", unnamed)
	}
}

func TestTagsCategoryOpensAndFiltersLocally(t *testing.T) {
	resource := ResourceProjection{
		Target: "ec2.vpc:vpc-001", Title: "main-vpc", Subtitle: "vpc-001 · available",
		Tags: []ProjectionTag{{Key: "Name", Value: "main-vpc"}, {Key: "Owner", Value: "platform"}},
	}
	dispatcher := new(recordingDispatcher)
	m := NewModel(context.Background(), Config{NoColor: true}, dispatcher)
	m.history = []routeFrame{{mode: routeDetail, detail: resource}}
	detail := m.View().Content
	if !viewLineContainsAll(detail, "Tags", "2", "VIEW") || strings.Contains(detail, "Owner=platform") {
		t.Fatalf("detail tags category is wrong:\n%s", detail)
	}

	model, _ := m.Update(key(tea.KeyDown))
	model, command := model.Update(key(tea.KeyRight))
	if command != nil || len(dispatcher.intents) != 0 {
		t.Fatal("opening Tags dispatched an AWS request")
	}
	view := model.View().Content
	if !strings.Contains(view, "AWS > main-vpc > Tags") || !strings.Contains(view, "Owner") || !strings.Contains(view, "platform") {
		t.Fatalf("tags screen is incomplete:\n%s", view)
	}
	for _, character := range "owner" {
		model, _ = model.Update(key(character))
	}
	view = model.View().Content
	if !strings.Contains(view, "Tags (1/2)") || strings.Contains(view, "> Name") || !strings.Contains(view, "> Owner") {
		t.Fatalf("tag filtering is wrong:\n%s", view)
	}
	model, _ = model.Update(key(tea.KeyLeft))
	currentModel := model.(Model)
	if currentModel.current().mode != routeDetail {
		t.Fatalf("left did not return from Tags: %+v", currentModel.current())
	}
}

func viewLineContainsAll(view string, values ...string) bool {
	for _, line := range strings.Split(view, "\n") {
		matched := true
		for _, value := range values {
			if !strings.Contains(line, value) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
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

func TestModelCancelledListFrameRejectsQueuedMessagesAfterNavigationBack(t *testing.T) {
	for _, test := range []struct {
		name     string
		navigate tea.KeyPressMsg
	}{
		{name: "detail", navigate: key(tea.KeyEnter)},
		{name: "search", navigate: ctrl('g')},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := newTestIntentStream()
			dispatcher := &recordingDispatcher{streams: []*testIntentStream{stream}}
			model, wait := runModelCommand(t, NewModel(context.Background(), Config{}, dispatcher), key(tea.KeyEnter))
			original := resourceProjection("original", "loading")
			stream.updates <- IntentUpdate{
				Query:      QueryUpdate{Snapshot: QuerySnapshot{State: LoadLoading}},
				Projection: IntentProjection{Resources: []ResourceProjection{original}},
			}
			model, wait = model.Update(wait())

			stream.updates <- IntentUpdate{
				Query:      QueryUpdate{Snapshot: QuerySnapshot{State: LoadLoading}},
				Projection: IntentProjection{Resources: []ResourceProjection{resourceProjection("late", "loading")}},
			}
			queuedUpdate := wait()
			concrete := model.(Model)
			generation := concrete.current().generation
			close(stream.updates)
			queuedClose := waitIntent(generation, stream)()

			model, _ = model.Update(test.navigate)
			model, _ = model.Update(key(tea.KeyEscape))
			model, _ = model.Update(queuedUpdate)
			model, _ = model.Update(queuedClose)

			concrete = model.(Model)
			frame := concrete.current()
			if frame == nil || frame.generation == generation || len(frame.projection.Resources) != 1 ||
				frame.projection.Resources[0].Title != "original" || strings.Contains(frame.status, "incomplete") {
				t.Fatalf("queued messages mutated cancelled frame: %+v", frame)
			}
			if stream.cancels != 1 {
				t.Fatalf("stream cancellations=%d", stream.cancels)
			}
		})
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

func TestModelQQuitsRootHelpDetailAndSearchControls(t *testing.T) {
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

func TestModelQFiltersResourceAndContextLists(t *testing.T) {
	resourceModel := Model{history: []routeFrame{{mode: routeList, projection: IntentProjection{Resources: []ResourceProjection{{Title: "qa-api"}}}}}}
	updated, command := resourceModel.Update(key('q'))
	updatedResourceModel := updated.(Model)
	if command != nil || updatedResourceModel.current().filterValue != "q" {
		t.Fatalf("q did not filter resource list: model=%+v command=%v", updated, command)
	}
	contextModel := Model{history: []routeFrame{{mode: routeContext, contextChoices: []ContextChoice{{Profile: "qa"}}}}}
	updated, command = contextModel.Update(key('q'))
	updatedContextModel := updated.(Model)
	if command != nil || updatedContextModel.current().contextQuery != "q" {
		t.Fatalf("q did not filter context list: model=%+v command=%v", updated, command)
	}
}

func TestModelCtrlCQuitsAndCancelsDispatchBeforeStreamAcquisition(t *testing.T) {
	dispatcher := &blockingDispatcher{started: make(chan struct{}), done: make(chan struct{})}
	model, dispatch := NewModel(context.Background(), Config{}, dispatcher).Update(key(tea.KeyEnter))
	result := make(chan tea.Msg, 1)
	go func() { result <- dispatch() }()
	<-dispatcher.started

	model, quit := model.Update(ctrl('c'))
	if quit == nil || quit() != tea.Quit() {
		t.Fatalf("ctrl+c did not request tea.Quit: %v", quit)
	}
	select {
	case <-dispatcher.done:
	case <-time.After(time.Second):
		t.Fatal("ctrl+c did not cancel the in-flight Dispatch context")
	}
	<-result
}

func TestModelRefreshPreservesOldProjectionAndRejectsLateStream(t *testing.T) {
	initial, refresh := newTestIntentStream(), newTestIntentStream()
	dispatcher := &recordingDispatcher{streams: []*testIntentStream{initial, refresh}}
	m := NewModel(context.Background(), Config{NoColor: true}, dispatcher)
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
	m := NewModel(context.Background(), Config{NoColor: true}, dispatcher)
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
	m := NewModel(context.Background(), Config{NoColor: true}, dispatcher)
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
	if relation.Target != "" || relation.TargetRef != "ec2.nat-gateway:nat-001" || relation.Label != "nat-001" || relation.Kind != "id-exact" || relation.Reason != "route target nat gateway id" {
		t.Fatalf("unmapped exact relation lost evidence or became navigable: %+v", relation)
	}
}

func TestEvidenceOnlyRelationEnterAndEOpenEvidenceDetail(t *testing.T) {
	relation := ProjectionRelation{
		Label: "API alias", TargetRef: "api.example.execute-api.us-east-1.amazonaws.com.",
		Type: "alias-to", Direction: "outgoing", Condition: "A alias", Kind: "api-exact",
		Reason: "alias-target-returned-by-api", Operation: OperationListResourceRecordSets,
		Scope: GlobalRegion, ObservedAt: "2026-08-28T08:00:00Z",
	}
	for _, keyName := range []string{"enter", "e"} {
		t.Run(keyName, func(t *testing.T) {
			m := NewModel(context.Background(), Config{NoColor: true}, new(recordingDispatcher))
			m.history = []routeFrame{{
				mode: routeRelations, label: "Relationship evidence", relationGroup: "evidence",
				detail: ResourceProjection{Title: "api.example.com", Relations: []ProjectionRelation{relation}},
			}}
			if view := m.View().Content; !strings.Contains(view, "api.example") || !strings.Contains(view, "enter evidence") {
				t.Fatalf("evidence target/footer missing:\n%s", view)
			}
			var model tea.Model
			var command tea.Cmd
			if keyName == "enter" {
				model, command = m.Update(key(tea.KeyEnter))
			} else {
				model, command = m.Update(key('e'))
			}
			current := model.(Model)
			frame := current.current()
			if command != nil || frame.mode != routeFields || frame.detail.Subtitle != "Relationship evidence · target navigation unavailable" ||
				len(frame.detail.Fields) == 0 || frame.detail.Fields[0].Value != relation.TargetRef {
				t.Fatalf("frame=%+v command=%v", frame, command != nil)
			}
		})
	}
}

func TestUnclassifiedRoute53AliasPreservesDNSReference(t *testing.T) {
	awsContext := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	recordKey, _ := NewGlobalResourceKey(awsContext, "resource-record-set", "api-record")
	resource := ProjectResourceFields(recordKey, map[string]any{
		"name":  "api.example.com.",
		"alias": map[string]any{"dns_name": "api.execute-api.us-east-1.amazonaws.com."},
		"alias_relation": map[string]any{
			"relation_type": "alias-to", "direction": "outgoing", "condition": "A alias",
			"kind": "api-exact", "reason": "alias-target-returned-by-api",
		},
	})
	if len(resource.Relations) != 1 || resource.Relations[0].Target != "" || resource.Relations[0].TargetRef != "api.execute-api.us-east-1.amazonaws.com." {
		t.Fatalf("relations=%+v", resource.Relations)
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
	model, _ = model.Update(key(tea.KeyEnter))
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
