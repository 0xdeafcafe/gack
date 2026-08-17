package ui

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/0xdeafcafe/gack/internal/config"
	"github.com/0xdeafcafe/gack/internal/gack"
)

type viewMode int

const (
	viewConversation viewMode = iota
	viewActivity
	viewNotifications
)

type focus int

const (
	focusSidebar focus = iota
	focusMessages
	focusThread
	focusComposer
)

type overlayMode int

const (
	overlayNone overlayMode = iota
	overlayGlobalSearch
	overlayFind
	overlayAction
	overlayReaction
	overlayHelp
)

type pickerOption struct {
	label string
	value string
}

type sidebarSaveState struct {
	latest atomic.Uint64
	mutex  sync.Mutex
}

type ExitAction uint8

const (
	ExitNone ExitAction = iota
	ExitLogin
	ExitUpdate
)

type Model struct {
	backend     gack.Backend
	saveSidebar func(config.SidebarPreferences) error
	saveState   *sidebarSaveState
	order       []string
	sidebarSort config.SidebarSort

	width  int
	height int
	ready  bool
	busy   string
	status string
	err    string
	spin   spinner.Model

	connectStarted  time.Time
	checkUpdate     func(context.Context) (string, error)
	updateAvailable string
	exitAction      ExitAction

	snapshot gack.Snapshot
	channels []gack.Conversation
	channel  int
	messages []gack.Message
	message  int

	threadTS string
	thread   []gack.Message
	threadAt int

	activity   []gack.ActivityItem
	activityAt int
	mode       viewMode
	focus      focus
	sidebarAt  int

	searchInput   textinput.Model
	searchResults []gack.SearchResult
	searchAt      int
	searchRan     bool
	findInput     textinput.Model
	composeInput  textarea.Model
	composeThread string
	composeEditTS string
	composeOrigin focus
	overlay       overlayMode

	pickerElement *struct {
		BlockID string
		Element gack.Element
	}
	pickerOptions []pickerOption
	pickerAt      int

	dialog *dialogState

	dragFrom  int
	dragAt    int
	dragMoved bool

	visibleChannelStart int
	channelRowStart     int
}

func New(backend gack.Backend, order []string, saveOrder func([]string) error) *Model {
	var saveSidebar func(config.SidebarPreferences) error
	if saveOrder != nil {
		saveSidebar = func(preferences config.SidebarPreferences) error {
			return saveOrder(preferences.ChannelOrder)
		}
	}
	return NewWithSidebar(backend, config.SidebarPreferences{
		ChannelOrder: order,
		Sort:         config.SidebarSortManual,
	}, saveSidebar)
}

// NewWithSidebar constructs a model with persistent ordering and sort
// preferences. New remains available for embedders that only persist a manual
// channel order.
func NewWithSidebar(backend gack.Backend, preferences config.SidebarPreferences, saveSidebar func(config.SidebarPreferences) error) *Model {
	search := textinput.New()
	search.Prompt = "Search › "
	search.Placeholder = "channels and messages"
	find := textinput.New()
	find.Prompt = "Find › "
	find.Placeholder = "text in this conversation"
	compose := textarea.New()
	compose.Prompt = ""
	compose.Placeholder = "Write a message"
	compose.ShowLineNumbers = false
	compose.EndOfBufferCharacter = ' '
	compose.CharLimit = 40_000
	compose.MaxHeight = 8
	compose.SetHeight(4)
	compose.SetWidth(72)
	progress := spinner.New()
	progress.Spinner = spinner.Dot
	progress.Style = selectedStyle
	return &Model{
		backend: backend, saveSidebar: saveSidebar, saveState: &sidebarSaveState{},
		order: append([]string(nil), preferences.ChannelOrder...), sidebarSort: preferences.Sort.Normalize(),
		channel: -1, message: -1, threadAt: -1, activityAt: 0,
		focus: focusSidebar, searchInput: search, findInput: find,
		composeInput: compose, spin: progress, dragFrom: -1, dragAt: -1,
	}
}

func (m *Model) Init() tea.Cmd {
	m.startConnecting()
	commands := []tea.Cmd{m.spin.Tick, bootstrapCmd(m.backend)}
	if m.checkUpdate != nil {
		commands = append(commands, checkUpdateCmd(m.checkUpdate))
	}
	return tea.Batch(commands...)
}

// SetUpdateCheck adds a bounded background version check. Errors are silent in
// the TUI; the explicit `gack update --check` command reports diagnostics.
func (m *Model) SetUpdateCheck(check func(context.Context) (string, error)) { m.checkUpdate = check }

// RequestedExit tells the command wrapper whether the user chose an in-place
// login repair or update from a recovery/banner action.
func (m *Model) RequestedExit() ExitAction { return m.exitAction }

type bootstrapResult struct {
	snapshot gack.Snapshot
	err      error
}

type messagesResult struct {
	channel  string
	messages []gack.Message
	err      error
}

type threadResult struct {
	channel string
	thread  string
	replies []gack.Message
	err     error
}

type postResult struct {
	channel string
	thread  string
	message gack.Message
	err     error
}

type editResult struct {
	channel string
	ts      string
	message gack.Message
	err     error
}

type searchResult struct {
	results []gack.SearchResult
	err     error
}

type activityResult struct {
	items []gack.ActivityItem
	err   error
}

type interactionResult struct {
	result gack.InteractionResult
	err    error
}

type reactionResult struct {
	channel string
	thread  string
	ts      string
	emoji   string
	remove  bool
	err     error
}

type sidebarSaved struct {
	revision uint64
	notice   string
	err      error
}
type copiedResult struct{ err error }
type versionResult struct {
	latest string
	err    error
}

func checkUpdateCmd(check func(context.Context) (string, error)) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		latest, err := check(ctx)
		return versionResult{latest: latest, err: err}
	}
}

func bootstrapCmd(backend gack.Backend) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		snapshot, err := backend.Bootstrap(ctx)
		return bootstrapResult{snapshot: snapshot, err: err}
	}
}

func messagesCmd(backend gack.Backend, channel string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		messages, err := backend.Messages(ctx, channel)
		return messagesResult{channel: channel, messages: messages, err: err}
	}
}

func threadCmd(backend gack.Backend, channel, thread string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		replies, err := backend.Thread(ctx, channel, thread)
		return threadResult{channel: channel, thread: thread, replies: replies, err: err}
	}
}

func postCmd(backend gack.Backend, channel, thread, text string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		message, err := backend.PostMessage(ctx, channel, thread, text)
		return postResult{channel: channel, thread: thread, message: message, err: err}
	}
}

func editCmd(backend gack.Backend, channel, ts, text string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		message, err := backend.EditMessage(ctx, channel, ts, text)
		return editResult{channel: channel, ts: ts, message: message, err: err}
	}
}

func copyCmd(value string) tea.Cmd {
	return func() tea.Msg { return copiedResult{err: clipboard.WriteAll(value)} }
}

func searchCmd(backend gack.Backend, query string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		results, err := backend.Search(ctx, query)
		return searchResult{results: results, err: err}
	}
}

func activityCmd(backend gack.Backend) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		items, err := backend.Activity(ctx)
		return activityResult{items: items, err: err}
	}
}

func interactionCmd(backend gack.Backend, interaction gack.Interaction) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := backend.Interact(ctx, interaction)
		return interactionResult{result: result, err: err}
	}
}

func reactionCmd(backend gack.Backend, channel, thread, ts, emoji string, remove bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := backend.ToggleReaction(ctx, channel, ts, emoji, remove)
		return reactionResult{channel: channel, thread: thread, ts: ts, emoji: emoji, remove: remove, err: err}
	}
}

func saveSidebarCmd(state *sidebarSaveState, save func(config.SidebarPreferences) error, preferences config.SidebarPreferences, notice string) tea.Cmd {
	revision := state.latest.Add(1)
	return func() tea.Msg {
		state.mutex.Lock()
		defer state.mutex.Unlock()
		// Commands are run asynchronously. If a newer preference has already
		// been issued, skipping this write prevents an older sort from winning
		// a race and appearing again after restart.
		if state.latest.Load() != revision {
			return sidebarSaved{revision: revision}
		}
		if save == nil {
			return sidebarSaved{revision: revision, notice: notice}
		}
		preferences.ChannelOrder = append([]string(nil), preferences.ChannelOrder...)
		return sidebarSaved{revision: revision, notice: notice, err: save(preferences)}
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		inputWidth := max(20, min(72, m.width-12))
		m.searchInput.Width = inputWidth
		m.findInput.Width = inputWidth
		m.composeInput.SetWidth(max(20, m.width-4))
		m.composeInput.SetHeight(min(6, max(3, m.height/5)))
		m.resizeDialogInputs()
		return m, nil
	case bootstrapResult:
		m.busy = ""
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.snapshot = msg.snapshot
		// Always establish the manual order before applying a computed view.
		// This also appends newly joined conversations without discarding the
		// user's existing arrangement.
		m.channels = config.ApplyOrder(msg.snapshot.Conversations, func(channel gack.Conversation) string { return channel.ID }, m.order)
		m.order = m.channelOrder()
		m.sortChannels(m.sidebarSort)
		m.activity = append([]gack.ActivityItem(nil), msg.snapshot.Activity...)
		m.ready = true
		if len(m.channels) == 0 {
			m.err = "No joined conversations are visible to this token"
			return m, nil
		}
		m.channel = firstInterestingChannel(m.channels)
		m.sidebarAt = m.channel + 2
		m.focus = focusMessages
		m.busy = "Loading messages…"
		return m, messagesCmd(m.backend, m.channels[m.channel].ID)
	case spinner.TickMsg:
		if m.busy == "" {
			return m, nil
		}
		var command tea.Cmd
		m.spin, command = m.spin.Update(msg)
		return m, command
	case versionResult:
		if msg.err == nil {
			m.updateAvailable = msg.latest
		}
		return m, nil
	case messagesResult:
		if msg.channel != m.currentChannelID() {
			return m, nil
		}
		m.busy = ""
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.err = ""
		m.messages = msg.messages
		m.message = len(m.messages) - 1
		return m, nil
	case threadResult:
		if msg.channel != m.currentChannelID() || msg.thread != m.threadTS {
			return m, nil
		}
		m.busy = ""
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.thread = msg.replies
		m.threadAt = len(m.thread) - 1
		m.focus = focusThread
		return m, nil
	case postResult:
		m.busy = ""
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.err = ""
		m.status = "Message sent"
		if msg.channel == m.currentChannelID() {
			if msg.thread != "" && msg.thread == m.threadTS {
				m.thread = appendBounded(m.thread, msg.message, 100)
				m.threadAt = len(m.thread) - 1
			} else if msg.thread == "" {
				m.messages = appendBounded(m.messages, msg.message, 100)
				m.message = len(m.messages) - 1
			}
		}
		return m, nil
	case editResult:
		m.busy = ""
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.err = ""
		m.status = "Message updated"
		if msg.channel == m.currentChannelID() {
			m.replaceMessage(msg.ts, msg.message)
		}
		return m, nil
	case searchResult:
		m.busy = ""
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.searchResults = msg.results
		m.searchRan = true
		m.searchAt = 0
		return m, nil
	case activityResult:
		m.busy = ""
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.activity = msg.items
		m.activityAt = min(m.activityAt, max(0, len(m.filteredActivity())-1))
		return m, nil
	case interactionResult:
		m.busy = ""
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.err = ""
		if len(msg.result.Errors) > 0 && m.dialog != nil {
			m.dialog.setErrors(msg.result.Errors)
			return m, nil
		}
		if msg.result.Replace != nil {
			next := newDialogState(*msg.result.Replace, m.width)
			next.parent = m.dialog
			m.dialog = next
			return m, nil
		}
		if msg.result.View != nil {
			m.dialog = newDialogState(*msg.result.View, m.width)
			return m, nil
		}
		m.dialog = nil
		if msg.result.Notice != "" {
			m.status = msg.result.Notice
		}
		if channel := m.currentChannelID(); channel != "" {
			return m, messagesCmd(m.backend, channel)
		}
		return m, nil
	case reactionResult:
		m.busy = ""
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.applyReaction(msg)
		m.status = ":" + msg.emoji + ": updated"
		return m, nil
	case sidebarSaved:
		if m.saveState != nil && msg.revision != m.saveState.latest.Load() {
			return m, nil
		}
		if msg.err != nil {
			m.err = "Could not save sidebar preferences: " + msg.err.Error()
		} else {
			m.status = msg.notice
		}
		return m, nil
	case copiedResult:
		if msg.err != nil {
			m.err = "Could not copy message: " + msg.err.Error()
		} else {
			m.status = "Message copied"
		}
		return m, nil
	case tea.MouseMsg:
		return m.updateMouse(msg)
	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

func (m *Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if !m.ready {
		if m.err != "" {
			switch key {
			case "r", "R", "enter":
				m.startConnecting()
				return m, tea.Batch(m.spin.Tick, bootstrapCmd(m.backend))
			case "l":
				m.exitAction = ExitLogin
				return m, tea.Quit
			}
		}
		if key == "q" || key == "esc" {
			return m, tea.Quit
		}
		return m, nil
	}
	if key == "u" && m.updateAvailable != "" {
		m.exitAction = ExitUpdate
		return m, tea.Quit
	}
	if m.dialog != nil {
		return m, m.updateDialog(msg)
	}
	switch m.overlay {
	case overlayGlobalSearch:
		return m, m.updateSearch(msg)
	case overlayFind:
		return m, m.updateFind(msg)
	case overlayAction, overlayReaction:
		return m, m.updatePicker(msg)
	case overlayHelp:
		if key == "esc" || key == "?" || key == "enter" {
			m.overlay = overlayNone
		}
		return m, nil
	}
	// Keep the command palette global, including while a draft is open. This is
	// also the byte terminals can map a physical Cmd+K chord to.
	if key == "ctrl+k" {
		m.openSearch()
		return m, nil
	}
	if m.focus == focusComposer {
		return m, m.updateComposer(msg)
	}
	switch key {
	case "ctrl+f":
		if m.mode == viewConversation {
			m.overlay = overlayFind
			m.findInput.Focus()
		}
		return m, nil
	case "?":
		m.overlay = overlayHelp
		return m, nil
	case "q":
		return m, tea.Quit
	case "R":
		m.err = ""
		if m.mode == viewActivity || m.mode == viewNotifications {
			m.busy = "Refreshing activity…"
			return m, activityCmd(m.backend)
		}
		if channel := m.currentChannelID(); channel != "" {
			m.busy = "Refreshing messages…"
			return m, messagesCmd(m.backend, channel)
		}
		return m, nil
	case "tab":
		m.cycleFocus(1)
		return m, nil
	case "shift+tab":
		m.cycleFocus(-1)
		return m, nil
	case "a":
		return m, m.openActivity(false)
	case "n":
		return m, m.openActivity(true)
	case "ctrl+up":
		if m.mode == viewConversation {
			return m, m.editLatestOwnMessage()
		}
		return m, nil
	case "esc":
		if m.threadTS != "" {
			m.threadTS, m.thread = "", nil
			m.focus = focusMessages
		} else if m.mode != viewConversation {
			m.mode = viewConversation
		}
		return m, nil
	}
	if m.focus == focusSidebar {
		return m, m.updateSidebar(key)
	}
	if m.mode == viewActivity || m.mode == viewNotifications {
		return m, m.updateActivity(key)
	}
	return m, m.updateConversation(key)
}

func (m *Model) startConnecting() {
	m.ready = false
	m.err = ""
	m.status = ""
	m.busy = "Connecting…"
	m.connectStarted = time.Now()
}

func (m *Model) updateSidebar(key string) tea.Cmd {
	selected := m.sidebarSelection()
	maximum := len(m.channels) + 1
	switch key {
	case "up", "k":
		selected = max(0, selected-1)
	case "down", "j":
		selected = min(maximum, selected+1)
	case "K":
		if selected >= 2 && selected-2 > 0 {
			m.beginManualReorder()
			m.reorderChannel(selected-2, selected-3)
			m.order = m.channelOrder()
			return m.saveSidebarPreferences("Manual channel order saved")
		}
	case "J":
		if selected >= 2 && selected-2 < len(m.channels)-1 {
			m.beginManualReorder()
			m.reorderChannel(selected-2, selected-1)
			m.order = m.channelOrder()
			return m.saveSidebarPreferences("Manual channel order saved")
		}
	case "s":
		m.sidebarSort = m.sidebarSort.Next()
		m.sortChannels(m.sidebarSort)
		return m.saveSidebarPreferences("Sidebar sort: " + m.sidebarSort.Label())
	case "enter", "right", "l":
		if selected == 0 {
			return m.openActivity(true)
		}
		if selected == 1 {
			return m.openActivity(false)
		}
		return m.openChannel(selected - 2)
	default:
		return nil
	}
	m.setSidebarSelection(selected)
	return nil
}

func (m *Model) updateConversation(key string) tea.Cmd {
	messages, selected := m.focusedMessages()
	if len(messages) == 0 {
		if key == "c" {
			m.openComposer("")
		}
		return nil
	}
	switch key {
	case "up", "k":
		m.setFocusedMessage(max(0, selected-1))
	case "down", "j":
		m.setFocusedMessage(min(len(messages)-1, selected+1))
	case "home", "g":
		m.setFocusedMessage(0)
	case "end", "G":
		m.setFocusedMessage(len(messages) - 1)
	case "t", "enter", "right", "l":
		if m.focus == focusThread {
			m.openComposer(m.threadTS)
			return nil
		}
		return m.openThread(messages[selected])
	case "left", "h":
		if m.focus == focusThread {
			m.focus = focusMessages
		} else {
			m.focus = focusSidebar
		}
	case "c":
		thread := ""
		if m.focus == focusThread {
			thread = m.threadTS
		}
		m.openComposer(thread)
	case "r":
		m.openReactionPicker(messages[selected])
	case "y":
		return copyCmd(messages[selected].Text)
	case "e":
		return m.openMessageEditor(messages[selected])
	case "i":
		return m.openActionPicker(messages[selected])
	default:
		if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
			index := int(key[0] - '1')
			return m.activateElement(messages[selected], index)
		}
	}
	return nil
}

func (m *Model) updateActivity(key string) tea.Cmd {
	items := m.filteredActivity()
	if len(items) == 0 {
		return nil
	}
	switch key {
	case "up", "k":
		m.activityAt = max(0, m.activityAt-1)
	case "down", "j":
		m.activityAt = min(len(items)-1, m.activityAt+1)
	case "enter", "right", "l":
		item := items[m.activityAt]
		for i, channel := range m.channels {
			if channel.ID == item.ChannelID {
				return m.openChannel(i)
			}
		}
	case "left", "h":
		m.focus = focusSidebar
	}
	return nil
}

func (m *Model) updateComposer(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.composeInput.Blur()
		m.composeInput.Reset()
		m.composeEditTS = ""
		m.focus = m.composeOrigin
		return nil
	case "ctrl+up":
		if strings.TrimSpace(m.composeInput.Value()) == "" && m.composeEditTS == "" {
			return m.editLatestOwnMessage()
		}
	case "ctrl+s", "alt+enter", "ctrl+enter":
		text := strings.TrimSpace(m.composeInput.Value())
		if text == "" {
			return nil
		}
		channel, thread, editTS := m.currentChannelID(), m.composeThread, m.composeEditTS
		m.composeInput.Reset()
		m.composeInput.Blur()
		m.composeEditTS = ""
		m.focus = m.composeOrigin
		if editTS != "" {
			m.busy = "Updating message…"
			return editCmd(m.backend, channel, editTS, text)
		}
		m.busy = "Sending message…"
		return postCmd(m.backend, channel, thread, text)
	}
	var cmd tea.Cmd
	m.composeInput, cmd = m.composeInput.Update(msg)
	return cmd
}

func (m *Model) updateSearch(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "ctrl+k":
		m.closeSearch()
		return nil
	case "up", "ctrl+p":
		m.searchAt = max(0, m.searchAt-1)
		return nil
	case "down", "ctrl+n":
		m.searchAt = min(max(0, m.searchItemCount()-1), m.searchAt+1)
		return nil
	case "enter":
		if m.searchRan {
			if m.searchAt < len(m.searchResults) {
				result := m.searchResults[m.searchAt]
				m.closeSearch()
				for i, channel := range m.channels {
					if channel.ID == result.ChannelID {
						return m.openChannel(i)
					}
				}
			}
			return nil
		}
		channels := m.filteredChannels(m.searchInput.Value())
		query := strings.TrimSpace(m.searchInput.Value())
		if len(channels) > 0 && m.searchAt < len(channels) {
			id := channels[m.searchAt].ID
			m.closeSearch()
			for i, channel := range m.channels {
				if channel.ID == id {
					return m.openChannel(i)
				}
			}
		}
		if query != "" {
			m.busy = "Searching…"
			m.searchRan = true
			return searchCmd(m.backend, query)
		}
		return nil
	}
	previous := m.searchInput.Value()
	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(msg)
	if m.searchInput.Value() != previous {
		m.searchRan = false
		m.searchResults = nil
		m.searchAt = 0
	}
	return cmd
}

func (m *Model) updateFind(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "ctrl+f":
		m.overlay = overlayNone
		m.findInput.Blur()
		return nil
	case "enter", "ctrl+n":
		m.findNext(1)
		return nil
	case "ctrl+p":
		m.findNext(-1)
		return nil
	}
	var cmd tea.Cmd
	m.findInput, cmd = m.findInput.Update(msg)
	return cmd
}

func (m *Model) updatePicker(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.closePicker()
	case "up", "k":
		m.pickerAt = max(0, m.pickerAt-1)
	case "down", "j":
		m.pickerAt = min(max(0, len(m.pickerOptions)-1), m.pickerAt+1)
	case "enter":
		if len(m.pickerOptions) == 0 {
			return nil
		}
		option := m.pickerOptions[m.pickerAt]
		if m.overlay == overlayReaction {
			message := m.currentSelectedMessage()
			remove := reactionIsMine(message, option.value)
			m.closePicker()
			m.busy = "Updating reaction…"
			return reactionCmd(m.backend, m.currentChannelID(), message.ThreadTS, message.TS, option.value, remove)
		}
		if m.pickerElement != nil {
			picked := *m.pickerElement
			m.closePicker()
			return m.runInteraction(picked.BlockID, picked.Element, option.value)
		}
	}
	return nil
}

func (m *Model) openChannel(index int) tea.Cmd {
	if index < 0 || index >= len(m.channels) {
		return nil
	}
	m.channel = index
	m.sidebarAt = index + 2
	m.messages = nil
	m.message = -1
	m.threadTS, m.thread = "", nil
	m.mode = viewConversation
	m.focus = focusMessages
	m.busy = "Loading messages…"
	m.err = ""
	return messagesCmd(m.backend, m.channels[index].ID)
}

func (m *Model) openThread(message gack.Message) tea.Cmd {
	m.threadTS = message.TS
	m.thread = nil
	m.threadAt = -1
	m.busy = "Loading thread…"
	return threadCmd(m.backend, m.currentChannelID(), message.TS)
}

func (m *Model) openActivity(notificationsOnly bool) tea.Cmd {
	if notificationsOnly {
		m.mode = viewNotifications
		m.sidebarAt = 0
	} else {
		m.mode = viewActivity
		m.sidebarAt = 1
	}
	m.focus = focusMessages
	m.activityAt = 0
	m.busy = "Loading activity…"
	return activityCmd(m.backend)
}

func (m *Model) openComposer(thread string) {
	m.composeOrigin = m.focus
	m.composeThread = thread
	m.composeEditTS = ""
	m.focus = focusComposer
	m.composeInput.Placeholder = "Write a message"
	if thread != "" {
		m.composeInput.Placeholder = "Reply in thread"
	}
	m.composeInput.Reset()
	m.composeInput.Focus()
}

func (m *Model) openMessageEditor(message gack.Message) tea.Cmd {
	if message.TS == "" {
		return nil
	}
	if message.UserID != m.snapshot.Self.ID {
		m.status = "You can only edit your own messages"
		return nil
	}
	if m.focus != focusComposer {
		m.composeOrigin = m.focus
	}
	m.composeThread = message.ThreadTS
	m.composeEditTS = message.TS
	m.composeInput.SetValue(message.Text)
	m.composeInput.CursorEnd()
	m.composeInput.Placeholder = "Edit message"
	m.composeInput.Focus()
	m.focus = focusComposer
	return nil
}

func (m *Model) editLatestOwnMessage() tea.Cmd {
	messages := m.messages
	if m.focus == focusThread || (m.focus == focusComposer && m.composeThread != "") {
		messages = m.thread
	}
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].UserID == m.snapshot.Self.ID {
			return m.openMessageEditor(messages[index])
		}
	}
	m.status = "No message of yours to edit here"
	return nil
}

func (m *Model) openSearch() {
	if m.focus == focusComposer {
		m.composeInput.Blur()
	}
	m.overlay = overlayGlobalSearch
	m.searchInput.SetValue("")
	m.searchInput.Focus()
	m.searchResults = nil
	m.searchAt = 0
	m.searchRan = false
}

func (m *Model) closeSearch() {
	m.overlay = overlayNone
	m.searchInput.Blur()
	if m.focus == focusComposer {
		m.composeInput.Focus()
	}
	m.searchResults = nil
	m.searchRan = false
}

func (m *Model) openReactionPicker(message gack.Message) {
	emojis := []pickerOption{{"👍  +1", "+1"}, {"❤️  heart", "heart"}, {"🎉  tada", "tada"}, {"👀  eyes", "eyes"}, {"✅  white_check_mark", "white_check_mark"}, {"🚀  rocket", "rocket"}, {"🙌  raised_hands", "raised_hands"}, {"😂  joy", "joy"}}
	m.overlay = overlayReaction
	m.pickerOptions = emojis
	m.pickerAt = 0
}

func (m *Model) openActionPicker(message gack.Message) tea.Cmd {
	elements := gack.InteractiveElements(message.Blocks)
	if len(elements) == 0 {
		m.status = "This message has no interactive Block Kit elements"
		return nil
	}
	if len(elements) == 1 {
		return m.activateElement(message, 0)
	}
	// A compact chooser for the common case. Selecting a select element opens
	// its options in a second step.
	m.overlay = overlayAction
	m.pickerOptions = nil
	for index, element := range elements {
		label := element.Element.Text
		if label == "" {
			label = element.Element.Placeholder
		}
		if label == "" {
			label = element.Element.ActionID
		}
		m.pickerOptions = append(m.pickerOptions, pickerOption{label: fmt.Sprintf("%d  %s", index+1, label), value: strconv.Itoa(index)})
	}
	m.pickerElement = &struct {
		BlockID string
		Element gack.Element
	}{BlockID: "__choose_element__", Element: gack.Element{ActionID: "__choose_element__", Value: message.TS}}
	m.pickerAt = 0
	return nil
}

func (m *Model) activateElement(message gack.Message, index int) tea.Cmd {
	elements := gack.InteractiveElements(message.Blocks)
	if index < 0 || index >= len(elements) {
		return nil
	}
	picked := elements[index]
	if len(picked.Element.Options) > 0 && picked.Element.Type != "button" {
		m.overlay = overlayAction
		m.pickerOptions = nil
		for _, option := range picked.Element.Options {
			m.pickerOptions = append(m.pickerOptions, pickerOption{label: option.Text, value: option.Value})
		}
		m.pickerElement = &struct {
			BlockID string
			Element gack.Element
		}{BlockID: picked.BlockID, Element: picked.Element}
		m.pickerAt = 0
		return nil
	}
	return m.runInteraction(picked.BlockID, picked.Element, picked.Element.Value)
}

func (m *Model) runInteraction(blockID string, element gack.Element, value string) tea.Cmd {
	if blockID == "__choose_element__" {
		index, _ := strconv.Atoi(value)
		return m.activateElement(m.currentSelectedMessage(), index)
	}
	message := m.currentSelectedMessage()
	m.busy = "Running “" + firstNonEmpty(element.Text, element.Placeholder, element.ActionID) + "”…"
	return interactionCmd(m.backend, gack.Interaction{
		Type: "block_actions", UserID: m.snapshot.Self.ID, ChannelID: m.currentChannelID(),
		MessageTS: message.TS, ThreadTS: message.ThreadTS, BlockID: blockID,
		ActionID: element.ActionID, ActionType: element.Type, Value: value,
	})
}

func (m *Model) closePicker() {
	m.overlay = overlayNone
	m.pickerOptions = nil
	m.pickerElement = nil
	m.pickerAt = 0
}

func (m *Model) findNext(direction int) {
	query := strings.ToLower(strings.TrimSpace(m.findInput.Value()))
	if query == "" {
		return
	}
	messages, at := m.focusedMessages()
	for offset := 1; offset <= len(messages); offset++ {
		candidate := (at + direction*offset) % len(messages)
		if candidate < 0 {
			candidate += len(messages)
		}
		if strings.Contains(strings.ToLower(messages[candidate].Text), query) {
			m.setFocusedMessage(candidate)
			m.status = fmt.Sprintf("Match %d of %d", candidate+1, len(messages))
			return
		}
	}
	m.status = "No match for “" + m.findInput.Value() + "”"
}

func (m *Model) cycleFocus(direction int) {
	available := []focus{focusSidebar, focusMessages}
	if m.threadTS != "" {
		available = append(available, focusThread)
	}
	index := slices.Index(available, m.focus)
	if index < 0 {
		index = 0
	}
	index = (index + direction + len(available)) % len(available)
	m.focus = available[index]
}

func (m *Model) sidebarSelection() int {
	return max(0, min(len(m.channels)+1, m.sidebarAt))
}

func (m *Model) setSidebarSelection(selected int) {
	m.sidebarAt = max(0, min(len(m.channels)+1, selected))
}

func (m *Model) focusedMessages() ([]gack.Message, int) {
	if m.focus == focusThread && len(m.thread) > 0 {
		return m.thread, max(0, m.threadAt)
	}
	return m.messages, max(0, m.message)
}

func (m *Model) setFocusedMessage(index int) {
	if m.focus == focusThread {
		m.threadAt = index
	} else {
		m.message = index
	}
}

func (m *Model) currentSelectedMessage() gack.Message {
	messages, index := m.focusedMessages()
	if index >= 0 && index < len(messages) {
		return messages[index]
	}
	return gack.Message{}
}

func (m *Model) currentChannelID() string {
	if m.channel < 0 || m.channel >= len(m.channels) {
		return ""
	}
	return m.channels[m.channel].ID
}

func (m *Model) filteredActivity() []gack.ActivityItem {
	if m.mode != viewNotifications {
		return m.activity
	}
	result := make([]gack.ActivityItem, 0, len(m.activity))
	for _, item := range m.activity {
		if item.Unread {
			result = append(result, item)
		}
	}
	return result
}

func (m *Model) filteredChannels(query string) []gack.Conversation {
	query = strings.ToLower(strings.TrimSpace(query))
	result := make([]gack.Conversation, 0, min(10, len(m.channels)))
	for _, channel := range m.channels {
		if query == "" || strings.Contains(strings.ToLower(channel.Label()+" "+channel.Topic), query) {
			result = append(result, channel)
			if len(result) == 10 {
				break
			}
		}
	}
	return result
}

func (m *Model) searchItemCount() int {
	if m.searchRan {
		return len(m.searchResults)
	}
	count := len(m.filteredChannels(m.searchInput.Value()))
	if strings.TrimSpace(m.searchInput.Value()) != "" {
		count++ // explicit "search messages" row
	}
	return count
}

func (m *Model) reorderChannel(from, to int) {
	if from < 0 || to < 0 || from >= len(m.channels) || to >= len(m.channels) || from == to {
		return
	}
	selectedID := m.currentChannelID()
	cursorID := ""
	if m.sidebarAt >= 2 && m.sidebarAt-2 < len(m.channels) {
		cursorID = m.channels[m.sidebarAt-2].ID
	}
	item := m.channels[from]
	if from < to {
		copy(m.channels[from:to], m.channels[from+1:to+1])
	} else {
		copy(m.channels[to+1:from+1], m.channels[to:from])
	}
	m.channels[to] = item
	for i := range m.channels {
		if m.channels[i].ID == selectedID {
			m.channel = i
		}
		if m.channels[i].ID == cursorID {
			m.sidebarAt = i + 2
		}
	}
}

func (m *Model) beginManualReorder() {
	if m.sidebarSort == config.SidebarSortManual {
		return
	}
	// A reorder begins from exactly what is on screen. This makes Shift+J/K
	// and dragging predictable even while a computed sort is active, while
	// leaving the saved manual order untouched until the first actual move.
	m.sidebarSort = config.SidebarSortManual
	m.order = m.channelOrder()
}

func (m *Model) sortChannels(sort config.SidebarSort) {
	sort = sort.Normalize()
	selectedID := m.currentChannelID()
	cursorID := ""
	if m.sidebarAt >= 2 && m.sidebarAt-2 < len(m.channels) {
		cursorID = m.channels[m.sidebarAt-2].ID
	}

	switch sort {
	case config.SidebarSortAlphabetical:
		slices.SortStableFunc(m.channels, compareChannelsAlphabetically)
	case config.SidebarSortAttention:
		slices.SortStableFunc(m.channels, compareChannelsByAttention)
	default:
		m.channels = config.ApplyOrder(m.channels, func(channel gack.Conversation) string { return channel.ID }, m.order)
	}
	m.sidebarSort = sort

	for index := range m.channels {
		if m.channels[index].ID == selectedID {
			m.channel = index
		}
		if m.channels[index].ID == cursorID {
			m.sidebarAt = index + 2
		}
	}
}

func compareChannelsAlphabetically(left, right gack.Conversation) int {
	if order := cmp.Compare(channelSortName(left), channelSortName(right)); order != 0 {
		return order
	}
	return cmp.Compare(left.ID, right.ID)
}

func compareChannelsByAttention(left, right gack.Conversation) int {
	if order := cmp.Compare(channelAttention(right), channelAttention(left)); order != 0 {
		return order
	}
	if order := cmp.Compare(right.Mentions, left.Mentions); order != 0 {
		return order
	}
	if order := cmp.Compare(right.Unread, left.Unread); order != 0 {
		return order
	}
	return compareChannelsAlphabetically(left, right)
}

func channelAttention(channel gack.Conversation) int {
	if channel.Mentions > 0 {
		return 3
	}
	if channel.Unread > 0 {
		return 2
	}
	if channel.IsFavorite {
		return 1
	}
	return 0
}

func channelSortName(channel gack.Conversation) string {
	name := channel.Name
	if channel.IsDM && channel.DisplayName != "" {
		// DisplayName carries the visual @ sigil. It should not force every DM
		// ahead of alphabetic channel names when the sidebar is sorted by name.
		name = strings.TrimPrefix(channel.DisplayName, "@")
	}
	return strings.ToLower(name)
}

func (m *Model) saveSidebarPreferences(notice string) tea.Cmd {
	if m.saveState == nil {
		m.saveState = &sidebarSaveState{}
	}
	return saveSidebarCmd(m.saveState, m.saveSidebar, config.SidebarPreferences{
		ChannelOrder: m.order,
		Sort:         m.sidebarSort,
	}, notice)
}

func (m *Model) sidebarSortLabel() string {
	return m.sidebarSort.Label()
}

func (m *Model) channelOrder() []string {
	result := make([]string, len(m.channels))
	for i, channel := range m.channels {
		result[i] = channel.ID
	}
	return result
}

func (m *Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.dialog != nil || m.overlay != overlayNone || !m.ready {
		return m, nil
	}
	if msg.Button == tea.MouseButtonWheelUp {
		if m.mode == viewConversation && m.focus != focusSidebar {
			messages, selected := m.focusedMessages()
			if len(messages) > 0 {
				m.setFocusedMessage(max(0, selected-2))
			}
		}
		return m, nil
	}
	if msg.Button == tea.MouseButtonWheelDown {
		if m.mode == viewConversation && m.focus != focusSidebar {
			messages, selected := m.focusedMessages()
			if len(messages) > 0 {
				m.setFocusedMessage(min(len(messages)-1, selected+2))
			}
		}
		return m, nil
	}
	channelIndex := m.visibleChannelStart + msg.Y - m.channelRowStart
	insideChannelList := msg.X >= 0 && msg.X < m.sidebarWidth() && channelIndex >= 0 && channelIndex < len(m.channels)
	switch msg.Action {
	case tea.MouseActionPress:
		if msg.Button == tea.MouseButtonLeft && insideChannelList {
			m.dragFrom, m.dragAt, m.dragMoved = channelIndex, channelIndex, false
			m.focus = focusSidebar
			m.sidebarAt = channelIndex + 2
		}
	case tea.MouseActionMotion:
		if m.dragFrom >= 0 && insideChannelList && channelIndex != m.dragAt {
			m.beginManualReorder()
			m.reorderChannel(m.dragAt, channelIndex)
			m.order = m.channelOrder()
			m.dragAt = channelIndex
			m.dragMoved = true
		}
	case tea.MouseActionRelease:
		if m.dragFrom >= 0 {
			moved := m.dragMoved
			at := m.dragAt
			m.dragFrom, m.dragAt, m.dragMoved = -1, -1, false
			if moved {
				return m, m.saveSidebarPreferences("Manual channel order saved")
			}
			return m, m.openChannel(at)
		}
	}
	return m, nil
}

func (m *Model) applyReaction(result reactionResult) {
	update := func(messages []gack.Message) {
		for i := range messages {
			if messages[i].TS != result.ts {
				continue
			}
			for j := range messages[i].Reactions {
				if messages[i].Reactions[j].Name != result.emoji {
					continue
				}
				if result.remove {
					messages[i].Reactions[j].Mine = false
					messages[i].Reactions[j].Count--
				} else {
					messages[i].Reactions[j].Mine = true
					messages[i].Reactions[j].Count++
				}
				return
			}
			if !result.remove {
				messages[i].Reactions = append(messages[i].Reactions, gack.Reaction{Name: result.emoji, Count: 1, Mine: true})
			}
		}
	}
	update(m.messages)
	update(m.thread)
}

func (m *Model) replaceMessage(ts string, replacement gack.Message) {
	update := func(messages []gack.Message) {
		for index := range messages {
			if messages[index].TS != ts {
				continue
			}
			original := messages[index]
			if replacement.TS == "" {
				replacement.TS = original.TS
			}
			if replacement.ChannelID == "" {
				replacement.ChannelID = original.ChannelID
			}
			if replacement.UserID == "" {
				replacement.UserID = original.UserID
			}
			if replacement.Username == "" {
				replacement.Username = original.Username
			}
			if replacement.Time.IsZero() {
				replacement.Time = original.Time
			}
			if replacement.ThreadTS == "" {
				replacement.ThreadTS = original.ThreadTS
			}
			if replacement.ReplyCount == 0 {
				replacement.ReplyCount = original.ReplyCount
			}
			if replacement.Reactions == nil {
				replacement.Reactions = original.Reactions
			}
			replacement.Edited = true
			messages[index] = replacement
		}
	}
	update(m.messages)
	update(m.thread)
}

func firstInterestingChannel(channels []gack.Conversation) int {
	for i, channel := range channels {
		if channel.Mentions > 0 {
			return i
		}
	}
	for i, channel := range channels {
		if channel.Unread > 0 {
			return i
		}
	}
	return 0
}

func appendBounded(messages []gack.Message, message gack.Message, limit int) []gack.Message {
	if len(messages) >= limit {
		copy(messages, messages[len(messages)-limit+1:])
		messages = messages[:limit-1]
	}
	return append(messages, message)
}

func reactionIsMine(message gack.Message, emoji string) bool {
	for _, reaction := range message.Reactions {
		if reaction.Name == emoji {
			return reaction.Mine
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "interaction"
}
