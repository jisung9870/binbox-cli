package bb

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
)

func TestParseAWSBrowseOptions(t *testing.T) {
	opts, err := parseAWSBrowseOptions([]string{"--profile", "dev", "--region", "ap-northeast-2"})
	if err != nil || opts.Profile != "dev" || opts.Region != "ap-northeast-2" {
		t.Fatalf("opts=%+v err=%v", opts, err)
	}
	for _, args := range [][]string{
		{"extra"}, {"--profile"}, {"--region", "bad\nregion"}, {"--profile", "bad profile"}, {"--json"},
		{"--profile", "dev", "--profile", "audit"}, {"--region", "us-east-1", "--region", "us-west-2"},
	} {
		if _, err := parseAWSBrowseOptions(args); ExitCode(err) != ExitInvalidInvocation {
			t.Fatalf("args=%q err=%v exit=%d", args, err, ExitCode(err))
		}
	}
}

func TestAWSBrowseHelpIsZeroCall(t *testing.T) {
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	a := New(stdout, stderr, nil)
	terminalCalls := 0
	a.awsBrowserTerminal = func() awsbrowser.Terminal { terminalCalls++; return awsbrowser.Terminal{} }
	a.lookPath = func(string) (string, error) { t.Fatal("help called lookPath"); return "", nil }
	a.command = func(string, ...string) *exec.Cmd { t.Fatal("help built command"); return nil }
	if err := a.Run([]string{"aws", "browse", "--help"}); err != nil {
		t.Fatal(err)
	}
	if terminalCalls != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "Home screen is local-only") {
		t.Fatalf("terminalCalls=%d stdout=%q stderr=%q", terminalCalls, stdout.String(), stderr.String())
	}
}

func TestAWSBrowseNonTTYIsExactZeroCall(t *testing.T) {
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	a := New(stdout, stderr, nil)
	a.awsBrowserTerminal = func() awsbrowser.Terminal { return awsbrowser.Terminal{In: strings.NewReader(""), Err: stderr} }
	a.lookPath = func(string) (string, error) { t.Fatal("non-TTY called lookPath"); return "", nil }
	if err := a.Run([]string{"aws", "browse"}); ExitCode(err) != ExitInvalidInvocation || !Reported(err) {
		t.Fatalf("err=%v exit=%d reported=%v", err, ExitCode(err), Reported(err))
	}
	if stdout.Len() != 0 || stderr.String() != awsbrowser.ScopedQueryGuidance {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestAWSBrowseRejectsLegacyJSONWithoutEnvelope(t *testing.T) {
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	a := New(stdout, stderr, nil)
	if err := a.Run([]string{"aws", "browse", "--json"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("err=%v", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestAWSBrowseInteractiveIgnoresStdoutTTY(t *testing.T) {
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	a := New(stdout, stderr, []string{"BB_SELECTOR=plain"})
	a.awsBrowserTerminal = func() awsbrowser.Terminal {
		return awsbrowser.Terminal{In: strings.NewReader("quit\n"), Err: stderr, StdinTTY: true, StderrTTY: true, Width: 80, Height: 24}
	}
	if err := a.Run([]string{"aws", "browse"}); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "AWS Browser · READ ONLY") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestAWSBrowsePlainEOFUsesExactGuidance(t *testing.T) {
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	a := New(stdout, stderr, []string{"BB_SELECTOR=plain"})
	a.awsBrowserTerminal = func() awsbrowser.Terminal {
		return awsbrowser.Terminal{In: strings.NewReader(""), Err: stderr, StdinTTY: true, StderrTTY: true, Width: 80, Height: 24}
	}
	if err := a.Run([]string{"aws", "browse"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("err=%v", err)
	}
	if stdout.Len() != 0 || stderr.String() != awsbrowser.ScopedQueryGuidance {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
