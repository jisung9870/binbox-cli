package awsbrowser

import (
	"context"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestBrowserUsesFullViewportAndAdaptiveColor(t *testing.T) {
	colored := NewModel(context.Background(), Config{}, nil)
	dark := colored.View().Content
	updated, _ := colored.Update(tea.BackgroundColorMsg{Color: color.White})
	light := updated.View().Content
	if !strings.Contains(dark, "\x1b[") || !strings.Contains(light, "\x1b[") || dark == light {
		t.Fatalf("adaptive color was not applied: dark=%q light=%q", dark, light)
	}

	plain := NewModel(context.Background(), Config{NoColor: true}, nil)
	plain.width, plain.height = 140, 40
	content := plain.View().Content
	if strings.Contains(content, "\x1b[") {
		t.Fatalf("NO_COLOR view contains escape sequence: %q", content)
	}
	lines := strings.Split(content, "\n")
	if len(lines) != 40 {
		t.Fatalf("full viewport height=%d, want 40", len(lines))
	}
	if !strings.HasPrefix(lines[0], "╭") || ansi.StringWidth(lines[0]) != 140 {
		t.Fatalf("full viewport width was not used: width=%d line=%q", ansi.StringWidth(lines[0]), lines[0])
	}
	if !strings.HasPrefix(lines[len(lines)-1], "╰") {
		t.Fatalf("full viewport bottom border is missing:\n%s", content)
	}
}

func TestHomeResponsiveGoldens(t *testing.T) {
	for _, size := range []struct {
		name          string
		width, height int
	}{
		{"120x30", 120, 30}, {"80x24", 80, 24}, {"50x16", 50, 16}, {"40x12", 40, 12},
	} {
		t.Run(size.name, func(t *testing.T) {
			m := NewModel(context.Background(), Config{Profile: "dev", Region: "ap-northeast-2", NoColor: true}, nil)
			model, _ := m.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
			assertGolden(t, size.name+".golden", model.View().Content)
		})
	}
}

func TestNarrowHomeKeepsSelectedLoadBalancerAndSearchRowsVisible(t *testing.T) {
	m := NewModel(context.Background(), Config{Profile: "dev", Region: "ap-northeast-2", NoColor: true}, nil)
	m.width, m.height = 40, 12
	for range 4 {
		model, _ := m.Update(key(tea.KeyDown))
		m = model.(Model)
	}
	if !strings.Contains(m.View().Content, "> Load Balancers") {
		t.Fatalf("selected load-balancer row is outside the narrow viewport:\n%s", m.View().Content)
	}
	model, _ := m.Update(key(tea.KeyDown))
	m = model.(Model)
	if !strings.Contains(m.View().Content, "> Cross-profile") {
		t.Fatalf("selected search row is outside the narrow viewport:\n%s", m.View().Content)
	}
}

func TestResourceAndDetailResponsiveGoldens(t *testing.T) {
	for _, test := range []struct {
		name          string
		width, height int
		detail        bool
	}{
		{name: "resources-120x30", width: 120, height: 30},
		{name: "resources-50x16", width: 50, height: 16},
		{name: "detail-80x24", width: 80, height: 24, detail: true},
		{name: "detail-40x12", width: 40, height: 12, detail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			resource := resourceProjection("web-api", "running")
			stream := newTestIntentStream()
			dispatcher := &recordingDispatcher{streams: []*testIntentStream{stream}}
			m := NewModel(context.Background(), Config{Profile: "dev", Region: "ap-northeast-2", NoColor: true}, dispatcher)
			m.width, m.height = test.width, test.height
			model, wait := runModelCommand(t, m, key(tea.KeyEnter))
			stream.updates <- IntentUpdate{
				Query:      QueryUpdate{Snapshot: QuerySnapshot{State: LoadReady, FetchedAt: time.Date(2026, 8, 28, 14, 32, 8, 0, time.Local)}},
				Projection: IntentProjection{Resources: []ResourceProjection{resource}}, Done: true,
			}
			model, _ = model.Update(wait())
			if test.detail {
				model, _ = model.Update(key(tea.KeyEnter))
			}
			content := model.View().Content
			if strings.Contains(content, "\x1b") {
				t.Fatalf("NO_COLOR view contains escape sequence: %q", content)
			}
			assertGolden(t, test.name+".golden", content)
		})
	}
}

func TestDetailWrapsLongValuesWithoutDroppingText(t *testing.T) {
	value := strings.Repeat("metadata-", 12)
	lines := wrappedField("Long value", value, 36)
	joined := strings.ReplaceAll(strings.Join(lines, ""), " ", "")
	if !strings.Contains(joined, value) {
		t.Fatalf("wrapped value lost content: lines=%q", lines)
	}
}

func TestOverviewPrioritizesServiceFieldsAndCompactsPreviews(t *testing.T) {
	resource := ResourceProjection{
		Target:   "ec2.instance:i-001",
		Title:    "web-api",
		Subtitle: "i-001 · running",
		Fields: []ProjectionField{
			{Label: "Full Only", Value: "open Detail to see this"},
			{Label: "Subnet Id", Value: "subnet-app"},
			{Label: "VPC Id", Value: "vpc-main"},
			{Label: "Public IP Address", Value: "203.0.113.10"},
			{Label: "Private IP Address", Value: "10.0.1.24"},
			{Label: "Availability Zone", Value: "ap-northeast-2a"},
			{Label: "Instance Type", Value: "m7i.large"},
			{Label: "State", Value: "running"},
			{Label: "Name", Value: "web-api"},
		},
		Relations: []ProjectionRelation{
			{Label: "web-sg", Target: "ec2.security-group:sg-web"},
			{Label: "ops-sg", Target: "ec2.security-group:sg-ops"},
			{Label: "metrics-sg", Target: "ec2.security-group:sg-metrics"},
		},
		Tags: []ProjectionTag{{Key: "Name", Value: "web-api"}, {Key: "Env", Value: "prod"}, {Key: "Owner", Value: "platform"}},
	}
	m := NewModel(context.Background(), Config{NoColor: true}, nil)
	m.width, m.height = 120, 30
	m.history = []routeFrame{{mode: routeDetail, detail: resource}}
	view := m.View().Content
	for _, want := range []string{
		"AWS > web-api > Overview", "At a glance", "Instance Type", "m7i.large", "VPC Id", "vpc-main",
		"1:web-sg · 2:ops-sg · +1", "Name=web-api · Env=prod · +1",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("overview missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "open Detail to see this") {
		t.Fatalf("overview exposed a non-priority full-detail field:\n%s", view)
	}
}

func TestWideResourceListShowsLocalQuickPreview(t *testing.T) {
	resource := resourceProjection("web-api", "running")
	resource.Relations = []ProjectionRelation{{Label: "web-sg", Target: "ec2.security-group:sg-web"}}
	resource.Tags = []ProjectionTag{{Key: "Env", Value: "prod"}}
	m := NewModel(context.Background(), Config{NoColor: true}, nil)
	m.width, m.height = 120, 30
	m.history = []routeFrame{{mode: routeList, label: "EC2", projection: IntentProjection{Resources: []ResourceProjection{resource}}}}
	view := m.View().Content
	for _, want := range []string{"Quick Preview", "At a glance", "Relations", "web-sg", "Tags", "Env=prod"} {
		if !strings.Contains(view, want) {
			t.Fatalf("quick preview missing %q:\n%s", want, view)
		}
	}
}

func TestNarrowOverviewKeepsSelectedExploreRowVisible(t *testing.T) {
	resource := ResourceProjection{
		Target: "ec2.instance:i-001",
		Title:  "web-api",
		Relations: []ProjectionRelation{
			{Label: "alias", Target: "elbv2.load-balancer-dns:api.example.com"},
			{Label: "listener", Target: "elbv2.listeners:lb"},
			{Label: "target-group", Target: "elbv2.target-group:tg"},
			{Label: "security-group", Target: "ec2.security-group:sg"},
			{Label: "volume", Target: "ec2.volume:vol"},
			{Label: "vpc", Target: "ec2.vpc:vpc"},
		},
	}
	categories := detailCategories(resource)
	m := NewModel(context.Background(), Config{NoColor: true}, nil)
	m.width, m.height = 40, 12
	m.history = []routeFrame{{mode: routeDetail, detail: resource, relationSelected: len(categories) - 1}}
	view := m.View().Content
	if !strings.Contains(view, "> Tags") || !strings.Contains(view, "Explore (9) · rows") {
		t.Fatalf("selected Explore row is outside the narrow viewport:\n%s", view)
	}
}

func TestDetailExpandsPolicyAndRoute53JSON(t *testing.T) {
	resource := ResourceProjection{
		Title: "ReadOnly",
		Fields: []ProjectionField{
			{Label: "Policy Document", Value: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`},
			{Label: "Alias", Value: `{"dns_name":"target.example.com.","evaluate_target_health":true}`},
		},
	}
	m := NewModel(context.Background(), Config{NoColor: true}, nil)
	m.width, m.height = 120, 30
	m.history = []routeFrame{{mode: routeFields, detail: resource}}
	view := m.View().Content
	for _, want := range []string{`"Statement": [`, `"Effect": "Allow"`, `"dns_name": "target.example.com."`} {
		if !strings.Contains(view, want) {
			t.Fatalf("expanded JSON detail missing %q:\n%s", want, view)
		}
	}
}

func TestNarrowDetailScrollReachesWrappedFields(t *testing.T) {
	resource := resourceProjection("web-api", "running")
	m := NewModel(context.Background(), Config{NoColor: true}, nil)
	m.width, m.height = 40, 12
	m.history = []routeFrame{{mode: routeFields, detail: resource, scroll: 100}}
	view := m.View().Content
	if !strings.Contains(view, "metadata") || !strings.Contains(view, "pg↑↓ scroll") {
		t.Fatalf("narrow scroll did not expose later detail content:\n%s", view)
	}
}

func TestNarrowListKeepsSelectionAndFooterVisible(t *testing.T) {
	resources := make([]ResourceProjection, 20)
	for index := range resources {
		resources[index] = resourceProjection(fmt.Sprintf("resource-%02d", index), "ready")
	}
	m := NewModel(context.Background(), Config{NoColor: true}, nil)
	m.width, m.height = 40, 12
	m.history = []routeFrame{{mode: routeList, label: "EC2", selected: 19, status: "Ready", projection: IntentProjection{Resources: resources}}}
	view := m.View().Content
	if !strings.Contains(view, "> resource-19") || !strings.Contains(view, "→/enter") {
		t.Fatalf("selection/footer not visible:\n%s", view)
	}
}

func TestNarrowContextKeepsSearchRegionAndFooterVisible(t *testing.T) {
	m := NewModel(context.Background(), Config{NoColor: true}, nil)
	m.width, m.height = 40, 12
	m.history = []routeFrame{{
		mode: routeContext, contextChoices: []ContextChoice{{Profile: "prod-readonly", Region: "ap-northeast-2"}},
		contextRegion: "ap-northeast-2", status: "Choose a profile and region, then verify its account.",
	}}
	view := m.View().Content
	for _, want := range []string{"Search", "prod-readonly", "Region", "enter verify"} {
		if !strings.Contains(view, want) {
			t.Fatalf("narrow context missing %q:\n%s", want, view)
		}
	}
}

func TestK9sRelationTableAdaptsColumnsWithoutLosingConditions(t *testing.T) {
	resource := ResourceProjection{Title: "distribution", Relations: []ProjectionRelation{{
		Label: "report origin", Target: "s3.bucket:reports", Type: "routes-to", Direction: "outgoing",
		Condition: "report/*", Kind: "inferred", Scope: GlobalRegion,
	}}}
	for _, test := range []struct {
		name          string
		width, height int
		want          []string
		hidden        []string
	}{
		{name: "120 columns", width: 120, height: 30, want: []string{"DIR", "RELATION", "TARGET", "CONDITION", "CONFIDENCE", "SCOPE"}},
		{name: "80 columns", width: 80, height: 24, want: []string{"DIR", "RELATION", "TARGET", "CONDITION", "CONFIDENCE"}, hidden: []string{"SCOPE"}},
		{name: "50 columns", width: 50, height: 16, want: []string{"DIR", "RELATION", "TARGET", "CONDITION"}, hidden: []string{"CONFIDENCE", "SCOPE"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := NewModel(context.Background(), Config{NoColor: true}, nil)
			m.width, m.height = test.width, test.height
			m.history = []routeFrame{{mode: routeRelations, label: "Origins", relationGroup: "origins", detail: resource}}
			view := m.View().Content
			if !viewLineContainsAll(view, test.want...) || !strings.Contains(view, "report/*") || !strings.Contains(view, "routes-to") {
				t.Fatalf("relation columns or condition missing:\n%s", view)
			}
			for _, hidden := range test.hidden {
				if viewLineContainsAll(view, hidden) {
					t.Fatalf("secondary column %q should be hidden:\n%s", hidden, view)
				}
			}
		})
	}
}

func TestK9sContextAndTagsUseColumnHeaders(t *testing.T) {
	m := NewModel(context.Background(), Config{NoColor: true}, nil)
	m.width, m.height = 120, 30
	m.history = []routeFrame{{mode: routeContext, contextChoices: []ContextChoice{{
		Profile: "prod-readonly", Region: "ap-northeast-2", Group: "udg", Regions: []string{"ap-northeast-2", "us-east-1"},
	}}, contextRegion: "ap-northeast-2"}}
	contextView := m.View().Content
	if !viewLineContainsAll(contextView, "PROFILE", "REGION", "GROUP", "SCOPE") ||
		!viewLineContainsAll(contextView, "prod-readonly", "ap-northeast-2", "udg", "2 regions") {
		t.Fatalf("context table is incomplete:\n%s", contextView)
	}

	m.width, m.height = 50, 16
	m.history = []routeFrame{{mode: routeTags, detail: ResourceProjection{Title: "main-vpc", Tags: []ProjectionTag{{Key: "Owner", Value: "platform"}}}}}
	tagsView := m.View().Content
	if !viewLineContainsAll(tagsView, "KEY", "VALUE") || !viewLineContainsAll(tagsView, "Owner", "platform") {
		t.Fatalf("tags table is incomplete:\n%s", tagsView)
	}
}

func TestStartupContextWithManyProfilesKeepsFooterVisible(t *testing.T) {
	choices := make([]ContextChoice, 14)
	for index := range choices {
		choices[index] = ContextChoice{Profile: fmt.Sprintf("profile-%02d", index), Region: "ap-northeast-2"}
	}
	m := NewModel(context.Background(), Config{NoColor: true}, nil)
	m.width, m.height = 120, 30
	m.history = []routeFrame{{
		mode: routeContext, contextStartup: true, contextChoices: choices,
		contextRegion: "ap-northeast-2", status: "Choose a profile and region, then verify its account.",
	}}
	view := m.View().Content
	for _, want := range []string{"profile-00", "Region", "enter verify", "esc ambient home"} {
		if !strings.Contains(view, want) {
			t.Fatalf("startup context missing %q:\n%s", want, view)
		}
	}
}

func TestResolvedContextReplacesUnresolvedHeader(t *testing.T) {
	resolved := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	m := NewModel(context.Background(), Config{Profile: "dev", Region: "us-east-1", NoColor: true}, nil)
	m.history = []routeFrame{{mode: routeList, label: "EC2", context: &resolved}}
	view := m.View().Content
	for _, want := range []string{"Profile dev", "Account 123456789012", "ReadOnly", "us-east-1"} {
		if !strings.Contains(view, want) {
			t.Fatalf("resolved context missing %q:\n%s", want, view)
		}
	}
}

func TestSearchCoverageAndSelectedResourceProvenanceAreVisible(t *testing.T) {
	audit := testStoreContext(t, "audit", "123456789012", "us-west-2", 1)
	resource := ResourceProjection{Title: "api.example.com.", Context: &audit, Current: true, AvailableViaProfiles: []string{"audit", "read-only"}}
	coverage := &SearchCoverage{DiscoveryStatus: "timed_out", Partial: true, Profiles: []SearchProfileCoverage{
		{Profile: "audit", AccountID: "123456789012", Status: "matched", Current: true, Matches: 1},
		{Profile: "locked", Status: "forbidden"},
	}}
	m := NewModel(context.Background(), Config{NoColor: true}, nil)
	m.width, m.height = 120, 30
	m.history = []routeFrame{{mode: routeList, label: "Search results", status: searchCoverageStatus(coverage, 1, LoadReady), coverage: coverage, projection: IntentProjection{Resources: []ResourceProjection{resource}}}}
	list := m.View().Content
	for _, want := range []string{"Partial coverage", "Profile discovery · timed_out", "PROFILE", "locked", "forbidden"} {
		if !strings.Contains(list, want) {
			t.Fatalf("search list missing %q:\n%s", want, list)
		}
	}
	if !viewLineContainsAll(list, "audit", "123456789012", "matched", "current", "1") {
		t.Fatalf("current profile coverage row is incomplete:\n%s", list)
	}
	m.history = []routeFrame{{mode: routeFields, context: &audit, detail: resource}}
	detail := m.View().Content
	for _, want := range []string{"Provenance", "Account 123456789012", "Principal arn:aws:sts::123456789012:assumed-role/ReadOnly/session", "Profile audit · current yes", "Region us-west-2", "Available via audit, read-only"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("search detail missing %q:\n%s", want, detail)
		}
	}
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("golden %s mismatch\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}
