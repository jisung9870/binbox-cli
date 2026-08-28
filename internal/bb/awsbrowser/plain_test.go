package awsbrowser

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type plainDispatchStub struct {
	stream IntentStream
	err    error
}

func (dispatcher plainDispatchStub) Dispatch(context.Context, Intent) (IntentStream, error) {
	return dispatcher.stream, dispatcher.err
}

func TestPlainStartupAndQuitAreZeroCall(t *testing.T) {
	dispatcher := new(recordingDispatcher)
	var out bytes.Buffer
	err := (Plain{Dispatcher: dispatcher}).Run(context.Background(), Terminal{In: strings.NewReader("quit\n"), Err: &out}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.intents) != 0 {
		t.Fatalf("startup intents=%+v", dispatcher.intents)
	}
	for _, want := range []string{"AWS Browser · READ ONLY", "Account unresolved", "1  EC2 Instances", "2  Route 53", "3  IAM Roles", "4  VPC & Networking", "5  Cross-profile search", "open <n>|context|back|refresh|quit"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("plain missing %q:\n%s", want, out.String())
		}
	}
}

func TestPlainSelectsVerifiedContextBeforeOpeningResource(t *testing.T) {
	verified := testStoreContext(t, "prod", "999999999999", "ap-southeast-1", 2)
	stream := newTestIntentStream()
	stream.updates <- IntentUpdate{Query: QueryUpdate{Snapshot: QuerySnapshot{State: LoadEmpty, FetchedAt: time.Now()}}, Done: true}
	dispatcher := &contextRecordingDispatcher{
		recordingDispatcher: recordingDispatcher{streams: []*testIntentStream{stream}},
		choices: []ContextChoice{
			{Profile: "dev", Region: "us-east-1"},
			{Profile: "prod", Region: "ap-northeast-2"},
		},
		resolution: ContextResolution{Context: &verified},
	}
	var out bytes.Buffer
	err := (Plain{Dispatcher: dispatcher}).Run(
		context.Background(),
		Terminal{In: strings.NewReader("context\nselect 2 ap-southeast-1\nopen 1\nquit\n"), Err: &out},
		Config{Profile: "dev", Region: "us-east-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Select AWS context", "Verified account 999999999999", "Profile prod", "Account 999999999999"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("plain context missing %q:\n%s", want, out.String())
		}
	}
	if dispatcher.resolvedProfile != "prod" || dispatcher.resolvedRegion != "ap-southeast-1" ||
		len(dispatcher.intents) != 1 || dispatcher.intents[0].Profile != "prod" || dispatcher.intents[0].Region != "ap-southeast-1" {
		t.Fatalf("resolved=%s/%s intents=%+v", dispatcher.resolvedProfile, dispatcher.resolvedRegion, dispatcher.intents)
	}
}

func TestPlainSelectsConfiguredAllRegionScope(t *testing.T) {
	verified := testStoreContext(t, "lg-udg-ops", "123456789012", "ap-northeast-2", 1)
	stream := newTestIntentStream()
	stream.updates <- IntentUpdate{Query: QueryUpdate{Snapshot: QuerySnapshot{State: LoadEmpty, FetchedAt: time.Now()}}, Done: true}
	dispatcher := &contextRecordingDispatcher{
		recordingDispatcher: recordingDispatcher{streams: []*testIntentStream{stream}},
		choices: []ContextChoice{{
			Profile: "lg-udg-ops", Region: "ap-northeast-2", Group: "UDG",
			Regions: []string{"ap-northeast-2", "ap-southeast-1", "us-east-1", "eu-central-1"},
		}},
		resolution: ContextResolution{Context: &verified},
	}
	var out bytes.Buffer
	err := (Plain{Dispatcher: dispatcher}).Run(context.Background(), Terminal{
		In: strings.NewReader("context\nselect 1 all\nopen 1\nquit\n"), Err: &out,
	}, Config{Profile: "dev", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	wantRegions := "ap-northeast-2,ap-southeast-1,us-east-1,eu-central-1"
	if !strings.Contains(out.String(), "UDG · 4 regions") || !strings.Contains(out.String(), "scope all") ||
		len(dispatcher.intents) != 1 || dispatcher.intents[0].Regions != wantRegions {
		t.Fatalf("output=%q intents=%+v", out.String(), dispatcher.intents)
	}
}

func TestPlainStartsWithContextSelectionWhenProfileIsOmitted(t *testing.T) {
	verified := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	dispatcher := &contextRecordingDispatcher{
		choices:    []ContextChoice{{Profile: "dev", Region: "us-east-1"}},
		resolution: ContextResolution{Context: &verified},
	}
	var out strings.Builder
	err := (Plain{Dispatcher: dispatcher}).Run(context.Background(), Terminal{
		In: strings.NewReader("select 1\nquit\n"), Err: &out,
	}, Config{})
	if err != nil || dispatcher.listCalls != 1 || dispatcher.resolvedProfile != "dev" ||
		!strings.Contains(out.String(), "Select AWS context") || !strings.Contains(out.String(), "Profile dev") {
		t.Fatalf("err=%v calls=%d profile=%q output=%q", err, dispatcher.listCalls, dispatcher.resolvedProfile, out.String())
	}
}

func TestPlainEOFReturnsBeforeOutput(t *testing.T) {
	var out bytes.Buffer
	err := (Plain{}).Run(context.Background(), Terminal{In: strings.NewReader(""), Err: &out}, Config{})
	if !errors.Is(err, ErrNoInput) || out.Len() != 0 {
		t.Fatalf("err=%v out=%q", err, out.String())
	}
}

func TestPlainConsumesProjectionAndNavigatesDetail(t *testing.T) {
	stream := newTestIntentStream()
	stream.updates <- IntentUpdate{
		Query:      QueryUpdate{Snapshot: QuerySnapshot{State: LoadReady, FetchedAt: time.Now()}},
		Projection: IntentProjection{Resources: []ResourceProjection{resourceProjection("web-api", "running")}},
		Done:       true,
	}
	dispatcher := &recordingDispatcher{streams: []*testIntentStream{stream}}
	var out bytes.Buffer
	err := (Plain{Dispatcher: dispatcher}).Run(context.Background(), Terminal{In: strings.NewReader("open 1\nopen 1\nback\nback\nquit\n"), Err: &out}, Config{Profile: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Ready · 1 resources", "1  web-api · running", "Private IP: 10.0.1.24", "relations:", "sg-web", "instance security group id"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("plain projection missing %q:\n%s", want, out.String())
		}
	}
	if len(dispatcher.intents) != 1 || dispatcher.intents[0].Target != "ec2-instances" || stream.cancels != 1 {
		t.Fatalf("intents=%+v cancels=%d", dispatcher.intents, stream.cancels)
	}
}

func TestPlainRefreshKeepsCachedProjectionUntilReady(t *testing.T) {
	initial, refresh := newTestIntentStream(), newTestIntentStream()
	initial.updates <- IntentUpdate{Query: QueryUpdate{Snapshot: QuerySnapshot{State: LoadReady, FetchedAt: time.Now()}}, Projection: IntentProjection{Resources: []ResourceProjection{resourceProjection("old", "running")}}, Done: true}
	refresh.updates <- IntentUpdate{Query: QueryUpdate{Snapshot: QuerySnapshot{State: LoadRefreshing}}, Projection: IntentProjection{Resources: []ResourceProjection{resourceProjection("staged", "pending")}}}
	refresh.updates <- IntentUpdate{Query: QueryUpdate{Snapshot: QuerySnapshot{State: LoadReady, FetchedAt: time.Now()}}, Projection: IntentProjection{Resources: []ResourceProjection{resourceProjection("new", "running")}}, Done: true}
	dispatcher := &recordingDispatcher{streams: []*testIntentStream{initial, refresh}}
	var out bytes.Buffer
	err := (Plain{Dispatcher: dispatcher}).Run(context.Background(), Terminal{In: strings.NewReader("open 1\nrefresh\nquit\n"), Err: &out}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	output := out.String()
	refreshing := strings.Index(output, "Showing cached 1 · refreshing")
	if refreshing < 0 {
		t.Fatalf("plain refresh status missing:\n%s", output)
	}
	refreshOutput := output[refreshing:]
	ready := strings.Index(refreshOutput, "Ready · 1 resources")
	if ready < 0 || !strings.Contains(refreshOutput[:ready], "1  old") || strings.Contains(refreshOutput[:ready], "staged") || !strings.Contains(refreshOutput[ready:], "1  new") {
		t.Fatalf("plain refresh was not atomic:\n%s", output)
	}
}

func TestPlainNonSearchRefreshPinsResolvedContext(t *testing.T) {
	initial, refresh := newTestIntentStream(), newTestIntentStream()
	resolved := testStoreContext(t, "dev", "123456789012", "us-west-2", 1)
	initial.updates <- IntentUpdate{
		Context:    &resolved,
		Query:      QueryUpdate{Snapshot: QuerySnapshot{State: LoadReady, FetchedAt: time.Now()}},
		Projection: IntentProjection{Resources: []ResourceProjection{resourceProjection("old", "running")}},
		Done:       true,
	}
	refresh.updates <- IntentUpdate{Query: QueryUpdate{Snapshot: QuerySnapshot{State: LoadReady, FetchedAt: time.Now()}}, Done: true}
	dispatcher := &recordingDispatcher{streams: []*testIntentStream{initial, refresh}}
	var out bytes.Buffer
	err := (Plain{Dispatcher: dispatcher}).Run(context.Background(), Terminal{In: strings.NewReader("open 1\nrefresh\nquit\n"), Err: &out}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.intents) != 2 || dispatcher.intents[0].Profile != "" || dispatcher.intents[0].Region != "" ||
		dispatcher.intents[1].Kind != IntentRefresh || dispatcher.intents[1].Profile != "dev" || dispatcher.intents[1].Region != "us-west-2" {
		t.Fatalf("plain non-search refresh did not pin resolved context: %+v", dispatcher.intents)
	}
}

func TestPlainSearchRefreshRepeatsValidatedSearchIntentAndContext(t *testing.T) {
	initial, refresh := newTestIntentStream(), newTestIntentStream()
	audit := testStoreContext(t, "audit", "999999999999", "us-west-2", 2)
	initial.updates <- IntentUpdate{
		Context:    &audit,
		Query:      QueryUpdate{Snapshot: QuerySnapshot{State: LoadReady, FetchedAt: time.Now()}},
		Projection: IntentProjection{Resources: []ResourceProjection{resourceProjection("old-role", "matched")}},
		Coverage:   &SearchCoverage{DiscoveryStatus: "cached-discovery", Profiles: []SearchProfileCoverage{{Profile: "audit", Status: "matched", Matches: 1}}},
		Done:       true,
	}
	refresh.updates <- IntentUpdate{
		Query:      QueryUpdate{Snapshot: QuerySnapshot{State: LoadRefreshing}},
		Projection: IntentProjection{Resources: []ResourceProjection{resourceProjection("new-role", "matched")}},
		Coverage:   &SearchCoverage{DiscoveryStatus: "replacement-discovery", Profiles: []SearchProfileCoverage{{Profile: "dev", Status: "matched", Matches: 1}}},
	}
	refresh.updates <- IntentUpdate{
		Query:      QueryUpdate{Snapshot: QuerySnapshot{State: LoadReady, FetchedAt: time.Now()}},
		Projection: IntentProjection{Resources: []ResourceProjection{resourceProjection("new-role", "matched")}},
		Done:       true,
	}
	dispatcher := &recordingDispatcher{streams: []*testIntentStream{initial, refresh}}
	var out bytes.Buffer
	err := (Plain{Dispatcher: dispatcher}).Run(context.Background(), Terminal{
		In: strings.NewReader("open 5\nsearch role all reader\nrefresh\nquit\n"), Err: &out,
	}, Config{Profile: "dev", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	want := Intent{Kind: IntentSearch, Target: "cross-profile-search", SearchKind: "role", Query: "reader", Scope: "all", Profile: "dev", Region: "us-east-1"}
	if len(dispatcher.intents) != 2 || dispatcher.intents[0] != want || dispatcher.intents[1] != want {
		t.Fatalf("plain search refresh intents=%+v want repeated %+v", dispatcher.intents, want)
	}
	output := out.String()
	refreshing := strings.LastIndex(output, "Showing cached 1 · refreshing")
	readyOffset := -1
	if refreshing >= 0 {
		readyOffset = strings.Index(output[refreshing:], "Ready · 1 resources")
	}
	if refreshing < 0 || readyOffset < 0 {
		t.Fatalf("plain search refresh output:\n%s", output)
	}
	ready := refreshing + readyOffset
	refreshOutput := output[refreshing:ready]
	readyOutput := output[ready:]
	if !strings.Contains(refreshOutput, "old-role") || strings.Contains(refreshOutput, "new-role") ||
		!strings.Contains(refreshOutput, "cached-discovery") || strings.Contains(refreshOutput, "replacement-discovery") ||
		!strings.Contains(readyOutput, "new-role") || strings.Contains(readyOutput, "cached-discovery") ||
		!strings.Contains(readyOutput, "replacement-discovery") || strings.Contains(output, "unsupported") {
		t.Fatalf("plain search refresh output:\n%s", output)
	}
}

func TestPlainSearchUsesIntentStreamAndSanitizesFailure(t *testing.T) {
	stream := newTestIntentStream()
	stream.updates <- IntentUpdate{Query: QueryUpdate{Snapshot: QuerySnapshot{State: LoadForbidden}, Failure: &ProviderFailure{State: LoadForbidden, Service: "iam\x1b[31m", Operation: "GetRole"}}, Done: true}
	dispatcher := &recordingDispatcher{streams: []*testIntentStream{stream}}
	var out bytes.Buffer
	err := (Plain{Dispatcher: dispatcher}).Run(context.Background(), Terminal{In: strings.NewReader("open 5\nsearch domain all api.example.com\nquit\n"), Err: &out}, Config{Profile: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.intents) != 1 || dispatcher.intents[0].Kind != IntentSearch || dispatcher.intents[0].Query != "api.example.com" || strings.Contains(out.String(), "\x1b") || !strings.Contains(out.String(), "access denied") {
		t.Fatalf("intents=%+v output=%q", dispatcher.intents, out.String())
	}
}

func TestPlainSearchOpenAndEditingAreZeroCallUntilSubmit(t *testing.T) {
	dispatcher := new(recordingDispatcher)
	var out bytes.Buffer
	err := (Plain{Dispatcher: dispatcher}).Run(context.Background(), Terminal{In: strings.NewReader("open 5\nback\nquit\n"), Err: &out}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.intents) != 0 || !strings.Contains(out.String(), "no AWS request until submit") {
		t.Fatalf("local plain search dispatched: intents=%+v output=%s", dispatcher.intents, out.String())
	}
}

func TestPlainSearchRelationUsesSelectedResourceContext(t *testing.T) {
	dev := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	audit := testStoreContext(t, "audit", "999999999999", "us-west-2", 2)
	search, relation := newTestIntentStream(), newTestIntentStream()
	search.updates <- IntentUpdate{
		Context: &dev,
		Query:   QueryUpdate{Snapshot: QuerySnapshot{State: LoadReady, FetchedAt: time.Now()}},
		Projection: IntentProjection{Resources: []ResourceProjection{
			{Target: "resource-record-set:first", Title: "first.example.com.", Context: &dev},
			{Target: "resource-record-set:api", Title: "api.example.com.", Context: &audit, Relations: []ProjectionRelation{{Label: "Hosted zone", Target: "hosted-zone:Z9"}}},
		}},
		Done: true,
	}
	relation.updates <- IntentUpdate{Query: QueryUpdate{Snapshot: QuerySnapshot{State: LoadEmpty, FetchedAt: time.Now()}}, Done: true}
	dispatcher := &recordingDispatcher{streams: []*testIntentStream{search, relation}}
	var out bytes.Buffer
	err := (Plain{Dispatcher: dispatcher}).Run(context.Background(), Terminal{In: strings.NewReader("open 5\nsearch domain all api.example.com\nopen 2\nopen 1\nquit\n"), Err: &out}, Config{Profile: "dev", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.intents) != 2 {
		t.Fatalf("intents=%+v output=%s", dispatcher.intents, out.String())
	}
	got := dispatcher.intents[1]
	if got.Target != "hosted-zone:Z9" || got.Profile != "audit" || got.Region != "us-west-2" {
		t.Fatalf("relation intent=%+v", got)
	}
}

func TestPlainSearchRendersCoverageAndSelectedResourceProvenance(t *testing.T) {
	audit := testStoreContext(t, "audit", "123456789012", "us-west-2", 1)
	stream := newTestIntentStream()
	stream.updates <- IntentUpdate{
		Query: QueryUpdate{Snapshot: QuerySnapshot{State: LoadReady, FetchedAt: time.Now()}},
		Projection: IntentProjection{Resources: []ResourceProjection{{
			Target: "resource-record-set:api", Title: "api.example.com.", Context: &audit, Current: true,
			AvailableViaProfiles: []string{"audit", "read-only"},
		}}},
		Coverage: &SearchCoverage{DiscoveryStatus: "timed_out", Partial: true, Profiles: []SearchProfileCoverage{
			{Profile: "audit", AccountID: "123456789012", Status: "matched", Current: true, Matches: 1},
			{Profile: "locked", Status: "forbidden"},
		}},
		Done: true,
	}
	dispatcher := &recordingDispatcher{streams: []*testIntentStream{stream}}
	var out bytes.Buffer
	err := (Plain{Dispatcher: dispatcher}).Run(context.Background(), Terminal{In: strings.NewReader("open 5\nsearch domain all api.example.com\nopen 1\nback\nquit\n"), Err: &out}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	output := out.String()
	if strings.Count(output, "Partial coverage") < 2 || strings.Count(output, "Profile discovery · timed_out") < 2 {
		t.Fatalf("plain search did not preserve coverage across redraw:\n%s", output)
	}
	for _, want := range []string{
		"Partial coverage", "Profile discovery · timed_out", "Coverage · current audit · 123456789012 · matched · matches 1",
		"Coverage · profile locked · unresolved · forbidden · matches 0", "Provenance", "Account 123456789012",
		"Principal arn:aws:sts::123456789012:assumed-role/ReadOnly/session", "Profile audit · current yes", "Region us-west-2", "Available via audit, read-only",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("plain search missing %q:\n%s", want, output)
		}
	}
}

func TestPlainLoadingCanBeCancelledByInput(t *testing.T) {
	stream := newTestIntentStream()
	dispatcher := &recordingDispatcher{streams: []*testIntentStream{stream}}
	var out bytes.Buffer
	err := (Plain{Dispatcher: dispatcher}).Run(context.Background(), Terminal{In: strings.NewReader("open 1\nback\nquit\n"), Err: &out}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if stream.cancels != 1 || len(dispatcher.intents) != 1 {
		t.Fatalf("plain cancellation: cancels=%d intents=%+v", stream.cancels, dispatcher.intents)
	}
}

func TestPlainCanCancelBlockingDispatchBeforeStreamAcquisition(t *testing.T) {
	dispatcher := &blockingDispatcher{started: make(chan struct{}), done: make(chan struct{})}
	var out bytes.Buffer
	err := (Plain{Dispatcher: dispatcher}).Run(context.Background(), Terminal{In: strings.NewReader("open 1\nback\nquit\n"), Err: &out}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-dispatcher.done:
	case <-time.After(time.Second):
		t.Fatal("plain Back did not cancel blocking Dispatch")
	}
}

type latePlainStream struct {
	updates   chan IntentUpdate
	cancelled chan struct{}
	cancels   atomic.Int32
}

func (stream *latePlainStream) Updates() <-chan IntentUpdate { return stream.updates }
func (stream *latePlainStream) Cancel() {
	if stream.cancels.Add(1) == 1 {
		close(stream.cancelled)
	}
}

type latePlainDispatcher struct {
	started chan struct{}
	stream  IntentStream
}

func (dispatcher *latePlainDispatcher) Dispatch(ctx context.Context, _ Intent) (IntentStream, error) {
	close(dispatcher.started)
	<-ctx.Done()
	return dispatcher.stream, nil
}

func TestPlainPreAcquisitionExitCancelsLateStream(t *testing.T) {
	for _, test := range []struct {
		name    string
		input   string
		wantErr error
	}{
		{name: "back", input: "back\nquit\n"},
		{name: "quit", input: "quit\n"},
		{name: "input closure", wantErr: ErrNoInput},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := &latePlainStream{updates: make(chan IntentUpdate), cancelled: make(chan struct{})}
			dispatcher := &latePlainDispatcher{started: make(chan struct{}), stream: stream}
			var out bytes.Buffer
			done := make(chan error, 1)
			go func() {
				done <- (Plain{Dispatcher: dispatcher}).Run(context.Background(), Terminal{
					In: strings.NewReader("open 1\n" + test.input), Err: &out,
				}, Config{})
			}()

			select {
			case <-dispatcher.started:
			case <-time.After(time.Second):
				t.Fatal("Dispatch did not start")
			}
			select {
			case err := <-done:
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("err=%v want %v", err, test.wantErr)
				}
			case <-time.After(time.Second):
				t.Fatal("pre-acquisition exit blocked")
			}
			select {
			case <-stream.cancelled:
			case <-time.After(time.Second):
				t.Fatal("late acquired stream was not cancelled")
			}
			if got := stream.cancels.Load(); got != 1 {
				t.Fatalf("stream cancellations=%d", got)
			}
		})
	}
}

func TestPlainPreAcquisitionContextCancellationCancelsLateStream(t *testing.T) {
	stream := &latePlainStream{updates: make(chan IntentUpdate), cancelled: make(chan struct{})}
	dispatcher := &latePlainDispatcher{started: make(chan struct{}), stream: stream}
	ctx, cancel := context.WithCancel(context.Background())
	inputs := &plainInputSource{ch: make(chan plainInput)}
	done := make(chan error, 1)
	go func() {
		done <- (Plain{Dispatcher: dispatcher}).load(ctx, io.Discard, Config{}, &plainFrame{target: "ec2-instances"}, Intent{
			Kind: IntentOpen, Target: "ec2-instances",
		}, inputs, false)
	}()
	<-dispatcher.started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("context cancellation blocked")
	}
	select {
	case <-stream.cancelled:
	case <-time.After(time.Second):
		t.Fatal("late acquired stream was not cancelled")
	}
	if got := stream.cancels.Load(); got != 1 {
		t.Fatalf("stream cancellations=%d", got)
	}
}

func TestPlainPrematureStreamClosureIsFailure(t *testing.T) {
	stream := newTestIntentStream()
	close(stream.updates)
	dispatcher := &recordingDispatcher{streams: []*testIntentStream{stream}}
	var out bytes.Buffer
	err := (Plain{Dispatcher: dispatcher}).Run(context.Background(), Terminal{In: strings.NewReader("open 1\nquit\n"), Err: &out}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "query failed") || !strings.Contains(out.String(), "incomplete stream") {
		t.Fatalf("plain premature closure was silent: %s", out.String())
	}
}

func TestPlainRefreshEarlyTerminalStatesPreserveCachedFrame(t *testing.T) {
	stagedContext := testStoreContext(t, "staged", "999999999999", "us-west-2", 2)
	stagedUpdate := IntentUpdate{
		Context:    &stagedContext,
		Query:      QueryUpdate{Snapshot: QuerySnapshot{State: LoadRefreshing}},
		Projection: IntentProjection{Resources: []ResourceProjection{resourceProjection("staged", "pending")}},
		Coverage:   &SearchCoverage{DiscoveryStatus: "staged-discovery", Profiles: []SearchProfileCoverage{{Profile: "staged", Status: "matched", Matches: 1}}},
	}
	tests := []struct {
		name       string
		dispatcher IntentDispatcher
		want       []string
	}{
		{
			name:       "dispatch error",
			dispatcher: plainDispatchStub{err: errors.New("boom\x1b[31m")},
			want:       []string{"refresh failed", "cross-profile-search: boom"},
		},
		{
			name:       "nil stream",
			dispatcher: plainDispatchStub{},
			want:       []string{"refresh failed", "no update stream"},
		},
		{
			name: "terminal cancellation",
			dispatcher: func() IntentDispatcher {
				stream := newTestIntentStream()
				stream.updates <- stagedUpdate
				stream.updates <- IntentUpdate{
					Query:      QueryUpdate{Snapshot: QuerySnapshot{State: LoadCancelled}},
					Projection: stagedUpdate.Projection,
					Coverage:   stagedUpdate.Coverage,
					Done:       true,
				}
				return plainDispatchStub{stream: stream}
			}(),
			want: []string{"refresh cancelled"},
		},
		{
			name: "terminal error",
			dispatcher: func() IntentDispatcher {
				stream := newTestIntentStream()
				stream.updates <- stagedUpdate
				update := stagedUpdate
				update.Query = QueryUpdate{Snapshot: QuerySnapshot{State: LoadThrottled}, Failure: &ProviderFailure{State: LoadThrottled, Service: "ec2\x1b[31m", Operation: "DescribeInstances"}}
				update.Done = true
				stream.updates <- update
				return plainDispatchStub{stream: stream}
			}(),
			want: []string{"refresh throttled"},
		},
		{
			name: "terminal stale",
			dispatcher: func() IntentDispatcher {
				stream := newTestIntentStream()
				stream.updates <- stagedUpdate
				update := stagedUpdate
				update.Query.Snapshot.State = LoadStale
				update.Done = true
				stream.updates <- update
				return plainDispatchStub{stream: stream}
			}(),
			want: []string{"Stale · showing cached 1"},
		},
		{
			name: "premature stream close",
			dispatcher: func() IntentDispatcher {
				stream := newTestIntentStream()
				stream.updates <- stagedUpdate
				close(stream.updates)
				return plainDispatchStub{stream: stream}
			}(),
			want: []string{"refresh query failed", "incomplete stream"},
		},
		{
			name: "nonterminal done",
			dispatcher: func() IntentDispatcher {
				stream := newTestIntentStream()
				update := stagedUpdate
				update.Done = true
				stream.updates <- update
				return plainDispatchStub{stream: stream}
			}(),
			want: []string{"refresh query failed", "incomplete stream"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frame := cachedPlainRefreshFrame(t)
			inputs := &plainInputSource{ch: make(chan plainInput)}
			var out bytes.Buffer
			err := (Plain{Dispatcher: test.dispatcher}).load(context.Background(), &out, Config{}, &frame, frame.intent, inputs, true)
			if err != nil {
				t.Fatal(err)
			}
			assertCachedPlainRefreshTerminal(t, frame, test.want...)
		})
	}
}

func TestPlainRefreshInputCancellationClearsStagedStateAndPersists(t *testing.T) {
	frame := cachedPlainRefreshFrame(t)
	stagedCoverage := frame.staged.coverage
	stream := &testIntentStream{updates: make(chan IntentUpdate)}
	inputs := make(chan plainInput, 1)
	go func() {
		stream.updates <- IntentUpdate{
			Query:      QueryUpdate{Snapshot: QuerySnapshot{State: LoadRefreshing}},
			Projection: IntentProjection{Resources: []ResourceProjection{resourceProjection("staged", "pending")}},
			Coverage:   stagedCoverage,
		}
		inputs <- plainInput{line: "cancel\n"}
	}()
	frame.staged.clear()
	var out bytes.Buffer
	err := (Plain{Dispatcher: plainDispatchStub{stream: stream}}).load(
		context.Background(), &out, Config{}, &frame, frame.intent, &plainInputSource{ch: inputs}, true,
	)
	if !errors.Is(err, errPlainBack) {
		t.Fatalf("err=%v", err)
	}
	assertCachedPlainRefreshTerminal(t, frame, "refresh cancelled")
}

func cachedPlainRefreshFrame(t *testing.T) plainFrame {
	t.Helper()
	cachedContext := testStoreContext(t, "cached", "123456789012", "us-east-1", 1)
	stagedContext := testStoreContext(t, "staged", "999999999999", "us-west-2", 2)
	stagedProjection := IntentProjection{Resources: []ResourceProjection{resourceProjection("staged", "pending")}}
	return plainFrame{
		target:     "cross-profile-search",
		label:      "Search results · reader",
		intent:     Intent{Kind: IntentSearch, Target: "cross-profile-search", SearchKind: "role", Query: "reader", Scope: "all"},
		projection: IntentProjection{Resources: []ResourceProjection{resourceProjection("cached", "running")}},
		context:    &cachedContext,
		coverage:   &SearchCoverage{DiscoveryStatus: "cached-discovery", Profiles: []SearchProfileCoverage{{Profile: "cached", Status: "matched", Matches: 1}}},
		staged: refreshStage{
			context:    &stagedContext,
			projection: &stagedProjection,
			coverage: &SearchCoverage{
				DiscoveryStatus: "staged-discovery",
				Profiles:        []SearchProfileCoverage{{Profile: "staged", Status: "matched", Matches: 1}},
			},
		},
		status: "Showing cached 1 · refreshing… · Esc cancel",
	}
}

func assertCachedPlainRefreshTerminal(t *testing.T, frame plainFrame, statuses ...string) {
	t.Helper()
	if len(frame.projection.Resources) != 1 || frame.projection.Resources[0].Title != "cached" {
		t.Fatalf("cached projection replaced: %+v", frame.projection.Resources)
	}
	if frame.coverage == nil || frame.coverage.DiscoveryStatus != "cached-discovery" {
		t.Fatalf("cached coverage replaced: %+v", frame.coverage)
	}
	if frame.context == nil || frame.context.Profile != "cached" {
		t.Fatalf("cached context replaced: %+v", frame.context)
	}
	if frame.staged != (refreshStage{}) {
		t.Fatalf("staged refresh state retained: %+v", frame.staged)
	}
	if strings.Contains(frame.status, "refreshing") || strings.Contains(frame.status, "\x1b") {
		t.Fatalf("unsafe or nonterminal status persisted: %q", frame.status)
	}
	if !strings.Contains(strings.ToLower(frame.status), "showing cached 1") {
		t.Fatalf("status %q does not describe cached data", frame.status)
	}
	for _, status := range statuses {
		if !strings.Contains(frame.status, status) {
			t.Fatalf("status %q missing %q", frame.status, status)
		}
	}
	var redraw bytes.Buffer
	if err := writePlainFrame(&redraw, frame); err != nil {
		t.Fatal(err)
	}
	output := redraw.String()
	if !strings.Contains(output, frame.status) || !strings.Contains(output, "cached-discovery") || !strings.Contains(output, "1  cached") ||
		strings.Contains(output, "staged-discovery") || strings.Contains(output, "1  staged") {
		t.Fatalf("terminal state did not persist on redraw:\n%s", output)
	}
}

func TestPlainRefreshPromotesContextCoverageAndProjectionAtomically(t *testing.T) {
	frame := cachedPlainRefreshFrame(t)
	frame.staged.clear()
	replacementContext := testStoreContext(t, "replacement", "999999999999", "us-west-2", 2)
	replacementCoverage := &SearchCoverage{DiscoveryStatus: "replacement-discovery", Profiles: []SearchProfileCoverage{{Profile: "replacement", Status: "matched", Matches: 1}}}
	replacementProjection := IntentProjection{Resources: []ResourceProjection{resourceProjection("replacement", "running")}}
	var out bytes.Buffer
	plain := Plain{}
	if err := plain.applyPlainUpdate(&out, &frame, true, IntentUpdate{
		Context: &replacementContext, Query: QueryUpdate{Snapshot: QuerySnapshot{State: LoadRefreshing}},
		Coverage: replacementCoverage, Projection: replacementProjection,
	}); err != nil {
		t.Fatal(err)
	}
	if frame.context.Profile != "cached" || frame.coverage.DiscoveryStatus != "cached-discovery" || frame.projection.Resources[0].Title != "cached" {
		t.Fatalf("staged refresh leaked before success: %+v", frame)
	}
	if err := plain.applyPlainUpdate(&out, &frame, true, IntentUpdate{Query: QueryUpdate{Snapshot: QuerySnapshot{State: LoadReady, FetchedAt: time.Now()}}, Done: true}); err != nil {
		t.Fatal(err)
	}
	if frame.context.Profile != "replacement" || frame.coverage.DiscoveryStatus != "replacement-discovery" || frame.projection.Resources[0].Title != "replacement" || frame.staged != (refreshStage{}) {
		t.Fatalf("successful refresh was not atomically promoted: %+v", frame)
	}
}
