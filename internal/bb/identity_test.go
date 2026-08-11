package bb

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestBuiltInSelectorUsesNumberOrExactName(t *testing.T) {
	a, _, _, _ := testApp(t)
	a.in = strings.NewReader("2\n")
	got, e := a.selectOne("Pick", []selectChoice{{"dev", "Development"}, {"prod", "Production"}})
	if e != nil || got != "prod" {
		t.Fatalf("got=%q err=%v", got, e)
	}
}

func TestBubbleSelectorSelectsStableValueAndCancels(t *testing.T) {
	model := newBubbleSelectorModel("Pick", []selectChoice{{"dev", "Development"}, {"prod", "Production"}})
	selected, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	selectedModel := selected.(bubbleSelectorModel)
	if selectedModel.selected != "dev" || selectedModel.cancelled {
		t.Fatalf("selected=%q cancelled=%v", selectedModel.selected, selectedModel.cancelled)
	}

	cancelled, _ := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	cancelledModel := cancelled.(bubbleSelectorModel)
	if !cancelledModel.cancelled || cancelledModel.selected != "" {
		t.Fatalf("selected=%q cancelled=%v", cancelledModel.selected, cancelledModel.cancelled)
	}
}

func TestProfileManagesConfigOnlyAndPreservesFields(t *testing.T) {
	a, out, _, state := testApp(t)
	config := filepath.Join(t.TempDir(), "config")
	a.env = append(a.env, "AWS_CONFIG_FILE="+config)
	if e := a.Run([]string{"profile", "add", "dev", "--sso-session", "corp", "--account-id", "123456789012", "--role-name", "Admin", "--region", "ap-northeast-2"}); e != nil {
		t.Fatal(e)
	}
	if e := a.Run([]string{"profile", "edit", "dev", "--region", "us-east-1"}); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(config)
	if e != nil {
		t.Fatal(e)
	}
	text := string(b)
	for _, want := range []string{"sso_session = corp", "sso_account_id = 123456789012", "sso_role_name = Admin", "region = us-east-1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("config=%s missing %s", text, want)
		}
	}
	out.Reset()
	if e := a.Run([]string{"profile", "list"}); e != nil || strings.TrimSpace(out.String()) != "dev" {
		t.Fatalf("list=%q err=%v", out.String(), e)
	}
	if _, e = os.Stat(filepath.Join(state, "bb", "aws-config-backups")); e != nil {
		t.Fatal("backup missing:", e)
	}
	if _, e = os.Stat(filepath.Join(filepath.Dir(config), "credentials")); !os.IsNotExist(e) {
		t.Fatalf("credentials touched: %v", e)
	}
}

func TestWenvImportRejectsExecutableSyntaxAndExportsSafely(t *testing.T) {
	a, out, _, _ := testApp(t)
	legacy := t.TempDir()
	if e := os.WriteFile(filepath.Join(legacy, "dev"), []byte("AWS_PROFILE=dev\nAWS_REGION=ap-northeast-2\nEXPORTS=(FOO='a b')\n"), 0o600); e != nil {
		t.Fatal(e)
	}
	if e := a.Run([]string{"wenv", "import", "--apply", "--dir", legacy}); e != nil {
		t.Fatal(e)
	}
	out.Reset()
	if e := a.Run([]string{"wenv", "export", "dev"}); e != nil {
		t.Fatal(e)
	}
	if got := out.String(); !strings.Contains(got, "export AWS_PROFILE='dev'") || !strings.Contains(got, "export FOO='a b'") {
		t.Fatalf("exports=%q", got)
	}
	if e := os.WriteFile(filepath.Join(legacy, "evil"), []byte("AWS_PROFILE=$(id)\n"), 0o600); e != nil {
		t.Fatal(e)
	}
	if e := a.Run([]string{"wenv", "import", "--check", "--dir", legacy}); ExitCode(e) != ExitInvalidInvocation {
		t.Fatalf("err=%v", e)
	}
}

func TestSecHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_SEC_HELPER") != "1" {
		return
	}
	args := os.Args
	idx := 0
	for i, x := range args {
		if x == "--" {
			idx = i + 1
			break
		}
	}
	name, argv := args[idx], args[idx+1:]
	if name == "age-keygen" && len(argv) > 0 && argv[0] == "-y" {
		fmt.Fprint(os.Stdout, "age1test\n")
		os.Exit(0)
	}
	if name == "age-keygen" && len(argv) > 1 && argv[0] == "-o" {
		_ = os.WriteFile(argv[1], []byte("AGE-SECRET-KEY-TEST\n"), 0o600)
		os.Exit(0)
	}
	if name == "age" && len(argv) > 0 && argv[0] == "-d" {
		b, _ := io.ReadAll(os.Stdin)
		os.Stdout.Write(b)
		os.Exit(0)
	}
	if name == "age" {
		for i, x := range argv {
			if x == "-o" && i+1 < len(argv) {
				b, _ := io.ReadAll(os.Stdin)
				_ = os.WriteFile(argv[i+1], b, 0o600)
				os.Exit(0)
			}
		}
	}
	os.Exit(90)
}
func TestSecCompatibleCRUDNeverPlacesValueInJournal(t *testing.T) {
	a, out, _, state := testApp(t)
	dir := t.TempDir()
	a.env = append(a.env, "BINBOX_SECRETS_FILE="+filepath.Join(dir, "secrets.json.age"), "BINBOX_AGE_KEY="+filepath.Join(dir, "age.key"), "GO_WANT_SEC_HELPER=1")
	a.lookPath = func(string) (string, error) { return "helper", nil }
	a.command = func(name string, args ...string) *exec.Cmd {
		return exec.Command(os.Args[0], append([]string{"-test.run=TestSecHelperProcess", "--", name}, args...)...)
	}
	if e := a.Run([]string{"sec", "init"}); e != nil {
		t.Fatal(e)
	}
	a.in = bytes.NewBufferString("fake-token\n")
	if e := a.Run([]string{"sec", "set", "svc", "token"}); e != nil {
		t.Fatal(e)
	}
	out.Reset()
	if e := a.Run([]string{"sec", "get", "svc", "token"}); e != nil || strings.TrimSpace(out.String()) != "fake-token" {
		t.Fatalf("out=%q err=%v", out.String(), e)
	}
	if b, e := os.ReadFile(filepath.Join(state, "bb", "journal.ndjson")); e == nil && bytes.Contains(b, []byte("fake-token")) {
		t.Fatal("secret leaked to journal")
	}
}
