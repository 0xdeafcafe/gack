package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/0xdeafcafe/gack/internal/gack"
)

type dialogState struct {
	view   gack.View
	fields []dialogField
	at     int
	parent *dialogState
}

type dialogField struct {
	block    gack.Block
	element  gack.Element
	input    textinput.Model
	selected int
	checked  map[string]bool
	error    string
	visible  bool
}

func newDialogState(view gack.View, width int) *dialogState {
	dialog := &dialogState{view: view, at: -1}
	for _, block := range view.Blocks {
		for _, element := range block.Elements {
			if element.ActionID == "" {
				continue
			}
			field := dialogField{block: block, element: element, selected: -1, checked: map[string]bool{}, visible: element.Type != "hidden"}
			input := textinput.New()
			input.Prompt = ""
			input.Placeholder = element.Placeholder
			input.Width = max(16, min(64, width-20))
			input.SetValue(element.InitialValue)
			field.input = input
			for index, option := range element.Options {
				if option.Value == element.InitialOption {
					field.selected = index
				}
			}
			if strings.Contains(element.Type, "multi_") || element.Type == "checkboxes" {
				for _, value := range strings.Split(element.InitialOption, ",") {
					if value != "" {
						field.checked[value] = true
					}
				}
				if len(element.Options) > 0 {
					field.selected = max(0, field.selected)
				}
			}
			dialog.fields = append(dialog.fields, field)
		}
	}
	dialog.at = dialog.firstVisible()
	dialog.focusCurrent()
	return dialog
}

func (d *dialogState) firstVisible() int {
	for i := range d.fields {
		if d.fields[i].visible {
			return i
		}
	}
	return -1
}

func (d *dialogState) focusCurrent() {
	for i := range d.fields {
		d.fields[i].input.Blur()
	}
	if d.at >= 0 && d.at < len(d.fields) && isTextField(d.fields[d.at].element.Type) {
		d.fields[d.at].input.Focus()
	}
}

func (d *dialogState) move(direction int) {
	if len(d.fields) == 0 {
		return
	}
	start := d.at
	if start < 0 {
		start = 0
	}
	for offset := 1; offset <= len(d.fields); offset++ {
		candidate := (start + direction*offset) % len(d.fields)
		if candidate < 0 {
			candidate += len(d.fields)
		}
		if d.fields[candidate].visible {
			d.at = candidate
			d.focusCurrent()
			return
		}
	}
}

func (d *dialogState) setErrors(errors map[string]string) {
	for i := range d.fields {
		d.fields[i].error = errors[d.fields[i].block.BlockID]
	}
}

func (d *dialogState) validate() bool {
	valid := true
	for i := range d.fields {
		field := &d.fields[i]
		field.error = ""
		if field.block.Type != "input" || field.block.Optional || field.element.Type == "hidden" {
			continue
		}
		empty := false
		switch {
		case isTextField(field.element.Type):
			empty = strings.TrimSpace(field.input.Value()) == ""
		case field.element.Type == "checkboxes" || strings.Contains(field.element.Type, "multi_"):
			empty = len(field.checked) == 0
		case len(field.element.Options) > 0:
			empty = field.selected < 0
		}
		if empty {
			field.error = "Required"
			valid = false
			if d.at < 0 {
				d.at = i
			}
		}
	}
	return valid
}

func (d *dialogState) state() map[string]map[string]any {
	state := map[string]map[string]any{}
	for _, field := range d.fields {
		if state[field.block.BlockID] == nil {
			state[field.block.BlockID] = map[string]any{}
		}
		var value any
		switch {
		case field.element.Type == "hidden":
			value = field.element.InitialValue
		case isTextField(field.element.Type):
			value = field.input.Value()
		case field.element.Type == "checkboxes" || strings.Contains(field.element.Type, "multi_"):
			values := make([]string, 0, len(field.checked))
			for _, option := range field.element.Options {
				if field.checked[option.Value] {
					values = append(values, option.Value)
				}
			}
			value = values
		case field.selected >= 0 && field.selected < len(field.element.Options):
			value = field.element.Options[field.selected].Value
		default:
			value = field.element.Value
		}
		state[field.block.BlockID][field.element.ActionID] = value
	}
	return state
}

func (m *Model) updateDialog(msg tea.KeyMsg) tea.Cmd {
	if m.dialog == nil {
		return nil
	}
	dialog := m.dialog
	switch msg.String() {
	case "esc":
		if dialog.parent != nil {
			m.dialog = dialog.parent
		} else {
			m.dialog = nil
		}
		return nil
	case "tab":
		dialog.move(1)
		return nil
	case "shift+tab":
		dialog.move(-1)
		return nil
	case "ctrl+s":
		if !dialog.validate() {
			dialog.focusCurrent()
			return nil
		}
		m.busy = "Submitting “" + dialog.view.Title + "”…"
		return interactionCmd(m.backend, gack.Interaction{
			Type: "view_submission", UserID: m.snapshot.Self.ID,
			ChannelID: m.currentChannelID(), ViewID: dialog.view.ID,
			CallbackID: dialog.view.CallbackID, PrivateMeta: dialog.view.PrivateMetadata,
			State: dialog.state(),
		})
	}
	if dialog.at < 0 || dialog.at >= len(dialog.fields) {
		return nil
	}
	field := &dialog.fields[dialog.at]
	field.error = ""
	switch {
	case isTextField(field.element.Type):
		var cmd tea.Cmd
		field.input, cmd = field.input.Update(msg)
		return cmd
	case field.element.Type == "checkboxes" || strings.Contains(field.element.Type, "multi_"):
		switch msg.String() {
		case "up", "left", "k", "h":
			field.selected = max(0, field.selected-1)
		case "down", "right", "j", "l":
			field.selected = min(len(field.element.Options)-1, field.selected+1)
		case " ", "enter":
			if field.selected >= 0 && field.selected < len(field.element.Options) {
				value := field.element.Options[field.selected].Value
				field.checked[value] = !field.checked[value]
			}
		}
	case len(field.element.Options) > 0:
		switch msg.String() {
		case "up", "left", "k", "h":
			field.selected--
			if field.selected < 0 {
				field.selected = len(field.element.Options) - 1
			}
		case "down", "right", "j", "l", " ", "enter":
			field.selected = (field.selected + 1) % len(field.element.Options)
		}
	case field.element.Type == "button" && msg.String() == "enter":
		m.busy = "Running “" + field.element.Text + "”…"
		return interactionCmd(m.backend, gack.Interaction{
			Type: "block_actions", UserID: m.snapshot.Self.ID, ChannelID: m.currentChannelID(),
			ViewID: dialog.view.ID, CallbackID: dialog.view.CallbackID,
			BlockID: field.block.BlockID, ActionID: field.element.ActionID,
			ActionType: field.element.Type, Value: field.element.Value, State: dialog.state(),
		})
	}
	return nil
}

func (m *Model) resizeDialogInputs() {
	if m.dialog == nil {
		return
	}
	for i := range m.dialog.fields {
		m.dialog.fields[i].input.Width = max(16, min(64, m.width-20))
	}
}

func isTextField(elementType string) bool {
	switch elementType {
	case "plain_text_input", "rich_text_input", "email_text_input", "url_text_input", "number_input", "datepicker", "timepicker", "datetimepicker", "external_select", "users_select", "conversations_select", "channels_select":
		return true
	default:
		return false
	}
}
