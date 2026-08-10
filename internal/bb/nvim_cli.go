package bb

import (
	"context"
	"fmt"
	"path/filepath"
	"time"
)

func (a *App) setup(args []string) error {
	if len(args) == 0 || helpRequested(args) {
		_, err := fmt.Fprintln(a.out, "Usage: bb setup nvim --config-dir <path> [--repository <url>] [--revision <commit>] [--lockfile-sha256 <sha>] [--dry-run | --apply --consent] [--json]")
		return err
	}
	if args[0] != "nvim" {
		return invalid(fmt.Sprintf("unknown setup command %q", args[0]))
	}
	args, jsonMode := takeFlag(args[1:], "--json")
	request, dryRun, err := a.parseNvimSetup(args)
	if err != nil {
		return err
	}
	var plan NvimSetupPlan
	if request.Apply {
		plan, err = ApplyNvimSetup(request)
	} else {
		plan, err = PlanNvimSetup(request)
	}
	if err != nil {
		return err
	}
	data := map[string]any{"mode": map[bool]string{true: "apply", false: "dry-run"}[request.Apply], "dry_run": dryRun || !request.Apply, "plan": plan}
	if jsonMode {
		return printEnvelope(a.out, data, nil)
	}
	return printJSON(a.out, data)
}

func (a *App) parseNvimSetup(args []string) (NvimSetupRequest, bool, error) {
	configRoot, _, err := a.paths()
	if err != nil {
		return NvimSetupRequest{}, false, err
	}
	request := NvimSetupRequest{XDGConfigHome: filepath.Dir(configRoot)}
	dryRun := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config-dir", "--repository", "--revision", "--lockfile-sha256":
			if i+1 >= len(args) {
				return request, false, invalid(args[i] + " requires a value")
			}
			value := args[i+1]
			i++
			switch args[i-1] {
			case "--config-dir":
				request.ConfigDir = value
			case "--repository":
				request.Expected.Repository = value
			case "--revision":
				request.Expected.Revision = value
			case "--lockfile-sha256":
				request.Expected.LockfileSHA256 = value
			}
		case "--dry-run":
			dryRun = true
		case "--apply":
			request.Apply = true
		case "--consent":
			request.Consent = true
		default:
			return request, false, invalid(fmt.Sprintf("unknown setup nvim option %q", args[i]))
		}
	}
	if request.ConfigDir == "" {
		return request, false, invalid("--config-dir is required")
	}
	if dryRun && request.Apply {
		return request, false, invalid("--dry-run and --apply are mutually exclusive")
	}
	if request.Consent && !request.Apply {
		return request, false, invalid("--consent requires --apply")
	}
	return request, dryRun, nil
}

func (a *App) doctorNvim(args []string) error {
	if helpRequested(args) {
		_, err := fmt.Fprintln(a.out, "Usage: bb doctor nvim --config-dir <path> [--repository <url>] [--revision <commit>] [--lockfile-sha256 <sha>] [--headless] [--json]")
		return err
	}
	args, jsonMode := takeFlag(args, "--json")
	configRoot, _, err := a.paths()
	if err != nil {
		return err
	}
	options := NvimDoctorOptions{XDGConfigHome: filepath.Dir(configRoot)}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config-dir", "--repository", "--revision", "--lockfile-sha256":
			if i+1 >= len(args) {
				return invalid(args[i] + " requires a value")
			}
			value := args[i+1]
			i++
			switch args[i-1] {
			case "--config-dir":
				options.ConfigDir = value
			case "--repository":
				options.Expected.Repository = value
			case "--revision":
				options.Expected.Revision = value
			case "--lockfile-sha256":
				options.Expected.LockfileSHA256 = value
			}
		case "--headless":
			options.Headless = true
		default:
			return invalid(fmt.Sprintf("unknown doctor nvim option %q", args[i]))
		}
	}
	if options.ConfigDir == "" {
		return invalid("--config-dir is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	report, err := DoctorNvim(ctx, options)
	if err != nil {
		return err
	}
	if jsonMode {
		return printEnvelope(a.out, report, nil)
	}
	return printJSON(a.out, report)
}
