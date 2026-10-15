package bb

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMCPSecretReferencesAreStoredAsReferences(t *testing.T) {
	a, _, config, _ := testApp(t)
	if err := a.Run([]string{"mcp", "add", "gitlab", "--stdio", "npx", "--arg", "@x/gitlab-mcp",
		"--secret-env", "GITLAB_TOKEN=gitlab/token", "--env", "GITLAB_URL"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(config, "bb", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"GITLAB_TOKEN": "sec://gitlab/token"`) {
		t.Fatalf("registry=%s", b)
	}

	for _, argv := range [][]string{
		{"mcp", "add", "bad1", "--http", "https://x.test/mcp", "--secret-env", "T=svc/f"},
		{"mcp", "add", "bad2", "--http", "https://x.test/mcp", "--bearer-token-env", "T", "--bearer-token-secret", "svc/f"},
		{"mcp", "add", "bad3", "--stdio", "npx", "--bearer-token-secret", "svc/f"},
		{"mcp", "add", "bad4", "--stdio", "npx", "--secret-env", "T=svc/a/b"},
		{"mcp", "add", "bad5", "--stdio", "npx", "--secret-env", "T=svc/f", "--env", "T"},
		{"mcp", "add", "bad6", "--stdio", "npx", "--secret-env", "T"},
	} {
		if err := a.Run(argv); ExitCode(err) != ExitInvalidInvocation {
			t.Fatalf("%v err=%v", argv, err)
		}
	}

	// Switching transport drops options that belong to the other one.
	if err := a.Run([]string{"mcp", "edit", "gitlab", "--http", "https://gitlab.test/mcp"}); err != nil {
		t.Fatal(err)
	}
	store, _, err := a.loadMCP()
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Servers["gitlab"].SecretEnv) != 0 {
		t.Fatalf("secret env survived a switch to http: %+v", store.Servers["gitlab"])
	}
}

func TestMCPSyncRegistersLauncherAndHeadersHelper(t *testing.T) {
	a, _, _, _ := testApp(t)
	a.lookPath = func(name string) (string, error) { return "/test/" + name, nil }
	if err := a.Run([]string{"mcp", "add", "gitlab", "--stdio", "npx", "--arg", "@x/gitlab-mcp", "--secret-env", "GITLAB_TOKEN=gitlab/token"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"mcp", "add", "jira", "--http", "https://jira.test/mcp", "--bearer-token-secret", "sec://jira/token", "--targets", "claude"}); err != nil {
		t.Fatal(err)
	}
	var requests [][]string
	a.command = outputCommand("", &requests)
	for _, argv := range [][]string{{"mcp", "sync", "codex", "gitlab"}, {"mcp", "sync", "claude", "gitlab"}, {"mcp", "sync", "claude", "jira"}} {
		if err := a.Run(argv); err != nil {
			t.Fatalf("%v: %v", argv, err)
		}
	}
	helper, _ := json.Marshal(map[string]string{"type": "http", "url": "https://jira.test/mcp", "headersHelper": "'/test/bb' mcp header 'jira'"})
	want := [][]string{
		{"codex", "mcp", "remove", "gitlab"},
		{"codex", "mcp", "add", "gitlab", "--", "/test/bb", "mcp", "launch", "gitlab"},
		{"claude", "mcp", "remove", "--scope", "user", "gitlab"},
		{"claude", "mcp", "add", "--scope", "user", "gitlab", "--", "/test/bb", "mcp", "launch", "gitlab"},
		{"claude", "mcp", "remove", "--scope", "user", "jira"},
		{"claude", "mcp", "add-json", "--scope", "user", "jira", string(helper)},
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%q\nwant=%q", requests, want)
	}

	// Codex has no headers helper: the registry refuses that target, and a
	// bearer secret without --targets defaults to Claude only.
	if err := a.Run([]string{"mcp", "edit", "jira", "--targets", "claude,codex"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("codex target err=%v", err)
	}
	if err := a.Run([]string{"mcp", "add", "wiki", "--http", "https://wiki.test/mcp", "--bearer-token-secret", "wiki/token"}); err != nil {
		t.Fatal(err)
	}
	store, _, err := a.loadMCP()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.Servers["wiki"].Targets, []string{"claude"}) {
		t.Fatalf("default targets=%q", store.Servers["wiki"].Targets)
	}
	requests = nil
	if err := a.Run([]string{"mcp", "sync", "codex"}); err != nil {
		t.Fatalf("codex sync with Claude-only servers: %v", err)
	}
	for _, request := range requests {
		if containsString(request, "jira") || containsString(request, "wiki") {
			t.Fatalf("codex touched a Claude-only server: %q", requests)
		}
	}
}

func TestMCPLaunchInjectsOnlyReferencedValuesIntoTheServer(t *testing.T) {
	a, _, _, _ := testApp(t)
	seedSecretStore(t, a, "gitlab", map[string]string{"token": "glpat-secret", "other": "unused-secret"})
	if err := a.Run([]string{"mcp", "add", "gitlab", "--stdio", "gitlab-mcp", "--arg", "--read-only", "--secret-env", "GITLAB_TOKEN=gitlab/token"}); err != nil {
		t.Fatal(err)
	}
	var gotPath string
	var gotArgv, gotEnv []string
	a.execReplace = func(path string, argv, env []string) error {
		gotPath, gotArgv, gotEnv = path, argv, env
		return nil
	}
	a.env = append(a.env, "GITLAB_TOKEN=stale-shell-value")
	if err := a.Run([]string{"mcp", "launch", "gitlab"}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "helper" || !reflect.DeepEqual(gotArgv, []string{"gitlab-mcp", "--read-only"}) {
		t.Fatalf("path=%q argv=%q", gotPath, gotArgv)
	}
	joined := strings.Join(gotEnv, "\n")
	if !strings.Contains(joined, "GITLAB_TOKEN=glpat-secret") || strings.Contains(joined, "stale-shell-value") || strings.Contains(joined, "unused-secret") {
		t.Fatalf("env=%q", gotEnv)
	}

	if err := a.Run([]string{"mcp", "add", "broken", "--stdio", "x", "--secret-env", "T=gitlab/missing"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"mcp", "launch", "broken"}); ExitCode(err) != ExitInvalidInvocation || strings.Contains(err.Error(), "secret") && strings.Contains(err.Error(), "glpat") {
		t.Fatalf("missing reference err=%v", err)
	}
}

func TestMCPLaunchWarnsAboutExpiringSecrets(t *testing.T) {
	a, _, _, _ := testApp(t)
	seedSecretStore(t, a, "gitlab", map[string]string{"token": "glpat-secret"})
	stderr := new(strings.Builder)
	a.err = stderr
	if err := a.Run([]string{"sec", "meta", "gitlab/token", "--expires", "3d"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"mcp", "add", "gitlab", "--stdio", "gitlab-mcp", "--secret-env", "GITLAB_TOKEN=gitlab/token"}); err != nil {
		t.Fatal(err)
	}
	a.execReplace = func(string, []string, []string) error { return nil }
	if err := a.Run([]string{"mcp", "launch", "gitlab"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "gitlab/token (D-3 !)") || strings.Contains(stderr.String(), "glpat") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestMCPHeaderRunsOnlyAsClaudeHelperForThatServer(t *testing.T) {
	a, out, _, _ := testApp(t)
	seedSecretStore(t, a, "jira", map[string]string{"token": "jira-secret"})
	if err := a.Run([]string{"mcp", "add", "jira", "--http", "https://jira.test/mcp", "--bearer-token-secret", "jira/token", "--targets", "claude"}); err != nil {
		t.Fatal(err)
	}
	base := append([]string{}, a.env...)
	for _, env := range []string{"", "CLAUDE_CODE_MCP_SERVER_NAME=other"} {
		a.env = append(append([]string{}, base...), env)
		out.Reset()
		if err := a.Run([]string{"mcp", "header", "jira"}); ExitCode(err) != ExitInvalidInvocation || strings.Contains(out.String(), "jira-secret") {
			t.Fatalf("env=%q out=%q err=%v", env, out.String(), err)
		}
	}
	a.env = append(append([]string{}, base...), "CLAUDE_CODE_MCP_SERVER_NAME=jira")
	out.Reset()
	if err := a.Run([]string{"mcp", "header", "jira"}); err != nil {
		t.Fatal(err)
	}
	var headers map[string]string
	if err := json.Unmarshal(out.Bytes(), &headers); err != nil || headers["Authorization"] != "Bearer jira-secret" {
		t.Fatalf("headers=%q err=%v", out.String(), err)
	}
}

func TestMCPCheckReportsMissingSecretReferences(t *testing.T) {
	a, out, _, _ := testApp(t)
	seedSecretStore(t, a, "gitlab", map[string]string{"token": "glpat-secret"})
	if err := a.Run([]string{"mcp", "add", "gitlab", "--stdio", "gitlab-mcp", "--secret-env", "A=gitlab/token", "--secret-env", "B=gitlab/gone", "--targets", "claude"}); err != nil {
		t.Fatal(err)
	}
	a.lookPath = func(name string) (string, error) {
		if name == "claude" {
			return "", os.ErrNotExist
		}
		return "helper", nil
	}
	out.Reset()
	if err := a.Run([]string{"mcp", "check", "gitlab", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"missing_secrets":["sec://gitlab/gone"]`) || strings.Contains(out.String(), "glpat") {
		t.Fatalf("check=%s", out.String())
	}
}
