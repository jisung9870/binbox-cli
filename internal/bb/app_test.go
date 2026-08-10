package bb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func testApp(t *testing.T) (*App, *bytes.Buffer, string, string) {
	t.Helper()
	config := t.TempDir()
	state := t.TempDir()
	out := new(bytes.Buffer)
	a := New(out, new(bytes.Buffer), []string{"XDG_CONFIG_HOME=" + config, "XDG_STATE_HOME=" + state, "HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")})
	a.now = func() time.Time { return time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC) }
	return a, out, config, state
}
func TestVersionAndHelp(t *testing.T) {
	a, out, _, _ := testApp(t)
	if err := a.Run([]string{"version"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != Version {
		t.Fatalf("version=%q", got)
	}
	out.Reset()
	if err := a.Run([]string{"help"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "mcp inventory|audit") {
		t.Fatal("help missing mcp")
	}
}
func TestProjectAndSessionPersistInXDG(t *testing.T) {
	a, out, config, state := testApp(t)
	project := t.TempDir()
	if err := a.Run([]string{"project", "add", project, "demo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(config, "bb", "projects.json")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"session", "start", "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"session", "stop", "demo"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(state, "bb", "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "stopped_at") {
		t.Fatal("session was not stopped")
	}
}

func TestEmptyProjectAndSessionListsAreArrays(t *testing.T) {
	a, out, _, _ := testApp(t)
	if err := a.Run([]string{"project", "list"}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("project list=%q", out.String())
	}
	out.Reset()
	if err := a.Run([]string{"session", "list"}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("session list=%q", out.String())
	}
}
func TestRunJournalRedacts(t *testing.T) {
	a, _, _, state := testApp(t)
	if err := a.Run([]string{"run", "sh", "-c", "echo TOKEN=topsecret"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(state, "bb", "journal.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "topsecret") {
		t.Fatalf("journal leaked secret: %s", b)
	}
	if !strings.Contains(string(b), `"argument_count":2`) {
		t.Fatalf("journal did not retain safe execution metadata: %s", b)
	}
}

func TestDoctorChecksDocumentedExternalDependencies(t *testing.T) {
	a, out, _, _ := testApp(t)
	if err := a.Run([]string{"doctor", "--json"}); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"git", "tmux", "kubectl", "aws", "terraform", "orca"} {
		if !strings.Contains(out.String(), `"command":"`+command+`"`) {
			t.Fatalf("doctor output missing %s: %s", command, out.String())
		}
	}
}

func TestDoctorJSONPreservesChecksAndAddsWorkbenchCapabilities(t *testing.T) {
	a, out, _, _ := testApp(t)
	a.lookPath = func(name string) (string, error) {
		if name == "git" {
			return "/usr/bin/git", nil
		}
		return "", os.ErrNotExist
	}
	if err := a.Run([]string{"doctor", "--json"}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		SchemaVersion int `json:"schema_version"`
		Data          struct {
			Checks       []json.RawMessage `json:"checks"`
			Capabilities []struct {
				Name        string  `json:"name"`
				Scope       string  `json:"scope"`
				Description string  `json:"description"`
				Available   bool    `json:"available"`
				Path        *string `json:"path"`
				Recovery    *string `json:"recovery"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 1 || len(got.Data.Checks) != 14 || len(got.Data.Capabilities) != 14 {
		t.Fatalf("doctor shape=%s", out.String())
	}
	if got.Data.Capabilities[0].Name != "git" || got.Data.Capabilities[0].Scope != "core" || !got.Data.Capabilities[0].Available || got.Data.Capabilities[0].Path == nil || *got.Data.Capabilities[0].Path != "/usr/bin/git" || got.Data.Capabilities[0].Recovery != nil {
		t.Fatalf("git capability=%+v", got.Data.Capabilities[0])
	}
	if got.Data.Capabilities[1].Name != "tmux" || got.Data.Capabilities[1].Scope != "optional" || got.Data.Capabilities[1].Available || got.Data.Capabilities[1].Path != nil || got.Data.Capabilities[1].Recovery == nil {
		t.Fatalf("tmux capability=%+v", got.Data.Capabilities[1])
	}
	if got.Data.Capabilities[6].Name != "docker" || got.Data.Capabilities[13].Name != "tf-summarize" {
		t.Fatalf("extended capabilities=%+v", got.Data.Capabilities)
	}
}

func TestTMProjectsUsesLazyVimEnvelope(t *testing.T) {
	a, out, _, _ := testApp(t)
	project := t.TempDir()
	if err := a.Run([]string{"project", "add", project, "demo"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"tm", "projects", "--json"}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		SchemaVersion int  `json:"schema_version"`
		OK            bool `json:"ok"`
		Data          struct {
			Projects []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Path string `json:"path"`
			} `json:"projects"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != SchemaVersion || !got.OK || len(got.Data.Projects) != 1 || got.Data.Projects[0].Path != project || got.Data.Projects[0].ID != projectID(project) {
		t.Fatalf("projects=%s", out.String())
	}
}

func TestTMExplicitProjectUsesTmuxWithoutFZFOrOrca(t *testing.T) {
	a, out, _, _ := testApp(t)
	project := t.TempDir()
	if err := a.Run([]string{"project", "add", project, "demo"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	var requested []string
	a.lookPath = func(name string) (string, error) {
		if name == "tmux" {
			return "/test/tmux", nil
		}
		return "", os.ErrNotExist
	}
	a.command = func(name string, args ...string) *exec.Cmd {
		requested = append([]string{name}, args...)
		return exec.Command("true")
	}
	if err := a.Run([]string{"tm", "--project", projectID(project)}); err != nil {
		t.Fatal(err)
	}
	want := []string{"tmux", "new-session", "-A", "-s", "bb-" + projectID(project), "-c", project}
	if strings.Join(requested, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("tmux request=%q want=%q", requested, want)
	}
	if out.Len() != 0 {
		t.Fatalf("unexpected output=%q", out.String())
	}
}

func TestTMExplicitProjectInsideTmuxCreatesAndSwitches(t *testing.T) {
	a, out, _, _ := testApp(t)
	a.env = append(a.env, "TMUX=/tmp/tmux-1/default,1,0")
	project := t.TempDir()
	if err := a.Run([]string{"project", "add", project, "demo"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	var requests [][]string
	a.lookPath = func(name string) (string, error) {
		if name == "tmux" {
			return "/test/tmux", nil
		}
		return "", os.ErrNotExist
	}
	a.command = func(name string, args ...string) *exec.Cmd {
		request := append([]string{name}, args...)
		requests = append(requests, request)
		if len(args) > 0 && args[0] == "has-session" {
			return exec.Command("false")
		}
		return exec.Command("true")
	}
	if err := a.Run([]string{"tm", "--project", projectID(project)}); err != nil {
		t.Fatal(err)
	}
	session := "bb-" + projectID(project)
	want := [][]string{
		{"tmux", "has-session", "-t", session},
		{"tmux", "new-session", "-d", "-s", session, "-c", project},
		{"tmux", "switch-client", "-t", session},
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("tmux requests=%q want=%q", requests, want)
	}
}

func TestAgentsPointsToOrcaOwnership(t *testing.T) {
	a, _, _, _ := testApp(t)
	err := a.Run([]string{"agents"})
	if ExitCode(err) != ExitCapabilityUnavailable || !strings.Contains(err.Error(), "Orca") {
		t.Fatalf("agents err=%v", err)
	}
}

func TestTMUnavailableErrorsAreActionable(t *testing.T) {
	a, _, _, _ := testApp(t)
	project := t.TempDir()
	if err := a.Run([]string{"project", "add", project, "demo"}); err != nil {
		t.Fatal(err)
	}
	a.lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	a.in = strings.NewReader("1\n")
	if err := a.Run([]string{"tm"}); ExitCode(err) != ExitCapabilityUnavailable || !strings.Contains(err.Error(), "tmux is not installed") {
		t.Fatalf("tmux error=%v", err)
	}
	if err := a.Run([]string{"tm", "--project", projectID(project)}); ExitCode(err) != ExitCapabilityUnavailable || !strings.Contains(err.Error(), "tmux is not installed") {
		t.Fatalf("tmux error=%v", err)
	}
}

func TestMCPInventoryDoesNotExposeConfigContent(t *testing.T) {
	a, out, config, _ := testApp(t)
	path := filepath.Join(config, "bb", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"token":"topsecret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"mcp", "inventory"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "topsecret") || !strings.Contains(out.String(), `"content_inspected":false`) {
		t.Fatalf("unsafe MCP inventory: %s", out.String())
	}
}
func TestExportProducesJSON(t *testing.T) {
	a, out, _, _ := testApp(t)
	if err := a.Run([]string{"run", "true"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"export"}); err != nil {
		t.Fatal(err)
	}
	var events []journalEvent
	if err := json.Unmarshal(out.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "run" {
		t.Fatalf("events=%+v", events)
	}
}
func TestRedactNestedValues(t *testing.T) {
	got := redact(map[string]any{"token": "abc", "nested": []any{"Authorization: Bearer sk-123"}}).(map[string]any)
	if got["token"] != "[REDACTED]" {
		t.Fatal("key not redacted")
	}
	if got["nested"].([]any)[0] != "Authorization: [REDACTED]" {
		t.Fatalf("text=%v", got["nested"])
	}
}

func TestJSONEnvelopeAndInvalidExitCode(t *testing.T) {
	a, out, _, _ := testApp(t)
	err := a.Run([]string{"project", "unknown", "--json"})
	if err == nil || ExitCode(err) != ExitInvalidInvocation || !Reported(err) {
		t.Fatalf("err=%v exit=%d reported=%v", err, ExitCode(err), Reported(err))
	}
	var got envelope
	if decodeErr := json.Unmarshal(out.Bytes(), &got); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if got.SchemaVersion != SchemaVersion || got.OK || got.Error == nil || got.Error.Code != "invalid_invocation" {
		t.Fatalf("envelope=%+v", got)
	}
}

func TestRunSubcommandJSONErrorUsesEnvelope(t *testing.T) {
	a, out, _, _ := testApp(t)
	err := a.Run([]string{"run", "show", "missing", "--json"})
	if err == nil || ExitCode(err) != ExitOperational || !Reported(err) {
		t.Fatalf("err=%v exit=%d reported=%v", err, ExitCode(err), Reported(err))
	}
	var got envelope
	if decodeErr := json.Unmarshal(out.Bytes(), &got); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if got.OK || got.Error == nil || got.Error.Code != "operational_error" {
		t.Fatalf("envelope=%+v", got)
	}
}

func TestSubcommandHelp(t *testing.T) {
	commands := [][]string{{"version"}, {"doctor"}, {"project"}, {"session"}, {"run"}, {"mcp"}, {"export"}, {"orca"}}
	for _, command := range commands {
		t.Run(command[0], func(t *testing.T) {
			a, out, _, _ := testApp(t)
			if err := a.Run(append(command, "--help")); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "Usage:") {
				t.Fatalf("help=%q", out.String())
			}
		})
	}
}

func TestStableProjectAndSessionIDs(t *testing.T) {
	a, out, _, _ := testApp(t)
	project := t.TempDir()
	if err := a.Run([]string{"project", "add", project, "demo", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"id":"`+projectID(project)+`"`) {
		t.Fatalf("project envelope=%s", out.String())
	}
	out.Reset()
	if err := a.Run([]string{"session", "start", "demo", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"id":"ses_`) {
		t.Fatalf("session envelope=%s", out.String())
	}
}

func TestConcurrentProjectWritesAreSerializedAndAtomic(t *testing.T) {
	config := t.TempDir()
	state := t.TempDir()
	env := []string{"XDG_CONFIG_HOME=" + config, "XDG_STATE_HOME=" + state, "HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}
	paths := make([]string, 12)
	for i := range paths {
		paths[i] = t.TempDir()
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(paths))
	for i, path := range paths {
		wg.Add(1)
		go func(i int, path string) {
			defer wg.Done()
			a := New(io.Discard, io.Discard, env)
			errs <- a.Run([]string{"project", "add", path, fmt.Sprintf("project-%02d", i)})
		}(i, path)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	registry := filepath.Join(config, "bb", "projects.json")
	records, err := loadProjects(registry)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != len(paths) {
		t.Fatalf("records=%d want=%d", len(records), len(paths))
	}
	info, err := os.Stat(registry)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(registry), ".bb-write-*"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("temporary files=%v err=%v", temps, err)
	}
}

func TestSessionizerCheckFixtureIsReadOnly(t *testing.T) {
	a, out, config, _ := testApp(t)
	root := t.TempDir()
	for _, dir := range []string{"parent/alpha", "parent/space project", "parent/.hidden", "direct"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fixture, err := os.ReadFile(filepath.Join("testdata", "sessionizer", "dirs"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "dirs")
	fixture = []byte(strings.ReplaceAll(string(fixture), "__ROOT__", root))
	if err := os.WriteFile(source, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"project", "add", filepath.Join(root, "direct"), "direct"}); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(config, "bb", "projects.json")
	registryBefore, err := os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"project", "import", "sessionizer", "--check", "--file", source, "--json"}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		SchemaVersion int              `json:"schema_version"`
		OK            bool             `json:"ok"`
		Data          sessionizerCheck `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != SchemaVersion || !got.OK || !got.Data.ReadOnly || got.Data.Mode != "check" {
		t.Fatalf("envelope=%+v", got)
	}
	names := make([]string, 0, len(got.Data.Candidates))
	foundConflict := false
	for _, candidate := range got.Data.Candidates {
		names = append(names, candidate.Name)
		if candidate.Name == "direct" && candidate.Conflict == "already_registered_path" {
			foundConflict = true
		}
	}
	sort.Strings(names)
	expectedBytes, err := os.ReadFile(filepath.Join("testdata", "sessionizer", "expected-names"))
	if err != nil {
		t.Fatal(err)
	}
	expected := strings.FieldsFunc(strings.TrimSpace(string(expectedBytes)), func(r rune) bool { return r == '\n' })
	if strings.Join(names, "\n") != strings.Join(expected, "\n") || !foundConflict {
		t.Fatalf("names=%v conflict=%v", names, foundConflict)
	}
	if len(got.Data.Warnings) < 3 {
		t.Fatalf("warnings=%v", got.Data.Warnings)
	}
	sourceAfter, _ := os.ReadFile(source)
	registryAfter, _ := os.ReadFile(registry)
	if !bytes.Equal(sourceAfter, fixture) || !bytes.Equal(registryAfter, registryBefore) {
		t.Fatal("check-only import mutated source or registry")
	}
}

func TestSessionizerApplyIsIdempotentAndKeepsLegacyBytes(t *testing.T) {
	a, out, config, state := testApp(t)
	root := t.TempDir()
	for _, name := range []string{"one", "two"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(root, "dirs")
	legacy := []byte("# untouched legacy grammar\n" + root + "\n")
	if err := os.WriteFile(source, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		out.Reset()
		if err := a.Run([]string{"project", "import", "sessionizer", "--apply", "--file", source, "--json"}); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := os.ReadFile(source); err != nil || !bytes.Equal(got, legacy) {
		t.Fatalf("legacy changed: %q err=%v", got, err)
	}
	records, err := loadProjects(filepath.Join(config, "bb", "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Origin.Kind != "sessionizer" || records[0].Origin.Source != source {
		t.Fatalf("records=%+v", records)
	}
	backups, err := filepath.Glob(filepath.Join(state, "bb", "migration-backups", "*.dirs"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v err=%v", backups, err)
	}
	if got, _ := os.ReadFile(backups[0]); !bytes.Equal(got, legacy) {
		t.Fatal("backup is not byte-identical")
	}
	if _, err := os.Stat(filepath.Join(state, "bb", "migration-backups", "sessionizer-recovery.json")); err != nil {
		t.Fatal(err)
	}
}

func TestProjectShowSessionOpenAndRunJournalCommands(t *testing.T) {
	a, out, _, _ := testApp(t)
	project := t.TempDir()
	if err := a.Run([]string{"project", "add", project, "demo"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"project", "show", projectID(project), "--json"}); err != nil || !strings.Contains(out.String(), `"name":"demo"`) {
		t.Fatalf("show=%s err=%v", out, err)
	}
	out.Reset()
	if err := a.Run([]string{"session", "open", projectID(project), "--backend", "shell", "--json"}); err != nil || !strings.Contains(out.String(), `"external_action":"none"`) {
		t.Fatalf("open=%s err=%v", out, err)
	}
	if err := a.Run([]string{"session", "open", projectID(project), "--backend", "orca"}); ExitCode(err) != ExitCapabilityUnavailable {
		t.Fatalf("orca err=%v", err)
	}
	if err := a.Run([]string{"run", "true"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.Run([]string{"run", "list", "--json"}); err != nil {
		t.Fatal(err)
	}
	var listed envelope
	if err := json.Unmarshal(out.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	runs := listed.Data.([]any)
	if len(runs) != 1 {
		t.Fatalf("runs=%v", runs)
	}
	id := runs[0].(map[string]any)["id"].(string)
	out.Reset()
	if err := a.Run([]string{"run", "show", id, "--json"}); err != nil || !strings.Contains(out.String(), id) {
		t.Fatalf("show run=%s err=%v", out, err)
	}
	out.Reset()
	if err := a.Run([]string{"run", "export", "--format", "json"}); err != nil || !strings.Contains(out.String(), id) {
		t.Fatalf("export=%s err=%v", out, err)
	}
}

func TestSessionizerMalformedInputIsWarningOnlyDuringCheck(t *testing.T) {
	a, out, config, _ := testApp(t)
	source := filepath.Join(t.TempDir(), "dirs")
	if err := os.WriteFile(source, []byte("~other-user/work\n=\n/path/that/does/not/exist\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"project", "import", "sessionizer", "--check", "--file", source, "--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"warnings"`) || strings.Contains(out.String(), `"candidates":[{`) {
		t.Fatalf("check=%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(config, "bb", "projects.json")); !os.IsNotExist(err) {
		t.Fatalf("check wrote registry: %v", err)
	}
}

func TestConcurrentSessionizerApplyAndRunsKeepUniqueRecords(t *testing.T) {
	config, state, home := t.TempDir(), t.TempDir(), t.TempDir()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "project"), 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "dirs")
	if err := os.WriteFile(source, []byte(root+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"XDG_CONFIG_HOME=" + config, "XDG_STATE_HOME=" + state, "HOME=" + home, "PATH=" + os.Getenv("PATH")}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- New(io.Discard, io.Discard, env).Run([]string{"project", "import", "sessionizer", "--apply", "--file", source})
		}()
	}
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- New(io.Discard, io.Discard, env).Run([]string{"run", "true"}) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	records, err := loadProjects(filepath.Join(config, "bb", "projects.json"))
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	events, err := readJournal(filepath.Join(state, "bb", "journal.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, event := range events {
		if event.Type == "run" {
			if event.ID == "" || ids[event.ID] {
				t.Fatalf("duplicate/empty run id %q", event.ID)
			}
			ids[event.ID] = true
		}
	}
	if len(ids) != 6 {
		t.Fatalf("run ids=%v", ids)
	}
}

func TestSessionizerApplyRejectsSourceChangedAfterCheck(t *testing.T) {
	a, _, config, state := testApp(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "project"), 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "dirs")
	if err := os.WriteFile(source, []byte(root+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	check, err := a.checkSessionizer(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("# changed\n"+root+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = a.applySessionizer(check, filepath.Join(config, "bb", "projects.json"), filepath.Join(state, "bb"))
	if err == nil || !strings.Contains(err.Error(), "source changed") {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(config, "bb", "projects.json")); !os.IsNotExist(statErr) {
		t.Fatalf("registry changed: %v", statErr)
	}
}

func TestRunPreservesCommandJSONArgument(t *testing.T) {
	a, _, _, _ := testApp(t)
	if err := a.Run([]string{"run", "sh", "-c", `test "$1" = --json`, "sh", "--json"}); err != nil {
		t.Fatal(err)
	}
}
