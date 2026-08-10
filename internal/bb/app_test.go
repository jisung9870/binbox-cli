package bb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
