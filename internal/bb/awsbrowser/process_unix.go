//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package awsbrowser

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureProcessCancellation isolates the CLI in a process group so context
// cancellation reaches descendants as well as the direct child. WaitDelay in
// cli.go remains the backstop for descendants that escape the group or retain
// an inherited pipe after the group has exited.
func configureProcessCancellation(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
