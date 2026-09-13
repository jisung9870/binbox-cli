package bb

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// attachTerminal makes bb look like it is talking to a human. stdin and stderr
// are what the gate inspects, so both become the same terminal-ish file.
func attachTerminal(t *testing.T, a *App) {
	t.Helper()
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { devnull.Close() })
	a.in, a.err = devnull, devnull
	a.isTerminal = func(uintptr) bool { return true }
}

// seedSecretStore creates a store with the given service fields and leaves the
// app configured against the fake age helper, without the output override.
func seedSecretStore(t *testing.T, a *App, service string, fields map[string]string) {
	t.Helper()
	enableSecStoreHelper(a, t.TempDir())
	if err := a.Run([]string{"sec", "init"}); err != nil {
		t.Fatal(err)
	}
	for field, value := range fields {
		a.in = strings.NewReader(value + "\n")
		if err := a.Run([]string{"sec", "set", service, field}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSecretOutputGateRequiresATerminal(t *testing.T) {
	a, out, _, _ := testApp(t)
	seedSecretStore(t, a, "svc", map[string]string{"token": "topsecret-value"})

	for _, argv := range [][]string{{"sec", "get", "svc", "token"}, {"sec", "env", "svc"}, {"sec", "copy", "svc", "token"}} {
		out.Reset()
		a.in = strings.NewReader("")
		if err := a.Run(argv); ExitCode(err) != ExitInvalidInvocation {
			t.Fatalf("%v err=%v, want an invalid invocation", argv, err)
		}
		if strings.Contains(out.String(), "topsecret-value") {
			t.Fatalf("%v leaked the value into stdout: %q", argv, out.String())
		}
	}

	// The same command succeeds for an operator at a terminal.
	attachTerminal(t, a)
	out.Reset()
	if err := a.Run([]string{"sec", "get", "svc", "token"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "topsecret-value" {
		t.Fatalf("terminal read=%q", got)
	}
}

func TestSecretOutputGateHasAnExplicitAutomationOverride(t *testing.T) {
	a, out, _, _ := testApp(t)
	seedSecretStore(t, a, "svc", map[string]string{"token": "topsecret-value"})
	a.env = append(a.env, secretOutputOverrideEnv+"=1")
	a.in = strings.NewReader("")
	if err := a.Run([]string{"sec", "get", "svc", "token"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "topsecret-value" {
		t.Fatalf("override read=%q", got)
	}
}

func TestWenvGateAppliesOnlyToPresetsThatReferenceSecrets(t *testing.T) {
	a, out, _, _ := testApp(t)
	seedSecretStore(t, a, "awx", map[string]string{"w-token": "controller-secret"})
	if err := a.Run([]string{"wenv", "set", "plain", "AWS_PROFILE=dev"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"wenv", "set", "awx", "CONTROLLER_OAUTH_TOKEN=sec://awx/w-token"}); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	a.in = strings.NewReader("")
	if err := a.Run([]string{"wenv", "export", "plain"}); err != nil {
		t.Fatalf("secret-free preset was gated: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "export AWS_PROFILE='dev'" {
		t.Fatalf("plain export=%q", got)
	}

	out.Reset()
	if err := a.Run([]string{"wenv", "export", "awx"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("referencing preset err=%v, want an invalid invocation", err)
	}
	if strings.Contains(out.String(), "controller-secret") {
		t.Fatalf("gated export leaked: %q", out.String())
	}
}

// runMCPServe feeds newline-delimited JSON-RPC requests through bb mcp serve and
// returns the decoded responses.
func runMCPServe(t *testing.T, a *App, requests ...string) []map[string]any {
	t.Helper()
	out := new(bytes.Buffer)
	a.in = strings.NewReader(strings.Join(requests, "\n") + "\n")
	a.out = out
	if err := a.Run([]string{"mcp", "serve"}); err != nil {
		t.Fatalf("mcp serve: %v", err)
	}
	responses := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("decode response %q: %v", line, err)
		}
		responses = append(responses, decoded)
	}
	return responses
}

func toolCall(id int, name, arguments string) string {
	return `{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"tools/call","params":{"name":"` + name + `","arguments":` + arguments + `}}`
}

// toolResult returns the structured payload of a successful tool call and fails
// when the call reported an error.
func toolResult(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("response has no result: %v", response)
	}
	if isError, _ := result["isError"].(bool); isError {
		t.Fatalf("tool reported an error: %v", result["content"])
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("result has no structured content: %v", result)
	}
	return structured
}

func toolErrorText(t *testing.T, response map[string]any) string {
	t.Helper()
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("response has no result: %v", response)
	}
	if isError, _ := result["isError"].(bool); !isError {
		t.Fatalf("tool did not report an error: %v", result)
	}
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	return text
}

func TestMCPServeNegotiatesProtocolAndListsNoValueReturningTool(t *testing.T) {
	a, _, _, _ := testApp(t)
	responses := runMCPServe(t, a,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	if len(responses) != 2 {
		t.Fatalf("notifications must not be answered: %d responses", len(responses))
	}
	initialize := responses[0]["result"].(map[string]any)
	if initialize["protocolVersion"] != "2025-03-26" {
		t.Fatalf("protocolVersion=%v", initialize["protocolVersion"])
	}
	if !strings.Contains(initialize["instructions"].(string), "never returned") {
		t.Fatalf("instructions do not state the contract: %v", initialize["instructions"])
	}

	tools := responses[1]["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, item := range tools {
		names[item.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"sec_list", "sec_ref", "wenv_list", "wenv_show", "wenv_set", "sec_exec", "wenv_exec"} {
		if !names[want] {
			t.Fatalf("tool %s missing from %v", want, names)
		}
	}
	// The entire design rests on there being no way to ask for a value.
	for _, forbidden := range []string{"sec_get", "sec_env", "wenv_export", "wenv_apply"} {
		if names[forbidden] {
			t.Fatalf("value-returning tool %s is exposed", forbidden)
		}
	}
}

func TestMCPServeReturnsReferencesAndNeverValues(t *testing.T) {
	a, _, _, _ := testApp(t)
	seedSecretStore(t, a, "awx", map[string]string{"w-token": "controller-secret"})
	if err := a.Run([]string{"wenv", "set", "awx", "CONTROLLER_HOST=https://at.example.test", "CONTROLLER_OAUTH_TOKEN=sec://awx/w-token"}); err != nil {
		t.Fatal(err)
	}

	responses := runMCPServe(t, a,
		toolCall(1, "sec_list", `{}`),
		toolCall(2, "sec_ref", `{"service":"awx"}`),
		toolCall(3, "wenv_show", `{"preset":"awx"}`),
		toolCall(4, "wenv_list", `{}`),
	)
	if len(responses) != 4 {
		t.Fatalf("responses=%d", len(responses))
	}

	services := toolResult(t, responses[0])["services"].([]any)
	first := services[0].(map[string]any)
	if first["name"] != "awx" || first["fields"].([]any)[0] != "w-token" {
		t.Fatalf("sec_list=%v", first)
	}

	reference := toolResult(t, responses[1])
	if reference["reference"] != "sec://awx/w-token" {
		t.Fatalf("sec_ref=%v", reference)
	}

	variables := toolResult(t, responses[2])["variables"].(map[string]any)
	if variables["CONTROLLER_OAUTH_TOKEN"] != "<secret:awx/w-token>" {
		t.Fatalf("wenv_show resolved a reference: %v", variables)
	}
	if variables["CONTROLLER_HOST"] != "https://at.example.test" {
		t.Fatalf("wenv_show hid a non-secret: %v", variables)
	}

	for index, response := range responses {
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(encoded, []byte("controller-secret")) {
			t.Fatalf("response %d carried the secret value: %s", index, encoded)
		}
	}
}

func TestMCPServeExecIsFailClosedUntilAllowlisted(t *testing.T) {
	a, out, _, state := testApp(t)
	seedSecretStore(t, a, "svc", map[string]string{"token": "topsecret-value"})

	responses := runMCPServe(t, a, toolCall(1, "sec_exec", `{"service":"svc","argv":["envcheck"]}`))
	if got := toolErrorText(t, responses[0]); !strings.Contains(got, "no command is allowlisted") {
		t.Fatalf("empty allowlist error=%q", got)
	}

	a.out = out
	if err := a.Run([]string{"mcp", "allow", "add", "envcheck"}); err != nil {
		t.Fatal(err)
	}
	responses = runMCPServe(t, a, toolCall(1, "sec_exec", `{"service":"svc","argv":["psql"]}`))
	if got := toolErrorText(t, responses[0]); !strings.Contains(got, "not on the MCP exec allowlist") {
		t.Fatalf("off-allowlist error=%q", got)
	}

	// Authorization decides before the store is opened, so an unrunnable command
	// is refused as such even when the service name is also wrong.
	responses = runMCPServe(t, a, toolCall(1, "sec_exec", `{"service":"absent","argv":["psql"]}`))
	if got := toolErrorText(t, responses[0]); !strings.Contains(got, "not on the MCP exec allowlist") {
		t.Fatalf("allowlist must be checked before the store: %q", got)
	}

	audit, err := os.ReadFile(filepath.Join(state, "bb", "mcp-serve-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), `"allowed":false`) || !strings.Contains(string(audit), `"command":"psql"`) {
		t.Fatalf("refusals were not audited: %s", audit)
	}
	if strings.Contains(string(audit), "topsecret-value") {
		t.Fatalf("audit recorded a value: %s", audit)
	}
}

func TestMCPServeSecExecInjectsValuesAndRedactsOutput(t *testing.T) {
	a, out, _, state := testApp(t)
	seedSecretStore(t, a, "svc", map[string]string{"token": "topsecret-value", "user-name": "operator"})
	a.out = out
	if err := a.Run([]string{"mcp", "allow", "add", "envcheck"}); err != nil {
		t.Fatal(err)
	}

	responses := runMCPServe(t, a, toolCall(1, "sec_exec", `{"service":"svc","argv":["envcheck"]}`))
	result := toolResult(t, responses[0])
	// The child saw both values; the response shows neither.
	if result["stdout"] != "***|***|" {
		t.Fatalf("stdout=%q, want both values redacted", result["stdout"])
	}
	if result["redacted"] != true || result["exit_code"].(float64) != 0 {
		t.Fatalf("result=%v", result)
	}
	encoded, err := json.Marshal(responses[0])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("topsecret-value")) || bytes.Contains(encoded, []byte("operator")) {
		t.Fatalf("response carried a value: %s", encoded)
	}

	audit, err := os.ReadFile(filepath.Join(state, "bb", "mcp-serve-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), `"tool":"sec_exec"`) || !strings.Contains(string(audit), `"exit_code":0`) {
		t.Fatalf("audit=%s", audit)
	}
}

func TestWenvExecScopesReferencedValuesToOneChildProcess(t *testing.T) {
	a, out, _, _ := testApp(t)
	seedSecretStore(t, a, "udg", map[string]string{"viewer": "viewer-token", "editor": "editor-token"})
	// A preset maps any environment name onto any field, which is what lets two
	// tokens in one service each become GRAFANA_SERVICE_ACCOUNT_TOKEN in turn.
	if err := a.Run([]string{"wenv", "set", "ro", "SVC_TOKEN=sec://udg/viewer", "KEEP=plain-config"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"wenv", "set", "rw", "SVC_TOKEN=sec://udg/editor"}); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	a.in = strings.NewReader("")
	if err := a.Run([]string{"wenv", "exec", "ro", "--", "envcheck"}); err != nil {
		t.Fatal(err)
	}
	// Only the viewer token reached the child: the editor field of the same
	// service is not injected, because the preset never referenced it.
	if got := out.String(); got != "viewer-token||plain-config" {
		t.Fatalf("ro child saw %q", got)
	}

	out.Reset()
	if err := a.Run([]string{"wenv", "exec", "rw", "--", "envcheck"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "editor-token||" {
		t.Fatalf("rw child saw %q", got)
	}

	// exec prints no value of its own, so it stays usable without a terminal.
	out.Reset()
	if err := a.Run([]string{"wenv", "exec", "missing", "--", "envcheck"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("unknown preset err=%v", err)
	}
	if err := a.Run([]string{"wenv", "exec", "ro"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("missing -- separator err=%v", err)
	}
}

func TestMCPServeWenvExecRedactsOnlyReferencedValues(t *testing.T) {
	a, out, _, _ := testApp(t)
	seedSecretStore(t, a, "svc", map[string]string{"token": "topsecret-value"})
	a.out = out
	if err := a.Run([]string{"wenv", "set", "dev", "SVC_TOKEN=sec://svc/token", "KEEP=plain-config"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"mcp", "allow", "add", "envcheck"}); err != nil {
		t.Fatal(err)
	}

	responses := runMCPServe(t, a, toolCall(1, "wenv_exec", `{"preset":"dev","argv":["envcheck"]}`))
	result := toolResult(t, responses[0])
	// Ordinary configuration stays readable; only the referenced value is hidden.
	if result["stdout"] != "***||plain-config" {
		t.Fatalf("stdout=%q", result["stdout"])
	}
}

func TestMCPServeShortSecretsAreReportedInsteadOfSilentlyExposed(t *testing.T) {
	a, out, _, _ := testApp(t)
	seedSecretStore(t, a, "svc", map[string]string{"token": "ab"})
	a.out = out
	if err := a.Run([]string{"mcp", "allow", "add", "envcheck"}); err != nil {
		t.Fatal(err)
	}
	responses := runMCPServe(t, a, toolCall(1, "sec_exec", `{"service":"svc","argv":["envcheck"]}`))
	result := toolResult(t, responses[0])
	warnings, _ := result["warnings"].([]any)
	if len(warnings) != 1 || !strings.Contains(warnings[0].(string), "SVC_TOKEN") {
		t.Fatalf("warnings=%v", warnings)
	}
	if result["redacted"] != false {
		t.Fatalf("a value below the redaction floor must not claim redaction: %v", result)
	}
}

func TestMCPServeWenvSetMergesAndKeepsThePlaintextBan(t *testing.T) {
	a, out, config, _ := testApp(t)
	a.out = out
	if err := a.Run([]string{"wenv", "set", "dev", "AWS_PROFILE=dev"}); err != nil {
		t.Fatal(err)
	}

	responses := runMCPServe(t, a,
		toolCall(1, "wenv_set", `{"preset":"dev","assignments":["CONTROLLER_OAUTH_TOKEN=sec://awx/w-token"]}`),
		toolCall(2, "wenv_set", `{"preset":"dev","assignments":["CONTROLLER_TOKEN=literal-secret"]}`),
	)
	variables := toolResult(t, responses[0])["variables"].(map[string]any)
	if variables["AWS_PROFILE"] != "dev" {
		t.Fatalf("wenv_set dropped an existing variable: %v", variables)
	}
	if variables["CONTROLLER_OAUTH_TOKEN"] != "<secret:awx/w-token>" {
		t.Fatalf("wenv_set preview=%v", variables)
	}
	if got := toolErrorText(t, responses[1]); !strings.Contains(got, "must not store secret-like variables") {
		t.Fatalf("plaintext secret error=%q", got)
	}

	stored, err := os.ReadFile(filepath.Join(config, "bb", "wenv.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "literal-secret") {
		t.Fatalf("a plaintext secret reached the preset file: %s", stored)
	}
}

func TestMCPServeRejectsUnknownToolsAndMethods(t *testing.T) {
	a, _, _, _ := testApp(t)
	responses := runMCPServe(t, a,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"sec_get","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"completion/complete"}`,
		`not json`,
	)
	if len(responses) != 3 {
		t.Fatalf("responses=%d", len(responses))
	}
	for index, response := range responses {
		failure, ok := response["error"].(map[string]any)
		if !ok {
			t.Fatalf("response %d is not an error: %v", index, response)
		}
		if failure["message"] == "" {
			t.Fatalf("response %d has no message: %v", index, response)
		}
	}
}

func TestMCPExecAllowlistNarrowsByArgumentPrefix(t *testing.T) {
	a, out, _, _ := testApp(t)
	seedSecretStore(t, a, "svc", map[string]string{"token": "topsecret-value"})
	a.out = out
	if err := a.Run([]string{"mcp", "allow", "add", "envcheck", "--", "read"}); err != nil {
		t.Fatal(err)
	}

	responses := runMCPServe(t, a, toolCall(1, "sec_exec", `{"service":"svc","argv":["envcheck","write"]}`))
	got := toolErrorText(t, responses[0])
	if !strings.Contains(got, "arguments are not allowed for envcheck") || !strings.Contains(got, "envcheck read") {
		t.Fatalf("prefix denial=%q", got)
	}

	// The permitted prefix runs, and extra arguments after it are still allowed.
	responses = runMCPServe(t, a, toolCall(1, "sec_exec", `{"service":"svc","argv":["envcheck","read","--verbose"]}`))
	if result := toolResult(t, responses[0]); result["exit_code"].(float64) != 0 {
		t.Fatalf("permitted prefix result=%v", result)
	}
}

func TestMCPExecAllowlistRefusesMixingBroadAndNarrowRules(t *testing.T) {
	a, _, _, _ := testApp(t)
	if err := a.Run([]string{"mcp", "allow", "add", "aws"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"mcp", "allow", "add", "aws", "--", "sts"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("narrowing an unrestricted entry err=%v", err)
	}
	if err := a.Run([]string{"mcp", "allow", "rm", "aws"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"mcp", "allow", "add", "aws", "--", "sts"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"mcp", "allow", "add", "aws"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("broadening a restricted entry err=%v", err)
	}
	// Removing without "--" revokes every prefix for that command at once.
	if err := a.Run([]string{"mcp", "allow", "add", "aws", "--", "s3", "ls"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"mcp", "allow", "rm", "aws"}); err != nil {
		t.Fatal(err)
	}
	config, err := a.loadMCPServeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(config.ExecRules) != 0 {
		t.Fatalf("rules survived a command-wide removal: %v", config.ExecRules)
	}
}

func TestMCPExecScopesNarrowServicesAndPresets(t *testing.T) {
	a, out, _, _ := testApp(t)
	seedSecretStore(t, a, "svc", map[string]string{"token": "topsecret-value"})
	a.out = out
	if err := a.Run([]string{"wenv", "set", "dev", "KEEP=plain-config"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"mcp", "allow", "add", "envcheck"}); err != nil {
		t.Fatal(err)
	}

	// An empty scope leaves the command allowlist as the only gate.
	responses := runMCPServe(t, a, toolCall(1, "sec_exec", `{"service":"svc","argv":["envcheck"]}`))
	if result := toolResult(t, responses[0]); result["exit_code"].(float64) != 0 {
		t.Fatalf("unscoped run=%v", result)
	}

	if err := a.Run([]string{"mcp", "allow", "service", "add", "other"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"mcp", "allow", "preset", "add", "prod"}); err != nil {
		t.Fatal(err)
	}
	responses = runMCPServe(t, a,
		toolCall(1, "sec_exec", `{"service":"svc","argv":["envcheck"]}`),
		toolCall(2, "wenv_exec", `{"preset":"dev","argv":["envcheck"]}`),
	)
	if got := toolErrorText(t, responses[0]); !strings.Contains(got, "secret service is outside the MCP exec scope") {
		t.Fatalf("service scope denial=%q", got)
	}
	if got := toolErrorText(t, responses[1]); !strings.Contains(got, "wenv preset is outside the MCP exec scope") {
		t.Fatalf("preset scope denial=%q", got)
	}

	if err := a.Run([]string{"mcp", "allow", "service", "add", "svc"}); err != nil {
		t.Fatal(err)
	}
	responses = runMCPServe(t, a, toolCall(1, "sec_exec", `{"service":"svc","argv":["envcheck"]}`))
	if result := toolResult(t, responses[0]); result["exit_code"].(float64) != 0 {
		t.Fatalf("in-scope run=%v", result)
	}
}

func TestMCPAllowlistCRUD(t *testing.T) {
	a, out, config, _ := testApp(t)
	if err := a.Run([]string{"mcp", "allow", "add", "psql", "aws"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"mcp", "allow", "add", "../evil"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("path-like entry err=%v", err)
	}
	out.Reset()
	if err := a.Run([]string{"mcp", "allow", "--json"}); err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Data struct {
			ExecCommands   []string `json:"exec_commands"`
			ExecEnabled    bool     `json:"exec_enabled"`
			ServicesScoped bool     `json:"services_scoped"`
			PresetsScoped  bool     `json:"presets_scoped"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if strings.Join(listed.Data.ExecCommands, ",") != "aws,psql" || !listed.Data.ExecEnabled {
		t.Fatalf("listed=%+v", listed.Data)
	}
	// An unscoped allowlist must say so rather than reading as narrowed.
	if listed.Data.ServicesScoped || listed.Data.PresetsScoped {
		t.Fatalf("empty scopes reported as scoped: %+v", listed.Data)
	}

	if err := a.Run([]string{"mcp", "allow", "rm", "psql"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"mcp", "allow", "rm", "psql"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("repeat removal err=%v", err)
	}
	stored, err := os.ReadFile(filepath.Join(config, "bb", "mcp-serve.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "psql") {
		t.Fatalf("removal did not persist: %s", stored)
	}
}
