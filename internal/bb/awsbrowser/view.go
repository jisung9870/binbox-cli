package awsbrowser

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func renderModel(m Model) string {
	if m.width < MinimumWidth || m.height < MinimumHeight {
		return MinimumSizeMessage
	}
	if m.help {
		return renderHelp(m.width, m.height)
	}
	if m.route != "" {
		return renderPending(m)
	}
	return renderHome(m)
}

func renderHome(m Model) string {
	inner := max(1, m.width-4)
	profile := m.config.Profile
	if profile == "" {
		profile = "ambient"
	}
	region := m.config.Region
	if region == "" {
		region = "unresolved"
	}
	lines := []string{
		fit("AWS Browser · READ ONLY", inner),
		fit(fmt.Sprintf("Profile %s  Account unresolved  Principal unresolved  %s", profile, region), inner),
		fit("Services / tasks · LIST", inner),
	}
	for i, item := range homeCatalog {
		marker := "  "
		if i == m.selected {
			marker = "> "
		}
		labelWidth := max(20, inner-32)
		if m.width < 60 {
			labelWidth = 24
		}
		lines = append(lines, fit(marker+padRight(item.Label, labelWidth)+item.Status, inner))
	}
	footer := "↑↓ move  enter open  ctrl+g cross-profile  ? help  ctrl+c quit"
	if m.width < 60 {
		footer = "↑↓ move · enter open · ? help\nctrl+g search · ctrl+c quit"
	}
	lines = append(lines, strings.Split(footer, "\n")...)
	return frame(lines, m.width, m.height)
}

func renderPending(m Model) string {
	item := catalogByID(m.route)
	lines := []string{
		"AWS Browser · READ ONLY",
		"AWS > " + item.Label,
		"",
		m.status,
		"",
		"No resource result has been loaded.",
		"esc back  ctrl+r refresh  ? help  ctrl+c quit",
	}
	return frame(lines, m.width, m.height)
}

func renderHelp(width, height int) string {
	lines := []string{
		"AWS Browser help · READ ONLY", "",
		"↑↓ / j k       move selection",
		"enter          open selected item",
		"ctrl+g         cross-profile search",
		"ctrl+r         refresh current route",
		"esc            back; exit from Home",
		"ctrl+c         quit and cancel requests", "",
		"? or esc close help",
	}
	return frame(lines, width, height)
}

func frame(lines []string, width, height int) string {
	inner := max(1, width-4)
	contentHeight := max(1, height-2)
	for len(lines) < contentHeight {
		lines = append(lines, "")
	}
	if len(lines) > contentHeight {
		lines = lines[:contentHeight]
	}
	var b strings.Builder
	b.WriteString("┌" + strings.Repeat("─", inner+2) + "┐\n")
	for i, line := range lines {
		b.WriteString("│ " + padRight(fit(line, inner), inner) + " │")
		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}
	b.WriteString("\n└" + strings.Repeat("─", inner+2) + "┘")
	return b.String()
}

func fit(value string, width int) string { return ansi.Truncate(value, width, "…") }

func padRight(value string, width int) string {
	missing := width - ansi.StringWidth(value)
	if missing <= 0 {
		return value
	}
	return value + strings.Repeat(" ", missing)
}

func catalogByID(id string) catalogItem {
	for _, item := range homeCatalog {
		if item.ID == id {
			return item
		}
	}
	return catalogItem{ID: id, Label: id}
}
