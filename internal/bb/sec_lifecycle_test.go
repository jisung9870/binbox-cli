package bb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSecWhereFindsReferencesAndRmWarns(t *testing.T) {
	a, out, stderr, _ := secTestApp(t)
	secSet(t, a, "glpat-1", "add", "gitlab/token")
	secSet(t, a, "other", "add", "gitlab/user")
	for _, argv := range [][]string{
		{"wenv", "set", "gl", "GITLAB_TOKEN=sec://gitlab/token", "HOST=x"},
		{"mcp", "add", "gitlab", "--stdio", "gitlab-mcp", "--secret-env", "GITLAB_TOKEN=gitlab/token"},
		{"mcp", "allow", "add", "glab", "--service", "gitlab"},
	} {
		if err := a.Run(argv); err != nil {
			t.Fatalf("%v: %v", argv, err)
		}
	}
	out.Reset()
	if err := a.Run([]string{"sec", "where", "gitlab/token"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"wenv  ", "gl", "GITLAB_TOKEN", "mcp  ", "gitlab", "mcp-allow", "glab"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("where missing %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if err := a.Run([]string{"sec", "where", "gitlab/user"}); err != nil {
		t.Fatal(err)
	}
	// A field nobody names still shows the service-wide exec binding.
	if strings.Contains(out.String(), "wenv") || !strings.Contains(out.String(), "mcp-allow") {
		t.Fatalf("where gitlab/user:\n%s", out.String())
	}

	stderr.Reset()
	if err := a.Run([]string{"sec", "rm", "gitlab/token"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "still referenced by") || !strings.Contains(stderr.String(), "wenv gl (GITLAB_TOKEN)") {
		t.Fatalf("rm warning=%q", stderr.String())
	}
}

func TestSecImportStoresDotenvWithoutPrintingValues(t *testing.T) {
	a, out, stderr, _ := secTestApp(t)
	envFile := filepath.Join(t.TempDir(), ".env")
	content := "# comment\nexport API_TOKEN=\"tok-secret\\nline2\"\nPLAIN=value # trailing\nQUOTED='a b'\nEMPTY=\n"
	if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"sec", "import", envFile, "--service", "app"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String()+out.String(), "tok-secret") || !strings.Contains(stderr.String(), "Imported 3 field(s) into app: API_TOKEN, PLAIN, QUOTED") {
		t.Fatalf("stderr=%q out=%q", stderr.String(), out.String())
	}
	for field, want := range map[string]string{"API_TOKEN": "tok-secret\nline2", "PLAIN": "value", "QUOTED": "a b"} {
		out.Reset()
		if err := a.Run([]string{"sec", "get", "app/" + field}); err != nil || strings.TrimSuffix(out.String(), "\n") != want {
			t.Fatalf("%s=%q err=%v", field, out.String(), err)
		}
	}
	if err := a.Run([]string{"sec", "import", envFile, "--service", "app"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("conflicting import err=%v", err)
	}
	a.in = strings.NewReader("NEW=1\nOTHER=2\n")
	if err := a.Run([]string{"sec", "import", "-", "--service", "app", "--only", "NEW"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "list", "app"}); err != nil || strings.Contains(out.String(), "OTHER") || !strings.Contains(out.String(), "NEW") {
		t.Fatalf("list=%q err=%v", out.String(), err)
	}
	a.in = strings.NewReader("not a line\n")
	if err := a.Run([]string{"sec", "import", "-", "--service", "bad"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("malformed err=%v", err)
	}
}

func TestSecRotateKeepsThePreviousValueAndUndoSwaps(t *testing.T) {
	a, out, stderr, _ := secTestApp(t)
	secSet(t, a, "old-token", "add", "svc/token", "--expires", "10d", "--url", "https://x.test/tokens")
	secSet(t, a, "new-token", "rotate", "svc/token")
	out.Reset()
	if err := a.Run([]string{"sec", "get", "svc/token"}); err != nil || strings.TrimSpace(out.String()) != "new-token" {
		t.Fatalf("after rotate=%q err=%v", out.String(), err)
	}
	meta := readMetaFile(t, a)
	// The old expiry belonged to the old token; the URL still applies.
	if strings.Contains(meta, "expires_at") || !strings.Contains(meta, "https://x.test/tokens") {
		t.Fatalf("meta after rotate=%s", meta)
	}
	if !strings.Contains(stderr.String(), "No expiry recorded") {
		t.Fatalf("stderr=%q", stderr.String())
	}
	a.in = strings.NewReader("new-token\n")
	if err := a.Run([]string{"sec", "rotate", "svc/token"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("same value err=%v", err)
	}

	if err := a.Run([]string{"sec", "rotate", "svc/token", "--undo"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "get", "svc/token"}); err != nil || strings.TrimSpace(out.String()) != "old-token" {
		t.Fatalf("after undo=%q err=%v", out.String(), err)
	}
	if !strings.Contains(readMetaFile(t, a), "expires_at") {
		t.Fatalf("undo lost the old expiry: %s", readMetaFile(t, a))
	}
	// Undo again swaps back to the rotated-in value.
	if err := a.Run([]string{"sec", "rotate", "svc/token", "--undo"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "get", "svc/token"}); err != nil || strings.TrimSpace(out.String()) != "new-token" {
		t.Fatalf("after second undo=%q err=%v", out.String(), err)
	}
	if err := a.Run([]string{"sec", "rotate", "svc/missing"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("missing field err=%v", err)
	}
	if err := a.Run([]string{"sec", "rotate", "svc/other", "--undo"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("undo without history err=%v", err)
	}
}

func TestSecRecordsLastUseAndReportsUnused(t *testing.T) {
	a, out, _, _ := secTestApp(t)
	secSet(t, a, "v1", "add", "svc/token")
	secSet(t, a, "v2", "add", "old/token")
	if strings.Contains(readMetaFile(t, a), "last_used_at") {
		t.Fatal("storing a value counted as a use")
	}
	if err := a.Run([]string{"sec", "exec", "svc", "--", "envcheck"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readMetaFile(t, a), `"last_used_at": "2026-08-10"`) {
		t.Fatalf("meta=%s", readMetaFile(t, a))
	}
	out.Reset()
	if err := a.Run([]string{"sec", "get", "old/token"}); err != nil {
		t.Fatal(err)
	}
	// 100 days on, svc/token is used again through a wenv preset and
	// old/token is not.
	a.now = func() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) }
	if err := a.Run([]string{"wenv", "set", "p", "T=sec://svc/token"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"wenv", "exec", "p", "--", "envcheck"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "check", "--unused", "90d"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "old/token") || strings.Contains(out.String(), "svc/token") || !strings.Contains(out.String(), "100d ago") {
		t.Fatalf("unused=%q", out.String())
	}
}

func TestSecCheckJSONPrintsOneDocumentWhenItFails(t *testing.T) {
	a, out, _, _ := secTestApp(t)
	secSet(t, a, "v1", "add", "svc/token", "--expires", "3d")
	out.Reset()
	if err := a.Run([]string{"sec", "check", "--json"}); ExitCode(err) != ExitOperational {
		t.Fatalf("err=%v", err)
	}
	if strings.Count(out.String(), `"schema_version"`) != 1 || !strings.Contains(out.String(), "svc/token") {
		t.Fatalf("check --json=%s", out.String())
	}
}
