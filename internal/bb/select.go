package bb

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

type selectChoice struct{ Value, Label string }

// selectOne is the interactive selector shared by bb commands. Real terminals
// get fuzzy filtering through Bubble Tea; pipes, tests, and dumb terminals keep
// the line-oriented selector. UI always goes to stderr so stdout remains a
// machine/eval-safe data channel.
func (a *App) selectOne(prompt string, choices []selectChoice) (string, error) {
	if len(choices) == 0 {
		return "", unavailable("no selectable items")
	}
	if a.useBubbleSelector() {
		return a.selectOneBubble(prompt, choices)
	}
	return selectOnePlain(a.in, a.err, prompt, choices)
}

func (a *App) useBubbleSelector() bool {
	selector := strings.ToLower(strings.TrimSpace(a.getenv("BB_SELECTOR")))
	if selector == "plain" || selector == "numbered" {
		return false
	}
	if selector != "" && selector != "bubble" && selector != "auto" {
		return false
	}
	if strings.EqualFold(a.getenv("TERM"), "dumb") {
		return false
	}
	input, inputOK := a.in.(*os.File)
	output, outputOK := a.err.(*os.File)
	return inputOK && outputOK && isCharacterDevice(input) && isCharacterDevice(output)
}

func isCharacterDevice(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func selectOnePlain(in io.Reader, out io.Writer, prompt string, choices []selectChoice) (string, error) {
	for i, choice := range choices {
		fmt.Fprintf(out, "  %d. %s\n", i+1, choice.Label)
	}
	fmt.Fprintf(out, "%s [1-%d, name, Enter=cancel]: ", prompt, len(choices))
	answer, err := bufio.NewReader(in).ReadString('\n')
	answer = strings.TrimSpace(answer)
	if err != nil && answer == "" {
		return "", fmt.Errorf("read selection: %w", err)
	}
	if answer == "" {
		return "", nil
	}
	if n, conv := strconv.Atoi(answer); conv == nil && n >= 1 && n <= len(choices) {
		return choices[n-1].Value, nil
	}
	for _, choice := range choices {
		if answer == choice.Value {
			return choice.Value, nil
		}
	}
	return "", invalid(fmt.Sprintf("unknown selection %q", answer))
}

type bubbleChoice struct{ value, label string }

func (i bubbleChoice) Title() string       { return i.label }
func (i bubbleChoice) Description() string { return "" }
func (i bubbleChoice) FilterValue() string { return i.label + " " + i.value }

type bubbleSelectorModel struct {
	list      list.Model
	selected  string
	cancelled bool
}

func newBubbleSelectorModel(prompt string, choices []selectChoice) bubbleSelectorModel {
	items := make([]list.Item, 0, len(choices))
	for _, choice := range choices {
		items = append(items, bubbleChoice{value: choice.Value, label: choice.Label})
	}
	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = false
	delegate.SetSpacing(0)
	picker := list.New(items, delegate, 80, 20)
	picker.Title = prompt
	picker.SetShowStatusBar(false)
	picker.SetFilteringEnabled(true)
	return bubbleSelectorModel{list: picker}
}

func (m bubbleSelectorModel) Init() tea.Cmd { return nil }

func (m bubbleSelectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(max(24, msg.Width), max(8, msg.Height))
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEnter:
			if item, ok := m.list.SelectedItem().(bubbleChoice); ok {
				m.selected = item.value
			}
			return m, tea.Quit
		case tea.KeyCtrlC:
			m.cancelled = true
			return m, tea.Quit
		case tea.KeyEsc:
			if m.list.FilterState() != list.Filtering {
				m.cancelled = true
				return m, tea.Quit
			}
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m bubbleSelectorModel) View() string { return m.list.View() }

func (a *App) selectOneBubble(prompt string, choices []selectChoice) (string, error) {
	program := tea.NewProgram(
		newBubbleSelectorModel(prompt, choices),
		tea.WithInput(a.in),
		tea.WithOutput(a.err),
		tea.WithAltScreen(),
	)
	result, err := program.Run()
	if err != nil {
		return "", fmt.Errorf("run selector: %w", err)
	}
	model, ok := result.(bubbleSelectorModel)
	if !ok {
		return "", fmt.Errorf("selector returned an unexpected model")
	}
	if model.cancelled {
		return "", nil
	}
	return model.selected, nil
}
