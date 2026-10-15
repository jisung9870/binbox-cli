package bb

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// secTestApp returns an app on the fake age helper with an initialized store,
// a fake clipboard file, and automatic clipboard clearing disabled.
func secTestApp(t *testing.T) (*App, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	a, out, _, _ := testApp(t)
	stderr := new(bytes.Buffer)
	a.err = stderr
	dir := t.TempDir()
	enableSecHelper(a, dir)
	clipboard := filepath.Join(dir, "clipboard")
	a.env = append(a.env, "BB_SELECTOR=plain", "SEC_CLIPBOARD_FILE="+clipboard, clipboardClearEnv+"=0")
	if err := a.Run([]string{"sec", "init"}); err != nil {
		t.Fatal(err)
	}
	return a, out, stderr, clipboard
}

func secSet(t *testing.T, a *App, value string, argv ...string) {
	t.Helper()
	a.in = strings.NewReader(value + "\n")
	if err := a.Run(append([]string{"sec"}, argv...)); err != nil {
		t.Fatalf("%v: %v", argv, err)
	}
}

func readMetaFile(t *testing.T, a *App) string {
	t.Helper()
	b, err := os.ReadFile(a.secMetaPath())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func TestSecPathSyntaxMatchesTwoArgumentForm(t *testing.T) {
	a, out, _, _ := secTestApp(t)
	secSet(t, a, "v1", "add", "svc/token")
	out.Reset()
	if err := a.Run([]string{"sec", "get", "svc/token"}); err != nil || strings.TrimSpace(out.String()) != "v1" {
		t.Fatalf("get path out=%q err=%v", out.String(), err)
	}
	if err := a.Run([]string{"sec", "rename", "svc/token", "pat", "--yes"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "get", "svc", "pat"}); err != nil || strings.TrimSpace(out.String()) != "v1" {
		t.Fatalf("renamed out=%q err=%v", out.String(), err)
	}
	if got := expandSecretPath([]string{"--force", "a/b"}); strings.Join(got, " ") != "--force a b" {
		t.Fatalf("flag-first expansion=%v", got)
	}
	if got := expandSecretPath([]string{"a/b/c"}); len(got) != 1 {
		t.Fatalf("nested path must stay as is: %v", got)
	}
}

func TestSecExpiryMetadataBadgesAndCheck(t *testing.T) {
	a, out, _, _ := secTestApp(t)
	// testApp pins today to 2026-08-10.
	secSet(t, a, "soon", "add", "svc/soon", "--expires", "5d", "--url", "https://example.test/tokens")
	secSet(t, a, "later", "add", "svc/later", "--expires=2026-12-31")
	secSet(t, a, "old", "add", "other/old", "--expires", "2026-08-01")
	secSet(t, a, "plain", "add", "plain/none")

	meta := readMetaFile(t, a)
	for _, value := range []string{"soon", "later", "old", "plain"} {
		if strings.Contains(meta, `"`+value+`"`) {
			t.Fatalf("metadata must never contain values: %s", meta)
		}
	}

	out.Reset()
	if err := a.Run([]string{"sec", "list", "svc"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "later  D-143") || !strings.Contains(got, "soon   D-5 !") {
		t.Fatalf("field badges:\n%s", got)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "list"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "other  expired 9d ago") || !strings.Contains(got, "svc    D-5 !") || !strings.Contains(got, "plain\n") {
		t.Fatalf("service badges:\n%s", got)
	}

	out.Reset()
	err := a.Run([]string{"sec", "check"})
	if ExitCode(err) != ExitOperational {
		t.Fatalf("check err=%v", err)
	}
	got := out.String()
	if !strings.Contains(got, "other/old") || !strings.Contains(got, "svc/soon") || !strings.Contains(got, "https://example.test/tokens") || strings.Contains(got, "svc/later") {
		t.Fatalf("check output:\n%s", got)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "check", "--quiet"}); ExitCode(err) != ExitOperational || !Reported(err) || out.Len() != 0 {
		t.Fatalf("quiet check out=%q err=%v", out.String(), err)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "check", "--json"}); ExitCode(err) != ExitOperational || !strings.Contains(out.String(), `"state":"expired"`) {
		t.Fatalf("json check out=%q err=%v", out.String(), err)
	}

	// Clearing the problems makes check succeed.
	for _, argv := range [][]string{{"sec", "meta", "svc/soon", "--expires", "none"}, {"sec", "meta", "other/old", "--expires", "1y"}} {
		out.Reset()
		if err := a.Run(argv); err != nil {
			t.Fatalf("%v: %v", argv, err)
		}
	}
	out.Reset()
	if err := a.Run([]string{"sec", "check"}); err != nil || out.Len() != 0 {
		t.Fatalf("clean check out=%q err=%v", out.String(), err)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "meta", "other/old"}); err != nil || !strings.Contains(out.String(), "2027-08-10") {
		t.Fatalf("meta show out=%q err=%v", out.String(), err)
	}
}

func TestSecSetRejectsBadMetadataBeforeReadingAValue(t *testing.T) {
	a, _, _, _ := secTestApp(t)
	a.in = strings.NewReader("never-stored\n")
	if err := a.Run([]string{"sec", "add", "svc/token", "--expires", "soon"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("bad expiry err=%v", err)
	}
	if err := a.Run([]string{"sec", "add", "svc/token", "--url", "javascript:alert(1)"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("bad url err=%v", err)
	}
	if err := a.Run([]string{"sec", "list", "svc"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("nothing should be stored, err=%v", err)
	}
}

func TestSecJWTExpiryIsReadFromExpClaim(t *testing.T) {
	a, _, stderr, _ := secTestApp(t)
	exp := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).Unix()
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"x","exp":` + strconv.FormatInt(exp, 10) + `}`))
	token := "eyJhbGciOiJIUzI1NiJ9." + payload + ".sig"
	secSet(t, a, token, "add", "svc/jwt")
	if meta := readMetaFile(t, a); !strings.Contains(meta, `"expires_at": "2026-09-01"`) || !strings.Contains(meta, `"expires_source": "jwt"`) || strings.Contains(meta, payload) {
		t.Fatalf("jwt metadata=%s", meta)
	}
	if !strings.Contains(stderr.String(), "exp claim") {
		t.Fatalf("stderr=%q", stderr.String())
	}
	// An explicit expiry wins over the claim.
	secSet(t, a, token, "add", "svc/jwt", "--force", "--expires", "2026-08-20")
	if meta := readMetaFile(t, a); !strings.Contains(meta, `"expires_at": "2026-08-20"`) {
		t.Fatalf("explicit expiry ignored: %s", meta)
	}
	if _, ok := jwtExpiry("not.a-jwt"); ok {
		t.Fatal("two-part value parsed as jwt")
	}
}

func TestSecReplaceWarnsAboutStaleExpiry(t *testing.T) {
	a, _, stderr, _ := secTestApp(t)
	secSet(t, a, "v1", "add", "svc/token", "--expires", "30d")
	stderr.Reset()
	secSet(t, a, "v2", "add", "svc/token", "--force")
	if !strings.Contains(stderr.String(), "Expiry is still 2026-09-09") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestSecRemoveMovesToTrashAndRestores(t *testing.T) {
	a, out, stderr, _ := secTestApp(t)
	secSet(t, a, "alpha-value", "add", "svc/alpha", "--expires", "30d")
	secSet(t, a, "beta-value", "add", "svc/beta")

	// A single field goes to the trash without a confirmation prompt.
	a.in = strings.NewReader("")
	if err := a.Run([]string{"sec", "rm", "svc/alpha"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "bb sec restore svc/alpha") {
		t.Fatalf("stderr=%q", stderr.String())
	}
	if strings.Contains(readMetaFile(t, a), "svc/alpha") {
		t.Fatal("metadata kept for a removed field")
	}
	trashBytes, err := os.ReadFile(a.secTrashPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(trashBytes), "alpha-value") {
		// The fake age helper stores plaintext; the real file is age ciphertext.
		t.Fatalf("trash content=%s", trashBytes)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "trash"}); err != nil || !strings.Contains(out.String(), "svc/alpha") || strings.Contains(out.String(), "alpha-value") {
		t.Fatalf("trash list out=%q err=%v", out.String(), err)
	}

	// Restoring refuses to overwrite and otherwise brings value and metadata back.
	secSet(t, a, "replacement", "add", "svc/alpha")
	if err := a.Run([]string{"sec", "restore", "svc/alpha"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("overwrite restore err=%v", err)
	}
	if err := a.Run([]string{"sec", "rename", "svc/alpha", "alpha2", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"sec", "restore", "svc/alpha"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "get", "svc/alpha"}); err != nil || strings.TrimSpace(out.String()) != "alpha-value" {
		t.Fatalf("restored value=%q err=%v", out.String(), err)
	}
	if !strings.Contains(readMetaFile(t, a), "svc/alpha") {
		t.Fatal("metadata not restored")
	}
	out.Reset()
	if err := a.Run([]string{"sec", "trash"}); err != nil || out.Len() != 0 {
		t.Fatalf("trash after restore=%q err=%v", out.String(), err)
	}

	// Removing a whole service still defaults to cancel.
	a.in = strings.NewReader("\n")
	if err := a.Run([]string{"sec", "rm", "svc"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("service removal without confirmation err=%v", err)
	}
	if err := a.Run([]string{"sec", "rm", "svc", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"sec", "restore", "svc"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "list", "svc"}); err != nil || !strings.Contains(out.String(), "alpha2") || !strings.Contains(out.String(), "beta") {
		t.Fatalf("restored service=%q err=%v", out.String(), err)
	}
}

func TestSecTrashExpiresAfterRetention(t *testing.T) {
	a, out, _, _ := secTestApp(t)
	secSet(t, a, "v", "add", "svc/token")
	if err := a.Run([]string{"sec", "rm", "svc/token"}); err != nil {
		t.Fatal(err)
	}
	base := a.now()
	a.now = func() time.Time { return base.Add(secretTrashRetention + time.Hour) }
	out.Reset()
	if err := a.Run([]string{"sec", "trash"}); err != nil || out.Len() != 0 {
		t.Fatalf("expired trash=%q err=%v", out.String(), err)
	}
	if err := a.Run([]string{"sec", "restore", "svc/token"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("restore after retention err=%v", err)
	}
}

func TestSecPasteStoresClipboardAndClearsIt(t *testing.T) {
	a, out, stderr, clipboard := secTestApp(t)
	if err := os.WriteFile(clipboard, []byte("pasted-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.in = strings.NewReader("")
	if err := a.Run([]string{"sec", "add", "svc/token", "--paste"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(clipboard); len(b) != 0 {
		t.Fatalf("clipboard not cleared: %q", b)
	}
	if strings.Contains(stderr.String(), "pasted-token") || !strings.Contains(stderr.String(), "clipboard cleared") {
		t.Fatalf("stderr=%q", stderr.String())
	}
	out.Reset()
	if err := a.Run([]string{"sec", "get", "svc/token"}); err != nil || strings.TrimSpace(out.String()) != "pasted-token" {
		t.Fatalf("stored=%q err=%v", out.String(), err)
	}
	if err := a.Run([]string{"sec", "add", "svc/other", "--paste"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("empty clipboard err=%v", err)
	}
}

func TestSecClipboardClearLeavesNewerContentAlone(t *testing.T) {
	a, _, _, clipboard := secTestApp(t)
	if err := os.WriteFile(clipboard, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.in = strings.NewReader(clipboardDigest([]byte("secret")))
	if err := a.Run([]string{"sec", clipboardClearCommand, "0"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(clipboard); len(b) != 0 {
		t.Fatalf("matching clipboard not cleared: %q", b)
	}
	if err := os.WriteFile(clipboard, []byte("something else"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.in = strings.NewReader(clipboardDigest([]byte("secret")))
	if err := a.Run([]string{"sec", clipboardClearCommand, "0"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(clipboard); string(b) != "something else" {
		t.Fatalf("newer clipboard content changed: %q", b)
	}
}

func TestSecReviewRegressions(t *testing.T) {
	a, out, _, _ := secTestApp(t)

	// An option value containing "/" is not taken for the path.
	secSet(t, a, "v", "add", "--note", "see a/b", "svc/token")
	if !strings.Contains(readMetaFile(t, a), `"note": "see a/b"`) {
		t.Fatalf("note=%s", readMetaFile(t, a))
	}

	// --purge also removes older trashed copies, so restore cannot revive them.
	if err := a.Run([]string{"sec", "rm", "svc/token"}); err != nil {
		t.Fatal(err)
	}
	secSet(t, a, "v2", "add", "svc/token")
	if err := a.Run([]string{"sec", "rm", "svc/token", "--purge", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"sec", "restore", "svc/token"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("restore after purge err=%v", err)
	}

	// Listing the trash persists the expiry purge instead of only hiding entries.
	secSet(t, a, "old-value", "add", "svc/old")
	if err := a.Run([]string{"sec", "rm", "svc/old"}); err != nil {
		t.Fatal(err)
	}
	base := a.now()
	a.now = func() time.Time { return base.Add(secretTrashRetention + time.Hour) }
	out.Reset()
	if err := a.Run([]string{"sec", "trash"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(a.secTrashPath()); strings.Contains(string(b), "old-value") {
		t.Fatalf("expired value still on disk: %s", b)
	}

	// Paste tools that append CRLF still match the copied value.
	if clipboardDigest([]byte("tok\r\n")) != clipboardDigest([]byte("tok")) {
		t.Fatal("digest must ignore trailing line endings")
	}
}
