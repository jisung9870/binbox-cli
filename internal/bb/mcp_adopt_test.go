package bb

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeClientConfigs(t *testing.T, a *App) (string, string) {
	t.Helper()
	claudeDir, codexHome := t.TempDir(), t.TempDir()
	a.env = append(a.env, "CLAUDE_CONFIG_DIR="+claudeDir, "CODEX_HOME="+codexHome)
	claude := `{
  "mcpServers": {
    "gitlab": {"type": "stdio", "command": "gitlab-mcp", "args": ["--read-only"],
      "env": {"GITLAB_TOKEN": "glpat-plaintextvalue", "GITLAB_URL": "https://git.example.test", "HOME_HINT": "${HOME}"}},
    "jira": {"type": "http", "url": "https://jira.example.test/mcp", "headers": {"Authorization": "Bearer jira-plaintext"}},
    "clean": {"type": "stdio", "command": "bb", "args": ["wenv", "exec", "x", "--", "srv"]},
    "argtoken": {"type": "stdio", "command": "srv", "args": ["--token", "abc-plaintext"]},
    "remote": {"command": "npx", "args": ["mcp-remote", "https://x.test", "--header", "Authorization: Bearer remote-plaintext"]},
    "docker": {"command": "docker", "args": ["run", "-e", "API_KEY=docker-plaintext", "img"]},
    "apikey": {"type": "http", "url": "https://k.test/mcp", "headers": {"X-API-Key": "hdr-plaintext"}},
    "sse": {"type": "sse", "url": "https://s.test/sse", "headers": {"Authorization": "Bearer sse-plaintext"}},
    "paths": {"command": "node", "env": {"NODE_PATH": "/opt/node", "AUTHOR": "me", "HOME_DIR": "/x"}}
  },
  "projects": {"/work/app": {"mcpServers": {"grafana": {"command": "g", "env": {"GRAFANA_TOKEN": "glsa_plaintext"}}}}}
}`
	if err := os.WriteFile(filepath.Join(claudeDir, ".claude.json"), []byte(claude), 0o600); err != nil {
		t.Fatal(err)
	}
	codex := `model = "x"

[mcp_servers.slack]
command = "slack-mcp"
args = [
  "--stdio",
]
env = { SLACK_BOT_TOKEN = "xoxb-plaintext", SLACK_TEAM = "T1" }

[mcp_servers."web.search"]
url = "https://search.example.test/mcp"

[mcp_servers.web.search.tools.x]
approval_mode = "approve"

[mcp_servers.aws]
command = "aws-mcp"
startup_timeout_sec = 30

[mcp_servers.aws.env]
AWS_SECRET_ACCESS_KEY = 'literal-secret'
AWS_REGION = "ap-northeast-2"
`
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(codex), 0o600); err != nil {
		t.Fatal(err)
	}
	return claudeDir, codexHome
}

func TestMCPAuditSecretsReportsLocationsNotValues(t *testing.T) {
	a, out, _, _ := testApp(t)
	writeClientConfigs(t, a)
	err := a.Run([]string{"mcp", "audit", "--secrets", "--json"})
	if ExitCode(err) != ExitOperational {
		t.Fatalf("err=%v", err)
	}
	var envelope struct {
		Data struct {
			Findings []mcpSecretFinding `json:"findings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	got := []string{}
	for _, f := range envelope.Data.Findings {
		got = append(got, f.Client+" "+f.Scope+" "+f.Server+" "+f.Location)
	}
	want := []string{
		"claude local:/work/app grafana env.GRAFANA_TOKEN",
		"claude user apikey headers.X-API-Key",
		"claude user argtoken args[1]",
		"claude user docker args[2]",
		"claude user gitlab env.GITLAB_TOKEN",
		"claude user jira headers.Authorization",
		"claude user remote args[3]",
		"claude user sse headers.Authorization",
		"codex user aws env.AWS_SECRET_ACCESS_KEY",
		"codex user slack env.SLACK_BOT_TOKEN",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findings=%q\nwant=%q", got, want)
	}
	for _, value := range []string{"glpat-plaintextvalue", "jira-plaintext", "abc-plaintext", "glsa_plaintext", "xoxb-plaintext", "literal-secret", "remote-plaintext", "docker-plaintext", "hdr-plaintext"} {
		if strings.Contains(out.String(), value) {
			t.Fatalf("audit printed %s: %s", value, out.String())
		}
	}
}

func TestMCPAuditSecretsReportsUnparsableConfigInsteadOfClean(t *testing.T) {
	a, out, _, _ := testApp(t)
	_, codexHome := writeClientConfigs(t, a)
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("[mcp_servers.x\ncommand = 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"mcp", "audit", "--secrets"}); ExitCode(err) != ExitOperational {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(out.String(), "could not parse") {
		t.Fatalf("out=%s", out.String())
	}
}

func TestMCPAdoptMovesValuesIntoSecAndResyncs(t *testing.T) {
	a, out, _, _ := testApp(t)
	seedSecretStore(t, a, "other", map[string]string{"x": "y"})
	writeClientConfigs(t, a)
	if err := a.Run([]string{"mcp", "adopt", "claude", "gitlab", "--service", "gitlab", "--yes"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("adopt without a terminal err=%v", err)
	}
	a.env = append(a.env, secretOutputOverrideEnv+"=1")
	a.lookPath = func(name string) (string, error) { return "/test/" + name, nil }
	var requests [][]string
	// Capture client CLI calls only; age keeps running through the helper.
	capture, helper := outputCommand("", &requests), a.command
	a.command = func(name string, args ...string) *exec.Cmd {
		if name == "claude" || name == "codex" {
			return capture(name, args...)
		}
		return helper(name, args...)
	}
	if err := a.Run([]string{"mcp", "adopt", "claude", "gitlab", "--service", "gitlab", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "glpat-plaintextvalue") {
		t.Fatalf("adopt printed the value: %s", out.String())
	}
	store, _, err := a.loadMCP()
	if err != nil {
		t.Fatal(err)
	}
	server := store.Servers["gitlab"]
	if server.Command != "gitlab-mcp" || !reflect.DeepEqual(server.Args, []string{"--read-only"}) ||
		server.SecretEnv["GITLAB_TOKEN"] != "sec://gitlab/GITLAB_TOKEN" || server.SecretEnv["GITLAB_URL"] != "sec://gitlab/GITLAB_URL" ||
		!reflect.DeepEqual(server.EnvVars, []string{"HOME"}) || !reflect.DeepEqual(server.Targets, []string{"claude"}) {
		t.Fatalf("registered=%+v", server)
	}
	data, err := a.readSecrets()
	if err != nil {
		t.Fatal(err)
	}
	if data["gitlab"]["GITLAB_TOKEN"] != "glpat-plaintextvalue" || data["gitlab"]["GITLAB_URL"] != "https://git.example.test" {
		t.Fatalf("store=%v", data["gitlab"])
	}
	wantSync := [][]string{
		{"claude", "mcp", "remove", "--scope", "user", "gitlab"},
		{"claude", "mcp", "add", "--scope", "user", "gitlab", "--", "/test/bb", "mcp", "launch", "gitlab"},
	}
	if !reflect.DeepEqual(requests, wantSync) {
		t.Fatalf("requests=%q", requests)
	}

	requests = nil
	if err := a.Run([]string{"mcp", "adopt", "claude", "jira", "--service", "jira", "--yes"}); err != nil {
		t.Fatal(err)
	}
	store, _, _ = a.loadMCP()
	if store.Servers["jira"].BearerTokenSecret != "sec://jira/token" || len(requests) != 2 || requests[1][2] != "add-json" {
		t.Fatalf("jira=%+v requests=%q", store.Servers["jira"], requests)
	}

	requests = nil
	if err := a.Run([]string{"mcp", "adopt", "codex", "slack", "--service", "slack", "--yes"}); err != nil {
		t.Fatal(err)
	}
	data, _ = a.readSecrets()
	if data["slack"]["SLACK_BOT_TOKEN"] != "xoxb-plaintext" || data["slack"]["SLACK_TEAM"] != "T1" {
		t.Fatalf("slack=%v", data["slack"])
	}

	for _, argv := range [][]string{
		{"mcp", "adopt", "claude", "argtoken", "--service", "s1", "--yes"}, // token in args
		{"mcp", "adopt", "claude", "grafana", "--service", "s2", "--yes"},  // local scope
		{"mcp", "adopt", "claude", "gitlab", "--service", "s3", "--yes"},   // already in bb
		{"mcp", "adopt", "claude", "sse", "--service", "s4", "--yes"},      // SSE transport
		{"mcp", "adopt", "codex", "aws", "--service", "s5", "--yes"},       // would drop startup_timeout_sec
	} {
		if err := a.Run(argv); ExitCode(err) != ExitInvalidInvocation {
			t.Fatalf("%v err=%v", argv, err)
		}
	}
	if err := a.Run([]string{"mcp", "adopt", "codex", "aws", "--service", "aws", "--drop-unmodeled", "--yes"}); err != nil {
		t.Fatal(err)
	}
	// Fields whose environment names collide after upper-casing are refused.
	a.in = strings.NewReader("x\n")
	if err := a.Run([]string{"sec", "set", "slackish", "slack_bot_token"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"mcp", "rm", "slack", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"mcp", "adopt", "codex", "slack", "--service", "slackish", "--yes"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("colliding adopt err=%v", err)
	}
	// An existing field is never overwritten.
	if err := a.Run([]string{"mcp", "adopt", "codex", "slack", "--service", "slack", "--yes"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("overwriting adopt err=%v", err)
	}
}
