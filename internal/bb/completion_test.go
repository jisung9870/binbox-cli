package bb

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestZshCompletionIsNativeDynamicAndOmitsGitCommands(t *testing.T) {
	a, out, _, _ := testApp(t)
	if err := a.Run([]string{"completion", "zsh"}); err != nil {
		t.Fatal(err)
	}
	completion := out.String()
	for _, want := range []string{"_bb()", "compdef _bb bb", "command bb completion candidates", "secret-service", "tmux-session"} {
		if !strings.Contains(completion, want) {
			t.Fatalf("completion missing %q", want)
		}
	}
	for _, forbidden := range []string{"'git:", "'gx:"} {
		if strings.Contains(completion, forbidden) {
			t.Fatalf("completion includes excluded Git command %q", forbidden)
		}
	}
}

func TestCompletionCandidatesUseSafeLocalMetadata(t *testing.T) {
	a, out, config, state := testApp(t)
	if err := a.Run([]string{"wenv", "set", "dev", "APP_MODE=local"}); err != nil {
		t.Fatal(err)
	}
	awsConfig := filepath.Join(a.getenv("HOME"), ".aws", "config")
	if err := os.MkdirAll(filepath.Dir(awsConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(awsConfig, []byte("[sso-session corp]\nsso_start_url = https://example.awsapps.com/start\nsso_region = ap-northeast-2\n\n[profile work]\nsso_session = corp\nregion = ap-northeast-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	projectPath := t.TempDir()
	if err := a.Run([]string{"project", "add", projectPath, "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"session", "start", "focus"}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		kind string
		want []string
	}{
		{kind: "wenv", want: []string{"dev"}},
		{kind: "profile", want: []string{"work"}},
		{kind: "sso-session", want: []string{"corp"}},
		{kind: "project", want: []string{"demo", projectID(projectPath)}},
		{kind: "session", want: []string{"focus"}},
	} {
		out.Reset()
		if err := a.Run([]string{"completion", "candidates", tc.kind}); err != nil {
			t.Fatalf("kind=%s err=%v", tc.kind, err)
		}
		for _, want := range tc.want {
			if !strings.Contains(out.String(), want+"\n") {
				t.Fatalf("kind=%s candidates=%q missing %q", tc.kind, out.String(), want)
			}
		}
	}

	if _, err := os.Stat(filepath.Join(config, "bb", "wenv.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, "bb", "sessions.json")); err != nil {
		t.Fatal(err)
	}
}

func TestSecretCompletionReturnsNamesWithoutValues(t *testing.T) {
	a, out, _, _ := testApp(t)
	dir := t.TempDir()
	enableSecHelper(a, dir)
	if err := a.Run([]string{"sec", "init"}); err != nil {
		t.Fatal(err)
	}
	a.in = strings.NewReader("sensitive-value\n")
	if err := a.Run([]string{"sec", "set", "service", "token"}); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"completion", "candidates", "secret-service"},
		{"completion", "candidates", "secret-field", "service"},
	} {
		out.Reset()
		if err := a.Run(args); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "sensitive-value") {
			t.Fatalf("completion leaked value: %q", out.String())
		}
	}
	if got := out.String(); got != "token\n" {
		t.Fatalf("field candidates=%q", got)
	}
}

func TestCompletionCandidatesRejectTerminalControlsAndDuplicates(t *testing.T) {
	got := safeCompletionCandidates([]string{"safe", "bad\nname", "bad\x1bname", "safe", ""})
	if len(got) != 1 || got[0] != "safe" {
		t.Fatalf("safe candidates=%q", got)
	}
}

func TestHumanOutputRendersNestedTablesAndSanitizesControls(t *testing.T) {
	var out bytes.Buffer
	value := map[string]any{
		"read_only": true,
		"items": []map[string]any{
			{"name": "alpha", "path": "/safe"},
			{"name": "bad\x1b]52;c;payload", "path": "/other"},
		},
	}
	if err := printHuman(&out, value); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Read Only:", "Items:", "Name", "Path", "alpha", "/safe"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("human output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "\x1b") || strings.Contains(out.String(), "payload]") {
		t.Fatalf("human output contains terminal control data: %q", out.String())
	}
}
