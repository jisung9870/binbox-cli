//go:build linux

package awsbrowser

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const ptyStdoutSentinel = "AWSBROWSER_STDOUT_SENTINEL"

func TestTerminalProcessAltScreenLifecycleAndStderrRouting(t *testing.T) {
	if _, err := exec.LookPath("script"); err != nil {
		t.Fatalf("required util-linux script command is unavailable: %v", err)
	}
	for _, test := range []struct {
		name  string
		input string
	}{
		{name: "q", input: "q"},
		{name: "ctrl-c", input: "\x03"},
	} {
		t.Run(test.name, func(t *testing.T) {
			transcript, stdout := runPTYHelper(t, "tui", test.input)
			if !strings.Contains(transcript, "\x1b[?1049h") {
				t.Fatalf("alt-screen enter missing from transcript %q", transcript)
			}
			if !strings.Contains(transcript, "\x1b[?1049l") {
				t.Fatalf("alt-screen cleanup missing from transcript %q", transcript)
			}
			if strings.Contains(transcript, ptyStdoutSentinel) {
				t.Fatalf("stdout leaked into the stderr TUI transcript %q", transcript)
			}
			if !strings.HasPrefix(stdout, ptyStdoutSentinel) {
				t.Fatalf("redirected stdout=%q", stdout)
			}
		})
	}
}

func TestTerminalProcessSmallTerminalUsesPlainFallback(t *testing.T) {
	transcript, stdout := runPTYHelper(t, "small", "q\n")
	if strings.Contains(transcript, "\x1b[?1049h") || strings.Contains(transcript, "\x1b[?1049l") {
		t.Fatalf("small-terminal fallback entered alt screen: %q", transcript)
	}
	if !strings.Contains(transcript, "command [open <n>") {
		t.Fatalf("plain fallback instructions missing: %q", transcript)
	}
	if !strings.HasPrefix(stdout, ptyStdoutSentinel) {
		t.Fatalf("redirected stdout=%q", stdout)
	}
}

func TestTerminalProcessNonTTYExitsBeforeRendering(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAWSBrowserTerminalHelperProcess$")
	cmd.Env = append(os.Environ(), "AWSBROWSER_TERMINAL_HELPER=non-tty")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("non-TTY helper: %v stderr=%q", err, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), ptyStdoutSentinel) || stderr.Len() != 0 {
		t.Fatalf("non-TTY stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestAWSBrowserTerminalHelperProcess(t *testing.T) {
	mode := os.Getenv("AWSBROWSER_TERMINAL_HELPER")
	if mode == "" {
		return
	}
	_, _ = fmt.Fprint(os.Stdout, ptyStdoutSentinel)
	terminal := Terminal{In: os.Stdin, Err: os.Stderr, Width: 80, Height: 24}
	if mode == "non-tty" {
		if err := NewRunner(nil).Run(context.Background(), terminal, Config{}); !errors.Is(err, ErrNoInput) {
			t.Fatalf("non-TTY error=%v", err)
		}
		return
	}
	terminal.StdinTTY, terminal.StderrTTY = true, true
	if mode == "small" {
		terminal.Width, terminal.Height = 39, 11
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := NewRunner(nil).Run(ctx, terminal, Config{}); err != nil {
		t.Fatalf("runner: %v", err)
	}
}

func runPTYHelper(t *testing.T, mode, input string) (string, string) {
	t.Helper()
	stdoutPath := filepath.Join(t.TempDir(), "stdout")
	shellCommand := shellQuote(os.Args[0]) + " -test.run=^TestAWSBrowserTerminalHelperProcess$ >" + shellQuote(stdoutPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "script", "-qefc", shellCommand, "/dev/null")
	cmd.Env = append(os.Environ(), "AWSBROWSER_TERMINAL_HELPER="+mode)
	inputReader, inputWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdin = inputReader
	var transcript, scriptError bytes.Buffer
	cmd.Stdout, cmd.Stderr = &transcript, &scriptError
	if err := cmd.Start(); err != nil {
		_ = inputReader.Close()
		_ = inputWriter.Close()
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	_, writeErr := inputWriter.Write([]byte(input))
	_ = inputWriter.Close()
	_ = inputReader.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("PTY helper: %v transcript=%q stderr=%q", err, transcript.String(), scriptError.String())
	}
	stdout, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	return transcript.String(), string(stdout)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
