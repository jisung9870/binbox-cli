package bb

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// tm is the narrow compatibility surface used by the current LazyVim client.
// It reads bb's registry, and when opened interactively delegates only to fzf
// and tmux. It never calls Orca or records ownership of the tmux session.
func (a *App) tm(args []string) error {
	if helpRequested(args) {
		_, err := fmt.Fprint(a.out, `Usage:
  bb tm projects --json
  bb tm [--project <project-id>]

With no arguments, select a registered project with fzf and attach or create a
local tmux session. --project is an explicit non-interactive selector.
`)
		return err
	}
	args, jsonMode := takeFlag(args, "--json")
	if len(args) > 0 && args[0] == "projects" {
		if len(args) != 1 || !jsonMode {
			return usage("tm projects", "--json")
		}
		projects, err := a.tmProjects()
		if err != nil {
			return err
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
