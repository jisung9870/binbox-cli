package awsbrowser

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

type Config struct {
	Profile  string
	Region   string
	Selector string
	NoColor  bool
}

type IntentDispatcher interface {
	Dispatch(context.Context, Intent) error
}

type catalogItem struct{ ID, Label, Status string }

var homeCatalog = []catalogItem{
	{ID: "ec2-instances", Label: "EC2 Instances", Status: "Not loaded"},
	{ID: "route53-hosted-zones", Label: "Route 53 Hosted Zones", Status: "Not loaded · AWS global"},
	{ID: "iam-roles", Label: "IAM Roles", Status: "Not loaded · AWS global"},
	{ID: "vpc-networking", Label: "VPC & Networking", Status: "Not loaded"},
	{ID: "cross-profile-search", Label: "Cross-profile search", Status: "Domain, role · scope on open"},
}

type Model struct {
	ctx                     context.Context
	config                  Config
	dispatcher              IntentDispatcher
	width, height, selected int
	help                    bool
	route, status           string
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
	case IntentResultMsg:
		if msg.Err != nil {
			m.status = "! " + msg.Error()
		}
		return m, nil
	case tea.KeyPressMsg:
		key := msg.String()
		if m.help {
			if key == "?" || key == "esc" {
				m.help = false
			}
			if key == "ctrl+c" {
				return m, tea.Quit
			}
			return m, nil
		}
		switch key {
		case "ctrl+c":
			return m, tea.Quit
		case "?":
			m.help = true
		case "up", "k":
			if m.route == "" && m.selected > 0 {
				m.selected--
			}
		case "pgup":
			if m.route == "" {
				m.selected = 0
			}
		case "down", "j":
			if m.route == "" && m.selected < len(homeCatalog)-1 {
				m.selected++
			}
		case "pgdown":
			if m.route == "" {
				m.selected = len(homeCatalog) - 1
			}
		case "home":
			if m.route == "" {
				m.selected = 0
			}
		case "end":
			if m.route == "" {
				m.selected = len(homeCatalog) - 1
			}
		case "esc":
			if m.route == "" {
				return m, tea.Quit
			}
			m.route, m.status = "", ""
		case "ctrl+g":
			return m.open(len(homeCatalog) - 1)
		case "ctrl+r":
			if m.route != "" {
				return m.dispatch(IntentRefresh, m.route)
			}
		case "enter":
			if m.route == "" {
				return m.open(m.selected)
			}
		}
	}
	return m, nil
}

func (m Model) open(index int) (tea.Model, tea.Cmd) {
	item := homeCatalog[index]
	m.route = item.ID
	m.status = "Loading " + item.Label + "… · Esc cancel"
	kind := IntentOpen
	if item.ID == "cross-profile-search" {
		kind = IntentSearch
	}
	return m.dispatch(kind, item.ID)
}

func (m Model) dispatch(kind IntentKind, target string) (tea.Model, tea.Cmd) {
	if m.dispatcher == nil {
		return m, nil
	}
	intent := Intent{Kind: kind, Target: target, Profile: m.config.Profile, Region: m.config.Region}
	return m, func() tea.Msg { return IntentResultMsg{Intent: intent, Err: m.dispatcher.Dispatch(m.ctx, intent)} }
}

func (m Model) View() tea.View {
	view := tea.NewView(renderModel(m))
	view.AltScreen = true
	return view
}
