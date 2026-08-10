package bb

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestGXUsesDirectExplicitGitArguments(t *testing.T) {
	tests := []struct{ args, want []string }{
		{[]string{"gx", "branch", "switch", "feature/x"}, []string{"git", "switch", "--", "feature/x"}},
		{[]string{"gx", "branch", "new", "feature/y", "main"}, []string{"git", "switch", "-c", "feature/y", "main"}},
		{[]string{"gx", "log", "--limit", "7"}, []string{"git", "log", "--graph", "--decorate", "--oneline", "-n", "7"}},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args[1:], "-"), func(t *testing.T) {
			a, _, _, _ := testApp(t)
			a.lookPath = func(name string) (string, error) { return "/test/" + name, nil }
			var requests [][]string
			a.command = outputCommand("", &requests)
			if err := a.Run(tt.args); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(requests, [][]string{tt.want}) {
				t.Fatalf("requests=%q want=%q", requests, tt.want)
			}
		})
	}
}

func TestGXRefusesCurrentBranchDeletion(t *testing.T) {
	a, _, _, _ := testApp(t)
	a.lookPath = func(name string) (string, error) { return "/test/" + name, nil }
	var requests [][]string
	a.command = outputCommand("main\n", &requests)
	if err := a.Run([]string{"gx", "branch", "delete", "main", "--force"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("err=%v", err)
	}
	if len(requests) != 1 || requests[0][1] != "branch" || requests[0][2] != "--show-current" {
		t.Fatalf("requests=%q", requests)
	}
}

func TestGXDeleteShowsAndReobservesExactRef(t *testing.T) {
	a, out, _, _ := testApp(t)
	a.lookPath = func(name string) (string, error) { return "/test/" + name, nil }
	var requests [][]string
	a.command = outputCommand("abc123\n", &requests)
	if err := a.Run([]string{"gx", "branch", "delete", "feature/x", "--yes"}); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"git", "branch", "--show-current"},
		{"git", "rev-parse", "--verify", "refs/heads/feature/x"},
		{"git", "branch", "--show-current"},
		{"git", "rev-parse", "--verify", "refs/heads/feature/x"},
		{"git", "branch", "-d", "--", "feature/x"},
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%q want=%q", requests, want)
	}
	if !strings.Contains(out.String(), "feature/x (abc123)") {
		t.Fatalf("out=%q", out.String())
	}
}

func TestKXUsesDirectKubectlArguments(t *testing.T) {
	tests := []struct{ args, want []string }{
		{[]string{"kx", "context", "use", "dev"}, []string{"kubectl", "config", "use-context", "dev"}},
		{[]string{"kx", "namespace", "use", "api"}, []string{"kubectl", "config", "set-context", "--current", "--namespace=api"}},
		{[]string{"kx", "log", "pod-a", "-n", "api", "--tail", "50"}, []string{"kubectl", "logs", "pod-a", "--namespace=api", "--tail", "50"}},
		{[]string{"kx", "exec", "pod-a", "-c", "web", "--", "env"}, []string{"kubectl", "exec", "-it", "pod-a", "--container=web", "--", "env"}},
		{[]string{"kx", "pf", "pod-a", "8080:80", "-n", "api"}, []string{"kubectl", "port-forward", "pod-a", "8080:80", "--namespace=api"}},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args[1:], "-"), func(t *testing.T) {
			a, _, _, _ := testApp(t)
			a.lookPath = func(name string) (string, error) { return "/test/" + name, nil }
			var requests [][]string
			a.command = outputCommand("", &requests)
			if err := a.Run(tt.args); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(requests, [][]string{tt.want}) {
				t.Fatalf("requests=%q want=%q", requests, tt.want)
			}
		})
	}
}

func TestKXRejectsUnsafePortAndTailValues(t *testing.T) {
	a, _, _, _ := testApp(t)
	for _, args := range [][]string{
		{"kx", "pf", "pod", "0:80"}, {"kx", "pf", "pod", "8080:70000"}, {"kx", "log", "pod", "--tail", "1;rm"},
	} {
		if err := a.Run(args); ExitCode(err) != ExitInvalidInvocation {
			t.Fatalf("args=%q err=%v", args, err)
		}
	}
}

func TestASSMBuildsJSONParametersWithoutShell(t *testing.T) {
	a, _, _, _ := testApp(t)
	a.lookPath = func(name string) (string, error) { return "/test/" + name, nil }
	var requests [][]string
	a.command = outputCommand("", &requests)
	if err := a.Run([]string{"assm", "pf", "i-0123456789abcdef0", "5432", "15432", "db.internal"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"aws", "ssm", "start-session", "--target", "i-0123456789abcdef0", "--document-name", "AWS-StartPortForwardingSessionToRemoteHost", "--parameters", `{"host":["db.internal"],"localPortNumber":["15432"],"portNumber":["5432"]}`}
	if !reflect.DeepEqual(requests, [][]string{want}) {
		t.Fatalf("requests=%q want=%q", requests, want)
	}
}

func TestExternalAdaptersReportMissingOwnerCLI(t *testing.T) {
	a, _, _, _ := testApp(t)
	a.lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if err := a.Run([]string{"assm", "shell", "i-0123456789abcdef0"}); ExitCode(err) != ExitCapabilityUnavailable {
		t.Fatalf("err=%v", err)
	}
}
