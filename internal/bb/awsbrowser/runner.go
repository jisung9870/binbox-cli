package awsbrowser

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
)

type ProgramRunner func(context.Context, tea.Model, Terminal) (tea.Model, error)

type Runner struct {
	dispatcher IntentDispatcher
	program    ProgramRunner
}

func NewRunner(dispatcher IntentDispatcher) *Runner {
	return &Runner{dispatcher: dispatcher, program: runTeaProgram}
}

func (r *Runner) Run(parent context.Context, terminal Terminal, config Config) error {
	if !terminal.Interactive() {
		return ErrNoInput
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if config.Selector == "plain" || terminal.Small() {
		return (Plain{Dispatcher: r.dispatcher}).Run(ctx, terminal, config)
	}
	model := NewModel(ctx, config, r.dispatcher)
	_, err := r.program(ctx, model, terminal)
	if err != nil {
		return fmt.Errorf("run terminal program: %w", err)
	}
	return nil
}

func runTeaProgram(ctx context.Context, model tea.Model, terminal Terminal) (tea.Model, error) {
	return tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(terminal.In), tea.WithOutput(terminal.Err)).Run()
}
