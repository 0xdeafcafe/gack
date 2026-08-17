// Package loginui provides gack's interactive Slack setup and sign-in wizard.
package loginui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/0xdeafcafe/gack/internal/auth"
	"github.com/0xdeafcafe/gack/internal/slackapp"
)

// ErrCanceled is returned when the user leaves the wizard without signing in.
var ErrCanceled = errors.New("Slack sign-in canceled")

// LoginFunc performs the PKCE authorization for clientID. It is invoked by a
// Bubble Tea command, never on the UI event loop.
type LoginFunc func(context.Context, string) (auth.Credential, error)

// Config supplies the saved setup state and replaceable side effects. Leaving
// Login or OpenCreator nil selects the normal Slack implementations.
type Config struct {
	ClientID    string
	Login       LoginFunc
	OpenCreator func() error
}

type stage uint8

const (
	stageSetup stage = iota
	stageConfirm
	stageAuthorizing
	stageFailure
	stageSuccess
)

type setupFocus uint8

const (
	focusCreator setupFocus = iota
	focusClientID
)

type creatorResult struct{ err error }
type browserWaitingMsg struct{ attempt uint64 }
type loginResult struct {
	attempt    uint64
	credential auth.Credential
	err        error
}

type bindings struct {
	enter  key.Binding
	tab    key.Binding
	back   key.Binding
	edit   key.Binding
	retry  key.Binding
	open   key.Binding
	cancel key.Binding
	quit   key.Binding
}

type keyMap []key.Binding

func (k keyMap) ShortHelp() []key.Binding  { return k }
func (k keyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k} }

// Model is an embeddable Bubble Tea model. Credential returns the authorized
// result after the success state has been reached.
type Model struct {
	ctx         context.Context
	login       LoginFunc
	openCreator func() error

	stage stage
	focus setupFocus
	input textinput.Model
	spin  spinner.Model
	help  help.Model
	keys  bindings

	width  int
	height int

	clientID       string
	credential     auth.Credential
	creatorOpened  bool
	openingCreator bool
	browserWaiting bool
	status         string
	errText        string

	attempt       uint64
	attemptCancel context.CancelFunc
	done          bool
	canceled      bool
}

var clientIDPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

// New creates a login wizard. A non-empty ClientID begins at the sign-in
// confirmation; otherwise the wizard begins with Slack app setup.
func New(ctx context.Context, config Config) *Model {
	if ctx == nil {
		ctx = context.Background()
	}
	config.ClientID = strings.TrimSpace(config.ClientID)
	invalidClientID := ""
	if config.ClientID != "" {
		validated, err := validateClientID(config.ClientID)
		if err != nil {
			invalidClientID = err.Error()
			config.ClientID = ""
		} else {
			config.ClientID = validated
		}
	}
	if config.Login == nil {
		config.Login = func(ctx context.Context, clientID string) (auth.Credential, error) {
			return (auth.OAuth{ClientID: clientID}).Login(ctx)
		}
	}
	if config.OpenCreator == nil {
		config.OpenCreator = func() error { return auth.OpenBrowser(slackapp.CreationURL()) }
	}

	input := textinput.New()
	input.Prompt = ""
	input.Placeholder = "1234567890.1234567890"
	input.CharLimit = 128
	input.SetValue(config.ClientID)
	input.Blur()

	progress := spinner.New()
	progress.Spinner = spinner.Dot
	progress.Style = lipgloss.NewStyle().Foreground(accent)

	helper := help.New()
	helper.ShowAll = false
	helper.Styles.ShortKey = lipgloss.NewStyle().Foreground(accent).Bold(true)
	helper.Styles.ShortDesc = lipgloss.NewStyle().Foreground(subtle)
	helper.Styles.ShortSeparator = lipgloss.NewStyle().Foreground(faint)

	m := &Model{
		ctx: ctx, login: config.Login, openCreator: config.OpenCreator,
		stage: stageSetup, focus: focusCreator, input: input, spin: progress, help: helper,
		width: 80, height: 24, clientID: config.ClientID,
		keys: bindings{
			enter:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
			tab:    key.NewBinding(key.WithKeys("tab", "shift+tab"), key.WithHelp("tab", "move focus")),
			back:   key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
			edit:   key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit client ID")),
			retry:  key.NewBinding(key.WithKeys("r", "enter"), key.WithHelp("enter", "retry")),
			open:   key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open creator")),
			cancel: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
			quit:   key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
		},
	}
	if config.ClientID != "" {
		m.stage = stageConfirm
	} else if invalidClientID != "" {
		m.errText = invalidClientID
		m.setSetupFocus(focusClientID)
	}
	return m
}

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = max(20, message.Width)
		m.height = max(6, message.Height)
		m.help.Width = m.width
		m.resizeInput()
		return m, nil
	case creatorResult:
		m.openingCreator = false
		if message.err != nil {
			m.errText = safeError(message.err)
			return m, nil
		}
		m.creatorOpened = true
		m.status = "Slack opened — create the app, then paste its Client ID below."
		m.errText = ""
		m.setSetupFocus(focusClientID)
		return m, nil
	case browserWaitingMsg:
		if m.stage == stageAuthorizing && message.attempt == m.attempt {
			m.browserWaiting = true
		}
		return m, nil
	case loginResult:
		if message.attempt != m.attempt || m.stage != stageAuthorizing {
			return m, nil
		}
		m.stopAttempt()
		if message.err != nil {
			m.stage = stageFailure
			m.errText = safeError(message.err)
			m.browserWaiting = false
			return m, nil
		}
		if message.credential.ClientID == "" {
			message.credential.ClientID = m.clientID
		}
		m.credential = message.credential
		m.stage = stageSuccess
		m.errText = ""
		m.browserWaiting = false
		return m, nil
	case spinner.TickMsg:
		if m.stage == stageAuthorizing || m.openingCreator {
			var command tea.Cmd
			m.spin, command = m.spin.Update(message)
			return m, command
		}
		return m, nil
	case tea.KeyMsg:
		return m.updateKey(message)
	default:
		return m, nil
	}
}

func (m *Model) updateKey(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(message, m.keys.quit) {
		m.stopAttempt()
		m.canceled = true
		return m, tea.Quit
	}

	switch m.stage {
	case stageSetup:
		if key.Matches(message, m.keys.cancel) {
			m.canceled = true
			return m, tea.Quit
		}
		if m.openingCreator {
			return m, nil
		}
		if key.Matches(message, m.keys.tab) {
			if m.focus == focusCreator {
				m.setSetupFocus(focusClientID)
			} else {
				m.setSetupFocus(focusCreator)
			}
			return m, nil
		}
		if m.focus == focusCreator && (key.Matches(message, m.keys.enter) || key.Matches(message, m.keys.open)) {
			m.openingCreator = true
			m.status = "Opening Slack’s app creator…"
			m.errText = ""
			return m, tea.Batch(m.spin.Tick, m.openCreatorCmd())
		}
		if m.focus == focusClientID {
			if key.Matches(message, m.keys.enter) {
				clientID, err := validateClientID(m.input.Value())
				if err != nil {
					m.errText = err.Error()
					return m, nil
				}
				m.clientID = clientID
				m.input.SetValue(clientID)
				m.input.Blur()
				m.stage = stageConfirm
				m.status = ""
				m.errText = ""
				return m, nil
			}
			var command tea.Cmd
			m.input, command = m.input.Update(message)
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(m.input.Value())), "xox") {
				// A pasted token is not a Client ID. Clear it immediately so a
				// screen recording or shoulder-surfer cannot recover it.
				m.input.SetValue("")
				m.errText = "That looked like a token, so gack cleared it. Paste the numeric Client ID instead."
			} else {
				m.errText = ""
			}
			return m, command
		}
	case stageConfirm:
		if key.Matches(message, m.keys.enter) {
			return m, m.startLogin()
		}
		if key.Matches(message, m.keys.edit) || key.Matches(message, m.keys.back) {
			m.stage = stageSetup
			m.status = ""
			m.errText = ""
			m.setSetupFocus(focusClientID)
			return m, nil
		}
	case stageAuthorizing:
		if key.Matches(message, m.keys.cancel) {
			m.stopAttempt()
			m.attempt++ // Ignore any result already queued by the canceled attempt.
			m.stage = stageConfirm
			m.browserWaiting = false
			m.status = "Sign-in canceled. Nothing was saved."
			return m, nil
		}
	case stageFailure:
		if key.Matches(message, m.keys.retry) {
			return m, m.startLogin()
		}
		if key.Matches(message, m.keys.edit) {
			m.stage = stageSetup
			m.errText = ""
			m.setSetupFocus(focusClientID)
			return m, nil
		}
		if key.Matches(message, m.keys.cancel) {
			m.canceled = true
			return m, tea.Quit
		}
	case stageSuccess:
		if key.Matches(message, m.keys.enter) {
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *Model) startLogin() tea.Cmd {
	m.stopAttempt()
	m.attempt++
	attempt := m.attempt
	ctx, cancel := context.WithCancel(m.ctx)
	m.attemptCancel = cancel
	m.stage = stageAuthorizing
	m.browserWaiting = false
	m.errText = ""
	m.status = ""
	return tea.Batch(m.spin.Tick, browserWaitCmd(attempt), loginCmd(ctx, attempt, m.clientID, m.login))
}

func (m *Model) stopAttempt() {
	if m.attemptCancel != nil {
		m.attemptCancel()
		m.attemptCancel = nil
	}
}

func (m *Model) setSetupFocus(focus setupFocus) {
	m.focus = focus
	if focus == focusClientID {
		m.input.Focus()
	} else {
		m.input.Blur()
	}
}

func (m *Model) resizeInput() {
	cardWidth := min(72, max(18, m.width-4))
	m.input.Width = max(8, cardWidth-10)
}

func (m *Model) openCreatorCmd() tea.Cmd {
	return func() tea.Msg { return creatorResult{err: m.openCreator()} }
}

func browserWaitCmd(attempt uint64) tea.Cmd {
	return tea.Tick(350*time.Millisecond, func(time.Time) tea.Msg {
		return browserWaitingMsg{attempt: attempt}
	})
}

func loginCmd(ctx context.Context, attempt uint64, clientID string, login LoginFunc) tea.Cmd {
	return func() tea.Msg {
		credential, err := login(ctx, clientID)
		return loginResult{attempt: attempt, credential: credential, err: err}
	}
}

func validateClientID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("Paste the Client ID from Slack’s Basic Information page.")
	}
	if strings.HasPrefix(strings.ToLower(value), "xox") {
		return "", errors.New("That looks like a token. Paste the numeric Client ID — tokens never belong here.")
	}
	if !clientIDPattern.MatchString(value) {
		return "", errors.New("Client IDs contain two groups of digits separated by a dot.")
	}
	return value, nil
}

// Credential reports the result without exposing it in the rendered view.
func (m *Model) Credential() (auth.Credential, bool) {
	return m.credential, m.credential.AccessToken != ""
}

// ClientID returns the validated app client ID selected by the user.
func (m *Model) ClientID() string { return m.clientID }

// Run opens the wizard in an alternate screen and returns its authorized
// credential. Callers remain responsible for persisting the credential.
func Run(ctx context.Context, config Config, options ...tea.ProgramOption) (auth.Credential, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	model := New(ctx, config)
	programOptions := []tea.ProgramOption{tea.WithContext(ctx), tea.WithAltScreen()}
	programOptions = append(programOptions, options...)
	final, err := tea.NewProgram(model, programOptions...).Run()
	if err != nil {
		if ctx.Err() != nil {
			return auth.Credential{}, ctx.Err()
		}
		return auth.Credential{}, err
	}
	result, ok := final.(*Model)
	if !ok {
		return auth.Credential{}, errors.New("login wizard returned an unexpected model")
	}
	if credential, authorized := result.Credential(); authorized && result.done {
		return credential, nil
	}
	return auth.Credential{}, ErrCanceled
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	// OAuth tokens should never appear in UI output, even if a custom Login
	// implementation accidentally includes one in an error.
	tokens := regexp.MustCompile(`(?i)xox[a-z0-9-]*-[a-z0-9-]+`)
	value = tokens.ReplaceAllString(value, "[redacted]")
	return ansi.Truncate(strings.TrimSpace(value), 240, "…")
}

func workspaceName(credential auth.Credential) string {
	if strings.TrimSpace(credential.TeamName) != "" {
		return credential.TeamName
	}
	if strings.TrimSpace(credential.TeamID) != "" {
		return credential.TeamID
	}
	return "your Slack workspace"
}

func (m *Model) String() string {
	return fmt.Sprintf("login wizard (stage %d)", m.stage)
}
