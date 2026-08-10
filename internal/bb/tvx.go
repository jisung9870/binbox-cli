package bb

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const tvxScanners = "vuln,misconfig,secret"

// tvx is a direct-argv adapter for the former shell command. It deliberately
// does not write Trivy configuration or credentials; output/cache side effects
// remain Trivy's own documented behavior.
func (a *App) tvx(args []string) error {
	if len(args) == 0 || helpRequested(args) {
		_, err := fmt.Fprint(a.out, `Usage:
  bb tvx image <image> [Trivy options...]
  bb tvx repo [path-or-url] [Trivy options...]       (default: .)
  bb tvx config [dir] [Trivy options...]             (default: .)
  bb tvx ci <image|repo|config> [target] [options...] (TVX_CI_SEVERITY; default HIGH,CRITICAL)
  bb tvx sbom <image|repo> [target] [options...]      (CycloneDX)
  bb tvx report <json|sarif> <image|repo|config> [target] [options...]
  bb tvx k8s [context] [options...]                    (summary; node collector disabled)
  bb tvx k8s [context] --with-node-collector [options...]
  bb tvx clean [Trivy clean options...]                (default: --scan-cache)
  bb tvx doctor

The --with-node-collector flag asks for confirmation because Trivy may create
cluster resources. CI policy flags (--severity, --exit-code, --scanners) and
SBOM/report format flags are owned by bb and cannot be overridden.
`)
		return err
	}
	switch args[0] {
	case "image", "repo", "config":
		return a.tvxScan(args[0], args[1:])
	case "ci":
		return a.tvxCI(args[1:])
	case "sbom":
		return a.tvxSBOM(args[1:])
	case "report":
		return a.tvxReport(args[1:])
	case "k8s":
		return a.tvxK8s(args[1:])
	case "clean":
		return a.tvxClean(args[1:])
	case "doctor":
		if len(args) != 1 {
			return usage("tvx doctor", "")
		}
		return a.tvxDoctor()
	default:
		return invalid(fmt.Sprintf("unknown tvx command %q; run 'bb tvx --help'", args[0]))
	}
}

func (a *App) tvxScan(kind string, args []string) error {
	target, rest, err := tvxTarget(kind, args)
	if err != nil {
		return err
	}
	argv := []string{kind}
	if kind != "config" {
		argv = append(argv, "--scanners", tvxScanners)
	}
	argv = append(argv, target)
	return a.tvxRun(argv, rest)
}

func (a *App) tvxCI(args []string) error {
	if len(args) == 0 || (args[0] != "image" && args[0] != "repo" && args[0] != "config") {
		return invalid("usage: bb tvx ci <image|repo|config> [target] [options...]")
	}
	kind := args[0]
	target, rest, err := tvxTarget(kind, args[1:])
	if err != nil {
		return err
	}
	severity := a.getenv("TVX_CI_SEVERITY")
	if severity == "" {
		severity = "HIGH,CRITICAL"
	}
	if !validTVXSeverity(severity) {
		return invalid("invalid TVX_CI_SEVERITY: " + severity)
	}
	if err := rejectTVXPolicy(rest); err != nil {
		return err
	}
	argv := []string{kind}
	if kind != "config" {
		argv = append(argv, "--scanners", tvxScanners)
	}
	argv = append(argv, "--severity", severity, "--exit-code", "1", target)
	return a.tvxRun(argv, rest)
}

func (a *App) tvxSBOM(args []string) error {
	if len(args) == 0 || (args[0] != "image" && args[0] != "repo") {
		return invalid("usage: bb tvx sbom <image|repo> [target] [options...]")
	}
	target, rest, err := tvxTarget(args[0], args[1:])
	if err != nil {
		return err
	}
	if err := rejectTVXFormat(rest); err != nil {
		return err
	}
	return a.tvxRun([]string{args[0], "--format", "cyclonedx", target}, rest)
}

func (a *App) tvxReport(args []string) error {
	if len(args) < 2 || (args[0] != "json" && args[0] != "sarif") || (args[1] != "image" && args[1] != "repo" && args[1] != "config") {
		return invalid("usage: bb tvx report <json|sarif> <image|repo|config> [target] [options...]")
	}
	format, kind := args[0], args[1]
	target, rest, err := tvxTarget(kind, args[2:])
	if err != nil {
		return err
	}
	if err := rejectTVXFormat(rest); err != nil {
		return err
	}
	argv := []string{kind}
	if kind != "config" {
		argv = append(argv, "--scanners", tvxScanners)
	}
	argv = append(argv, "--format", format, target)
	return a.tvxRun(argv, rest)
}

func (a *App) tvxK8s(args []string) error {
	context := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		context, args = args[0], args[1:]
	}
	collector := false
	rest := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--with-node-collector" {
			collector = true
		} else {
			rest = append(rest, arg)
		}
	}
	if collector {
		fmt.Fprintln(a.err, "warning: node collector can create Job/namespace resources in the target cluster")
		fmt.Fprint(a.err, "run node collector? [y/N] ")
		answer, _ := bufio.NewReader(a.in).ReadString('\n')
		if strings.ToLower(strings.TrimSpace(answer)) != "y" && strings.ToLower(strings.TrimSpace(answer)) != "yes" {
			fmt.Fprintln(a.err, "cancelled")
			return nil
		}
	}
	argv := []string{"kubernetes", "--report", "summary"}
	if !collector {
		argv = append(argv, "--disable-node-collector")
	}
	argv = append(argv, rest...)
	if context != "" {
		argv = append(argv, context)
	}
	return a.tvxRun(argv, nil)
}

func (a *App) tvxClean(args []string) error {
	if len(args) == 0 {
		args = []string{"--scan-cache"}
	}
	return a.tvxRun([]string{"clean"}, args)
}

func (a *App) tvxDoctor() error {
	if _, err := a.lookPath("trivy"); err != nil {
		return unavailable("trivy is not installed; install Trivy to use bb tvx")
	}
	if _, err := fmt.Fprintln(a.out, "tvx doctor"); err != nil {
		return err
	}
	if err := a.tvxRun([]string{"version"}, nil); err != nil {
		return err
	}
	for _, file := range []string{"trivy.yaml", ".trivyignore", "trivy-secret.yaml"} {
		state := "absent"
		if _, err := os.Stat(file); err == nil {
			state = "present"
		} else if !os.IsNotExist(err) {
			state = "unreadable"
		}
		if _, err := fmt.Fprintf(a.out, "%s: %s (contents not read; never mutated)\n", file, state); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) tvxRun(argv, rest []string) error {
	if _, err := a.lookPath("trivy"); err != nil {
		return unavailable("trivy is not installed; install Trivy to use bb tvx")
	}
	cmd := a.command("trivy", append(argv, rest...)...)
	cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = a.env, a.in, a.out, a.err
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return &CommandError{Code: "operational_error", Message: fmt.Sprintf("trivy exited with status %d", exit.ExitCode()), Exit: exit.ExitCode(), Cause: err}
		}
		return fmt.Errorf("run trivy: %w", err)
	}
	return nil
}

func tvxTarget(kind string, args []string) (string, []string, error) {
	if kind == "image" {
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			return "", nil, invalid("usage: bb tvx image <image> [Trivy options...]")
		}
		return args[0], args[1:], nil
	}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:], nil
	}
	return ".", args, nil
}

func validTVXSeverity(value string) bool {
	if value == "" {
		return false
	}
	for _, item := range strings.Split(value, ",") {
		switch item {
		case "UNKNOWN", "LOW", "MEDIUM", "HIGH", "CRITICAL":
		default:
			return false
		}
	}
	return true
}

func rejectTVXPolicy(args []string) error {
	for _, arg := range args {
		if arg == "-s" || strings.HasPrefix(arg, "-s") || arg == "--severity" || strings.HasPrefix(arg, "--severity=") || arg == "--exit-code" || strings.HasPrefix(arg, "--exit-code=") || arg == "--scanners" || strings.HasPrefix(arg, "--scanners=") {
			return invalid("tvx ci policy option cannot be overridden: " + arg + " (use TVX_CI_SEVERITY for severity)")
		}
	}
	return nil
}

func rejectTVXFormat(args []string) error {
	for _, arg := range args {
		if arg == "-f" || strings.HasPrefix(arg, "-f") || arg == "--format" || strings.HasPrefix(arg, "--format=") {
			return invalid("tvx output format is selected by the subcommand: " + arg)
		}
	}
	return nil
}
