package bb

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
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

func TestBubbleSelectorFiltersAndReturnsStableValue(t *testing.T) {
	model := newBubbleSelectorModel("Pick", []selectChoice{{"dev-id", "Development"}, {"prod-id", "Production"}})
	model.list.SetFilterText("prod")
	if got := model.list.VisibleItems(); len(got) != 1 {
		t.Fatalf("visible items=%d, want 1", len(got))
	}

	selected, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	selectedModel := selected.(bubbleSelectorModel)
	if selectedModel.selected != "prod-id" || selectedModel.cancelled {
		t.Fatalf("selected=%q cancelled=%v", selectedModel.selected, selectedModel.cancelled)
	}
}

func TestBubbleSelectorEmptyFilterDoesNotExit(t *testing.T) {
	model := newBubbleSelectorModel("Pick", []selectChoice{{"dev", "Development"}, {"prod", "Production"}})
	model.list.SetFilterText("no-such-environment")
	if got := model.list.VisibleItems(); len(got) != 0 {
		t.Fatalf("visible items=%d, want 0", len(got))
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updatedModel := updated.(bubbleSelectorModel)
	if updatedModel.selected != "" || updatedModel.cancelled {
		t.Fatalf("selected=%q cancelled=%v", updatedModel.selected, updatedModel.cancelled)
	}
	if cmd != nil {
		if _, quits := cmd().(tea.QuitMsg); quits {
			t.Fatal("Enter with no visible result quit the selector")
		}
	}
}

func TestBubbleSelectorEscapeClearsFilterBeforeCancelling(t *testing.T) {
	model := newBubbleSelectorModel("Pick", []selectChoice{{"dev", "Development"}, {"prod", "Production"}})
	model.list.SetFilterText("prod")

	cleared, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	clearedModel := cleared.(bubbleSelectorModel)
	if clearedModel.cancelled || clearedModel.list.FilterState() != list.Unfiltered {
		t.Fatalf("cancelled=%v filter_state=%v", clearedModel.cancelled, clearedModel.list.FilterState())
	}

	cancelled, _ := clearedModel.Update(tea.KeyMsg{Type: tea.KeyEsc})
	cancelledModel := cancelled.(bubbleSelectorModel)
	if !cancelledModel.cancelled {
		t.Fatal("second Escape did not cancel the selector")
	}
}

func TestBubbleSelectorResizesAndHandlesLongLists(t *testing.T) {
	choices := make([]selectChoice, 250)
	for i := range choices {
		choices[i] = selectChoice{Value: "id-" + strconv.Itoa(i), Label: "Environment " + strconv.Itoa(i)}
	}
	choices[173].Label = "Unique needle target"
	model := newBubbleSelectorModel("Pick", choices)

	small, _ := model.Update(tea.WindowSizeMsg{Width: 10, Height: 3})
	smallModel := small.(bubbleSelectorModel)
	if smallModel.list.Width() != 24 || smallModel.list.Height() != 8 {
		t.Fatalf("small size=%dx%d", smallModel.list.Width(), smallModel.list.Height())
	}
	large, _ := smallModel.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	largeModel := large.(bubbleSelectorModel)
	if largeModel.list.Width() != 120 || largeModel.list.Height() != 40 {
		t.Fatalf("large size=%dx%d", largeModel.list.Width(), largeModel.list.Height())
	}

	largeModel.list.SetFilterText("needle target")
	selected, _ := largeModel.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := selected.(bubbleSelectorModel).selected; got != "id-173" {
		t.Fatalf("selected=%q, want id-173", got)
	}
}

func TestCommandSelectorsReturnStableValuesWithoutStdout(t *testing.T) {
	newApp := func(input string) (*App, *bytes.Buffer, *bytes.Buffer) {
		stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
		a := New(stdout, stderr, []string{"BB_SELECTOR=plain", "TERM=xterm-256color"})
		a.in = strings.NewReader(input)
		return a, stdout, stderr
	}

	t.Run("tm project", func(t *testing.T) {
		a, stdout, stderr := newApp("2\n")
		projects := []projectRecord{{ID: "project-a", Name: "Duplicate", Path: "/work/a"}, {ID: "project-b", Name: "Duplicate", Path: "/work/b"}}
		got, err := a.selectTMProject(projects)
		if err != nil || got.ID != "project-b" {
			t.Fatalf("project=%+v err=%v", got, err)
		}
		assertSelectorStreams(t, stdout, stderr)
	})

	t.Run("tm session", func(t *testing.T) {
		a, stdout, stderr := newApp("2\n")
		sessions := []tmSession{{ID: "$1", Name: "dev"}, {ID: "$9", Name: "prod"}}
		got, err := a.selectTMSession(sessions)
		if err != nil || got.ID != "$9" {
			t.Fatalf("session=%+v err=%v", got, err)
		}
		assertSelectorStreams(t, stdout, stderr)
	})

	t.Run("wenv", func(t *testing.T) {
		a, stdout, _, _ := testApp(t)
		stderr := new(bytes.Buffer)
		a.err = stderr
		for _, args := range [][]string{{"wenv", "set", "zeta", "TARGET=zeta"}, {"wenv", "set", "alpha", "TARGET=alpha"}} {
			if err := a.Run(args); err != nil {
				t.Fatal(err)
			}
		}
		a.in = strings.NewReader("2\n")
		if err := a.Run([]string{"wenv", "apply", "--yes"}); err != nil {
			t.Fatal(err)
		}
		if got, want := stdout.String(), "export TARGET='zeta'\n"; got != want {
			t.Fatalf("stdout=%q, want %q", got, want)
		}
		if stderr.Len() == 0 {
			t.Fatal("selector did not write its UI to stderr")
		}
	})
}

func assertSelectorStreams(t *testing.T, stdout, stderr *bytes.Buffer) {
	t.Helper()
	if stdout.Len() != 0 {
		t.Fatalf("selector wrote to stdout: %q", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Fatal("selector did not write its UI to stderr")
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
	if e := os.WriteFile(filepath.Join(legacy, "dev"), []byte("AWS_PROFILE=dev\nAWS_REGION=ap-northeast-2\nEXPORTS=(\n  FOO='a b'\n  BAR=literal\n)\n"), 0o600); e != nil {
		t.Fatal(e)
	}
	if e := a.Run([]string{"wenv", "import", "--apply", "--dir", legacy}); e != nil {
		t.Fatal(e)
	}
	out.Reset()
	if e := a.Run([]string{"wenv", "export", "dev"}); e != nil {
		t.Fatal(e)
	}
	if got := out.String(); !strings.Contains(got, "export AWS_PROFILE='dev'") || !strings.Contains(got, "export FOO='a b'") || !strings.Contains(got, "export BAR='literal'") {
		t.Fatalf("exports=%q", got)
	}
	if e := os.WriteFile(filepath.Join(legacy, "evil"), []byte("AWS_PROFILE=$(id)\n"), 0o600); e != nil {
		t.Fatal(e)
	}
	if e := a.Run([]string{"wenv", "import", "--check", "--dir", legacy}); ExitCode(e) != ExitInvalidInvocation {
		t.Fatalf("err=%v", e)
	}
	unterminated := filepath.Join(t.TempDir(), "unterminated")
	if e := os.WriteFile(unterminated, []byte("EXPORTS=(\nFOO=bar\n"), 0o600); e != nil {
		t.Fatal(e)
	}
	if _, e := parseLegacyWenv(unterminated); ExitCode(e) != ExitInvalidInvocation {
		t.Fatalf("unterminated EXPORTS err=%v", e)
	}
}

func TestWenvShowAndConfirmedApply(t *testing.T) {
	a, out, _, _ := testApp(t)
	stderr := new(bytes.Buffer)
	a.err = stderr
	if err := a.Run([]string{"wenv", "set", "dev", "AWS_PROFILE=dev", "AWS_REGION=ap-northeast-2"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"wenv", "show", "dev"}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "AWS_PROFILE='dev'\nAWS_REGION='ap-northeast-2'\n"; got != want {
		t.Fatalf("show=%q, want %q", got, want)
	}

	out.Reset()
	a.in = strings.NewReader("n\n")
	if err := a.Run([]string{"wenv", "apply", "dev"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("cancel err=%v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("cancel emitted eval output: %q", out.String())
	}

	stderr.Reset()
	a.in = strings.NewReader("y\n")
	if err := a.Run([]string{"wenv", "apply", "dev"}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "export AWS_PROFILE='dev'\nexport AWS_REGION='ap-northeast-2'\n"; got != want {
		t.Fatalf("apply=%q, want %q", got, want)
	}
	if !strings.Contains(stderr.String(), `AWS_PROFILE: "" -> "dev"`) || !strings.Contains(stderr.String(), "Apply this environment? [y/N]") {
		t.Fatalf("preview=%q", stderr.String())
	}

	out.Reset()
	stderr.Reset()
	if err := a.Run([]string{"wenv", "apply", "dev", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "export AWS_PROFILE='dev'\nexport AWS_REGION='ap-northeast-2'\n"; got != want {
		t.Fatalf("non-interactive apply=%q, want %q", got, want)
	}
	if strings.Contains(stderr.String(), "Apply this environment? [y/N]") {
		t.Fatalf("--yes prompted for confirmation: %q", stderr.String())
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
	if name == "pbcopy" {
		b, _ := io.ReadAll(os.Stdin)
		_ = os.WriteFile(os.Getenv("SEC_CLIPBOARD_FILE"), b, 0o600)
		os.Exit(0)
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

func TestSecCopySelectorUsesStableValueAndKeepsStdoutClean(t *testing.T) {
	a, out, _, _ := testApp(t)
	dir := t.TempDir()
	clipboard := filepath.Join(dir, "clipboard")
	a.env = append(a.env,
		"BINBOX_SECRETS_FILE="+filepath.Join(dir, "secrets.json.age"),
		"BINBOX_AGE_KEY="+filepath.Join(dir, "age.key"),
		"GO_WANT_SEC_HELPER=1",
		"SEC_CLIPBOARD_FILE="+clipboard,
		"BB_SELECTOR=plain",
	)
	a.lookPath = func(string) (string, error) { return "helper", nil }
	a.command = func(name string, args ...string) *exec.Cmd {
		return exec.Command(os.Args[0], append([]string{"-test.run=TestSecHelperProcess", "--", name}, args...)...)
	}
	if err := a.Run([]string{"sec", "init"}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		service, field, value string
	}{{"alpha", "password", "alpha-secret"}, {"zeta", "token", "zeta-secret"}} {
		a.in = strings.NewReader(item.value + "\n")
		if err := a.Run([]string{"sec", "set", item.service, item.field}); err != nil {
			t.Fatal(err)
		}
	}

	stderr := new(bytes.Buffer)
	a.err = stderr
	a.in = strings.NewReader("2\n")
	out.Reset()
	if err := a.Run([]string{"sec", "copy"}); err != nil {
		t.Fatal(err)
	}
	assertSelectorStreams(t, out, stderr)
	got, err := os.ReadFile(clipboard)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "zeta-secret" {
		t.Fatalf("clipboard=%q, want zeta-secret", got)
	}
}
