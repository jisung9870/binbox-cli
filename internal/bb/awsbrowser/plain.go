package awsbrowser

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

var ErrNoInput = errors.New("AWS browser input ended")

type Plain struct{ Dispatcher IntentDispatcher }

var plainCatalog = []catalogItem{homeCatalog[0], homeCatalog[1], homeCatalog[2], homeCatalog[4]}

func (p Plain) Run(ctx context.Context, terminal Terminal, config Config) error {
	reader := bufio.NewReader(terminal.In)
	// Known finite empty inputs (tests, redirected EOF) must fail before the
	// first prompt so the caller can emit only the scoped-query guidance.
	if sized, ok := terminal.In.(interface{ Len() int }); ok && sized.Len() == 0 {
		return ErrNoInput
	}
	for {
		if err := writePlainHome(terminal.Err); err != nil {
			return err
		}
		line, err := reader.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			if errors.Is(err, io.EOF) {
				return ErrNoInput
			}
			return err
		}
		fields := strings.Fields(strings.ToLower(line))
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "quit", "q", "exit":
			return nil
		case "back":
			continue
		case "refresh":
			if p.Dispatcher != nil {
				_ = p.Dispatcher.Dispatch(ctx, Intent{Kind: IntentRefresh, Profile: config.Profile, Region: config.Region})
			}
		case "open":
			if len(fields) != 2 {
				fmt.Fprintln(terminal.Err, "command: open <n>")
				continue
			}
			n, convErr := strconv.Atoi(fields[1])
			if convErr != nil || n < 1 || n > len(plainCatalog) {
				fmt.Fprintln(terminal.Err, "command: open <n>")
				continue
			}
			item := plainCatalog[n-1]
			kind := IntentOpen
			if item.ID == "cross-profile-search" {
				kind = IntentSearch
			}
			if p.Dispatcher != nil {
				_ = p.Dispatcher.Dispatch(ctx, Intent{Kind: kind, Target: item.ID, Profile: config.Profile, Region: config.Region})
			}
			fmt.Fprintf(terminal.Err, "%s: not loaded\n", item.Label)
		default:
			fmt.Fprintln(terminal.Err, "command: open <n>, back, refresh, or quit")
		}
	}
}

func writePlainHome(out io.Writer) error {
	if _, err := fmt.Fprintln(out, "AWS Browser · READ ONLY"); err != nil {
		return err
	}
	for number, item := range plainCatalog {
		label := item.Label
		if item.ID == "route53-hosted-zones" {
			label = "Route 53"
		}
		status := "not loaded"
		if item.ID == "cross-profile-search" {
			if _, err := fmt.Fprintf(out, "%d  %s\n", number+1, label); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(out, "%d  %-20s %s\n", number+1, label, status); err != nil {
			return err
		}
	}
	_, err := fmt.Fprint(out, "command [open <n>|back|refresh|quit]: ")
	return err
}
