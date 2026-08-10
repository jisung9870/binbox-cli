package bb

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// tm is the narrow compatibility surface used by the current LazyVim client.
// It reads bb's registry, and when opened interactively delegates only to fzf
// and tmux. It never calls Orca or records ownership of the tmux session.
func (a *App) tm(args []string) error {
	if helpRequested(args) {
		_, err := fmt.Fprint(a.out, `Usage:
  bb tm projects --plain|--json
  bb tm sessions [--json]
  bb tm [--project <project-id>]

With no arguments, select a registered project with fzf and attach or create a
local tmux session. --project is an explicit non-interactive selector.
`)
		return err
	}
	args, jsonMode := takeFlag(args, "--json")
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
		// The fzf selection below is the normal interactive mode.
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
	if _, err := a.lookPath("fzf"); err != nil {
		return projectRecord{}, unavailable("fzf is not installed; install fzf to select a project or pass --project <project-id>")
	}
	var input strings.Builder
	for _, project := range projects {
		// Registry fields may be manually edited. Keep fzf's tab-delimited
		// protocol unambiguous instead of allowing a record to select another.
		if strings.ContainsAny(project.ID+project.Name+project.Path, "\t\n\r") {
			continue
		}
		fmt.Fprintf(&input, "%s\t%s\t%s\n", project.ID, project.Name, project.Path)
	}
	if input.Len() == 0 {
		return projectRecord{}, unavailable("no selectable projects are registered; repair project records or add a project")
	}
	cmd := a.command("fzf", "--delimiter=\t", "--with-nth=2,3", "--nth=2,3")
	cmd.Env = a.env
	cmd.Stdin = strings.NewReader(input.String())
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = a.err
	if err := cmd.Run(); err != nil {
		var exitErr interface{ ExitCode() int }
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 130 {
			return projectRecord{}, fmt.Errorf("project selection cancelled")
		}
		return projectRecord{}, fmt.Errorf("run fzf project selector: %w", err)
	}
	selectedID, _, _ := strings.Cut(strings.TrimSpace(output.String()), "\t")
	for _, project := range projects {
		if project.ID == selectedID {
			return project, nil
		}
	}
	return projectRecord{}, fmt.Errorf("fzf returned an unknown project selection")
}
