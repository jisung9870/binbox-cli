package bb

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

func (a *App) readCommand(name string, args ...string) (string, error) {
	cmd := a.command(name, args...)
	cmd.Env = a.env
	var output, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return "", fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), message)
		}
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return output.String(), nil
}

func parseLimit(args []string) (int, error) {
	if len(args) == 0 {
		return 20, nil
	}
	if len(args) != 2 || args[0] != "--limit" {
		return 0, usage("gx log", "[--limit N]")
	}
	limit, err := strconv.Atoi(args[1])
	if err != nil || limit < 1 || limit > 1000 {
		return 0, invalid("gx log limit must be an integer between 1 and 1000")
	}
	return limit, nil
}

func (a *App) port(args []string) error {
	if helpRequested(args) {
		_, err := fmt.Fprint(a.out, `Usage:
  bb port inspect <1..65535> [--json]
  bb port kill <1..65535> [--yes]
`)
		return err
	}
	args, jsonMode := takeFlag(args, "--json")
	if len(args) >= 1 && args[0] == "kill" {
		if jsonMode {
			return usage("port kill", "<1..65535> [--yes]")
		}
		return a.portKill(args[1:])
	}
	if len(args) != 2 || args[0] != "inspect" {
		return usage("port", "inspect <1..65535> [--json] | kill <1..65535> [--yes]")
	}
	port, err := strconv.Atoi(args[1])
	if err != nil || port < 1 || port > 65535 {
		return invalid("port must be an integer between 1 and 65535")
	}
	listeners, source, err := a.inspectPort(port)
	if err != nil {
		return err
	}
	data := map[string]any{"port": port, "listening": len(listeners) > 0, "source": source, "listeners": listeners}
	if jsonMode {
		return printEnvelope(a.out, data, nil)
	}
	return printHuman(a.out, data)
}

func (a *App) portKill(args []string) error {
	args, yes := takeFlag(args, "--yes")
	if len(args) != 1 {
		return usage("port kill", "<1..65535> [--yes]")
	}
	port, err := validPort(args[0])
	if err != nil {
		return err
	}
	pids, err := a.observePortPIDs(port)
	if err != nil {
		return err
	}
	if len(pids) == 0 {
		_, err := fmt.Fprintf(a.out, "No processes found on port %d.\n", port)
		return err
	}
	fmt.Fprintf(a.out, "Processes using port %d: %s\n", port, strings.Join(pids, ", "))
	if !yes {
		confirmed, confirmErr := a.confirmAction("Send SIGTERM to exactly these processes?")
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			return invalid("port kill cancelled")
		}
	}
	current, err := a.observePortPIDs(port)
	if err != nil {
		return err
	}
	if strings.Join(current, ",") != strings.Join(pids, ",") {
		return unavailable("processes using the port changed during confirmation; inspect and retry")
	}
	return a.runExternal("kill", append([]string{"-TERM", "--"}, pids...)...)
}

func (a *App) observePortPIDs(port int) ([]string, error) {
	if _, err := a.lookPath("lsof"); err != nil {
		return nil, unavailable("lsof is required for exact PID observation before port termination")
	}
	cmd := a.command("lsof", "-nP", "-t", "-i", ":"+strconv.Itoa(port))
	cmd.Env = a.env
	var output, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return []string{}, nil
		}
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return nil, fmt.Errorf("observe port %d processes: %s", port, message)
		}
		return nil, fmt.Errorf("observe port %d processes: %w", port, err)
	}
	seen := map[int]bool{}
	for _, field := range strings.Fields(output.String()) {
		pid, parseErr := strconv.Atoi(field)
		if parseErr != nil || pid < 1 {
			return nil, fmt.Errorf("lsof returned an invalid PID %q", field)
		}
		seen[pid] = true
	}
	values := make([]int, 0, len(seen))
	for pid := range seen {
		values = append(values, pid)
	}
	sort.Ints(values)
	pids := make([]string, 0, len(values))
	for _, pid := range values {
		pids = append(pids, strconv.Itoa(pid))
	}
	return pids, nil
}

type portListener struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	State    string `json:"state,omitempty"`
	Process  string `json:"process,omitempty"`
}

func (a *App) inspectPort(port int) ([]portListener, string, error) {
	if runtime.GOOS != "darwin" {
		if _, err := a.lookPath("ss"); err == nil {
			output, runErr := a.readCommand("ss", "-H", "-ltnup", "sport", "=", ":"+strconv.Itoa(port))
			if runErr == nil {
				return parseSSListeners(output), "ss", nil
			}
		}
	}
	if _, err := a.lookPath("lsof"); err == nil {
		output, runErr := a.readCommand("lsof", "-nP", "-i", ":"+strconv.Itoa(port))
		if runErr != nil {
			var exitErr *exec.ExitError
			if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1 {
				return []portListener{}, "lsof", nil
			}
			return nil, "", runErr
		}
		return parseLsofListeners(output), "lsof", nil
	}
	return nil, "", unavailable("neither ss nor lsof is installed; install one to inspect local ports")
}

func parseSSListeners(output string) []portListener {
	listeners := []portListener{}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		listener := portListener{Protocol: fields[0], State: fields[1], Address: fields[4]}
		if len(fields) > 6 {
			listener.Process = strings.Join(fields[6:], " ")
		}
		listeners = append(listeners, listener)
	}
	return listeners
}

func parseLsofListeners(output string) []portListener {
	listeners := []portListener{}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 9 || fields[0] == "COMMAND" {
			continue
		}
		protocol := strings.ToLower(fields[7])
		if protocol != "tcp" && protocol != "udp" {
			continue
		}
		listener := portListener{Protocol: protocol, Process: fields[0] + " " + fields[1]}
		listener.Address = fields[8]
		if strings.Contains(strings.Join(fields[9:], " "), "(LISTEN)") {
			listener.State = "LISTEN"
		}
		listeners = append(listeners, listener)
	}
	return listeners
}
