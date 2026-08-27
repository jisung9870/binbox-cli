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
	frame := m.current()
	if frame == nil {
		return renderHome(m)
	}
	if frame.mode == routeSearch {
		return renderSearch(m, *frame)
	}
	if frame.mode == routeDetail {
		return renderDetail(m, *frame)
	}
	return renderList(m, *frame)
}

func renderSearch(m Model, route routeFrame) string {
	inner := max(1, m.width-4)
	marker := func(index int) string {
		if route.searchFocus == index {
			return "> "
		}
		return "  "
	}
	value := route.searchValue
	if value == "" {
		value = "type a domain, role name, ID, or ARN"
	}
	lines := []string{
		"AWS Browser · READ ONLY",
		"AWS > Cross-profile search",
		"Local editor · no AWS request until submit",
		"",
		fit(marker(0)+"Kind   < "+searchChoiceLabel(searchKinds[route.searchKind])+" >", inner),
		fit(marker(1)+"Value  "+safeIntentText(value), inner),
		fit(marker(2)+"Scope  < "+searchChoiceLabel(searchScopes[route.searchScope])+" >", inner),
	}
	if route.status != "" {
		lines = append(lines, "", fit(route.status, inner))
	}
	lines = append(lines, "", "tab field · ←→ choose · enter submit · esc back")
	return frameView(lines, m.width, m.height)
}

func searchChoiceLabel(value string) string {
	switch value {
	case "ec2-instances":
		return "EC2 instances"
	case "all":
		return "all profiles"
	case "current":
		return "current profile"
	default:
		return value
	}
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
		fit(fmt.Sprintf("Profile %s  Account unresolved  Principal unresolved  %s", safeIntentText(profile), safeIntentText(region)), inner),
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
	return frameView(lines, m.width, m.height)
}

func renderList(m Model, route routeFrame) string {
	inner := max(1, m.width-4)
	lines := []string{
		"AWS Browser · READ ONLY",
		fit(contextLine(m.config, route.context), inner),
		fit("AWS > "+safeIntentText(route.label), inner),
		fit(route.status, inner),
		"",
	}
	resources := route.projection.Resources
	if len(resources) == 0 {
		lines = append(lines, "No resource result has been loaded.")
	} else {
		preview := []string{}
		if m.width >= 80 {
			selected := resources[route.selected]
			preview = append(preview, "", fit("Preview · "+safeIntentText(selected.Title), inner))
			for _, field := range selected.Fields {
				preview = append(preview, wrappedField(field.Label, field.Value, inner)...)
				if len(preview) >= 6 {
					preview = preview[:6]
					break
				}
			}
		}
		footerRows := 2
		available := max(1, m.height-2-len(lines)-len(preview)-footerRows-1)
		start := max(0, route.selected-available+1)
		end := min(len(resources), start+available)
		lines = append(lines, fmt.Sprintf("Resources (%d) · rows %d-%d", len(resources), start+1, end))
		for index := start; index < end; index++ {
			resource := resources[index]
			marker := "  "
			if index == route.selected {
				marker = "> "
			}
			row := marker + safeIntentText(resource.Title)
			if resource.Subtitle != "" {
				row += "  " + safeIntentText(resource.Subtitle)
			}
			lines = append(lines, fit(row, inner))
		}
		lines = append(lines, preview...)
	}
	lines = append(lines, "", "↑↓ move  enter detail  esc back  ctrl+r refresh  ? help")
	return frameView(lines, m.width, m.height)
}

func renderDetail(m Model, route routeFrame) string {
	inner := max(1, m.width-4)
	resource := route.detail
	header := []string{
		"AWS Browser · READ ONLY",
		fit(contextLine(m.config, route.context), inner),
		fit("AWS > "+safeIntentText(resource.Title), inner),
	}
	content := []string{}
	if resource.Subtitle != "" {
		content = append(content, fit(safeIntentText(resource.Subtitle), inner), "")
	}
	content = append(content, fmt.Sprintf("Relations (%d)", len(resource.Relations)))
	if len(resource.Relations) == 0 {
		content = append(content, "  No relationship evidence.")
	}
	for index, relation := range resource.Relations {
		marker := "  "
		if index == route.relationSelected {
			marker = "> "
		}
		label := safeIntentText(relation.Label)
		if relation.Target == "" {
			label += " · evidence only"
		} else {
			label += " · enter open"
		}
		content = append(content, fit(marker+label, inner))
		if relation.Reason != "" {
			content = append(content, wrapText("    "+safeIntentText(relation.Reason), inner)...)
		}
		evidence := stringsJoinNonEmpty(relation.Kind, relation.Scope, relation.Operation, relation.ObservedAt)
		if evidence != "" {
			content = append(content, wrapText("    evidence: "+safeIntentText(evidence), inner)...)
		}
	}
	content = append(content, "", "Details")
	if len(resource.Fields) == 0 {
		content = append(content, "No detail fields were returned.")
	} else {
		for _, field := range resource.Fields {
			content = append(content, wrappedField(field.Label, field.Value, inner)...)
		}
	}
	if route.status != "" {
		content = append(content, "", fit(route.status, inner))
	}
	available := max(1, m.height-2-len(header)-1)
	maxScroll := max(0, len(content)-available)
	scroll := min(route.scroll, maxScroll)
	end := min(len(content), scroll+available)
	lines := append(header, content[scroll:end]...)
	footer := "↑↓ relation · enter · pg↑↓ scroll · esc"
	lines = append(lines, fit(footer, inner))
	return frameView(lines, m.width, m.height)
}

func contextLine(config Config, context *AWSContext) string {
	profile, region := config.Profile, config.Region
	if profile == "" {
		profile = "ambient"
	}
	if region == "" {
		region = "unresolved"
	}
	if context == nil || context.Validate() != nil {
		return fmt.Sprintf("Profile %s  Account unresolved  Principal unresolved  %s", safeIntentText(profile), safeIntentText(region))
	}
	principal := context.RoleName
	if principal == "" {
		principal = context.PrincipalARN
	}
	profile = context.Profile
	if profile == "" {
		profile = "ambient"
	}
	return fmt.Sprintf("%s/%s  %s  %s", safeIntentText(context.AccountID), safeIntentText(profile), safeIntentText(principal), safeIntentText(context.Region))
}

func wrappedField(label, value string, width int) []string {
	label = safeIntentText(label)
	value = safeIntentText(value)
	prefix := padRight(label, min(18, max(10, width/3))) + " "
	available := max(1, width-ansi.StringWidth(prefix))
	parts := wrapText(value, available)
	lines := make([]string, len(parts))
	for index, part := range parts {
		if index == 0 {
			lines[index] = prefix + part
		} else {
			lines[index] = strings.Repeat(" ", ansi.StringWidth(prefix)) + part
		}
	}
	return lines
}

func wrapText(value string, width int) []string {
	if value == "" {
		return []string{""}
	}
	var lines []string
	for value != "" {
		if ansi.StringWidth(value) <= width {
			lines = append(lines, value)
			break
		}
		part := ansi.Truncate(value, width, "")
		if part == "" {
			break
		}
		lines = append(lines, part)
		value = strings.TrimPrefix(value, part)
	}
	return lines
}

func renderHelp(width, height int) string {
	lines := []string{
		"AWS Browser help · READ ONLY", "",
		"↑↓ / j k       move selection",
		"enter          open resource or relation",
		"pgup / pgdown  scroll long detail",
		"ctrl+g         cross-profile search",
		"ctrl+r         refresh current list",
		"esc            cancel and go back; exit Home",
		"ctrl+c         quit and cancel requests", "",
		"? or esc close help",
	}
	return frameView(lines, width, height)
}

func frameView(lines []string, width, height int) string {
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
