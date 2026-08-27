package providers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/aws/smithy-go"
	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
)

type route53Fake struct {
	listHostedZones       func(context.Context, *route53.ListHostedZonesInput) (*route53.ListHostedZonesOutput, error)
	listHostedZonesByName func(context.Context, *route53.ListHostedZonesByNameInput) (*route53.ListHostedZonesByNameOutput, error)
	listRecordSets        func(context.Context, *route53.ListResourceRecordSetsInput) (*route53.ListResourceRecordSetsOutput, error)
}

var _ func(awsbrowser.Route53API, func() time.Time) (awsbrowser.QueryExecutor, error) = NewRoute53

func TestNewRoute53RequiresClientAndClock(t *testing.T) {
	if _, err := NewRoute53(nil, time.Now); err == nil {
		t.Fatal("nil client accepted")
	}
	var typedNil *route53Fake
	if _, err := NewRoute53(typedNil, time.Now); err == nil {
		t.Fatal("typed-nil client accepted")
	}
	if _, err := NewRoute53(&route53Fake{}, nil); err == nil {
		t.Fatal("nil clock accepted")
	}
}

func (fake *route53Fake) ListHostedZones(ctx context.Context, input *route53.ListHostedZonesInput) (*route53.ListHostedZonesOutput, error) {
	if fake.listHostedZones == nil {
		return nil, fmt.Errorf("unexpected ListHostedZones")
	}
	return fake.listHostedZones(ctx, input)
}

func (fake *route53Fake) ListHostedZonesByName(ctx context.Context, input *route53.ListHostedZonesByNameInput) (*route53.ListHostedZonesByNameOutput, error) {
	if fake.listHostedZonesByName == nil {
		return nil, fmt.Errorf("unexpected ListHostedZonesByName")
	}
	return fake.listHostedZonesByName(ctx, input)
}

func (fake *route53Fake) ListResourceRecordSets(ctx context.Context, input *route53.ListResourceRecordSetsInput) (*route53.ListResourceRecordSetsOutput, error) {
	if fake.listRecordSets == nil {
		return nil, fmt.Errorf("unexpected ListResourceRecordSets")
	}
	return fake.listRecordSets(ctx, input)
}

type collectingSink struct {
	mu        sync.Mutex
	pages     []awsbrowser.QueryPage
	completed int
}

func (sink *collectingSink) Page(page awsbrowser.QueryPage) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.pages = append(sink.pages, page)
	return nil
}

func (sink *collectingSink) Complete(time.Time) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.completed++
	return nil
}

func TestRoute53ListHostedZonesStreamsMarkerPages(t *testing.T) {
	var calls int
	fake := &route53Fake{listHostedZones: func(_ context.Context, input *route53.ListHostedZonesInput) (*route53.ListHostedZonesOutput, error) {
		calls++
		if input.MaxItems == nil || *input.MaxItems != 100 {
			t.Fatalf("max items=%v", input.MaxItems)
		}
		switch calls {
		case 1:
			if input.Marker != nil {
				t.Fatalf("first marker=%q", aws.ToString(input.Marker))
			}
			return &route53.ListHostedZonesOutput{
				HostedZones: []types.HostedZone{hostedZone("/hostedzone/ZPUBLIC", "Example.COM", false)},
				IsTruncated: true, NextMarker: aws.String("next-marker"),
			}, nil
		case 2:
			if aws.ToString(input.Marker) != "next-marker" {
				t.Fatalf("second marker=%q", aws.ToString(input.Marker))
			}
			return &route53.ListHostedZonesOutput{
				HostedZones: []types.HostedZone{hostedZone("ZPRIVATE", "example.com.", true)},
			}, nil
		default:
			t.Fatal("overfetched hosted zones")
			return nil, nil
		}
	}}
	executor := newRoute53ForTest(t, fake)
	sink := &collectingSink{}
	if err := executor.Execute(context.Background(), route53Key(t, awsbrowser.OperationListHostedZones, nil), sink); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(sink.pages) != 2 || sink.completed != 1 {
		t.Fatalf("calls=%d pages=%d completes=%d", calls, len(sink.pages), sink.completed)
	}
	first := sink.pages[0].Resources()[0]
	second := sink.pages[1].Resources()[0]
	if first.Key.Region != awsbrowser.GlobalRegion || first.Key == second.Key {
		t.Fatalf("global/private identity keys first=%+v second=%+v", first.Key, second.Key)
	}
	if got := first.Observation.Fields()["name"]; got != "example.com." {
		t.Fatalf("canonical name=%v", got)
	}
}

func TestRoute53RejectsBrokenHostedZoneCursors(t *testing.T) {
	tests := []struct {
		name string
		next func(int) *string
	}{
		{name: "missing", next: func(int) *string { return nil }},
		{name: "non-advancing", next: func(int) *string { return aws.String("same") }},
		{name: "repeated", next: func(call int) *string {
			if call == 1 {
				return aws.String("a")
			}
			if call == 2 {
				return aws.String("b")
			}
			return aws.String("a")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			call := 0
			fake := &route53Fake{listHostedZones: func(_ context.Context, input *route53.ListHostedZonesInput) (*route53.ListHostedZonesOutput, error) {
				call++
				if test.name == "non-advancing" && call == 1 {
					return &route53.ListHostedZonesOutput{IsTruncated: true, NextMarker: aws.String("same")}, nil
				}
				return &route53.ListHostedZonesOutput{IsTruncated: true, NextMarker: test.next(call)}, nil
			}}
			executor := newRoute53ForTest(t, fake)
			key := route53Key(t, awsbrowser.OperationListHostedZones, nil)
			if test.name == "non-advancing" {
				// The first cursor advances from nil; make the second page repeat it.
				fake.listHostedZones = func(_ context.Context, input *route53.ListHostedZonesInput) (*route53.ListHostedZonesOutput, error) {
					return &route53.ListHostedZonesOutput{IsTruncated: true, NextMarker: aws.String("same")}, nil
				}
			}
			err := executor.Execute(context.Background(), key, &collectingSink{})
			if !errors.Is(err, awsbrowser.ErrQueryDecode) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestRoute53HostedZonesByNameUsesPairedCursorAndStopsAfterExactVariants(t *testing.T) {
	var calls int
	fake := &route53Fake{listHostedZonesByName: func(_ context.Context, input *route53.ListHostedZonesByNameInput) (*route53.ListHostedZonesByNameOutput, error) {
		calls++
		if input.MaxItems == nil || *input.MaxItems != 100 {
			t.Fatalf("max items=%v", input.MaxItems)
		}
		switch calls {
		case 1:
			if aws.ToString(input.DNSName) != "example.com." || input.HostedZoneId != nil {
				t.Fatalf("first cursor name=%q id=%q", aws.ToString(input.DNSName), aws.ToString(input.HostedZoneId))
			}
			return &route53.ListHostedZonesByNameOutput{
				HostedZones: []types.HostedZone{hostedZone("Z1", "example.com.", false)}, IsTruncated: true,
				NextDNSName: aws.String("example.com."), NextHostedZoneId: aws.String("Z2"),
			}, nil
		case 2:
			if aws.ToString(input.DNSName) != "example.com." || aws.ToString(input.HostedZoneId) != "Z2" {
				t.Fatalf("second cursor name=%q id=%q", aws.ToString(input.DNSName), aws.ToString(input.HostedZoneId))
			}
			return &route53.ListHostedZonesByNameOutput{
				HostedZones: []types.HostedZone{
					hostedZone("Z2", "EXAMPLE.COM", true),
					hostedZone("Z3", "later.example.com.", false),
				},
				IsTruncated: true, NextDNSName: aws.String("later.example.com."), NextHostedZoneId: aws.String("Z3"),
			}, nil
		default:
			t.Fatal("exact hosted-zone search overfetched")
			return nil, nil
		}
	}}
	sink := &collectingSink{}
	err := newRoute53ForTest(t, fake).Execute(context.Background(), route53Key(t, awsbrowser.OperationListHostedZonesByName, map[string]string{"dns-name": "Example.COM"}), sink)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || sink.completed != 1 || resourceCount(sink.pages) != 2 {
		t.Fatalf("calls=%d complete=%d resources=%d", calls, sink.completed, resourceCount(sink.pages))
	}
}

func TestRoute53HostedZonesByNameRejectsIncompleteAndRepeatedPairs(t *testing.T) {
	tests := []struct {
		name string
		next func(int) (*string, *string)
	}{
		{name: "missing dns name", next: func(int) (*string, *string) { return nil, aws.String("Z2") }},
		{name: "missing zone id", next: func(int) (*string, *string) { return aws.String("example.com."), nil }},
		{name: "repeated pair", next: func(call int) (*string, *string) {
			if call == 1 {
				return aws.String("first.example."), aws.String("Z1")
			}
			if call == 2 {
				return aws.String("second.example."), aws.String("Z2")
			}
			return aws.String("first.example."), aws.String("Z1")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			call := 0
			fake := &route53Fake{listHostedZonesByName: func(context.Context, *route53.ListHostedZonesByNameInput) (*route53.ListHostedZonesByNameOutput, error) {
				call++
				name, id := test.next(call)
				return &route53.ListHostedZonesByNameOutput{IsTruncated: true, NextDNSName: name, NextHostedZoneId: id}, nil
			}}
			key := route53Key(t, awsbrowser.OperationListHostedZonesByName, nil)
			err := newRoute53ForTest(t, fake).Execute(context.Background(), key, &collectingSink{})
			if !errors.Is(err, awsbrowser.ErrQueryDecode) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestRoute53ExactRecordSearchUsesFullTupleAndUniqueRoutingKeys(t *testing.T) {
	var calls int
	fake := &route53Fake{listRecordSets: func(_ context.Context, input *route53.ListResourceRecordSetsInput) (*route53.ListResourceRecordSetsOutput, error) {
		calls++
		if aws.ToString(input.HostedZoneId) != "Z1" || input.MaxItems == nil || *input.MaxItems != 300 {
			t.Fatalf("zone/max=%q/%v", aws.ToString(input.HostedZoneId), input.MaxItems)
		}
		switch calls {
		case 1:
			if aws.ToString(input.StartRecordName) != "www.example.com." || input.StartRecordType != types.RRTypeA || input.StartRecordIdentifier != nil {
				t.Fatalf("first tuple=%q/%q/%q", aws.ToString(input.StartRecordName), input.StartRecordType, aws.ToString(input.StartRecordIdentifier))
			}
			return &route53.ListResourceRecordSetsOutput{
				ResourceRecordSets: []types.ResourceRecordSet{weightedRecord("www.example.com", "blue", 10)},
				IsTruncated:        true, NextRecordName: aws.String("www.example.com."), NextRecordType: types.RRTypeA, NextRecordIdentifier: aws.String("green"),
			}, nil
		case 2:
			if aws.ToString(input.StartRecordName) != "www.example.com." || input.StartRecordType != types.RRTypeA || aws.ToString(input.StartRecordIdentifier) != "green" {
				t.Fatalf("second tuple=%q/%q/%q", aws.ToString(input.StartRecordName), input.StartRecordType, aws.ToString(input.StartRecordIdentifier))
			}
			alias := weightedRecord("WWW.EXAMPLE.COM.", "green", 10)
			alias.ResourceRecords = nil
			alias.AliasTarget = &types.AliasTarget{HostedZoneId: aws.String("/hostedzone/ZALIAS"), DNSName: aws.String("target.example.net"), EvaluateTargetHealth: true}
			return &route53.ListResourceRecordSetsOutput{
				ResourceRecordSets: []types.ResourceRecordSet{alias, simpleRecord("zzz.example.com.", types.RRTypeA, "192.0.2.1")},
				IsTruncated:        true, NextRecordName: aws.String("zzz.example.com."), NextRecordType: types.RRTypeA,
			}, nil
		default:
			t.Fatal("exact record search overfetched")
			return nil, nil
		}
	}}
	sink := &collectingSink{}
	key := route53Key(t, awsbrowser.OperationListResourceRecordSets, map[string]string{
		"hosted-zone-id": "/hostedzone/Z1", "record-name": "WWW.EXAMPLE.COM", "record-type": "a",
	})
	if err := newRoute53ForTest(t, fake).Execute(context.Background(), key, sink); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || sink.completed != 1 || resourceCount(sink.pages) != 2 {
		t.Fatalf("calls=%d completes=%d resources=%d", calls, sink.completed, resourceCount(sink.pages))
	}
	resources := append(sink.pages[0].Resources(), sink.pages[1].Resources()...)
	if resources[0].Key == resources[1].Key || resources[0].Key.Region != awsbrowser.GlobalRegion {
		t.Fatalf("record keys=%+v %+v", resources[0].Key, resources[1].Key)
	}
	fields := resources[1].Observation.Fields()
	alias := fields["alias"].(map[string]any)
	if alias["dns_name"] != "target.example.net." || alias["hosted_zone_id"] != "ZALIAS" {
		t.Fatalf("alias fields=%v", alias)
	}
	relation := fields["alias_relation"].(map[string]any)
	if relation["kind"] != string(awsbrowser.RelationAPIExact) || relation["scope"] != awsbrowser.GlobalRegion {
		t.Fatalf("alias relation=%v", relation)
	}
}

func TestRoute53RecordKeyIsStableWhenMutableRoutingValuesChange(t *testing.T) {
	call := 0
	fake := &route53Fake{listRecordSets: func(context.Context, *route53.ListResourceRecordSetsInput) (*route53.ListResourceRecordSetsOutput, error) {
		call++
		weight := int64(10)
		if call == 2 {
			weight = 90
		}
		return &route53.ListResourceRecordSetsOutput{
			ResourceRecordSets: []types.ResourceRecordSet{weightedRecord("www.example.com.", "blue", weight)},
		}, nil
	}}
	executor := newRoute53ForTest(t, fake)
	key := route53Key(t, awsbrowser.OperationListResourceRecordSets, map[string]string{"hosted-zone-id": "Z1"})
	firstSink := &collectingSink{}
	secondSink := &collectingSink{}
	if err := executor.Execute(context.Background(), key, firstSink); err != nil {
		t.Fatal(err)
	}
	if err := executor.Execute(context.Background(), key, secondSink); err != nil {
		t.Fatal(err)
	}
	first := firstSink.pages[0].Resources()[0]
	second := secondSink.pages[0].Resources()[0]
	if first.Key != second.Key {
		t.Fatalf("mutable weight changed canonical key: first=%+v second=%+v", first.Key, second.Key)
	}
	firstRouting := first.Observation.Fields()["routing"].(map[string]any)
	secondRouting := second.Observation.Fields()["routing"].(map[string]any)
	if firstRouting["weight"] != int64(10) || secondRouting["weight"] != int64(90) || firstRouting["policy"] != "weighted" {
		t.Fatalf("routing fields first=%v second=%v", firstRouting, secondRouting)
	}
	zoneRelation := first.Observation.Fields()["zone_relation"].(map[string]any)
	if source, ok := zoneRelation["source"].(awsbrowser.ResourceKey); !ok || source != first.Key {
		t.Fatalf("zone relation source=%+v resource=%+v", zoneRelation["source"], first.Key)
	}
	if target, ok := zoneRelation["target"].(awsbrowser.ResourceKey); !ok || target.Type != "hosted-zone" || target.ID != "Z1" {
		t.Fatalf("zone relation target=%+v", zoneRelation["target"])
	}
}

func TestRoute53RejectsSemanticallyInvalidNamesAndZoneIDsBeforeCallingAPI(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		params    map[string]string
	}{
		{name: "zone id punctuation", operation: awsbrowser.OperationListResourceRecordSets, params: map[string]string{"hosted-zone-id": "Z1?bad"}},
		{name: "zone id embedded space", operation: awsbrowser.OperationListResourceRecordSets, params: map[string]string{"hosted-zone-id": "Z 1"}},
		{name: "zone id wrong prefix", operation: awsbrowser.OperationListResourceRecordSets, params: map[string]string{"hosted-zone-id": "hosted-zone-1"}},
		{name: "zone id extra path", operation: awsbrowser.OperationListResourceRecordSets, params: map[string]string{"hosted-zone-id": "/hostedzone/Z1/extra"}},
		{name: "dns embedded space", operation: awsbrowser.OperationListHostedZonesByName, params: map[string]string{"dns-name": "bad name.example.com"}},
		{name: "dns punctuation", operation: awsbrowser.OperationListHostedZonesByName, params: map[string]string{"dns-name": "bad!.example.com"}},
		{name: "dns leading empty label", operation: awsbrowser.OperationListHostedZonesByName, params: map[string]string{"dns-name": ".example.com"}},
		{name: "dns repeated trailing dot", operation: awsbrowser.OperationListHostedZonesByName, params: map[string]string{"dns-name": "example.com.."}},
		{name: "dns misplaced wildcard", operation: awsbrowser.OperationListResourceRecordSets, params: map[string]string{"hosted-zone-id": "Z1", "record-name": "www.*.example.com"}},
		{name: "dns unicode whitespace", operation: awsbrowser.OperationListResourceRecordSets, params: map[string]string{"hosted-zone-id": "Z1", "record-name": "bad\u00a0name.example.com"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			fake := &route53Fake{
				listHostedZones: func(context.Context, *route53.ListHostedZonesInput) (*route53.ListHostedZonesOutput, error) {
					calls++
					return &route53.ListHostedZonesOutput{}, nil
				},
				listHostedZonesByName: func(context.Context, *route53.ListHostedZonesByNameInput) (*route53.ListHostedZonesByNameOutput, error) {
					calls++
					return &route53.ListHostedZonesByNameOutput{}, nil
				},
				listRecordSets: func(context.Context, *route53.ListResourceRecordSetsInput) (*route53.ListResourceRecordSetsOutput, error) {
					calls++
					return &route53.ListResourceRecordSetsOutput{}, nil
				},
			}
			err := newRoute53ForTest(t, fake).Execute(context.Background(), route53Key(t, test.operation, test.params), &collectingSink{})
			if !errors.Is(err, awsbrowser.ErrInvalidQueryKey) || calls != 0 {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
		})
	}
}

func TestRoute53AcceptsSupportedDNSNameForms(t *testing.T) {
	tests := map[string]string{
		"_acme-challenge.Example.COM": "_acme-challenge.example.com.",
		"*.example.com.":              "*.example.com.",
		"\\052.example.com":           "\\052.example.com.",
	}
	for input, want := range tests {
		if got, err := canonicalDNSName(input); err != nil || got != want {
			t.Errorf("canonicalDNSName(%q)=%q, %v; want %q", input, got, err, want)
		}
	}
}

func TestRoute53RecordCursorRequiresCompleteAdvancingTuple(t *testing.T) {
	tests := []struct {
		name   string
		output *route53.ListResourceRecordSetsOutput
	}{
		{name: "missing name", output: &route53.ListResourceRecordSetsOutput{IsTruncated: true, NextRecordType: types.RRTypeA}},
		{name: "missing type", output: &route53.ListResourceRecordSetsOutput{IsTruncated: true, NextRecordName: aws.String("next.example.com.")}},
		{name: "same tuple", output: &route53.ListResourceRecordSetsOutput{IsTruncated: true, NextRecordName: aws.String("www.example.com."), NextRecordType: types.RRTypeA}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &route53Fake{listRecordSets: func(context.Context, *route53.ListResourceRecordSetsInput) (*route53.ListResourceRecordSetsOutput, error) {
				return test.output, nil
			}}
			key := route53Key(t, awsbrowser.OperationListResourceRecordSets, map[string]string{
				"hosted-zone-id": "Z1", "record-name": "www.example.com", "record-type": "A",
			})
			err := newRoute53ForTest(t, fake).Execute(context.Background(), key, &collectingSink{})
			if !errors.Is(err, awsbrowser.ErrQueryDecode) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestRoute53RecordCursorRejectsRepeatedTupleCycle(t *testing.T) {
	call := 0
	fake := &route53Fake{listRecordSets: func(context.Context, *route53.ListResourceRecordSetsInput) (*route53.ListResourceRecordSetsOutput, error) {
		call++
		name := "a.example.com."
		if call == 2 {
			name = "b.example.com."
		}
		return &route53.ListResourceRecordSetsOutput{
			IsTruncated: true, NextRecordName: aws.String(name), NextRecordType: types.RRTypeA,
		}, nil
	}}
	key := route53Key(t, awsbrowser.OperationListResourceRecordSets, map[string]string{"hosted-zone-id": "Z1"})
	err := newRoute53ForTest(t, fake).Execute(context.Background(), key, &collectingSink{})
	if !errors.Is(err, awsbrowser.ErrQueryDecode) || call != 3 {
		t.Fatalf("error=%v calls=%d", err, call)
	}
}

func TestRoute53CancellationAndOperationIsolation(t *testing.T) {
	started := make(chan struct{})
	fake := &route53Fake{listHostedZones: func(ctx context.Context, _ *route53.ListHostedZonesInput) (*route53.ListHostedZonesOutput, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	executor := newRoute53ForTest(t, fake)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- executor.Execute(ctx, route53Key(t, awsbrowser.OperationListHostedZones, nil), &collectingSink{})
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}

	calls := 0
	isolationFake := &route53Fake{listHostedZonesByName: func(context.Context, *route53.ListHostedZonesByNameInput) (*route53.ListHostedZonesByNameOutput, error) {
		calls++
		return &route53.ListHostedZonesByNameOutput{}, nil
	}}
	executor = newRoute53ForTest(t, isolationFake)
	if err := executor.Execute(context.Background(), route53Key(t, awsbrowser.OperationListHostedZonesByName, map[string]string{"dns-name": "example.com"}), &collectingSink{}); err != nil {
		t.Fatal(err)
	}
	badKey := route53Key(t, awsbrowser.OperationListHostedZonesByName, map[string]string{"dns-name": "example.com", "hosted-zone-id": "Z1"})
	if err := executor.Execute(context.Background(), badKey, &collectingSink{}); !errors.Is(err, awsbrowser.ErrInvalidQueryKey) {
		t.Fatalf("bad params error=%v", err)
	}
	if calls != 1 {
		t.Fatalf("selected operation calls=%d", calls)
	}
}

func TestRoute53CoordinatorRetainsPartialPageOnProviderFailure(t *testing.T) {
	call := 0
	fake := &route53Fake{listHostedZones: func(context.Context, *route53.ListHostedZonesInput) (*route53.ListHostedZonesOutput, error) {
		call++
		if call == 1 {
			return &route53.ListHostedZonesOutput{
				HostedZones: []types.HostedZone{hostedZone("Z1", "example.com.", false)},
				IsTruncated: true, NextMarker: aws.String("next"),
			}, nil
		}
		return nil, &smithy.GenericAPIError{Code: "AccessDenied", Message: "secret raw message"}
	}}
	executor := newRoute53ForTest(t, fake)
	store := awsbrowser.NewSessionStore()
	coordinator, err := awsbrowser.NewQueryCoordinator(store, executor, 1)
	if err != nil {
		t.Fatal(err)
	}
	key := route53Key(t, awsbrowser.OperationListHostedZones, nil)
	subscription, err := coordinator.Subscribe(key)
	if err != nil {
		t.Fatal(err)
	}
	var terminal awsbrowser.QueryUpdate
	for update := range subscription.Updates() {
		terminal = update
	}
	if terminal.Failure == nil || terminal.Failure.Kind != awsbrowser.ProviderForbidden || terminal.Failure.PartialPages != 1 {
		t.Fatalf("failure=%+v", terminal.Failure)
	}
	if terminal.Snapshot.ResourceCount() != 1 {
		t.Fatalf("partial resources=%d state=%s", terminal.Snapshot.ResourceCount(), terminal.Snapshot.State)
	}
}

func newRoute53ForTest(t *testing.T, fake awsbrowser.Route53API) awsbrowser.QueryExecutor {
	t.Helper()
	now := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	executor, err := NewRoute53(fake, func() time.Time {
		now = now.Add(time.Second)
		return now
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func route53Key(t *testing.T, operation string, params map[string]string) awsbrowser.QueryKey {
	t.Helper()
	awsContext, err := awsbrowser.NewAWSContext(
		awsbrowser.ContextSpec{Mode: awsbrowser.ContextModeNamedProfile, Profile: "test", Region: "ap-northeast-2"},
		awsbrowser.VerifiedIdentity{
			Partition: "aws", AccountID: "123456789012",
			PrincipalARN: "arn:aws:sts::123456789012:assumed-role/ReadOnly/test", CredentialGeneration: 1,
		},
		"ReadOnly",
	)
	if err != nil {
		t.Fatal(err)
	}
	key, err := awsbrowser.NewQueryKey(awsContext, awsbrowser.ProviderRoute53, operation, params)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func hostedZone(id, name string, private bool) types.HostedZone {
	return types.HostedZone{Id: aws.String(id), Name: aws.String(name), Config: &types.HostedZoneConfig{PrivateZone: private}}
}

func simpleRecord(name string, recordType types.RRType, value string) types.ResourceRecordSet {
	return types.ResourceRecordSet{Name: aws.String(name), Type: recordType, TTL: aws.Int64(60), ResourceRecords: []types.ResourceRecord{{Value: aws.String(value)}}}
}

func weightedRecord(name, identifier string, weight int64) types.ResourceRecordSet {
	record := simpleRecord(name, types.RRTypeA, "192.0.2.1")
	record.SetIdentifier = aws.String(identifier)
	record.Weight = aws.Int64(weight)
	return record
}

func resourceCount(pages []awsbrowser.QueryPage) int {
	count := 0
	for _, page := range pages {
		count += len(page.Resources())
	}
	return count
}
