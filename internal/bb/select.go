package bb

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type selectChoice struct {
	Value       string
	Label       string
	Description string
	SearchText  string
}

type selectOutcome struct {
	Value       string
	Interrupted bool
}

// selectOne is the interactive selector shared by bb commands. Real terminals
// get a search-first Bubble Tea UI; pipes, tests, and dumb terminals keep the
// line-oriented selector. UI always goes to stderr so stdout remains a
// machine/eval-safe data channel.
func (a *App) selectOne(prompt string, choices []selectChoice) (string, error) {
	result, err := a.selectOneOutcome(prompt, choices)
	return result.Value, err
}

func (a *App) selectOneOutcome(prompt string, choices []selectChoice) (selectOutcome, error) {
	if len(choices) == 0 {
		return selectOutcome{}, unavailable("no selectable items")
	}
	if a.useBubbleSelector() {
		return a.selectOneBubbleOutcome(prompt, choices)
	}
	value, err := selectOnePlain(a.in, a.err, prompt, choices)
	return selectOutcome{Value: value}, err
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
	noColor                                                                            bool
}

func newSelectorStyles(noColor, dark bool) selectorStyles {
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
		noColor:  noColor,
	}
	if noColor {
		return styles
	}
	accent := lipgloss.Color("#5B50D6")
	selectedBackground := lipgloss.Color("#E9E7FF")
	muted := lipgloss.Color("#626262")
	border := lipgloss.Color("#C9C7D8")
	if dark {
		accent = lipgloss.Color("#A78BFA")
		selectedBackground = lipgloss.Color("#2D2842")
		muted = lipgloss.Color("#9A9A9A")
		border = lipgloss.Color("#4C465F")
	}
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
	prompt      string
	choices     []bubbleChoice
	matches     []selectorMatch
	input       textinput.Model
	styles      selectorStyles
	width       int
	height      int
	cursor      int
	offset      int
	selected    string
	cancelled   bool
	interrupted bool
	noColor     bool
	// embedded marks a selector that is one level of a staged walk. It records
	// its outcome and lets the owning model decide whether the program ends,
	// so entering and leaving a level never restarts the alternate screen.
	embedded bool
	// readOnly marks rows that are shown for reading rather than choosing.
	// Enter does nothing and the footer offers only navigation, so a viewer
	// cannot be closed by the key used to drill into it.
	readOnly bool
	// title replaces the default "Select <prompt>" heading.
	title string
}

// finish ends the program for a standalone selector and yields control to the
// owning staged model for an embedded one.
func (m bubbleSelectorModel) finish() tea.Cmd {
	if m.embedded {
		return nil
	}
	return tea.Quit
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
	if noColor {
		query.SetStyles(textinput.Styles{})
		query.SetVirtualCursor(false)
	}
	model := bubbleSelectorModel{
		prompt:  safeTerminalText(prompt),
		choices: items,
		input:   query,
		styles:  newSelectorStyles(noColor, true),
		width:   80,
		height:  20,
		noColor: noColor,
	}
	model.refreshMatches()
	return model
}

func (m bubbleSelectorModel) Init() tea.Cmd {
	if m.noColor {
		return textinput.Blink
	}
	return tea.Batch(textinput.Blink, tea.RequestBackgroundColor)
}

func (m bubbleSelectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(1, msg.Width)
		m.height = max(1, msg.Height)
		m.ensureVisible()
		return m, nil
	case tea.BackgroundColorMsg:
		m.styles = newSelectorStyles(m.noColor, msg.IsDark())
	case tea.KeyPressMsg:
		switch msg.String() {
		case "/":
			if m.input.Value() == "" {
				return m, nil
			}
		case "enter":
			if m.readOnly {
				return m, nil
			}
			if len(m.matches) > 0 {
				m.selected = m.matches[m.cursor].choice.value
				return m, m.finish()
			}
			return m, nil
		case "ctrl+c":
			m.cancelled = true
			m.interrupted = true
			return m, m.finish()
		case "esc":
			if m.input.Value() != "" {
				m.input.SetValue("")
				m.refreshMatches()
				return m, nil
			}
			m.cancelled = true
			return m, m.finish()
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

func (m bubbleSelectorModel) View() tea.View {
	boxWidth := max(1, min(m.width, 96))
	bordered := boxWidth >= 50 && m.height >= 10
	innerWidth := boxWidth
	if bordered {
		innerWidth -= 4
	} else {
		innerWidth = max(1, innerWidth-2)
	}

	title := m.title
	if title == "" {
		title = "Select " + m.prompt
	}
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
	inputWidth := max(1, innerWidth-lipgloss.Width("Search  "))
	input.SetWidth(inputWidth)
	input.Placeholder = ansi.Truncate(input.Placeholder, inputWidth, "")
	if !m.noColor {
		inputStyles := input.Styles()
		inputStyles.Focused.Text = m.styles.query
		inputStyles.Focused.Placeholder = m.styles.detail
		inputStyles.Cursor.Color = m.styles.prompt.GetForeground()
		input.SetStyles(inputStyles)
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

	// A read-only level offers no selection, so the footer omits it rather than
	// showing a key that does nothing.
	footer := "↑↓ move  enter select  esc clear/cancel"
	switch {
	case m.readOnly && innerWidth >= 64:
		footer = "↑↓/ctrl+n,p move  esc clear/back  ctrl+c quit"
	case m.readOnly:
		footer = "↑↓ move  esc clear/back"
	case innerWidth >= 64:
		footer = "↑↓/ctrl+n,p move  enter select  esc clear/cancel  ctrl+c quit"
	case innerWidth < 40:
		footer = "↑↓ move  enter select\nesc clear/cancel"
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
		box := m.styles.box.Border(lipgloss.RoundedBorder()).Padding(0, 1).Width(innerWidth + 4).Render(content)
		view := tea.NewView(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box))
		view.AltScreen = true
		return view
	}
	box := lipgloss.NewStyle().Padding(0, 1).Width(innerWidth + 2).Render(content)
	view := tea.NewView(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box))
	view.AltScreen = true
	return view
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

// selectStage is one level of a multi-level selection.
type selectStage struct {
	Prompt  string
	Choices []selectChoice
	// Title replaces the default "Select <Prompt>" heading.
	Title string
	// ReadOnly marks a level whose rows are read rather than chosen. Enter does
	// nothing; Escape is the only way out. A walk cannot complete on such a
	// level, so it is always a leaf.
	ReadOnly bool
}

// stageOutcome reports the values chosen at each level, outermost first. Path
// is empty when the walk was cancelled.
type stageOutcome struct {
	Path        []string
	Cancelled   bool
	Interrupted bool
}

// selectStages walks a multi-level selection. next receives the values chosen
// so far and returns the level that follows, or nil when the path is complete.
// A level with no choices is treated as complete rather than shown empty.
//
// Real terminals run the whole walk inside one Bubble Tea program: entering a
// value pushes a level and Escape pops back to the previous one with its query
// and cursor intact. Pipes, tests, and dumb terminals walk the same stage graph
// with the line-oriented selector, where an empty answer pops instead.
func (a *App) selectStages(root selectStage, next func(path []string) *selectStage) (stageOutcome, error) {
	if len(root.Choices) == 0 {
		return stageOutcome{}, unavailable("no selectable items")
	}
	if a.useBubbleSelector() {
		return a.selectStagesBubble(root, next)
	}
	return a.selectStagesPlain(root, next)
}

func (a *App) selectStagesPlain(root selectStage, next func(path []string) *selectStage) (stageOutcome, error) {
	stages := []selectStage{root}
	var path []string
	pop := func() {
		stages = stages[:len(stages)-1]
		path = path[:len(path)-1]
	}
	for {
		current := stages[len(stages)-1]
		// A read-only level has nothing to answer: show it, then step back on
		// whatever the reader types.
		if current.ReadOnly {
			if err := showStagePlain(a.in, a.err, current); err != nil {
				return stageOutcome{}, err
			}
			if len(stages) == 1 {
				return stageOutcome{Cancelled: true}, nil
			}
			pop()
			continue
		}
		value, err := selectOnePlain(a.in, a.err, current.Prompt, current.Choices)
		if err != nil {
			return stageOutcome{}, err
		}
		if value == "" {
			if len(stages) == 1 {
				return stageOutcome{Cancelled: true}, nil
			}
			pop()
			continue
		}
		path = append(path, value)
		following := next(clonePath(path))
		if following == nil || len(following.Choices) == 0 {
			return stageOutcome{Path: clonePath(path)}, nil
		}
		stages = append(stages, *following)
	}
}

func clonePath(path []string) []string {
	return append([]string(nil), path...)
}

// showStagePlain renders a read-only level for the line-oriented walk and waits
// for one line before stepping back.
func showStagePlain(in io.Reader, out io.Writer, stage selectStage) error {
	heading := stage.Title
	if heading == "" {
		heading = stage.Prompt
	}
	if _, err := fmt.Fprintf(out, "%s\n", safeTerminalText(heading)); err != nil {
		return err
	}
	for _, choice := range stage.Choices {
		line := "  " + safeTerminalText(choice.Label)
		if choice.Description != "" {
			line += "  " + safeTerminalText(choice.Description)
		}
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprint(out, "[Enter=back]: "); err != nil {
		return err
	}
	// Exhausted or closed input steps back rather than failing: there is no
	// answer to lose on a level that only displays.
	_, _ = readLine(in)
	return nil
}

// stagedSelectorModel owns a stack of ordinary selectors and switches between
// them in place, so the alternate screen is entered once for the whole walk.
type stagedSelectorModel struct {
	stack   []bubbleSelectorModel
	path    []string
	next    func(path []string) *selectStage
	noColor bool
	width   int
	height  int
	outcome stageOutcome
}

func newStageLevel(stage selectStage, noColor bool) bubbleSelectorModel {
	level := newBubbleSelectorModelWithColor(stage.Prompt, stage.Choices, noColor)
	level.embedded = true
	level.readOnly = stage.ReadOnly
	level.title = safeTerminalText(stage.Title)
	return level
}

func newStagedSelectorModel(root selectStage, next func(path []string) *selectStage, noColor bool) stagedSelectorModel {
	first := newStageLevel(root, noColor)
	return stagedSelectorModel{
		stack:   []bubbleSelectorModel{first},
		next:    next,
		noColor: noColor,
		width:   first.width,
		height:  first.height,
	}
}

func (m stagedSelectorModel) Init() tea.Cmd {
	if m.noColor {
		return textinput.Blink
	}
	return tea.Batch(textinput.Blink, tea.RequestBackgroundColor)
}

func (m stagedSelectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Every level keeps its own cursor and offset, so a resize has to reach the
	// levels that are not currently visible as well.
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = max(1, size.Width)
		m.height = max(1, size.Height)
		for i := range m.stack {
			resized, _ := m.stack[i].Update(msg)
			m.stack[i] = resized.(bubbleSelectorModel)
		}
		return m, nil
	}
	if background, ok := msg.(tea.BackgroundColorMsg); ok {
		for i := range m.stack {
			updated, _ := m.stack[i].Update(background)
			m.stack[i] = updated.(bubbleSelectorModel)
		}
		return m, nil
	}

	top := len(m.stack) - 1
	updated, cmd := m.stack[top].Update(msg)
	current := updated.(bubbleSelectorModel)
	m.stack[top] = current

	switch {
	case current.interrupted:
		m.outcome = stageOutcome{Cancelled: true, Interrupted: true}
		return m, tea.Quit
	case current.cancelled:
		if top == 0 {
			m.outcome = stageOutcome{Cancelled: true}
			return m, tea.Quit
		}
		m.stack = m.stack[:top]
		m.path = m.path[:len(m.path)-1]
		return m, textinput.Blink
	case current.selected != "":
		m.path = append(m.path, current.selected)
		// Clear it so returning to this level later does not re-enter the child.
		m.stack[top].selected = ""
		following := m.next(clonePath(m.path))
		if following == nil || len(following.Choices) == 0 {
			m.outcome = stageOutcome{Path: clonePath(m.path)}
			return m, tea.Quit
		}
		child := newStageLevel(*following, m.noColor)
		child.width = m.width
		child.height = m.height
		child.ensureVisible()
		m.stack = append(m.stack, child)
		return m, textinput.Blink
	}
	return m, cmd
}

func (m stagedSelectorModel) View() tea.View {
	return m.stack[len(m.stack)-1].View()
}

func (a *App) selectStagesBubble(root selectStage, next func(path []string) *selectStage) (stageOutcome, error) {
	program := tea.NewProgram(
		newStagedSelectorModel(root, next, a.getenv("NO_COLOR") != ""),
		tea.WithInput(a.in),
		tea.WithOutput(a.err),
	)
	result, err := program.Run()
	if err != nil {
		return stageOutcome{}, fmt.Errorf("run selector: %w", err)
	}
	model, ok := result.(stagedSelectorModel)
	if !ok {
		return stageOutcome{}, fmt.Errorf("selector returned an unexpected model")
	}
	return model.outcome, nil
}

func (a *App) selectOneBubbleOutcome(prompt string, choices []selectChoice) (selectOutcome, error) {
	program := tea.NewProgram(
		newBubbleSelectorModelWithColor(prompt, choices, a.getenv("NO_COLOR") != ""),
		tea.WithInput(a.in),
		tea.WithOutput(a.err),
	)
	result, err := program.Run()
	if err != nil {
		return selectOutcome{}, fmt.Errorf("run selector: %w", err)
	}
	model, ok := result.(bubbleSelectorModel)
	if !ok {
		return selectOutcome{}, fmt.Errorf("selector returned an unexpected model")
	}
	if model.cancelled {
		return selectOutcome{Interrupted: model.interrupted}, nil
	}
	return selectOutcome{Value: model.selected}, nil
}
