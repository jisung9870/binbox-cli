package awsbrowser

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

type Config struct {
	Profile  string
	Region   string
	Selector string
	NoColor  bool
}

type catalogItem struct{ ID, Label, Status string }

var homeCatalog = []catalogItem{
	{ID: "ec2-instances", Label: "EC2 Instances", Status: "Not loaded"},
	{ID: "route53-hosted-zones", Label: "Route 53 Hosted Zones", Status: "Not loaded · AWS global"},
	{ID: "iam-roles", Label: "IAM Roles", Status: "Not loaded · AWS global"},
	{ID: "vpc-networking", Label: "VPC & Networking", Status: "Not loaded"},
	{ID: "cross-profile-search", Label: "Cross-profile search", Status: "Domain, role · scope on open"},
}

type routeMode uint8

const (
	routeList routeMode = iota
	routeDetail
	routeSearch
)

var searchKinds = []string{"domain", "role", "ec2-instances"}
var searchScopes = []string{"all", "current"}

type routeFrame struct {
	mode             routeMode
	target, label    string
	status           string
	projection       IntentProjection
	selected         int
	context          *AWSContext
	stream           IntentStream
	generation       uint64
	refreshing       bool
	detail           ResourceProjection
	relationSelected int
	scroll           int
	terminalUpdate   bool
	dispatchCancel   context.CancelFunc
	coverage         *SearchCoverage
	searchKind       int
	searchScope      int
	searchValue      string
	searchFocus      int
}

type Model struct {
	ctx                     context.Context
	config                  Config
	dispatcher              IntentDispatcher
	width, height, selected int
	help                    bool
	history                 []routeFrame
	nextGeneration          uint64
}

type intentStartedMsg struct {
	generation uint64
	result     IntentResultMsg
}

type intentStreamMsg struct {
	generation uint64
	update     IntentUpdate
	open       bool
}

func NewModel(ctx context.Context, config Config, dispatcher IntentDispatcher) Model {
	if ctx == nil {
		ctx = context.Background()
	}
	return Model{ctx: ctx, config: config, dispatcher: dispatcher, width: 80, height: 24}
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
		return m, nil
	case intentStartedMsg:
		frame := m.current()
		if frame == nil || frame.generation != msg.generation {
			if msg.result.Stream != nil {
				msg.result.Stream.Cancel()
			}
			return m, nil
		}
		if msg.result.Err != nil {
			m.finishFrame(frame)
			frame.status = "! " + msg.result.Error()
			frame.refreshing = false
			return m, nil
		}
		if msg.result.Stream == nil || msg.result.Stream.Updates() == nil {
			m.finishFrame(frame)
			frame.status = "! " + safeIntentText(msg.result.Intent.Target+": no update stream")
			frame.refreshing = false
			return m, nil
		}
		frame.stream = msg.result.Stream
		return m, waitIntent(msg.generation, msg.result.Stream)
	case intentStreamMsg:
		frame := m.current()
		if frame == nil || frame.generation != msg.generation {
			return m, nil
		}
		if !msg.open {
			m.finishFrame(frame)
			frame.refreshing = false
			if !frame.terminalUpdate {
				frame.status = queryStatus(QueryUpdate{Snapshot: QuerySnapshot{State: LoadUnknown}}, len(frame.projection.Resources)) + " · incomplete stream"
			}
			return m, nil
		}
		if frame.terminalUpdate {
			return m, nil
		}
		m.applyIntentUpdate(frame, msg.update)
		frame.terminalUpdate = frame.terminalUpdate || terminalLoadState(msg.update.Query.Snapshot.State)
		if frame.terminalUpdate {
			m.finishFrame(frame)
			frame.refreshing = false
			return m, nil
		}
		if msg.update.Done && !frame.terminalUpdate {
			frame.status = queryStatus(QueryUpdate{Snapshot: QuerySnapshot{State: LoadUnknown}}, len(frame.projection.Resources)) + " · incomplete stream"
			m.finishFrame(frame)
			frame.refreshing = false
			return m, nil
		}
		return m, waitIntent(msg.generation, frame.stream)
	case IntentResultMsg:
		// Kept as a public message for small external adapters. Model-originated
		// dispatches use intentStartedMsg so stale starts remain fenced.
		frame := m.current()
		if frame != nil && msg.Err != nil {
			frame.status = "! " + msg.Error()
		}
		return m, nil
	case tea.KeyPressMsg:
		return m.updateKey(msg.String())
	}
	return m, nil
}

func (m Model) updateKey(key string) (tea.Model, tea.Cmd) {
	frame := m.current()
	searchValueFocused := frame != nil && frame.mode == routeSearch && frame.searchFocus == 1
	if key == "q" && !searchValueFocused {
		m.cancelAll()
		return m, tea.Quit
	}
	if m.help {
		if key == "?" || key == "esc" {
			m.help = false
		}
		if key == "ctrl+c" {
			m.cancelAll()
			return m, tea.Quit
		}
		return m, nil
	}
	if frame != nil && frame.mode == routeSearch {
		return m.updateSearchKey(key)
	}
	switch key {
	case "ctrl+c":
		m.cancelAll()
		return m, tea.Quit
	case "?":
		m.help = true
	case "esc", "backspace":
		if frame == nil {
			return m, tea.Quit
		}
		m.pop()
	case "ctrl+g":
		return m.openSearch()
	case "ctrl+r":
		if frame != nil && frame.mode == routeList {
			return m.refreshCurrent()
		}
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup":
		if frame != nil && frame.mode == routeDetail {
			frame.scroll = max(0, frame.scroll-max(1, m.height/2))
		} else {
			m.moveTo(false)
		}
	case "pgdown":
		if frame != nil && frame.mode == routeDetail {
			frame.scroll += max(1, m.height/2)
		} else {
			m.moveTo(true)
		}
	case "home":
		m.moveTo(false)
	case "end":
		m.moveTo(true)
	case "enter":
		if frame == nil {
			return m.openCatalog(m.selected)
		}
		return m.enterCurrent()
	}
	return m, nil
}

func (m Model) openCatalog(index int) (tea.Model, tea.Cmd) {
	item := homeCatalog[index]
	if item.ID == "cross-profile-search" {
		return m.openSearch()
	}
	return m.pushAndDispatch(Intent{Kind: IntentOpen, Target: item.ID}, item.Label, nil)
}

func (m Model) openSearch() (tea.Model, tea.Cmd) {
	if current := m.current(); current != nil {
		m.finishFrame(current)
	}
	m.history = append(m.history, routeFrame{mode: routeSearch, target: "cross-profile-search", label: "Cross-profile search", searchFocus: 1})
	return m, nil
}

func (m Model) updateSearchKey(key string) (tea.Model, tea.Cmd) {
	frame := m.current()
	switch key {
	case "ctrl+c":
		m.cancelAll()
		return m, tea.Quit
	case "esc":
		m.pop()
	case "tab", "shift+tab":
		delta := 1
		if key == "shift+tab" {
			delta = -1
		}
		frame.searchFocus = (frame.searchFocus + delta + 3) % 3
	case "left":
		m.adjustSearchChoice(frame, -1)
	case "right":
		m.adjustSearchChoice(frame, 1)
	case "backspace":
		if frame.searchFocus == 1 && frame.searchValue != "" {
			runes := []rune(frame.searchValue)
			frame.searchValue = string(runes[:len(runes)-1])
		}
	case "enter":
		query := strings.TrimSpace(frame.searchValue)
		if query == "" {
			frame.status = "Enter a search value before submitting."
			return m, nil
		}
		kind, scope := searchKinds[frame.searchKind], searchScopes[frame.searchScope]
		m.pop()
		return m.pushAndDispatch(Intent{
			Kind: IntentSearch, Target: "cross-profile-search", SearchKind: kind,
			Query: query, Scope: scope,
		}, "Search results · "+query, nil)
	default:
		if frame.searchFocus == 1 && len([]rune(key)) == 1 {
			frame.searchValue += key
			frame.status = ""
		}
	}
	return m, nil
}

func (m *Model) adjustSearchChoice(frame *routeFrame, delta int) {
	if frame.searchFocus == 0 {
		frame.searchKind = (frame.searchKind + delta + len(searchKinds)) % len(searchKinds)
		if searchKinds[frame.searchKind] == "ec2-instances" {
			frame.searchScope = 1
		}
	} else if frame.searchFocus == 2 {
		if searchKinds[frame.searchKind] == "ec2-instances" {
			frame.searchScope = 1
			frame.status = "EC2 instance search uses the current profile."
			return
		}
		frame.searchScope = (frame.searchScope + delta + len(searchScopes)) % len(searchScopes)
	}
}

func (m Model) enterCurrent() (tea.Model, tea.Cmd) {
	frame := m.current()
	if frame.mode == routeList {
		if len(frame.projection.Resources) == 0 {
			return m, nil
		}
		if frame.stream != nil {
			frame.stream.Cancel()
			frame.stream = nil
		}
		resource := frame.projection.Resources[frame.selected]
		resourceContext := frame.context
		if resource.Context != nil && resource.Context.Validate() == nil {
			copy := *resource.Context
			resourceContext = &copy
		}
		m.history = append(m.history, routeFrame{mode: routeDetail, target: resource.Target, label: resource.Title, context: resourceContext, detail: resource})
		return m, nil
	}
	if len(frame.detail.Relations) == 0 {
		frame.status = "No navigable relations for this resource."
		return m, nil
	}
	relation := frame.detail.Relations[frame.relationSelected]
	if relation.Target == "" {
		frame.status = "Relation is evidence-only: " + safeIntentText(relation.Reason)
		return m, nil
	}
	return m.pushAndDispatch(Intent{Kind: IntentOpen, Target: relation.Target}, relation.Label, frame.context)
}

func (m Model) pushAndDispatch(intent Intent, label string, inherited *AWSContext) (tea.Model, tea.Cmd) {
	if current := m.current(); current != nil {
		m.finishFrame(current)
	}
	m.nextGeneration++
	if inherited != nil && inherited.Validate() == nil {
		intent.Profile, intent.Region = inherited.Profile, inherited.Region
	}
	dispatchCtx, cancel := context.WithCancel(m.ctx)
	frame := routeFrame{
		mode: routeList, target: intent.Target, label: label, context: inherited,
		status: "Loading " + safeIntentText(label) + "… · Esc cancel", generation: m.nextGeneration, dispatchCancel: cancel,
	}
	m.history = append(m.history, frame)
	return m, m.dispatch(dispatchCtx, intent, m.nextGeneration)
}

func (m Model) refreshCurrent() (tea.Model, tea.Cmd) {
	frame := m.current()
	m.finishFrame(frame)
	m.nextGeneration++
	dispatchCtx, cancel := context.WithCancel(m.ctx)
	frame.generation = m.nextGeneration
	frame.dispatchCancel = cancel
	frame.refreshing = true
	frame.terminalUpdate = false
	frame.status = fmt.Sprintf("Showing cached %d · refreshing… · Esc cancel", len(frame.projection.Resources))
	intent := Intent{Kind: IntentRefresh, Target: frame.target}
	if frame.context != nil && frame.context.Validate() == nil {
		intent.Profile, intent.Region = frame.context.Profile, frame.context.Region
	}
	return m, m.dispatch(dispatchCtx, intent, frame.generation)
}

func (m Model) dispatch(ctx context.Context, intent Intent, generation uint64) tea.Cmd {
	if m.dispatcher == nil {
		return nil
	}
	if intent.Profile == "" && intent.Region == "" {
		intent.Profile, intent.Region = m.config.Profile, m.config.Region
	} else if intent.Region == "" {
		intent.Region = m.config.Region
	}
	return func() tea.Msg {
		stream, err := m.dispatcher.Dispatch(ctx, intent)
		return intentStartedMsg{generation: generation, result: IntentResultMsg{Intent: intent, Stream: stream, Err: err}}
	}
}

func waitIntent(generation uint64, stream IntentStream) tea.Cmd {
	return func() tea.Msg {
		update, ok := <-stream.Updates()
		return intentStreamMsg{generation: generation, update: update, open: ok}
	}
}

func (m *Model) applyIntentUpdate(frame *routeFrame, update IntentUpdate) {
	if update.Context != nil && update.Context.Validate() == nil {
		copy := *update.Context
		frame.context = &copy
	} else if update.Query.Key.Context.Validate() == nil {
		copy := update.Query.Key.Context
		frame.context = &copy
	}
	projection := update.Projection
	if update.Coverage != nil {
		frame.coverage = cloneSearchCoverage(update.Coverage)
	}
	if len(projection.Resources) == 0 && update.Query.Snapshot.ResourceCount() != 0 {
		projection = ProjectQueryUpdate(update.Query)
	}
	state := update.Query.Snapshot.State
	preserve := frame.refreshing && state == LoadRefreshing && len(frame.projection.Resources) != 0
	if !preserve && (len(projection.Resources) != 0 || state == LoadReady || state == LoadEmpty) {
		frame.projection = projection
		if frame.selected >= len(frame.projection.Resources) {
			frame.selected = max(0, len(frame.projection.Resources)-1)
		}
	}
	frame.status = queryStatus(update.Query, len(frame.projection.Resources))
	if frame.coverage != nil {
		frame.status = searchCoverageStatus(frame.coverage, len(frame.projection.Resources), state)
	}
	if state != LoadRefreshing && state != LoadLoading && state != LoadQueued {
		frame.refreshing = false
	}
}

func cloneSearchCoverage(coverage *SearchCoverage) *SearchCoverage {
	if coverage == nil {
		return nil
	}
	copy := *coverage
	copy.Profiles = append([]SearchProfileCoverage(nil), coverage.Profiles...)
	return &copy
}

func searchCoverageStatus(coverage *SearchCoverage, count int, state LoadState) string {
	searched, matched := 0, 0
	for _, profile := range coverage.Profiles {
		if profile.Status != "not_searched" {
			searched++
		}
		if profile.Status == "matched" {
			matched++
		}
	}
	prefix := "Ready"
	if state != LoadReady && state != LoadEmpty {
		prefix = "! " + loadStateLabel(state)
	}
	status := fmt.Sprintf("%s · %d resources · searched %d/%d · matched %d", prefix, count, searched, len(coverage.Profiles), matched)
	if coverage.Partial {
		status += " · Partial coverage"
	}
	return status
}

func queryStatus(update QueryUpdate, count int) string {
	snapshot := update.Snapshot
	switch snapshot.State {
	case LoadNotLoaded:
		return "Not loaded"
	case LoadQueued:
		return "Queued… · Esc cancel"
	case LoadLoading:
		if count != 0 {
			return fmt.Sprintf("Loaded %d · loading more… · Esc cancel", count)
		}
		return "Loading… · Esc cancel"
	case LoadRefreshing:
		return fmt.Sprintf("Showing cached %d · refreshing… · Esc cancel", count)
	case LoadReady:
		return fmt.Sprintf("Ready · %d resources · fetched %s", count, displayTime(snapshot.FetchedAt))
	case LoadEmpty:
		return "Ready · 0 resources"
	case LoadStale:
		status := fmt.Sprintf("Stale · showing cached %d", count)
		if snapshot.RefreshFailure != nil {
			status += " · refresh " + loadStateLabel(snapshot.RefreshFailure.State) + " at " + displayTime(snapshot.RefreshFailure.ObservedAt)
		}
		return status
	default:
		status := "! " + loadStateLabel(snapshot.State)
		if update.Failure != nil {
			if update.Failure.Service != "" || update.Failure.Operation != "" {
				status += " · " + safeIntentText(stringsJoinNonEmpty(update.Failure.Service, update.Failure.Operation))
			}
			if update.Failure.PartialPages != 0 {
				status += fmt.Sprintf(" · %d complete pages kept", update.Failure.PartialPages)
			}
		}
		return status
	}
}

func stringsJoinNonEmpty(values ...string) string {
	result := ""
	for _, value := range values {
		if value == "" {
			continue
		}
		if result != "" {
			result += ":"
		}
		result += value
	}
	return result
}

func loadStateLabel(state LoadState) string {
	switch state {
	case LoadForbidden:
		return "access denied"
	case LoadAuthRequired:
		return "login required"
	case LoadThrottled:
		return "throttled"
	case LoadTimedOut:
		return "timed out"
	case LoadCancelled:
		return "cancelled"
	case LoadUnsupported:
		return "unsupported"
	default:
		return "query failed"
	}
}

func displayTime(value time.Time) string {
	if value.IsZero() {
		return "unknown time"
	}
	return value.Local().Format("15:04:05")
}

func (m *Model) move(delta int) {
	frame := m.current()
	if frame == nil {
		m.selected = clamp(m.selected+delta, 0, len(homeCatalog)-1)
		return
	}
	if frame.mode == routeList {
		frame.selected = clamp(frame.selected+delta, 0, max(0, len(frame.projection.Resources)-1))
	} else {
		frame.relationSelected = clamp(frame.relationSelected+delta, 0, max(0, len(frame.detail.Relations)-1))
	}
}

func (m *Model) moveTo(end bool) {
	frame := m.current()
	if frame == nil {
		m.selected = 0
		if end {
			m.selected = len(homeCatalog) - 1
		}
		return
	}
	if frame.mode == routeList {
		frame.selected = 0
		if end && len(frame.projection.Resources) != 0 {
			frame.selected = len(frame.projection.Resources) - 1
		}
	} else {
		frame.relationSelected = 0
		if end && len(frame.detail.Relations) != 0 {
			frame.relationSelected = len(frame.detail.Relations) - 1
		}
	}
}

func (m *Model) current() *routeFrame {
	if len(m.history) == 0 {
		return nil
	}
	return &m.history[len(m.history)-1]
}

func (m *Model) pop() {
	frame := m.current()
	if frame != nil {
		m.finishFrame(frame)
	}
	if len(m.history) != 0 {
		m.history = m.history[:len(m.history)-1]
	}
}

func (m *Model) cancelAll() {
	for index := range m.history {
		m.finishFrame(&m.history[index])
	}
}

func (m *Model) finishFrame(frame *routeFrame) {
	if frame.dispatchCancel != nil {
		frame.dispatchCancel()
		frame.dispatchCancel = nil
	}
	if frame.stream != nil {
		frame.stream.Cancel()
		frame.stream = nil
	}
}

func terminalLoadState(state LoadState) bool {
	switch state {
	case LoadReady, LoadEmpty, LoadStale, LoadForbidden, LoadAuthRequired, LoadThrottled, LoadTimedOut, LoadCancelled, LoadUnsupported, LoadUnknown:
		return true
	default:
		return false
	}
}

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func (m Model) View() tea.View {
	view := tea.NewView(renderModel(m))
	view.AltScreen = true
	return view
}
