package bb

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func (a *App) confirmAction(question string) (bool, error) {
	if !a.useBubbleSelector() {
		return confirmActionPlain(a, question)
	}
	program := tea.NewProgram(
		newConfirmModel(question, a.getenv("NO_COLOR") != ""),
		tea.WithInput(a.in),
		tea.WithOutput(a.err),
	)
	result, err := program.Run()
	if err != nil {
		return false, fmt.Errorf("run confirmation: %w", err)
	}
	model, ok := result.(confirmModel)
	if !ok {
		return false, fmt.Errorf("confirmation returned an unexpected model")
	}
	return model.confirmed, nil
}

func confirmActionPlain(a *App, question string) (bool, error) {
	if _, err := fmt.Fprintf(a.err, "%s [y/N]: ", safeTerminalText(question)); err != nil {
		return false, err
	}
	answer, err := readLine(a.in)
	if err != nil && strings.TrimSpace(answer) == "" {
		return false, fmt.Errorf("read confirmation: %w", err)
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

type confirmModel struct {
	question  string
	styles    selectorStyles
	width     int
	height    int
	selected  int
	confirmed bool
	done      bool
}

func newConfirmModel(question string, noColor bool) confirmModel {
	return confirmModel{question: safeTerminalText(question), styles: newSelectorStyles(noColor), width: 80, height: 12}
}

func (m confirmModel) Init() tea.Cmd { return nil }

func (m confirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(1, msg.Width)
		m.height = max(1, msg.Height)
	case tea.KeyMsg:
		switch msg.String() {
		case "left", "shift+tab", "h":
			m.selected = 0
		case "right", "tab", "l":
			m.selected = 1
		case "y", "Y":
			m.confirmed = true
			m.done = true
			return m, tea.Quit
		case "n", "N", "esc", "ctrl+c":
			m.done = true
			return m, tea.Quit
		case "enter":
			m.confirmed = m.selected == 1
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m confirmModel) View() string {
	outerWidth := max(1, min(m.width, 80))
	bordered := outerWidth >= 48 && m.height >= 8
	width := outerWidth
	if bordered {
		width = max(1, outerWidth-4)
	}
	question := ansi.Hardwrap(m.question, width, false)
	maxQuestionLines := max(1, m.height-5)
	if bordered {
		maxQuestionLines = max(1, m.height-7)
	}
	question = clampTextLines(question, maxQuestionLines, width)
	cancel := "[ Cancel ]"
	confirm := "[ Confirm ]"
	if m.selected == 0 {
		cancel = m.styles.selected.Render(cancel)
		confirm = m.styles.normal.Render(confirm)
	} else {
		cancel = m.styles.normal.Render(cancel)
		confirm = m.styles.selected.Render(confirm)
	}
	footer := "enter cancel  y confirm"
	if width >= 32 {
		footer = "←→ choose  enter accept  y confirm  esc cancel"
	}
	footer = m.styles.footer.Render(ansi.Truncate(footer, width, "…"))
	parts := []string{
		m.styles.title.Render("Confirm action"),
		question,
		cancel + "  " + confirm,
		footer,
	}
	if m.height >= 10 {
		parts = append(parts[:2], append([]string{""}, parts[2:]...)...)
	}
	content := strings.Join(parts, "\n")
	if m.done {
		return ""
	}
	if bordered {
		return m.styles.box.Border(lipgloss.RoundedBorder()).Padding(0, 1).Width(width + 2).Render(content)
	}
	return lipgloss.NewStyle().Width(width).Render(content)
}

func clampTextLines(value string, limit, width int) string {
	lines := strings.Split(value, "\n")
	if len(lines) <= limit {
		return value
	}
	lines = lines[:limit]
	lines[limit-1] = ansi.Truncate(lines[limit-1], max(1, width-1), "") + "…"
	return strings.Join(lines, "\n")
}
