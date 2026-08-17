package ui

import (
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/0xdeafcafe/gack/internal/gack"
)

var (
	purple     = lipgloss.AdaptiveColor{Light: "#5B2C6F", Dark: "#C792EA"}
	deepPurple = lipgloss.AdaptiveColor{Light: "#3F1D4D", Dark: "#6E3A78"}
	muted      = lipgloss.AdaptiveColor{Light: "#666666", Dark: "#8B8B8B"}
	soft       = lipgloss.AdaptiveColor{Light: "#EEEEEE", Dark: "#26232B"}
	warning    = lipgloss.AdaptiveColor{Light: "#9A3412", Dark: "#FFB86C"}
	danger     = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#FF6B6B"}
	green      = lipgloss.AdaptiveColor{Light: "#166534", Dark: "#50FA7B"}
	white      = lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#FFFFFF"}
	ink        = lipgloss.AdaptiveColor{Light: "#2D1734", Dark: "#F4ECF7"}
	selection  = lipgloss.AdaptiveColor{Light: "#E9D7EF", Dark: "#3B2942"}

	headerStyle    = lipgloss.NewStyle().Bold(true).Foreground(white).Background(deepPurple)
	locationStyle  = lipgloss.NewStyle().Foreground(ink).Background(soft)
	footerStyle    = lipgloss.NewStyle().Foreground(muted)
	activeBorder   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(purple)
	inactiveBorder = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(muted)
	floatingBorder = lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).BorderForeground(purple)
	selectedStyle  = lipgloss.NewStyle().Foreground(purple).Bold(true)
	selectedRow    = lipgloss.NewStyle().Foreground(ink).Background(selection)
	selectedNav    = lipgloss.NewStyle().Foreground(white).Background(deepPurple).Bold(true)
	dimStyle       = lipgloss.NewStyle().Foreground(muted)
	errorStyle     = lipgloss.NewStyle().Foreground(danger).Bold(true)
	successStyle   = lipgloss.NewStyle().Foreground(green)

	mentionPattern = regexp.MustCompile(`<@([A-Z0-9_]+)>`)
	channelPattern = regexp.MustCompile(`<#([A-Z0-9_]+)(\|([^>]+))?>`)
	linkPattern    = regexp.MustCompile(`<((https?://|mailto:)[^>|]+)(\|([^>]+))?>`)
	emojiPattern   = regexp.MustCompile(`:([a-zA-Z0-9_+\-]+):`)
)

func (m *Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return "Starting gack…"
	}
	header := m.renderHeader()
	footer := m.renderFooter()
	bodyHeight := max(3, m.height-lipgloss.Height(header)-lipgloss.Height(footer))
	var body string
	switch {
	case m.dialog != nil:
		body = m.renderDialog(m.renderBody(bodyHeight), bodyHeight)
	case m.overlay == overlayGlobalSearch:
		body = m.renderSearch(m.renderBody(bodyHeight), bodyHeight)
	case m.overlay == overlayAction || m.overlay == overlayReaction:
		body = m.renderPicker(m.renderBody(bodyHeight), bodyHeight)
	case m.overlay == overlayHelp:
		body = m.renderHelp(m.renderBody(bodyHeight), bodyHeight)
	default:
		body = m.renderBody(bodyHeight)
	}
	return header + "\n" + body + "\n" + footer
}

func (m *Model) renderHeader() string {
	location := "Starting"
	if !m.ready && m.err != "" {
		location = "Connection problem"
	} else if m.ready {
		switch m.mode {
		case viewActivity:
			location = "Activity"
		case viewNotifications:
			location = "Notifications"
		default:
			if m.channel >= 0 && m.channel < len(m.channels) {
				location = m.channels[m.channel].Label()
			}
		}
	}
	left := " GACK  /  " + m.snapshot.Team
	if m.snapshot.Team == "" {
		left = " GACK"
	}
	if m.width < 64 {
		left = " GACK"
	}
	command := "Ctrl+K  commands "
	if !m.ready {
		command = "q  cancel "
		if m.err != "" {
			command = "R retry  ·  L sign in "
		}
	}
	if m.updateAvailable != "" && m.ready {
		command = "↑ " + m.updateAvailable + "  ·  u update "
	}
	brandLine := joinAcross(left, command, max(1, m.width))
	brandLine = headerStyle.Width(max(0, m.width)).Render(brandLine)

	focus := " YOU ARE HERE  ›  " + location + "  ›  " + m.focusPath()
	locationLine := locationStyle.Width(max(0, m.width)).Render(truncate(focus, max(1, m.width)))
	return brandLine + "\n" + locationLine
}

func (m *Model) renderFooter() string {
	if m.focus == focusComposer && m.overlay == overlayNone && m.dialog == nil {
		return m.renderComposer()
	}
	var value string
	style := footerStyle
	switch {
	case !m.ready && m.err != "":
		value = "RECOVERY · R retry · L sign in again · q quit"
		style = lipgloss.NewStyle().Foreground(warning).Bold(true)
	case !m.ready && m.busy != "":
		value = m.spin.View() + " Connecting to Slack · q cancel"
	case m.dialog != nil:
		value = m.dialogFooter()
	case m.overlay == overlayGlobalSearch:
		value = "COMMAND PALETTE · type to filter · ↑/↓ choose · Enter open · Esc close"
	case m.overlay == overlayAction:
		value = "ACTION PICKER · ↑/↓ choose · Enter apply · Esc close"
	case m.overlay == overlayReaction:
		value = "REACTION PICKER · ↑/↓ choose · Enter toggle · Esc close"
	case m.overlay == overlayHelp:
		value = "HELP · ? or Esc closes"
	case m.overlay == overlayFind:
		value = "FIND · " + m.findInput.View() + "  Enter next · Ctrl+P previous · Esc close"
	case m.err != "":
		value = "Error: " + m.err
		style = errorStyle
	case m.busy != "":
		value = "◌ " + m.busy
	case m.status != "":
		value = m.status
		style = successStyle
	default:
		value = m.contextualHelp()
	}
	return style.Width(max(0, m.width)).Render(truncateANSI(value, max(1, m.width)))
}

func (m *Model) renderComposer() string {
	context := "NEW MESSAGE"
	if m.composeEditTS != "" {
		context = "EDITING MESSAGE"
	} else if m.composeThread != "" {
		context = "REPLYING IN THREAD"
	}
	left := selectedStyle.Render("● COMPOSER") + dimStyle.Render(" · "+context)
	right := dimStyle.Render("Ctrl+S send · Enter newline · Esc cancel")
	heading := joinAcross(left, right, max(1, m.width))
	editor := lipgloss.NewStyle().
		PaddingLeft(2).
		Width(max(1, m.width-2)).
		Render(m.composeInput.View())
	content := heading + "\n" + editor
	return lipgloss.NewStyle().
		Border(lipgloss.ThickBorder(), true, false, false, false).
		BorderForeground(purple).
		Width(max(1, m.width)).
		Render(content)
}

func (m *Model) renderBody(height int) string {
	if !m.ready {
		return m.renderStartup(height)
	}
	sidebarWidth := m.sidebarWidth()
	if sidebarWidth >= m.width {
		return m.renderSidebar(m.width, height)
	}
	sidebar := ""
	if sidebarWidth > 0 {
		sidebar = m.renderSidebar(sidebarWidth, height)
	}
	available := m.width - sidebarWidth
	var content string
	if m.mode == viewActivity || m.mode == viewNotifications {
		content = m.renderActivity(available, height)
	} else if m.threadTS != "" && available >= 72 {
		threadWidth := max(30, available*2/5)
		conversationWidth := available - threadWidth
		content = lipgloss.JoinHorizontal(lipgloss.Top,
			m.renderConversation(conversationWidth, height),
			m.renderThread(threadWidth, height),
		)
	} else if m.threadTS != "" && m.focus == focusThread {
		content = m.renderThread(available, height)
	} else {
		content = m.renderConversation(available, height)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, sidebar, content)
}

func (m *Model) renderStartup(height int) string {
	boxWidth := max(8, min(68, m.width-2))
	inner := max(4, boxWidth-6)
	var lines []string
	if m.err == "" {
		elapsed := ""
		if !m.connectStarted.IsZero() {
			elapsed = fmt.Sprintf("%ds", max(0, int(time.Since(m.connectStarted).Round(time.Second)/time.Second)))
		}
		lines = []string{
			joinAcross(selectedStyle.Render("CONNECTING TO SLACK"), dimStyle.Render(elapsed), inner),
			"",
			m.spin.View() + "  Opening your workspace",
			"",
			dimStyle.Render("Checking the session, people, and joined conversations."),
		}
		if time.Since(m.connectStarted) > 8*time.Second {
			lines = append(lines, "", dimStyle.Render("Still working — large workspaces can take a few seconds."))
		}
	} else {
		title, explanation := friendlyConnectionError(m.err)
		lines = []string{
			errorStyle.Render(title),
			"",
		}
		lines = append(lines, wrapText(explanation, max(8, inner-2))...)
		lines = append(lines,
			"",
			dimStyle.Render("Nothing was changed. You can retry safely."),
			"",
			selectedNav.Render(" R  TRY AGAIN ")+"  "+selectedStyle.Render("L  SIGN IN AGAIN"),
			"",
			dimStyle.Render(truncate(oneLine(m.err), inner-2)),
		)
	}
	content := cropLines(strings.Join(lines, "\n"), max(1, height-4), inner)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(purple).
		Width(inner).
		Padding(1).
		Render(content)
	return centerBlock(box, m.width, height)
}

func friendlyConnectionError(value string) (string, string) {
	lower := strings.ToLower(value)
	switch {
	case strings.Contains(lower, "context deadline exceeded") || strings.Contains(lower, "client.timeout"):
		return "SLACK TOOK TOO LONG TO ANSWER", "The network or Slack API timed out while loading this workspace."
	case strings.Contains(lower, "invalid_auth"), strings.Contains(lower, "token_expired"), strings.Contains(lower, "token_revoked"):
		return "YOUR SLACK LOGIN NEEDS ATTENTION", "Sign in again to replace the expired or revoked workspace session."
	case strings.Contains(lower, "missing_scope"):
		return "THE SLACK APP NEEDS ANOTHER PERMISSION", "Update the app scopes, reinstall it, then sign in again."
	default:
		return "COULDN’T OPEN THIS WORKSPACE", "Slack returned an error while gack was preparing the conversation view."
	}
}

func (m *Model) sidebarWidth() int {
	if m.width < 48 {
		if m.focus == focusSidebar {
			return m.width
		}
		return 0
	}
	return min(38, max(24, m.width/5))
}

func (m *Model) renderSidebar(width, height int) string {
	innerHeight := max(1, height-2)
	innerWidth := max(1, width-2)
	lines := make([]string, 0, innerHeight)
	unreadActivity := 0
	for _, item := range m.activity {
		if item.Unread {
			unreadActivity++
		}
	}
	lines = append(lines,
		paneHeading("Workspace", m.focus == focusSidebar, innerWidth),
		dimStyle.Render("  PRIORITY"),
		m.sidebarLine("  ●  Notifications", unreadActivity, m.sidebarAt == 0 && m.focus == focusSidebar, m.mode == viewNotifications, innerWidth),
		m.sidebarLine("  ◷  Activity", len(m.activity), m.sidebarAt == 1 && m.focus == focusSidebar, m.mode == viewActivity, innerWidth),
		"",
	)
	// Reserve one row for the channel group heading so a long list still has a
	// clear label and visible position within the virtualized window.
	available := max(0, innerHeight-len(lines)-1)
	cursor := m.channel
	if m.sidebarAt >= 2 {
		cursor = m.sidebarAt - 2
	}
	start := max(0, cursor-available/2)
	if start+available > len(m.channels) {
		start = max(0, len(m.channels)-available)
	}
	end := min(len(m.channels), start+available)
	rangeLabel := fmt.Sprintf("%d CHANNELS", len(m.channels))
	if len(m.channels) > available && available > 0 {
		rangeLabel = fmt.Sprintf("CHANNELS  %d–%d OF %d", start+1, end, len(m.channels))
	}
	sortLabel := "s: " + strings.ToUpper(m.sidebarSortLabel())
	lines = append(lines, joinAcross(dimStyle.Render("  "+rangeLabel), dimStyle.Render(sortLabel), innerWidth))
	m.visibleChannelStart = start
	// Global terminal row: the app header, pane border, then the sidebar rows
	// above the first channel. Keep this derived from the rendered structure so
	// mouse dragging stays correct as the navigation chrome evolves.
	m.channelRowStart = lipgloss.Height(m.renderHeader()) + 1 + len(lines)
	for index := start; index < len(m.channels) && len(lines) < innerHeight; index++ {
		channel := m.channels[index]
		label := channel.Label()
		if channel.IsFavorite {
			label = "★  " + label
		} else {
			label = "   " + label
		}
		if index == m.dragAt {
			label = "↕ " + label
		}
		lines = append(lines, m.sidebarLine(label, channel.Unread, m.sidebarAt == index+2 && m.focus == focusSidebar, index == m.channel && m.mode == viewConversation, innerWidth))
	}
	for len(lines) < innerHeight {
		lines = append(lines, "")
	}
	style := inactiveBorder
	if m.focus == focusSidebar {
		style = activeBorder
	}
	return style.Width(innerWidth).Height(innerHeight).Render(cropLines(strings.Join(lines, "\n"), innerHeight, innerWidth))
}

func (m *Model) sidebarLine(label string, count int, selected, current bool, width int) string {
	badge := ""
	if count > 0 {
		badge = fmt.Sprintf(" ● %d", count)
	}
	prefix := "  "
	if selected {
		prefix = "› "
	} else if current {
		prefix = "▌ "
	}
	space := max(1, width-lipgloss.Width(prefix)-lipgloss.Width(label)-lipgloss.Width(badge))
	line := prefix + truncate(label, max(1, width-lipgloss.Width(badge)-lipgloss.Width(prefix)-1)) + strings.Repeat(" ", space) + badge
	line = truncate(line, width)
	if selected {
		return selectedNav.Width(width).Render(line)
	}
	if current {
		return selectedRow.Bold(true).Width(width).Render(line)
	}
	if count > 0 {
		return lipgloss.NewStyle().Bold(true).Width(width).Render(line)
	}
	return line
}

func (m *Model) renderConversation(width, height int) string {
	title := "Conversation"
	if m.channel >= 0 && m.channel < len(m.channels) {
		channel := m.channels[m.channel]
		title = channel.Label()
		if channel.Topic != "" && width > 48 {
			title += dimStyle.Render("  " + truncate(channel.Topic, width-lipgloss.Width(channel.Label())-8))
		}
	}
	contentHeight := max(1, height-3)
	measure, inset := readingColumn(max(1, width-2), 94)
	content := m.virtualMessages(m.messages, m.message, contentHeight, measure, m.focus == focusMessages)
	if len(m.messages) == 0 && m.busy == "" {
		content = dimStyle.Render("No messages in this conversation.")
	}
	content = insetLines(content, inset)
	return m.renderPane(title, content, width, height, m.focus == focusMessages)
}

func (m *Model) renderThread(width, height int) string {
	title := "Thread"
	if len(m.thread) > 0 {
		title += dimStyle.Render(fmt.Sprintf("  %d replies", max(0, len(m.thread)-1)))
	}
	measure, inset := readingColumn(max(1, width-2), 72)
	content := m.virtualMessages(m.thread, m.threadAt, max(1, height-3), measure, m.focus == focusThread)
	if len(m.thread) == 0 {
		content = dimStyle.Render("Loading thread…")
	}
	content = insetLines(content, inset)
	return m.renderPane(title, content, width, height, m.focus == focusThread)
}

func (m *Model) renderActivity(width, height int) string {
	title := "Activity"
	if m.mode == viewNotifications {
		title = "Notifications"
	}
	items := m.filteredActivity()
	contentHeight := max(1, height-3)
	if len(items) == 0 {
		return m.renderPane(title, dimStyle.Render("You’re all caught up."), width, height, m.focus != focusSidebar)
	}
	itemHeight := 4
	visible := max(1, contentHeight/itemHeight)
	start := max(0, m.activityAt-visible/2)
	if start+visible > len(items) {
		start = max(0, len(items)-visible)
	}
	measure, inset := readingColumn(max(1, width-2), 88)
	var rendered []string
	for index := start; index < len(items) && index < start+visible; index++ {
		item := items[index]
		marker := "  "
		if index == m.activityAt && m.focus != focusSidebar {
			marker = selectedStyle.Render("▌ ")
		}
		unread := ""
		if item.Unread {
			unread = " ●"
		}
		heading := fmt.Sprintf("%s%s in #%s%s", marker, activityIcon(item.Kind), item.ChannelName, unread)
		bodyWidth := max(8, measure-4)
		body := wrapText(formatSlackText(item.Text, m.snapshot.Users, m.channels), bodyWidth)
		itemView := heading + "\n  " + item.Actor + dimStyle.Render(" · "+relativeTime(item.Time)) + "\n  " + strings.Join(body, "\n  ")
		if index == m.activityAt && m.focus != focusSidebar {
			itemView = styleLines(itemView, selectedRow, measure)
		}
		rendered = append(rendered, itemView)
	}
	return m.renderPane(title, insetLines(strings.Join(rendered, "\n\n"), inset), width, height, m.focus != focusSidebar)
}

func (m *Model) renderPane(title, content string, width, height int, active bool) string {
	if width <= 2 || height <= 2 {
		return ""
	}
	innerWidth, innerHeight := width-2, height-2
	titleLine := paneHeading(title, active, innerWidth)
	contentHeight := max(0, innerHeight-1)
	content = cropLines(content, contentHeight, innerWidth)
	if contentHeight > 0 {
		content = titleLine + "\n" + content
	} else {
		content = titleLine
	}
	style := inactiveBorder
	if active {
		style = activeBorder
	}
	return style.Width(innerWidth).Height(innerHeight).Render(content)
}

func (m *Model) virtualMessages(messages []gack.Message, selected, height, width int, active bool) string {
	if len(messages) == 0 || height <= 0 {
		return ""
	}
	selected = max(0, min(len(messages)-1, selected))
	type renderedMessage struct {
		index  int
		value  string
		height int
	}
	current := m.renderMessage(messages[selected], width, active, selected)
	rows := []renderedMessage{{selected, current, max(1, lipgloss.Height(current))}}
	used := rows[0].height
	// Fill beneath the cursor first, then use all remaining rows above it. Only
	// visible messages are formatted, so a very large backing history has flat
	// rendering cost.
	for index := selected + 1; index < len(messages) && used < height/2; index++ {
		value := m.renderMessage(messages[index], width, false, index)
		h := max(1, lipgloss.Height(value)) + 1
		if used+h > height {
			break
		}
		rows = append(rows, renderedMessage{index, value, h})
		used += h
	}
	for index := selected - 1; index >= 0 && used < height; index-- {
		value := m.renderMessage(messages[index], width, false, index)
		h := max(1, lipgloss.Height(value)) + 1
		if used+h > height {
			break
		}
		rows = append([]renderedMessage{{index, value, h}}, rows...)
		used += h
	}
	for index := rows[len(rows)-1].index + 1; index < len(messages) && used < height; index++ {
		value := m.renderMessage(messages[index], width, false, index)
		h := max(1, lipgloss.Height(value)) + 1
		if used+h > height {
			break
		}
		rows = append(rows, renderedMessage{index, value, h})
		used += h
	}
	parts := make([]string, len(rows))
	for i, row := range rows {
		parts[i] = row.value
	}
	return strings.Join(parts, "\n\n")
}

func (m *Model) renderMessage(message gack.Message, width int, selected bool, index int) string {
	marker := "  "
	if selected {
		marker = selectedStyle.Render("▌ ")
	}
	username := message.Username
	if user, ok := m.snapshot.Users[message.UserID]; ok && username == "" {
		username = user.DisplayName()
	}
	if username == "" {
		username = "unknown"
	}
	heading := marker + lipgloss.NewStyle().Bold(true).Render(username) + dimStyle.Render("  "+message.Time.Format("15:04"))
	if message.Edited {
		heading += dimStyle.Render("  edited")
	}
	if selected {
		heading = joinAcross(heading, selectedNav.Render(" SELECTED "), width)
	}
	bodyWidth := max(8, width-2)
	lines := []string{heading}
	text := strings.TrimSpace(formatSlackText(message.Text, m.snapshot.Users, m.channels))
	if text != "" {
		for _, line := range wrapText(text, bodyWidth) {
			lines = append(lines, "  "+line)
		}
	}
	actionNumber := 0
	for _, block := range message.Blocks {
		switch block.Type {
		case "divider":
			lines = append(lines, dimStyle.Render("  "+strings.Repeat("─", max(3, min(bodyWidth, 40)))))
		default:
			blockText := strings.TrimSpace(formatSlackText(block.Text, m.snapshot.Users, m.channels))
			if blockText != "" && blockText != text {
				for _, line := range wrapText(blockText, bodyWidth) {
					lines = append(lines, "  "+line)
				}
			}
		}
		var controls []string
		for _, element := range block.Elements {
			if element.Type == "field" {
				for _, line := range wrapText(formatSlackText(element.Text, m.snapshot.Users, m.channels), bodyWidth) {
					lines = append(lines, "  "+line)
				}
				continue
			}
			if element.ActionID == "" {
				continue
			}
			actionNumber++
			label := firstNonEmpty(element.Text, element.Placeholder, element.ActionID)
			if element.Type != "button" {
				label += " ▾"
			}
			if selected {
				controls = append(controls, selectedStyle.Render(fmt.Sprintf("[%d %s]", actionNumber, label)))
			} else {
				controls = append(controls, "["+label+"]")
			}
		}
		if len(controls) > 0 {
			lines = append(lines, "  "+strings.Join(controls, "  "))
		}
	}
	if len(message.Reactions) > 0 {
		var reactions []string
		for _, reaction := range message.Reactions {
			if reaction.Count <= 0 {
				continue
			}
			label := fmt.Sprintf("%s %d", renderEmoji(reaction.Name), reaction.Count)
			if reaction.Mine {
				label = selectedStyle.Render("[" + label + "]")
			} else {
				label = "[" + label + "]"
			}
			reactions = append(reactions, label)
		}
		if len(reactions) > 0 {
			lines = append(lines, "  "+strings.Join(reactions, " "))
		}
	}
	if message.ReplyCount > 0 {
		lines = append(lines, selectedStyle.Render(fmt.Sprintf("  ↳ %d replies  Enter to open", message.ReplyCount)))
	}
	messageView := strings.Join(lines, "\n")
	if selected {
		messageView = styleLines(messageView, selectedRow, width)
	}
	return messageView
}

func (m *Model) renderSearch(background string, height int) string {
	width := max(8, min(m.width-4, 82))
	inner := max(1, width-4)
	resultCount := m.searchItemCount()
	position := "TYPE TO FILTER"
	if resultCount > 0 {
		position = fmt.Sprintf("ITEM %d OF %d", min(m.searchAt+1, resultCount), resultCount)
	}
	lines := []string{
		joinAcross(selectedStyle.Render("⌕  COMMAND PALETTE"), dimStyle.Render(position), inner),
		dimStyle.Render("Jump to a channel or search every message"),
		"",
		lipgloss.NewStyle().Background(soft).Padding(0, 1).Width(max(1, inner-2)).Render(m.searchInput.View()),
		"",
	}
	if m.busy == "Searching…" {
		lines = append(lines, dimStyle.Render("Searching messages…"))
	} else if m.searchRan {
		if len(m.searchResults) == 0 {
			lines = append(lines, dimStyle.Render("No messages found"))
		}
		for index, result := range m.searchResults {
			label := fmt.Sprintf("#%-16s %s", result.ChannelName, oneLine(formatSlackText(result.Message.Text, m.snapshot.Users, m.channels)))
			lines = append(lines, chooserLine(label, index == m.searchAt, inner))
		}
	} else {
		channels := m.filteredChannels(m.searchInput.Value())
		for index, channel := range channels {
			label := channel.Label()
			if channel.Topic != "" {
				label += dimStyle.Render("  " + oneLine(channel.Topic))
			}
			lines = append(lines, chooserLine(label, index == m.searchAt, inner))
		}
		if strings.TrimSpace(m.searchInput.Value()) != "" {
			label := "⌕  Search messages for “" + m.searchInput.Value() + "”"
			lines = append(lines, chooserLine(label, m.searchAt == len(channels), inner))
		}
	}
	lines = append(lines, "", dimStyle.Render("↑/↓ choose · Enter open/search · Esc close · Ctrl+K toggle"))
	boxHeight := max(3, min(height-1, max(10, len(lines)+2)))
	box := floatingBorder.Width(max(1, width-2)).Height(max(1, boxHeight-2)).Render(cropLines(strings.Join(lines, "\n"), max(1, boxHeight-2), max(1, width-4)))
	return floatingOverlay(background, withShadow(box), m.width, height)
}

func (m *Model) renderPicker(background string, height int) string {
	title := "Choose an action"
	if m.overlay == overlayReaction {
		title = "Add or remove a reaction"
	}
	width := max(8, min(54, m.width-4))
	inner := max(1, width-4)
	position := "NO OPTIONS"
	if len(m.pickerOptions) > 0 {
		position = fmt.Sprintf("OPTION %d OF %d", min(m.pickerAt+1, len(m.pickerOptions)), len(m.pickerOptions))
	}
	lines := []string{joinAcross(selectedStyle.Render(title), dimStyle.Render(position), inner), ""}
	for index, option := range m.pickerOptions {
		lines = append(lines, chooserLine(option.label, index == m.pickerAt, inner))
	}
	lines = append(lines, "", dimStyle.Render("↑/↓ choose · Enter apply · Esc close"))
	boxHeight := max(3, min(height-1, len(lines)+2))
	box := floatingBorder.Width(max(1, width-2)).Height(max(1, boxHeight-2)).Render(cropLines(strings.Join(lines, "\n"), max(1, boxHeight-2), inner))
	return floatingOverlay(background, withShadow(box), m.width, height)
}

func (m *Model) renderDialog(background string, height int) string {
	dialog := m.dialog
	width := max(8, min(76, m.width-4))
	inner := max(1, width-4)
	fieldAt, fieldCount := dialogFieldPosition(dialog)
	position := "NO FIELDS"
	if fieldCount > 0 {
		position = fmt.Sprintf("FIELD %d OF %d", fieldAt, fieldCount)
	}
	lines := []string{
		joinAcross(selectedStyle.Render("INTERACTIVE DIALOG · "+dialog.view.Title), dimStyle.Render(position), inner),
		"",
	}
	for _, block := range dialog.view.Blocks {
		if block.Type == "section" && block.Text != "" {
			lines = append(lines, wrapText(formatSlackText(block.Text, m.snapshot.Users, m.channels), inner)...)
			lines = append(lines, "")
		}
	}
	for index := range dialog.fields {
		field := &dialog.fields[index]
		if !field.visible {
			continue
		}
		marker := "  "
		if index == dialog.at {
			marker = selectedStyle.Render("▌ ")
		}
		label := field.block.Label
		if label == "" {
			label = firstNonEmpty(field.element.Text, field.element.Placeholder, field.element.ActionID)
		}
		if field.block.Optional {
			label += dimStyle.Render(" (optional)")
		}
		if index == dialog.at {
			label += selectedNav.Render(" EDITING THIS FIELD ")
		}
		lines = append(lines, marker+label)
		switch {
		case isTextField(field.element.Type):
			lines = append(lines, "  "+field.input.View())
		case field.element.Type == "checkboxes" || strings.Contains(field.element.Type, "multi_"):
			for optionIndex, option := range field.element.Options {
				check := "[ ]"
				if field.checked[option.Value] {
					check = "[x]"
				}
				cursor := "  "
				if index == dialog.at && optionIndex == field.selected {
					cursor = "› "
				}
				lines = append(lines, "  "+cursor+check+" "+option.Text)
			}
		case len(field.element.Options) > 0:
			value := dimStyle.Render("Choose with ←/→")
			if field.selected >= 0 && field.selected < len(field.element.Options) {
				value = selectedStyle.Render("‹ " + field.element.Options[field.selected].Text + " ›")
			}
			lines = append(lines, "    "+value)
		case field.element.Type == "button":
			lines = append(lines, "    "+selectedStyle.Render("[Enter  "+field.element.Text+"]"))
		}
		if field.error != "" {
			lines = append(lines, "  "+errorStyle.Render(field.error))
		}
		lines = append(lines, "")
	}
	submit := dialog.view.Submit
	if submit == "" {
		submit = "Submit"
	}
	closeLabel := dialog.view.Close
	if closeLabel == "" {
		closeLabel = "Cancel"
	}
	lines = append(lines,
		selectedStyle.Render("[Ctrl+S  "+submit+"]")+"  "+dimStyle.Render("[Esc  "+closeLabel+"]"),
		dimStyle.Render("Tab moves fields · arrows choose · Space toggles"),
	)
	boxHeight := max(3, min(height-1, max(12, len(lines)+2)))
	content := cropLines(strings.Join(lines, "\n"), max(1, boxHeight-2), inner)
	box := floatingBorder.Width(max(1, width-2)).Height(max(1, boxHeight-2)).Render(content)
	return floatingOverlay(background, withShadow(box), m.width, height)
}

func (m *Model) renderHelp(background string, height int) string {
	width := max(8, min(74, m.width-4))
	type shortcut struct{ key, description string }
	shortcuts := []shortcut{
		{"Ctrl+K", "Command palette"}, {"Ctrl+F", "Find in conversation"},
		{"j / k", "Move selection"}, {"g / G", "First / last message"},
		{"Enter / t", "Open thread"}, {"c", "Compose or reply"},
		{"e / Ctrl+Up", "Edit own message"}, {"y", "Copy message"},
		{"r", "Toggle reaction"}, {"i or 1–9", "Block Kit actions"},
		{"a / n", "Activity / unread"}, {"Tab", "Move between panes"},
		{"s", "Sort sidebar"}, {"Shift+J/K", "Reorder channel"},
		{"R", "Refresh view"}, {"Mouse drag", "Reorder channels"},
		{"Enter (write)", "Insert newline"}, {"Ctrl+S", "Send / submit"},
		{"Esc", "Back / cancel"}, {"q", "Quit"},
	}
	lines := []string{lipgloss.NewStyle().Bold(true).Foreground(purple).Render("Keyboard & mouse"), ""}
	innerWidth := max(1, width-4)
	columns := 1
	if innerWidth >= 60 {
		columns = 2
	}
	rows := (len(shortcuts) + columns - 1) / columns
	columnWidth := max(1, innerWidth/columns)
	for row := 0; row < rows; row++ {
		line := ""
		for column := 0; column < columns; column++ {
			index := row + column*rows
			entry := ""
			if index < len(shortcuts) {
				keyCell := selectedStyle.Render(padRight(shortcuts[index].key, 14))
				entry = truncateANSI(keyCell+shortcuts[index].description, columnWidth-1)
			}
			line += padRight(entry, columnWidth)
		}
		lines = append(lines, truncateANSI(line, innerWidth))
	}
	lines = append(lines, "",
		dimStyle.Render("Cmd+K: map the chord to Ctrl+K in your terminal profile."),
		dimStyle.Render("Press ? or Esc to close"),
	)
	boxHeight := max(3, min(height-1, len(lines)+2))
	box := floatingBorder.Width(max(1, width-2)).Height(max(1, boxHeight-2)).Render(cropLines(strings.Join(lines, "\n"), max(1, boxHeight-2), max(1, width-4)))
	return floatingOverlay(background, withShadow(box), m.width, height)
}

func (m *Model) focusPath() string {
	if !m.ready {
		if m.err != "" {
			return "Recovery › R retry or L sign in"
		}
		return "Connecting"
	}
	if m.dialog != nil {
		label := "Dialog"
		if m.dialog.at >= 0 && m.dialog.at < len(m.dialog.fields) {
			field := m.dialog.fields[m.dialog.at]
			label = firstNonEmpty(field.block.Label, field.element.Text, field.element.Placeholder, field.element.ActionID, "Dialog")
		}
		at, count := dialogFieldPosition(m.dialog)
		if count > 0 {
			return fmt.Sprintf("Dialog › %s (%d/%d)", oneLine(label), at, count)
		}
		return "Dialog"
	}
	switch m.overlay {
	case overlayGlobalSearch:
		count := m.searchItemCount()
		if count > 0 {
			return fmt.Sprintf("Command palette › item %d/%d", min(m.searchAt+1, count), count)
		}
		return "Command palette › search"
	case overlayFind:
		return "Find in " + m.focusedPaneName()
	case overlayAction:
		return pickerPath("Action picker", m.pickerAt, len(m.pickerOptions))
	case overlayReaction:
		return pickerPath("Reaction picker", m.pickerAt, len(m.pickerOptions))
	case overlayHelp:
		return "Help"
	}
	if m.focus == focusSidebar {
		return "Sidebar › " + m.sidebarFocusLabel()
	}
	if m.focus == focusComposer {
		context := "Composer"
		if m.composeEditTS != "" {
			context = "Editing message"
		} else if m.composeThread != "" {
			context = "Thread › Composer"
		}
		return context
	}
	if m.mode == viewActivity || m.mode == viewNotifications {
		items := m.filteredActivity()
		return itemPath(m.focusedPaneName(), m.activityAt, len(items), "item")
	}
	if m.focus == focusThread {
		return itemPath("Thread", m.threadAt, len(m.thread), "reply")
	}
	return itemPath("Conversation", m.message, len(m.messages), "message")
}

func (m *Model) focusedPaneName() string {
	if m.focus == focusThread {
		return "thread"
	}
	if m.mode == viewNotifications {
		return "notifications"
	}
	if m.mode == viewActivity {
		return "activity"
	}
	return "conversation"
}

func (m *Model) sidebarFocusLabel() string {
	selected := m.sidebarSelection()
	switch selected {
	case 0:
		return "Notifications"
	case 1:
		return "Activity"
	default:
		index := selected - 2
		if index >= 0 && index < len(m.channels) {
			return m.channels[index].Label()
		}
	}
	return "Navigation"
}

func (m *Model) contextualHelp() string {
	context := "CONVERSATION"
	bindings := []key.Binding{
		helpBinding("↑/↓", "select"),
		helpBinding("Enter", "thread"),
		helpBinding("c", "write"),
		helpBinding("r", "react"),
		helpBinding("i", "actions"),
		helpBinding("Tab", "panes"),
		helpBinding("?", "all keys"),
	}
	switch {
	case m.focus == focusSidebar:
		context = "SIDEBAR"
		bindings = []key.Binding{
			helpBinding("↑/↓", "select"),
			helpBinding("Enter", "open"),
			helpBinding("s", "sort"),
			helpBinding("⇧J/K", "reorder"),
			helpBinding("Tab", "next pane"),
			helpBinding("?", "all keys"),
		}
	case m.mode == viewActivity || m.mode == viewNotifications:
		context = strings.ToUpper(m.focusedPaneName())
		bindings = []key.Binding{
			helpBinding("↑/↓", "select"),
			helpBinding("Enter", "open"),
			helpBinding("h", "sidebar"),
			helpBinding("Tab", "next pane"),
			helpBinding("?", "all keys"),
		}
	case m.focus == focusThread:
		context = "THREAD"
		bindings = []key.Binding{
			helpBinding("↑/↓", "select"),
			helpBinding("c", "reply"),
			helpBinding("r", "react"),
			helpBinding("i", "actions"),
			helpBinding("h", "conversation"),
			helpBinding("?", "all keys"),
		}
	}

	badge := selectedNav.Render(" " + context + " ")
	helper := help.New()
	helper.Width = max(1, m.width-lipgloss.Width(badge)-2)
	helper.ShortSeparator = "  ·  "
	helper.Styles.ShortKey = selectedStyle
	helper.Styles.ShortDesc = dimStyle
	helper.Styles.ShortSeparator = dimStyle
	helper.Styles.Ellipsis = dimStyle
	return badge + "  " + helper.ShortHelpView(bindings)
}

func (m *Model) dialogFooter() string {
	label := "No interactive fields"
	hint := "Ctrl+S submit · Esc cancel"
	if m.dialog != nil && m.dialog.at >= 0 && m.dialog.at < len(m.dialog.fields) {
		field := m.dialog.fields[m.dialog.at]
		label = firstNonEmpty(field.block.Label, field.element.Text, field.element.Placeholder, field.element.ActionID, "Field")
		switch {
		case isTextField(field.element.Type):
			hint = "type · Tab next field · Ctrl+S submit · Esc cancel"
		case field.element.Type == "checkboxes" || strings.Contains(field.element.Type, "multi_"):
			hint = "arrows choose · Space toggle · Tab next field · Ctrl+S submit"
		case len(field.element.Options) > 0:
			hint = "←/→ choose · Tab next field · Ctrl+S submit · Esc cancel"
		case field.element.Type == "button":
			hint = "Enter press · Tab next field · Ctrl+S submit · Esc cancel"
		}
	}
	return "DIALOG · " + oneLine(label) + " · " + hint
}

func itemPath(pane string, at, count int, noun string) string {
	if count <= 0 {
		return pane
	}
	at = max(0, min(count-1, at))
	return fmt.Sprintf("%s › %s %d/%d", pane, noun, at+1, count)
}

func pickerPath(picker string, at, count int) string {
	if count <= 0 {
		return picker
	}
	return fmt.Sprintf("%s › option %d/%d", picker, min(at+1, count), count)
}

func dialogFieldPosition(dialog *dialogState) (int, int) {
	if dialog == nil {
		return 0, 0
	}
	position, count := 0, 0
	for index := range dialog.fields {
		if !dialog.fields[index].visible {
			continue
		}
		count++
		if index == dialog.at {
			position = count
		}
	}
	return position, count
}

func paneHeading(title string, active bool, width int) string {
	marker := dimStyle.Render("○")
	label := lipgloss.NewStyle().Bold(true).Render(title)
	right := ""
	if active {
		marker = selectedStyle.Render("◆")
		right = selectedNav.Render(" ACTIVE PANE ")
	}
	heading := joinAcross(marker+" "+label, right, width)
	if active {
		return selectedRow.Width(width).Render(heading)
	}
	return heading
}

func helpBinding(keyName, description string) key.Binding {
	return key.NewBinding(key.WithKeys(keyName), key.WithHelp(keyName, description))
}

// readingColumn gives message text a comfortable maximum measure and places
// it slightly in from the pane edge. The pane may span a cinema-wide terminal;
// prose should not. The unused columns deliberately become breathing room.
func readingColumn(available, maximum int) (measure, inset int) {
	available = max(1, available)
	inset = 1
	if available > maximum+8 {
		inset = min(12, max(2, (available-maximum)/5))
	}
	measure = max(8, min(maximum, available-inset))
	return measure, inset
}

func insetLines(value string, inset int) string {
	if value == "" || inset <= 0 {
		return value
	}
	prefix := strings.Repeat(" ", inset)
	lines := strings.Split(value, "\n")
	for index := range lines {
		lines[index] = prefix + lines[index]
	}
	return strings.Join(lines, "\n")
}

func styleLines(value string, style lipgloss.Style, width int) string {
	lines := strings.Split(value, "\n")
	for index := range lines {
		lines[index] = style.Width(width).Render(truncateANSI(lines[index], width))
	}
	return strings.Join(lines, "\n")
}

func centerBlock(value string, width, height int) string {
	value = cropLines(value, height, width)
	lines := strings.Split(value, "\n")
	top := max(0, (height-len(lines))/2)
	result := make([]string, 0, height)
	blank := strings.Repeat(" ", max(1, width))
	for range top {
		result = append(result, blank)
	}
	for _, line := range lines {
		result = append(result, lipgloss.PlaceHorizontal(width, lipgloss.Center, line))
	}
	for len(result) < height {
		result = append(result, blank)
	}
	if len(result) > height {
		result = result[:height]
	}
	return strings.Join(result, "\n")
}

func joinAcross(left, right string, width int) string {
	if width <= 0 {
		return ""
	}
	if right == "" {
		return truncateANSI(left, width)
	}
	right = truncateANSI(right, width)
	available := width - lipgloss.Width(right) - 1
	if available <= 0 {
		return truncateANSI(left, width)
	}
	left = truncateANSI(left, available)
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	return left + strings.Repeat(" ", gap) + right
}

func withShadow(box string) string {
	boxWidth := lipgloss.Width(box)
	lines := strings.Split(box, "\n")
	for index := range lines {
		lines[index] = padRight(lines[index], boxWidth) + dimStyle.Render("░░")
	}
	lines = append(lines, dimStyle.Render("  "+strings.Repeat("░", boxWidth)))
	return strings.Join(lines, "\n")
}

// floatingOverlay composites a modal above a dimmed copy of the current body.
// Keeping the panes visible around it makes the palette feel spatially anchored
// instead of looking like navigation replaced the entire screen.
func floatingOverlay(background, foreground string, width, height int) string {
	background = cropLines(background, height, width)
	foregroundLines := strings.Split(foreground, "\n")
	foregroundWidth := min(width, lipgloss.Width(foreground))
	foregroundHeight := min(height, len(foregroundLines))
	x := max(0, (width-foregroundWidth)/2)
	y := max(0, (height-foregroundHeight)/3)

	backgroundLines := strings.Split(background, "\n")
	for row := range backgroundLines {
		plain := ansi.Strip(backgroundLines[row])
		plain = padRight(truncateANSI(plain, width), width)
		if row < y || row >= y+foregroundHeight {
			backgroundLines[row] = dimStyle.Render(plain)
			continue
		}
		foregroundLine := truncateANSI(foregroundLines[row-y], foregroundWidth)
		foregroundLine = padRight(foregroundLine, foregroundWidth)
		left := padRight(ansi.Cut(plain, 0, x), x)
		rightWidth := max(0, width-x-foregroundWidth)
		right := padRight(ansi.Cut(plain, x+foregroundWidth, width), rightWidth)
		backgroundLines[row] = dimStyle.Render(left) + foregroundLine + dimStyle.Render(right)
	}
	return strings.Join(backgroundLines, "\n")
}

func padRight(value string, width int) string {
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}

func chooserLine(label string, selected bool, width int) string {
	prefix := "  "
	if selected {
		prefix = "› "
	}
	line := truncate(prefix+label, width)
	if selected {
		return selectedNav.Width(width).Render(line)
	}
	return line
}

func formatSlackText(text string, users map[string]gack.User, channels []gack.Conversation) string {
	text = html.UnescapeString(text)
	text = mentionPattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := mentionPattern.FindStringSubmatch(match)
		if user, ok := users[parts[1]]; ok {
			return "@" + user.DisplayName()
		}
		return "@" + parts[1]
	})
	text = channelPattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := channelPattern.FindStringSubmatch(match)
		if len(parts) > 3 && parts[3] != "" {
			return "#" + parts[3]
		}
		for _, channel := range channels {
			if channel.ID == parts[1] {
				return channel.Label()
			}
		}
		return "#" + parts[1]
	})
	text = linkPattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := linkPattern.FindStringSubmatch(match)
		if len(parts) > 4 && parts[4] != "" {
			return parts[4] + " (" + parts[1] + ")"
		}
		return parts[1]
	})
	text = emojiPattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := emojiPattern.FindStringSubmatch(match)
		if emoji := renderEmoji(parts[1]); emoji != ":"+parts[1]+":" {
			return emoji
		}
		return match
	})
	return text
}

func renderEmoji(name string) string {
	if emoji, ok := map[string]string{
		"+1": "👍", "thumbsup": "👍", "heart": "❤️", "tada": "🎉", "eyes": "👀",
		"white_check_mark": "✅", "rocket": "🚀", "raised_hands": "🙌", "joy": "😂",
		"warning": "⚠️", "fire": "🔥", "wave": "👋", "thinking_face": "🤔",
	}[name]; ok {
		return emoji
	}
	return ":" + name + ":"
}

func wrapText(text string, width int) []string {
	if width <= 0 {
		return nil
	}
	var result []string
	for _, paragraph := range strings.Split(text, "\n") {
		if paragraph == "" {
			result = append(result, "")
			continue
		}
		words := strings.Fields(paragraph)
		line := ""
		for _, word := range words {
			if utf8.RuneCountInString(word) > width {
				if line != "" {
					result = append(result, line)
					line = ""
				}
				runes := []rune(word)
				for len(runes) > width {
					result = append(result, string(runes[:width]))
					runes = runes[width:]
				}
				line = string(runes)
				continue
			}
			if line == "" {
				line = word
			} else if utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) <= width {
				line += " " + word
			} else {
				result = append(result, line)
				line = word
			}
		}
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func cropLines(value string, height, width int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(value, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i := range lines {
		lines[i] = truncateANSI(lines[i], width)
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func truncate(value string, width int) string {
	if width <= 0 || lipgloss.Width(value) <= width {
		return value
	}
	runes := []rune(value)
	if width == 1 {
		return "…"
	}
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

func truncateANSI(value string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(value, width, "")
}

func oneLine(value string) string { return strings.Join(strings.Fields(value), " ") }

func relativeTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	delta := time.Since(value)
	if delta < time.Minute {
		return "now"
	}
	if delta < time.Hour {
		return fmt.Sprintf("%dm", int(delta.Minutes()))
	}
	if delta < 24*time.Hour {
		return fmt.Sprintf("%dh", int(delta.Hours()))
	}
	return value.Format("Jan 2")
}

func activityIcon(kind string) string {
	icons := map[string]string{"mention": "@", "thread": "↳", "reaction": "☺", "message": "●"}
	if icon := icons[kind]; icon != "" {
		return icon
	}
	return "•"
}

// Sorting is kept deterministic for tests and for bridge responses that omit
// timestamp ordering.
func sortMessages(messages []gack.Message) {
	sort.SliceStable(messages, func(i, j int) bool { return messages[i].Time.Before(messages[j].Time) })
}
