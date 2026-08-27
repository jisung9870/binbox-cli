package awsbrowser

import "testing"

func TestTerminalContract(t *testing.T) {
	for _, test := range []struct {
		name                   string
		terminal               Terminal
		interactive, smallWant bool
	}{
		{"stdin and stderr tty", Terminal{StdinTTY: true, StderrTTY: true, Width: 80, Height: 24}, true, false},
		{"stdout is irrelevant", Terminal{StdinTTY: true, StderrTTY: true, Width: 40, Height: 12}, true, false},
		{"stdin pipe", Terminal{StderrTTY: true, Width: 80, Height: 24}, false, false},
		{"stderr pipe", Terminal{StdinTTY: true, Width: 80, Height: 24}, false, false},
		{"narrow", Terminal{StdinTTY: true, StderrTTY: true, Width: 39, Height: 12}, true, true},
		{"short", Terminal{StdinTTY: true, StderrTTY: true, Width: 40, Height: 11}, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.terminal.Interactive(); got != test.interactive {
				t.Fatalf("Interactive()=%v, want %v", got, test.interactive)
			}
			if got := test.terminal.Small(); got != test.smallWant {
				t.Fatalf("Small()=%v, want %v", got, test.smallWant)
			}
		})
	}
}

func TestTerminalMessagesAreExact(t *testing.T) {
	if ScopedQueryGuidance != "bb: aws browse requires an interactive TTY; use a scoped query:\n  bb aws query ec2 instances --profile dev --region ap-northeast-2 --json\n  bb aws query domain api.example.com --scope all --json\n" {
		t.Fatalf("guidance changed: %q", ScopedQueryGuidance)
	}
	if MinimumSizeMessage != "Terminal too small (need 40x12).\nResize or rerun with BB_SELECTOR=plain." {
		t.Fatalf("minimum size message changed: %q", MinimumSizeMessage)
	}
}

func TestTerminalUnknownDimensionsStayInteractiveAndUseAdaptiveTUI(t *testing.T) {
	terminal := Terminal{StdinTTY: true, StderrTTY: true}
	if !terminal.Interactive() {
		t.Fatal("TTYs with temporarily unknown dimensions must remain interactive")
	}
	if terminal.Small() {
		t.Fatal("unknown dimensions must not force the plain small-terminal fallback")
	}
}
