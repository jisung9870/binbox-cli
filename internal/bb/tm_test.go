package bb

import (
	"bytes"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

const fakeTMSessions = "$1\talpha\t1\t0\t1\n$2\tbeta\t2\t1\t2\n"

func fakeTMCommand(requests *[][]string) func(string, ...string) *exec.Cmd {
	return func(name string, args ...string) *exec.Cmd {
		*requests = append(*requests, append([]string{name}, args...))
		if len(args) > 0 && args[0] == "list-sessions" {
			return exec.Command("printf", fakeTMSessions)
		}
		return exec.Command("true")
	}
}

func tmTestApp(t *testing.T) (*App, *bytes.Buffer, *[][]string) {
	t.Helper()
	a, out, _, _ := testApp(t)
	requests := new([][]string)
	a.lookPath = func(name string) (string, error) {
		if name == "tmux" || name == "fzf" {
			return "/fake/" + name, nil
		}
		return "", os.ErrNotExist
	}
	a.command = fakeTMCommand(requests)
	return a, out, requests
}

func TestTMAttachReobservesExactSession(t *testing.T) {
	a, _, requests := tmTestApp(t)
	if err := a.Run([]string{"tm", "attach", "--session", "alpha"}); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"tmux", "list-sessions", "-F", "#{session_id}\t#{session_name}\t#{session_windows}\t#{session_attached}\t#{session_created}"},
		{"tmux", "attach-session", "-t", "alpha"},
	}
	if !reflect.DeepEqual(*requests, want) {
		t.Fatalf("requests=%q want=%q", *requests, want)
	}
}

func TestTMAttachInsideTmuxSwitchesClient(t *testing.T) {
	a, _, requests := tmTestApp(t)
	a.env = append(a.env, "TMUX=/tmp/fake,1,0")
	if err := a.Run([]string{"tm", "attach", "--session", "beta"}); err != nil {
		t.Fatal(err)
	}
	if got := (*requests)[len(*requests)-1]; !reflect.DeepEqual(got, []string{"tmux", "switch-client", "-t", "beta"}) {
		t.Fatalf("last request=%q", got)
	}
}

func TestTMAttachRefusesSameNameReplacementAfterSelection(t *testing.T) {
	a, _, _, _ := testApp(t)
	a.lookPath = func(name string) (string, error) { return "/fake/" + name, nil }
	var listCalls int
	a.command = func(name string, args ...string) *exec.Cmd {
		if name == "fzf" {
			return exec.Command("printf", "$1\talpha\n")
		}
		if name == "tmux" && len(args) > 0 && args[0] == "list-sessions" {
			listCalls++
			id := "$1"
			if listCalls > 1 {
				id = "$9"
			}
			return exec.Command("printf", "%s", id+"\talpha\t1\t0\t1\n")
		}
		return exec.Command("true")
	}
	err := a.Run([]string{"tm", "attach"})
	if ExitCode(err) != ExitCapabilityUnavailable || !strings.Contains(err.Error(), "changed after selection") {
		t.Fatalf("err=%v", err)
	}
}

func TestTMKillShowsTargetAndReobserves(t *testing.T) {
	a, out, requests := tmTestApp(t)
	if err := a.Run([]string{"tm", "kill", "--session", "alpha", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Target tmux session: alpha") {
		t.Fatalf("out=%q", out.String())
	}
	var lists int
	for _, request := range *requests {
		if len(request) > 1 && request[1] == "list-sessions" {
			lists++
		}
	}
	if lists != 2 {
		t.Fatalf("list calls=%d want 2: %q", lists, *requests)
	}
	if got := (*requests)[len(*requests)-1]; !reflect.DeepEqual(got, []string{"tmux", "kill-session", "-t", "alpha"}) {
		t.Fatalf("last request=%q", got)
	}
}

func TestTMKillRefusesSameNameReplacement(t *testing.T) {
	a, _, _, _ := testApp(t)
	a.lookPath = func(name string) (string, error) { return "/fake/" + name, nil }
	var calls int
	a.command = func(name string, args ...string) *exec.Cmd {
		if len(args) > 0 && args[0] == "list-sessions" {
			calls++
			id := "$1"
			if calls > 1 {
				id = "$9"
			}
			return exec.Command("printf", "%s", id+"\talpha\t1\t0\t1\n")
		}
		return exec.Command("true")
	}
	err := a.Run([]string{"tm", "kill", "--session", "alpha", "--yes"})
	if ExitCode(err) != ExitCapabilityUnavailable || !strings.Contains(err.Error(), "changed before kill") {
		t.Fatalf("err=%v", err)
	}
}

func TestTMDirsOnlyMutatesBBRegistry(t *testing.T) {
	a, out, _, _ := testApp(t)
	directory := t.TempDir()
	if err := a.Run([]string{"tm", "dirs", "add", "--direct", directory}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"tm", "dirs", "list"}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != directory {
		t.Fatalf("dirs=%q", out.String())
	}
	if err := a.Run([]string{"tm", "dirs", "remove", "--yes", projectID(directory)}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"tm", "dirs", "list"}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Fatalf("dirs after remove=%q", out.String())
	}
}

func TestTMDirsPruneListsBeforeMutation(t *testing.T) {
	a, out, _, _ := testApp(t)
	stale := t.TempDir()
	if err := a.Run([]string{"project", "add", stale, "stale"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(stale); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"tm", "dirs", "prune", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Stale bb project paths:") || !strings.Contains(out.String(), stale) {
		t.Fatalf("out=%q", out.String())
	}
}

func TestTMLayoutUsesBuiltInDirectArgvAndRefusesCollision(t *testing.T) {
	a, out, requests := tmTestApp(t)
	path := t.TempDir()
	if err := a.Run([]string{"tm", "layout", "--layout", "golang", "--session", "dev", "--path", path}); err != nil {
		t.Fatal(err)
	}
	wantLast := [][]string{
		{"tmux", "new-session", "-d", "-s", "dev", "-c", path, "-n", "golang"},
		{"tmux", "split-window", "-h", "-t", "dev:0", "-c", path},
		{"tmux", "select-layout", "-t", "dev:0", "even-horizontal"},
	}
	got := (*requests)[1:]
	if !reflect.DeepEqual(got, wantLast) {
		t.Fatalf("requests=%q want=%q", got, wantLast)
	}
	if !strings.Contains(out.String(), "Created golang") {
		t.Fatalf("out=%q", out.String())
	}
	requestsBefore := len(*requests)
	err := a.Run([]string{"tm", "layout", "--layout", "k8s", "--session", "alpha", "--path", path})
	if err == nil || ExitCode(err) != ExitInvalidInvocation || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("collision error=%v", err)
	}
	if got := (*requests)[requestsBefore:]; len(got) != 1 || got[0][1] != "list-sessions" {
		t.Fatalf("collision should only inspect session, got=%q", got)
	}
}
