package bb

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nvimConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "init.lua"), []byte("return {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lazy-lock.json"), []byte(`{"LazyVim":{"commit":"abc"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestValidateNvimConfigChecksLayoutLockAndExplicitIdentity(t *testing.T) {
	dir := nvimConfig(t)
	got, err := ValidateNvimConfig(dir, NvimIdentity{Revision: "not-the-current-revision"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Valid || !strings.Contains(strings.Join(got.Problems, ";"), "revision does not match") {
		t.Fatalf("validation=%+v", got)
	}
	got, err = ValidateNvimConfig(dir, NvimIdentity{})
	if err != nil || !got.Valid || got.Identity.LockfileSHA256 == "" {
		t.Fatalf("validation=%+v err=%v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lazy-lock.json"), []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = ValidateNvimConfig(dir, NvimIdentity{})
	if err != nil || got.Valid {
		t.Fatalf("validation=%+v err=%v", got, err)
	}
}

func TestClassifyNvimTarget(t *testing.T) {
	config := nvimConfig(t)
	target := filepath.Join(t.TempDir(), "nvim")
	check := func(want NvimTargetKind) {
		t.Helper()
		got, err := ClassifyNvimTarget(target, config)
		if err != nil || got.Kind != want {
			t.Fatalf("kind=%s want=%s err=%v", got.Kind, want, err)
		}
	}
	check(NvimTargetMissing)
	if err := os.Symlink(config, target); err != nil {
		t.Fatal(err)
	}
	check(NvimTargetDesiredLink)
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "gone"), target); err != nil {
		t.Fatal(err)
	}
	check(NvimTargetBrokenLink)
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	check(NvimTargetDirectory)
}

func TestNvimSetupPlansAndOnlyLinksWithApplyAndConsent(t *testing.T) {
	config, xdg := nvimConfig(t), t.TempDir()
	request := NvimSetupRequest{ConfigDir: config, XDGConfigHome: xdg}
	plan, err := PlanNvimSetup(request)
	if err != nil || !plan.CanApply || len(plan.Actions) != 1 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if _, err := ApplyNvimSetup(request); err == nil {
		t.Fatal("setup without consent succeeded")
	}
	request.Apply, request.Consent = true, true
	if _, err := ApplyNvimSetup(request); err != nil {
		t.Fatal(err)
	}
	target := NvimTargetPath(xdg)
	if info, err := os.Lstat(target); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("target not symlink: %v %v", info, err)
	}
	second, err := ApplyNvimSetup(request)
	if err != nil || !second.AlreadyConfigured {
		t.Fatalf("idempotent apply=%+v err=%v", second, err)
	}
}

func TestNvimSetupNeverOverwritesConflictAndBackupRestoreIsExplicit(t *testing.T) {
	config, xdg := nvimConfig(t), t.TempDir()
	target := NvimTargetPath(xdg)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BackupNvimTarget(target, filepath.Join(target, "not-safe")); err == nil {
		t.Fatal("backup inside target was accepted")
	}
	request := NvimSetupRequest{ConfigDir: config, XDGConfigHome: xdg, Apply: true, Consent: true}
	plan, err := ApplyNvimSetup(request)
	if err == nil || plan.Target.Kind != NvimTargetRegularFile {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if got, _ := os.ReadFile(target); string(got) != "foreign" {
		t.Fatal("conflict changed")
	}
	backup, err := BackupNvimTarget(target, filepath.Join(xdg, "nvim.before-bb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := backup.Restore(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "foreign" {
		t.Fatal("backup did not restore")
	}
}

func TestDoctorNvimReportsChecksAndSkipsUnsafeProbe(t *testing.T) {
	config, xdg := nvimConfig(t), t.TempDir()
	report, err := DoctorNvim(context.Background(), NvimDoctorOptions{ConfigDir: config, XDGConfigHome: xdg, Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.Validation.Valid != true || report.LinkOK || report.Target.Kind != NvimTargetMissing || report.Headless == nil || report.Headless.OK || report.Headless.Error == "" {
		t.Fatalf("report=%+v", report)
	}
}

func TestNvimCLIPlanApplyAndDoctor(t *testing.T) {
	a, out, configHome, _ := testApp(t)
	config := nvimConfig(t)
	if err := a.Run([]string{"setup", "nvim", "--config-dir", config, "--dry-run", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"mode":"dry-run"`) {
		t.Fatalf("plan=%s", out.String())
	}
	target := filepath.Join(configHome, "nvim")
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("dry-run changed target: %v", err)
	}
	out.Reset()
	err := a.Run([]string{"setup", "nvim", "--config-dir", config, "--apply", "--json"})
	if err == nil || ExitCode(err) != ExitOperational {
		t.Fatalf("apply without consent err=%v", err)
	}
	out.Reset()
	if err := a.Run([]string{"setup", "nvim", "--config-dir", config, "--apply", "--consent", "--json"}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(target); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("target=%v err=%v", info, err)
	}
	out.Reset()
	if err := a.Run([]string{"doctor", "nvim", "--config-dir", config, "--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"link_ok":true`) {
		t.Fatalf("doctor=%s", out.String())
	}
}

func TestSetupRejectsUnknownSubcommand(t *testing.T) {
	a, _, _, _ := testApp(t)
	err := a.Run([]string{"setup", "unknown"})
	if err == nil || ExitCode(err) != ExitInvalidInvocation {
		t.Fatalf("err=%v exit=%d", err, ExitCode(err))
	}
}
