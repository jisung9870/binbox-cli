package awsbrowser

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const cliHelperSentinel = "awsbrowser-cli-helper-8f5cc184"

func TestCLIExposesOnlyApprovedCapabilities(t *testing.T) {
	interfaceType := reflect.TypeOf((*CLI)(nil)).Elem()
	want := []string{"ExportCredentials", "ListProfiles"}
	got := make([]string, interfaceType.NumMethod())
	for i := range got {
		got[i] = interfaceType.Method(i).Name
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CLI methods=%v want=%v", got, want)
	}

	execType := reflect.TypeOf((*ExecCLI)(nil))
	got = make([]string, execType.NumMethod())
	for i := range got {
		got[i] = execType.Method(i).Name
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExecCLI methods=%v want=%v", got, want)
	}
}

func TestApprovedCLIArgsRejectAnyOtherOperation(t *testing.T) {
	if args, ok := approvedCLIArgs("sts-get-caller-identity", ""); ok || args != nil {
		t.Fatalf("generic resource operation produced argv=%q", args)
	}
}

func TestExecCLIUsesDirectApprovedArgvAndNilStdin(t *testing.T) {
	type invocation struct {
		name string
		args []string
		cmd  *exec.Cmd
	}
	var invocations []invocation
	cli := &ExecCLI{
		path: "aws-safe-path",
		command: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			cmd := cliHelperCommand(ctx, "success")
			invocations = append(invocations, invocation{name: name, args: slices.Clone(args), cmd: cmd})
			return cmd
		},
	}

	baseEnv := []string{
		"SAFE=value",
		"AWS_PROFILE=ambient",
		"AWS_ACCESS_KEY_ID=ambient-key",
		"AWS_ENDPOINT_URL=http://127.0.0.1:1",
		"AWS_ENDPOINT_URL_STS=http://127.0.0.1:2",
		"AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=false",
	}
	if _, err := cli.ListProfiles(context.Background(), baseEnv); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.ExportCredentials(context.Background(), "named", baseEnv); err != nil {
		t.Fatal(err)
	}

	if got, want := len(invocations), 2; got != want {
		t.Fatalf("invocations=%d want=%d", got, want)
	}
	if got, want := invocations[0].name, "aws-safe-path"; got != want {
		t.Fatalf("list executable=%q want=%q", got, want)
	}
	if got, want := invocations[0].args, []string{
		"configure", "list-profiles",
		"--no-cli-pager",
		"--no-cli-auto-prompt",
		"--cli-error-format", "json",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("list argv=%q want=%q", got, want)
	}
	if got, want := invocations[1].args, []string{
		"--profile", "named",
		"configure", "export-credentials",
		"--format", "process",
		"--no-cli-pager",
		"--no-cli-auto-prompt",
		"--cli-error-format", "json",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("export argv=%q want=%q", got, want)
	}
	for i, invocation := range invocations {
		if invocation.cmd.Stdin != nil {
			t.Fatalf("invocation %d stdin=%T want nil", i, invocation.cmd.Stdin)
		}
	}

	listEnv := environmentMap(invocations[0].cmd.Env)
	if listEnv["AWS_PROFILE"] != "ambient" || listEnv["AWS_ACCESS_KEY_ID"] != "ambient-key" {
		t.Fatalf("ambient identity environment was not preserved: %v", listEnv)
	}
	assertEndpointEnvironmentStripped(t, listEnv)

	exportEnv := environmentMap(invocations[1].cmd.Env)
	if _, ok := exportEnv["AWS_PROFILE"]; ok {
		t.Fatalf("named export retained AWS_PROFILE: %v", exportEnv)
	}
	if _, ok := exportEnv["AWS_ACCESS_KEY_ID"]; ok {
		t.Fatalf("named export retained AWS_ACCESS_KEY_ID: %v", exportEnv)
	}
	assertEndpointEnvironmentStripped(t, exportEnv)
}

func TestExecCLIParsesProfiles(t *testing.T) {
	cli := helperExecCLI("list-profiles")
	profiles, err := cli.ListProfiles(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"default", "dev-1", "prod_2"}; !reflect.DeepEqual(profiles, want) {
		t.Fatalf("profiles=%q want=%q", profiles, want)
	}
}

func TestExecCLIRejectsMalformedProfileOutput(t *testing.T) {
	cli := helperExecCLI("invalid-profiles")
	_, err := cli.ListProfiles(context.Background(), nil)
	var cliError *CLIError
	if !errors.As(err, &cliError) || cliError.Kind != CLIInvalidOutput {
		t.Fatalf("error=%v want CLIInvalidOutput", err)
	}
	if strings.Contains(err.Error(), "unsafe") || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("error leaked malformed output: %q", err)
	}
}

func TestExecCLIBoundsBothOutputStreams(t *testing.T) {
	tests := []struct {
		name   string
		mode   string
		stream string
	}{
		{name: "stdout", mode: "stdout-overflow", stream: "stdout"},
		{name: "stderr", mode: "stderr-overflow", stream: "stderr"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cli := helperExecCLI(test.mode)
			_, err := cli.ExportCredentials(context.Background(), "", nil)
			var cliError *CLIError
			if !errors.As(err, &cliError) || cliError.Kind != CLIOutputTooLarge {
				t.Fatalf("error=%v want CLIOutputTooLarge", err)
			}
			var limitError *OutputLimitError
			if !errors.As(err, &limitError) || limitError.Stream != test.stream {
				t.Fatalf("error chain=%v want %s OutputLimitError", err, test.stream)
			}
		})
	}
}

func TestExecCLICancelsRunningChild(t *testing.T) {
	cli := helperExecCLI("block")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := cli.ExportCredentials(ctx, "", nil)
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancelled child returned after %s", elapsed)
	}
	var cliError *CLIError
	if !errors.As(err, &cliError) || cliError.Kind != CLICancelled {
		t.Fatalf("error=%v want CLICancelled", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v does not retain deadline cause", err)
	}
}

func TestExecCLIDoesNotExposeRawStderr(t *testing.T) {
	cli := helperExecCLI("secret-error")
	_, err := cli.ExportCredentials(context.Background(), "", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	const secret = "credential-super-secret"
	for current := err; current != nil; current = errors.Unwrap(current) {
		if strings.Contains(current.Error(), secret) {
			t.Fatalf("error chain leaked stderr: %q", current)
		}
	}
}

func TestExecCLICancellationBoundsEndlessOutput(t *testing.T) {
	cli := helperExecCLI("endless-output")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := cli.ExportCredentials(ctx, "", nil)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("endless-output cancellation returned after %s", elapsed)
	}
	var cliError *CLIError
	if !errors.As(err, &cliError) || cliError.Kind != CLICancelled {
		t.Fatalf("error=%v want CLICancelled", err)
	}
}

func TestExecCLIBoundsInheritedPipeShutdown(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	cli := helperExecCLI("inherit-pipe", pidFile)

	started := time.Now()
	_, err := cli.ExportCredentials(context.Background(), "", nil)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("inherited-pipe shutdown returned after %s", elapsed)
	}
	if err == nil {
		t.Fatal("expected bounded pipe-shutdown error")
	}
	data, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("read grandchild PID: %v", readErr)
	}
	if _, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr != nil {
		t.Fatalf("grandchild PID=%q: %v", data, parseErr)
	}
}

func TestCLIHelperProcess(t *testing.T) {
	index := slices.Index(os.Args, cliHelperSentinel)
	if index < 0 {
		return
	}
	if index+1 >= len(os.Args) {
		os.Exit(97)
	}
	runCLIHelperProcess(os.Args[index+1], os.Args[index+2:])
}

func runCLIHelperProcess(mode string, extra []string) {
	switch mode {
	case "success":
	case "list-profiles":
		_, _ = os.Stdout.WriteString("default\r\ndev-1\nprod_2\n")
	case "invalid-profiles":
		_, _ = os.Stdout.WriteString("default\nunsafe\x1bprofile\n")
	case "stdout-overflow":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), stdoutLimit+1))
	case "stderr-overflow":
		_, _ = os.Stderr.Write(bytes.Repeat([]byte("x"), stderrLimit+1))
	case "secret-error":
		_, _ = os.Stderr.WriteString("credential-super-secret")
		os.Exit(17)
	case "block":
		time.Sleep(30 * time.Second)
	case "endless-output":
		chunk := bytes.Repeat([]byte("x"), 4096)
		for {
			_, _ = os.Stdout.Write(chunk)
		}
	case "inherit-pipe":
		if len(extra) != 1 {
			os.Exit(96)
		}
		startCLIHelperGrandchild(extra[0], "grandchild-short")
	case "grandchild-short":
		time.Sleep(3 * time.Second)
	case "spawn-tree":
		if len(extra) != 1 {
			os.Exit(95)
		}
		startCLIHelperGrandchild(extra[0], "grandchild-long")
		time.Sleep(30 * time.Second)
	case "grandchild-long":
		time.Sleep(30 * time.Second)
	default:
		os.Exit(19)
	}
	os.Exit(0)
}

func startCLIHelperGrandchild(pidFile, mode string) {
	cmd := cliHelperCommand(context.Background(), mode)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		os.Exit(94)
	}
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		_ = cmd.Process.Kill()
		os.Exit(93)
	}
}

func helperExecCLI(mode string, extra ...string) *ExecCLI {
	return &ExecCLI{
		path: "aws-test-helper",
		command: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return cliHelperCommand(ctx, mode, extra...)
		},
	}
}

func cliHelperCommand(ctx context.Context, mode string, extra ...string) *exec.Cmd {
	args := []string{"-test.run=^TestCLIHelperProcess$", "--", cliHelperSentinel, mode}
	args = append(args, extra...)
	return exec.CommandContext(ctx, os.Args[0], args...)
}

func environmentMap(env []string) map[string]string {
	result := make(map[string]string, len(env))
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			result[name] = value
		}
	}
	return result
}

func assertEndpointEnvironmentStripped(t *testing.T, env map[string]string) {
	t.Helper()
	for name := range env {
		if name == "AWS_ENDPOINT_URL" || strings.HasPrefix(name, "AWS_ENDPOINT_URL_") {
			t.Fatalf("endpoint environment retained %s", name)
		}
	}
	if got := env["AWS_IGNORE_CONFIGURED_ENDPOINT_URLS"]; got != "true" {
		t.Fatalf("AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=%q want true", got)
	}
}
