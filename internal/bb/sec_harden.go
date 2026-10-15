package bb

// bb sec harden adds deny rules that keep a coding agent's shell away from the
// commands that print secret values. bb's own gates stop accidents; these rules
// are the agent client's half, enforced before a command runs. Both are
// prefix matches over command text, so they narrow the easy paths rather than
// contain a determined same-user process.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode/utf8"
)

const codexHardenRulesName = "bb-sec.rules"

// hardenCommandPrefixes are argv prefixes an agent shell must not run. The
// exec forms are included because the agent chooses the child command and can
// print its environment; agents use the MCP sec_exec/wenv_exec tools instead,
// which are allowlisted and redact values from output.
func (a *App) hardenCommandPrefixes() [][]string {
	launchers := []string{"bb"}
	if path, err := a.bbLauncherPath(); err == nil && path != "bb" && !strings.ContainsAny(path, " \t") {
		launchers = append(launchers, path)
	}
	prefixes := [][]string{}
	for _, bb := range launchers {
		for _, sub := range [][]string{
			{"sec", "get"}, {"sec", "env"}, {"sec", "copy"}, {"sec", "exec"},
			{"wenv", "apply"}, {"wenv", "export"}, {"wenv", "exec"},
			{"mcp", "header"},
			// An agent that can register a server or widen the allowlist can
			// launch a command of its choosing with a token; those changes
			// belong to the operator.
			{"mcp", "add"}, {"mcp", "edit"}, {"mcp", "launch"}, {"mcp", "allow"}, {"mcp", "adopt"},
		} {
			prefixes = append(prefixes, append([]string{bb}, sub...))
		}
	}
	prefixes = append(prefixes,
		[]string{"env", secretOutputOverrideEnv + "=1"},
		[]string{"age", "-d"}, []string{"age", "--decrypt"},
	)
	if runtime.GOOS == "darwin" {
		// BSD ps prints each process environment with -E or the e keyword.
		for _, flags := range []string{"-E", "-Eww", "-wwE", "eww", "auxe", "auxeww"} {
			prefixes = append(prefixes, []string{"ps", flags})
		}
	} else {
		for _, flags := range []string{"e", "eww", "auxe", "auxeww"} {
			prefixes = append(prefixes, []string{"ps", flags})
		}
	}
	_, key := a.secPaths()
	if !strings.ContainsAny(key, " \t") {
		prefixes = append(prefixes, []string{"cat", key})
	}
	// A token that cannot be written as a plain rule string is left out rather
	// than risk a rules file the client refuses to load.
	kept := prefixes[:0]
	for _, prefix := range prefixes {
		if printableRuleTokens(prefix) {
			kept = append(kept, prefix)
		}
	}
	return kept
}

func printableRuleTokens(tokens []string) bool {
	for _, token := range tokens {
		if !utf8.ValidString(token) || strings.ContainsFunc(token, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return false
		}
	}
	return true
}

func (a *App) claudeHardenRules() []string {
	rules := []string{}
	for _, prefix := range a.hardenCommandPrefixes() {
		rules = append(rules, "Bash("+strings.Join(prefix, " ")+":*)")
	}
	// An inline assignment comes before the command name, so it needs a
	// wildcard rather than a prefix.
	rules = append(rules, "Bash(*"+secretOutputOverrideEnv+"*)")
	_, key := a.secPaths()
	// Claude writes absolute paths in permission rules with a leading "//".
	rules = append(rules, "Read(/"+key+")")
	// The registries decide what runs with which token; the agent's own file
	// tools must not rewrite them either.
	if config, _, err := a.paths(); err == nil {
		for _, name := range []string{"mcp.json", "mcp-serve.json"} {
			rules = append(rules, "Edit(/"+filepath.Join(config, name)+")")
		}
	}
	// The metadata holds verify targets, which decide where a token is sent.
	rules = append(rules, "Edit(/"+a.secMetaPath()+")")
	if runtime.GOOS == "linux" {
		rules = append(rules, "Read(//proc/**/environ)")
	}
	return rules
}

func (a *App) codexHardenRules() string {
	var b strings.Builder
	b.WriteString("# Managed by bb sec harden; regenerate with: bb sec harden codex\n")
	b.WriteString("# Keeps agent shells away from commands that print bb secret values.\n")
	for _, prefix := range a.hardenCommandPrefixes() {
		quoted := make([]string, len(prefix))
		for i, token := range prefix {
			quoted[i] = strconv.Quote(token)
		}
		fmt.Fprintf(&b, "prefix_rule(pattern=[%s], decision=\"forbidden\")\n", strings.Join(quoted, ", "))
	}
	return b.String()
}

func (a *App) claudeSettingsPath() string {
	if dir := a.getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "settings.json")
	}
	return filepath.Join(a.getenv("HOME"), ".claude", "settings.json")
}

func (a *App) codexRulesPath() string {
	home := a.getenv("CODEX_HOME")
	if home == "" {
		home = filepath.Join(a.getenv("HOME"), ".codex")
	}
	return filepath.Join(home, "rules", codexHardenRulesName)
}

func (a *App) secHarden(args []string) error {
	args, printOnly := takeFlag(args, "--print")
	args, yes := takeFlag(args, "--yes")
	if len(args) != 1 || (args[0] != "claude" && args[0] != "codex") {
		return usage("sec harden", "claude|codex [--print] [--yes]")
	}
	var path string
	var added []string
	var content []byte
	switch args[0] {
	case "claude":
		path = a.claudeSettingsPath()
		current, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("read Claude settings: %w", err)
		}
		content, added, err = addClaudeDenyRules(current, a.claudeHardenRules())
		if err != nil {
			return err
		}
	case "codex":
		path = a.codexRulesPath()
		content = []byte(a.codexHardenRules())
		current, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("read Codex rules: %w", err)
		}
		if !bytes.Equal(current, content) {
			for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
				if !strings.HasPrefix(line, "#") && !bytes.Contains(current, []byte(line)) {
					added = append(added, line)
				}
			}
			if len(added) == 0 {
				// Only comments or dropped rules differ.
				added = []string{"(no new rules; the managed file is rewritten)"}
			}
			if len(current) > 0 {
				fmt.Fprintf(a.err, "%s is managed by bb and will be replaced; keep your own rules in another file\n", safeTerminalText(path))
			}
		}
	}
	if len(added) == 0 {
		_, err := fmt.Fprintf(a.err, "%s already has the bb deny rules\n", safeTerminalText(path))
		return err
	}
	fmt.Fprintf(a.out, "%s: %d deny rule(s) to add\n", safeTerminalText(path), len(added))
	for _, rule := range added {
		fmt.Fprintln(a.out, "  + "+safeTerminalText(rule))
	}
	if printOnly {
		return nil
	}
	if !yes {
		ok, err := a.confirmAction("Add these deny rules to " + path + "?")
		if err != nil {
			return err
		}
		if !ok {
			return invalid("harden cancelled")
		}
	}
	// Dotfile managers often link these files; write through the link so the
	// managed copy changes instead of being replaced by a regular file.
	target := path
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("%s is a symlink bb cannot resolve: %w", path, err)
		}
		target = resolved
	}
	if err := writeBytesAtomic(target, content); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	fmt.Fprintf(a.err, "Updated %s; restart running %s sessions to apply it\n", safeTerminalText(path), args[0])
	return nil
}

type jsonMember struct {
	key   string
	value json.RawMessage
}

// decodeJSONObject reads an object's members in file order, so rewriting a
// settings file the operator maintains keeps its layout recognizable.
func decodeJSONObject(raw []byte) ([]jsonMember, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("not a JSON object")
	}
	members := []jsonMember{}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, _ := token.(string)
		// Readers disagree on which duplicate wins, so editing one could leave
		// the rules where the client never looks.
		if seen[key] {
			return nil, fmt.Errorf("duplicate key %q", key)
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		members = append(members, jsonMember{key: key, value: value})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected content after the JSON object")
	}
	return members, nil
}

func encodeJSONObject(members []jsonMember) ([]byte, error) {
	var compact bytes.Buffer
	compact.WriteByte('{')
	for index, member := range members {
		if index > 0 {
			compact.WriteByte(',')
		}
		key, err := json.Marshal(member.key)
		if err != nil {
			return nil, err
		}
		compact.Write(key)
		compact.WriteByte(':')
		if err := json.Compact(&compact, member.value); err != nil {
			return nil, err
		}
	}
	compact.WriteByte('}')
	return compact.Bytes(), nil
}

func setJSONMember(members []jsonMember, key string, value json.RawMessage) []jsonMember {
	for index := range members {
		if members[index].key == key {
			members[index].value = value
			return members
		}
	}
	return append(members, jsonMember{key: key, value: value})
}

func findJSONMember(members []jsonMember, key string) json.RawMessage {
	for _, member := range members {
		if member.key == key {
			return member.value
		}
	}
	return nil
}

// addClaudeDenyRules appends missing rules to permissions.deny and returns the
// new file and the rules it added. Other settings keep their order.
func addClaudeDenyRules(current []byte, rules []string) ([]byte, []string, error) {
	top := []jsonMember{}
	if len(bytes.TrimSpace(current)) > 0 {
		var err error
		if top, err = decodeJSONObject(current); err != nil {
			return nil, nil, fmt.Errorf("parse Claude settings: %w", err)
		}
	}
	permissions := []jsonMember{}
	if raw := findJSONMember(top, "permissions"); raw != nil {
		var err error
		if permissions, err = decodeJSONObject(raw); err != nil {
			return nil, nil, fmt.Errorf("parse Claude settings permissions: %w", err)
		}
	}
	deny := []string{}
	if raw := findJSONMember(permissions, "deny"); raw != nil {
		if err := json.Unmarshal(raw, &deny); err != nil {
			return nil, nil, fmt.Errorf("parse Claude settings permissions.deny: %w", err)
		}
	}
	added := []string{}
	for _, rule := range rules {
		if !containsString(deny, rule) {
			deny = append(deny, rule)
			added = append(added, rule)
		}
	}
	if len(added) == 0 {
		return current, nil, nil
	}
	// Keep "&&" and "<" readable in rules the operator wrote.
	var encodedDeny bytes.Buffer
	encoder := json.NewEncoder(&encodedDeny)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(deny); err != nil {
		return nil, nil, err
	}
	denyRaw := json.RawMessage(bytes.TrimSpace(encodedDeny.Bytes()))
	permissions = setJSONMember(permissions, "deny", denyRaw)
	permissionsRaw, err := encodeJSONObject(permissions)
	if err != nil {
		return nil, nil, err
	}
	top = setJSONMember(top, "permissions", permissionsRaw)
	compact, err := encodeJSONObject(top)
	if err != nil {
		return nil, nil, err
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, compact, "", "  "); err != nil {
		return nil, nil, err
	}
	indented.WriteByte('\n')
	return indented.Bytes(), added, nil
}
