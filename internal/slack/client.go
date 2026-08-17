package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/0xdeafcafe/gack/internal/gack"
)

const defaultBaseURL = "https://slack.com/api/"

type InteractionBridge interface {
	Interact(context.Context, gack.Interaction) (gack.InteractionResult, error)
}

type Config struct {
	Token             string
	BaseURL           string
	HTTPClient        *http.Client
	Bridge            InteractionBridge
	MessageLimit      int
	ConversationLimit int
}

type Client struct {
	token        string
	baseURL      string
	http         *http.Client
	bridge       InteractionBridge
	messageLimit int
	channelLimit int

	mu     sync.RWMutex
	selfID string
	users  map[string]gack.User
}

func New(config Config) (*Client, error) {
	if strings.TrimSpace(config.Token) == "" {
		return nil, errors.New("Slack token is empty")
	}
	if config.BaseURL == "" {
		config.BaseURL = defaultBaseURL
	}
	if !strings.HasSuffix(config.BaseURL, "/") {
		config.BaseURL += "/"
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 20 * time.Second}
	}
	if config.MessageLimit <= 0 {
		// Slack currently caps some internal apps at 15 history items per
		// request. Keeping the default at that limit also bounds memory.
		config.MessageLimit = 15
	}
	if config.MessageLimit > 100 {
		config.MessageLimit = 100
	}
	if config.ConversationLimit <= 0 {
		config.ConversationLimit = 500
	}
	return &Client{
		token: config.Token, baseURL: config.BaseURL, http: config.HTTPClient,
		bridge: config.Bridge, messageLimit: config.MessageLimit,
		channelLimit: config.ConversationLimit, users: map[string]gack.User{},
	}, nil
}

type authResponse struct {
	UserID string `json:"user_id"`
	User   string `json:"user"`
	Team   string `json:"team"`
}

type usersResponse struct {
	Members []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Deleted bool   `json:"deleted"`
		IsBot   bool   `json:"is_bot"`
		Profile struct {
			RealName    string `json:"real_name"`
			DisplayName string `json:"display_name"`
			StatusEmoji string `json:"status_emoji"`
		} `json:"profile"`
	} `json:"members"`
	ResponseMetadata struct {
		NextCursor string `json:"next_cursor"`
	} `json:"response_metadata"`
}

type conversationsResponse struct {
	Channels []struct {
		ID                 string `json:"id"`
		Name               string `json:"name"`
		User               string `json:"user"`
		IsIM               bool   `json:"is_im"`
		IsMPIM             bool   `json:"is_mpim"`
		IsPrivate          bool   `json:"is_private"`
		IsMember           bool   `json:"is_member"`
		IsArchived         bool   `json:"is_archived"`
		IsStarred          bool   `json:"is_starred"`
		UnreadCountDisplay int    `json:"unread_count_display"`
		LastRead           string `json:"last_read"`
		Topic              struct {
			Value string `json:"value"`
		} `json:"topic"`
	} `json:"channels"`
	ResponseMetadata struct {
		NextCursor string `json:"next_cursor"`
	} `json:"response_metadata"`
}

func (c *Client) Bootstrap(ctx context.Context) (gack.Snapshot, error) {
	var auth authResponse
	if err := c.call(ctx, "auth.test", nil, &auth); err != nil {
		return gack.Snapshot{}, err
	}
	users, err := c.loadUsers(ctx)
	if err != nil {
		return gack.Snapshot{}, fmt.Errorf("load users: %w", err)
	}
	c.mu.Lock()
	c.selfID = auth.UserID
	c.users = users
	c.mu.Unlock()

	channels, err := c.loadConversations(ctx, users)
	if err != nil {
		return gack.Snapshot{}, fmt.Errorf("load conversations: %w", err)
	}
	self := users[auth.UserID]
	if self.ID == "" {
		self = gack.User{ID: auth.UserID, Name: auth.User, RealName: auth.User}
	}
	return gack.Snapshot{Team: auth.Team, Self: self, Users: users, Conversations: channels}, nil
}

func (c *Client) loadUsers(ctx context.Context) (map[string]gack.User, error) {
	users := map[string]gack.User{}
	cursor := ""
	for len(users) < c.channelLimit {
		var response usersResponse
		params := map[string]any{"limit": min(200, c.channelLimit-len(users))}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := c.call(ctx, "users.list", params, &response); err != nil {
			return nil, err
		}
		for _, source := range response.Members {
			if source.Deleted {
				continue
			}
			realName := source.Profile.DisplayName
			if realName == "" {
				realName = source.Profile.RealName
			}
			users[source.ID] = gack.User{ID: source.ID, Name: source.Name, RealName: realName, Emoji: source.Profile.StatusEmoji}
		}
		cursor = strings.TrimSpace(response.ResponseMetadata.NextCursor)
		if cursor == "" {
			break
		}
	}
	return users, nil
}

func (c *Client) loadConversations(ctx context.Context, users map[string]gack.User) ([]gack.Conversation, error) {
	var result []gack.Conversation
	seen := make(map[string]struct{}, c.channelLimit)
	cursor := ""
	for len(result) < c.channelLimit {
		var response conversationsResponse
		params := map[string]any{
			"types": "public_channel,private_channel,mpim,im", "exclude_archived": true,
			"limit": min(200, c.channelLimit-len(result)),
		}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := c.call(ctx, "conversations.list", params, &response); err != nil {
			return nil, err
		}
		for _, source := range response.Channels {
			if source.IsArchived || (!source.IsMember && !source.IsIM) {
				continue
			}
			// A conversation can occasionally be repeated across cursor pages while
			// membership is changing. Never turn that transport quirk into duplicate
			// sidebar rows.
			if _, exists := seen[source.ID]; exists {
				continue
			}
			seen[source.ID] = struct{}{}
			name := source.Name
			display := ""
			if source.IsIM {
				user := users[source.User]
				name = user.Name
				display = "@" + user.DisplayName()
			}
			result = append(result, gack.Conversation{
				ID: source.ID, Name: name, DisplayName: display, UserID: source.User,
				Topic: source.Topic.Value, IsDM: source.IsIM, IsPrivate: source.IsPrivate,
				IsMember: source.IsMember || source.IsIM, IsArchived: source.IsArchived,
				IsFavorite: source.IsStarred, Unread: source.UnreadCountDisplay, LastRead: source.LastRead,
			})
		}
		cursor = strings.TrimSpace(response.ResponseMetadata.NextCursor)
		if cursor == "" {
			break
		}
	}
	return result, nil
}

type slackReaction struct {
	Name  string   `json:"name"`
	Count int      `json:"count"`
	Users []string `json:"users"`
}

type slackMessage struct {
	Type       string          `json:"type"`
	TS         string          `json:"ts"`
	ThreadTS   string          `json:"thread_ts"`
	User       string          `json:"user"`
	Username   string          `json:"username"`
	Text       string          `json:"text"`
	ReplyCount int             `json:"reply_count"`
	Blocks     json.RawMessage `json:"blocks"`
	Reactions  []slackReaction `json:"reactions"`
	Edited     *struct{}       `json:"edited"`
	BotProfile *struct {
		Name string `json:"name"`
	} `json:"bot_profile"`
}

type messagesResponse struct {
	Messages         []slackMessage `json:"messages"`
	ResponseMetadata struct {
		NextCursor string `json:"next_cursor"`
	} `json:"response_metadata"`
}

func (c *Client) Messages(ctx context.Context, channel string) ([]gack.Message, error) {
	var response messagesResponse
	if err := c.call(ctx, "conversations.history", map[string]any{"channel": channel, "limit": c.messageLimit, "include_all_metadata": true}, &response); err != nil {
		return nil, err
	}
	result := c.convertMessages(channel, response.Messages)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result, nil
}

func (c *Client) Thread(ctx context.Context, channel, thread string) ([]gack.Message, error) {
	var response messagesResponse
	if err := c.call(ctx, "conversations.replies", map[string]any{"channel": channel, "ts": thread, "limit": c.messageLimit}, &response); err != nil {
		return nil, err
	}
	return c.convertMessages(channel, response.Messages), nil
}

func (c *Client) convertMessages(channel string, messages []slackMessage) []gack.Message {
	c.mu.RLock()
	selfID := c.selfID
	users := c.users
	c.mu.RUnlock()
	result := make([]gack.Message, 0, len(messages))
	for _, source := range messages {
		blocks, _ := gack.ParseBlocks(source.Blocks)
		username := source.Username
		if source.BotProfile != nil && source.BotProfile.Name != "" {
			username = source.BotProfile.Name
		}
		if username == "" {
			username = users[source.User].DisplayName()
		}
		message := gack.Message{
			TS: source.TS, ThreadTS: source.ThreadTS, ChannelID: channel, UserID: source.User,
			Username: username, Text: source.Text, Time: parseTimestamp(source.TS),
			Edited: source.Edited != nil, ReplyCount: source.ReplyCount, Blocks: blocks,
		}
		for _, reaction := range source.Reactions {
			mine := false
			for _, user := range reaction.Users {
				if user == selfID {
					mine = true
					break
				}
			}
			message.Reactions = append(message.Reactions, gack.Reaction{Name: reaction.Name, Count: reaction.Count, Mine: mine})
		}
		result = append(result, message)
	}
	return result
}

func (c *Client) PostMessage(ctx context.Context, channel, thread, text string) (gack.Message, error) {
	params := map[string]any{"channel": channel, "text": text}
	if thread != "" {
		params["thread_ts"] = thread
	}
	var response struct {
		Channel string       `json:"channel"`
		TS      string       `json:"ts"`
		Message slackMessage `json:"message"`
	}
	if err := c.call(ctx, "chat.postMessage", params, &response); err != nil {
		return gack.Message{}, err
	}
	converted := c.convertMessages(response.Channel, []slackMessage{response.Message})
	if len(converted) == 0 {
		return gack.Message{TS: response.TS, ChannelID: response.Channel, Text: text, Time: parseTimestamp(response.TS)}, nil
	}
	return converted[0], nil
}

func (c *Client) EditMessage(ctx context.Context, channel, ts, text string) (gack.Message, error) {
	var response struct {
		Channel string       `json:"channel"`
		TS      string       `json:"ts"`
		Text    string       `json:"text"`
		Message slackMessage `json:"message"`
	}
	if err := c.call(ctx, "chat.update", map[string]any{"channel": channel, "ts": ts, "text": text}, &response); err != nil {
		return gack.Message{}, err
	}
	if response.Message.TS == "" {
		response.Message.TS = response.TS
	}
	if response.Message.Text == "" {
		response.Message.Text = response.Text
	}
	converted := c.convertMessages(response.Channel, []slackMessage{response.Message})
	if len(converted) == 0 {
		return gack.Message{TS: response.TS, ChannelID: channel, Text: text, Edited: true, Time: parseTimestamp(response.TS)}, nil
	}
	converted[0].Edited = true
	return converted[0], nil
}

func (c *Client) ToggleReaction(ctx context.Context, channel, ts, emoji string, remove bool) error {
	method := "reactions.add"
	if remove {
		method = "reactions.remove"
	}
	emoji = strings.Trim(strings.TrimSpace(emoji), ":")
	return c.call(ctx, method, map[string]any{"channel": channel, "timestamp": ts, "name": emoji}, nil)
}

type searchResponse struct {
	Messages struct {
		Matches []struct {
			slackMessage
			Channel struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"channel"`
		} `json:"matches"`
	} `json:"messages"`
}

func (c *Client) Search(ctx context.Context, query string) ([]gack.SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	var response searchResponse
	if err := c.call(ctx, "search.messages", map[string]any{"query": query, "count": min(c.messageLimit, 100), "sort": "timestamp", "sort_dir": "desc"}, &response); err != nil {
		return nil, err
	}
	result := make([]gack.SearchResult, 0, len(response.Messages.Matches))
	for _, match := range response.Messages.Matches {
		messages := c.convertMessages(match.Channel.ID, []slackMessage{match.slackMessage})
		if len(messages) == 0 {
			continue
		}
		messages[0].ChannelName = match.Channel.Name
		result = append(result, gack.SearchResult{ChannelID: match.Channel.ID, ChannelName: match.Channel.Name, Message: messages[0]})
	}
	return result, nil
}

func (c *Client) Activity(ctx context.Context) ([]gack.ActivityItem, error) {
	c.mu.RLock()
	selfID := c.selfID
	c.mu.RUnlock()
	if selfID == "" {
		return nil, errors.New("client has not been bootstrapped")
	}
	results, err := c.Search(ctx, "<@"+selfID+">")
	if err != nil {
		return nil, err
	}
	activity := make([]gack.ActivityItem, 0, len(results))
	for _, result := range results {
		activity = append(activity, gack.ActivityItem{
			ID: result.Message.TS, Kind: "mention", ChannelID: result.ChannelID,
			ChannelName: result.ChannelName, MessageTS: result.Message.TS,
			Actor: result.Message.Username, Text: result.Message.Text,
			Time: result.Message.Time, Unread: true,
		})
	}
	return activity, nil
}

func (c *Client) Interact(ctx context.Context, interaction gack.Interaction) (gack.InteractionResult, error) {
	if c.bridge == nil {
		return gack.InteractionResult{}, errors.New("this Block Kit action needs GACK_INTERACTION_URL; Slack has no public API for third-party clients to click another app's controls")
	}
	c.mu.RLock()
	if interaction.UserID == "" {
		interaction.UserID = c.selfID
	}
	c.mu.RUnlock()
	return c.bridge.Interact(ctx, interaction)
}

type apiEnvelope struct {
	OK      bool            `json:"ok"`
	Error   string          `json:"error"`
	Warning string          `json:"warning"`
	Raw     json.RawMessage `json:"-"`
}

type APIError struct {
	Method string
	Code   string
}

func (e *APIError) Error() string { return fmt.Sprintf("Slack %s: %s", e.Method, e.Code) }

func (c *Client) call(ctx context.Context, method string, params map[string]any, output any) error {
	if params == nil {
		params = map[string]any{}
	}
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+url.PathEscape(method), bytes.NewReader(body))
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+c.token)
		request.Header.Set("Content-Type", "application/json; charset=utf-8")
		response, err := c.http.Do(request)
		if err != nil {
			return fmt.Errorf("Slack %s: %w", method, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		response.Body.Close()
		if readErr != nil {
			return fmt.Errorf("Slack %s response: %w", method, readErr)
		}
		if response.StatusCode == http.StatusTooManyRequests && attempt == 0 {
			delay, _ := strconv.Atoi(response.Header.Get("Retry-After"))
			if delay <= 0 {
				delay = 1
			}
			timer := time.NewTimer(time.Duration(delay) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
				continue
			}
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("Slack %s: HTTP %s", method, response.Status)
		}
		var envelope apiEnvelope
		if err := json.Unmarshal(data, &envelope); err != nil {
			return fmt.Errorf("Slack %s decode: %w", method, err)
		}
		if !envelope.OK {
			return &APIError{Method: method, Code: envelope.Error}
		}
		if output != nil {
			if err := json.Unmarshal(data, output); err != nil {
				return fmt.Errorf("Slack %s decode payload: %w", method, err)
			}
		}
		return nil
	}
	return fmt.Errorf("Slack %s: retry exhausted", method)
}

func parseTimestamp(value string) time.Time {
	seconds, fraction, _ := strings.Cut(value, ".")
	sec, _ := strconv.ParseInt(seconds, 10, 64)
	fraction = (fraction + "000000000")[:9]
	nsec, _ := strconv.ParseInt(fraction, 10, 64)
	return time.Unix(sec, nsec)
}
