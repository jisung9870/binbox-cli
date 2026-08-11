package bb

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type selectChoice struct {
	Value       string
	Label       string
	Description string
	SearchText  string
}

// selectOne is the interactive selector shared by bb commands. Real terminals
// get a search-first Bubble Tea UI; pipes, tests, and dumb terminals keep the
// line-oriented selector. UI always goes to stderr so stdout remains a
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
		label := safeTerminalText(choice.Label)
		if choice.Description != "" {
			label += " — " + safeTerminalText(choice.Description)
		}
		if _, err := fmt.Fprintf(out, "  %d. %s\n", i+1, label); err != nil {
			return "", err
		}
	}
	if _, err := fmt.Fprintf(out, "%s [1-%d, name, Enter=cancel]: ", safeTerminalText(prompt), len(choices)); err != nil {
		return "", err
	}
	answer, err := readLine(in)
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
	matchedValue := ""
	for _, choice := range choices {
		if answer != choice.Label {
			continue
		}
		if matchedValue != "" {
			return "", invalid(fmt.Sprintf("selection name %q is ambiguous; use its number or exact value", answer))
		}
		matchedValue = choice.Value
	}
	if matchedValue != "" {
		return matchedValue, nil
	}
	return "", invalid(fmt.Sprintf("unknown selection %q", answer))
}

// readLine deliberately reads one byte at a time so sequential plain-mode
// prompts cannot lose buffered input when each prompt returns to its caller.
func readLine(in io.Reader) (string, error) {
	var value bytes.Buffer
	var one [1]byte
	for {
		_, err := io.ReadFull(in, one[:])
		if err != nil {
			return value.String(), err
		}
		value.WriteByte(one[0])
		if one[0] == '\n' {
			return value.String(), nil
		}
	}
}

type bubbleChoice struct {
	value       string
	label       string
	description string
	searchText  string
}

func (i bubbleChoice) filterValue() string {
	return strings.Join([]string{i.label, i.description, safeTerminalText(i.value), i.searchText}, " ")
}

func safeTerminalText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Bidi_Control) {
			return -1
		}
		return r
	}, value)
}

type selectorMatch struct {
	choice  bubbleChoice
	matched []int
}

type selectorStyles struct {
	box, title, count, prompt, query, selected, normal, detail, matched, footer, empty lipgloss.Style
}

func newSelectorStyles(noColor bool) selectorStyles {
	styles := selectorStyles{
		box:      lipgloss.NewStyle(),
		title:    lipgloss.NewStyle(),
		count:    lipgloss.NewStyle(),
		prompt:   lipgloss.NewStyle(),
		query:    lipgloss.NewStyle(),
		selected: lipgloss.NewStyle(),
		normal:   lipgloss.NewStyle(),
		detail:   lipgloss.NewStyle(),
		matched:  lipgloss.NewStyle(),
		footer:   lipgloss.NewStyle(),
		empty:    lipgloss.NewStyle(),
	}
	if noColor {
		return styles
	}
	accent := lipgloss.AdaptiveColor{Light: "#5B50D6", Dark: "#A78BFA"}
	selectedBackground := lipgloss.AdaptiveColor{Light: "#E9E7FF", Dark: "#2D2842"}
	muted := lipgloss.AdaptiveColor{Light: "#626262", Dark: "#9A9A9A"}
	border := lipgloss.AdaptiveColor{Light: "#C9C7D8", Dark: "#4C465F"}
	styles.box = styles.box.BorderForeground(border)
	styles.title = styles.title.Bold(true).Foreground(accent)
	styles.count = styles.count.Foreground(muted)
	styles.prompt = styles.prompt.Bold(true).Foreground(accent)
	styles.selected = styles.selected.Bold(true).Foreground(accent).Background(selectedBackground)
	styles.detail = styles.detail.Foreground(muted)
	styles.matched = styles.matched.Underline(true).Bold(true)
	styles.footer = styles.footer.Foreground(muted)
	styles.empty = styles.empty.Foreground(muted).Italic(true)
	return styles
}

type bubbleSelectorModel struct {
	prompt    string
	choices   []bubbleChoice
	matches   []selectorMatch
	input     textinput.Model
	styles    selectorStyles
	width     int
	height    int
	cursor    int
	offset    int
	selected  string
	cancelled bool
	noColor   bool
}

func newBubbleSelectorModel(prompt string, choices []selectChoice) bubbleSelectorModel {
	return newBubbleSelectorModelWithColor(prompt, choices, false)
}

func newBubbleSelectorModelWithColor(prompt string, choices []selectChoice, noColor bool) bubbleSelectorModel {
	items := make([]bubbleChoice, 0, len(choices))
	for _, choice := range choices {
		items = append(items, bubbleChoice{
			value:       choice.Value,
			label:       safeTerminalText(choice.Label),
			description: safeTerminalText(choice.Description),
			searchText:  safeTerminalText(choice.SearchText),
		})
	}
	query := textinput.New()
	query.Prompt = ""
	query.Placeholder = "Type to search…"
	query.CharLimit = 128
	query.Focus()
	model := bubbleSelectorModel{
		prompt:  safeTerminalText(prompt),
		choices: items,
		input:   query,
		styles:  newSelectorStyles(noColor),
		width:   80,
		height:  20,
		noColor: noColor,
	}
	model.refreshMatches()
	return model
}

func (m bubbleSelectorModel) Init() tea.Cmd { return textinput.Blink }

func (m bubbleSelectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(1, msg.Width)
		m.height = max(1, msg.Height)
		m.ensureVisible()
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "/":
			if m.input.Value() == "" {
				return m, nil
			}
		case "enter":
			if len(m.matches) > 0 {
				m.selected = m.matches[m.cursor].choice.value
				return m, tea.Quit
			}
			return m, nil
		case "ctrl+c":
			m.cancelled = true
			return m, tea.Quit
		case "esc":
			if m.input.Value() != "" {
				m.input.SetValue("")
				m.refreshMatches()
				return m, nil
			}
			m.cancelled = true
			return m, tea.Quit
		case "up", "ctrl+p", "shift+tab":
			m.move(-1)
			return m, nil
		case "down", "ctrl+n", "tab":
			m.move(1)
			return m, nil
		case "pgup":
			m.move(-m.pageSize())
			return m, nil
		case "pgdown":
			m.move(m.pageSize())
			return m, nil
		case "home":
			m.cursor = 0
			m.ensureVisible()
			return m, nil
		case "end":
			if len(m.matches) > 0 {
				m.cursor = len(m.matches) - 1
				m.ensureVisible()
			}
			return m, nil
		}
	}

	before := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	sanitized := safeTerminalText(m.input.Value())
	if sanitized != m.input.Value() {
		m.input.SetValue(sanitized)
	}
	if sanitized != before {
		m.refreshMatches()
	}
	return m, cmd
}

func (m *bubbleSelectorModel) refreshMatches() {
	query := strings.TrimSpace(m.input.Value())
	if query == "" {
		m.matches = make([]selectorMatch, len(m.choices))
		for i, choice := range m.choices {
			m.matches[i] = selectorMatch{choice: choice}
		}
	} else {
		targets := make([]string, len(m.choices))
		for i, choice := range m.choices {
			targets[i] = choice.filterValue()
		}
		ranks := list.DefaultFilter(query, targets)
		m.matches = make([]selectorMatch, len(ranks))
		for i, rank := range ranks {
			m.matches[i] = selectorMatch{choice: m.choices[rank.Index], matched: rank.MatchedIndexes}
		}
	}
	m.cursor = 0
	m.offset = 0
}

func (m *bubbleSelectorModel) move(delta int) {
	if len(m.matches) == 0 {
		return
	}
	m.cursor = min(max(0, m.cursor+delta), len(m.matches)-1)
	m.ensureVisible()
}

func (m bubbleSelectorModel) showDescriptions() bool {
	if m.width < 50 {
		return false
	}
	for _, choice := range m.choices {
		if choice.description != "" {
			return true
		}
	}
	return false
}

func (m bubbleSelectorModel) pageSize() int {
	reserved := 7
	rows := max(1, m.height-reserved)
	if m.showDescriptions() {
		rows /= 2
	}
	return max(1, rows)
}

func (m *bubbleSelectorModel) ensureVisible() {
	page := m.pageSize()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+page {
		m.offset = m.cursor - page + 1
	}
	maxOffset := max(0, len(m.matches)-page)
	m.offset = min(max(0, m.offset), maxOffset)
}

func (m bubbleSelectorModel) View() string {
	boxWidth := max(1, min(m.width, 96))
	bordered := boxWidth >= 50 && m.height >= 10
	innerWidth := boxWidth
	if bordered {
		innerWidth -= 4
	} else {
		innerWidth = max(1, innerWidth-2)
	}

	title := "Select " + m.prompt
	resultWord := "results"
	if len(m.matches) == 1 {
		resultWord = "result"
	}
	count := fmt.Sprintf("%d/%d %s", len(m.matches), len(m.choices), resultWord)
	header := padBetween(m.styles.title.Render(title), m.styles.count.Render(count), innerWidth)
	if innerWidth < 40 {
		header = ansi.Truncate(m.styles.title.Render(title), innerWidth, "…") + "\n" + m.styles.count.Render(count)
	}

	input := m.input
	input.SetValue(safeTerminalText(input.Value()))
	input.Width = max(1, innerWidth-8)
	if !m.noColor {
		input.TextStyle = m.styles.query
		input.PlaceholderStyle = m.styles.detail
		input.Cursor.Style = m.styles.prompt
	}
	search := m.styles.prompt.Render("Search") + "  " + input.View()
	separator := strings.Repeat("─", innerWidth)

	var rows []string
	if len(m.matches) == 0 {
		query := ansi.Truncate(safeTerminalText(m.input.Value()), max(1, innerWidth-20), "…")
		rows = append(rows, m.styles.empty.Render(fmt.Sprintf("No matches for %q", query)))
	} else {
		end := min(len(m.matches), m.offset+m.pageSize())
		for index := m.offset; index < end; index++ {
			match := m.matches[index]
			selected := index == m.cursor
			marker := "  "
			style := m.styles.normal
			if selected {
				marker = "> "
				style = m.styles.selected
			}
			labelWidth := max(1, innerWidth-lipgloss.Width(marker))
			label := ansi.Truncate(match.choice.label, labelWidth, "…")
			if len(match.matched) > 0 && !m.noColor {
				label = highlightLabelMatches(label, match.matched, m.styles.matched, style)
			} else {
				label = style.Render(label)
			}
			rows = append(rows, marker+label)
			if m.showDescriptions() && match.choice.description != "" {
				detail := ansi.Truncate(match.choice.description, max(1, innerWidth-4), "…")
				rows = append(rows, "    "+m.styles.detail.Render(detail))
			}
		}
	}

	footer := "↑↓ move  enter select  esc clear/cancel"
	if innerWidth < 40 {
		footer = "↑↓ move  enter select\nesc clear/cancel"
	}
	if innerWidth >= 64 {
		footer = "↑↓/ctrl+n,p move  enter select  esc clear/cancel  ctrl+c quit"
	}
	footerLines := strings.Split(footer, "\n")
	for i := range footerLines {
		footerLines[i] = ansi.Truncate(footerLines[i], innerWidth, "…")
	}
	footer = strings.Join(footerLines, "\n")
	content := strings.Join([]string{
		header,
		search,
		separator,
		strings.Join(rows, "\n"),
		separator,
		m.styles.footer.Render(footer),
	}, "\n")

	if bordered {
		box := m.styles.box.Border(lipgloss.RoundedBorder()).Padding(0, 1).Width(innerWidth + 2).Render(content)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
	}
	box := lipgloss.NewStyle().Padding(0, 1).Width(innerWidth + 2).Render(content)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func highlightLabelMatches(label string, indexes []int, matched, base lipgloss.Style) string {
	labelRunes := []rune(label)
	valid := make([]int, 0, len(indexes))
	for _, index := range indexes {
		if index >= 0 && index < len(labelRunes) {
			valid = append(valid, index)
		}
	}
	return lipgloss.StyleRunes(label, valid, base.Inherit(matched), base)
}

func padBetween(left, right string, width int) string {
	space := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	return ansi.Truncate(left+strings.Repeat(" ", space)+right, width, "…")
}

func (a *App) selectOneBubble(prompt string, choices []selectChoice) (string, error) {
	program := tea.NewProgram(
		newBubbleSelectorModelWithColor(prompt, choices, a.getenv("NO_COLOR") != ""),
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
