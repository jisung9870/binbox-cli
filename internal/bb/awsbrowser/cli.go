package awsbrowser

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

const (
	stdoutLimit      = 32 << 10
	stderrLimit      = 64 << 10
	commandWaitDelay = 500 * time.Millisecond

	cliOperationListProfiles      = "list-profiles"
	cliOperationExportCredentials = "export-credentials"
)

type ProfileLister interface {
	ListProfiles(ctx context.Context, env []string) ([]string, error)
}

type CredentialExporter interface {
	ExportCredentials(ctx context.Context, profile string, env []string) ([]byte, error)
}

// CLI is the complete AWS CLI surface available to the browser. Resource
// operations are intentionally impossible through this interface; they use
// narrowed SDK clients instead.
type CLI interface {
	ProfileLister
	CredentialExporter
}

type commandFactory func(context.Context, string, ...string) *exec.Cmd

// ExecCLI runs the AWS CLI without a shell and bounds all captured output.
type ExecCLI struct {
	path    string
	command commandFactory
}

func NewExecCLI(path string) *ExecCLI {
	return &ExecCLI{path: path, command: exec.CommandContext}
}

func (c *ExecCLI) ListProfiles(ctx context.Context, env []string) ([]string, error) {
	stdout, err := c.runApproved(ctx, cliOperationListProfiles, "", credentialEnvironment(env, false))
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.ReplaceAll(string(stdout), "\r\n", "\n"), "\n")
	profiles := make([]string, 0, len(lines))
	for _, line := range lines {
		profile := strings.TrimSpace(line)
		if profile == "" {
			continue
		}
		if !validProfileName(profile) {
			return nil, &CLIError{Kind: CLIInvalidOutput, Operation: cliOperationListProfiles}
		}
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

func (c *ExecCLI) ExportCredentials(ctx context.Context, profile string, env []string) ([]byte, error) {
	if profile != "" && !validProfileName(profile) {
		return nil, &CLIError{Kind: CLIInvalidOutput, Operation: cliOperationExportCredentials}
	}

	return c.runApproved(ctx, cliOperationExportCredentials, profile, credentialEnvironment(env, profile != ""))
}

func (c *ExecCLI) runApproved(ctx context.Context, operation, profile string, env []string) ([]byte, error) {
	if c == nil || c.path == "" || c.command == nil {
		return nil, &CLIError{Kind: CLIUnsupported, Operation: operation}
	}
	args, ok := approvedCLIArgs(operation, profile)
	if !ok {
		return nil, &CLIError{Kind: CLIUnsupported, Operation: operation}
	}

	stdout := newCappedBuffer(stdoutLimit)
	stderr := newCappedBuffer(stderrLimit)
	cmd := c.command(ctx, c.path, args...)
	if cmd == nil {
		return nil, &CLIError{Kind: CLIUnsupported, Operation: operation}
	}
	cmd.Env = env
	cmd.Stdin = nil
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	configureProcessCancellation(cmd)
	cmd.WaitDelay = commandWaitDelay
	err := cmd.Run()
	if stdout.overflow {
		return nil, classifyCLIError(ctx, operation, nil, &OutputLimitError{Stream: "stdout", Limit: stdoutLimit})
	}
	if stderr.overflow {
		return nil, classifyCLIError(ctx, operation, nil, &OutputLimitError{Stream: "stderr", Limit: stderrLimit})
	}
	if err != nil {
		return nil, classifyCLIError(ctx, operation, stderr.Bytes(), err)
	}
	return stdout.Bytes(), nil
}

func approvedCLIArgs(operation, profile string) ([]string, bool) {
	switch operation {
	case cliOperationListProfiles:
		if profile != "" {
			return nil, false
		}
		return []string{
			"configure", "list-profiles",
			"--no-cli-pager",
			"--no-cli-auto-prompt",
			"--cli-error-format", "json",
		}, true
	case cliOperationExportCredentials:
		args := make([]string, 0, 10)
		if profile != "" {
			args = append(args, "--profile", profile)
		}
		return append(args,
			"configure", "export-credentials",
			"--format", "process",
			"--no-cli-pager",
			"--no-cli-auto-prompt",
			"--cli-error-format", "json",
		), true
	default:
		return nil, false
	}
}

type cappedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func newCappedBuffer(limit int) *cappedBuffer {
	return &cappedBuffer{limit: limit}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.overflow = true
		return written, nil
	}
	if len(p) > remaining {
		_, _ = b.buffer.Write(p[:remaining])
		b.overflow = true
		return written, nil
	}
	_, _ = b.buffer.Write(p)
	return written, nil
}

func (b *cappedBuffer) Bytes() []byte {
	return b.buffer.Bytes()
}
