package awsbrowser

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type recordingDispatcher struct {
	intents []Intent
	err     error
}

func (d *recordingDispatcher) Dispatch(_ context.Context, intent Intent) error {
	d.intents = append(d.intents, intent)
	return d.err
}

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

func TestModelLabelsEmptyProfileAsAmbient(t *testing.T) {
	m := NewModel(context.Background(), Config{}, nil)
	if !strings.Contains(m.View().Content, "Profile ambient") {
		t.Fatalf("Home did not label the ambient context:\n%s", m.View().Content)
	}
}

func TestModelDispatchesOnlyWhenIntentCommandRuns(t *testing.T) {
	dispatcher := new(recordingDispatcher)
	m := NewModel(context.Background(), Config{Profile: "dev", Region: "ap-northeast-2"}, dispatcher)
	updated, cmd := m.Update(key(tea.KeyEnter))
	if len(dispatcher.intents) != 0 {
		t.Fatal("Update eagerly dispatched")
	}
	if cmd == nil {
		t.Fatal("open returned nil intent command")
	}
	result := cmd()
	if len(dispatcher.intents) != 1 || dispatcher.intents[0].Target != "ec2-instances" {
		t.Fatalf("intents=%+v", dispatcher.intents)
	}
	updated, _ = updated.Update(result)
	if !strings.Contains(updated.View().Content, "Loading EC2 Instances") {
		t.Fatalf("view=%s", updated.View().Content)
	}
}

func TestModelResizePreservesRouteAndSelection(t *testing.T) {
	m := NewModel(context.Background(), Config{}, nil)
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
	if !strings.Contains(model.View().Content, "> Route 53 Hosted Zones") {
		t.Fatalf("selection not restored:\n%s", model.View().Content)
	}
}

func TestIntentErrorIsNotProviderSuccess(t *testing.T) {
	dispatcher := &recordingDispatcher{err: errors.New("not\x1b[31m integrated")}
	m := NewModel(context.Background(), Config{}, dispatcher)
	model, cmd := m.Update(key(tea.KeyEnter))
	model, _ = model.Update(cmd())
	if strings.Contains(model.View().Content, "\x1b") || !strings.Contains(model.View().Content, "! ec2-instances: not [31m integrated") {
		t.Fatalf("view=%s", model.View().Content)
	}
}
