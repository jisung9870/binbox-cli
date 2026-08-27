package bb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
)

type awsBrowseOptions struct {
	Profile string
	Region  string
}

func parseAWSBrowseOptions(args []string) (awsBrowseOptions, error) {
	var opts awsBrowseOptions
	profileSet, regionSet := false, false
	for len(args) > 0 {
		switch args[0] {
		case "--json":
			return opts, invalid("bb aws browse is interactive; --json was removed; use a scoped 'bb aws query ... --json' command")
		case "--profile", "--region":
			if len(args) < 2 || !validExplicitName(args[1]) {
				return opts, usage("aws browse", "[--profile NAME] [--region REGION]")
			}
			if args[0] == "--profile" {
				if profileSet {
					return opts, invalid("AWS profile may be specified only once")
				}
				if awsbrowser.ValidateContextSelection(args[1], "") != nil {
					return opts, invalid("invalid AWS profile name")
				}
				opts.Profile = args[1]
				profileSet = true
			} else {
				if regionSet {
					return opts, invalid("AWS region may be specified only once")
				}
				if awsbrowser.ValidateContextSelection("", args[1]) != nil {
					return opts, invalid("invalid AWS region")
				}
				opts.Region = args[1]
				regionSet = true
			}
			args = args[2:]
		default:
			return opts, usage("aws browse", "[--profile NAME] [--region REGION]")
		}
	}
	return opts, nil
}

func (a *App) awsBrowse(args []string) error {
	if helpRequested(args) {
		_, err := fmt.Fprint(a.out, `Usage:
  bb aws browse [--profile NAME] [--region REGION]

Opens the read-only interactive AWS browser. The Home screen is local-only;
AWS access starts only after an explicit resource or search intent.
For automation, use a scoped "bb aws query ... --json" command.
`)
		return err
	}
	opts, err := parseAWSBrowseOptions(args)
	if err != nil {
		return err
	}

	terminal := a.awsBrowserTerminal()
	if !terminal.Interactive() {
		return a.reportAWSBrowserTTYGuidance()
	}
	runner := awsbrowser.NewRunner(a.awsBrowserDispatcher)
	err = runner.Run(context.Background(), terminal, awsbrowser.Config{
		Profile: opts.Profile, Region: opts.Region,
		Selector: strings.ToLower(strings.TrimSpace(a.getenv("BB_SELECTOR"))),
	})
	if errors.Is(err, awsbrowser.ErrNoInput) {
		return a.reportAWSBrowserTTYGuidance()
	}
	if err != nil {
		return fmt.Errorf("run AWS browser: %w", err)
	}
	return nil
}

func (a *App) reportAWSBrowserTTYGuidance() error {
	if _, err := fmt.Fprint(a.err, awsbrowser.ScopedQueryGuidance); err != nil {
		return err
	}
	return &CommandError{Code: "invalid_invocation", Message: "aws browse requires an interactive TTY", Exit: ExitInvalidInvocation, Reported: true}
}
