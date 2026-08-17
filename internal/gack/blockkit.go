package gack

import (
	"encoding/json"
	"fmt"
	"strings"
)

type slackText struct {
	Text string `json:"text"`
}

func (t *slackText) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '"' {
		return json.Unmarshal(data, &t.Text)
	}
	type textAlias slackText
	var decoded textAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*t = slackText(decoded)
	return nil
}

type slackOption struct {
	Text  slackText `json:"text"`
	Value string    `json:"value"`
}

type slackElement struct {
	Type           string          `json:"type"`
	ActionID       string          `json:"action_id"`
	Text           slackText       `json:"text"`
	Value          string          `json:"value"`
	Name           string          `json:"name"`
	Range          string          `json:"range"`
	URL            string          `json:"url"`
	UserID         string          `json:"user_id"`
	ChannelID      string          `json:"channel_id"`
	InitialValue   string          `json:"initial_value"`
	Placeholder    slackText       `json:"placeholder"`
	Multiline      bool            `json:"multiline"`
	Options        []slackOption   `json:"options"`
	InitialOption  *slackOption    `json:"initial_option"`
	InitialDate    string          `json:"initial_date"`
	InitialTime    string          `json:"initial_time"`
	InitialOptions []slackOption   `json:"initial_options"`
	Elements       []slackElement  `json:"elements"`
	Confirm        json.RawMessage `json:"confirm"`
}

type slackBlock struct {
	Type      string         `json:"type"`
	BlockID   string         `json:"block_id"`
	Text      slackText      `json:"text"`
	Label     slackText      `json:"label"`
	Optional  bool           `json:"optional"`
	Element   *slackElement  `json:"element"`
	Accessory *slackElement  `json:"accessory"`
	Elements  []slackElement `json:"elements"`
	Fields    []slackText    `json:"fields"`
	Title     slackText      `json:"title"`
	AltText   string         `json:"alt_text"`
}

// ParseBlocks keeps the parts of Block Kit that can be meaningfully rendered
// and operated with a keyboard. Unknown blocks are retained as labelled text so
// a workflow never disappears just because Slack added a new block type.
func ParseBlocks(raw json.RawMessage) ([]Block, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var source []slackBlock
	if err := json.Unmarshal(raw, &source); err != nil {
		return nil, fmt.Errorf("decode Block Kit: %w", err)
	}
	blocks := make([]Block, 0, len(source))
	for _, b := range source {
		block := Block{Type: b.Type, BlockID: b.BlockID, Text: b.Text.Text, Label: b.Label.Text, Optional: b.Optional}
		if block.Text == "" && b.Type == "rich_text" {
			block.Text = flattenRichText(b.Elements)
		}
		if block.Text == "" && (b.Type == "image" || b.Type == "video") {
			block.Text = strings.TrimSpace(strings.Join([]string{b.Title.Text, b.AltText}, " — "))
		}
		for _, field := range b.Fields {
			block.Elements = append(block.Elements, Element{Type: "field", Text: field.Text})
		}
		if b.Element != nil {
			block.Elements = append(block.Elements, convertElement(*b.Element))
		}
		if b.Accessory != nil {
			block.Elements = append(block.Elements, convertElement(*b.Accessory))
		}
		for _, element := range b.Elements {
			if b.Type == "rich_text" && element.ActionID == "" {
				continue
			}
			converted := convertElement(element)
			if converted.ActionID == "" && converted.Text != "" {
				converted.Type = "field"
			}
			if converted.ActionID != "" || converted.Text != "" {
				block.Elements = append(block.Elements, converted)
			}
		}
		if block.Text == "" && len(block.Elements) == 0 && b.Type != "divider" {
			block.Text = "Unsupported Block Kit block: " + b.Type
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

func convertElement(e slackElement) Element {
	initial := e.InitialValue
	if initial == "" {
		initial = e.InitialDate
	}
	if initial == "" {
		initial = e.InitialTime
	}
	result := Element{
		Type: e.Type, ActionID: e.ActionID, Text: e.Text.Text, Value: e.Value,
		InitialValue: initial, Placeholder: e.Placeholder.Text, Multiline: e.Multiline,
	}
	if result.Text == "" {
		result.Text = flattenRichText([]slackElement{e})
	}
	for _, option := range e.Options {
		result.Options = append(result.Options, Option{Text: option.Text.Text, Value: option.Value})
	}
	if e.InitialOption != nil {
		result.InitialOption = e.InitialOption.Value
	}
	if len(e.InitialOptions) > 0 {
		values := make([]string, 0, len(e.InitialOptions))
		for _, option := range e.InitialOptions {
			values = append(values, option.Value)
		}
		result.InitialOption = strings.Join(values, ",")
	}
	return result
}

func flattenRichText(elements []slackElement) string {
	var builder strings.Builder
	var walk func([]slackElement)
	walk = func(items []slackElement) {
		for _, item := range items {
			switch item.Type {
			case "rich_text_list":
				for _, child := range item.Elements {
					builder.WriteString("• ")
					walk(child.Elements)
					builder.WriteByte('\n')
				}
			case "rich_text_quote":
				builder.WriteString("> ")
				walk(item.Elements)
				builder.WriteByte('\n')
			case "rich_text_preformatted":
				builder.WriteString("`\n")
				walk(item.Elements)
				builder.WriteString("\n`")
			default:
				if len(item.Elements) > 0 {
					walk(item.Elements)
				}
				switch item.Type {
				case "link":
					label := item.Text.Text
					switch {
					case item.URL == "":
						builder.WriteString(label)
					case label == "" || label == item.URL:
						builder.WriteString(item.URL)
					default:
						// Keep Slack's mrkdwn link form until the UI resolves it.
						// A terminal cannot attach a URL to styled text invisibly,
						// so this becomes "label (URL)" at render time.
						builder.WriteString("<" + item.URL + "|" + label + ">")
					}
				case "emoji":
					builder.WriteString(":" + item.Name + ":")
				case "user":
					builder.WriteString("<@" + item.UserID + ">")
				case "channel":
					builder.WriteString("<#" + item.ChannelID + ">")
				case "broadcast":
					rangeName := item.Range
					if rangeName == "" {
						rangeName = item.Name
					}
					builder.WriteString("@" + rangeName)
				default:
					builder.WriteString(item.Text.Text)
				}
			}
		}
	}
	walk(elements)
	return strings.TrimSpace(builder.String())
}

func InteractiveElements(blocks []Block) []struct {
	BlockID string
	Element Element
} {
	var result []struct {
		BlockID string
		Element Element
	}
	for _, block := range blocks {
		for _, element := range block.Elements {
			if element.ActionID == "" || element.Type == "field" {
				continue
			}
			result = append(result, struct {
				BlockID string
				Element Element
			}{BlockID: block.BlockID, Element: element})
		}
	}
	return result
}
