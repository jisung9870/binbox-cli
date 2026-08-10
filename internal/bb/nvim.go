package bb

// The nvim helpers deliberately do not acquire a configuration, install packages,
// or update plugins.  They make a selected, already-present LazyVim checkout safe
// to inspect and (only with explicit consent) link into the XDG config directory.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// NvimIdentity is the expected identity of an already-selected config checkout.
// Revision is the local git HEAD; LockfileSHA256 is the digest of lazy-lock.json.
// All populated fields must match. Repository is compared to origin's URL when
// the checkout has an origin remote.
type NvimIdentity struct {
	Repository     string `json:"repository,omitempty"`
	Revision       string `json:"revision,omitempty"`
	LockfileSHA256 string `json:"lockfile_sha256,omitempty"`
}

// NvimValidation is the observed, non-mutating identity of a config directory.
type NvimValidation struct {
	ConfigDir string       `json:"config_dir"`
	Identity  NvimIdentity `json:"identity"`
	Valid     bool         `json:"valid"`
	Problems  []string     `json:"problems"`
}

// ValidateNvimConfig verifies the minimum LazyVim layout, parses its lockfile,
// and checks every explicitly supplied identity field. It performs no mutation.
func ValidateNvimConfig(configDir string, expected NvimIdentity) (NvimValidation, error) {
	if configDir == "" {
		return NvimValidation{Problems: []string{}}, errors.New("nvim config directory is required")
	}
	absolute, err := filepath.Abs(configDir)
	if err != nil {
		return NvimValidation{}, fmt.Errorf("resolve nvim config: %w", err)
	}
	configDir = canonicalPath(absolute)
	result := NvimValidation{ConfigDir: configDir, Problems: []string{}}
	info, err := os.Stat(configDir)
	if err != nil {
		return result, fmt.Errorf("inspect nvim config: %w", err)
	}
	if !info.IsDir() {
		return result, fmt.Errorf("nvim config is not a directory: %s", configDir)
	}
	if _, err := os.Stat(filepath.Join(configDir, "init.lua")); err != nil {
		result.Problems = append(result.Problems, "missing init.lua")
	}
	lockfile := filepath.Join(configDir, "lazy-lock.json")
	lock, err := os.ReadFile(lockfile)
	if err != nil {
		result.Problems = append(result.Problems, "missing lazy-lock.json")
	} else {
		var parsed map[string]json.RawMessage
		if json.Unmarshal(lock, &parsed) != nil {
			result.Problems = append(result.Problems, "lazy-lock.json is not valid JSON")
		} else {
			digest := sha256.Sum256(lock)
			result.Identity.LockfileSHA256 = hex.EncodeToString(digest[:])
		}
	}
	result.Identity.Revision = gitOutput(configDir, "rev-parse", "HEAD")
	result.Identity.Repository = gitOutput(configDir, "config", "--get", "remote.origin.url")
	if expected.Repository != "" && result.Identity.Repository != expected.Repository {
		result.Problems = append(result.Problems, "repository identity does not match")
	}
	if expected.Revision != "" && result.Identity.Revision != expected.Revision {
		result.Problems = append(result.Problems, "revision does not match")
	}
	if expected.LockfileSHA256 != "" && !strings.EqualFold(result.Identity.LockfileSHA256, expected.LockfileSHA256) {
		result.Problems = append(result.Problems, "lockfile digest does not match")
	}
	result.Valid = len(result.Problems) == 0
	return result, nil
}

func gitOutput(dir string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

type NvimTargetKind string

const (
	NvimTargetMissing     NvimTargetKind = "missing"
	NvimTargetDesiredLink NvimTargetKind = "desired_link"
	NvimTargetOtherLink   NvimTargetKind = "other_link"
	NvimTargetBrokenLink  NvimTargetKind = "broken_link"
	NvimTargetDirectory   NvimTargetKind = "directory"
	NvimTargetRegularFile NvimTargetKind = "regular_file"
	NvimTargetOther       NvimTargetKind = "other"
)

type NvimTargetConflict struct {
	Path   string         `json:"path"`
	Kind   NvimTargetKind `json:"kind"`
	LinkTo string         `json:"link_to,omitempty"`
}

// ClassifyNvimTarget observes the exact path that would be linked. A target is
// safe to link only when Kind is NvimTargetMissing; no existing object is ever
// overwritten by ApplyNvimSetup.
func ClassifyNvimTarget(target, configDir string) (NvimTargetConflict, error) {
	result := NvimTargetConflict{Path: filepath.Clean(target)}
	info, err := os.Lstat(target)
	if errors.Is(err, fs.ErrNotExist) {
		result.Kind = NvimTargetMissing
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("inspect nvim target: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(target)
		if err != nil {
			return result, fmt.Errorf("read nvim target link: %w", err)
		}
		result.LinkTo = link
		resolved := link
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(filepath.Dir(target), resolved)
		}
		resolved, _ = filepath.Abs(filepath.Clean(resolved))
		desired, _ := filepath.Abs(filepath.Clean(configDir))
		if resolved == desired {
			result.Kind = NvimTargetDesiredLink
			return result, nil
		}
		if _, err := os.Stat(target); errors.Is(err, fs.ErrNotExist) {
			result.Kind = NvimTargetBrokenLink
		} else {
			result.Kind = NvimTargetOtherLink
		}
		return result, nil
	}
	if info.IsDir() {
		result.Kind = NvimTargetDirectory
	} else if info.Mode().IsRegular() {
		result.Kind = NvimTargetRegularFile
	} else {
		result.Kind = NvimTargetOther
	}
	return result, nil
}

type NvimSetupRequest struct {
	ConfigDir     string       `json:"config_dir"`
	XDGConfigHome string       `json:"xdg_config_home"`
	Expected      NvimIdentity `json:"expected"`
	Apply         bool         `json:"apply"`
	Consent       bool         `json:"consent"`
}
type NvimSetupPlan struct {
	Validation        NvimValidation     `json:"validation"`
	Target            NvimTargetConflict `json:"target"`
	Actions           []string           `json:"actions"`
	CanApply          bool               `json:"can_apply"`
	AlreadyConfigured bool               `json:"already_configured"`
}

func NvimTargetPath(xdgConfigHome string) string { return filepath.Join(xdgConfigHome, "nvim") }

// PlanNvimSetup is dry-run only. The caller may render its actions before asking
// for consent; it cannot change the filesystem.
func PlanNvimSetup(request NvimSetupRequest) (NvimSetupPlan, error) {
	if request.XDGConfigHome == "" {
		return NvimSetupPlan{}, errors.New("XDG config home is required")
	}
	validation, err := ValidateNvimConfig(request.ConfigDir, request.Expected)
	if err != nil {
		return NvimSetupPlan{}, err
	}
	target, err := ClassifyNvimTarget(NvimTargetPath(request.XDGConfigHome), request.ConfigDir)
	if err != nil {
		return NvimSetupPlan{}, err
	}
	plan := NvimSetupPlan{Validation: validation, Target: target, Actions: []string{}, CanApply: validation.Valid && (target.Kind == NvimTargetMissing || target.Kind == NvimTargetDesiredLink), AlreadyConfigured: target.Kind == NvimTargetDesiredLink}
	if !validation.Valid {
		plan.Actions = append(plan.Actions, "stop: config identity validation failed")
	} else if target.Kind == NvimTargetMissing {
		plan.Actions = append(plan.Actions, "create symlink "+target.Path+" -> "+validation.ConfigDir)
	} else if target.Kind == NvimTargetDesiredLink {
		plan.Actions = append(plan.Actions, "no change: desired link already exists")
	} else {
		plan.Actions = append(plan.Actions, "stop: existing target will not be overwritten ("+string(target.Kind)+")")
	}
	return plan, nil
}

// ApplyNvimSetup re-plans immediately before mutation and creates precisely one
// symlink. Both Apply and Consent must be true. Existing targets, including an
// already-correct link, are not treated as success because this operation never
// overwrites or replaces a filesystem object.
func ApplyNvimSetup(request NvimSetupRequest) (NvimSetupPlan, error) {
	plan, err := PlanNvimSetup(request)
	if err != nil {
		return plan, err
	}
	if !request.Apply || !request.Consent {
		return plan, errors.New("nvim link requires explicit apply and consent")
	}
	if plan.AlreadyConfigured {
		return plan, nil
	}
	if !plan.CanApply {
		return plan, fmt.Errorf("nvim target is not safe to link: %s", plan.Target.Kind)
	}
	if err := os.MkdirAll(filepath.Dir(plan.Target.Path), 0o700); err != nil {
		return plan, fmt.Errorf("create XDG config directory: %w", err)
	}
	// Recheck after creating the parent: another process may have created it.
	if target, err := ClassifyNvimTarget(plan.Target.Path, request.ConfigDir); err != nil || target.Kind != NvimTargetMissing {
		if err != nil {
			return plan, err
		}
		return plan, fmt.Errorf("nvim target changed before link: %s", target.Kind)
	}
	if err := os.Symlink(plan.Validation.ConfigDir, plan.Target.Path); err != nil {
		return plan, fmt.Errorf("link nvim config: %w", err)
	}
	plan.Actions = append(plan.Actions, "linked")
	return plan, nil
}

// NvimBackup records a caller-selected, recoverable move. Backup and restore are
// intentionally separate from setup: callers must first see a conflict and then
// explicitly choose a path outside the active XDG target.
type NvimBackup struct{ Original, Backup string }

func BackupNvimTarget(target, backup string) (NvimBackup, error) {
	if target == "" || backup == "" {
		return NvimBackup{}, errors.New("target and backup paths are required")
	}
	if filepath.Clean(target) == filepath.Clean(backup) {
		return NvimBackup{}, errors.New("backup path must differ from target")
	}
	if relative, err := filepath.Rel(filepath.Clean(target), filepath.Clean(backup)); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return NvimBackup{}, errors.New("backup path must be outside the active nvim target")
	}
	if _, err := os.Lstat(target); err != nil {
		return NvimBackup{}, fmt.Errorf("inspect backup target: %w", err)
	}
	if _, err := os.Lstat(backup); !errors.Is(err, fs.ErrNotExist) {
		if err == nil {
			return NvimBackup{}, errors.New("backup path already exists")
		}
		return NvimBackup{}, err
	}
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		return NvimBackup{}, err
	}
	if err := os.Rename(target, backup); err != nil {
		return NvimBackup{}, fmt.Errorf("move nvim target to backup: %w", err)
	}
	return NvimBackup{Original: target, Backup: backup}, nil
}

func (b NvimBackup) Restore() error {
	if b.Original == "" || b.Backup == "" {
		return errors.New("backup record is incomplete")
	}
	if _, err := os.Lstat(b.Original); !errors.Is(err, fs.ErrNotExist) {
		if err == nil {
			return errors.New("refuse restore: original path exists")
		}
		return err
	}
	if _, err := os.Lstat(b.Backup); err != nil {
		return fmt.Errorf("inspect nvim backup: %w", err)
	}
	return os.Rename(b.Backup, b.Original)
}

type NvimDoctorOptions struct {
	ConfigDir, XDGConfigHome string
	Expected                 NvimIdentity
	Headless                 bool
}
type NvimDoctorReport struct {
	Executable     bool               `json:"executable"`
	ExecutablePath string             `json:"executable_path,omitempty"`
	Validation     NvimValidation     `json:"validation"`
	Target         NvimTargetConflict `json:"target"`
	LinkOK         bool               `json:"link_ok"`
	Headless       *NvimProbe         `json:"headless,omitempty"`
}
type NvimProbe struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// DoctorNvim checks executable availability, config identity, lockfile validity,
// and the intended XDG link. Headless is opt-in and never triggers plugin sync.
func DoctorNvim(ctx context.Context, options NvimDoctorOptions) (NvimDoctorReport, error) {
	if options.XDGConfigHome == "" {
		return NvimDoctorReport{}, errors.New("XDG config home is required")
	}
	validation, err := ValidateNvimConfig(options.ConfigDir, options.Expected)
	if err != nil {
		return NvimDoctorReport{}, err
	}
	target, err := ClassifyNvimTarget(NvimTargetPath(options.XDGConfigHome), options.ConfigDir)
	if err != nil {
		return NvimDoctorReport{}, err
	}
	report := NvimDoctorReport{Validation: validation, Target: target, LinkOK: target.Kind == NvimTargetDesiredLink}
	if path, err := exec.LookPath("nvim"); err == nil {
		report.Executable, report.ExecutablePath = true, path
	}
	if options.Headless {
		probe := &NvimProbe{}
		if !report.Executable || !report.LinkOK || !validation.Valid {
			probe.Error = "headless probe skipped because executable, config, or link check failed"
		} else {
			// -u NONE proves the executable can start without bootstrapping plugins or
			// allowing the selected config to perform network activity.
			cmd := exec.CommandContext(ctx, report.ExecutablePath, "--headless", "-u", "NONE", "+qa")
			cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+options.XDGConfigHome)
			if output, err := cmd.CombinedOutput(); err != nil {
				probe.Error = strings.TrimSpace(string(output))
				if probe.Error == "" {
					probe.Error = err.Error()
				}
			} else {
				probe.OK = true
			}
		}
		report.Headless = probe
	}
	return report, nil
}
