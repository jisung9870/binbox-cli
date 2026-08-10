package bb

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
	Source         string            `json:"source"`
	Mode           string            `json:"mode"`
	ReadOnly       bool              `json:"read_only"`
	Candidates     []importCandidate `json:"candidates"`
	Warnings       []string          `json:"warnings"`
	Imported       int               `json:"imported,omitempty"`
	AlreadyPresent int               `json:"already_present,omitempty"`
	Backup         string            `json:"backup,omitempty"`
	SourceSHA256   string            `json:"source_sha256"`
}

type sessionizerRecovery struct {
	Source       string    `json:"source"`
	SourceSHA256 string    `json:"source_sha256"`
	Backup       string    `json:"backup"`
	CreatedAt    time.Time `json:"created_at"`
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
	legacy, err := os.ReadFile(source)
	if err != nil {
		return sessionizerCheck{}, fmt.Errorf("read sessionizer source %s: %w", source, err)
	}
	sourceSum := sha256.Sum256(legacy)

	result := sessionizerCheck{Source: source, Mode: "check", ReadOnly: true, Candidates: []importCandidate{}, Warnings: []string{}, SourceSHA256: hex.EncodeToString(sourceSum[:])}
	seen := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(legacy))
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

// applySessionizer owns only bb's XDG records. It reads the legacy input once,
// copies those exact bytes to bb state for recovery, and never writes the input.
func (a *App) applySessionizer(check sessionizerCheck, registryPath, state string) (sessionizerCheck, error) {
	legacy, err := os.ReadFile(check.Source)
	if err != nil {
		return check, fmt.Errorf("read sessionizer source for backup: %w", err)
	}
	sum := sha256.Sum256(legacy)
	if hex.EncodeToString(sum[:]) != check.SourceSHA256 {
		return check, errors.New("sessionizer source changed after check; rerun the import")
	}
	backup := filepath.Join(state, "migration-backups", "sessionizer-"+hex.EncodeToString(sum[:8])+".dirs")
	metadata := filepath.Join(state, "migration-backups", "sessionizer-recovery.json")
	// Persist exact recovery bytes before changing bb-owned registry state.
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		return check, err
	}
	if _, err := os.Stat(backup); os.IsNotExist(err) {
		if err := writeBytesAtomic(backup, legacy); err != nil {
			return check, fmt.Errorf("write sessionizer backup: %w", err)
		}
	} else if err != nil {
		return check, err
	}
	// Lock the registry so check/apply additions cannot race other project writes.
	err = withFileLock(registryPath, func() error {
		records, err := loadProjects(registryPath)
		if err != nil {
			return err
		}
		present := make(map[string]bool, len(records))
		for _, record := range records {
			present[canonicalPath(record.Path)] = true
		}
		for _, candidate := range check.Candidates {
			if present[candidate.Path] || candidate.Conflict != "" {
				check.AlreadyPresent++
				if candidate.Conflict == "registered_identity_collision" {
					check.Warnings = append(check.Warnings, fmt.Sprintf("not imported due to identity collision: %s", candidate.Path))
				}
				continue
			}
			records = append(records, projectRecord{ID: candidate.ID, Name: candidate.Name, Path: candidate.Path, AddedAt: a.now().UTC(), Origin: projectOrigin{Kind: "sessionizer", Source: check.Source, SourceLine: candidate.SourceLine, Mode: candidate.Mode}})
			present[candidate.Path] = true
			check.Imported++
		}
		sort.Slice(records, func(i, j int) bool {
			return records[i].Name < records[j].Name || (records[i].Name == records[j].Name && records[i].ID < records[j].ID)
		})
		return writeJSONAtomic(registryPath, records)
	})
	if err != nil {
		return check, fmt.Errorf("apply sessionizer registry: %w", err)
	}
	recovery := sessionizerRecovery{Source: check.Source, SourceSHA256: hex.EncodeToString(sum[:]), Backup: backup, CreatedAt: a.now().UTC()}
	if err := writeJSON(metadata, recovery); err != nil {
		return check, fmt.Errorf("write sessionizer recovery metadata: %w", err)
	}
	check.Mode, check.ReadOnly, check.Backup = "apply", false, backup
	return check, nil
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
