package awsbrowser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestHomeResponsiveGoldens(t *testing.T) {
	for _, size := range []struct {
		name          string
		width, height int
	}{
		{"120x30", 120, 30}, {"80x24", 80, 24}, {"50x16", 50, 16}, {"40x12", 40, 12},
	} {
		t.Run(size.name, func(t *testing.T) {
			m := NewModel(context.Background(), Config{Profile: "dev", Region: "ap-northeast-2"}, nil)
			model, _ := m.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
			assertGolden(t, size.name+".golden", model.View().Content)
		})
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

func TestNarrowDetailScrollReachesWrappedFields(t *testing.T) {
	resource := resourceProjection("web-api", "running")
	m := NewModel(context.Background(), Config{NoColor: true}, nil)
	m.width, m.height = 40, 12
	m.history = []routeFrame{{mode: routeDetail, detail: resource, scroll: 100}}
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
	m := NewModel(context.Background(), Config{}, nil)
	m.width, m.height = 40, 12
	m.history = []routeFrame{{mode: routeList, label: "EC2", selected: 19, status: "Ready", projection: IntentProjection{Resources: resources}}}
	view := m.View().Content
	if !strings.Contains(view, "> resource-19") || !strings.Contains(view, "enter detail") {
		t.Fatalf("selection/footer not visible:\n%s", view)
	}
}

func TestResolvedContextReplacesUnresolvedHeader(t *testing.T) {
	resolved := testStoreContext(t, "dev", "123456789012", "us-east-1", 1)
	m := NewModel(context.Background(), Config{Profile: "dev", Region: "us-east-1"}, nil)
	m.history = []routeFrame{{mode: routeList, label: "EC2", context: &resolved}}
	view := m.View().Content
	for _, want := range []string{"123456789012/dev", "ReadOnly", "us-east-1"} {
		if !strings.Contains(view, want) {
			t.Fatalf("resolved context missing %q:\n%s", want, view)
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
