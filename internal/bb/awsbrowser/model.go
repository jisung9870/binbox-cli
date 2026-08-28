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
	Regions  string
	Selector string
	NoColor  bool
}

type catalogItem struct{ ID, Label, Status string }

var homeCatalog = []catalogItem{
	{ID: "ec2-instances", Label: "EC2 Instances", Status: "Not loaded"},
	{ID: "route53-hosted-zones", Label: "Route 53 Hosted Zones", Status: "Not loaded · AWS global"},
	{ID: "iam-roles", Label: "IAM Roles", Status: "Not loaded · AWS global"},
	{ID: "vpc-networking", Label: "VPC & Networking", Status: "Not loaded"},
	{ID: "elbv2-load-balancers", Label: "Load Balancers (ALB/NLB)", Status: "Not loaded"},
	{ID: "cross-profile-search", Label: "Cross-profile search", Status: "Domain, role · scope on open"},
}

type routeMode uint8

const (
	routeList routeMode = iota
	routeDetail
	routeFields
	routeRelations
	routeTags
	routeSearch
	routeContext
)

var searchKinds = []string{"domain", "role", "ec2-instances"}
var searchScopes = []string{"all", "current"}

type routeFrame struct {
	mode              routeMode
	target, label     string
	intent            Intent
	status            string
	projection        IntentProjection
	selected          int
	context           *AWSContext
	stream            IntentStream
	generation        uint64
	refreshing        bool
	detail            ResourceProjection
	relationSelected  int
	relationGroup     string
	scroll            int
	terminalUpdate    bool
	dispatchCancel    context.CancelFunc
	coverage          *SearchCoverage
	graph             *GraphSnapshot
	staged            refreshStage
	searchKind        int
	searchScope       int
	searchValue       string
	searchFocus       int
	filterValue       string
	filterActive      bool
	contextChoices    []ContextChoice
	contextSelected   int
	contextQuery      string
	contextRegion     string
	contextRegions    []string
	contextAllRegions bool
	contextFocus      int
	contextLoading    bool
	contextStartup    bool
	verifiedContext   *AWSContext
}

type Model struct {
	ctx                     context.Context
	config                  Config
	dispatcher              IntentDispatcher
	startupContext          context.Context
	width, height, selected int
	help                    bool
	dark                    bool
	history                 []routeFrame
	forwardHistory          []routeFrame
	commandActive           bool
	commandValue            string
	commandStatus           string
	nextGeneration          uint64
	activeContext           *AWSContext
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

type contextChoicesMsg struct {
	generation uint64
	choices    []ContextChoice
	err        error
}

type contextResolvedMsg struct {
	generation uint64
	resolution ContextResolution
	err        error
}

func NewModel(ctx context.Context, config Config, dispatcher IntentDispatcher) Model {
	if ctx == nil {
		ctx = context.Background()
	}
	m := Model{ctx: ctx, config: config, dispatcher: dispatcher, width: 80, height: 24, dark: true}
	if catalog, ok := dispatcher.(ContextCatalog); config.Profile == "" && ok && catalog != nil {
		m.nextGeneration++
		requestCtx, cancel := context.WithCancel(ctx)
		m.startupContext = requestCtx
		m.history = append(m.history, routeFrame{
			mode: routeContext, label: "Select AWS context", status: "Loading configured profiles…",
			generation: m.nextGeneration, dispatchCancel: cancel, contextLoading: true, contextStartup: true,
		})
	}
	return m
}

func (m Model) Init() tea.Cmd {
	commands := make([]tea.Cmd, 0, 2)
	if !m.config.NoColor {
		commands = append(commands, tea.RequestBackgroundColor)
	}
	if frame := m.current(); frame != nil && frame.contextStartup && frame.contextLoading && m.startupContext != nil {
		commands = append(commands, m.listContexts(m.startupContext, frame.generation))
	}
	switch len(commands) {
	case 0:
		return nil
	case 1:
		return commands[0]
	default:
		return tea.Batch(commands...)
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
		return m, nil
	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
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
			if frame.refreshing {
				m.finalizeRefresh(frame, "failed · "+msg.result.Error())
			} else {
				m.finishFrame(frame)
				frame.status = "! " + msg.result.Error()
			}
			return m, nil
		}
		if msg.result.Stream == nil || msg.result.Stream.Updates() == nil {
			if frame.refreshing {
				m.finalizeRefresh(frame, "failed · "+msg.result.Intent.Target+": no update stream")
			} else {
				m.finishFrame(frame)
				frame.status = "! " + safeIntentText(msg.result.Intent.Target+": no update stream")
			}
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
			if !frame.terminalUpdate {
				if frame.refreshing {
					m.finalizeRefresh(frame, "query failed · incomplete stream")
				} else {
					m.finishFrame(frame)
					frame.status = queryStatus(QueryUpdate{Snapshot: QuerySnapshot{State: LoadUnknown}}, len(frame.projection.Resources)) + " · incomplete stream"
				}
			} else {
				m.finishFrame(frame)
			}
			return m, nil
		}
		if frame.terminalUpdate {
			return m, nil
		}
		m.applyIntentUpdate(frame, msg.update)
		frame.terminalUpdate = frame.terminalUpdate || terminalLoadState(msg.update.Query.Snapshot.State)
		if frame.terminalUpdate {
			if frame.refreshing {
				m.finalizeRefresh(frame, "")
			} else {
				m.finishFrame(frame)
				m.promoteSingletonSummary(frame)
			}
			return m, nil
		}
		if msg.update.Done && !frame.terminalUpdate {
			if frame.refreshing {
				m.finalizeRefresh(frame, "query failed · incomplete stream")
			} else {
				frame.status = queryStatus(QueryUpdate{Snapshot: QuerySnapshot{State: LoadUnknown}}, len(frame.projection.Resources)) + " · incomplete stream"
				m.finishFrame(frame)
			}
			return m, nil
		}
		return m, waitIntent(msg.generation, frame.stream)
	case contextChoicesMsg:
		frame := m.current()
		if frame == nil || frame.mode != routeContext || frame.generation != msg.generation {
			return m, nil
		}
		m.finishContextRequest(frame)
		if msg.err != nil {
			frame.status = "! configured profiles unavailable"
			return m, nil
		}
		frame.contextChoices = append([]ContextChoice(nil), msg.choices...)
		if len(frame.contextChoices) == 0 {
			frame.status = "No configured AWS profiles were found."
			return m, nil
		}
		frame.contextSelected = 0
		for index, choice := range frame.contextChoices {
			if choice.Profile == m.config.Profile {
				frame.contextSelected = index
				break
			}
		}
		m.resetContextChoice(frame)
		if m.config.Region != "" && (m.config.Profile == "" || frame.contextChoices[frame.contextSelected].Profile == m.config.Profile) {
			frame.contextRegion = m.config.Region
			frame.contextRegions = currentFirst(frame.contextRegions, m.config.Region)
		}
		frame.contextAllRegions = m.config.Regions != "" && len(frame.contextRegions) > 1
		frame.status = "Choose a profile and region, then verify its account."
		return m, nil
	case contextResolvedMsg:
		frame := m.current()
		if frame == nil || frame.mode != routeContext || frame.generation != msg.generation {
			return m, nil
		}
		m.finishContextRequest(frame)
		if msg.err != nil {
			frame.status = "! context verification failed"
			return m, nil
		}
		if msg.resolution.Failure != nil {
			frame.status = "! " + loadStateLabel(msg.resolution.Failure.State)
			return m, nil
		}
		if msg.resolution.Context == nil || msg.resolution.Context.Validate() != nil {
			frame.status = "! context verification failed"
			return m, nil
		}
		verified := *msg.resolution.Context
		frame.verifiedContext = &verified
		frame.status = "Verified account " + safeIntentText(verified.AccountID) + " · enter apply"
		return m, nil
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
	if m.commandActive {
		return m.updateCommandKey(key)
	}
	if key == ":" {
		m.commandActive = true
		m.commandValue = ""
		m.commandStatus = ""
		return m, nil
	}
	frame := m.current()
	textValueFocused := frame != nil && (frame.mode == routeSearch && frame.searchFocus == 1 ||
		frame.mode == routeContext || frame.mode == routeList || frame.mode == routeRelations || frame.mode == routeTags)
	if key == "q" && !textValueFocused {
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
	if frame != nil && frame.mode == routeContext {
		return m.updateContextKey(key)
	}
	switch key {
	case "ctrl+c":
		m.cancelAll()
		return m, tea.Quit
	case "?":
		if frame == nil || !filterableRoute(frame.mode) || !frame.filterActive && frame.filterValue == "" {
			m.help = true
		} else {
			m.appendResourceFilter(frame, key)
		}
	case "/":
		if frame != nil && filterableRoute(frame.mode) {
			frame.filterActive = true
		}
	case "esc":
		if frame == nil {
			return m, tea.Quit
		}
		if filterableRoute(frame.mode) && frame.filterValue != "" {
			frame.filterValue = ""
			frame.filterActive = false
			m.resetFilteredSelection(frame)
			return m, nil
		}
		if filterableRoute(frame.mode) && frame.filterActive {
			frame.filterActive = false
			return m, nil
		}
		if frame.refreshing {
			m.finalizeRefresh(frame, "cancelled")
			return m, nil
		}
		m.pop()
	case "backspace":
		if frame == nil {
			return m, tea.Quit
		}
		if filterableRoute(frame.mode) && frame.filterValue != "" {
			runes := []rune(frame.filterValue)
			frame.filterValue = string(runes[:len(runes)-1])
			m.resetFilteredSelection(frame)
			return m, nil
		}
		if filterableRoute(frame.mode) && frame.filterActive {
			frame.filterActive = false
			return m, nil
		}
		m.pop()
	case "ctrl+g":
		return m.openSearch()
	case "c":
		if frame == nil || !filterableRoute(frame.mode) || !frame.filterActive && frame.filterValue == "" {
			return m.openContext()
		}
		m.appendResourceFilter(frame, key)
	case "e":
		if frame != nil && frame.mode == routeRelations && !frame.filterActive && frame.filterValue == "" {
			return m.openSelectedRelationEvidence()
		}
		if frame != nil && filterableRoute(frame.mode) {
			m.appendResourceFilter(frame, key)
		}
	case "ctrl+o":
		m.pop()
	case "ctrl+i":
		m.forward()
	case "ctrl+r":
		if frame != nil && frame.mode == routeList {
			return m.refreshCurrent()
		}
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "left":
		if frame != nil {
			if frame.refreshing {
				m.finalizeRefresh(frame, "cancelled")
			}
			m.pop()
		}
	case "right":
		if frame == nil {
			return m.openCatalog(m.selected)
		}
		return m.enterCurrent()
	case "pgup":
		if frame != nil && frame.mode == routeFields {
			frame.scroll = max(0, frame.scroll-max(1, m.height/2))
		} else {
			m.moveTo(false)
		}
	case "pgdown":
		if frame != nil && frame.mode == routeFields {
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
	default:
		if frame != nil && filterableRoute(frame.mode) && printableFilterInput(key) {
			m.appendResourceFilter(frame, key)
		}
	}
	return m, nil
}

func (m Model) updateCommandKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "ctrl+c":
		m.cancelAll()
		return m, tea.Quit
	case "esc":
		m.closeCommand()
		return m, nil
	case "backspace":
		if m.commandValue != "" {
			runes := []rune(m.commandValue)
			m.commandValue = string(runes[:len(runes)-1])
			m.commandStatus = ""
		}
		return m, nil
	case "enter":
		return m.executeCommand()
	default:
		if printableFilterInput(key) {
			m.commandValue += key
			m.commandStatus = ""
		}
		return m, nil
	}
}

func (m Model) executeCommand() (tea.Model, tea.Cmd) {
	command := strings.ToLower(strings.TrimSpace(m.commandValue))
	switch command {
	case "ec2", "instances", "ec2-instances":
		m.closeCommand()
		return m.openCatalogByID("ec2-instances")
	case "vpc", "network", "networking", "sg", "security-groups":
		m.closeCommand()
		return m.openCatalogByID("vpc-networking")
	case "route53", "r53", "dns", "hosted-zones":
		m.closeCommand()
		return m.openCatalogByID("route53-hosted-zones")
	case "iam", "roles", "iam-roles":
		m.closeCommand()
		return m.openCatalogByID("iam-roles")
	case "elb", "elbv2", "load-balancers":
		m.closeCommand()
		return m.openCatalogByID("elbv2-load-balancers")
	case "alb", "application-load-balancers":
		m.closeCommand()
		return m.pushAndDispatch(Intent{Kind: IntentOpen, Target: "elbv2-application-load-balancers"}, "Application Load Balancers", nil)
	case "nlb", "network-load-balancers":
		m.closeCommand()
		return m.pushAndDispatch(Intent{Kind: IntentOpen, Target: "elbv2-network-load-balancers"}, "Network Load Balancers", nil)
	case "search", "find":
		m.closeCommand()
		return m.openSearch()
	case "context", "ctx", "profile", "region":
		m.closeCommand()
		return m.openContext()
	case "home":
		m.closeCommand()
		m.cancelAll()
		m.history = nil
		m.forwardHistory = nil
		m.selected = 0
		return m, nil
	case "back":
		m.closeCommand()
		m.pop()
		return m, nil
	case "forward":
		m.closeCommand()
		m.forward()
		return m, nil
	case "refresh":
		m.closeCommand()
		if frame := m.current(); frame != nil && frame.mode == routeList {
			return m.refreshCurrent()
		}
		return m, nil
	case "help", "?":
		m.closeCommand()
		m.help = true
		return m, nil
	case "quit", "q":
		m.closeCommand()
		m.cancelAll()
		return m, tea.Quit
	case "":
		m.commandStatus = "Type a resource alias or action."
	default:
		m.commandStatus = "Unknown command: " + safeIntentText(command)
	}
	return m, nil
}

func (m *Model) closeCommand() {
	m.commandActive = false
	m.commandValue = ""
	m.commandStatus = ""
}

func (m Model) openCatalogByID(id string) (tea.Model, tea.Cmd) {
	for index, item := range homeCatalog {
		if item.ID == id {
			return m.openCatalog(index)
		}
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
	m.pushFrame(routeFrame{mode: routeSearch, target: "cross-profile-search", label: "Cross-profile search", searchFocus: 1})
	return m, nil
}

func (m Model) openContext() (tea.Model, tea.Cmd) {
	if current := m.current(); current != nil {
		m.finishFrame(current)
	}
	m.nextGeneration++
	requestCtx, cancel := context.WithCancel(m.ctx)
	m.pushFrame(routeFrame{
		mode: routeContext, label: "Select AWS context", status: "Loading configured profiles…",
		generation: m.nextGeneration, dispatchCancel: cancel, contextLoading: true,
	})
	return m, m.listContexts(requestCtx, m.nextGeneration)
}

func (m Model) listContexts(ctx context.Context, generation uint64) tea.Cmd {
	catalog, ok := m.dispatcher.(ContextCatalog)
	if !ok || catalog == nil {
		return func() tea.Msg {
			return contextChoicesMsg{generation: generation, err: fmt.Errorf("context catalog unavailable")}
		}
	}
	return func() tea.Msg {
		choices, err := catalog.ListContexts(ctx)
		return contextChoicesMsg{generation: generation, choices: choices, err: err}
	}
}

func (m Model) updateContextKey(key string) (tea.Model, tea.Cmd) {
	frame := m.current()
	if key == "ctrl+c" {
		m.cancelAll()
		return m, tea.Quit
	}
	if key == "esc" {
		if frame.contextFocus == 0 && frame.contextQuery != "" {
			frame.contextQuery = ""
			m.resetContextFilter(frame)
			return m, nil
		}
		m.pop()
		return m, nil
	}
	if frame.contextLoading {
		return m, nil
	}
	switch key {
	case "tab", "shift+tab":
		delta := 1
		if key == "shift+tab" {
			delta = -1
		}
		frame.contextFocus = (frame.contextFocus + delta + 3) % 3
	case "up", "left":
		m.adjustContextChoice(frame, -1)
	case "down", "right":
		m.adjustContextChoice(frame, 1)
	case "k":
		if frame.contextFocus == 0 && frame.contextQuery == "" {
			m.moveContextChoice(frame, -1)
		} else if frame.contextFocus == 0 {
			m.appendContextFilter(frame, key)
		}
	case "j":
		if frame.contextFocus == 0 && frame.contextQuery == "" {
			m.moveContextChoice(frame, 1)
		} else if frame.contextFocus == 0 {
			m.appendContextFilter(frame, key)
		}
	case "home":
		if frame.contextFocus == 0 && len(filteredContextChoices(*frame)) != 0 {
			frame.contextSelected = 0
			m.resetContextChoice(frame)
		}
	case "end":
		if choices := filteredContextChoices(*frame); frame.contextFocus == 0 && len(choices) != 0 {
			frame.contextSelected = len(choices) - 1
			m.resetContextChoice(frame)
		}
	case "backspace":
		if frame.contextFocus == 0 && frame.contextQuery != "" {
			runes := []rune(frame.contextQuery)
			frame.contextQuery = string(runes[:len(runes)-1])
			m.resetContextFilter(frame)
		} else if frame.contextFocus == 1 && frame.contextRegion != "" {
			runes := []rune(frame.contextRegion)
			frame.contextRegion = string(runes[:len(runes)-1])
			frame.verifiedContext = nil
		}
	case "ctrl+u":
		if frame.contextFocus == 0 {
			frame.contextQuery = ""
			m.resetContextFilter(frame)
		} else if frame.contextFocus == 1 {
			frame.contextRegion = ""
			frame.verifiedContext = nil
		}
	case "enter":
		choices := filteredContextChoices(*frame)
		if len(choices) == 0 {
			return m, nil
		}
		choice := choices[frame.contextSelected]
		region := strings.TrimSpace(frame.contextRegion)
		if frame.verifiedContext != nil && frame.verifiedContext.Profile == choice.Profile && frame.verifiedContext.Region == region {
			verified := *frame.verifiedContext
			regions := ""
			if frame.contextAllRegions && len(frame.contextRegions) > 1 {
				regions, _ = CanonicalRegionSet(frame.contextRegions, verified.Region)
			}
			m.cancelAll()
			m.history = nil
			m.forwardHistory = nil
			m.config.Profile, m.config.Region, m.config.Regions = verified.Profile, verified.Region, regions
			m.activeContext = &verified
			m.selected = 0
			return m, nil
		}
		if region == "" || ValidateContextSelection(choice.Profile, region) != nil {
			frame.status = "Enter a valid AWS region before verifying."
			return m, nil
		}
		catalog, ok := m.dispatcher.(ContextCatalog)
		if !ok || catalog == nil {
			frame.status = "! context catalog unavailable"
			return m, nil
		}
		m.nextGeneration++
		requestCtx, cancel := context.WithCancel(m.ctx)
		frame.generation = m.nextGeneration
		frame.dispatchCancel = cancel
		frame.contextLoading = true
		frame.verifiedContext = nil
		frame.status = "Verifying profile account… · Esc cancel"
		generation := frame.generation
		profile := choice.Profile
		return m, func() tea.Msg {
			resolution, err := catalog.ResolveContext(requestCtx, profile, region)
			return contextResolvedMsg{generation: generation, resolution: resolution, err: err}
		}
	default:
		if frame.contextFocus == 1 && len([]rune(key)) == 1 && validRegionInputRune([]rune(key)[0]) {
			frame.contextRegion += key
			frame.verifiedContext = nil
		} else if frame.contextFocus == 0 && printableFilterInput(key) {
			m.appendContextFilter(frame, key)
		}
	}
	return m, nil
}

func validRegionInputRune(value rune) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '-'
}

func (m *Model) moveContextChoice(frame *routeFrame, delta int) {
	choices := filteredContextChoices(*frame)
	if len(choices) == 0 {
		return
	}
	frame.contextSelected = clamp(frame.contextSelected+delta, 0, len(choices)-1)
	m.resetContextChoice(frame)
}

func (m *Model) resetContextChoice(frame *routeFrame) {
	choices := filteredContextChoices(*frame)
	if len(choices) == 0 {
		frame.contextRegion = ""
		frame.contextRegions = nil
		frame.contextAllRegions = false
		frame.verifiedContext = nil
		return
	}
	frame.contextSelected = clamp(frame.contextSelected, 0, len(choices)-1)
	choice := choices[frame.contextSelected]
	frame.contextRegion = choice.Region
	frame.contextRegions = append([]string(nil), choice.Regions...)
	if frame.contextRegion != "" && !containsString(frame.contextRegions, frame.contextRegion) {
		frame.contextRegions = append([]string{frame.contextRegion}, frame.contextRegions...)
	}
	frame.contextAllRegions = false
	frame.verifiedContext = nil
	frame.status = "Choose a profile and region, then verify its account."
}

func (m *Model) adjustContextChoice(frame *routeFrame, delta int) {
	switch frame.contextFocus {
	case 0:
		m.moveContextChoice(frame, delta)
	case 1:
		if len(frame.contextRegions) == 0 {
			return
		}
		index := 0
		for candidate, region := range frame.contextRegions {
			if region == frame.contextRegion {
				index = candidate
				break
			}
		}
		index = (index + delta + len(frame.contextRegions)) % len(frame.contextRegions)
		frame.contextRegion = frame.contextRegions[index]
		frame.verifiedContext = nil
		frame.status = "Region changed · verify the selected account."
	case 2:
		if len(frame.contextRegions) > 1 {
			frame.contextAllRegions = !frame.contextAllRegions
			frame.status = "Scope changed · verify the selected account."
		}
	}
}

func (m *Model) resetContextFilter(frame *routeFrame) {
	frame.contextSelected = 0
	m.resetContextChoice(frame)
}

func (m *Model) appendContextFilter(frame *routeFrame, value string) {
	frame.contextQuery += value
	m.resetContextFilter(frame)
}

func (m *Model) finishContextRequest(frame *routeFrame) {
	if frame.dispatchCancel != nil {
		frame.dispatchCancel()
		frame.dispatchCancel = nil
	}
	frame.contextLoading = false
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
		resources := filteredResources(*frame)
		if len(resources) == 0 {
			return m, nil
		}
		m.finishFrame(frame)
		resource := resources[frame.selected]
		resourceContext := frame.context
		if resource.Context != nil && resource.Context.Validate() == nil {
			copy := *resource.Context
			resourceContext = &copy
		}
		if hydrateListResource(resource.Target) {
			return m.pushAndDispatch(Intent{Kind: IntentOpen, Target: resource.Target}, resource.Title, resourceContext)
		}
		m.pushFrame(routeFrame{mode: routeDetail, target: resource.Target, label: resource.Title, context: resourceContext, detail: resource})
		return m, nil
	}
	if frame.mode == routeDetail {
		categories := detailCategories(frame.detail)
		if len(categories) == 0 {
			frame.status = "No categories for this resource."
			return m, nil
		}
		category := categories[frame.relationSelected]
		if category.Key == "incoming-relations" {
			intent := Intent{Kind: IntentIncoming, Target: frame.detail.Target}
			if frame.context != nil && frame.context.Validate() == nil {
				intent.ExpectedPartition = frame.context.Partition
				intent.ExpectedAccountID = frame.context.AccountID
			}
			return m.pushAndDispatch(intent, "Incoming relations", frame.context)
		}
		if category.Key == "detail" {
			m.pushFrame(routeFrame{
				mode: routeFields, target: frame.target, label: category.Label, context: frame.context, detail: frame.detail,
			})
			return m, nil
		}
		if category.Key == "tags" {
			m.pushFrame(routeFrame{
				mode: routeTags, target: frame.target, label: category.Label, context: frame.context, detail: frame.detail,
			})
			return m, nil
		}
		group := category.Group
		if directRelationGroup(group) {
			relation := group.Relations[0]
			return m.pushAndDispatch(Intent{Kind: IntentOpen, Target: relation.Target}, relation.Label, frame.context)
		}
		m.pushFrame(routeFrame{
			mode: routeRelations, target: frame.target, label: group.Label, context: frame.context,
			detail: frame.detail, relationGroup: group.Key,
		})
		return m, nil
	}
	if frame.mode == routeTags {
		frame.status = "Tags are read-only metadata."
		return m, nil
	}
	if frame.mode == routeFields {
		frame.status = "Detail fields are read-only."
		return m, nil
	}
	relations := filteredRelations(*frame)
	if len(relations) == 0 {
		frame.status = "No navigable relations for this resource."
		return m, nil
	}
	relation := relations[frame.relationSelected]
	if relation.Target == "" {
		return m.openRelationEvidence(relation)
	}
	return m.pushAndDispatch(Intent{Kind: IntentOpen, Target: relation.Target}, relation.Label, frame.context)
}

func (m Model) openSelectedRelationEvidence() (tea.Model, tea.Cmd) {
	frame := m.current()
	if frame == nil || frame.mode != routeRelations {
		return m, nil
	}
	relations := filteredRelations(*frame)
	if len(relations) == 0 {
		frame.status = "No relationship evidence to show."
		return m, nil
	}
	return m.openRelationEvidence(relations[frame.relationSelected])
}

func (m Model) openRelationEvidence(relation ProjectionRelation) (tea.Model, tea.Cmd) {
	target := relation.TargetRef
	if target == "" {
		target = "Unresolved target"
	}
	subtitle := "Relationship evidence"
	navigation := "Available"
	if relation.Target == "" {
		subtitle += " · target navigation unavailable"
		navigation = "Unavailable · no supported exact-read resolver"
	}
	fields := []ProjectionField{
		{Label: "Target reference", Value: target},
		{Label: "Target navigation", Value: navigation},
		{Label: "Relation", Value: relation.Type},
		{Label: "Direction", Value: relation.Direction},
		{Label: "Condition", Value: relation.Condition},
		{Label: "Confidence", Value: relation.Kind},
		{Label: "Reason", Value: relation.Reason},
		{Label: "Operation", Value: relation.Operation},
		{Label: "Scope", Value: relation.Scope},
		{Label: "Observed at", Value: relation.ObservedAt},
	}
	m.pushFrame(routeFrame{
		mode: routeFields, target: relation.Target, label: "Relationship evidence", context: m.current().context,
		detail: ResourceProjection{Title: relation.Label, Subtitle: subtitle, Fields: fields},
	})
	return m, nil
}

func hydrateListResource(target string) bool {
	resourceType, _, ok := strings.Cut(target, ":")
	if !ok {
		return false
	}
	switch resourceType {
	case "iam.role", "iam.managed-policy", "iam.inline-policy":
		return true
	default:
		return false
	}
}

func filterableRoute(mode routeMode) bool {
	return mode == routeList || mode == routeRelations || mode == routeTags
}

func (m *Model) promoteSingletonSummary(frame *routeFrame) bool {
	if frame == nil || frame.mode != routeList || frame.intent.Kind != IntentOpen ||
		!singletonDetailTarget(frame.intent.Target) || len(frame.projection.Resources) != 1 {
		return false
	}
	resource := frame.projection.Resources[0]
	resourceContext := frame.context
	if resource.Context != nil && resource.Context.Validate() == nil {
		copy := *resource.Context
		resourceContext = &copy
	}
	frame.mode = routeDetail
	frame.target = resource.Target
	frame.label = resource.Title
	frame.status = ""
	frame.context = resourceContext
	frame.detail = resource
	frame.projection = IntentProjection{}
	frame.selected = 0
	frame.relationSelected = 0
	frame.scroll = 0
	frame.filterValue = ""
	return true
}

func singletonDetailTarget(target string) bool {
	resourceType, _, ok := strings.Cut(target, ":")
	if !ok {
		return false
	}
	switch resourceType {
	case "ec2.instance", "ec2.volume", "ec2.security-group", "ec2.security-group-rule",
		"ec2.vpc", "ec2.subnet", "ec2.route-table", "iam.role", "iam.instance-profile",
		"iam.managed-policy", "iam.inline-policy", "iam.managed-policy-version",
		"cloudfront.distribution-domain", "elbv2.load-balancer-dns", "elbv2.load-balancer",
		"elbv2.target-group", "s3.bucket":
		return true
	default:
		return false
	}
}

func directRelationGroup(group relationGroup) bool {
	return len(group.Relations) == 1 && group.Relations[0].Target != "" &&
		(group.Key == "inbound-rules" || group.Key == "outbound-rules" ||
			group.Key == "attached-policies" || group.Key == "inline-policies" ||
			group.Key == "policy-document" || group.Key == "dns-records" || group.Key == "alias-targets" ||
			group.Key == "listeners" || group.Key == "listener-rules" || group.Key == "target-groups" || group.Key == "targets")
}

func (m Model) pushAndDispatch(intent Intent, label string, inherited *AWSContext) (tea.Model, tea.Cmd) {
	if current := m.current(); current != nil {
		m.finishFrame(current)
	}
	m.nextGeneration++
	if inherited != nil && inherited.Validate() == nil {
		intent.Profile, intent.Region = inherited.Profile, inherited.Region
		intent.Regions = ""
	}
	intent = m.resolveIntentContext(intent)
	dispatchCtx, cancel := context.WithCancel(m.ctx)
	frame := routeFrame{
		mode: routeList, target: intent.Target, label: label, intent: intent, context: inherited,
		status: "Loading " + safeIntentText(label) + "… · Esc cancel", generation: m.nextGeneration, dispatchCancel: cancel,
	}
	m.pushFrame(frame)
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
	frame.staged.clear()
	frame.status = fmt.Sprintf("Showing cached %d · refreshing… · Esc cancel", len(frame.projection.Resources))
	intent := frame.intent
	if intent.Kind == IntentIncoming {
		intent.Force = true
	} else if intent.Kind != IntentSearch || intent.Target != "cross-profile-search" {
		intent = Intent{Kind: IntentRefresh, Target: frame.target, Regions: frame.intent.Regions}
		if frame.context != nil && frame.context.Validate() == nil {
			intent.Profile, intent.Region = frame.context.Profile, frame.context.Region
		} else {
			intent.Profile, intent.Region = frame.intent.Profile, frame.intent.Region
		}
	}
	return m, m.dispatch(dispatchCtx, intent, frame.generation)
}

func (m Model) dispatch(ctx context.Context, intent Intent, generation uint64) tea.Cmd {
	if m.dispatcher == nil {
		return func() tea.Msg {
			return intentStartedMsg{generation: generation, result: IntentResultMsg{Intent: intent, Err: fmt.Errorf("no dispatcher")}}
		}
	}
	intent = m.resolveIntentContext(intent)
	return func() tea.Msg {
		stream, err := m.dispatcher.Dispatch(ctx, intent)
		return intentStartedMsg{generation: generation, result: IntentResultMsg{Intent: intent, Stream: stream, Err: err}}
	}
}

func (m Model) resolveIntentContext(intent Intent) Intent {
	if intent.Profile == "" && intent.Region == "" {
		intent.Profile, intent.Region, intent.Regions = m.config.Profile, m.config.Region, m.config.Regions
	} else if intent.Region == "" {
		intent.Region = m.config.Region
	}
	return intent
}

func waitIntent(generation uint64, stream IntentStream) tea.Cmd {
	return func() tea.Msg {
		update, ok := <-stream.Updates()
		return intentStreamMsg{generation: generation, update: update, open: ok}
	}
}

func (m *Model) applyIntentUpdate(frame *routeFrame, update IntentUpdate) {
	if frame.refreshing {
		frame.staged.apply(update)
		state := update.Query.Snapshot.State
		if state == LoadReady || state == LoadEmpty {
			frame.staged.promote(&frame.context, &frame.coverage, &frame.graph, &frame.projection)
			if frame.selected >= len(frame.projection.Resources) {
				frame.selected = max(0, len(frame.projection.Resources)-1)
			}
			frame.status = queryStatus(update.Query, len(frame.projection.Resources))
			if frame.coverage != nil {
				frame.status = searchCoverageStatus(frame.coverage, len(frame.projection.Resources), state)
			}
		} else if terminalLoadState(state) {
			if state == LoadStale {
				frame.status = queryStatus(update.Query, len(frame.projection.Resources))
			} else {
				frame.status = cachedRefreshStatus(len(frame.projection.Resources), strings.TrimPrefix(queryStatus(update.Query, len(frame.projection.Resources)), "! "))
			}
		} else {
			frame.status = fmt.Sprintf("Showing cached %d · refreshing… · Esc cancel", len(frame.projection.Resources))
		}
		return
	}
	if update.Context != nil && update.Context.Validate() == nil {
		copy := *update.Context
		frame.context = &copy
	} else if update.Query.Key.Context.Validate() == nil {
		copy := update.Query.Key.Context
		frame.context = &copy
	}
	if frame.context != nil && frame.context.Validate() == nil && frame.context.Profile == m.config.Profile && frame.context.Region == m.config.Region {
		copy := *frame.context
		m.activeContext = &copy
	}
	projection := update.Projection
	if len(projection.Resources) == 0 && update.Query.Snapshot.ResourceCount() != 0 {
		projection = ProjectQueryUpdate(update.Query)
	}
	state := update.Query.Snapshot.State
	if update.Coverage != nil {
		frame.coverage = cloneSearchCoverage(update.Coverage)
	}
	if update.Graph != nil {
		frame.graph = cloneGraphSnapshot(update.Graph)
	}
	replace := len(projection.Resources) != 0 || state == LoadReady || state == LoadEmpty
	if replace {
		frame.projection = projection
		if frame.selected >= len(frame.projection.Resources) {
			frame.selected = max(0, len(frame.projection.Resources)-1)
		}
	}
	frame.status = queryStatus(update.Query, len(frame.projection.Resources))
	if frame.coverage != nil {
		frame.status = searchCoverageStatus(frame.coverage, len(frame.projection.Resources), state)
	} else if frame.graph != nil {
		frame.status = graphSnapshotStatus(frame.graph, len(frame.projection.Resources), state)
	}
}

type refreshStage struct {
	context    *AWSContext
	coverage   *SearchCoverage
	projection *IntentProjection
	graph      *GraphSnapshot
}

func (stage *refreshStage) apply(update IntentUpdate) {
	if update.Context != nil && update.Context.Validate() == nil {
		copy := *update.Context
		stage.context = &copy
	} else if update.Query.Key.Context.Validate() == nil {
		copy := update.Query.Key.Context
		stage.context = &copy
	}
	if update.Coverage != nil {
		stage.coverage = cloneSearchCoverage(update.Coverage)
	}
	if update.Graph != nil {
		stage.graph = cloneGraphSnapshot(update.Graph)
	}
	projection := update.Projection
	if len(projection.Resources) == 0 && update.Query.Snapshot.ResourceCount() != 0 {
		projection = ProjectQueryUpdate(update.Query)
	}
	state := update.Query.Snapshot.State
	if projection.Resources != nil || state == LoadEmpty {
		copy := projection
		stage.projection = &copy
	}
}

func (stage *refreshStage) promote(awsContext **AWSContext, coverage **SearchCoverage, graph **GraphSnapshot, projection *IntentProjection) {
	if stage.context != nil {
		copy := *stage.context
		*awsContext = &copy
	}
	if stage.coverage != nil {
		*coverage = cloneSearchCoverage(stage.coverage)
	}
	if stage.graph != nil {
		*graph = cloneGraphSnapshot(stage.graph)
	}
	if stage.projection != nil {
		*projection = *stage.projection
	}
}

func (stage *refreshStage) clear() { *stage = refreshStage{} }

func cachedRefreshStatus(count int, outcome string) string {
	return fmt.Sprintf("Showing cached %d · refresh %s", count, safeIntentText(outcome))
}

func cloneSearchCoverage(coverage *SearchCoverage) *SearchCoverage {
	if coverage == nil {
		return nil
	}
	copy := *coverage
	copy.Profiles = append([]SearchProfileCoverage(nil), coverage.Profiles...)
	return &copy
}

func cloneGraphSnapshot(snapshot *GraphSnapshot) *GraphSnapshot {
	if snapshot == nil {
		return nil
	}
	copy := *snapshot
	return &copy
}

func graphSnapshotStatus(snapshot *GraphSnapshot, count int, state LoadState) string {
	if snapshot == nil {
		return queryStatus(QueryUpdate{Snapshot: QuerySnapshot{State: state}}, count)
	}
	if snapshot.Error || state.isFailure() {
		return "! automatic relationship collection failed"
	}
	if snapshot.Collecting || state == LoadLoading || state == LoadQueued {
		return "AUTO · collecting group " + safeIntentText(snapshot.Group) + "… · Esc cancel"
	}
	mode := "collected"
	if snapshot.Reused {
		mode = "cached"
	}
	return fmt.Sprintf("SNAPSHOT · %s · %ds old · %d references · coverage %d succeeded, %d failed, %d not observed",
		mode, max(int64(0), snapshot.AgeSeconds), count, snapshot.Succeeded, snapshot.Failed, snapshot.NotObserved)
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
	switch state {
	case LoadQueued, LoadLoading, LoadRefreshing, LoadNotLoaded:
		prefix = "Loading"
	case LoadReady, LoadEmpty:
	default:
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
		frame.selected = clamp(frame.selected+delta, 0, max(0, len(filteredResources(*frame))-1))
	} else if frame.mode == routeDetail {
		frame.relationSelected = clamp(frame.relationSelected+delta, 0, max(0, len(detailCategories(frame.detail))-1))
		frame.scroll = 0
	} else if frame.mode == routeRelations {
		frame.relationSelected = clamp(frame.relationSelected+delta, 0, max(0, len(filteredRelations(*frame))-1))
	} else if frame.mode == routeTags {
		frame.selected = clamp(frame.selected+delta, 0, max(0, len(filteredTags(*frame))-1))
	} else if frame.mode == routeFields {
		frame.scroll = max(0, frame.scroll+delta)
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
		if resources := filteredResources(*frame); end && len(resources) != 0 {
			frame.selected = len(resources) - 1
		}
	} else if frame.mode == routeDetail {
		frame.relationSelected = 0
		if categories := detailCategories(frame.detail); end && len(categories) != 0 {
			frame.relationSelected = len(categories) - 1
		}
		frame.scroll = 0
	} else if frame.mode == routeRelations {
		frame.relationSelected = 0
		if relations := filteredRelations(*frame); end && len(relations) != 0 {
			frame.relationSelected = len(relations) - 1
		}
	} else if frame.mode == routeTags {
		frame.selected = 0
		if tags := filteredTags(*frame); end && len(tags) != 0 {
			frame.selected = len(tags) - 1
		}
	} else if frame.mode == routeFields {
		frame.scroll = 0
		if end {
			frame.scroll = int(^uint(0) >> 1)
		}
	}
}

type detailCategory struct {
	Key, Label string
	Count      int
	Group      relationGroup
}

func detailCategories(resource ResourceProjection) []detailCategory {
	groups := relationGroups(resource)
	categories := make([]detailCategory, 0, len(groups)+3)
	for _, group := range groups {
		categories = append(categories, detailCategory{Key: group.Key, Label: group.Label, Count: len(group.Relations), Group: group})
	}
	if supportsIncomingRelations(resource.Target) {
		categories = append(categories, detailCategory{Key: "incoming-relations", Label: "Incoming relations", Count: -1})
	}
	categories = append(categories, detailCategory{Key: "detail", Label: "Detail", Count: len(resource.Fields)})
	categories = append(categories, detailCategory{Key: "tags", Label: "Tags", Count: len(resource.Tags)})
	return categories
}

func supportsIncomingRelations(target string) bool {
	resourceType, id, ok := strings.Cut(target, ":")
	return ok && id != "" && (resourceType == "ec2.security-group" || resourceType == "ec2.vpc")
}

type relationGroup struct {
	Key       string
	Label     string
	Relations []ProjectionRelation
}

var relationGroupOrder = []struct{ Key, Label string }{
	{Key: "alias-targets", Label: "Alias target"},
	{Key: "listeners", Label: "Listeners"},
	{Key: "listener-rules", Label: "Listener rules"},
	{Key: "target-groups", Label: "Target groups"},
	{Key: "targets", Label: "Registered targets"},
	{Key: "origins", Label: "Origins"},
	{Key: "inbound-rules", Label: "Inbound rules"},
	{Key: "outbound-rules", Label: "Outbound rules"},
	{Key: "attached-policies", Label: "Attached policies"},
	{Key: "inline-policies", Label: "Inline policies"},
	{Key: "policy-document", Label: "Policy document"},
	{Key: "dns-records", Label: "DNS records"},
	{Key: "security-groups", Label: "Security groups"},
	{Key: "volumes", Label: "Volumes"},
	{Key: "instances", Label: "EC2 instances"},
	{Key: "vpcs", Label: "VPCs"},
	{Key: "subnets", Label: "Subnets"},
	{Key: "route-tables", Label: "Route tables"},
	{Key: "instance-profiles", Label: "IAM instance profiles"},
	{Key: "iam-roles", Label: "IAM roles"},
	{Key: "iam-policies", Label: "IAM policies"},
	{Key: "hosted-zones", Label: "Hosted zones"},
	{Key: "related", Label: "Related resources"},
	{Key: "evidence", Label: "Relationship evidence"},
}

func relationGroups(resource ResourceProjection) []relationGroup {
	grouped := make(map[string][]ProjectionRelation)
	for _, relation := range resource.Relations {
		key := relationGroupKey(relation)
		grouped[key] = append(grouped[key], relation)
	}
	groups := make([]relationGroup, 0, len(grouped))
	for _, definition := range relationGroupOrder {
		if relations := grouped[definition.Key]; len(relations) != 0 {
			groups = append(groups, relationGroup{Key: definition.Key, Label: definition.Label, Relations: relations})
		}
	}
	return groups
}

func relationGroupKey(relation ProjectionRelation) string {
	if relation.Target == "" {
		return "evidence"
	}
	targetType, _, _ := strings.Cut(relation.Target, ":")
	switch targetType {
	case "cloudfront.distribution-domain", "elbv2.load-balancer-dns":
		return "alias-targets"
	case "elbv2.listeners":
		return "listeners"
	case "elbv2.rules":
		return "listener-rules"
	case "elbv2.target-group":
		return "target-groups"
	case "elbv2.targets":
		return "targets"
	case "s3.bucket":
		return "origins"
	case "ec2.security-group-rules-inbound":
		return "inbound-rules"
	case "ec2.security-group-rules-outbound":
		return "outbound-rules"
	case "ec2.security-group", "ec2.security-group-rule":
		return "security-groups"
	case "ec2.volume":
		return "volumes"
	case "ec2.instance":
		return "instances"
	case "ec2.vpc":
		return "vpcs"
	case "ec2.subnet":
		return "subnets"
	case "ec2.route-table":
		return "route-tables"
	case "iam.instance-profile":
		return "instance-profiles"
	case "iam.role":
		return "iam-roles"
	case "iam.role-attached-policies":
		return "attached-policies"
	case "iam.role-inline-policies":
		return "inline-policies"
	case "iam.managed-policy-version":
		return "policy-document"
	case "route53.records":
		return "dns-records"
	case "hosted-zone":
		return "hosted-zones"
	default:
		if strings.HasPrefix(targetType, "iam.") {
			return "iam-policies"
		}
		return "related"
	}
}

func relationsForGroup(frame routeFrame) []ProjectionRelation {
	for _, group := range relationGroups(frame.detail) {
		if group.Key == frame.relationGroup {
			return group.Relations
		}
	}
	return nil
}

func filteredRelations(frame routeFrame) []ProjectionRelation {
	relations := relationsForGroup(frame)
	query := strings.ToLower(strings.TrimSpace(frame.filterValue))
	if query == "" {
		return relations
	}
	filtered := make([]ProjectionRelation, 0, len(relations))
	for _, relation := range relations {
		searchable := stringsJoinNonEmpty(relation.Label, relation.Target, relation.TargetRef, relation.Type, relation.Direction, relation.Condition, relation.Kind, relation.Reason, relation.Scope, relation.Operation)
		if strings.Contains(strings.ToLower(searchable), query) {
			filtered = append(filtered, relation)
		}
	}
	return filtered
}

func filteredTags(frame routeFrame) []ProjectionTag {
	query := strings.ToLower(strings.TrimSpace(frame.filterValue))
	if query == "" {
		return frame.detail.Tags
	}
	filtered := make([]ProjectionTag, 0, len(frame.detail.Tags))
	for _, tag := range frame.detail.Tags {
		if strings.Contains(strings.ToLower(tag.Key+" "+tag.Value), query) {
			filtered = append(filtered, tag)
		}
	}
	return filtered
}

func (m *Model) appendResourceFilter(frame *routeFrame, value string) {
	frame.filterActive = true
	frame.filterValue += value
	m.resetFilteredSelection(frame)
}

func (m *Model) resetFilteredSelection(frame *routeFrame) {
	if frame.mode == routeRelations {
		frame.relationSelected = 0
	} else {
		frame.selected = 0
	}
}

func printableFilterInput(value string) bool {
	runes := []rune(value)
	return len(runes) == 1 && runes[0] >= 0x20 && runes[0] != 0x7f
}

func filteredResources(frame routeFrame) []ResourceProjection {
	query := strings.ToLower(strings.TrimSpace(frame.filterValue))
	if query == "" {
		return frame.projection.Resources
	}
	resources := make([]ResourceProjection, 0, len(frame.projection.Resources))
	for _, resource := range frame.projection.Resources {
		if resourceMatches(resource, query) {
			resources = append(resources, resource)
		}
	}
	return resources
}

func resourceMatches(resource ResourceProjection, query string) bool {
	values := []string{resource.Title, resource.Subtitle, resource.Target, strings.Join(resource.AvailableViaProfiles, " ")}
	for _, field := range resource.Fields {
		values = append(values, field.Label, field.Value)
	}
	for _, tag := range resource.Tags {
		values = append(values, tag.Key, tag.Value)
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}

func filteredContextChoices(frame routeFrame) []ContextChoice {
	query := strings.ToLower(strings.TrimSpace(frame.contextQuery))
	if query == "" {
		return frame.contextChoices
	}
	choices := make([]ContextChoice, 0, len(frame.contextChoices))
	for _, choice := range frame.contextChoices {
		searchable := choice.Profile + " " + choice.Region + " " + choice.Group + " " + strings.Join(choice.Regions, " ")
		if strings.Contains(strings.ToLower(searchable), query) {
			choices = append(choices, choice)
		}
	}
	return choices
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
		restorable := restorableHistoryFrame(*frame)
		m.finishFrame(frame)
		if restorable {
			m.forwardHistory = append(m.forwardHistory, *frame)
		}
	}
	if len(m.history) != 0 {
		m.history = m.history[:len(m.history)-1]
	}
}

func (m *Model) forward() {
	if len(m.forwardHistory) == 0 {
		return
	}
	index := len(m.forwardHistory) - 1
	frame := m.forwardHistory[index]
	m.forwardHistory = m.forwardHistory[:index]
	frame.dispatchCancel = nil
	frame.stream = nil
	frame.refreshing = false
	m.history = append(m.history, frame)
}

func (m *Model) pushFrame(frame routeFrame) {
	m.forwardHistory = nil
	m.history = append(m.history, frame)
}

func navigableHistoryMode(mode routeMode) bool {
	return mode == routeList || mode == routeDetail || mode == routeFields || mode == routeRelations || mode == routeTags
}

func restorableHistoryFrame(frame routeFrame) bool {
	if !navigableHistoryMode(frame.mode) {
		return false
	}
	return frame.mode != routeList || frame.terminalUpdate || frame.intent.Target == ""
}

func (m *Model) cancelAll() {
	for index := range m.history {
		m.finishFrame(&m.history[index])
	}
}

func (m *Model) finishFrame(frame *routeFrame) {
	if frame.refreshing {
		m.finalizeRefresh(frame, "cancelled")
		return
	}
	fence := !frame.terminalUpdate && (frame.dispatchCancel != nil || frame.stream != nil)
	m.releaseFrame(frame)
	if fence {
		m.nextGeneration++
		frame.generation = m.nextGeneration
	}
}

func (m *Model) finalizeRefresh(frame *routeFrame, outcome string) {
	m.releaseFrame(frame)
	m.nextGeneration++
	frame.generation = m.nextGeneration
	frame.staged.clear()
	frame.refreshing = false
	if outcome != "" {
		frame.status = cachedRefreshStatus(len(frame.projection.Resources), outcome)
	}
}

func (m *Model) releaseFrame(frame *routeFrame) {
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
