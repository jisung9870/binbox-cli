package awsbrowser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecCLICancellationTerminatesProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	cli := helperExecCLI("spawn-tree", pidFile)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := cli.ExportCredentials(ctx, "", nil)
		result <- err
	}()

	pid := waitForHelperPID(t, pidFile)
	defer func() {
		if process, err := os.FindProcess(pid); err == nil {
			_ = process.Kill()
		}
	}()
	cancel()

	select {
	case err := <-result:
		var cliError *CLIError
		if !errors.As(err, &cliError) || cliError.Kind != CLICancelled {
			t.Fatalf("error=%v want CLICancelled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("process-group cancellation did not return")
	}

	deadline := time.Now().Add(2 * time.Second)
	for linuxProcessAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if linuxProcessAlive(pid) {
		t.Fatalf("grandchild PID %d remained alive after cancellation", pid)
	}
}

func waitForHelperPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr != nil {
				t.Fatalf("parse helper PID %q: %v", data, parseErr)
			}
			return pid
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read helper PID: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("helper did not publish grandchild PID")
	return 0
}

func linuxProcessAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return false
	}
	data, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if readErr != nil {
		return !errors.Is(readErr, os.ErrNotExist)
	}
	fields := strings.Fields(string(data))
	return len(fields) < 3 || fields[2] != "Z"
}
