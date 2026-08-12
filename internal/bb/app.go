// Package bb implements the small, local-first bb command-line interface.
package bb

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
)

var (
	Version   = "0.1.0"
	Commit    = "unknown"
	BuildTime = "unknown"
)

type App struct {
	in           io.Reader
	out, err     io.Writer
	env          []string
	now          func() time.Time
	lookPath     func(string) (string, error)
	command      func(string, ...string) *exec.Cmd
	isTerminal   func(uintptr) bool
	readPassword func(uintptr) ([]byte, error)
}

func New(out, err io.Writer, env []string) *App {
	return &App{
		in:           os.Stdin,
		out:          out,
		err:          err,
		env:          env,
		now:          time.Now,
		lookPath:     exec.LookPath,
		command:      exec.Command,
		isTerminal:   term.IsTerminal,
		readPassword: term.ReadPassword,
	}
}

func (a *App) Run(args []string) error {
	jsonMode := jsonRequested(args)
	err := a.dispatch(args)
	if err == nil {
		return nil
	}
	commandErr := commandError(err)
	if jsonMode {
		if printErr := printErrorEnvelope(a.out, commandErr); printErr == nil {
			commandErr.Reported = true
		}
	}
	return commandErr
}

func (a *App) dispatch(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		return a.help()
	}
	switch args[0] {
	case "version":
		return a.version(args[1:])
	case "doctor":
		return a.doctor(args[1:])
	case "setup":
		return a.setup(args[1:])
	case "shell":
		return a.shell(args[1:])
	case "completion":
		return a.completion(args[1:])
	case "project":
		return a.project(args[1:])
	case "session":
		return a.session(args[1:])
	case "tm":
		return a.tm(args[1:])
	case "git":
		return a.git(args[1:])
	case "gx":
		return a.gx(args[1:])
	case "kx":
		return a.kx(args[1:])
	case "assm":
		return a.assm(args[1:])
	case "aws":
		return a.aws(args[1:])
	case "assume":
		return a.assume(args[1:])
	case "profile":
		return a.profile(args[1:])
	case "wenv":
		return a.wenv(args[1:])
	case "sec":
		return a.sec(args[1:])
	case "port":
		return a.port(args[1:])
	case "tfx":
		return a.tfx(args[1:])
	case "tvx":
		return a.tvx(args[1:])
	case "agents":
		return unavailable("agent lifecycle belongs to Orca; use the Orca app or 'orca-ide status --json'")
	case "run":
		return a.run(args[1:])
	case "mcp":
		return a.mcp(args[1:])
	case "export":
		return a.export(args[1:])
	case "orca":
		return a.orca(args[1:])
	default:
		return invalid(fmt.Sprintf("unknown command %q; run 'bb help'", args[0]))
	}
}

func (a *App) version(args []string) error {
	if helpRequested(args) {
		_, err := fmt.Fprintln(a.out, "Usage: bb version [--json]")
		return err
	}
	args, jsonMode := takeFlag(args, "--json")
	if len(args) != 0 {
		return usage("version", "[--json]")
	}
	data := map[string]any{"version": Version, "commit": Commit, "build_time": BuildTime, "schema_version": SchemaVersion}
	if jsonMode {
		return printEnvelope(a.out, data, nil)
	}
	_, err := fmt.Fprintln(a.out, Version)
	return err
}

func (a *App) help() error {
	_, err := fmt.Fprint(a.out, `bb is a local-first developer workspace helper.

Usage: bb <command> [arguments]

Commands:
  version                 Print bb version
  doctor [--json]         Check external CLI capabilities
  setup nvim ...          Plan or link a selected LazyVim config
  shell init zsh          Print checkout-independent zsh integration
  completion zsh          Print native zsh completion
  project ...             Manage/import the local project registry
  tm [projects|sessions|--project]  Select a project or inspect local tmux sessions
  git root|branch|log    Read Git repository metadata without modifying it
  gx ...                 Explicit Git workflow compatibility adapter
  kx ...                 Explicit kubectl workflow compatibility adapter
  assm ...               Explicit AWS SSM session adapter
  aws sso|assume ...     Authenticate SSO sessions or apply profile credentials
  assume ...             Compatibility alias for "bb aws assume"
  profile ...            Compatibility profile configuration surface
  wenv ...               Manage and apply declarative environment presets
  sec ...                Manage the existing age-encrypted secret store
  port inspect|kill ...  Inspect a local port or terminate an exact re-observed PID set
  tfx ...                 Guarded Terraform compatibility workflow
  tvx ...                 Direct Trivy compatibility adapter with fixed policies
  agents                  Explain the Orca-owned agent lifecycle boundary
  session start|stop|list Manage local session records
  run <command> [args]    Run a command and append a redacted journal event
  mcp inventory|audit     Read-only MCP configuration inventory/audit
  export [--output path]  Export the redacted journal as JSON
  orca status             Read-only Orca runtime status
`)
	return err
}

func (a *App) paths() (string, string, error) {
	config := a.getenv("XDG_CONFIG_HOME")
	if config == "" {
		var err error
		config, err = os.UserConfigDir()
		if err != nil {
			return "", "", fmt.Errorf("find XDG config directory: %w", err)
		}
	}
	state := a.getenv("XDG_STATE_HOME")
	if state == "" {
		home := a.getenv("HOME")
		if home == "" {
			var err error
			home, err = os.UserHomeDir()
			if err != nil {
				return "", "", fmt.Errorf("find home directory for XDG state: %w", err)
			}
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(config, "bb"), filepath.Join(state, "bb"), nil
}

func (a *App) getenv(key string) string {
	prefix := key + "="
	for _, entry := range a.env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

type projectRecord struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Path    string        `json:"path"`
	AddedAt time.Time     `json:"added_at"`
	Origin  projectOrigin `json:"origin"`
}

// projectOrigin records how a registry entry was discovered. It is metadata only:
// bb never treats a legacy source as a writable registry.
type projectOrigin struct {
	Kind       string `json:"kind"`
	Source     string `json:"source,omitempty"`
	SourceLine int    `json:"source_line,omitempty"`
	Mode       string `json:"mode,omitempty"`
}
type sessionRecord struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Project   string     `json:"project,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	StoppedAt *time.Time `json:"stopped_at,omitempty"`
}
type journalEvent struct {
	ID      string    `json:"id,omitempty"`
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Outcome string    `json:"outcome,omitempty"`
	Data    any       `json:"data"`
}

func (a *App) project(args []string) error {
	if helpRequested(args) {
		_, err := fmt.Fprint(a.out, `Usage:
  bb project list [--json]
  bb project add <path> [name] [--json]
  bb project remove <id|name> [--json]
  bb project show <id|name> [--json]
  bb project import sessionizer --check|--apply [--file <path>] [--json]
`)
		return err
	}
	args, jsonMode := takeFlag(args, "--json")
	config, _, err := a.paths()
	if err != nil {
		return err
	}
	path := filepath.Join(config, "projects.json")
	if len(args) == 0 || args[0] == "list" {
		if len(args) > 1 {
			return usage("project list", "[--json]")
		}
		records, err := loadProjects(path)
		if err != nil {
			return err
		}
		if jsonMode {
			return printEnvelope(a.out, records, nil)
		}
		return printHuman(a.out, records)
	}
	switch args[0] {
	case "add":
		if len(args) < 2 || len(args) > 3 {
			return usage("project add", "<path> [name] [--json]")
		}
		absolute, err := a.expandPath(args[1])
		if err != nil {
			return err
		}
		info, err := os.Stat(absolute)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("project path must be an existing directory: %s", absolute)
		}
		name := filepath.Base(absolute)
		if len(args) > 2 {
			name = args[2]
		}
		var added projectRecord
		err = withFileLock(path, func() error {
			records, loadErr := loadProjects(path)
			if loadErr != nil {
				return loadErr
			}
			for _, r := range records {
				if r.Name == name || canonicalPath(r.Path) == absolute {
					return fmt.Errorf("project already registered: %s", name)
				}
			}
			added = projectRecord{ID: projectID(absolute), Name: name, Path: absolute, AddedAt: a.now().UTC(), Origin: projectOrigin{Kind: "bb"}}
			records = append(records, added)
			sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
			return writeJSONAtomic(path, records)
		})
		if err != nil {
			return fmt.Errorf("write projects: %w", err)
		}
		if jsonMode {
			return printEnvelope(a.out, added, nil)
		}
		return printHuman(a.out, added)
	case "remove":
		if len(args) != 2 {
			return usage("project remove", "<id|name> [--json]")
		}
		removed := ""
		err := withFileLock(path, func() error {
			records, loadErr := loadProjects(path)
			if loadErr != nil {
				return loadErr
			}
			kept := make([]projectRecord, 0, len(records))
			for _, r := range records {
				if r.Name == args[1] || r.ID == args[1] {
					removed = r.ID
					continue
				}
				kept = append(kept, r)
			}
			if removed == "" {
				return fmt.Errorf("project not found: %s", args[1])
			}
			return writeJSONAtomic(path, kept)
		})
		if err != nil {
			return err
		}
		if jsonMode {
			return printEnvelope(a.out, map[string]string{"removed": removed}, nil)
		}
		return printHuman(a.out, map[string]string{"removed": removed})
	case "show":
		if len(args) != 2 {
			return usage("project show", "<id|name> [--json]")
		}
		records, err := loadProjects(path)
		if err != nil {
			return err
		}
		matches := make([]projectRecord, 0, 1)
		for _, record := range records {
			if record.ID == args[1] || record.Name == args[1] {
				matches = append(matches, record)
			}
		}
		if len(matches) == 0 {
			return fmt.Errorf("project not found: %s", args[1])
		}
		if len(matches) > 1 {
			return invalid(fmt.Sprintf("project reference is ambiguous: %s", args[1]))
		}
		if jsonMode {
			return printEnvelope(a.out, matches[0], nil)
		}
		return printHuman(a.out, matches[0])
	case "import":
		_, state, err := a.paths()
		if err != nil {
			return err
		}
		return a.projectImport(args[1:], jsonMode, path, state)
	default:
		return invalid(fmt.Sprintf("unknown project command %q", args[0]))
	}
}

func loadProjects(path string) ([]projectRecord, error) {
	records := []projectRecord{}
	if err := readJSON(path, &records); err != nil {
		return nil, fmt.Errorf("read projects: %w", err)
	}
	for i := range records {
		records[i].Path = canonicalPath(records[i].Path)
		if records[i].ID == "" {
			records[i].ID = projectID(records[i].Path)
		}
		if records[i].Origin.Kind == "" {
			records[i].Origin = projectOrigin{Kind: "bb"}
		}
	}
	return records, nil
}

func (a *App) projectImport(args []string, jsonMode bool, registryPath, state string) error {
	if len(args) < 2 || args[0] != "sessionizer" || (args[1] != "--check" && args[1] != "--apply") {
		return usage("project import", "sessionizer --check|--apply [--file <path>] [--json]")
	}
	apply := args[1] == "--apply"
	source := ""
	for i := 2; i < len(args); i++ {
		if args[i] != "--file" || i+1 >= len(args) || source != "" {
			return usage("project import", "sessionizer --check|--apply [--file <path>] [--json]")
		}
		source = args[i+1]
		i++
	}
	records, err := loadProjects(registryPath)
	if err != nil {
		return err
	}
	check, err := a.checkSessionizer(source, records)
	if err != nil {
		return err
	}
	if apply {
		check, err = a.applySessionizer(check, registryPath, state)
		if err != nil {
			return err
		}
	}
	if jsonMode {
		return printEnvelope(a.out, check, check.Warnings)
	}
	return printHuman(a.out, check)
}

func canonicalPath(path string) string {
	if path == "" {
		return ""
	}
	clean := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return resolved
	}
	return clean
}

func stableID(prefix, value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(sum[:6]))
}

func projectID(path string) string { return stableID("prj", canonicalPath(path)) }

func (a *App) session(args []string) error {
	if helpRequested(args) {
		_, err := fmt.Fprint(a.out, `Usage:
  bb session list [--json]
  bb session start <name> [--json]
  bb session stop <id|name> [--json]
  bb session open <project-id> [--backend auto|tmux|orca|shell] [--json]
`)
		return err
	}
	args, jsonMode := takeFlag(args, "--json")
	_, state, err := a.paths()
	if err != nil {
		return err
	}
	path := filepath.Join(state, "sessions.json")
	if len(args) == 0 || args[0] == "list" {
		if len(args) > 1 {
			return usage("session list", "[--json]")
		}
		records, err := loadSessions(path)
		if err != nil {
			return err
		}
		if jsonMode {
			return printEnvelope(a.out, records, nil)
		}
		return printHuman(a.out, records)
	}
	if args[0] == "open" {
		return a.sessionOpen(args[1:], jsonMode)
	}
	if len(args) != 2 {
		return usage("session", "start|stop <id|name> [--json]")
	}
	var changed sessionRecord
	switch args[0] {
	case "start":
		err = withFileLock(path, func() error {
			records, loadErr := loadSessions(path)
			if loadErr != nil {
				return loadErr
			}
			for _, r := range records {
				if r.Name == args[1] && r.StoppedAt == nil {
					return fmt.Errorf("session already running: %s", args[1])
				}
			}
			started := a.now().UTC()
			changed = sessionRecord{ID: stableID("ses", args[1]+"\x00"+started.Format(time.RFC3339Nano)), Name: args[1], StartedAt: started}
			records = append(records, changed)
			return writeJSONAtomic(path, records)
		})
	case "stop":
		err = withFileLock(path, func() error {
			records, loadErr := loadSessions(path)
			if loadErr != nil {
				return loadErr
			}
			now := a.now().UTC()
			found := false
			for i := range records {
				if (records[i].Name == args[1] || records[i].ID == args[1]) && records[i].StoppedAt == nil {
					records[i].StoppedAt = &now
					changed = records[i]
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("running session not found: %s", args[1])
			}
			return writeJSONAtomic(path, records)
		})
	default:
		return invalid(fmt.Sprintf("unknown session command %q", args[0]))
	}
	if err != nil {
		return err
	}
	if jsonMode {
		return printEnvelope(a.out, changed, nil)
	}
	return printHuman(a.out, changed)
}

// sessionOpen deliberately produces an opening plan, never an external session.
// In particular it does not invoke Orca, tmux, or a shell, so it cannot create a
// duplicate lifecycle or alter an external workspace.
func (a *App) sessionOpen(args []string, jsonMode bool) error {
	if len(args) != 1 && len(args) != 3 {
		return usage("session open", "<project-id> [--backend auto|tmux|orca|shell] [--json]")
	}
	backend := "auto"
	if len(args) == 3 {
		if args[1] != "--backend" {
			return usage("session open", "<project-id> [--backend auto|tmux|orca|shell] [--json]")
		}
		backend = args[2]
	}
	if backend != "auto" && backend != "tmux" && backend != "orca" && backend != "shell" {
		return invalid(fmt.Sprintf("unknown session backend %q", backend))
	}
	config, _, err := a.paths()
	if err != nil {
		return err
	}
	projects, err := loadProjects(filepath.Join(config, "projects.json"))
	if err != nil {
		return err
	}
	var project *projectRecord
	for i := range projects {
		if projects[i].ID == args[0] {
			project = &projects[i]
			break
		}
	}
	if project == nil {
		return fmt.Errorf("project not found: %s", args[0])
	}
	resolved := backend
	if resolved == "auto" {
		resolved = "shell"
	}
	if resolved == "orca" {
		return unavailable("Orca session opening is unavailable; bb does not manage Orca lifecycles")
	}
	if resolved == "tmux" {
		if _, err := a.lookPath("tmux"); err != nil {
			return unavailable("tmux is not installed; choose --backend shell or install tmux")
		}
	}
	data := map[string]any{"project": project, "requested_backend": backend, "backend": resolved, "planned": true, "external_action": "none", "next_step": "open the project manually using the selected backend"}
	if jsonMode {
		return printEnvelope(a.out, data, nil)
	}
	return printHuman(a.out, data)
}

func loadSessions(path string) ([]sessionRecord, error) {
	records := []sessionRecord{}
	if err := readJSON(path, &records); err != nil {
		return nil, fmt.Errorf("read sessions: %w", err)
	}
	for i := range records {
		if records[i].ID == "" {
			records[i].ID = stableID("ses", records[i].Name+"\x00"+records[i].StartedAt.Format(time.RFC3339Nano))
		}
	}
	return records, nil
}

func (a *App) doctor(args []string) error {
	if len(args) > 0 && args[0] == "nvim" {
		return a.doctorNvim(args[1:])
	}
	if helpRequested(args) {
		_, err := fmt.Fprintln(a.out, "Usage: bb doctor [--json] | bb doctor nvim --config-dir <path> [options]")
		return err
	}
	args, jsonMode := takeFlag(args, "--json")
	if len(args) != 0 {
		return usage("doctor", "[--json]")
	}
	type check struct {
		Command   string `json:"command"`
		Purpose   string `json:"purpose"`
		Available bool   `json:"available"`
		Path      string `json:"path,omitempty"`
		Recovery  string `json:"recovery,omitempty"`
	}
	type capability struct {
		Name        string  `json:"name"`
		Scope       string  `json:"scope"`
		Description string  `json:"description"`
		Available   bool    `json:"available"`
		Path        *string `json:"path"`
		Recovery    *string `json:"recovery"`
	}
	dependencies := []struct {
		name, purpose, scope string
		lookups              []string
	}{
		{"git", "source and project operations", "core", []string{"git"}},
		{"tmux", "human terminal sessions", "optional", []string{"tmux"}},
		{"kubectl", "Kubernetes integrations", "optional", []string{"kubectl"}},
		{"aws", "AWS integrations", "optional", []string{"aws"}},
		{"terraform", "Terraform integrations", "optional", []string{"terraform"}},
		{"orca", "read-only Orca status and jump pointers", "optional", []string{"orca-ide", "orca"}},
		{"docker", "container inspection integrations", "optional", []string{"docker"}},
		{"lsof", "local port inspection fallback", "optional", []string{"lsof"}},
		{"session-manager-plugin", "AWS session manager integrations", "optional", []string{"session-manager-plugin"}},
		{"age", "encrypted local export integrations", "optional", []string{"age"}},
		{"age-keygen", "encrypted secret key management", "optional", []string{"age-keygen"}},
		{"jq", "JSON query integrations", "optional", []string{"jq"}},
		{"trivy", "security scan integrations", "optional", []string{"trivy"}},
		{"tf-summarize", "Terraform summary integrations", "optional", []string{"tf-summarize"}},
	}
	checks := make([]check, 0, len(dependencies))
	capabilities := make([]capability, 0, len(dependencies))
	for _, dependency := range dependencies {
		item := check{Command: dependency.name, Purpose: dependency.purpose}
		for _, candidate := range dependency.lookups {
			if path, lookupErr := a.lookPath(candidate); lookupErr == nil {
				item.Available, item.Path = true, path
				break
			}
		}
		if !item.Available {
			item.Recovery = fmt.Sprintf("install %s to use %s", dependency.name, dependency.purpose)
		}
		checks = append(checks, item)
		var recovery *string
		if item.Recovery != "" {
			recovery = &item.Recovery
		}
		var capabilityPath *string
		if item.Path != "" {
			capabilityPath = &item.Path
		}
		capabilities = append(capabilities, capability{Name: dependency.name, Scope: dependency.scope, Description: dependency.purpose, Available: item.Available, Path: capabilityPath, Recovery: recovery})
	}
	if jsonMode {
		return printEnvelope(a.out, map[string]any{"checks": checks, "capabilities": capabilities}, nil)
	}
	for _, c := range checks {
		status := "missing"
		if c.Available {
			status = c.Path
		}
		fmt.Fprintf(a.out, "%-10s %s\n", c.Command, status)
	}
	return nil
}

func (a *App) run(args []string) error {
	if helpRequested(args) && len(args) == 1 {
		_, err := fmt.Fprintln(a.out, "Usage: bb run list|show|export [options] | bb run [--json] <command> [args]")
		return err
	}
	if len(args) > 0 && args[0] == "list" {
		subargs, jsonMode := takeFlag(args[1:], "--json")
		return a.runList(subargs, jsonMode)
	}
	if len(args) > 0 && args[0] == "show" {
		subargs, jsonMode := takeFlag(args[1:], "--json")
		return a.runShow(subargs, jsonMode)
	}
	if len(args) > 0 && args[0] == "export" {
		subargs, jsonMode := takeFlag(args[1:], "--json")
		return a.runExport(subargs, jsonMode)
	}
	jsonMode := false
	if len(args) > 0 && args[0] == "--json" {
		jsonMode = true
		args = args[1:]
	}
	if len(args) == 0 {
		return usage("run", "[--json] <command> [args]")
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = a.env
	if jsonMode {
		cmd.Stdout = a.err
	} else {
		cmd.Stdout = a.out
	}
	cmd.Stderr = a.err
	err := cmd.Run()
	code := 0
	if err != nil {
		code = 1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
	}
	_, state, pathErr := a.paths()
	if pathErr != nil {
		return pathErr
	}
	event := journalEvent{Time: a.now().UTC(), Type: "run", Outcome: "succeeded", Data: map[string]any{
		"executable":     filepath.Base(args[0]),
		"argument_count": len(args) - 1,
		"exit_code":      code,
	}}
	if err != nil {
		event.Outcome = "failed"
	}
	if journalErr := appendRun(filepath.Join(state, "journal.ndjson"), &event); journalErr != nil {
		return fmt.Errorf("journal run: %w", journalErr)
	}
	if err != nil {
		return fmt.Errorf("command failed (exit %d): %w", code, err)
	}
	if jsonMode {
		return printEnvelope(a.out, event, nil)
	}
	return nil
}

func appendRun(path string, event *journalEvent) error {
	return withFileLock(path, func() error {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		existing, err := readJournal(path)
		if err != nil {
			return err
		}
		base := stableID("run", event.Time.Format(time.RFC3339Nano)+"\x00"+fmt.Sprint(event.Data))
		used := make(map[string]bool, len(existing))
		for _, item := range existing {
			used[item.ID] = true
		}
		event.ID = base
		for n := 2; used[event.ID]; n++ {
			event.ID = fmt.Sprintf("%s_%d", base, n)
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		return json.NewEncoder(f).Encode(event)
	})
}

func readJournal(path string) ([]journalEvent, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []journalEvent{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	events := []journalEvent{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var event journalEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("read journal: %w", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func (a *App) runList(args []string, jsonMode bool) error {
	if len(args) != 0 {
		return usage("run list", "[--json]")
	}
	runs, err := a.readRunEvents()
	if err != nil {
		return err
	}
	if jsonMode {
		return printEnvelope(a.out, runs, nil)
	}
	return printHuman(a.out, runs)
}

func (a *App) readRunEvents() ([]journalEvent, error) {
	_, state, err := a.paths()
	if err != nil {
		return nil, err
	}
	events, err := readJournal(filepath.Join(state, "journal.ndjson"))
	if err != nil {
		return nil, err
	}
	runs := make([]journalEvent, 0)
	for _, event := range events {
		if event.Type == "run" {
			event.Data = redact(event.Data)
			runs = append(runs, event)
		}
	}
	return runs, nil
}
func (a *App) runShow(args []string, jsonMode bool) error {
	if len(args) != 1 {
		return usage("run show", "<run-id> [--json]")
	}
	_, state, err := a.paths()
	if err != nil {
		return err
	}
	events, err := readJournal(filepath.Join(state, "journal.ndjson"))
	if err != nil {
		return err
	}
	for _, event := range events {
		if event.Type == "run" && event.ID == args[0] {
			event.Data = redact(event.Data)
			if jsonMode {
				return printEnvelope(a.out, event, nil)
			}
			return printHuman(a.out, event)
		}
	}
	return fmt.Errorf("run not found: %s", args[0])
}
func (a *App) runExport(args []string, jsonMode bool) error {
	if len(args) != 0 && (len(args) != 2 || args[0] != "--format" || args[1] != "json") {
		return usage("run export", "[--format json] [--json]")
	}
	if len(args) == 2 {
		runs, err := a.readRunEvents()
		if err != nil {
			return err
		}
		if jsonMode {
			return printEnvelope(a.out, runs, nil)
		}
		return printJSON(a.out, runs)
	}
	return a.runList(nil, jsonMode)
}

func appendJournal(path string, event journalEvent) error {
	return withFileLock(path, func() error {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		return json.NewEncoder(f).Encode(event)
	})
}
func (a *App) export(args []string) error {
	if helpRequested(args) {
		_, err := fmt.Fprintln(a.out, "Usage: bb export [--output path] [--json]")
		return err
	}
	args, jsonMode := takeFlag(args, "--json")
	output := ""
	if len(args) == 2 && args[0] == "--output" {
		output = args[1]
	} else if len(args) != 0 {
		return usage("export", "[--output path] [--json]")
	}
	_, state, err := a.paths()
	if err != nil {
		return err
	}
	f, err := os.Open(filepath.Join(state, "journal.ndjson"))
	if errors.Is(err, os.ErrNotExist) {
		return writeExport(a.out, output, []journalEvent{}, jsonMode)
	}
	if err != nil {
		return err
	}
	defer f.Close()
	var events []journalEvent
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var e journalEvent
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			return fmt.Errorf("read journal: %w", err)
		}
		e.Data = redact(e.Data)
		events = append(events, e)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return writeExport(a.out, output, events, jsonMode)
}
func writeExport(out io.Writer, output string, events []journalEvent, jsonMode bool) error {
	if output == "" {
		if jsonMode {
			return printEnvelope(out, events, nil)
		}
		return printJSON(out, events)
	}
	return writeJSON(output, events)
}

func (a *App) mcp(args []string) error {
	if helpRequested(args) {
		_, err := fmt.Fprintln(a.out, "Usage: bb mcp inventory|audit [--json]")
		return err
	}
	args, jsonMode := takeFlag(args, "--json")
	if len(args) != 1 || (args[0] != "inventory" && args[0] != "audit") {
		return usage("mcp", "inventory|audit [--json]")
	}
	config, _, err := a.paths()
	if err != nil {
		return err
	}
	candidates := []string{filepath.Join(config, "mcp.json"), filepath.Join(a.getenv("HOME"), ".config", "Claude", "claude_desktop_config.json"), filepath.Join(a.getenv("HOME"), ".codex", "mcp.json")}
	type item struct {
		Path   string `json:"path"`
		Exists bool   `json:"exists"`
		SHA256 string `json:"sha256,omitempty"`
	}
	items := make([]item, 0, len(candidates))
	for _, path := range candidates {
		b, readErr := os.ReadFile(path)
		if errors.Is(readErr, os.ErrNotExist) {
			items = append(items, item{Path: path})
			continue
		}
		if readErr != nil {
			return fmt.Errorf("read MCP config %s: %w", path, readErr)
		}
		sum := sha256.Sum256(b)
		i := item{Path: path, Exists: true, SHA256: hex.EncodeToString(sum[:])}
		items = append(items, i)
	}
	existing := 0
	for _, item := range items {
		if item.Exists {
			existing++
		}
	}
	_, state, err := a.paths()
	if err != nil {
		return err
	}
	event := journalEvent{Time: a.now().UTC(), Type: "mcp_audit", Data: map[string]any{
		"mode": args[0], "candidate_count": len(items), "existing_count": existing,
	}}
	if err := appendJournal(filepath.Join(state, "journal.ndjson"), event); err != nil {
		return fmt.Errorf("journal MCP audit: %w", err)
	}
	data := map[string]any{"mode": args[0], "read_only": true, "content_inspected": false, "items": items}
	if jsonMode {
		return printEnvelope(a.out, data, nil)
	}
	return printHuman(a.out, data)
}

func (a *App) orca(args []string) error {
	if helpRequested(args) {
		_, err := fmt.Fprintln(a.out, "Usage: bb orca status [--json]")
		return err
	}
	args, jsonMode := takeFlag(args, "--json")
	if len(args) != 1 || args[0] != "status" {
		return usage("orca", "status [--json]")
	}
	path, err := exec.LookPath("orca-ide")
	if err != nil {
		return unavailable("orca-ide not found; bb only exposes read-only Orca status")
	}
	cmd := exec.Command(path, "status", "--json")
	cmd.Env = a.env
	b, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("read Orca status: %w", err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return fmt.Errorf("decode Orca status: %w", err)
	}
	data := redact(v)
	if jsonMode {
		return printEnvelope(a.out, data, nil)
	}
	return printHuman(a.out, data)
}

var sensitiveKey = regexp.MustCompile(`(?i)(token|secret|password|authorization|api[_-]?key|cookie|credential)`)
var assignmentSecret = regexp.MustCompile(`(?i)\b(token|password|secret|api[_-]?key)\s*([=:])\s*[^\s,]+`)
var authorizationSecret = regexp.MustCompile(`(?i)\bauthorization\s*([=:])\s*[^,\n]+`)
var bearerSecret = regexp.MustCompile(`(?i)\bbearer\s+(?:sk|ghp|github_pat|xox[baprs])[-_a-z0-9]+`)

func redact(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if sensitiveKey.MatchString(k) {
				out[k] = "[REDACTED]"
			} else {
				out[k] = redact(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = redact(x[i])
		}
		return out
	case []string:
		out := make([]string, len(x))
		for i := range x {
			out[i] = redact(x[i]).(string)
		}
		return out
	case string:
		x = assignmentSecret.ReplaceAllString(x, "$1$2 [REDACTED]")
		x = authorizationSecret.ReplaceAllString(x, "Authorization$1 [REDACTED]")
		return bearerSecret.ReplaceAllString(x, "[REDACTED]")
	default:
		return v
	}
}
