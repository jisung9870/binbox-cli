package bb

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecHardenClaudeAddsDenyRulesAndKeepsSettings(t *testing.T) {
	a, out, _, _ := testApp(t)
	dir := t.TempDir()
	a.env = append(a.env, "CLAUDE_CONFIG_DIR="+dir)
	path := filepath.Join(dir, "settings.json")
	original := `{
  "model": "opus",
  "permissions": {
    "allow": ["Bash(ls:*)"],
    "deny": ["Bash(rm -rf:*)"]
  },
  "theme": "dark"
}
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := a.Run([]string{"sec", "harden", "claude", "--print"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "+ Bash(bb sec get:*)") {
		t.Fatalf("print=%s", out.String())
	}
	if b, _ := os.ReadFile(path); string(b) != original {
		t.Fatalf("--print wrote the file: %s", b)
	}

	a.in = strings.NewReader("n\n")
	if err := a.Run([]string{"sec", "harden", "claude"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("declined harden err=%v", err)
	}
	if err := a.Run([]string{"sec", "harden", "claude", "--yes"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	// Key order survives: model, permissions, theme.
	if !(strings.Index(text, `"model"`) < strings.Index(text, `"permissions"`) && strings.Index(text, `"permissions"`) < strings.Index(text, `"theme"`)) {
		t.Fatalf("settings reordered: %s", text)
	}
	var settings struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(b, &settings); err != nil {
		t.Fatal(err)
	}
	_, key := a.secPaths()
	for _, want := range []string{"Bash(rm -rf:*)", "Bash(bb sec get:*)", "Bash(bb mcp add:*)", "Bash(bb mcp allow:*)", "Bash(bb sec exec:*)", "Bash(bb mcp header:*)", "Bash(*BB_ALLOW_SECRET_OUTPUT*)", "Read(/" + key + ")"} {
		if !containsString(settings.Permissions.Deny, want) {
			t.Fatalf("deny missing %q: %q", want, settings.Permissions.Deny)
		}
	}
	if len(settings.Permissions.Allow) != 1 || settings.Permissions.Deny[0] != "Bash(rm -rf:*)" {
		t.Fatalf("existing rules changed: %+v", settings.Permissions)
	}

	// A second run finds nothing to add and leaves the file alone.
	before := text
	out.Reset()
	if err := a.Run([]string{"sec", "harden", "claude", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != before || out.Len() != 0 {
		t.Fatalf("second run changed something: out=%q", out.String())
	}
}

func TestSecHardenClaudeCreatesSettingsAndRejectsBrokenJSON(t *testing.T) {
	a, _, _, _ := testApp(t)
	dir := t.TempDir()
	a.env = append(a.env, "CLAUDE_CONFIG_DIR="+dir)
	if err := a.Run([]string{"sec", "harden", "claude", "--yes"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil || !strings.Contains(string(b), "Bash(bb sec copy:*)") {
		t.Fatalf("settings=%s err=%v", b, err)
	}
	for _, broken := range []string{"{broken", `{"permissions":{},"permissions":{}}`, `{"a":1}{"b":2}`, `{"permissions":[]}`} {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(broken), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := a.Run([]string{"sec", "harden", "claude", "--yes"}); err == nil {
			t.Fatalf("%s was rewritten", broken)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "settings.json")); string(b) != broken {
			t.Fatalf("broken settings changed: %s", b)
		}
	}
	// Operator rules keep their characters instead of \u0026 escapes.
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"permissions":{"deny":["Bash(a && b)"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"sec", "harden", "claude", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "settings.json")); !strings.Contains(string(b), `"Bash(a && b)"`) {
		t.Fatalf("operator rule escaped: %s", b)
	}
}

func TestSecHardenCodexWritesManagedRulesFile(t *testing.T) {
	a, out, _, _ := testApp(t)
	home := t.TempDir()
	a.env = append(a.env, "CODEX_HOME="+home)
	if err := a.Run([]string{"sec", "harden", "codex", "--yes"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, "rules", "bb-sec.rules"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `prefix_rule(pattern=["bb", "sec", "get"], decision="forbidden")`) {
		t.Fatalf("rules=%s", b)
	}
	out.Reset()
	if err := a.Run([]string{"sec", "harden", "codex", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("up-to-date rules were rewritten: %s", out.String())
	}
	if err := a.Run([]string{"sec", "harden", "vim"}); ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("unknown client err=%v", err)
	}
}

func TestSecHardenWritesThroughASymlinkedSettingsFile(t *testing.T) {
	a, _, _, _ := testApp(t)
	dir, dotfiles := t.TempDir(), t.TempDir()
	managed := filepath.Join(dotfiles, "claude-settings.json")
	if err := os.WriteFile(managed, []byte(`{"model":"opus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink(managed, link); err != nil {
		t.Fatal(err)
	}
	a.env = append(a.env, "CLAUDE_CONFIG_DIR="+dir)
	if err := a.Run([]string{"sec", "harden", "claude", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced: %v", err)
	}
	if b, _ := os.ReadFile(managed); !strings.Contains(string(b), "Bash(bb sec get:*)") || !strings.Contains(string(b), `"model": "opus"`) {
		t.Fatalf("managed file=%s", b)
	}
}

func TestSecHardenRefusesADanglingSymlink(t *testing.T) {
	a, _, _, _ := testApp(t)
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "missing", "settings.json"), filepath.Join(dir, "settings.json")); err != nil {
		t.Fatal(err)
	}
	a.env = append(a.env, "CLAUDE_CONFIG_DIR="+dir)
	if err := a.Run([]string{"sec", "harden", "claude", "--yes"}); err == nil {
		t.Fatal("dangling symlink was replaced")
	}
}
