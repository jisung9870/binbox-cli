// Package bb implements the small, local-first bb command-line interface.
package bb

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	case "tm":
		return a.tm(args[1:])
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
	case "mcp":
		return a.mcp(args[1:])
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
  setup shell|nvim ...    Configure zsh integration or a selected LazyVim config
  shell init zsh          Print checkout-independent zsh integration
  completion zsh          Print native zsh completion
  project ...             Manage/import the local project registry
  tm [projects|sessions|--project]  Select a project or inspect local tmux sessions
  gx ...                 Explicit Git workflow compatibility adapter
  kx ...                 Explicit kubectl workflow compatibility adapter
  assm ...               Explicit AWS SSM session adapter
  aws browse|sso|assume ...  Browse resources or authenticate/apply credentials
  assume ...             Compatibility alias for "bb aws assume"
  profile ...            Compatibility profile configuration surface
  wenv ...               Manage and apply declarative environment presets
  sec ...                Manage the existing age-encrypted secret store
  port inspect|kill ...  Inspect a local port or terminate an exact re-observed PID set
  tfx ...                 Guarded Terraform compatibility workflow
  tvx ...                 Direct Trivy compatibility adapter with fixed policies
  mcp ...                 Manage and synchronize MCP server registrations
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
		{"lsof", "local port inspection fallback", "optional", []string{"lsof"}},
		{"session-manager-plugin", "AWS session manager integrations", "optional", []string{"session-manager-plugin"}},
		{"age", "encrypted secret store integrations", "optional", []string{"age"}},
		{"age-keygen", "encrypted secret key management", "optional", []string{"age-keygen"}},
		{"trivy", "security scan integrations", "optional", []string{"trivy"}},
		{"tf-summarize", "Terraform summary integrations", "optional", []string{"tf-summarize"}},
		{"claude", "Claude MCP registration integrations", "optional", []string{"claude"}},
		{"codex", "Codex MCP registration integrations", "optional", []string{"codex"}},
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
