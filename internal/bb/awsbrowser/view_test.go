package awsbrowser

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
