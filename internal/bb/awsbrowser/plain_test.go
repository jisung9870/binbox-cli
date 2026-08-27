package awsbrowser

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

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
	for _, want := range []string{"AWS Browser · READ ONLY", "1  EC2 Instances", "2  Route 53", "3  IAM Roles", "4  Cross-profile search", "open <n>|back|refresh|quit"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("plain missing %q:\n%s", want, out.String())
		}
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
		In: strings.NewReader("open 4\nsearch role all reader\nrefresh\nquit\n"), Err: &out,
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
	err := (Plain{Dispatcher: dispatcher}).Run(context.Background(), Terminal{In: strings.NewReader("open 4\nsearch domain all api.example.com\nquit\n"), Err: &out}, Config{Profile: "dev"})
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
	err := (Plain{Dispatcher: dispatcher}).Run(context.Background(), Terminal{In: strings.NewReader("open 4\nback\nquit\n"), Err: &out}, Config{})
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
	err := (Plain{Dispatcher: dispatcher}).Run(context.Background(), Terminal{In: strings.NewReader("open 4\nsearch domain all api.example.com\nopen 2\nopen 1\nquit\n"), Err: &out}, Config{Profile: "dev", Region: "us-east-1"})
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
	err := (Plain{Dispatcher: dispatcher}).Run(context.Background(), Terminal{In: strings.NewReader("open 4\nsearch domain all api.example.com\nopen 1\nback\nquit\n"), Err: &out}, Config{})
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
