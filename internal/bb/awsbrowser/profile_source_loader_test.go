package awsbrowser

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSharedProfilePathsUseSnapshotOverridesAndHome(t *testing.T) {
	home := t.TempDir()
	configOverride := filepath.Join(t.TempDir(), "custom-config")
	credentialsOverride := filepath.Join(t.TempDir(), "custom-credentials")
	env := []string{
		"HOME=" + home,
		"AWS_CONFIG_FILE=" + configOverride,
		"AWS_SHARED_CREDENTIALS_FILE=" + credentialsOverride,
	}
	configPath, credentialsPath := sharedProfilePaths(env, runtime.GOOS)
	if configPath != configOverride || credentialsPath != credentialsOverride {
		t.Fatalf("paths=(%q, %q) want=(%q, %q)", configPath, credentialsPath, configOverride, credentialsOverride)
	}

	configPath, credentialsPath = sharedProfilePaths([]string{"HOME=" + home}, runtime.GOOS)
	if runtime.GOOS != "windows" && runtime.GOOS != "plan9" {
		if want := filepath.Join(home, ".aws", "config"); configPath != want {
			t.Fatalf("config path=%q want=%q", configPath, want)
		}
		if want := filepath.Join(home, ".aws", "credentials"); credentialsPath != want {
			t.Fatalf("credentials path=%q want=%q", credentialsPath, want)
		}
	}
}

func TestSharedProfileHomeSupportsWindowsAndPlan9Snapshots(t *testing.T) {
	if got := sharedProfileHome([]string{"userprofile=C:\\Users\\reader"}, "windows"); got != `C:\Users\reader` {
		t.Fatalf("Windows home=%q", got)
	}
	if got := sharedProfileHome([]string{`HOMEDRIVE=D:`, `HOMEPATH=\Users\reader`}, "windows"); got != `D:\Users\reader` {
		t.Fatalf("Windows drive/path home=%q", got)
	}
	if got := sharedProfileHome([]string{"home=/usr/reader"}, "plan9"); got != "/usr/reader" {
		t.Fatalf("Plan 9 home=%q", got)
	}
}

func TestSharedProfilePathsNeverFallBackToWorkingDirectory(t *testing.T) {
	configPath, credentialsPath := sharedProfilePaths(nil, "linux")
	if configPath != "" || credentialsPath != "" {
		t.Fatalf("paths without snapshot home=(%q, %q)", configPath, credentialsPath)
	}
	if data, err := readOptionalSharedProfileFile(context.Background(), ""); err != nil || data != nil {
		t.Fatalf("empty optional path data=%v err=%v", data, err)
	}
}

func TestValidateNamedProfileSourceToleratesEitherMissingOptionalFile(t *testing.T) {
	tests := []struct {
		name        string
		config      string
		credentials string
	}{
		{name: "missing credentials", config: "[profile dev]\ncredential_process = safe-command\n"},
		{name: "missing config", credentials: "[dev]\naws_access_key_id = AKIAEXAMPLE\naws_secret_access_key = secret\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			configPath := filepath.Join(directory, "config")
			credentialsPath := filepath.Join(directory, "credentials")
			if test.config != "" {
				if err := os.WriteFile(configPath, []byte(test.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if test.credentials != "" {
				if err := os.WriteFile(credentialsPath, []byte(test.credentials), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			env := []string{"AWS_CONFIG_FILE=" + configPath, "AWS_SHARED_CREDENTIALS_FILE=" + credentialsPath}
			if err := validateNamedProfileSource(context.Background(), "dev", env); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidateNamedProfileSourceUsesSnapshotHomeDefaults(t *testing.T) {
	home := t.TempDir()
	awsDirectory := filepath.Join(home, ".aws")
	if err := os.Mkdir(awsDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(awsDirectory, "credentials"), []byte("[dev]\naws_access_key_id = AKIAEXAMPLE\naws_secret_access_key = secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var env []string
	switch runtime.GOOS {
	case "windows":
		env = []string{"USERPROFILE=" + home}
	case "plan9":
		env = []string{"home=" + home}
	default:
		env = []string{"HOME=" + home}
	}
	if err := validateNamedProfileSource(context.Background(), "dev", env); err != nil {
		t.Fatal(err)
	}
}

func TestValidateNamedProfileSourceBoundsReadsAndRedactsReadErrors(t *testing.T) {
	directory := t.TempDir()
	tooLargePath := filepath.Join(directory, "sensitive-large-config")
	tooLarge := make([]byte, maxSharedProfileFileSize+1)
	if err := os.WriteFile(tooLargePath, tooLarge, 0o600); err != nil {
		t.Fatal(err)
	}
	err := validateNamedProfileSource(context.Background(), "dev", []string{
		"AWS_CONFIG_FILE=" + tooLargePath,
		"AWS_SHARED_CREDENTIALS_FILE=" + filepath.Join(directory, "missing"),
	})
	if !errors.Is(err, errSharedProfileTooLarge) || strings.Contains(err.Error(), tooLargePath) {
		t.Fatalf("large-file error=%q", err)
	}

	unreadablePath := filepath.Join(directory, "sensitive-directory")
	if err := os.Mkdir(unreadablePath, 0o700); err != nil {
		t.Fatal(err)
	}
	err = validateNamedProfileSource(context.Background(), "dev", []string{
		"AWS_CONFIG_FILE=" + unreadablePath,
		"AWS_SHARED_CREDENTIALS_FILE=" + filepath.Join(directory, "missing"),
	})
	if !errors.Is(err, errSharedProfileRead) || strings.Contains(err.Error(), unreadablePath) {
		t.Fatalf("read error=%q", err)
	}
}

func TestValidateNamedProfileSourceHonorsCancellationBeforeReading(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	secretPath := filepath.Join(t.TempDir(), "must-not-be-read")
	err := validateNamedProfileSource(ctx, "dev", []string{"AWS_CONFIG_FILE=" + secretPath})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v want context cancellation", err)
	}
}

func TestValidateNamedProfileSourceDoesNotExposeSelectedPaths(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "sensitive-config-name")
	if err := os.WriteFile(configPath, []byte("[profile other]\ncredential_process = command\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := validateNamedProfileSource(context.Background(), "dev", []string{
		"AWS_CONFIG_FILE=" + configPath,
		"AWS_SHARED_CREDENTIALS_FILE=" + filepath.Join(directory, "sensitive-credentials-name"),
	})
	if err == nil {
		t.Fatal("missing requested profile was accepted")
	}
	if strings.Contains(err.Error(), directory) || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("error exposed selected path: %q", err)
	}
}
