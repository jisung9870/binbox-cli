package bb

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type importCandidate struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	SourceLine int    `json:"source_line"`
	Mode       string `json:"mode"`
	Conflict   string `json:"conflict,omitempty"`
}

type sessionizerCheck struct {
	Source     string            `json:"source"`
	Mode       string            `json:"mode"`
	ReadOnly   bool              `json:"read_only"`
	Candidates []importCandidate `json:"candidates"`
	Warnings   []string          `json:"warnings"`
}

func (a *App) checkSessionizer(source string, records []projectRecord) (sessionizerCheck, error) {
	if source == "" {
		config := a.getenv("XDG_CONFIG_HOME")
		if config == "" {
			config = filepath.Join(a.getenv("HOME"), ".config")
		}
		source = filepath.Join(config, "tmux-sessionizer", "dirs")
	}
	source, err := a.expandPath(source)
	if err != nil {
		return sessionizerCheck{}, err
	}
	f, err := os.Open(source)
	if err != nil {
		return sessionizerCheck{}, fmt.Errorf("read sessionizer source %s: %w", source, err)
	}
	defer f.Close()

	result := sessionizerCheck{Source: source, Mode: "check", ReadOnly: true, Candidates: []importCandidate{}, Warnings: []string{}}
	seen := map[string]bool{}
	scanner := bufio.NewScanner(f)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		direct := strings.HasPrefix(line, "=")
		if direct {
			line = strings.TrimSpace(strings.TrimPrefix(line, "="))
		}
		if line == "" {
			result.Warnings = append(result.Warnings, fmt.Sprintf("line %d: empty path", lineNumber))
			continue
		}
		path, expandErr := a.expandPath(line)
		if expandErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("line %d: %v", lineNumber, expandErr))
			continue
		}
		info, statErr := os.Stat(path)
		if statErr != nil || !info.IsDir() {
			result.Warnings = append(result.Warnings, fmt.Sprintf("line %d: directory unavailable: %s", lineNumber, path))
			continue
		}
		if direct {
			var duplicate bool
			result.Candidates, duplicate = appendCandidate(result.Candidates, seen, records, path, lineNumber, "direct")
			if duplicate {
				result.Warnings = append(result.Warnings, fmt.Sprintf("line %d: duplicate candidate: %s", lineNumber, path))
			}
			continue
		}
		entries, readErr := os.ReadDir(path)
		if readErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("line %d: list directory: %v", lineNumber, readErr))
			continue
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			child := filepath.Join(path, entry.Name())
			childInfo, childErr := os.Stat(child)
			if childErr != nil || !childInfo.IsDir() {
				continue
			}
			var duplicate bool
			result.Candidates, duplicate = appendCandidate(result.Candidates, seen, records, child, lineNumber, "child")
			if duplicate {
				result.Warnings = append(result.Warnings, fmt.Sprintf("line %d: duplicate candidate: %s", lineNumber, child))
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return sessionizerCheck{}, fmt.Errorf("scan sessionizer source: %w", err)
	}
	sort.Slice(result.Candidates, func(i, j int) bool { return result.Candidates[i].Path < result.Candidates[j].Path })
	return result, nil
}

func appendCandidate(candidates []importCandidate, seen map[string]bool, records []projectRecord, path string, line int, mode string) ([]importCandidate, bool) {
	path = canonicalPath(path)
	if seen[path] {
		return candidates, true
	}
	seen[path] = true
	candidate := importCandidate{ID: projectID(path), Name: filepath.Base(path), Path: path, SourceLine: line, Mode: mode}
	for _, record := range records {
		if canonicalPath(record.Path) == path {
			candidate.Conflict = "already_registered_path"
			break
		}
		if record.ID == candidate.ID || record.Name == candidate.Name {
			candidate.Conflict = "registered_identity_collision"
		}
	}
	return append(candidates, candidate), false
}

func (a *App) expandPath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home := a.getenv("HOME")
		if home == "" {
			return "", fmt.Errorf("HOME is required to expand %q", path)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	} else if strings.HasPrefix(path, "~") {
		return "", fmt.Errorf("unsupported home expansion %q", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return canonicalPath(abs), nil
}
