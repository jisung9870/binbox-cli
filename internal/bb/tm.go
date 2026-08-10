package bb

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// tm is the narrow compatibility surface used by the current LazyVim client.
// It reads bb's registry and delegates session operations only to tmux.
func (a *App) tm(args []string) error {
	if helpRequested(args) {
		_, err := fmt.Fprint(a.out, `Usage:
  bb tm projects --plain|--json
  bb tm sessions [--json]
  bb tm attach [--session <name>]
  bb tm kill --session <name> [--yes]
  bb tm dirs [list|add <path>|remove <id|name>|prune] [--direct] [--yes]
  bb tm layout --layout <golang|k8s|terraform> --session <name> --path <dir>
  bb tm [--project <project-id>]

With no arguments, select a registered project with bb's built-in selector and attach or create a
local tmux session. --project is an explicit non-interactive selector.
`)
		return err
	}
	args, jsonMode := takeFlag(args, "--json")
	if len(args) > 0 {
		switch args[0] {
		case "attach":
			if jsonMode {
				return usage("tm attach", "[--session <name>]")
			}
			return a.tmAttach(args[1:])
		case "kill":
			if jsonMode {
				return usage("tm kill", "--session <name> [--yes]")
			}
			return a.tmKill(args[1:])
		case "dirs":
			if jsonMode {
				return usage("tm dirs", "[list|add <path>|remove <id|name>|prune] [--direct] [--yes]")
			}
			return a.tmDirs(args[1:])
		case "layout":
			if jsonMode {
				return usage("tm layout", "--layout <golang|k8s|terraform> --session <name> --path <dir>")
			}
			return a.tmLayout(args[1:])
		}
	}
	if len(args) > 0 && args[0] == "sessions" {
		if len(args) != 1 {
			return usage("tm sessions", "[--json]")
		}
		sessions, err := a.tmSessions()
		if err != nil {
			return err
		}
		if jsonMode {
			return printEnvelope(a.out, map[string]any{"sessions": sessions}, nil)
		}
		return printJSON(a.out, sessions)
	}
	if len(args) > 0 && args[0] == "projects" {
		plain := len(args) == 2 && args[1] == "--plain"
		if (!plain && (len(args) != 1 || !jsonMode)) || (plain && jsonMode) {
			return usage("tm projects", "--plain|--json")
		}
		projects, err := a.tmProjects()
		if err != nil {
			return err
		}
		if plain {
			for _, project := range projects {
				if _, err := fmt.Fprintln(a.out, project.Path); err != nil {
					return err
				}
			}
			return nil
		}
		return printEnvelope(a.out, map[string]any{"projects": projects}, nil)
	}
	if jsonMode {
		return usage("tm", "[--project <project-id>]")
	}
	projectID := ""
	switch len(args) {
	case 0:
		// The built-in selection below is the normal interactive mode.
	case 2:
		if args[0] != "--project" {
			return usage("tm", "[--project <project-id>]")
		}
		projectID = args[1]
	default:
		return usage("tm", "[--project <project-id>]")
	}
	projects, err := a.tmProjects()
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		return unavailable("no projects are registered; add one with 'bb project add <path>'")
	}
	var project projectRecord
	if projectID == "" {
		project, err = a.selectTMProject(projects)
		if err != nil {
			return err
		}
	} else {
		for _, candidate := range projects {
			if candidate.ID == projectID {
				project = candidate
				break
			}
		}
		if project.ID == "" {
			return fmt.Errorf("project not found: %s", projectID)
		}
	}
	if _, err := a.lookPath("tmux"); err != nil {
		return unavailable("tmux is not installed; install tmux to open a project session")
	}
	// project.ID is a deterministic bb identifier, not user-supplied shell text.
	// Arguments are passed directly to tmux; no shell is evaluated. Inside tmux,
	// switch the current client instead of attempting a nested attach.
	session := "bb-" + project.ID
	if a.getenv("TMUX") != "" {
		probe := a.command("tmux", "has-session", "-t", session)
		probe.Env = a.env
		if err := probe.Run(); err != nil {
			if err := a.runTMux("new-session", "-d", "-s", session, "-c", project.Path); err != nil {
				return fmt.Errorf("create tmux session for %s: %w", project.Name, err)
			}
		}
		if err := a.runTMux("switch-client", "-t", session); err != nil {
			return fmt.Errorf("switch tmux session for %s: %w", project.Name, err)
		}
		return nil
	}
	if err := a.runTMux("new-session", "-A", "-s", session, "-c", project.Path); err != nil {
		return fmt.Errorf("open tmux session for %s: %w", project.Name, err)
	}
	return nil
}

// tmSession is deliberately limited to tmux's session-level format fields.
// bb does not scrape panes, commands, or terminal content during inventory.
type tmSession struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Windows       int    `json:"windows"`
	Attached      bool   `json:"attached"`
	CreatedAtUnix int64  `json:"created_at_unix"`
	StateSource   string `json:"state_source"`
}

func (a *App) tmSessions() ([]tmSession, error) {
	if _, err := a.lookPath("tmux"); err != nil {
		return nil, unavailable("tmux is not installed; install tmux to inspect local sessions")
	}
	cmd := a.command("tmux", "list-sessions", "-F", "#{session_id}\t#{session_name}\t#{session_windows}\t#{session_attached}\t#{session_created}")
	cmd.Env = a.env
	var output, stderr bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			// tmux uses exit 1 when no server exists; that is an empty inventory.
			return []tmSession{}, nil
		}
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return nil, fmt.Errorf("list tmux sessions: %s", message)
		}
		return nil, fmt.Errorf("list tmux sessions: %w", err)
	}
	return parseTMSessions(output.String()), nil
}

func parseTMSessions(output string) []tmSession {
	sessions := []tmSession{}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 5 || fields[0] == "" || fields[1] == "" {
			continue
		}
		windows, windowsErr := strconv.Atoi(fields[2])
		created, createdErr := strconv.ParseInt(fields[4], 10, 64)
		if windowsErr != nil || createdErr != nil {
			continue
		}
		item := tmSession{ID: fields[0], Name: fields[1], Windows: windows, Attached: fields[3] != "0", CreatedAtUnix: created, StateSource: "tmux"}
		sessions = append(sessions, item)
	}
	return sessions
}

func (a *App) runTMux(args ...string) error {
	cmd := a.command("tmux", args...)
	cmd.Env = a.env
	cmd.Stdin = a.in
	cmd.Stdout = a.out
	cmd.Stderr = a.err
	return cmd.Run()
}

func (a *App) tmProjects() ([]projectRecord, error) {
	config, _, err := a.paths()
	if err != nil {
		return nil, err
	}
	return loadProjects(filepath.Join(config, "projects.json"))
}

func (a *App) selectTMProject(projects []projectRecord) (projectRecord, error) {
	choices := make([]selectChoice, 0, len(projects))
	for _, project := range projects {
		if !strings.ContainsAny(project.ID+project.Name+project.Path, "\n\r") {
			choices = append(choices, selectChoice{project.ID, project.Name + " — " + project.Path})
		}
	}
	selectedID, err := a.selectOne("Project", choices)
	if err != nil {
		return projectRecord{}, err
	}
	if selectedID == "" {
		return projectRecord{}, invalid("project selection cancelled")
	}
	for _, project := range projects {
		if project.ID == selectedID {
			return project, nil
		}
	}
	return projectRecord{}, fmt.Errorf("selector returned an unknown project selection")
}

// tmAttach attaches only to a session that has just been observed.  The second
// lookup makes a stale interactive choice fail safely rather than sending tmux a name
// that might now identify a different session.
func (a *App) tmAttach(args []string) error {
	sessionName := ""
	expectedID := ""
	if len(args) == 0 {
		sessions, err := a.tmSessions()
		if err != nil {
			return err
		}
		if len(sessions) == 0 {
			return unavailable("no local tmux sessions are available to attach")
		}
		selected, err := a.selectTMSession(sessions)
		if err != nil {
			return err
		}
		sessionName, expectedID = selected.Name, selected.ID
	} else if len(args) == 2 && args[0] == "--session" && validTMSessionName(args[1]) {
		sessionName = args[1]
	} else {
		return usage("tm attach", "[--session <name>]")
	}
	fresh, err := a.tmExactSession(sessionName)
	if err != nil {
		return err
	}
	if expectedID != "" && fresh.ID != expectedID {
		return unavailable("tmux session changed after selection; inspect sessions and retry")
	}
	if a.getenv("TMUX") != "" {
		if err := a.runTMux("switch-client", "-t", sessionName); err != nil {
			return fmt.Errorf("switch tmux session %q: %w", sessionName, err)
		}
		return nil
	}
	if err := a.runTMux("attach-session", "-t", sessionName); err != nil {
		return fmt.Errorf("attach tmux session %q: %w", sessionName, err)
	}
	return nil
}

func (a *App) tmKill(args []string) error {
	args, yes := takeFlag(args, "--yes")
	if len(args) != 2 || args[0] != "--session" || !validTMSessionName(args[1]) {
		return usage("tm kill", "--session <name> [--yes]")
	}
	target, err := a.tmExactSession(args[1])
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.out, "Target tmux session: %s\n", target.Name); err != nil {
		return err
	}
	if !yes {
		ok, err := a.confirmTM("Kill tmux session " + target.Name + "?")
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("tmux session kill cancelled")
		}
	}
	// Re-observe both name and tmux's opaque session ID after a human has had a
	// chance to act. A same-name replacement must not inherit authorization.
	fresh, err := a.tmExactSession(target.Name)
	if err != nil || fresh.ID != target.ID {
		return unavailable("tmux session changed before kill; inspect sessions and retry")
	}
	if err := a.runTMux("kill-session", "-t", target.Name); err != nil {
		return fmt.Errorf("kill tmux session %q: %w", target.Name, err)
	}
	return nil
}

// tmDirs is a compatibility spelling for the bb project registry.  It never
// reads or writes a sessionizer dirs file.
func (a *App) tmDirs(args []string) error {
	args, direct := takeFlag(args, "--direct")
	args, yes := takeFlag(args, "--yes")
	_ = direct // accepted for legacy callers; bb records direct paths identically.
	if len(args) == 0 || (len(args) == 1 && args[0] == "list") {
		if yes {
			return usage("tm dirs", "[list|add <path>|remove <id|name>|prune] [--direct] [--yes]")
		}
		projects, err := a.tmProjects()
		if err != nil {
			return err
		}
		for _, p := range projects {
			if _, err := fmt.Fprintln(a.out, p.Path); err != nil {
				return err
			}
		}
		return nil
	}
	if len(args) == 2 && args[0] == "add" {
		if yes {
			return usage("tm dirs add", "<path> [--direct]")
		}
		return a.project([]string{"add", args[1]})
	}
	if len(args) == 2 && args[0] == "remove" {
		return a.tmRemoveProject(args[1], yes)
	}
	if len(args) == 1 && args[0] == "prune" {
		return a.tmPruneProjects(yes)
	}
	return usage("tm dirs", "[list|add <path>|remove <id|name>|prune] [--direct] [--yes]")
}

func (a *App) tmRemoveProject(ref string, yes bool) error {
	config, _, err := a.paths()
	if err != nil {
		return err
	}
	registry := filepath.Join(config, "projects.json")
	var target projectRecord
	if err := withFileLock(registry, func() error {
		projects, err := loadProjects(registry)
		if err != nil {
			return err
		}
		matches := matchingTMProjects(projects, ref)
		if len(matches) == 0 {
			return fmt.Errorf("project not found: %s", ref)
		}
		if len(matches) > 1 {
			return invalid(fmt.Sprintf("project reference is ambiguous: %s", ref))
		}
		target = matches[0]
		return nil
	}); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.out, "Target project: %s (%s)\n", target.Name, target.Path); err != nil {
		return err
	}
	if !yes {
		ok, err := a.confirmTM("Remove project " + target.Name + " from bb registry?")
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("project removal cancelled")
		}
	}
	return withFileLock(registry, func() error {
		projects, err := loadProjects(registry)
		if err != nil {
			return err
		}
		matches := matchingTMProjects(projects, ref)
		if len(matches) != 1 || matches[0].ID != target.ID {
			return unavailable("project registry changed before removal; inspect it and retry")
		}
		kept := make([]projectRecord, 0, len(projects))
		for _, p := range projects {
			if p.ID != target.ID {
				kept = append(kept, p)
			}
		}
		return writeJSONAtomic(registry, kept)
	})
}

func (a *App) tmPruneProjects(yes bool) error {
	config, _, err := a.paths()
	if err != nil {
		return err
	}
	registry := filepath.Join(config, "projects.json")
	projects, err := loadProjects(registry)
	if err != nil {
		return err
	}
	stale := make([]projectRecord, 0)
	for _, p := range projects {
		info, statErr := os.Stat(p.Path)
		if statErr != nil || !info.IsDir() {
			stale = append(stale, p)
		}
	}
	if len(stale) == 0 {
		_, err := fmt.Fprintln(a.out, "No stale bb project paths.")
		return err
	}
	if _, err := fmt.Fprintln(a.out, "Stale bb project paths:"); err != nil {
		return err
	}
	for _, p := range stale {
		if _, err := fmt.Fprintf(a.out, "- %s\t%s\n", p.Name, p.Path); err != nil {
			return err
		}
	}
	if !yes {
		ok, err := a.confirmTM("Remove the listed stale projects from bb registry?")
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("project prune cancelled")
		}
	}
	return withFileLock(registry, func() error {
		current, err := loadProjects(registry)
		if err != nil {
			return err
		}
		staleIDs := make(map[string]bool, len(stale))
		for _, p := range stale {
			staleIDs[p.ID] = true
		}
		kept := make([]projectRecord, 0, len(current))
		for _, p := range current {
			if staleIDs[p.ID] {
				info, statErr := os.Stat(p.Path)
				if statErr != nil || !info.IsDir() {
					continue
				}
			}
			kept = append(kept, p)
		}
		return writeJSONAtomic(registry, kept)
	})
}

func matchingTMProjects(projects []projectRecord, ref string) []projectRecord {
	var matches []projectRecord
	for _, p := range projects {
		if p.ID == ref || p.Name == ref {
			matches = append(matches, p)
		}
	}
	return matches
}

func (a *App) confirmTM(question string) (bool, error) {
	if _, err := fmt.Fprintf(a.out, "%s [y/N]: ", question); err != nil {
		return false, err
	}
	answer, err := bufio.NewReader(a.in).ReadString('\n')
	if err != nil && len(answer) == 0 {
		return false, fmt.Errorf("read confirmation: %w", err)
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

func (a *App) tmLayout(args []string) error {
	flags, err := tmLayoutFlags(args)
	if err != nil {
		return err
	}
	if _, err := a.lookPath("tmux"); err != nil {
		return unavailable("tmux is not installed; install tmux to create a built-in layout")
	}
	info, err := os.Stat(flags.path)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("layout path must be an existing directory: %s", flags.path)
	}
	if _, err := a.tmExactSession(flags.session); err == nil {
		return invalid(fmt.Sprintf("tmux session already exists: %s", flags.session))
	} else if ExitCode(err) != ExitOperational {
		return err
	}
	if err := a.runTMux("new-session", "-d", "-s", flags.session, "-c", flags.path, "-n", flags.layout); err != nil {
		return fmt.Errorf("create tmux layout %q: %w", flags.layout, err)
	}
	layout := map[string]string{"golang": "even-horizontal", "k8s": "main-vertical", "terraform": "tiled"}[flags.layout]
	if err := a.runTMux("split-window", "-h", "-t", flags.session+":0", "-c", flags.path); err != nil {
		return fmt.Errorf("add tmux layout pane: %w", err)
	}
	if err := a.runTMux("select-layout", "-t", flags.session+":0", layout); err != nil {
		return fmt.Errorf("set tmux layout: %w", err)
	}
	_, err = fmt.Fprintf(a.out, "Created %s tmux layout in session %s\n", flags.layout, flags.session)
	return err
}

type tmLayoutOptions struct{ layout, session, path string }

func tmLayoutFlags(args []string) (tmLayoutOptions, error) {
	var out tmLayoutOptions
	for len(args) > 0 {
		if len(args) < 2 {
			return out, usage("tm layout", "--layout <golang|k8s|terraform> --session <name> --path <dir>")
		}
		key, value := args[0], args[1]
		args = args[2:]
		switch key {
		case "--layout":
			out.layout = value
		case "--session":
			out.session = value
		case "--path":
			out.path = value
		default:
			return out, usage("tm layout", "--layout <golang|k8s|terraform> --session <name> --path <dir>")
		}
	}
	if (out.layout != "golang" && out.layout != "k8s" && out.layout != "terraform") || !validTMSessionName(out.session) || out.path == "" {
		return out, usage("tm layout", "--layout <golang|k8s|terraform> --session <name> --path <dir>")
	}
	return out, nil
}

func validTMSessionName(name string) bool {
	return name != "" && !strings.ContainsAny(name, "\x00\n\r")
}

func (a *App) tmExactSession(name string) (tmSession, error) {
	sessions, err := a.tmSessions()
	if err != nil {
		return tmSession{}, err
	}
	for _, s := range sessions {
		if s.Name == name {
			return s, nil
		}
	}
	return tmSession{}, fmt.Errorf("tmux session not found: %s", name)
}

func (a *App) selectTMSession(sessions []tmSession) (tmSession, error) {
	choices := make([]selectChoice, 0, len(sessions))
	for _, s := range sessions {
		if validTMSessionName(s.Name) {
			choices = append(choices, selectChoice{s.ID, s.Name})
		}
	}
	selectedID, err := a.selectOne("Tmux session", choices)
	if err != nil {
		return tmSession{}, err
	}
	if selectedID == "" {
		return tmSession{}, invalid("tmux session selection cancelled")
	}
	for _, s := range sessions {
		if s.ID == selectedID {
			return s, nil
		}
	}
	return tmSession{}, fmt.Errorf("selector returned an unknown tmux session selection")
}
