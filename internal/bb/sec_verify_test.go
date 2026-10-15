package bb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// fakeIssuer answers the identity endpoints of every supported kind, accepting
// only the token "good-token" (and user "alice" for Jenkins).
func fakeIssuer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/user":
			if r.Header.Get("Private-Token") != "good-token" {
				http.Error(w, `{"message":"401 Unauthorized good-token"}`, http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"username":"gl-user"}`))
		case "/api/v4/personal_access_tokens/self":
			w.Write([]byte(`{"name":"ci","expires_at":"2026-09-01"}`))
		case "/user":
			if r.Header.Get("Authorization") != "Bearer good-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("GitHub-Authentication-Token-Expiration", "2026-12-24 09:00:00 +0900")
			w.Write([]byte(`{"login":"octo"}`))
		case "/me/api/json":
			user, token, ok := r.BasicAuth()
			if !ok || user != "alice" || token != "good-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"id":"alice"}`))
		case "/api/v2/me/":
			if r.Header.Get("Authorization") != "Bearer good-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"results":[{"username":"awx-admin"}]}`))
		case "/redirect/api/v4/user":
			http.Redirect(w, r, "https://elsewhere.test/api/v4/user", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSecVerifyAdaptersReportIdentityAndExpiry(t *testing.T) {
	a, out, _, _ := secTestApp(t)
	issuer := fakeIssuer(t)
	secSet(t, a, "good-token", "add", "gitlab/token")
	secSet(t, a, "good-token", "add", "github/token")
	secSet(t, a, "good-token", "add", "jenkins/token")
	secSet(t, a, "alice", "add", "jenkins/user")
	secSet(t, a, "good-token", "add", "awx/token")
	secSet(t, a, "bad-token", "add", "broken/token")
	for _, argv := range [][]string{
		{"sec", "meta", "gitlab/token", "--verify", "gitlab=" + issuer.URL + "/"},
		{"sec", "meta", "github/token", "--verify", "github=" + issuer.URL},
		{"sec", "meta", "jenkins/token", "--verify", "jenkins=" + issuer.URL, "--verify-user", "user"},
		{"sec", "meta", "awx/token", "--verify", "awx=" + issuer.URL},
		{"sec", "meta", "broken/token", "--verify", "gitlab=" + issuer.URL},
	} {
		if err := a.Run(argv); err != nil {
			t.Fatalf("%v: %v", argv, err)
		}
	}
	out.Reset()
	err := a.Run([]string{"sec", "verify", "--json"})
	if ExitCode(err) != ExitOperational {
		t.Fatalf("one failure must exit 1: %v", err)
	}
	var envelope struct {
		Data []secretVerifyResult `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	got := map[string]secretVerifyResult{}
	for _, result := range envelope.Data {
		got[result.Key] = result
	}
	if r := got["gitlab/token"]; !r.OK || r.Identity != "gl-user (token ci)" || r.ExpiresAt != "2026-09-01" {
		t.Fatalf("gitlab=%+v", r)
	}
	if r := got["github/token"]; !r.OK || r.Identity != "octo" || r.ExpiresAt != "2026-12-24" {
		t.Fatalf("github=%+v", r)
	}
	if r := got["jenkins/token"]; !r.OK || r.Identity != "alice" {
		t.Fatalf("jenkins=%+v", r)
	}
	if r := got["awx/token"]; !r.OK || r.Identity != "awx-admin" {
		t.Fatalf("awx=%+v", r)
	}
	// The failure names the status, not the body that echoed the token.
	if r := got["broken/token"]; r.OK || r.Error != "HTTP 401" || r.Status != 401 {
		t.Fatalf("broken=%+v", r)
	}
	if strings.Contains(out.String(), "good-token") || strings.Contains(out.String(), "bad-token") {
		t.Fatalf("output carried a token: %s", out.String())
	}
	meta := readMetaFile(t, a)
	if !strings.Contains(meta, `"expires_at": "2026-09-01"`) || !strings.Contains(meta, `"expires_source": "verify"`) {
		t.Fatalf("reported expiry not recorded: %s", meta)
	}

	out.Reset()
	if err := a.Run([]string{"sec", "verify", "gitlab/token"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "gitlab/token") || strings.Contains(out.String(), "github") {
		t.Fatalf("single verify=%s", out.String())
	}
}

func TestSecVerifyTargetIsOperatorOnlyAndHTTPSOnly(t *testing.T) {
	a, _, _, _ := secTestApp(t)
	secSet(t, a, "good-token", "add", "gitlab/token")
	for _, target := range []string{"gitlab=http://git.example.test", "gitlab", "gitlab=https://u:p@git.example.test", "vault=https://x.test", "jenkins=https://j.test"} {
		if err := a.Run([]string{"sec", "meta", "gitlab/token", "--verify", target}); ExitCode(err) != ExitInvalidInvocation {
			t.Fatalf("%s err=%v", target, err)
		}
	}
	// Without a terminal or the explicit override, nobody can redirect where
	// the token goes.
	kept := []string{}
	for _, entry := range a.env {
		if !strings.HasPrefix(entry, secretOutputOverrideEnv+"=") {
			kept = append(kept, entry)
		}
	}
	a.env = kept
	err := a.Run([]string{"sec", "meta", "gitlab/token", "--verify", "gitlab=https://git.example.test"})
	if ExitCode(err) != ExitInvalidInvocation || !strings.Contains(err.Error(), "requires a terminal") {
		t.Fatalf("agent-set verify target err=%v", err)
	}
}

func TestSecVerifyRefusesRedirects(t *testing.T) {
	a, out, _, _ := secTestApp(t)
	issuer := fakeIssuer(t)
	secSet(t, a, "good-token", "add", "gitlab/token")
	if err := a.Run([]string{"sec", "meta", "gitlab/token", "--verify", "gitlab=" + issuer.URL + "/redirect"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "verify", "gitlab/token", "--json"}); ExitCode(err) != ExitOperational {
		t.Fatalf("redirect err=%v", err)
	}
	if !strings.Contains(out.String(), "HTTP 302") {
		t.Fatalf("redirect result=%s", out.String())
	}
}

func TestSecRotateVerifiesTheNewValue(t *testing.T) {
	a, _, stderr, _ := secTestApp(t)
	issuer := fakeIssuer(t)
	secSet(t, a, "old-token", "add", "gitlab/token")
	if err := a.Run([]string{"sec", "meta", "gitlab/token", "--verify", "gitlab=" + issuer.URL}); err != nil {
		t.Fatal(err)
	}
	secSet(t, a, "good-token", "rotate", "gitlab/token")
	if !strings.Contains(stderr.String(), "Verified as gl-user") || !strings.Contains(stderr.String(), "Expiry 2026-09-01 recorded") {
		t.Fatalf("stderr=%q", stderr.String())
	}
	stderr.Reset()
	secSet(t, a, "bad-token", "rotate", "gitlab/token")
	if !strings.Contains(stderr.String(), "failed verification (HTTP 401)") || !strings.Contains(stderr.String(), "--undo") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestMCPServeSecVerifyReturnsNoToken(t *testing.T) {
	a, _, _, _ := secTestApp(t)
	issuer := fakeIssuer(t)
	secSet(t, a, "good-token", "add", "gitlab/token")
	if err := a.Run([]string{"sec", "meta", "gitlab/token", "--verify", "gitlab=" + issuer.URL}); err != nil {
		t.Fatal(err)
	}
	responses := runMCPServe(t, a, toolCall(1, "sec_verify", `{"service":"gitlab"}`), toolCall(2, "sec_verify", `{"field":"token"}`))
	results := toolResult(t, responses[0])["results"].([]any)
	if first := results[0].(map[string]any); first["ok"] != true || first["identity"] != "gl-user (token ci)" {
		t.Fatalf("sec_verify=%v", results)
	}
	toolErrorText(t, responses[1])
	encoded, _ := json.Marshal(responses)
	if strings.Contains(string(encoded), "good-token") {
		t.Fatalf("response carried the token: %s", encoded)
	}
}

func TestSecVerifyRefusesTargetsWrittenOutsideBB(t *testing.T) {
	a, out, _, _ := secTestApp(t)
	issuer := fakeIssuer(t)
	secSet(t, a, "good-token", "add", "gitlab/token")
	secSet(t, a, "aws-secret", "add", "aws/key")
	if err := a.Run([]string{"sec", "meta", "gitlab/token", "--verify", "gitlab=" + issuer.URL}); err != nil {
		t.Fatal(err)
	}
	// An agent edits the plaintext metadata: it points the signed gitlab target
	// elsewhere and adds a target for a field that never had one.
	path := a.secMetaPath()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file secretMetaFile
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	gitlab := file.Entries["gitlab/token"]
	moved := *gitlab.Verify
	moved.URL = issuer.URL + "/redirect"
	gitlab.Verify = &moved
	file.Entries["gitlab/token"] = gitlab
	file.Entries["aws/key"] = secretMeta{Verify: &secretVerify{Kind: "github", URL: "http://evil.example.test", MAC: moved.MAC}}
	encoded, _ := json.Marshal(file)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	var requests int
	a.httpDo = func(r *http.Request) (*http.Response, error) {
		requests++
		return nil, http.ErrHandlerTimeout
	}
	out.Reset()
	if err := a.Run([]string{"sec", "verify", "--json"}); ExitCode(err) != ExitOperational {
		t.Fatalf("err=%v", err)
	}
	if requests != 0 || strings.Count(out.String(), "was not set by bb sec meta") != 1 || !strings.Contains(out.String(), "invalid verify target") {
		t.Fatalf("requests=%d out=%s", requests, out.String())
	}
}

func TestSecImportForceKeepsReplacedValues(t *testing.T) {
	a, out, stderr, _ := secTestApp(t)
	secSet(t, a, "live-token", "add", "app/API_TOKEN", "--expires", "30d")
	a.in = strings.NewReader("API_TOKEN=\"typo\" # wrong\n")
	if err := a.Run([]string{"sec", "import", "-", "--service", "app", "--force"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "Replaced app/API_TOKEN") || strings.Contains(readMetaFile(t, a), "expires_at") {
		t.Fatalf("stderr=%q meta=%s", stderr.String(), readMetaFile(t, a))
	}
	if err := a.Run([]string{"sec", "rotate", "app/API_TOKEN", "--undo"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "get", "app/API_TOKEN"}); err != nil || strings.TrimSpace(out.String()) != "live-token" {
		t.Fatalf("after undo=%q err=%v", out.String(), err)
	}
	for _, bad := range []string{"K=\"open\n", "K='a' b\n"} {
		a.in = strings.NewReader(bad)
		if err := a.Run([]string{"sec", "import", "-", "--service", "x"}); ExitCode(err) != ExitInvalidInvocation {
			t.Fatalf("%q err=%v", bad, err)
		}
	}
}
