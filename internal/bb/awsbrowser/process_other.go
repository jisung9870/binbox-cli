//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package awsbrowser

import "os/exec"

// CommandContext's direct-child cancellation is the portable fallback. The
// command's WaitDelay still bounds shutdown if a descendant inherits a pipe.
func configureProcessCancellation(_ *exec.Cmd) {}
