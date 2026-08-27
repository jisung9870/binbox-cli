package awsbrowser

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
)

const (
	stdoutLimit = 32 << 10
	stderrLimit = 64 << 10
)

// CLI is the only AWS CLI surface used by the browser data plane. It is
// intentionally limited to credential export; resource providers use the SDK.
type CLI interface {
	Run(ctx context.Context, args, env []string) (stdout, stderr []byte, err error)
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

type OutputLimitError struct {
	Stream string
	Limit  int
}

func (e *OutputLimitError) Error() string {
	return fmt.Sprintf("AWS CLI %s exceeded %d bytes", e.Stream, e.Limit)
}

func (c *ExecCLI) Run(ctx context.Context, args, env []string) ([]byte, []byte, error) {
	if c == nil || c.path == "" || c.command == nil {
		return nil, nil, errors.New("AWS CLI runner is not configured")
	}

	stdout := newCappedBuffer(stdoutLimit)
	stderr := newCappedBuffer(stderrLimit)
	cmd := c.command(ctx, c.path, args...)
	cmd.Env = env
	cmd.Stdin = nil
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if stdout.overflow {
		return nil, nil, &OutputLimitError{Stream: "stdout", Limit: stdoutLimit}
	}
	if stderr.overflow {
		return nil, nil, &OutputLimitError{Stream: "stderr", Limit: stderrLimit}
	}
	return stdout.Bytes(), stderr.Bytes(), err
}

type cappedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func newCappedBuffer(limit int) *cappedBuffer {
	return &cappedBuffer{limit: limit}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.overflow = true
		return written, nil
	}
	if len(p) > remaining {
		_, _ = b.Buffer.Write(p[:remaining])
		b.overflow = true
		return written, nil
	}
	_, _ = b.Buffer.Write(p)
	return written, nil
}
