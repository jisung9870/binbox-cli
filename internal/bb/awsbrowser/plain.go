package awsbrowser

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

var ErrNoInput = errors.New("AWS browser input ended")

type Plain struct{ Dispatcher IntentDispatcher }

var plainCatalog = []catalogItem{homeCatalog[0], homeCatalog[1], homeCatalog[2], homeCatalog[4]}

type plainFrame struct {
	target, label string
	projection    IntentProjection
	context       *AWSContext
	detail        *ResourceProjection
	search        bool
}

type plainInput struct {
	line string
	err  error
}

type plainInputSource struct {
	ch      <-chan plainInput
	pending []plainInput
}

func (source *plainInputSource) next() (plainInput, bool) {
	if len(source.pending) != 0 {
		input := source.pending[0]
		source.pending = source.pending[1:]
		return input, true
	}
	input, open := <-source.ch
	return input, open
}

type plainDispatchResult struct {
	stream IntentStream
	err    error
}

var errPlainBack = errors.New("plain load cancelled")
var errPlainQuit = errors.New("plain load quit")

func (p Plain) Run(ctx context.Context, terminal Terminal, config Config) error {
	reader := bufio.NewReader(terminal.In)
	if sized, ok := terminal.In.(interface{ Len() int }); ok && sized.Len() == 0 {
		return ErrNoInput
	}
	done := make(chan struct{})
	defer close(done)
	inputs := &plainInputSource{ch: plainInputs(reader, done)}
	var history []plainFrame
	for {
		if len(history) == 0 {
			if err := writePlainHome(terminal.Err); err != nil {
				return err
			}
		} else if err := writePlainFrame(terminal.Err, history[len(history)-1]); err != nil {
			return err
		}
		input, open := inputs.next()
		if !open {
			return ErrNoInput
		}
		line, err := input.line, input.err
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
		case "back", "b":
			if len(history) != 0 {
				history = history[:len(history)-1]
			}
		case "refresh":
			if len(history) == 0 || history[len(history)-1].detail != nil || history[len(history)-1].search {
				fmt.Fprintln(terminal.Err, "refresh is available on a resource list")
				continue
			}
			frame := &history[len(history)-1]
			if err := p.load(ctx, terminal.Err, config, frame, Intent{Kind: IntentRefresh, Target: frame.target}, inputs); err != nil {
				if errors.Is(err, errPlainBack) {
					continue
				}
				if errors.Is(err, errPlainQuit) {
					return nil
				}
				return err
			}
		case "search":
			if len(history) == 0 || !history[len(history)-1].search || len(fields) < 4 ||
				!plainChoice(fields[1], searchKinds) || !plainChoice(fields[2], searchScopes) {
				fmt.Fprintln(terminal.Err, "command: search <domain|role|ec2-instances> <all|current> <value>")
				continue
			}
			query := strings.TrimSpace(strings.Join(strings.Fields(line)[3:], " "))
			if query == "" {
				fmt.Fprintln(terminal.Err, "search value is required")
				continue
			}
			if fields[1] == "ec2-instances" && fields[2] != "current" {
				fmt.Fprintln(terminal.Err, "EC2 instance search uses current scope")
				continue
			}
			frame := &history[len(history)-1]
			frame.search = false
			frame.label = "Search results · " + query
			if err := p.load(ctx, terminal.Err, config, frame, Intent{Kind: IntentSearch, Target: frame.target, SearchKind: fields[1], Query: query, Scope: fields[2]}, inputs); err != nil {
				if errors.Is(err, errPlainBack) {
					history = history[:len(history)-1]
					continue
				}
				if errors.Is(err, errPlainQuit) {
					return nil
				}
				return err
			}
		case "open":
			if len(fields) != 2 {
				fmt.Fprintln(terminal.Err, "command: open <n>")
				continue
			}
			n, convErr := strconv.Atoi(fields[1])
			if convErr != nil || n < 1 {
				fmt.Fprintln(terminal.Err, "command: open <n>")
				continue
			}
			if len(history) == 0 {
				if n > len(plainCatalog) {
					fmt.Fprintln(terminal.Err, "command: open <n>")
					continue
				}
				item := plainCatalog[n-1]
				frame := plainFrame{target: item.ID, label: item.Label, search: item.ID == "cross-profile-search"}
				history = append(history, frame)
				if frame.search {
					continue
				}
				kind := IntentOpen
				if err := p.load(ctx, terminal.Err, config, &history[len(history)-1], Intent{Kind: kind, Target: item.ID}, inputs); err != nil {
					if errors.Is(err, errPlainBack) {
						history = history[:len(history)-1]
						continue
					}
					if errors.Is(err, errPlainQuit) {
						return nil
					}
					return err
				}
				continue
			}
			frame := &history[len(history)-1]
			if frame.detail == nil {
				if n > len(frame.projection.Resources) {
					fmt.Fprintln(terminal.Err, "command: open <n>")
					continue
				}
				resource := frame.projection.Resources[n-1]
				history = append(history, plainFrame{target: resource.Target, label: resource.Title, context: frame.context, detail: &resource})
				continue
			}
			if n > len(frame.detail.Relations) {
				fmt.Fprintln(terminal.Err, "command: open <n>")
				continue
			}
			relation := frame.detail.Relations[n-1]
			if relation.Target == "" {
				fmt.Fprintln(terminal.Err, "relation is evidence-only: "+safeIntentText(relation.Reason))
				continue
			}
			next := plainFrame{target: relation.Target, label: relation.Label, context: frame.context}
			history = append(history, next)
			if err := p.load(ctx, terminal.Err, config, &history[len(history)-1], Intent{Kind: IntentOpen, Target: relation.Target}, inputs); err != nil {
				if errors.Is(err, errPlainBack) {
					history = history[:len(history)-1]
					continue
				}
				if errors.Is(err, errPlainQuit) {
					return nil
				}
				return err
			}
		default:
			fmt.Fprintln(terminal.Err, "command: open <n>, back, refresh, or quit")
		}
	}
}

func (p Plain) load(ctx context.Context, out io.Writer, config Config, frame *plainFrame, intent Intent, inputs *plainInputSource) error {
	if p.Dispatcher == nil {
		fmt.Fprintln(out, safeIntentText(frame.label)+": not loaded")
		return nil
	}
	dispatchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	intent.Profile, intent.Region = config.Profile, config.Region
	started := make(chan plainDispatchResult, 1)
	go func() {
		stream, err := p.Dispatcher.Dispatch(dispatchCtx, intent)
		started <- plainDispatchResult{stream: stream, err: err}
	}()
	var stream IntentStream
	var err error
	acquired := false
	select {
	case <-ctx.Done():
		return ctx.Err()
	case result := <-started:
		stream, err, acquired = result.stream, result.err, true
	case <-time.After(time.Millisecond):
	}
	for !acquired {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-started:
			stream, err = result.stream, result.err
			acquired = true
		case input, open := <-inputs.ch:
			if !open || (input.err != nil && strings.TrimSpace(input.line) == "") {
				if !open || errors.Is(input.err, io.EOF) {
					return ErrNoInput
				}
				return input.err
			}
			switch strings.ToLower(strings.TrimSpace(input.line)) {
			case "back", "b", "esc", "cancel":
				return errPlainBack
			case "quit", "q", "exit":
				return errPlainQuit
			default:
				inputs.pending = append(inputs.pending, input)
			}
		}
	}
	if err != nil {
		fmt.Fprintln(out, "! "+safeIntentText(frame.target+": "+err.Error()))
		return nil
	}
	if stream == nil || stream.Updates() == nil {
		fmt.Fprintln(out, "! "+safeIntentText(frame.target)+": no update stream")
		return nil
	}
	defer stream.Cancel()
	terminal := false
	for {
		select {
		case update, ok := <-stream.Updates():
			if !ok {
				if !terminal {
					fmt.Fprintln(out, queryStatus(QueryUpdate{Snapshot: QuerySnapshot{State: LoadUnknown}}, len(frame.projection.Resources))+" · incomplete stream")
				}
				return nil
			}
			terminal = terminal || terminalLoadState(update.Query.Snapshot.State)
			if err := p.applyPlainUpdate(out, frame, intent.Kind, update); err != nil {
				return err
			}
			if terminal {
				return nil
			}
			if update.Done {
				if !terminal {
					fmt.Fprintln(out, queryStatus(QueryUpdate{Snapshot: QuerySnapshot{State: LoadUnknown}}, len(frame.projection.Resources))+" · incomplete stream")
				}
				return nil
			}
			continue
		default:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case input, open := <-inputs.ch:
			if !open {
				return ErrNoInput
			}
			command := strings.ToLower(strings.TrimSpace(input.line))
			if input.err != nil && command == "" {
				if errors.Is(input.err, io.EOF) {
					return ErrNoInput
				}
				return input.err
			}
			switch command {
			case "back", "b", "esc", "cancel":
				return errPlainBack
			case "quit", "q", "exit":
				return errPlainQuit
			default:
				inputs.pending = append(inputs.pending, input)
			}
		case update, ok := <-stream.Updates():
			if !ok {
				if !terminal {
					fmt.Fprintln(out, queryStatus(QueryUpdate{Snapshot: QuerySnapshot{State: LoadUnknown}}, len(frame.projection.Resources))+" · incomplete stream")
				}
				return nil
			}
			terminal = terminal || terminalLoadState(update.Query.Snapshot.State)
			if err := p.applyPlainUpdate(out, frame, intent.Kind, update); err != nil {
				return err
			}
			if terminal {
				return nil
			}
			if update.Done {
				if !terminal {
					fmt.Fprintln(out, queryStatus(QueryUpdate{Snapshot: QuerySnapshot{State: LoadUnknown}}, len(frame.projection.Resources))+" · incomplete stream")
				}
				return nil
			}
		}
	}
}

func (p Plain) applyPlainUpdate(out io.Writer, frame *plainFrame, kind IntentKind, update IntentUpdate) error {
	if update.Context != nil && update.Context.Validate() == nil {
		copy := *update.Context
		frame.context = &copy
	} else if update.Query.Key.Context.Validate() == nil {
		copy := update.Query.Key.Context
		frame.context = &copy
	}
	projection := update.Projection
	if len(projection.Resources) == 0 && update.Query.Snapshot.ResourceCount() != 0 {
		projection = ProjectQueryUpdate(update.Query)
	}
	state := update.Query.Snapshot.State
	preserve := kind == IntentRefresh && state == LoadRefreshing && len(frame.projection.Resources) != 0
	if !preserve && (len(projection.Resources) != 0 || state == LoadReady || state == LoadEmpty) {
		frame.projection = projection
	}
	if _, err := fmt.Fprintln(out, queryStatus(update.Query, len(frame.projection.Resources))); err != nil {
		return err
	}
	writePlainResources(out, frame.projection.Resources)
	return nil
}

func plainInputs(reader *bufio.Reader, done <-chan struct{}) <-chan plainInput {
	inputs := make(chan plainInput, 1)
	go func() {
		defer close(inputs)
		for {
			line, err := reader.ReadString('\n')
			select {
			case inputs <- plainInput{line: line, err: err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return inputs
}

func plainChoice(value string, choices []string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
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

func writePlainFrame(out io.Writer, frame plainFrame) error {
	if _, err := fmt.Fprintln(out, "AWS Browser · READ ONLY"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "AWS > "+safeIntentText(frame.label)); err != nil {
		return err
	}
	if frame.context != nil && frame.context.Validate() == nil {
		profile := frame.context.Profile
		if profile == "" {
			profile = "ambient"
		}
		fmt.Fprintf(out, "Context: %s/%s · %s\n", safeIntentText(frame.context.AccountID), safeIntentText(profile), safeIntentText(frame.context.Region))
	}
	if frame.search {
		fmt.Fprintln(out, "Local editor · no AWS request until submit")
		_, err := fmt.Fprint(out, "command [search <domain|role|ec2-instances> <all|current> <value>|back|quit]: ")
		return err
	}
	if frame.detail == nil {
		writePlainResources(out, frame.projection.Resources)
		_, err := fmt.Fprint(out, "command [open <n>|back|refresh|quit]: ")
		return err
	}
	for _, field := range frame.detail.Fields {
		fmt.Fprintf(out, "%s: %s\n", safeIntentText(field.Label), safeIntentText(field.Value))
	}
	if len(frame.detail.Relations) == 0 {
		fmt.Fprintln(out, "relations: none")
	} else {
		fmt.Fprintln(out, "relations:")
		for index, relation := range frame.detail.Relations {
			target := "evidence only"
			if relation.Target != "" {
				target = relation.Target
			}
			fmt.Fprintf(out, "%d  %s · %s · %s\n", index+1, safeIntentText(relation.Label), safeIntentText(target), safeIntentText(relation.Reason))
			evidence := stringsJoinNonEmpty(relation.Kind, relation.Scope, relation.Operation, relation.ObservedAt)
			if evidence != "" {
				fmt.Fprintln(out, "   evidence: "+safeIntentText(evidence))
			}
		}
	}
	_, err := fmt.Fprint(out, "command [open <n>|back|quit]: ")
	return err
}

func writePlainResources(out io.Writer, resources []ResourceProjection) {
	if len(resources) == 0 {
		fmt.Fprintln(out, "no resources")
		return
	}
	for index, resource := range resources {
		line := fmt.Sprintf("%d  %s", index+1, safeIntentText(resource.Title))
		if resource.Subtitle != "" {
			line += " · " + safeIntentText(resource.Subtitle)
		}
		fmt.Fprintln(out, line)
	}
}
