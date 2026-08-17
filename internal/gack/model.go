package gack

import (
	"context"
	"time"
)

// Backend is the deliberately small boundary between the terminal UI and Slack.
// The demo backend, Slack Web API backend, and interaction bridge all implement
// the same behavior so the UI never has to know where a message came from.
type Backend interface {
	Bootstrap(context.Context) (Snapshot, error)
	Messages(context.Context, string) ([]Message, error)
	Thread(context.Context, string, string) ([]Message, error)
	PostMessage(context.Context, string, string, string) (Message, error)
	ToggleReaction(context.Context, string, string, string, bool) error
	Search(context.Context, string) ([]SearchResult, error)
	Activity(context.Context) ([]ActivityItem, error)
	Interact(context.Context, Interaction) (InteractionResult, error)
}

type Snapshot struct {
	Team          string
	Self          User
	Users         map[string]User
	Conversations []Conversation
	Activity      []ActivityItem
}

type User struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	RealName string `json:"real_name,omitempty"`
	Presence string `json:"presence,omitempty"`
	Emoji    string `json:"emoji,omitempty"`
}

func (u User) DisplayName() string {
	if u.RealName != "" {
		return u.RealName
	}
	if u.Name != "" {
		return u.Name
	}
	return "unknown"
}

type Conversation struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Topic       string `json:"topic,omitempty"`
	IsDM        bool   `json:"is_dm,omitempty"`
	IsPrivate   bool   `json:"is_private,omitempty"`
	IsMember    bool   `json:"is_member,omitempty"`
	UserID      string `json:"user_id,omitempty"`
	Unread      int    `json:"unread,omitempty"`
	Mentions    int    `json:"mentions,omitempty"`
	LastRead    string `json:"last_read,omitempty"`
	IsArchived  bool   `json:"is_archived,omitempty"`
	IsFavorite  bool   `json:"is_favorite,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

func (c Conversation) Label() string {
	if c.DisplayName != "" {
		return c.DisplayName
	}
	if c.IsDM {
		return "@" + c.Name
	}
	if c.IsPrivate {
		return "🔒 " + c.Name
	}
	return "# " + c.Name
}

type Message struct {
	TS          string     `json:"ts"`
	ThreadTS    string     `json:"thread_ts,omitempty"`
	ChannelID   string     `json:"channel_id,omitempty"`
	ChannelName string     `json:"channel_name,omitempty"`
	UserID      string     `json:"user_id,omitempty"`
	Username    string     `json:"username,omitempty"`
	Text        string     `json:"text"`
	Time        time.Time  `json:"time"`
	Edited      bool       `json:"edited,omitempty"`
	ReplyCount  int        `json:"reply_count,omitempty"`
	Reactions   []Reaction `json:"reactions,omitempty"`
	Blocks      []Block    `json:"blocks,omitempty"`
}

type Reaction struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	Mine  bool   `json:"mine,omitempty"`
}

type Block struct {
	Type     string    `json:"type"`
	BlockID  string    `json:"block_id,omitempty"`
	Text     string    `json:"text,omitempty"`
	Label    string    `json:"label,omitempty"`
	Optional bool      `json:"optional,omitempty"`
	Elements []Element `json:"elements,omitempty"`
}

type Element struct {
	Type          string   `json:"type"`
	ActionID      string   `json:"action_id,omitempty"`
	Text          string   `json:"text,omitempty"`
	Value         string   `json:"value,omitempty"`
	InitialValue  string   `json:"initial_value,omitempty"`
	Placeholder   string   `json:"placeholder,omitempty"`
	Multiline     bool     `json:"multiline,omitempty"`
	Options       []Option `json:"options,omitempty"`
	InitialOption string   `json:"initial_option,omitempty"`
}

type Option struct {
	Text  string `json:"text"`
	Value string `json:"value"`
}

type ActivityItem struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	ChannelID   string    `json:"channel_id"`
	ChannelName string    `json:"channel_name"`
	MessageTS   string    `json:"message_ts"`
	Actor       string    `json:"actor"`
	Text        string    `json:"text"`
	Time        time.Time `json:"time"`
	Unread      bool      `json:"unread"`
}

type SearchResult struct {
	ChannelID   string  `json:"channel_id"`
	ChannelName string  `json:"channel_name"`
	Message     Message `json:"message"`
}

type Interaction struct {
	Type        string                    `json:"type"`
	TeamID      string                    `json:"team_id,omitempty"`
	UserID      string                    `json:"user_id,omitempty"`
	ChannelID   string                    `json:"channel_id,omitempty"`
	MessageTS   string                    `json:"message_ts,omitempty"`
	ThreadTS    string                    `json:"thread_ts,omitempty"`
	BlockID     string                    `json:"block_id,omitempty"`
	ActionID    string                    `json:"action_id,omitempty"`
	ActionType  string                    `json:"action_type,omitempty"`
	Value       string                    `json:"value,omitempty"`
	ViewID      string                    `json:"view_id,omitempty"`
	CallbackID  string                    `json:"callback_id,omitempty"`
	PrivateMeta string                    `json:"private_metadata,omitempty"`
	State       map[string]map[string]any `json:"state,omitempty"`
}

type InteractionResult struct {
	Notice  string            `json:"notice,omitempty"`
	View    *View             `json:"view,omitempty"`
	Replace *View             `json:"replace,omitempty"`
	Errors  map[string]string `json:"errors,omitempty"`
}

type View struct {
	ID              string  `json:"id,omitempty"`
	CallbackID      string  `json:"callback_id,omitempty"`
	PrivateMetadata string  `json:"private_metadata,omitempty"`
	Title           string  `json:"title"`
	Submit          string  `json:"submit,omitempty"`
	Close           string  `json:"close,omitempty"`
	Blocks          []Block `json:"blocks"`
}
