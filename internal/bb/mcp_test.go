package bb

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestMCPCRUDStoresOnlyEnvironmentNames(t *testing.T) {
	a, out, config, _ := testApp(t)
	if err := a.Run([]string{
		"mcp", "add", "jira",
		"--http", "https://jira.example.test/mcp",
		"--description", "Jira work items",
		"--bearer-token-env", "JIRA_TOKEN",
		"--env", "JIRA_SITE",
		"--targets", "codex,claude",
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(config, "bb", "mcp.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "sec://") || strings.Contains(string(b), "topsecret") {
		t.Fatalf("registry contains a secret value: %s", b)
	}

	if err := a.Run([]string{"mcp", "show", "jira", "--json"}); err != nil {
		t.Fatal(err)
	}
	var shown struct {
		Data namedMCPServer `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &shown); err != nil {
		t.Fatal(err)
	}
	if shown.Data.Name != "jira" || shown.Data.BearerTokenEnvVar != "JIRA_TOKEN" || !reflect.DeepEqual(shown.Data.Targets, []string{"claude", "codex"}) {
		t.Fatalf("shown=%+v", shown.Data)
	}

	if err := a.Run([]string{"mcp", "edit", "jira", "--clear-env", "--clear-bearer-token-env", "--targets", "codex"}); err != nil {
		t.Fatal(err)
	}
	store, _, err := a.loadMCP()
	if err != nil {
		t.Fatal(err)
	}
	server := store.Servers["jira"]
	if len(server.EnvVars) != 0 || server.BearerTokenEnvVar != "" || !reflect.DeepEqual(server.Targets, []string{"codex"}) {
		t.Fatalf("edited=%+v", server)
	}
	if err := a.Run([]string{"mcp", "rm", "jira", "--yes"}); err != nil {
		t.Fatal(err)
	}
	store, _, err = a.loadMCP()
	if err != nil || len(store.Servers) != 0 {
		t.Fatalf("store=%+v err=%v", store, err)
	}
}

func TestMCPRejectsSecretValuesAndCredentialURLs(t *testing.T) {
	for _, args := range [][]string{
		{"mcp", "add", "bad-env", "--stdio", "server", "--env", "TOKEN=topsecret"},
		{"mcp", "add", "bad-url", "--http", "https://user:password@example.test/mcp"},
		{"mcp", "add", "bad-target", "--stdio", "server", "--targets", "other"},
		{"mcp", "add", "bad-command", "--stdio", "--not-a-command"},
	} {
		a, _, _, _ := testApp(t)
		if err := a.Run(args); ExitCode(err) != ExitInvalidInvocation {
			t.Fatalf("args=%v err=%v", args, err)
		}
	}
}

func TestConcurrentMCPAddsAreSerialized(t *testing.T) {
	a, _, _, _ := testApp(t)
	b := New(new(strings.Builder), new(strings.Builder), append([]string{}, a.env...))
	start := make(chan struct{})
	errs := make(chan error, 2)
	var group sync.WaitGroup
	for _, item := range []struct {
		app  *App
		name string
	}{{a, "jira"}, {b, "grafana"}} {
		group.Add(1)
		go func(app *App, name string) {
			defer group.Done()
			<-start
			errs <- app.Run([]string{"mcp", "add", name, "--http", "https://" + name + ".example.test/mcp"})
		}(item.app, item.name)
	}
	close(start)
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	store, _, err := a.loadMCP()
	if err != nil || len(store.Servers) != 2 {
		t.Fatalf("servers=%v err=%v", store.Servers, err)
	}
}

func TestMCPSyncUsesOwnerCLIsWithoutSecretValues(t *testing.T) {
	a, _, _, _ := testApp(t)
	a.lookPath = func(name string) (string, error) { return "/test/" + name, nil }
	if err := a.Run([]string{"mcp", "add", "jira", "--http", "https://jira.example.test/mcp", "--bearer-token-env", "JIRA_TOKEN"}); err != nil {
		t.Fatal(err)
	}
	var requests [][]string
	a.command = outputCommand("", &requests)
	if err := a.Run([]string{"mcp", "sync", "codex", "jira"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"mcp", "sync", "claude", "jira"}); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"codex", "mcp", "remove", "jira"},
		{"codex", "mcp", "add", "jira", "--url", "https://jira.example.test/mcp", "--bearer-token-env-var", "JIRA_TOKEN"},
		{"claude", "mcp", "remove", "--scope", "user", "jira"},
		{"claude", "mcp", "add", "--scope", "user", "--transport", "http", "jira", "https://jira.example.test/mcp", "--header", "Authorization: Bearer ${JIRA_TOKEN}"},
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%q want=%q", requests, want)
	}
	if strings.Contains(strings.Join(flattenStrings(requests), " "), "topsecret") {
		t.Fatalf("sync leaked a secret: %q", requests)
	}
}

func TestMCPSyncStdioPreservesArgumentBoundaries(t *testing.T) {
	a, _, _, _ := testApp(t)
	a.lookPath = func(name string) (string, error) { return "/test/" + name, nil }
	if err := a.Run([]string{"mcp", "add", "personal", "--stdio", "npx", "--arg", "-y", "--arg", "my mcp server", "--targets", "codex"}); err != nil {
		t.Fatal(err)
	}
	var requests [][]string
	a.command = outputCommand("", &requests)
	if err := a.Run([]string{"mcp", "sync", "codex", "personal"}); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"codex", "mcp", "remove", "personal"},
		{"codex", "mcp", "add", "personal", "--", "npx", "-y", "my mcp server"},
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%q want=%q", requests, want)
	}
}

func TestMCPCheckReportsMissingEnvironmentWithoutPrintingValues(t *testing.T) {
	a, out, _, _ := testApp(t)
	a.env = append(a.env, "PRESENT_TOKEN=topsecret")
	a.lookPath = func(name string) (string, error) { return "/test/" + name, nil }
	if err := a.Run([]string{"mcp", "add", "grafana", "--http", "https://grafana.example.test/mcp", "--env", "MISSING_SITE", "--bearer-token-env", "PRESENT_TOKEN", "--targets", "codex"}); err != nil {
		t.Fatal(err)
	}
	var requests [][]string
	a.command = outputCommand(`{"name":"grafana"}`, &requests)
	if err := a.Run([]string{"mcp", "check", "grafana", "--json"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "topsecret") || !strings.Contains(out.String(), "MISSING_SITE") || !strings.Contains(out.String(), `"codex":"registered"`) {
		t.Fatalf("check output=%s", out.String())
	}
	want := [][]string{{"codex", "mcp", "get", "grafana", "--json"}}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%q want=%q", requests, want)
	}
}

func TestClaudeMCPHealthDistinguishesRegistrationFromConnection(t *testing.T) {
	tests := map[string]string{
		"Status: ✘ Failed to connect": "registered but unhealthy",
		"Status: ✓ Connected":         "registered and healthy",
		"Pending approval":            "pending approval",
		"Scope: User":                 "registered; health unknown",
	}
	for output, want := range tests {
		if got := claudeMCPHealth(output); got != want {
			t.Fatalf("output=%q got=%q want=%q", output, got, want)
		}
	}
}

func TestMCPPlainManagerCanAddServer(t *testing.T) {
	a, _, _, _ := testApp(t)
	a.env = append(a.env, "BB_SELECTOR=plain")
	a.in = strings.NewReader("1\npersonal\nLocal tools\nstdio\nnpx\n-y,@example/mcp\n\nclaude,codex\n")
	if err := a.Run([]string{"mcp"}); err != nil {
		t.Fatal(err)
	}
	store, _, err := a.loadMCP()
	if err != nil {
		t.Fatal(err)
	}
	server, exists := store.Servers["personal"]
	if !exists || server.Command != "npx" || !reflect.DeepEqual(server.Args, []string{"-y", "@example/mcp"}) {
		t.Fatalf("server=%+v exists=%v", server, exists)
	}
}

func flattenStrings(values [][]string) []string {
	var result []string
	for _, value := range values {
		result = append(result, value...)
	}
	return result
}
