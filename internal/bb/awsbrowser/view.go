package awsbrowser

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type browserStyles struct {
	border, title, badge, breadcrumb, context, prompt, query, selected, tableHeader lipgloss.Style
	muted, section, footer, ready, warning, failure, normal                         lipgloss.Style
	noColor                                                                         bool
}

func newBrowserStyles(noColor, dark bool) browserStyles {
	styles := browserStyles{noColor: noColor}
	if noColor {
		return styles
	}
	accent := lipgloss.Color("#5B50D6")
	accentSoft := lipgloss.Color("#E9E7FF")
	selectedText := lipgloss.Color("#2D245C")
	cyan := lipgloss.Color("#006D77")
	muted := lipgloss.Color("#626262")
	border := lipgloss.Color("#AAA2CF")
	green := lipgloss.Color("#207A3B")
	amber := lipgloss.Color("#9A5B00")
	red := lipgloss.Color("#B42318")
	badgeText := lipgloss.Color("#FFFFFF")
	badgeBackground := lipgloss.Color("#B45309")
	if dark {
		accent = lipgloss.Color("#C4B5FD")
		accentSoft = lipgloss.Color("#3B3158")
		selectedText = lipgloss.Color("#F5F3FF")
		cyan = lipgloss.Color("#67E8F9")
		muted = lipgloss.Color("#A3A3A3")
		border = lipgloss.Color("#6D5D8F")
		green = lipgloss.Color("#6EE7B7")
		amber = lipgloss.Color("#FCD34D")
		red = lipgloss.Color("#FDA4AF")
		badgeText = lipgloss.Color("#1C1300")
		badgeBackground = lipgloss.Color("#F59E0B")
	}
	styles.border = lipgloss.NewStyle().Foreground(border)
	styles.title = lipgloss.NewStyle().Bold(true).Foreground(accent)
	styles.badge = lipgloss.NewStyle().Bold(true).Foreground(badgeText).Background(badgeBackground)
	styles.breadcrumb = lipgloss.NewStyle().Bold(true).Foreground(cyan)
	styles.context = lipgloss.NewStyle().Foreground(muted)
	styles.prompt = lipgloss.NewStyle().Bold(true).Foreground(cyan)
	styles.query = lipgloss.NewStyle().Bold(true).Foreground(accent)
	styles.selected = lipgloss.NewStyle().Bold(true).Foreground(selectedText).Background(accentSoft)
	styles.tableHeader = lipgloss.NewStyle().Bold(true).Foreground(cyan)
	styles.muted = lipgloss.NewStyle().Foreground(muted)
	styles.section = lipgloss.NewStyle().Bold(true).Foreground(cyan)
	styles.footer = lipgloss.NewStyle().Foreground(muted)
	styles.ready = lipgloss.NewStyle().Bold(true).Foreground(green)
	styles.warning = lipgloss.NewStyle().Bold(true).Foreground(amber)
	styles.failure = lipgloss.NewStyle().Bold(true).Foreground(red)
	styles.normal = lipgloss.NewStyle()
	return styles
}

func renderModel(m Model) string {
	if m.width < MinimumWidth || m.height < MinimumHeight {
		return MinimumSizeMessage
	}
	var view string
	if m.help {
		view = renderHelp(m)
	} else {
		frame := m.current()
		switch {
		case frame == nil:
			view = renderHome(m)
		case frame.mode == routeContext:
			view = renderContext(m, *frame)
		case frame.mode == routeSearch:
			view = renderSearch(m, *frame)
		case frame.mode == routeDetail:
			view = renderSummary(m, *frame)
		case frame.mode == routeFields:
			view = renderDetail(m, *frame)
		case frame.mode == routeRelations:
			view = renderRelationGroup(m, *frame)
		case frame.mode == routeTags:
			view = renderTags(m, *frame)
		default:
			view = renderList(m, *frame)
		}
	}
	if m.commandActive {
		return overlayCommandLine(view, m)
	}
	return view
}

func browserHeader(styles browserStyles) string {
	if styles.noColor {
		return "AWS Browser · READ ONLY"
	}
	return styles.title.Render("AWS Browser") + " · " + styles.badge.Render("READ ONLY")
}

func browserBreadcrumb(styles browserStyles, value string) string {
	return styles.breadcrumb.Render("AWS > " + safeIntentText(value))
}

func browserSearchLine(styles browserStyles, query, placeholder string) string {
	value := query
	style := styles.query
	if value == "" {
		value = placeholder
		style = styles.muted
	}
	return styles.prompt.Render("Search") + "  " + style.Render(safeIntentText(value))
}

func browserFilterLine(styles browserStyles, query, placeholder string, active bool) string {
	if !active {
		return browserSearchLine(styles, query, placeholder)
	}
	value := query
	style := styles.query
	if value == "" {
		value = placeholder
		style = styles.muted
	}
	return styles.prompt.Render("/") + "  " + style.Render(safeIntentText(value))
}

func browserStatus(styles browserStyles, value string) string {
	trimmed := strings.TrimSpace(value)
	switch {
	case strings.HasPrefix(trimmed, "!") || strings.Contains(strings.ToLower(trimmed), "failed"):
		return styles.failure.Render(value)
	case strings.HasPrefix(trimmed, "Ready") || strings.HasPrefix(trimmed, "Verified"):
		return styles.ready.Render(value)
	case strings.Contains(trimmed, "Loading") || strings.Contains(trimmed, "refresh") || strings.Contains(trimmed, "Queued") || strings.Contains(trimmed, "Stale"):
		return styles.warning.Render(value)
	default:
		return styles.context.Render(value)
	}
}

func tableHeader(styles browserStyles, headers []string, widths []int, inner int) string {
	return styles.tableHeader.Render(fit("  "+tableCells(headers, widths), inner))
}

func tableRow(styles browserStyles, selected bool, cells []string, widths []int, inner int) string {
	marker := "  "
	if selected {
		marker = "> "
	}
	row := padRight(fit(marker+tableCells(cells, widths), inner), inner)
	if selected {
		return styles.selected.Render(row)
	}
	return row
}

func tableCells(cells []string, widths []int) string {
	parts := make([]string, 0, min(len(cells), len(widths)))
	for index := 0; index < len(cells) && index < len(widths); index++ {
		value := fit(safeIntentText(cells[index]), max(1, widths[index]))
		parts = append(parts, padRight(value, max(1, widths[index])))
	}
	return strings.TrimRight(strings.Join(parts, "  "), " ")
}

func expandedTableWidths(contentWidth int, widths []int, flexible int) []int {
	result := append([]int(nil), widths...)
	used := max(0, len(result)-1) * 2
	for _, width := range result {
		used += width
	}
	if flexible >= 0 && flexible < len(result) {
		result[flexible] = max(4, result[flexible]+contentWidth-used)
	}
	return result
}

func tableDirection(value string) string {
	switch value {
	case string(RelationOutgoing):
		return "→"
	case string(RelationIncoming):
		return "←"
	default:
		return "·"
	}
}

func renderContext(m Model, route routeFrame) string {
	styles := newBrowserStyles(m.config.NoColor, m.dark)
	inner := max(1, m.width-4)
	lines := []string{
		browserHeader(styles),
		browserBreadcrumb(styles, "Select AWS context"),
		styles.context.Render("Account follows verified profile credentials"),
	}
	lines = append(lines, fit(browserSearchLine(styles, route.contextQuery, "type to filter profiles"), inner))
	if m.height >= 16 {
		lines = append(lines, "")
	}
	choices := filteredContextChoices(route)
	headers, widths := contextTableLayout(inner)
	lines = append(lines, tableHeader(styles, headers, widths, inner))
	reservedRows := 14
	if route.verifiedContext != nil {
		reservedRows += 2
	}
	available := max(1, m.height-reservedRows)
	start := max(0, route.contextSelected-available/2)
	end := min(len(choices), start+available)
	if end-start < available {
		start = max(0, end-available)
	}
	for index := start; index < end; index++ {
		choice := choices[index]
		region := choice.Region
		if region == "" {
			region = "unconfigured"
		}
		group := choice.Group
		if group == "" {
			group = "-"
		}
		regionCount := len(choice.Regions)
		if regionCount == 0 && choice.Region != "" {
			regionCount = 1
		}
		scope := fmt.Sprintf("%d region", regionCount)
		if regionCount != 1 {
			scope += "s"
		}
		cells := []string{choice.Profile, region, group, scope}
		lines = append(lines, tableRow(styles, route.contextFocus == 0 && index == route.contextSelected, cells[:len(headers)], widths, inner))
	}
	if len(route.contextChoices) == 0 && !route.contextLoading {
		lines = append(lines, styles.muted.Render("No configured profiles"))
	} else if len(choices) == 0 {
		lines = append(lines, styles.warning.Render("No matches for “"+safeIntentText(route.contextQuery)+"”"))
	}
	regionMarker := "  "
	if route.contextFocus == 1 {
		regionMarker = "> "
	}
	region := route.contextRegion
	if region == "" {
		region = "type region, for example ap-northeast-2"
	}
	if m.height >= 16 {
		lines = append(lines, "")
	}
	regionLine := regionMarker + styles.prompt.Render("Region") + "  " + styles.query.Render(safeIntentText(region))
	lines = append(lines, fit(regionLine, inner))
	scopeMarker := "  "
	if route.contextFocus == 2 {
		scopeMarker = "> "
	}
	scope := "Current region"
	if route.contextAllRegions && len(route.contextRegions) > 1 {
		group := "configured"
		choices := filteredContextChoices(route)
		if len(choices) != 0 && choices[route.contextSelected].Group != "" {
			group = choices[route.contextSelected].Group
		}
		scope = fmt.Sprintf("All %s regions (%d)", safeIntentText(group), len(route.contextRegions))
	}
	if len(route.contextRegions) < 2 {
		scope += " · no region group"
	}
	lines = append(lines, fit(scopeMarker+styles.prompt.Render("Scope")+"   "+styles.query.Render(scope), inner))
	if route.verifiedContext != nil && route.verifiedContext.Validate() == nil {
		principal := route.verifiedContext.RoleName
		if principal == "" {
			principal = route.verifiedContext.PrincipalARN
		}
		lines = append(lines,
			fit(styles.ready.Render("Account  "+safeIntentText(route.verifiedContext.AccountID)), inner),
			fit(styles.context.Render("Principal  "+safeIntentText(principal)), inner),
		)
	}
	if route.status != "" {
		if m.height >= 16 {
			lines = append(lines, "")
		}
		lines = append(lines, fit(browserStatus(styles, route.status), inner))
	}
	footer := "type search · tab field · ←→ change · enter verify/apply · esc clear/back"
	if route.contextStartup {
		footer = "type search · tab field · ←→ change · enter verify/apply · esc ambient home"
	}
	if route.verifiedContext != nil {
		footer = "enter apply · tab/edit to change · esc keep previous"
	}
	if m.width < 60 {
		footer = "type search · ↑↓ profile · tab region\nenter verify · esc clear/back"
		if route.contextStartup {
			footer = "type search · ↑↓ profile · tab region\nenter verify · esc ambient home"
		}
		if route.verifiedContext != nil {
			footer = "enter apply · tab/edit change\nesc keep previous"
		}
	}
	if m.height >= 16 {
		lines = append(lines, "")
	}
	for _, line := range strings.Split(footer, "\n") {
		lines = append(lines, fit(styles.footer.Render(line), inner))
	}
	return frameView(lines, m.width, m.height, styles)
}

func renderSearch(m Model, route routeFrame) string {
	styles := newBrowserStyles(m.config.NoColor, m.dark)
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
		browserHeader(styles),
		browserBreadcrumb(styles, "Cross-profile search"),
		styles.context.Render("Local editor · no AWS request until submit"),
		"",
		fit(marker(0)+styles.prompt.Render("Kind")+"   < "+styles.query.Render(searchChoiceLabel(searchKinds[route.searchKind]))+" >", inner),
		fit(marker(1)+styles.prompt.Render("Value")+"  "+styles.query.Render(safeIntentText(value)), inner),
		fit(marker(2)+styles.prompt.Render("Scope")+"  < "+styles.query.Render(searchChoiceLabel(searchScopes[route.searchScope]))+" >", inner),
	}
	if route.status != "" {
		lines = append(lines, "", fit(browserStatus(styles, route.status), inner))
	}
	lines = append(lines, "", styles.footer.Render("tab field · ←→ choose · enter submit · esc back"))
	return frameView(lines, m.width, m.height, styles)
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
	styles := newBrowserStyles(m.config.NoColor, m.dark)
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
		fit(browserHeader(styles), inner),
		fit(styles.context.Render(contextLine(Config{Profile: profile, Region: region}, m.activeContext)), inner),
		fit(styles.section.Render("Services / tasks")+styles.muted.Render(" · LIST"), inner),
	}
	headers, widths := homeTableLayout(inner)
	lines = append(lines, tableHeader(styles, headers, widths, inner))
	footer := "↑↓ move  →/enter open  : command  c context  ctrl+g search  ? help"
	if m.width < 60 {
		footer = "↑↓ move · →/enter open · : command\nc context · ctrl+g search · ? help"
	}
	footerLines := strings.Split(footer, "\n")
	available := max(1, m.height-2-len(lines)-len(footerLines))
	start := max(0, m.selected-available+1)
	end := min(len(homeCatalog), start+available)
	for i := start; i < end; i++ {
		item := homeCatalog[i]
		lines = append(lines, tableRow(styles, i == m.selected, homeTableCells(headers, item), widths, inner))
	}
	for _, line := range footerLines {
		lines = append(lines, styles.footer.Render(line))
	}
	return frameView(lines, m.width, m.height, styles)
}

func renderList(m Model, route routeFrame) string {
	styles := newBrowserStyles(m.config.NoColor, m.dark)
	inner := max(1, m.width-4)
	lines := []string{
		browserHeader(styles),
		fit(styles.context.Render(contextLine(m.config, route.context)), inner),
		fit(browserBreadcrumb(styles, route.label), inner),
		fit(browserStatus(styles, route.status), inner),
	}
	if route.coverage != nil {
		if route.coverage.DiscoveryStatus != "" {
			lines = append(lines, fit(styles.section.Render("Profile discovery")+styles.context.Render(" · "+safeIntentText(route.coverage.DiscoveryStatus)), inner))
		}
		coverageHeaders, coverageWidths := coverageTableLayout(inner)
		if len(route.coverage.Profiles) != 0 {
			lines = append(lines, tableHeader(styles, coverageHeaders, coverageWidths, inner))
		}
		for _, profile := range route.coverage.Profiles {
			name := profile.Profile
			if name == "" {
				name = "ambient"
			}
			marker := "profile"
			if profile.Current {
				marker = "current"
			}
			account := profile.AccountID
			if account == "" {
				account = "unresolved"
			}
			region := profile.Region
			if region == "" {
				region = "-"
			}
			cells := coverageTableCells(coverageHeaders, name, account, region, profile.Status, marker, profile.Matches)
			lines = append(lines, styles.context.Render(tableRow(styles, false, cells, coverageWidths, inner)))
		}
		lines = append(lines, "")
	}
	if route.graph != nil && !route.graph.Collecting && !route.graph.CompletedAt.IsZero() {
		cache := "new collection"
		if route.graph.Reused {
			cache = "cached"
		}
		lines = append(lines,
			fit(styles.section.Render("Source")+styles.context.Render(" · SNAPSHOT · group "+safeIntentText(route.graph.Group)+" · "+cache), inner),
			fit(styles.context.Render(fmt.Sprintf("Coverage · %d succeeded · %d failed · %d not observed", route.graph.Succeeded, route.graph.Failed, route.graph.NotObserved)), inner),
			"",
		)
	}
	lines = append(lines, fit(browserFilterLine(styles, route.filterValue, "type to filter loaded resources", route.filterActive), inner))
	if m.height >= 16 {
		lines = append(lines, "")
	}
	resources := filteredResources(route)
	if len(route.projection.Resources) == 0 {
		empty := "No resource result has been loaded."
		if route.graph != nil && !route.graph.Collecting && !route.graph.Error {
			empty = "No incoming relations were observed."
			if route.graph.Failed != 0 || route.graph.NotObserved != 0 {
				empty += " Coverage is incomplete."
			}
		}
		lines = append(lines, styles.muted.Render(empty))
	} else if len(resources) == 0 {
		lines = append(lines, styles.warning.Render("No matches for “"+safeIntentText(route.filterValue)+"”"))
	} else {
		headers, widths := resourceTableLayout(inner)
		available := max(1, m.height-2-len(lines)-3)
		start := max(0, route.selected-available+1)
		end := min(len(resources), start+available)
		count := fmt.Sprintf("Resources (%d)", len(resources))
		if route.filterValue != "" {
			count = fmt.Sprintf("Resources (%d/%d)", len(resources), len(route.projection.Resources))
		}
		lines = append(lines, styles.section.Render(count)+styles.muted.Render(fmt.Sprintf(" · rows %d-%d", start+1, end)))
		lines = append(lines, tableHeader(styles, headers, widths, inner))
		for index := start; index < end; index++ {
			resource := resources[index]
			lines = append(lines, tableRow(styles, index == route.selected, resourceTableCells(resource, headers), widths, inner))
		}
	}
	if m.height >= 16 {
		lines = append(lines, "")
	}
	lines = append(lines, styles.footer.Render("type or / filter  ↑↓ move  →/enter detail  ← back  : command  ^o/^i history"))
	return frameView(lines, m.width, min(m.height, max(14, len(lines)+2)), styles)
}

func renderSummary(m Model, route routeFrame) string {
	styles := newBrowserStyles(m.config.NoColor, m.dark)
	inner := max(1, m.width-4)
	resource := route.detail
	header := []string{
		browserHeader(styles),
		fit(styles.context.Render(contextLine(m.config, route.context)), inner),
		fit(browserBreadcrumb(styles, resource.Title+" > Summary"), inner),
	}
	content := []string{}
	if resource.Subtitle != "" {
		content = append(content, fit(styles.context.Render(safeIntentText(resource.Subtitle)), inner))
		if m.height >= 16 {
			content = append(content, "")
		}
	}
	categories := detailCategories(resource)
	content = append(content, styles.section.Render(fmt.Sprintf("Categories (%d)", len(categories))))
	if len(categories) == 0 {
		content = append(content, styles.muted.Render("  No categories."))
	}
	categoryHeaders, categoryWidths := categoryTableLayout(inner)
	if len(categories) != 0 {
		content = append(content, tableHeader(styles, categoryHeaders, categoryWidths, inner))
	}
	for index, category := range categories {
		action := "OPEN"
		if category.Key == "incoming-relations" {
			action = "AUTO"
		} else if category.Key == "detail" {
			action = "VIEW"
		} else if category.Key == "tags" {
			action = "VIEW"
		} else if category.Key == "evidence" {
			action = "EVIDENCE"
		} else if directRelationGroup(category.Group) {
			action = "LIST"
			if category.Key == "policy-document" {
				action = "VIEW"
			} else if category.Key == "alias-targets" {
				action = "TRACE"
			}
		}
		content = append(content, tableRow(styles, index == route.relationSelected,
			categoryTableCells(categoryHeaders, category.Label, category.Count, action), categoryWidths, inner))
	}
	content = append(content, "", styles.section.Render("Summary"))
	fields := summaryFields(resource)
	if len(fields) == 0 {
		content = append(content, styles.muted.Render("No summary fields were returned."))
	} else {
		fieldHeaders, fieldWidths := fieldTableLayout(inner)
		content = append(content, tableHeader(styles, fieldHeaders, fieldWidths, inner))
		for _, field := range fields {
			content = append(content, tableRow(styles, false, []string{field.Label, field.Value}, fieldWidths, inner))
		}
	}
	if route.status != "" {
		content = append(content, "", fit(browserStatus(styles, route.status), inner))
	}
	available := max(1, m.height-2-len(header)-1)
	end := min(len(content), available)
	lines := append(header, content[:end]...)
	footer := "↑↓ category · →/enter open · ← back · : command · ^o/^i history"
	lines = append(lines, fit(styles.footer.Render(footer), inner))
	return frameView(lines, m.width, m.height, styles)
}

func summaryFields(resource ResourceProjection) []ProjectionField {
	const limit = 6
	priority := []string{
		"Name", "Id", "State", "Status", "Type", "Instance Type", "Description",
		"VPC Id", "Subnet Id", "CIDR Block", "Availability Zone", "Private IP", "Public IP",
		"Volume Type", "Size", "Size GiB", "Encrypted", "IOPS", "Throughput", "Create Time",
	}
	result := make([]ProjectionField, 0, min(limit, len(resource.Fields)))
	used := make(map[int]bool, len(resource.Fields))
	for _, label := range priority {
		for index, field := range resource.Fields {
			if !used[index] && strings.EqualFold(field.Label, label) {
				result = append(result, field)
				used[index] = true
				break
			}
		}
		if len(result) == limit {
			return result
		}
	}
	for index, field := range resource.Fields {
		value := strings.TrimSpace(field.Value)
		if used[index] || value == "" || len([]rune(value)) > 120 || strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[") {
			continue
		}
		result = append(result, field)
		if len(result) == limit {
			break
		}
	}
	return result
}

func renderDetail(m Model, route routeFrame) string {
	styles := newBrowserStyles(m.config.NoColor, m.dark)
	inner := max(1, m.width-4)
	resource := route.detail
	header := []string{
		browserHeader(styles),
		fit(styles.context.Render(contextLine(m.config, route.context)), inner),
		fit(browserBreadcrumb(styles, resource.Title+" > Detail"), inner),
	}
	content := []string{}
	if resource.Subtitle != "" {
		content = append(content, fit(styles.context.Render(safeIntentText(resource.Subtitle)), inner), "")
	}
	if resource.Context != nil && resource.Context.Validate() == nil {
		profile := resource.Context.Profile
		if profile == "" {
			profile = "ambient"
		}
		current := "no"
		if resource.Current {
			current = "yes"
		}
		available := make([]string, len(resource.AvailableViaProfiles))
		for index, value := range resource.AvailableViaProfiles {
			if value == "" {
				value = "ambient"
			}
			available[index] = safeIntentText(value)
		}
		if len(available) == 0 {
			available = []string{safeIntentText(profile)}
		}
		content = append(content, styles.section.Render("Provenance"),
			fit("  Account "+safeIntentText(resource.Context.AccountID), inner),
			fit("  Principal "+safeIntentText(resource.Context.PrincipalARN), inner),
			fit("  Profile "+safeIntentText(profile)+" · current "+current, inner),
			fit("  Region "+safeIntentText(resource.Context.Region), inner),
			fit("  Available via "+strings.Join(available, ", "), inner), "")
	}
	content = append(content, styles.section.Render("Fields"))
	if len(resource.Fields) == 0 {
		content = append(content, "No detail fields were returned.")
	} else {
		for _, field := range resource.Fields {
			content = append(content, wrappedField(field.Label, expandedFieldValue(field.Value), inner)...)
		}
	}
	if route.status != "" {
		content = append(content, "", fit(browserStatus(styles, route.status), inner))
	}
	available := max(1, m.height-2-len(header)-1)
	maxScroll := max(0, len(content)-available)
	scroll := min(route.scroll, maxScroll)
	end := min(len(content), scroll+available)
	lines := append(header, content[scroll:end]...)
	footer := "↑↓/pg↑↓ scroll · ← back · : command · ^o/^i history"
	lines = append(lines, fit(styles.footer.Render(footer), inner))
	return frameView(lines, m.width, m.height, styles)
}

func expandedFieldValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed[0] != '{' && trimmed[0] != '[' {
		return value
	}
	var document any
	if err := json.Unmarshal([]byte(trimmed), &document); err != nil {
		return value
	}
	formatted, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return value
	}
	return string(formatted)
}

func renderTags(m Model, route routeFrame) string {
	styles := newBrowserStyles(m.config.NoColor, m.dark)
	inner := max(1, m.width-4)
	tags := filteredTags(route)
	lines := []string{
		browserHeader(styles),
		fit(styles.context.Render(contextLine(m.config, route.context)), inner),
		fit(browserBreadcrumb(styles, route.detail.Title+" > Tags"), inner),
	}
	if route.status != "" {
		lines = append(lines, fit(browserStatus(styles, route.status), inner))
	}
	if m.height >= 16 {
		lines = append(lines, "")
	}
	lines = append(lines, fit(browserFilterLine(styles, route.filterValue, "type to filter tags", route.filterActive), inner))
	count := fmt.Sprintf("Tags (%d)", len(tags))
	if route.filterValue != "" {
		count = fmt.Sprintf("Tags (%d/%d)", len(tags), len(route.detail.Tags))
	}
	lines = append(lines, styles.section.Render(count))
	if len(route.detail.Tags) == 0 {
		lines = append(lines, styles.muted.Render("No tags."))
	} else if len(tags) == 0 {
		lines = append(lines, styles.warning.Render("No matches for “"+safeIntentText(route.filterValue)+"”"))
	} else {
		headers, widths := tagTableLayout(inner)
		lines = append(lines, tableHeader(styles, headers, widths, inner))
		preview := []string{}
		selected := tags[route.selected]
		if m.height >= 18 {
			preview = append(preview, "", styles.section.Render("Tag value"))
			preview = append(preview, wrappedField(selected.Key, selected.Value, inner)...)
		}
		available := max(1, m.height-2-len(lines)-len(preview)-2)
		start := max(0, route.selected-available+1)
		end := min(len(tags), start+available)
		for index := start; index < end; index++ {
			tag := tags[index]
			lines = append(lines, tableRow(styles, index == route.selected, []string{tag.Key, tag.Value}, widths, inner))
		}
		lines = append(lines, preview...)
	}
	if m.height >= 16 {
		lines = append(lines, "")
	}
	lines = append(lines, fit(styles.footer.Render("type or / filter · ↑↓ move · ← back · : command · ^o/^i history"), inner))
	return frameView(lines, m.width, m.height, styles)
}

func renderRelationGroup(m Model, route routeFrame) string {
	styles := newBrowserStyles(m.config.NoColor, m.dark)
	inner := max(1, m.width-4)
	allRelations := relationsForGroup(route)
	relations := filteredRelations(route)
	lines := []string{
		browserHeader(styles),
		fit(styles.context.Render(contextLine(m.config, route.context)), inner),
		fit(browserBreadcrumb(styles, route.detail.Title+" > "+route.label), inner),
	}
	if route.status != "" {
		lines = append(lines, fit(browserStatus(styles, route.status), inner))
	}
	if m.height >= 16 {
		lines = append(lines, "")
	}
	lines = append(lines, fit(browserFilterLine(styles, route.filterValue, "type to filter related resources", route.filterActive), inner))
	count := fmt.Sprintf("%s (%d)", route.label, len(relations))
	if route.filterValue != "" {
		count = fmt.Sprintf("%s (%d/%d)", route.label, len(relations), len(allRelations))
	}
	lines = append(lines, styles.section.Render(count))
	if len(allRelations) == 0 {
		lines = append(lines, styles.muted.Render("No related resources."))
	} else if len(relations) == 0 {
		lines = append(lines, styles.warning.Render("No matches for “"+safeIntentText(route.filterValue)+"”"))
	} else {
		headers, widths := relationTableLayout(inner)
		lines = append(lines, tableHeader(styles, headers, widths, inner))
		preview := []string{}
		selected := relations[route.relationSelected]
		if m.height >= 18 {
			meaning := stringsJoinNonEmpty(selected.Type, selected.Direction)
			if selected.Condition != "" {
				meaning = stringsJoinNonEmpty(meaning, "condition "+selected.Condition)
			}
			evidence := stringsJoinNonEmpty(selected.Kind, selected.Scope, selected.Operation, selected.ObservedAt)
			if meaning != "" || selected.Reason != "" || evidence != "" {
				preview = append(preview, "", styles.section.Render("Relationship evidence"))
				if meaning != "" {
					preview = append(preview, wrapText("  "+safeIntentText(meaning), inner)...)
				}
				if selected.Reason != "" {
					preview = append(preview, wrapText("  "+safeIntentText(selected.Reason), inner)...)
				}
				if evidence != "" {
					preview = append(preview, wrapText("  "+safeIntentText(evidence), inner)...)
				}
			}
		}
		available := max(1, m.height-2-len(lines)-len(preview)-2)
		start := max(0, route.relationSelected-available+1)
		end := min(len(relations), start+available)
		for index := start; index < end; index++ {
			relation := relations[index]
			lines = append(lines, tableRow(styles, index == route.relationSelected, relationTableCells(relation, headers), widths, inner))
		}
		lines = append(lines, preview...)
	}
	if m.height >= 16 {
		lines = append(lines, "")
	}
	openAction := "→/enter target"
	if len(relations) != 0 && relations[route.relationSelected].Target == "" {
		openAction = "→/enter evidence"
	}
	lines = append(lines, fit(styles.footer.Render("type or / filter · ↑↓ move · "+openAction+" · e evidence · ← back · : command · ^o/^i history"), inner))
	return frameView(lines, m.width, m.height, styles)
}

func contextLine(config Config, context *AWSContext) string {
	profile, region := config.Profile, config.Region
	if profile == "" {
		profile = "ambient"
	}
	if region == "" {
		region = "unresolved"
	}
	scope := region
	if regions, err := ParseRegionSet(config.Regions, config.Region); err == nil && len(regions) > 1 {
		scope = fmt.Sprintf("%d regions · current %s", len(regions), region)
	}
	if context == nil || context.Validate() != nil {
		return fmt.Sprintf("Profile %s  Account unresolved  Principal unresolved  %s", safeIntentText(profile), safeIntentText(scope))
	}
	principal := context.RoleName
	if principal == "" {
		principal = context.PrincipalARN
	}
	profile = context.Profile
	if profile == "" {
		profile = "ambient"
	}
	return fmt.Sprintf("Profile %s  Account %s  Principal %s  %s", safeIntentText(profile), safeIntentText(context.AccountID), safeIntentText(principal), safeIntentText(scope))
}

func wrappedField(label, value string, width int) []string {
	label = safeIntentText(label)
	prefix := padRight(label, min(18, max(10, width/3))) + " "
	available := max(1, width-ansi.StringWidth(prefix))
	parts := make([]string, 0, 1)
	for _, line := range strings.Split(value, "\n") {
		parts = append(parts, wrapText(safeIntentText(line), available)...)
	}
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

func homeTableLayout(inner int) ([]string, []int) {
	content := max(1, inner-2)
	if inner < 70 {
		return []string{"RESOURCE", "STATUS"}, expandedTableWidths(content, []int{18, 14}, 0)
	}
	return []string{"RESOURCE", "ALIAS", "STATUS"}, expandedTableWidths(content, []int{30, 24, 18}, 2)
}

func homeTableCells(headers []string, item catalogItem) []string {
	values := map[string]string{"RESOURCE": item.Label, "ALIAS": item.ID, "STATUS": item.Status}
	return tableValues(headers, values)
}

func contextTableLayout(inner int) ([]string, []int) {
	content := max(1, inner-2)
	switch {
	case inner >= 90:
		return []string{"PROFILE", "REGION", "GROUP", "SCOPE"}, expandedTableWidths(content, []int{24, 18, 20, 14}, 0)
	case inner >= 60:
		return []string{"PROFILE", "REGION", "GROUP"}, expandedTableWidths(content, []int{24, 18, 18}, 0)
	default:
		return []string{"PROFILE", "REGION"}, expandedTableWidths(content, []int{16, 16}, 0)
	}
}

func coverageTableLayout(inner int) ([]string, []int) {
	content := max(1, inner-2)
	switch {
	case inner >= 100:
		return []string{"PROFILE", "ACCOUNT", "REGION", "STATUS", "SCOPE", "MATCHES"}, expandedTableWidths(content, []int{18, 12, 14, 14, 9, 7}, 0)
	case inner >= 72:
		return []string{"PROFILE", "ACCOUNT", "STATUS", "MATCHES"}, expandedTableWidths(content, []int{20, 12, 14, 7}, 0)
	default:
		return []string{"PROFILE", "STATUS"}, expandedTableWidths(content, []int{16, 16}, 0)
	}
}

func coverageTableCells(headers []string, profile, account, region, status, scope string, matches int) []string {
	return tableValues(headers, map[string]string{
		"PROFILE": profile, "ACCOUNT": account, "REGION": region, "STATUS": status,
		"SCOPE": scope, "MATCHES": fmt.Sprintf("%d", matches),
	})
}

func resourceTableLayout(inner int) ([]string, []int) {
	content := max(1, inner-2)
	switch {
	case inner >= 110:
		return []string{"NAME", "TYPE", "ID", "STATUS", "ACCOUNT", "REGION"}, expandedTableWidths(content, []int{22, 18, 18, 10, 12, 12}, 0)
	case inner >= 72:
		return []string{"NAME", "TYPE", "ID", "STATUS"}, expandedTableWidths(content, []int{22, 16, 20, 10}, 0)
	case inner >= 46:
		return []string{"NAME", "TYPE", "ID"}, expandedTableWidths(content, []int{20, 14, 14}, 0)
	default:
		return []string{"NAME", "ID"}, expandedTableWidths(content, []int{16, 16}, 0)
	}
}

func resourceTableCells(resource ResourceProjection, headers []string) []string {
	resourceType, resourceID, _ := strings.Cut(resource.Target, ":")
	displayType := resourceType
	if resourceType == "elbv2.load-balancer" {
		switch projectionFieldValue(resource.Fields, "Type") {
		case "application":
			displayType = "ALB"
		case "network":
			displayType = "NLB"
		}
	}
	status := projectionFieldValue(resource.Fields, "State", "Status")
	if status == "-" {
		status = strings.TrimSpace(resource.Subtitle)
		status = strings.TrimPrefix(status, resourceID+" · ")
		if status == "" || status == resourceID {
			status = "-"
		}
	}
	account, region := "-", "-"
	if resource.Context != nil && resource.Context.Validate() == nil {
		account, region = resource.Context.AccountID, resource.Context.Region
	}
	return tableValues(headers, map[string]string{
		"NAME": resource.Title, "TYPE": displayType, "ID": resourceID,
		"STATUS": status, "ACCOUNT": account, "REGION": region,
	})
}

func projectionFieldValue(fields []ProjectionField, labels ...string) string {
	for _, label := range labels {
		for _, field := range fields {
			if strings.EqualFold(field.Label, label) && strings.TrimSpace(field.Value) != "" {
				return field.Value
			}
		}
	}
	return "-"
}

func categoryTableLayout(inner int) ([]string, []int) {
	content := max(1, inner-2)
	return []string{"CATEGORY", "COUNT", "ACTION"}, expandedTableWidths(content, []int{18, 5, 7}, 0)
}

func categoryTableCells(headers []string, label string, count int, action string) []string {
	countLabel := fmt.Sprintf("%d", count)
	if count < 0 {
		countLabel = "-"
	}
	return tableValues(headers, map[string]string{"CATEGORY": label, "COUNT": countLabel, "ACTION": action})
}

func fieldTableLayout(inner int) ([]string, []int) {
	content := max(1, inner-2)
	return []string{"FIELD", "VALUE"}, expandedTableWidths(content, []int{18, 14}, 1)
}

func tagTableLayout(inner int) ([]string, []int) {
	content := max(1, inner-2)
	return []string{"KEY", "VALUE"}, expandedTableWidths(content, []int{18, 14}, 1)
}

func relationTableLayout(inner int) ([]string, []int) {
	content := max(1, inner-2)
	switch {
	case inner >= 110:
		return []string{"DIR", "RELATION", "TARGET", "CONDITION", "CONFIDENCE", "SCOPE"}, expandedTableWidths(content, []int{3, 15, 24, 20, 12, 14}, 2)
	case inner >= 72:
		return []string{"DIR", "RELATION", "TARGET", "CONDITION", "CONFIDENCE"}, expandedTableWidths(content, []int{3, 13, 18, 14, 10}, 2)
	case inner >= 54:
		return []string{"DIR", "RELATION", "TARGET", "CONDITION"}, expandedTableWidths(content, []int{3, 12, 12, 12}, 2)
	default:
		return []string{"DIR", "RELATION", "TARGET", "CONDITION"}, expandedTableWidths(content, []int{3, 9, 7, 9}, 2)
	}
}

func relationTableCells(relation ProjectionRelation, headers []string) []string {
	target := relation.Target
	if target == "" {
		target = relation.TargetRef
	}
	if target == "" {
		target = relation.Label
	}
	return tableValues(headers, map[string]string{
		"DIR": tableDirection(relation.Direction), "RELATION": relation.Type, "TARGET": target,
		"CONDITION": relation.Condition, "CONFIDENCE": relation.Kind, "SCOPE": relation.Scope,
	})
}

func tableValues(headers []string, values map[string]string) []string {
	result := make([]string, len(headers))
	for index, header := range headers {
		result[index] = values[header]
		if result[index] == "" {
			result[index] = "-"
		}
	}
	return result
}

func renderHelp(m Model) string {
	styles := newBrowserStyles(m.config.NoColor, m.dark)
	lines := []string{
		browserHeader(styles), styles.section.Render("Keyboard help"), "",
		"↑↓ / j k       move selection",
		"→ / enter      open resource or relation",
		"←              return one browser screen",
		":              resource command line",
		"               ec2 vpc route53 iam elbv2 alb nlb context search home refresh",
		"/              focus local filter",
		"ctrl+o/ctrl+i  browser history back/forward",
		"c              select profile/account/region",
		"pgup / pgdown  scroll long detail",
		"ctrl+g         cross-profile search",
		"ctrl+r         refresh current list",
		"esc            cancel and go back; exit Home",
		"ctrl+c         quit and cancel requests", "",
		styles.footer.Render("? or esc close help"),
	}
	return frameView(lines, m.width, m.height, styles)
}

func frameView(lines []string, width, height int, styles browserStyles) string {
	inner := max(1, width-4)
	contentHeight := max(1, height-2)
	if len(lines) != 0 {
		footer := lines[len(lines)-1]
		body := append([]string(nil), lines[:len(lines)-1]...)
		for len(body) < contentHeight-1 {
			body = append(body, "")
		}
		if len(body) > contentHeight-1 {
			body = body[:contentHeight-1]
		}
		lines = append(body, footer)
	} else {
		lines = make([]string, contentHeight)
	}
	var b strings.Builder
	b.WriteString(styles.border.Render("╭" + strings.Repeat("─", inner+2) + "╮"))
	b.WriteByte('\n')
	for i, line := range lines {
		b.WriteString(styles.border.Render("│") + " " + padRight(fit(line, inner), inner) + " " + styles.border.Render("│"))
		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}
	b.WriteByte('\n')
	b.WriteString(styles.border.Render("╰" + strings.Repeat("─", inner+2) + "╯"))
	return b.String()
}

func overlayCommandLine(view string, m Model) string {
	lines := strings.Split(view, "\n")
	if len(lines) < 3 {
		return view
	}
	styles := newBrowserStyles(m.config.NoColor, m.dark)
	inner := max(1, m.width-4)
	value := m.commandValue
	if value == "" {
		value = "ec2 · vpc · route53 · iam · elbv2 · alb · nlb · context · search · home · refresh"
	}
	command := styles.prompt.Render(":") + styles.query.Render(safeIntentText(value))
	if m.commandStatus != "" {
		command += styles.failure.Render("  " + safeIntentText(m.commandStatus))
	}
	lines[len(lines)-2] = styles.border.Render("│") + " " + padRight(fit(command, inner), inner) + " " + styles.border.Render("│")
	return strings.Join(lines, "\n")
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
