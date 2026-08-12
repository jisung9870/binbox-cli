package bb

import (
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func outputCommand(output string, requested *[][]string) func(string, ...string) *exec.Cmd {
	return func(name string, args ...string) *exec.Cmd {
		*requested = append(*requested, append([]string{name}, args...))
		return exec.Command("printf", "%s", output)
	}
}

func TestTMSessionsUsesSessionFormatOnly(t *testing.T) {
	a, out, _, _ := testApp(t)
	a.lookPath = func(name string) (string, error) { return "/test/" + name, nil }
	var requests [][]string
	a.command = outputCommand("$1\twork\t2\t1\t1700000000\n$2\tapi\t1\t0\t0\n", &requests)
	if err := a.Run([]string{"tm", "sessions", "--json"}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Data struct {
			Sessions []tmSession `json:"sessions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Data.Sessions) != 2 || got.Data.Sessions[0].ID != "$1" || got.Data.Sessions[0].Windows != 2 || !got.Data.Sessions[0].Attached || got.Data.Sessions[0].CreatedAtUnix != 1700000000 || got.Data.Sessions[0].StateSource != "tmux" || got.Data.Sessions[1].Attached {
		t.Fatalf("sessions=%s", out.String())
	}
	want := [][]string{{"tmux", "list-sessions", "-F", "#{session_id}\t#{session_name}\t#{session_windows}\t#{session_attached}\t#{session_created}"}}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%q want=%q", requests, want)
	}
}

func TestGXLogLimitRejectsUnsafeOrUnboundedValues(t *testing.T) {
	a, _, _, _ := testApp(t)
	for _, value := range []string{"0", "1001", "x", "1;evil"} {
		if err := a.Run([]string{"gx", "log", "--limit", value}); ExitCode(err) != ExitInvalidInvocation {
			t.Fatalf("limit %q err=%v", value, err)
		}
	}
}

func TestPortInspectUsesPlatformReaderAndNeverInvokesKill(t *testing.T) {
	a, out, _, _ := testApp(t)
	reader := "ss"
	commandOutput := "tcp LISTEN 0 4096 127.0.0.1:4321 0.0.0.0:* users:((\"api\",pid=99,fd=3))\n"
	if runtime.GOOS == "darwin" {
		reader = "lsof"
		commandOutput = "COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME\napi 99 user 3u IPv4 0x0 0t0 TCP 127.0.0.1:4321 (LISTEN)\n"
	}
	a.lookPath = func(name string) (string, error) {
		if name == reader {
			return "/test/" + reader, nil
		}
		return "", os.ErrNotExist
	}
	var requests [][]string
	a.command = outputCommand(commandOutput, &requests)
	if err := a.Run([]string{"port", "inspect", "4321", "--json"}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0][0] != reader || strings.Contains(strings.Join(requests[0], " "), "kill") {
		t.Fatalf("requests=%q", requests)
	}
	if !strings.Contains(out.String(), `"source":"`+reader+`"`) || !strings.Contains(out.String(), `"listening":true`) {
		t.Fatalf("inspect=%s", out.String())
	}
}

func TestParseLsofListenersUsesNodeProtocolAndNameAddress(t *testing.T) {
	listeners := parseLsofListeners("COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME\napi 99 user 3u IPv4 0x0 0t0 TCP 127.0.0.1:4321 (LISTEN)\n")
	if len(listeners) != 1 || listeners[0].Protocol != "tcp" || listeners[0].Address != "127.0.0.1:4321" || listeners[0].State != "LISTEN" || listeners[0].Process != "api 99" {
		t.Fatalf("listeners=%+v", listeners)
	}
}

func TestPortKillReobservesExactSortedPIDsAndUsesSIGTERM(t *testing.T) {
	a, out, _, _ := testApp(t)
	a.lookPath = func(name string) (string, error) { return "/test/" + name, nil }
	var requests [][]string
	a.command = outputCommand("22\n11\n22\n", &requests)
	if err := a.Run([]string{"port", "kill", "4321", "--yes"}); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"lsof", "-nP", "-t", "-i", ":4321"},
		{"lsof", "-nP", "-t", "-i", ":4321"},
		{"kill", "-TERM", "--", "11", "22"},
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%q want=%q", requests, want)
	}
	if !strings.Contains(out.String(), "11, 22") {
		t.Fatalf("out=%q", out.String())
	}
}

func TestPortKillCancellationDoesNotMutate(t *testing.T) {
	a, _, _, _ := testApp(t)
	a.in = strings.NewReader("n\n")
	a.lookPath = func(name string) (string, error) { return "/test/" + name, nil }
	var requests [][]string
	a.command = outputCommand("99\n", &requests)
	err := a.Run([]string{"port", "kill", "4321"})
	if ExitCode(err) != ExitInvalidInvocation || len(requests) != 1 {
		t.Fatalf("err=%v requests=%q", err, requests)
	}
}

func TestPortAndTMHelpAreAvailable(t *testing.T) {
	for _, command := range [][]string{{"port", "--help"}, {"tm", "--help"}} {
		a, out, _, _ := testApp(t)
		if err := a.Run(command); err != nil || !strings.Contains(out.String(), "Usage:") {
			t.Fatalf("command=%v err=%v help=%q", command, err, out.String())
		}
	}
}

func TestTMProjectsPlainPreservesPathListCompatibility(t *testing.T) {
	a, out, _, _ := testApp(t)
	first := t.TempDir()
	second := t.TempDir()
	if err := a.Run([]string{"project", "add", first, "first"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"project", "add", second, "second"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"tm", "projects", "--plain"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(out.String())
	if len(lines) != 2 || lines[0] != canonicalPath(first) || lines[1] != canonicalPath(second) {
		t.Fatalf("plain projects=%q", out.String())
	}
}
