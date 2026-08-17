package loginui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	accent     = lipgloss.AdaptiveColor{Light: "#5B2C6F", Dark: "#C792EA"}
	accentDeep = lipgloss.AdaptiveColor{Light: "#3F1D4D", Dark: "#6E3A78"}
	foreground = lipgloss.AdaptiveColor{Light: "#201A24", Dark: "#F7F3F8"}
	subtle     = lipgloss.AdaptiveColor{Light: "#5E5761", Dark: "#AAA2AD"}
	faint      = lipgloss.AdaptiveColor{Light: "#CAC3CC", Dark: "#514A54"}
	panel      = lipgloss.AdaptiveColor{Light: "#F7F3F8", Dark: "#26232B"}
	good       = lipgloss.AdaptiveColor{Light: "#166534", Dark: "#50FA7B"}
	bad        = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#FF6B6B"}

	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(foreground)
	subtleStyle  = lipgloss.NewStyle().Foreground(subtle)
	focusStyle   = lipgloss.NewStyle().Bold(true).Foreground(accent)
	errorStyle   = lipgloss.NewStyle().Bold(true).Foreground(bad)
	successStyle = lipgloss.NewStyle().Bold(true).Foreground(good)
)

func (m *Model) View() string {
	width, height := max(20, m.width), max(6, m.height)
	header := m.renderHeader(width)
	footer := m.renderHelp(width)
	bodyHeight := max(1, height-2)
	cardWidth := min(72, max(18, width-4))
	innerWidth := max(14, cardWidth-4)
	compact := bodyHeight < 15 || innerWidth < 44

	content := m.renderStage(innerWidth, compact)
	card := lipgloss.NewStyle().
		Width(innerWidth).
		Padding(0, 1).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accentDeep).
		Render(content)
	card = fitBlock(card, cardWidth, bodyHeight)
	body := lipgloss.Place(width, bodyHeight, lipgloss.Center, lipgloss.Center, card)
	return fitBlock(header+"\n"+body+"\n"+footer, width, height)
}

func (m *Model) renderHeader(width int) string {
	left := " GACK"
	right := "CONNECT SLACK "
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	line := left + strings.Repeat(" ", gap) + right
	return lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#FFFFFF")).
		Background(accentDeep).
		Render(ansi.Truncate(line, width, ""))
}

func (m *Model) renderHelp(width int) string {
	bindings := m.helpBindings()
	if len(bindings) == 0 {
		return strings.Repeat(" ", width)
	}
	value := m.help.View(keyMap(bindings))
	return lipgloss.NewStyle().PaddingLeft(1).Render(ansi.Truncate(value, max(1, width-1), ""))
}

func (m *Model) helpBindings() []key.Binding {
	switch m.stage {
	case stageSetup:
		if m.openingCreator {
			return []key.Binding{m.keys.quit}
		}
		return []key.Binding{m.keys.tab, m.keys.enter, m.keys.cancel}
	case stageConfirm:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "sign in")),
			m.keys.edit,
			m.keys.back,
			m.keys.quit,
		}
	case stageAuthorizing:
		return []key.Binding{m.keys.cancel, m.keys.quit}
	case stageFailure:
		return []key.Binding{m.keys.retry, m.keys.edit, m.keys.cancel}
	case stageSuccess:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "continue"))}
	default:
		return nil
	}
}

func (m *Model) renderStage(width int, compact bool) string {
	lines := []string{
		m.progress(width),
		"",
	}
	switch m.stage {
	case stageSetup:
		lines = append(lines, m.renderSetup(width, compact)...)
	case stageConfirm:
		lines = append(lines, m.renderConfirm(width, compact)...)
	case stageAuthorizing:
		lines = append(lines, m.renderAuthorizing(width, compact)...)
	case stageFailure:
		lines = append(lines, m.renderFailure(width, compact)...)
	case stageSuccess:
		lines = append(lines, m.renderSuccess(width, compact)...)
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderSetup(width int, compact bool) []string {
	lines := []string{
		titleStyle.Render("Connect a Slack workspace"),
	}
	if !compact {
		lines = append(lines,
			subtleStyle.Render("Create a small Slack app for gack, then connect it securely."),
			"",
		)
	}
	creatorLabel := "OPEN SLACK APP CREATOR"
	if m.openingCreator {
		creatorLabel = m.spin.View() + " OPENING SLACK…"
	}
	lines = append(lines,
		m.stepLabel("1", "Create the app", m.focus == focusCreator),
	)
	if !compact {
		lines = append(lines, subtleStyle.Render("   Permissions and the localhost callback are already filled in."))
	}
	lines = append(lines,
		"   "+renderButton(creatorLabel, m.focus == focusCreator && !m.openingCreator),
		"",
		m.stepLabel("2", "Paste the Client ID", m.focus == focusClientID),
	)
	if !compact {
		lines = append(lines, subtleStyle.Render("   Slack app → Basic Information → App Credentials"))
	}
	lines = append(lines, "   "+m.renderInput(max(8, width-3)))
	if m.errText != "" {
		lines = append(lines, errorStyle.Render("! "+m.errText))
	} else if m.status != "" {
		lines = append(lines, successStyle.Render("✓ "+m.status))
	} else if m.creatorOpened {
		lines = append(lines, successStyle.Render("✓ App creator opened"))
	}
	return lines
}

func (m *Model) renderConfirm(width int, compact bool) []string {
	lines := []string{
		titleStyle.Render("Ready to sign in"),
		subtleStyle.Render("App client ID  ") + focusStyle.Render(ansi.Truncate(m.clientID, max(8, width-16), "…")),
		"",
	}
	if !compact {
		lines = append(lines,
			"Slack will open in your browser for workspace approval.",
			subtleStyle.Render("The callback stays on localhost. No client secret is required."),
			"",
		)
	}
	lines = append(lines, renderButton("SIGN IN WITH SLACK", true))
	if m.status != "" {
		lines = append(lines, "", subtleStyle.Render(m.status))
	}
	return lines
}

func (m *Model) renderAuthorizing(width int, compact bool) []string {
	title := "Opening Slack…"
	detail := "Starting secure browser authorization."
	if m.browserWaiting {
		title = "Waiting for approval in Slack…"
		detail = "Choose a workspace and approve gack in the browser."
	}
	lines := []string{
		focusStyle.Render(m.spin.View() + " " + title),
		subtleStyle.Render(ansi.Truncate(detail, width, "…")),
	}
	if !compact {
		lines = append(lines,
			"",
			subtleStyle.Render("Keep this terminal open. The browser returns here automatically."),
			subtleStyle.Render("Press Esc if you want to stop and try again."),
		)
	}
	return lines
}

func (m *Model) renderFailure(width int, compact bool) []string {
	lines := []string{
		errorStyle.Render("Couldn’t connect to Slack"),
		lipgloss.NewStyle().Foreground(foreground).Render(ansi.Truncate(m.errText, width, "…")),
		"",
		renderButton("TRY AGAIN", true) + "  " + subtleStyle.Render("e  Edit Client ID"),
	}
	if !compact {
		lines = append(lines, "", subtleStyle.Render("Nothing was saved. You can safely retry."))
	}
	return lines
}

func (m *Model) renderSuccess(_ int, compact bool) []string {
	lines := []string{
		successStyle.Render("✓ Connected"),
		titleStyle.Render(workspaceName(m.credential)),
	}
	if !compact {
		lines = append(lines,
			"",
			subtleStyle.Render("Authorization is complete. gack can now finish setup."),
		)
	}
	lines = append(lines, "", renderButton("CONTINUE TO GACK", true))
	return lines
}

func (m *Model) progress(width int) string {
	current := 1
	if m.stage == stageConfirm || m.stage == stageAuthorizing || m.stage == stageFailure {
		current = 2
	} else if m.stage == stageSuccess {
		current = 3
	}
	labels := []string{"App", "Sign in", "Done"}
	parts := make([]string, 0, len(labels))
	for index, label := range labels {
		step := index + 1
		marker := fmt.Sprintf("%d", step)
		style := subtleStyle
		if step < current {
			marker = "✓"
			style = successStyle
		} else if step == current {
			marker = "●"
			style = focusStyle
		}
		parts = append(parts, style.Render(marker+" "+label))
	}
	separator := subtleStyle.Render("  ──  ")
	if width < 40 {
		separator = subtleStyle.Render(" · ")
	}
	return ansi.Truncate(strings.Join(parts, separator), width, "")
}

func (m *Model) stepLabel(number, label string, focused bool) string {
	marker := "○"
	style := subtleStyle
	if focused {
		marker = "●"
		style = focusStyle
	}
	return style.Render(marker + " " + number + "  " + label)
}

func (m *Model) renderInput(width int) string {
	style := lipgloss.NewStyle().Width(max(4, width-2)).Border(lipgloss.NormalBorder())
	if m.focus == focusClientID {
		style = style.BorderForeground(accent).Bold(true)
	} else {
		style = style.BorderForeground(faint)
	}
	return style.Render(m.input.View())
}

func renderButton(label string, focused bool) string {
	style := lipgloss.NewStyle().Padding(0, 1).Bold(true)
	if focused {
		return style.Foreground(lipgloss.Color("#FFFFFF")).Background(accentDeep).Render("▶ " + label)
	}
	return style.Foreground(subtle).Border(lipgloss.NormalBorder(), false, false, true, false).Render("  " + label)
}

func fitBlock(value string, width, height int) string {
	lines := strings.Split(value, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for index := range lines {
		lines[index] = ansi.Truncate(lines[index], max(1, width), "")
	}
	return strings.Join(lines, "\n")
}
