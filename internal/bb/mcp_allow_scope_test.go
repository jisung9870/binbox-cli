package bb

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMCPAllowBindsACommandToItsOwnServices(t *testing.T) {
	a, out, _, _ := testApp(t)
	seedSecretStore(t, a, "gitlab", map[string]string{"token": "glpat-secret"})
	a.in = strings.NewReader("aws-secret\n")
	if err := a.Run([]string{"sec", "set", "aws", "key"}); err != nil {
		t.Fatal(err)
	}
	a.out = out
	if err := a.Run([]string{"mcp", "allow", "add", "envcheck", "--service", "gitlab"}); err != nil {
		t.Fatal(err)
	}
	responses := runMCPServe(t, a,
		toolCall(1, "sec_exec", `{"service":"gitlab","argv":["envcheck"]}`),
		toolCall(2, "sec_exec", `{"service":"aws","argv":["envcheck"]}`),
		toolCall(3, "wenv_exec", `{"preset":"any","argv":["envcheck"]}`),
	)
	if result := toolResult(t, responses[0]); result["exit_code"].(float64) != 0 {
		t.Fatalf("bound service run=%v", result)
	}
	if got := toolErrorText(t, responses[1]); !strings.Contains(got, "envcheck is not bound to secret service aws") || !strings.Contains(got, "services: gitlab") {
		t.Fatalf("other service denial=%q", got)
	}
	// A rule bound only to services runs with no preset at all.
	if got := toolErrorText(t, responses[2]); !strings.Contains(got, "not bound to wenv preset any") {
		t.Fatalf("preset denial=%q", got)
	}

	// Binding an unbound rule, or unbinding a bound one, is an explicit step.
	if err := a.Run([]string{"mcp", "allow", "add", "envcheck"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("widening a bound rule err=%v", err)
	}
	if err := a.Run([]string{"mcp", "allow", "add", "envcheck", "--service", "aws"}); err != nil {
		t.Fatal(err)
	}
	config, err := a.loadMCPServeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(config.ExecRules) != 1 || strings.Join(config.ExecRules[0].Services, ",") != "aws,gitlab" {
		t.Fatalf("rules=%+v", config.ExecRules)
	}

	// Taking away bindings never widens: the last one removes the rule.
	if err := a.Run([]string{"mcp", "allow", "rm", "envcheck", "--service", "gitlab"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"mcp", "allow", "rm", "envcheck", "--service", "aws"}); err != nil {
		t.Fatal(err)
	}
	config, err = a.loadMCPServeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(config.ExecRules) != 0 {
		t.Fatalf("an unbound rule survived its last binding: %+v", config.ExecRules)
	}

	if err := a.Run([]string{"mcp", "allow", "add", "envcheck"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"mcp", "allow", "add", "envcheck", "--service", "gitlab"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("binding an unbound rule err=%v", err)
	}
	if err := a.Run([]string{"mcp", "allow", "add", "envcheck", "--service", "../x"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("bad service name err=%v", err)
	}
}

func TestMCPAllowBundlesAddReadOnlyPrefixes(t *testing.T) {
	a, out, _, _ := testApp(t)
	if err := a.Run([]string{"mcp", "allow", "add", "--bundle", "glab-read", "--service", "gitlab"}); err != nil {
		t.Fatal(err)
	}
	config, err := a.loadMCPServeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !config.permits("sec_exec", "gitlab", []string{"glab", "mr", "list", "--assignee=@me"}) {
		t.Fatalf("bundle rule missing: %+v", config.ExecRules)
	}
	// Read-only prefixes still refuse being pointed at another host, which
	// would send the token there, and the token-printing auth status.
	for _, argv := range [][]string{
		{"glab", "mr", "merge", "1"}, {"glab", "api", "projects"},
		{"glab", "mr", "view", "-R", "https://evil.test/g/r", "1"},
		{"glab", "mr", "view", "-Revil.test/g/r", "1"},
		{"glab", "mr", "view", "--repo=evil.test/g/r", "1"},
		{"glab", "mr", "view", "https://evil.test/g/r/-/merge_requests/1"},
		{"glab", "auth", "status", "--show-token"},
	} {
		if config.permits("sec_exec", "gitlab", argv) {
			t.Fatalf("bundle allowed %q", argv)
		}
	}
	if config.permits("sec_exec", "aws", []string{"glab", "mr", "list"}) {
		t.Fatal("bundle ignored its service binding")
	}
	if err := a.Run([]string{"mcp", "allow", "add", "--bundle", "gh-read"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("unbound bundle err=%v", err)
	}
	if err := a.Run([]string{"mcp", "allow", "add", "--bundle", "nope", "--service", "x"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("unknown bundle err=%v", err)
	}
	// A bare "glab" cannot sit beside the bundle's narrow rules.
	if err := a.Run([]string{"mcp", "allow", "add", "glab"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("broad rule beside bundle err=%v", err)
	}
	out.Reset()
	if err := a.Run([]string{"mcp", "allow", "bundles"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "gh-read") || !strings.Contains(out.String(), "glab mr view") {
		t.Fatalf("bundles=%s", out.String())
	}
	out.Reset()
	if err := a.Run([]string{"mcp", "allow"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "glab mr list [services: gitlab; deny:") {
		t.Fatalf("listing hides bindings: %s", out.String())
	}
}

func TestMCPServeReportsExpiryWithoutValues(t *testing.T) {
	a, out, _, _ := testApp(t)
	seedSecretStore(t, a, "gitlab", map[string]string{"token": "glpat-secret", "url": "https://git.example.test"})
	a.out = out
	if err := a.Run([]string{"sec", "meta", "gitlab/token", "--expires", "5d", "--url", "https://git.example.test/-/user_settings/personal_access_tokens"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"mcp", "allow", "add", "envcheck"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"wenv", "set", "gl", "GITLAB_TOKEN=sec://gitlab/token"}); err != nil {
		t.Fatal(err)
	}
	responses := runMCPServe(t, a,
		toolCall(1, "sec_list", `{"service":"gitlab"}`),
		toolCall(2, "sec_check", `{}`),
		toolCall(3, "sec_check", `{"within_days":2}`),
		toolCall(4, "sec_exec", `{"service":"gitlab","argv":["envcheck"]}`),
		toolCall(5, "wenv_exec", `{"preset":"gl","argv":["envcheck"]}`),
	)
	listed := toolResult(t, responses[0])
	expiry := listed["expiry"].(map[string]any)
	token := expiry["token"].(map[string]any)
	if token["state"] != "expiring" || token["days_left"].(float64) != 5 || expiry["url"] != nil {
		t.Fatalf("sec_list expiry=%v", expiry)
	}
	check := toolResult(t, responses[1])
	entries := check["entries"].([]any)
	if check["problems"].(float64) != 1 || len(entries) != 1 || !strings.Contains(entries[0].(map[string]any)["renew_url"].(string), "personal_access_tokens") {
		t.Fatalf("sec_check=%v", check)
	}
	if narrow := toolResult(t, responses[2]); narrow["problems"].(float64) != 0 {
		t.Fatalf("sec_check within 2=%v", narrow)
	}
	for _, response := range responses[3:] {
		warnings := toolResult(t, response)["warnings"].([]any)
		if len(warnings) == 0 || !strings.Contains(warnings[0].(string), "gitlab/token (D-5 !)") {
			t.Fatalf("exec warnings=%v", warnings)
		}
	}
	for index, response := range responses {
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "glpat-secret") {
			t.Fatalf("response %d carried a value: %s", index, encoded)
		}
	}
}

func TestMCPAllowRefusesABroaderRuleThatMasksABinding(t *testing.T) {
	a, _, _, _ := testApp(t)
	if err := a.Run([]string{"mcp", "allow", "add", "glab", "--", "mr"}); err != nil {
		t.Fatal(err)
	}
	// "glab mr" already runs with every service, so binding "glab mr view"
	// would list a restriction that does not exist.
	if err := a.Run([]string{"mcp", "allow", "add", "glab", "--service", "gitlab", "--", "mr", "view"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("masked binding err=%v", err)
	}
	if err := a.Run([]string{"mcp", "allow", "rm", "glab"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"mcp", "allow", "add", "glab", "--service", "gitlab", "--service", "other", "--", "mr"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"mcp", "allow", "add", "glab", "--service", "gitlab", "--", "mr", "view"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("broader targets on a covering rule err=%v", err)
	}
	if err := a.Run([]string{"mcp", "allow", "add", "glab", "--service", "gitlab", "--service", "other", "--", "mr", "view"}); err != nil {
		t.Fatalf("matching bindings err=%v", err)
	}
	// Unbinding the narrower rule alone would leave it masked.
	if err := a.Run([]string{"mcp", "allow", "rm", "glab", "--service", "other", "--", "mr", "view"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("masking unbind err=%v", err)
	}
	if err := a.Run([]string{"mcp", "allow", "rm", "glab", "--service", "other"}); err != nil {
		t.Fatalf("command-wide unbind err=%v", err)
	}
}
