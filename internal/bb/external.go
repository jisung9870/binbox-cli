package bb

import (
	"bufio"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// runExternal deliberately bypasses a shell. Compatibility commands accept
// structured arguments and pass them directly to the owning system CLI.
func (a *App) runExternal(name string, args ...string) error {
	if _, err := a.lookPath(name); err != nil {
		return unavailable(fmt.Sprintf("%s is not installed; run 'bb doctor' for dependency guidance", name))
	}
	cmd := a.command(name, args...)
	cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = a.env, a.in, a.out, a.err
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func (a *App) gx(args []string) error {
	if helpRequested(args) || len(args) == 0 {
		_, err := fmt.Fprint(a.out, `Usage:
  bb gx root
  bb gx branch list [--all]
  bb gx branch switch <name>
  bb gx branch new <name> [base]
  bb gx branch delete <name> [--force] [--yes]
  bb gx log [--limit N]

Mutating operations require an explicit branch name. Arguments are passed
directly to git; bb never evaluates them through a shell.
`)
		return err
	}
	switch args[0] {
	case "root":
		if len(args) != 1 {
			return usage("gx root", "")
		}
		return a.runExternal("git", "rev-parse", "--show-toplevel")
	case "log":
		limit, err := parseLimit(args[1:])
		if err != nil {
			return err
		}
		return a.runExternal("git", "log", "--graph", "--decorate", "--oneline", "-n", strconv.Itoa(limit))
	case "branch", "br":
		return a.gxBranch(args[1:])
	default:
		return invalid(fmt.Sprintf("unknown gx command %q", args[0]))
	}
}

func (a *App) gxBranch(args []string) error {
	if len(args) == 0 {
		return usage("gx branch", "list|switch|new|delete")
	}
	switch args[0] {
	case "list":
		if len(args) == 1 {
			return a.runExternal("git", "branch", "--list")
		}
		if len(args) == 2 && args[1] == "--all" {
			return a.runExternal("git", "branch", "--all", "--list")
		}
		return usage("gx branch list", "[--all]")
	case "switch":
		if len(args) != 2 || !validExplicitName(args[1]) {
			return usage("gx branch switch", "<name>")
		}
		return a.runExternal("git", "switch", "--", args[1])
	case "new", "create":
		if len(args) < 2 || len(args) > 3 || !validExplicitName(args[1]) || (len(args) == 3 && !validExplicitName(args[2])) {
			return usage("gx branch new", "<name> [base]")
		}
		argv := []string{"switch", "-c", args[1]}
		if len(args) == 3 {
			argv = append(argv, args[2])
		}
		return a.runExternal("git", argv...)
	case "delete":
		args, force := takeFlag(args, "--force")
		args, yes := takeFlag(args, "--yes")
		if len(args) != 2 || !validExplicitName(args[1]) {
			return usage("gx branch delete", "<name> [--force] [--yes]")
		}
		flag := "-d"
		if force {
			flag = "-D"
		}
		current, err := a.readCommand("git", "branch", "--show-current")
		if err != nil {
			return err
		}
		if strings.TrimSpace(current) == args[1] {
			return invalid("refusing to delete the currently checked-out branch")
		}
		ref := "refs/heads/" + args[1]
		before, err := a.readCommand("git", "rev-parse", "--verify", ref)
		if err != nil {
			return err
		}
		before = strings.TrimSpace(before)
		fmt.Fprintf(a.out, "Target Git branch: %s (%s)\n", args[1], before)
		if !yes {
			ok, err := a.confirmExternal("Delete this local branch? [y/N] ")
			if err != nil {
				return err
			}
			if !ok {
				return invalid("branch deletion cancelled")
			}
		}
		current, err = a.readCommand("git", "branch", "--show-current")
		if err != nil || strings.TrimSpace(current) == args[1] {
			return unavailable("current branch changed during confirmation; inspect and retry")
		}
		after, err := a.readCommand("git", "rev-parse", "--verify", ref)
		if err != nil || strings.TrimSpace(after) != before {
			return unavailable("target branch changed during confirmation; inspect and retry")
		}
		return a.runExternal("git", "branch", flag, "--", args[1])
	default:
		return invalid(fmt.Sprintf("unknown gx branch command %q", args[0]))
	}
}

func validExplicitName(value string) bool {
	return value != "" && !strings.HasPrefix(value, "-") && !strings.ContainsAny(value, "\x00\r\n")
}

func (a *App) confirmExternal(question string) (bool, error) {
	fmt.Fprint(a.out, question)
	answer, err := bufio.NewReader(a.in).ReadString('\n')
	if err != nil && strings.TrimSpace(answer) == "" {
		return false, fmt.Errorf("read confirmation: %w", err)
	}
	return strings.ToLower(strings.TrimSpace(answer)) == "y", nil
}

func (a *App) kx(args []string) error {
	if helpRequested(args) || len(args) == 0 {
		_, err := fmt.Fprint(a.out, `Usage:
  bb kx context list
  bb kx context use <name>
  bb kx namespace list
  bb kx namespace use <name>
  bb kx log <pod> [-n namespace] [--tail N]
  bb kx exec <pod> [-n namespace] [-c container] [-- command ...]
  bb kx port-forward <pod> <local-port>:<remote-port> [-n namespace]
`)
		return err
	}
	switch args[0] {
	case "context", "ctx":
		return a.kxContext(args[1:])
	case "namespace", "ns":
		return a.kxNamespace(args[1:])
	case "log", "logs":
		return a.kxLog(args[1:])
	case "exec":
		return a.kxExec(args[1:])
	case "port-forward", "pf":
		return a.kxPortForward(args[1:])
	default:
		return invalid(fmt.Sprintf("unknown kx command %q", args[0]))
	}
}

func (a *App) kxContext(args []string) error {
	if len(args) == 1 && args[0] == "list" {
		return a.runExternal("kubectl", "config", "get-contexts")
	}
	if len(args) == 2 && args[0] == "use" && validExplicitName(args[1]) {
		return a.runExternal("kubectl", "config", "use-context", args[1])
	}
	return usage("kx context", "list|use <name>")
}

func (a *App) kxNamespace(args []string) error {
	if len(args) == 1 && args[0] == "list" {
		return a.runExternal("kubectl", "get", "namespaces")
	}
	if len(args) == 2 && args[0] == "use" && validExplicitName(args[1]) {
		return a.runExternal("kubectl", "config", "set-context", "--current", "--namespace="+args[1])
	}
	return usage("kx namespace", "list|use <name>")
}

func parseKubeOptions(args []string, allowContainer, allowTail bool) (pos, flags []string, err error) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-n", "--namespace":
			if i+1 >= len(args) {
				return nil, nil, invalid(args[i] + " requires a value")
			}
			if !validExplicitName(args[i+1]) {
				return nil, nil, invalid("namespace must be an explicit non-option value")
			}
			flags = append(flags, "--namespace="+args[i+1])
			i++
		case "-c", "--container":
			if !allowContainer || i+1 >= len(args) {
				return nil, nil, invalid(args[i] + " is not valid here")
			}
			if !validExplicitName(args[i+1]) {
				return nil, nil, invalid("container must be an explicit non-option value")
			}
			flags = append(flags, "--container="+args[i+1])
			i++
		case "--tail":
			if !allowTail || i+1 >= len(args) {
				return nil, nil, invalid("--tail requires a value")
			}
			n, e := strconv.Atoi(args[i+1])
			if e != nil || n < 1 {
				return nil, nil, invalid("--tail must be a positive integer")
			}
			flags = append(flags, "--tail", args[i+1])
			i++
		default:
			pos = append(pos, args[i])
		}
	}
	return pos, flags, nil
}

func (a *App) kxLog(args []string) error {
	pos, flags, err := parseKubeOptions(args, false, true)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usage("kx log", "<pod> [-n namespace] [--tail N]")
	}
	if !validExplicitName(pos[0]) {
		return invalid("pod name must be an explicit non-option value")
	}
	return a.runExternal("kubectl", append([]string{"logs", pos[0]}, flags...)...)
}

func (a *App) kxExec(args []string) error {
	separator := len(args)
	for i, arg := range args {
		if arg == "--" {
			separator = i
			break
		}
	}
	pos, flags, err := parseKubeOptions(args[:separator], true, false)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usage("kx exec", "<pod> [-n namespace] [-c container] [-- command ...]")
	}
	if !validExplicitName(pos[0]) {
		return invalid("pod name must be an explicit non-option value")
	}
	command := []string{"/bin/sh"}
	if separator < len(args)-1 {
		command = args[separator+1:]
	}
	argv := append([]string{"exec", "-it", pos[0]}, flags...)
	argv = append(argv, "--")
	argv = append(argv, command...)
	return a.runExternal("kubectl", argv...)
}

var portPairPattern = regexp.MustCompile(`^[1-9][0-9]{0,4}:[1-9][0-9]{0,4}$`)

func (a *App) kxPortForward(args []string) error {
	pos, flags, err := parseKubeOptions(args, false, false)
	if err != nil {
		return err
	}
	if len(pos) != 2 || !validPortPair(pos[1]) {
		return usage("kx port-forward", "<pod> <local-port>:<remote-port> [-n namespace]")
	}
	if !validExplicitName(pos[0]) {
		return invalid("pod name must be an explicit non-option value")
	}
	return a.runExternal("kubectl", append([]string{"port-forward", pos[0], pos[1]}, flags...)...)
}

func validPortPair(value string) bool {
	if !portPairPattern.MatchString(value) {
		return false
	}
	parts := strings.Split(value, ":")
	for _, p := range parts {
		n, _ := strconv.Atoi(p)
		if n > 65535 {
			return false
		}
	}
	return true
}

var instanceIDPattern = regexp.MustCompile(`^i-[0-9a-fA-F]{8,17}$`)

func (a *App) assm(args []string) error {
	if helpRequested(args) || len(args) == 0 {
		_, err := fmt.Fprint(a.out, `Usage:
  bb assm shell <instance-id>
  bb assm port-forward <instance-id> <remote-port> [local-port] [host]
`)
		return err
	}
	if args[0] == "shell" {
		if len(args) != 2 || !instanceIDPattern.MatchString(args[1]) {
			return usage("assm shell", "<instance-id>")
		}
		return a.runExternal("aws", "ssm", "start-session", "--target", args[1])
	}
	if args[0] != "port-forward" && args[0] != "pf" {
		return invalid(fmt.Sprintf("unknown assm command %q", args[0]))
	}
	if len(args) < 3 || len(args) > 5 || !instanceIDPattern.MatchString(args[1]) {
		return usage("assm port-forward", "<instance-id> <remote-port> [local-port] [host]")
	}
	remote, err := validPort(args[2])
	if err != nil {
		return err
	}
	local := remote
	if len(args) >= 4 {
		local, err = validPort(args[3])
		if err != nil {
			return err
		}
	}
	document := "AWS-StartPortForwardingSession"
	params := map[string][]string{"portNumber": {strconv.Itoa(remote)}, "localPortNumber": {strconv.Itoa(local)}}
	if len(args) == 5 {
		if strings.TrimSpace(args[4]) == "" {
			return invalid("host must not be empty")
		}
		document = "AWS-StartPortForwardingSessionToRemoteHost"
		params["host"] = []string{args[4]}
	}
	encoded, _ := json.Marshal(params)
	return a.runExternal("aws", "ssm", "start-session", "--target", args[1], "--document-name", document, "--parameters", string(encoded))
}

func validPort(value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, invalid("port must be an integer between 1 and 65535")
	}
	return port, nil
}
