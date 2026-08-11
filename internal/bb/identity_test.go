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
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestBuiltInSelectorUsesNumberOrExactName(t *testing.T) {
	a, _, _, _ := testApp(t)
	a.in = strings.NewReader("2\n")
	got, e := a.selectOne("Pick", []selectChoice{{Value: "dev", Label: "Development"}, {Value: "prod", Label: "Production"}})
	if e != nil || got != "prod" {
		t.Fatalf("got=%q err=%v", got, e)
	}
}

func TestBubbleSelectorSelectsStableValueAndCancels(t *testing.T) {
	model := newBubbleSelectorModel("Pick", []selectChoice{{Value: "dev", Label: "Development"}, {Value: "prod", Label: "Production"}})
	selected, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	selectedModel := selected.(bubbleSelectorModel)
	if selectedModel.selected != "dev" || selectedModel.cancelled {
		t.Fatalf("selected=%q cancelled=%v", selectedModel.selected, selectedModel.cancelled)
	}

	cancelled, _ := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	cancelledModel := cancelled.(bubbleSelectorModel)
	if !cancelledModel.cancelled || !cancelledModel.interrupted || cancelledModel.selected != "" {
		t.Fatalf("selected=%q cancelled=%v interrupted=%v", cancelledModel.selected, cancelledModel.cancelled, cancelledModel.interrupted)
	}
}

func TestBubbleSelectorFiltersAndReturnsStableValue(t *testing.T) {
	model := newBubbleSelectorModel("Pick", []selectChoice{{Value: "dev-id", Label: "Development"}, {Value: "prod-id", Label: "Production"}})
	model = typeInSelector(model, "prod")
	if got := len(model.matches); got != 1 {
		t.Fatalf("visible items=%d, want 1", got)
	}

	selected, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	selectedModel := selected.(bubbleSelectorModel)
	if selectedModel.selected != "prod-id" || selectedModel.cancelled {
		t.Fatalf("selected=%q cancelled=%v", selectedModel.selected, selectedModel.cancelled)
	}
}

func TestBubbleSelectorEmptyFilterDoesNotExit(t *testing.T) {
	model := newBubbleSelectorModel("Pick", []selectChoice{{Value: "dev", Label: "Development"}, {Value: "prod", Label: "Production"}})
	model = typeInSelector(model, "no-such-environment")
	if got := len(model.matches); got != 0 {
		t.Fatalf("visible items=%d, want 0", got)
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
	model := newBubbleSelectorModel("Pick", []selectChoice{{Value: "dev", Label: "Development"}, {Value: "prod", Label: "Production"}})
	model = typeInSelector(model, "prod")

	cleared, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	clearedModel := cleared.(bubbleSelectorModel)
	if clearedModel.cancelled || clearedModel.input.Value() != "" || len(clearedModel.matches) != 2 {
		t.Fatalf("cancelled=%v query=%q matches=%d", clearedModel.cancelled, clearedModel.input.Value(), len(clearedModel.matches))
	}

	cancelled, _ := clearedModel.Update(tea.KeyMsg{Type: tea.KeyEsc})
	cancelledModel := cancelled.(bubbleSelectorModel)
	if !cancelledModel.cancelled || cancelledModel.interrupted {
		t.Fatalf("second Escape cancellation: cancelled=%v interrupted=%v", cancelledModel.cancelled, cancelledModel.interrupted)
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
	if smallModel.width != 10 || smallModel.height != 3 {
		t.Fatalf("small size=%dx%d", smallModel.width, smallModel.height)
	}
	large, _ := smallModel.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	largeModel := large.(bubbleSelectorModel)
	if largeModel.width != 120 || largeModel.height != 40 {
		t.Fatalf("large size=%dx%d", largeModel.width, largeModel.height)
	}

	largeModel = typeInSelector(largeModel, "needle target")
	selected, _ := largeModel.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := selected.(bubbleSelectorModel).selected; got != "id-173" {
		t.Fatalf("selected=%q, want id-173", got)
	}
}

func TestBubbleSelectorFirstArrowMovesWhileSearchRemainsActive(t *testing.T) {
	model := newBubbleSelectorModel("Pick", []selectChoice{
		{Value: "prod-a", Label: "Production Alpha"},
		{Value: "prod-b", Label: "Production Beta"},
		{Value: "dev", Label: "Development"},
	})
	model = typeInSelector(model, "prod")
	if model.cursor != 0 || len(model.matches) != 2 {
		t.Fatalf("cursor=%d matches=%d", model.cursor, len(model.matches))
	}
	moved, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	movedModel := moved.(bubbleSelectorModel)
	if movedModel.cursor != 1 || movedModel.input.Value() != "prod" {
		t.Fatalf("cursor=%d query=%q", movedModel.cursor, movedModel.input.Value())
	}
	want := movedModel.matches[1].choice.value
	selected, _ := movedModel.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := selected.(bubbleSelectorModel).selected; got != want {
		t.Fatalf("selected=%q, want second visible result %q", got, want)
	}
}

func TestBubbleSelectorViewIsSearchFirstResponsiveAndNoColor(t *testing.T) {
	model := newBubbleSelectorModelWithColor("Project", []selectChoice{
		{Value: "dev", Label: "Development", Description: "/workspace/dev", SearchText: "backend"},
		{Value: "prod", Label: "Production", Description: "/workspace/prod", SearchText: "frontend"},
	}, true)
	resized, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	model = resized.(bubbleSelectorModel)
	view := model.View()
	for _, want := range []string{"Select Project", "Search", "Type to search", "2/2 results", "> Development", "/workspace/dev", "enter select"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "\x1b[") {
		t.Fatalf("NO_COLOR view contains ANSI escape: %q", view)
	}

	narrow, _ := model.Update(tea.WindowSizeMsg{Width: 49, Height: 12})
	narrowView := narrow.(bubbleSelectorModel).View()
	if strings.Contains(narrowView, "/workspace/dev") {
		t.Fatalf("narrow view showed metadata:\n%s", narrowView)
	}

	filtered := typeInSelector(model, "missing")
	if got := filtered.View(); !strings.Contains(got, `No matches for "missing"`) || !strings.Contains(got, "0/2 results") {
		t.Fatalf("empty search view:\n%s", got)
	}
}

func TestBubbleSelectorLayoutNeverExceedsTerminal(t *testing.T) {
	choices := []selectChoice{
		{Value: "dev", Label: "개발 환경", Description: "/workspace/development/very/long/path", SearchText: "backend"},
		{Value: "prod", Label: "Production", Description: "/workspace/production/very/long/path", SearchText: "frontend"},
	}
	for _, size := range []struct{ width, height int }{{24, 8}, {40, 9}, {49, 12}, {50, 12}, {80, 20}, {120, 40}} {
		model := newBubbleSelectorModelWithColor("Environment", choices, true)
		resized, _ := model.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
		view := resized.(bubbleSelectorModel).View()
		lines := strings.Split(view, "\n")
		if len(lines) > size.height {
			t.Fatalf("%dx%d rendered %d lines:\n%s", size.width, size.height, len(lines), view)
		}
		for lineNumber, line := range lines {
			if width := ansi.StringWidth(line); width > size.width {
				t.Fatalf("%dx%d line %d width=%d:\n%s", size.width, size.height, lineNumber, width, view)
			}
		}
		if size.width == 24 {
			for _, want := range []string{"2/2 results", "enter select", "esc clear/cancel"} {
				if !strings.Contains(view, want) {
					t.Fatalf("24x8 view missing %q:\n%s", want, view)
				}
			}
		}
	}
}

func TestBubbleSelectorSearchesMetadataAndHandlesUnicodeEditing(t *testing.T) {
	model := newBubbleSelectorModel("Pick", []selectChoice{
		{Value: "dev", Label: "개발 환경", Description: "/workspace/dev", SearchText: "backend"},
		{Value: "prod", Label: "Production", Description: "/workspace/prod", SearchText: "frontend"},
	})
	model = typeInSelector(model, "backend")
	if len(model.matches) != 1 || model.matches[0].choice.value != "dev" {
		t.Fatalf("metadata matches=%+v", model.matches)
	}
	for range len([]rune("backend")) {
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		model = updated.(bubbleSelectorModel)
	}
	model = typeInSelector(model, "개발")
	if model.input.Value() != "개발" || len(model.matches) != 1 || model.matches[0].choice.value != "dev" {
		t.Fatalf("unicode query=%q matches=%+v", model.input.Value(), model.matches)
	}
}

func typeInSelector(model bubbleSelectorModel, value string) bubbleSelectorModel {
	for _, r := range value {
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		model = updated.(bubbleSelectorModel)
	}
	return model
}

// secretWalk mirrors the Service -> Field -> Action graph used by `bb sec`.
func secretWalk() (selectStage, func([]string) *selectStage) {
	root := selectStage{Prompt: "Secret service", Choices: []selectChoice{
		{Value: "alpha", Label: "alpha"},
		{Value: "zeta", Label: "zeta"},
	}}
	next := func(path []string) *selectStage {
		switch len(path) {
		case 1:
			return &selectStage{Prompt: "Field in " + path[0], Choices: []selectChoice{
				{Value: "password", Label: "password"},
				{Value: "token", Label: "token"},
			}}
		case 2:
			return &selectStage{Prompt: "Action for " + path[0] + "/" + path[1], Choices: []selectChoice{
				{Value: "copy", Label: "Copy to clipboard"},
				{Value: "remove-field", Label: "Remove field"},
			}}
		default:
			return nil
		}
	}
	return root, next
}

// pressStaged reports whether the returned command ends the program. tea.Quit
// answers immediately; the cursor blink command sleeps for about half a second,
// so a short deadline separates the two without waiting on the timer.
func pressStaged(t *testing.T, model stagedSelectorModel, msg tea.Msg) (stagedSelectorModel, bool) {
	t.Helper()
	updated, cmd := model.Update(msg)
	next := updated.(stagedSelectorModel)
	if cmd == nil {
		return next, false
	}
	answered := make(chan tea.Msg, 1)
	go func() { answered <- cmd() }()
	select {
	case msg := <-answered:
		_, quits := msg.(tea.QuitMsg)
		return next, quits
	case <-time.After(200 * time.Millisecond):
		return next, false
	}
}

// typeInStagedSelector ignores the returned commands: typing never ends the
// program, so there is nothing to wait on.
func typeInStagedSelector(t *testing.T, model stagedSelectorModel, value string) stagedSelectorModel {
	t.Helper()
	for _, r := range value {
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		model = updated.(stagedSelectorModel)
	}
	return model
}

func TestStagedSelectorWalksLevelsWithoutQuittingInBetween(t *testing.T) {
	root, next := secretWalk()
	model := newStagedSelectorModel(root, next, true)

	model, quits := pressStaged(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if quits {
		t.Fatal("entering a service ended the program instead of pushing the next level")
	}
	if len(model.stack) != 2 || model.stack[1].prompt != "Field in alpha" {
		t.Fatalf("stack=%d prompt=%q", len(model.stack), model.stack[len(model.stack)-1].prompt)
	}

	model, quits = pressStaged(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if quits {
		t.Fatal("entering a field ended the program instead of pushing the next level")
	}
	if len(model.stack) != 3 || model.stack[2].prompt != "Action for alpha/password" {
		t.Fatalf("stack=%d prompt=%q", len(model.stack), model.stack[len(model.stack)-1].prompt)
	}

	model, quits = pressStaged(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if !quits {
		t.Fatal("a complete path did not end the program")
	}
	want := []string{"alpha", "password", "copy"}
	if got := model.outcome.Path; !slicesEqual(got, want) || model.outcome.Cancelled {
		t.Fatalf("path=%v cancelled=%v", got, model.outcome.Cancelled)
	}
}

func TestStagedSelectorEscapeClearsQueryThenPopsThenExits(t *testing.T) {
	root, next := secretWalk()
	model := newStagedSelectorModel(root, next, true)

	model = typeInStagedSelector(t, model, "zet")
	model, quits := pressStaged(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if quits || len(model.stack) != 1 || model.stack[0].input.Value() != "" {
		t.Fatalf("first Escape should only clear the query: stack=%d query=%q quits=%v", len(model.stack), model.stack[0].input.Value(), quits)
	}

	model, _ = pressStaged(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if len(model.stack) != 2 {
		t.Fatalf("stack=%d, want 2", len(model.stack))
	}

	model, quits = pressStaged(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if quits || len(model.stack) != 1 || len(model.path) != 0 {
		t.Fatalf("Escape should pop one level: stack=%d path=%v quits=%v", len(model.stack), model.path, quits)
	}

	model, quits = pressStaged(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if !quits || !model.outcome.Cancelled || model.outcome.Interrupted {
		t.Fatalf("Escape at the outermost level should cancel: %+v quits=%v", model.outcome, quits)
	}
}

func TestStagedSelectorKeepsLevelStateWhenReturning(t *testing.T) {
	root, next := secretWalk()
	model := newStagedSelectorModel(root, next, true)

	model = typeInStagedSelector(t, model, "zeta")
	model, _ = pressStaged(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = typeInStagedSelector(t, model, "token")
	if got := model.stack[1].matches; len(got) != 1 || got[0].choice.value != "token" {
		t.Fatalf("field level did not filter: %+v", got)
	}

	model, _ = pressStaged(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	model, _ = pressStaged(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if len(model.stack) != 1 {
		t.Fatalf("stack=%d, want 1", len(model.stack))
	}
	if got := model.stack[0].input.Value(); got != "zeta" {
		t.Fatalf("returning to the outer level lost its query: %q", got)
	}
	if got := model.stack[0].matches; len(got) != 1 || got[0].choice.value != "zeta" {
		t.Fatalf("returning to the outer level lost its filter: %+v", got)
	}

	// The cleared selection must not immediately re-enter the level we left.
	model, quits := pressStaged(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'!'}})
	if quits || len(model.stack) != 1 {
		t.Fatalf("typing after returning changed levels: stack=%d quits=%v", len(model.stack), quits)
	}
}

func TestStagedSelectorCtrlCExitsFromAnyLevel(t *testing.T) {
	root, next := secretWalk()
	model := newStagedSelectorModel(root, next, true)
	model, _ = pressStaged(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model, _ = pressStaged(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if len(model.stack) != 3 {
		t.Fatalf("stack=%d, want 3", len(model.stack))
	}

	model, quits := pressStaged(t, model, tea.KeyMsg{Type: tea.KeyCtrlC})
	if !quits || !model.outcome.Interrupted || !model.outcome.Cancelled || len(model.outcome.Path) != 0 {
		t.Fatalf("Ctrl+C from a deep level: %+v quits=%v", model.outcome, quits)
	}
}

func TestStagedSelectorResizeReachesLevelsThatAreNotVisible(t *testing.T) {
	root, next := secretWalk()
	model := newStagedSelectorModel(root, next, true)
	model, _ = pressStaged(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	model, _ = pressStaged(t, model, tea.WindowSizeMsg{Width: 44, Height: 12})
	for i, level := range model.stack {
		if level.width != 44 || level.height != 12 {
			t.Fatalf("level %d kept %dx%d", i, level.width, level.height)
		}
	}

	model, _ = pressStaged(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if view := model.View(); ansi.StringWidth(strings.Split(view, "\n")[0]) > 44 {
		t.Fatalf("view is wider than the terminal after returning:\n%s", view)
	}
}

func TestStagedSelectorTreatsAnEmptyNextLevelAsComplete(t *testing.T) {
	root := selectStage{Prompt: "Service", Choices: []selectChoice{{Value: "alpha", Label: "alpha"}}}
	next := func([]string) *selectStage {
		return &selectStage{Prompt: "Field in alpha", Choices: nil}
	}
	model := newStagedSelectorModel(root, next, true)

	model, quits := pressStaged(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if !quits {
		t.Fatal("an empty following level was pushed instead of completing the walk")
	}
	if got := model.outcome.Path; !slicesEqual(got, []string{"alpha"}) {
		t.Fatalf("path=%v", got)
	}
}

func TestSelectStagesPlainPopsOnEmptyAnswer(t *testing.T) {
	a, out, _, _ := testApp(t)
	a.env = append(a.env, "BB_SELECTOR=plain")
	root, next := secretWalk()

	// alpha -> password -> back -> back -> zeta -> token -> remove-field
	a.in = strings.NewReader("1\n1\n\n\n2\n2\n2\n")
	outcome, err := a.selectStages(root, next)
	if err != nil {
		t.Fatal(err)
	}
	if !slicesEqual(outcome.Path, []string{"zeta", "token", "remove-field"}) || outcome.Cancelled {
		t.Fatalf("path=%v cancelled=%v", outcome.Path, outcome.Cancelled)
	}
	if out.Len() != 0 {
		t.Fatalf("staged walk wrote stdout: %q", out.String())
	}

	a.in = strings.NewReader("\n")
	cancelled, err := a.selectStages(root, next)
	if err != nil || !cancelled.Cancelled || len(cancelled.Path) != 0 {
		t.Fatalf("outcome=%+v err=%v", cancelled, err)
	}
}

func slicesEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
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
	if !strings.Contains(stderr.String(), `AWS_PROFILE: "" -> "dev"`) || !strings.Contains(stderr.String(), "Apply this environment? [y/N]:") {
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
	if strings.Contains(stderr.String(), "Apply this environment? [y/N]:") {
		t.Fatalf("--yes prompted for confirmation: %q", stderr.String())
	}
}

func TestConfirmationDefaultsToCancelAndSupportsExplicitConfirm(t *testing.T) {
	model := newConfirmModel("Remove selected item?", true)
	cancelled, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := cancelled.(confirmModel); got.confirmed || !got.done {
		t.Fatalf("default confirmation=%v done=%v", got.confirmed, got.done)
	}

	model = newConfirmModel("Remove selected item?", true)
	moved, _ := model.Update(tea.KeyMsg{Type: tea.KeyRight})
	confirmed, _ := moved.(confirmModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := confirmed.(confirmModel); !got.confirmed || !got.done {
		t.Fatalf("explicit confirmation=%v done=%v", got.confirmed, got.done)
	}

	view := newConfirmModel("Remove selected item?", true).View()
	for _, want := range []string{"Confirm action", "Remove selected item?", "[ Cancel ]", "[ Confirm ]", "enter accept"} {
		if !strings.Contains(view, want) {
			t.Fatalf("confirmation view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "\x1b[") {
		t.Fatalf("NO_COLOR confirmation contains ANSI: %q", view)
	}

	for _, size := range []struct{ width, height int }{{24, 8}, {40, 9}, {50, 12}, {80, 20}} {
		long := newConfirmModel("Apply plan "+strings.Repeat("a", 100)+"?", true)
		resized, _ := long.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
		view := resized.(confirmModel).View()
		lines := strings.Split(view, "\n")
		if len(lines) > size.height {
			t.Fatalf("confirmation %dx%d rendered %d lines", size.width, size.height, len(lines))
		}
		for lineNumber, line := range lines {
			if width := ansi.StringWidth(line); width > size.width {
				t.Fatalf("confirmation %dx%d line %d width=%d", size.width, size.height, lineNumber, width)
			}
		}
		for _, want := range []string{"[ Cancel ]", "[ Confirm ]", "y confirm"} {
			if !strings.Contains(view, want) {
				t.Fatalf("confirmation %dx%d missing %q:\n%s", size.width, size.height, want, view)
			}
		}
	}
}

func TestTUIEscapesTerminalControlSequences(t *testing.T) {
	unsafe := "project\x1b]52;c;Y2xpcGJvYXJk\a\u202eevil"
	choice := selectChoice{Value: "stable", Label: unsafe, Description: unsafe, SearchText: unsafe}
	var plain bytes.Buffer
	selected, err := selectOnePlain(strings.NewReader("1\n"), &plain, unsafe, []selectChoice{choice})
	if err != nil || selected != "stable" {
		t.Fatalf("selected=%q err=%v", selected, err)
	}
	queryModel := newBubbleSelectorModelWithColor("Query", []selectChoice{{Value: "stable", Label: "safe"}}, true)
	updated, _ := queryModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(unsafe)})
	queryModel = updated.(bubbleSelectorModel)
	if strings.ContainsAny(queryModel.input.Value(), "\x1b\a") || strings.Contains(queryModel.input.Value(), "\u202e") {
		t.Fatalf("terminal control sequence survived query sanitization: %q", queryModel.input.Value())
	}
	for _, output := range []string{
		plain.String(),
		newBubbleSelectorModelWithColor(unsafe, []selectChoice{choice}, true).View(),
		queryModel.View(),
		newConfirmModel(unsafe, true).View(),
	} {
		if strings.ContainsAny(output, "\x1b\a") || strings.Contains(output, "\u202e") {
			t.Fatalf("terminal control sequence survived sanitization: %q", output)
		}
	}
	if validTMSessionName(unsafe) {
		t.Fatal("tmux session name with terminal controls was accepted")
	}
}

func TestPlainConfirmationKeepsStdoutClean(t *testing.T) {
	a, out, _, _ := testApp(t)
	stderr := new(bytes.Buffer)
	a.err = stderr
	a.in = strings.NewReader("yes\n")
	confirmed, err := a.confirmAction("Proceed?")
	if err != nil || !confirmed {
		t.Fatalf("confirmed=%v err=%v", confirmed, err)
	}
	if out.Len() != 0 {
		t.Fatalf("confirmation wrote stdout: %q", out.String())
	}
	if got, want := stderr.String(), "Proceed? [y/N]: "; got != want {
		t.Fatalf("stderr=%q, want %q", got, want)
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
	if name == "envcheck" {
		fmt.Fprintf(os.Stdout, "%s|%s|%s", os.Getenv("SVC_TOKEN"), os.Getenv("SVC_USER_NAME"), os.Getenv("KEEP"))
		os.Exit(0)
	}
	os.Exit(90)
}

func enableSecHelper(a *App, dir string) {
	a.env = append(a.env,
		"BINBOX_SECRETS_FILE="+filepath.Join(dir, "secrets.json.age"),
		"BINBOX_AGE_KEY="+filepath.Join(dir, "age.key"),
		"GO_WANT_SEC_HELPER=1",
	)
	a.lookPath = func(string) (string, error) { return "helper", nil }
	a.command = func(name string, args ...string) *exec.Cmd {
		return exec.Command(os.Args[0], append([]string{"-test.run=TestSecHelperProcess", "--", name}, args...)...)
	}
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

func TestSecSetPromptsWithoutEchoOnTerminal(t *testing.T) {
	a, out, _, _ := testApp(t)
	dir := t.TempDir()
	enableSecHelper(a, dir)
	if err := a.Run([]string{"sec", "init"}); err != nil {
		t.Fatal(err)
	}

	stderr := new(bytes.Buffer)
	a.err = stderr
	a.in = os.Stdin
	a.isTerminal = func(uintptr) bool { return true }
	a.readPassword = func(uintptr) ([]byte, error) { return []byte("fake-token"), nil }
	if err := a.Run([]string{"sec", "set", "svc", "token"}); err != nil {
		t.Fatal(err)
	}
	if got := stderr.String(); got != "Secret value: \n" || strings.Contains(got, "fake-token") {
		t.Fatalf("terminal prompt=%q", got)
	}
	if out.Len() != 0 {
		t.Fatalf("set wrote stdout: %q", out.String())
	}

	a.in = strings.NewReader("")
	if err := a.Run([]string{"sec", "get", "svc", "token"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "fake-token" {
		t.Fatalf("stored value=%q", got)
	}
}

func TestSecSetProtectsOverwriteAndForceIsExplicit(t *testing.T) {
	a, out, _, _ := testApp(t)
	dir := t.TempDir()
	enableSecHelper(a, dir)
	a.env = append(a.env, "BB_SELECTOR=plain")
	if err := a.Run([]string{"sec", "init"}); err != nil {
		t.Fatal(err)
	}
	a.in = strings.NewReader("old-value\n")
	if err := a.Run([]string{"sec", "set", "svc", "token"}); err != nil {
		t.Fatal(err)
	}

	a.in = strings.NewReader("\n")
	if err := a.Run([]string{"sec", "set", "svc", "token"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("default overwrite err=%v", err)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "get", "svc", "token"}); err != nil || strings.TrimSpace(out.String()) != "old-value" {
		t.Fatalf("cancelled overwrite value=%q err=%v", out.String(), err)
	}

	a.in = strings.NewReader("yes\nconfirmed-value\n")
	if err := a.Run([]string{"sec", "set", "svc", "token"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "get", "svc", "token"}); err != nil || strings.TrimSpace(out.String()) != "confirmed-value" {
		t.Fatalf("confirmed overwrite value=%q err=%v", out.String(), err)
	}

	a.in = strings.NewReader("forced-value\n")
	if err := a.Run([]string{"sec", "set", "svc", "token", "--force"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "get", "svc", "token"}); err != nil || strings.TrimSpace(out.String()) != "forced-value" {
		t.Fatalf("forced overwrite value=%q err=%v", out.String(), err)
	}
}

func TestSecSetRejectsOversizedInputInsteadOfTruncating(t *testing.T) {
	a, _, _, _ := testApp(t)
	a.in = bytes.NewReader(bytes.Repeat([]byte("x"), maxSecretValueBytes+1))
	if _, err := a.readSecretValue(); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("oversized input err=%v", err)
	}
}

func TestSecRejectsMissingTargetsAndEnvironmentCollisions(t *testing.T) {
	a, out, _, _ := testApp(t)
	dir := t.TempDir()
	enableSecHelper(a, dir)
	if err := a.Run([]string{"sec", "init"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"sec", "list", "missing"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("missing service list err=%v", err)
	}
	for _, field := range []string{"api-key", "api_key"} {
		a.in = strings.NewReader(field + "-value\n")
		if err := a.Run([]string{"sec", "set", "svc", field}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Run([]string{"sec", "rm", "svc", "missing", "--yes"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("missing field removal err=%v", err)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "env", "svc"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("environment collision err=%v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("collision emitted partial environment: %q", out.String())
	}
	if got := secretEnvName("1password", "token"); got != "_1PASSWORD_TOKEN" {
		t.Fatalf("numeric environment name=%q", got)
	}
}

func TestSecInitRecoversExistingKeyWhenStoreIsMissing(t *testing.T) {
	a, _, _, _ := testApp(t)
	dir := t.TempDir()
	enableSecHelper(a, dir)
	key := filepath.Join(dir, "age.key")
	if err := os.WriteFile(key, []byte("AGE-SECRET-KEY-TEST\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"sec", "init"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(key)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode=%o", info.Mode().Perm())
	}
	if err := a.Run([]string{"sec", "list"}); err != nil {
		t.Fatal(err)
	}
}

func TestSecRejectsUnsafeAgeKeyPermissions(t *testing.T) {
	a, _, _, _ := testApp(t)
	dir := t.TempDir()
	enableSecHelper(a, dir)
	if err := a.Run([]string{"sec", "init"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "age.key"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"sec", "list"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("unsafe key permissions err=%v", err)
	}
}

func TestSecListEnvAndRemovalLifecycle(t *testing.T) {
	a, out, _, _ := testApp(t)
	dir := t.TempDir()
	enableSecHelper(a, dir)
	if err := a.Run([]string{"sec", "init"}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ field, value string }{{"alpha", "first'value"}, {"beta", "second"}} {
		a.in = strings.NewReader(item.value + "\n")
		if err := a.Run([]string{"sec", "set", "1service", item.field}); err != nil {
			t.Fatal(err)
		}
	}

	out.Reset()
	if err := a.Run([]string{"sec", "list", "1service"}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "alpha\nbeta\n"; got != want {
		t.Fatalf("fields=%q, want %q", got, want)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "env", "1service"}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "export _1SERVICE_ALPHA='first'\"'\"'value'\nexport _1SERVICE_BETA='second'\n"; got != want {
		t.Fatalf("environment=%q, want %q", got, want)
	}

	if err := a.Run([]string{"sec", "rm", "1service", "beta", "--yes"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "get", "1service"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "first'value" {
		t.Fatalf("single-field get=%q", got)
	}
	if err := a.Run([]string{"sec", "rm", "1service", "--yes"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "list"}); err != nil || out.Len() != 0 {
		t.Fatalf("final list=%q err=%v", out.String(), err)
	}
}

func TestSecExecScopesNormalizedValuesToChild(t *testing.T) {
	a, out, _, _ := testApp(t)
	dir := t.TempDir()
	enableSecHelper(a, dir)
	a.env = append(a.env, "SVC_TOKEN=parent-value", "KEEP=present")
	if err := a.Run([]string{"sec", "init"}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ field, value string }{{"token", "child-token"}, {"user-name", "child-user"}} {
		a.in = strings.NewReader(item.value + "\n")
		if err := a.Run([]string{"sec", "set", "svc", item.field}); err != nil {
			t.Fatal(err)
		}
	}
	out.Reset()
	a.in = strings.NewReader("")
	if err := a.Run([]string{"sec", "exec", "svc", "--", "envcheck"}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "child-token|child-user|present"; got != want {
		t.Fatalf("child environment=%q, want %q", got, want)
	}
	if got := a.getenv("SVC_TOKEN"); got != "parent-value" {
		t.Fatalf("parent environment changed to %q", got)
	}
}

func TestSecExecRejectsCollisionBeforeStartingChild(t *testing.T) {
	a, _, _, _ := testApp(t)
	dir := t.TempDir()
	enableSecHelper(a, dir)
	if err := a.Run([]string{"sec", "init"}); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"api-key", "api_key"} {
		a.in = strings.NewReader("value\n")
		if err := a.Run([]string{"sec", "set", "svc", field}); err != nil {
			t.Fatal(err)
		}
	}
	started := false
	helperCommand := a.command
	a.command = func(name string, args ...string) *exec.Cmd {
		if name == "age" || name == "age-keygen" {
			return helperCommand(name, args...)
		}
		started = true
		return exec.Command("false")
	}
	if err := a.Run([]string{"sec", "exec", "svc", "--", "anything"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("collision err=%v", err)
	}
	if started {
		t.Fatal("child started before environment collision was rejected")
	}
}

func TestSecManagerPlainCopyAndReplace(t *testing.T) {
	a, out, _, _ := testApp(t)
	dir := t.TempDir()
	clipboard := filepath.Join(dir, "clipboard")
	enableSecHelper(a, dir)
	a.env = append(a.env, "BB_SELECTOR=plain", "SEC_CLIPBOARD_FILE="+clipboard)
	if err := a.Run([]string{"sec", "init"}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ service, field, value string }{
		{"alpha", "password", "alpha-value"},
		{"zeta", "token", "zeta-value"},
	} {
		a.in = strings.NewReader(item.value + "\n")
		if err := a.Run([]string{"sec", "set", item.service, item.field}); err != nil {
			t.Fatal(err)
		}
	}

	stderr := new(bytes.Buffer)
	a.err = stderr
	a.in = strings.NewReader("2\n1\n1\n")
	out.Reset()
	if err := a.Run([]string{"sec"}); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || strings.Contains(stderr.String(), "zeta-value") {
		t.Fatalf("manager streams stdout=%q stderr=%q", out.String(), stderr.String())
	}
	if got, err := os.ReadFile(clipboard); err != nil || string(got) != "zeta-value" {
		t.Fatalf("clipboard=%q err=%v", got, err)
	}

	stderr.Reset()
	a.in = strings.NewReader("1\n1\n2\nyes\nreplaced-value\n")
	if err := a.Run([]string{"sec"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "get", "alpha", "password"}); err != nil || strings.TrimSpace(out.String()) != "replaced-value" {
		t.Fatalf("manager replace=%q err=%v", out.String(), err)
	}
}

func TestSecRenameFieldPreservesValueAndRejectsConflicts(t *testing.T) {
	a, out, _, _ := testApp(t)
	dir := t.TempDir()
	enableSecHelper(a, dir)
	a.env = append(a.env, "BB_SELECTOR=plain")
	if err := a.Run([]string{"sec", "init"}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ field, value string }{{"old", "secret-value"}, {"existing", "other-value"}} {
		a.in = strings.NewReader(item.value + "\n")
		if err := a.Run([]string{"sec", "set", "svc", item.field}); err != nil {
			t.Fatal(err)
		}
	}

	store, _ := a.secPaths()
	before, err := os.ReadFile(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"sec", "rename", "svc", "old", "existing", "--yes"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("rename conflict err=%v", err)
	}
	after, err := os.ReadFile(store)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("rename conflict rewrote the encrypted store")
	}

	a.in = strings.NewReader("\n")
	if err := a.Run([]string{"sec", "rename", "svc", "old", "renamed"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("cancelled rename err=%v", err)
	}
	afterCancel, err := os.ReadFile(store)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, afterCancel) {
		t.Fatal("cancelled rename rewrote the encrypted store")
	}
	if err := a.Run([]string{"sec", "rename", "svc", "old", "renamed", "--yes"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "get", "svc", "renamed"}); err != nil || strings.TrimSpace(out.String()) != "secret-value" {
		t.Fatalf("renamed value=%q err=%v", out.String(), err)
	}
	if err := a.Run([]string{"sec", "get", "svc", "old"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("old field still resolves: %v", err)
	}
}

func TestSecManagerRenamesSelectedField(t *testing.T) {
	a, out, _, _ := testApp(t)
	stderr := new(bytes.Buffer)
	a.err = stderr
	dir := t.TempDir()
	enableSecHelper(a, dir)
	a.env = append(a.env, "BB_SELECTOR=plain")
	if err := a.Run([]string{"sec", "init"}); err != nil {
		t.Fatal(err)
	}
	a.in = strings.NewReader("sensitive-secret\n")
	if err := a.Run([]string{"sec", "set", "svc", "field"}); err != nil {
		t.Fatal(err)
	}
	a.in = strings.NewReader("1\n1\n3\nrenamed\nyes\n")
	stderr.Reset()
	out.Reset()
	if err := a.Run([]string{"sec"}); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || strings.Contains(stderr.String(), "sensitive-secret") {
		t.Fatalf("rename streams stdout=%q stderr=%q", out.String(), stderr.String())
	}
	out.Reset()
	if err := a.Run([]string{"sec", "get", "svc", "renamed"}); err != nil || strings.TrimSpace(out.String()) != "sensitive-secret" {
		t.Fatalf("manager renamed value=%q err=%v", out.String(), err)
	}
}

func TestSecretChoicesSeparateServicesAndFieldsWithoutValues(t *testing.T) {
	data := secretStore{"service": {"alpha": "not-rendered", "beta": "also-secret"}}
	services := secretServiceChoices(data)
	if len(services) != 1 || services[0].Value != "service" || services[0].Label != "service" || services[0].Description != "2 fields" {
		t.Fatalf("service choices=%+v", services)
	}
	serviceView := newBubbleSelectorModelWithColor("Secret service", services, true).View()
	if !strings.Contains(serviceView, "> service") || !strings.Contains(serviceView, "2 fields") || strings.Contains(serviceView, "not-rendered") || strings.Contains(serviceView, "also-secret") {
		t.Fatalf("service view:\n%s", serviceView)
	}

	fields := secretFieldChoices("service", data["service"])
	if len(fields) != 2 || fields[0].Value != "alpha" || fields[0].Label != "alpha" || fields[1].Value != "beta" {
		t.Fatalf("field choices=%+v", fields)
	}
	fieldView := newBubbleSelectorModelWithColor("Field in service", fields, true).View()
	if !strings.Contains(fieldView, "> alpha") || strings.Contains(fieldView, "not-rendered") || strings.Contains(fieldView, "also-secret") {
		t.Fatalf("field view:\n%s", fieldView)
	}
}

func TestSecManagerPlainRemovalActions(t *testing.T) {
	for _, tc := range []struct {
		name          string
		action        string
		remaining     string
		serviceExists bool
	}{
		{name: "field", action: "4", remaining: "beta\n", serviceExists: true},
		{name: "service", action: "5", serviceExists: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, out, _, _ := testApp(t)
			dir := t.TempDir()
			enableSecHelper(a, dir)
			a.env = append(a.env, "BB_SELECTOR=plain")
			if err := a.Run([]string{"sec", "init"}); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"alpha", "beta"} {
				a.in = strings.NewReader(field + "-value\n")
				if err := a.Run([]string{"sec", "set", "svc", field}); err != nil {
					t.Fatal(err)
				}
			}
			a.in = strings.NewReader("1\n1\n" + tc.action + "\nyes\n")
			if err := a.Run([]string{"sec"}); err != nil {
				t.Fatal(err)
			}
			out.Reset()
			err := a.Run([]string{"sec", "list", "svc"})
			if tc.serviceExists {
				if err != nil || out.String() != tc.remaining {
					t.Fatalf("remaining=%q err=%v", out.String(), err)
				}
			} else if ExitCode(err) != ExitInvalidInvocation {
				t.Fatalf("removed service err=%v", err)
			}
		})
	}
}

func TestSecManagerPlainCancelAtEveryNavigationStage(t *testing.T) {
	a, out, _, _ := testApp(t)
	dir := t.TempDir()
	enableSecHelper(a, dir)
	a.env = append(a.env, "BB_SELECTOR=plain")
	if err := a.Run([]string{"sec", "init"}); err != nil {
		t.Fatal(err)
	}
	a.in = strings.NewReader("value\n")
	if err := a.Run([]string{"sec", "set", "svc", "field"}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"\n", "1\n\n\n", "1\n1\n\n\n\n"} {
		a.in = strings.NewReader(input)
		out.Reset()
		if err := a.Run([]string{"sec"}); err != nil {
			t.Fatalf("input=%q err=%v", input, err)
		}
		if out.Len() != 0 {
			t.Fatalf("cancel input=%q wrote stdout %q", input, out.String())
		}
	}
}

func TestSecManagerEmptyStoreExplainsHowToAdd(t *testing.T) {
	a, _, _, _ := testApp(t)
	dir := t.TempDir()
	enableSecHelper(a, dir)
	if err := a.Run([]string{"sec", "init"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"sec"}); ExitCode(err) != ExitCapabilityUnavailable || !strings.Contains(err.Error(), "bb sec set") {
		t.Fatalf("empty manager err=%v", err)
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
	}{{"alpha", "password", "alpha-secret"}, {"zeta", "password", "zeta-password-secret"}, {"zeta", "token", "zeta-secret"}} {
		a.in = strings.NewReader(item.value + "\n")
		if err := a.Run([]string{"sec", "set", item.service, item.field}); err != nil {
			t.Fatal(err)
		}
	}

	stderr := new(bytes.Buffer)
	a.err = stderr
	a.in = strings.NewReader("2\n2\n")
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
