package awsbrowser

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestRunnerRejectsNonTTYBeforeProgram(t *testing.T) {
	runner := NewRunner(nil)
	called := false
	runner.program = func(context.Context, tea.Model, Terminal) (tea.Model, error) { called = true; return nil, nil }
	err := runner.Run(context.Background(), Terminal{In: strings.NewReader(""), Err: new(bytes.Buffer)}, Config{})
	if !errors.Is(err, ErrNoInput) || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestRunnerUsesPlainBeforeAltScreenForSmallStartup(t *testing.T) {
	runner := NewRunner(nil)
	called := false
	runner.program = func(context.Context, tea.Model, Terminal) (tea.Model, error) { called = true; return nil, nil }
	var out bytes.Buffer
	err := runner.Run(context.Background(), Terminal{In: strings.NewReader("quit\n"), Err: &out, StdinTTY: true, StderrTTY: true, Width: 39, Height: 12}, Config{})
	if err != nil || called || !strings.Contains(out.String(), "command [open <n>") {
		t.Fatalf("err=%v called=%v out=%q", err, called, out.String())
	}
}

func TestRunnerCancelsRootAndRequestsAltScreen(t *testing.T) {
	runner := NewRunner(nil)
	var runCtx context.Context
	runner.program = func(ctx context.Context, model tea.Model, terminal Terminal) (tea.Model, error) {
		runCtx = ctx
		if !model.View().AltScreen {
			t.Fatal("TUI did not request alt screen")
		}
		return model, nil
	}
	err := runner.Run(context.Background(), Terminal{In: strings.NewReader(""), Err: new(bytes.Buffer), StdinTTY: true, StderrTTY: true, Width: 80, Height: 24}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runCtx.Done():
	default:
		t.Fatal("root context not cancelled after program cleanup")
	}
}
