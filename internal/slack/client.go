package slack

import (
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
	slackapi "github.com/slack-go/slack"
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
	api          *slackapi.Client
	token        string
	baseURL      string
	http         responseLimitClient
	bridge       InteractionBridge
	messageLimit int
	channelLimit int

	mu     sync.RWMutex
	selfID string
	users  map[string]gack.User
}

func New(config Config) (*Client, error) {
	token := strings.TrimSpace(config.Token)
	if token == "" {
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

	// One context-aware retry on 429 matches Gack's previous transport without
	// retrying writes after ambiguous network failures. Typed SDK methods own the
	// form/query encoding, which prevents required Slack fields from silently
	// disappearing into an unsupported JSON body.
	retry := slackapi.DefaultRetryConfig()
	retry.MaxRetries = 1
	retry.RetryAfterDuration = time.Second
	retry.RetryAfterJitter = 0
	retry.Handlers = slackapi.DefaultRetryHandlers(retry)
	boundedHTTP := responseLimitClient{client: config.HTTPClient}
	api := slackapi.New(
		token,
		slackapi.OptionHTTPClient(boundedHTTP),
		slackapi.OptionAPIURL(config.BaseURL),
		slackapi.OptionRetryConfig(retry),
	)

	return &Client{
		api: api, token: token, baseURL: config.BaseURL, http: boundedHTTP,
		bridge: config.Bridge, messageLimit: config.MessageLimit,
		channelLimit: config.ConversationLimit, users: map[string]gack.User{},
	}, nil
}

// responseLimitClient preserves the old transport's response bound while the
// official SDK handles request construction and response decoding. A malformed
// proxy or endpoint therefore cannot make one API page grow without limit.
type responseLimitClient struct {
	client *http.Client
}

func (c responseLimitClient) Do(request *http.Request) (*http.Response, error) {
	response, err := c.client.Do(request)
	if err != nil || response == nil || response.Body == nil {
		return response, err
	}
	response.Body = struct {
		io.Reader
		io.Closer
	}{Reader: io.LimitReader(response.Body, 8<<20), Closer: response.Body}
	return response, nil
}

// BootstrapCore deliberately excludes users.list. Large workspaces can take
// many cursor pages to enumerate, while auth.test and users.conversations are
// enough to render a useful workspace and start loading message history.
func (c *Client) BootstrapCore(ctx context.Context) (gack.Snapshot, error) {
	bootstrapCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		auth     *slackapi.AuthTestResponse
		channels []gack.Conversation
		failure  struct {
			stage string
			err   error
		}
		failOnce sync.Once
		workers  sync.WaitGroup
	)
	fetch := func(stage string, run func() error) {
		defer workers.Done()
		if err := run(); err != nil {
			failOnce.Do(func() {
				failure.stage = stage
				failure.err = err
				cancel()
			})
		}
	}

	workers.Add(2)
	go fetch("auth", func() (err error) {
		auth, err = c.api.AuthTestContext(bootstrapCtx)
		return wrapAPIError("auth.test", err)
	})
	go fetch("conversations", func() error {
		var err error
		channels, err = c.loadConversations(bootstrapCtx)
		return err
	})
	workers.Wait()
	if failure.err != nil {
		if failure.stage == "conversations" {
			return gack.Snapshot{}, fmt.Errorf("load conversations: %w", failure.err)
		}
		return gack.Snapshot{}, failure.err
	}

	c.mu.Lock()
	c.selfID = auth.UserID
	users := c.users
	c.mu.Unlock()
	resolveConversationUsers(channels, users)
	self := users[auth.UserID]
	if self.ID == "" {
		self = gack.User{ID: auth.UserID, Name: auth.User, RealName: auth.User}
	}
	return gack.Snapshot{Team: auth.Team, Self: self, Users: users, Conversations: channels}, nil
}

// HydrateUsers enriches a progressively bootstrapped workspace without being a
// prerequisite for rendering it. The client cache also improves subsequent
// message conversions even when hydration finishes after history loading.
func (c *Client) HydrateUsers(ctx context.Context) (map[string]gack.User, error) {
	users, err := c.loadUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("load users: %w", err)
	}
	c.mu.Lock()
	c.users = users
	c.mu.Unlock()
	return users, nil
}

func (c *Client) Bootstrap(ctx context.Context) (gack.Snapshot, error) {
	bootstrapCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		auth     *slackapi.AuthTestResponse
		users    map[string]gack.User
		channels []gack.Conversation
		failure  struct {
			stage string
			err   error
		}
		failOnce sync.Once
		workers  sync.WaitGroup
	)
	fetch := func(stage string, run func() error) {
		defer workers.Done()
		if err := run(); err != nil {
			failOnce.Do(func() {
				failure.stage = stage
				failure.err = err
				cancel()
			})
		}
	}

	// These datasets are independent on the wire. DM labels are resolved from
	// the user map only after every request has completed, keeping startup to the
	// duration of the slowest pagination stream instead of the sum of all three.
	workers.Add(3)
	go fetch("auth", func() (err error) {
		auth, err = c.api.AuthTestContext(bootstrapCtx)
		return wrapAPIError("auth.test", err)
	})
	go fetch("users", func() error {
		var err error
		users, err = c.loadUsers(bootstrapCtx)
		return err
	})
	go fetch("conversations", func() error {
		var err error
		channels, err = c.loadConversations(bootstrapCtx)
		return err
	})
	workers.Wait()

	if failure.err != nil {
		switch failure.stage {
		case "users":
			return gack.Snapshot{}, fmt.Errorf("load users: %w", failure.err)
		case "conversations":
			return gack.Snapshot{}, fmt.Errorf("load conversations: %w", failure.err)
		default:
			return gack.Snapshot{}, failure.err
		}
	}

	resolveConversationUsers(channels, users)
	self := users[auth.UserID]
	if self.ID == "" {
		self = gack.User{ID: auth.UserID, Name: auth.User, RealName: auth.User}
	}
	c.mu.Lock()
	c.selfID = auth.UserID
	c.users = users
	c.mu.Unlock()
	return gack.Snapshot{Team: auth.Team, Self: self, Users: users, Conversations: channels}, nil
}

func (c *Client) loadUsers(ctx context.Context) (map[string]gack.User, error) {
	users := make(map[string]gack.User, min(c.channelLimit, 200))
	cursor := ""
	for len(users) < c.channelLimit {
		page := c.api.GetUsersPaginated(
			slackapi.GetUsersOptionLimit(min(200, c.channelLimit-len(users))),
			slackapi.GetUsersOptionCursor(cursor),
		)
		page, err := page.Next(ctx)
		if err != nil {
			return nil, wrapAPIError("users.list", err)
		}
		for _, source := range page.Users {
			// Deleted accounts still own historical messages. Keep their profile
			// metadata so old conversations do not degrade to "unknown".
			realName := source.Profile.DisplayName
			if realName == "" {
				realName = source.Profile.RealName
			}
			users[source.ID] = gack.User{ID: source.ID, Name: source.Name, RealName: realName, Emoji: source.Profile.StatusEmoji}
			if len(users) == c.channelLimit {
				break
			}
		}
		cursor = strings.TrimSpace(page.Cursor)
		if cursor == "" {
			break
		}
	}
	return users, nil
}

func (c *Client) loadConversations(ctx context.Context) ([]gack.Conversation, error) {
	result := make([]gack.Conversation, 0, min(c.channelLimit, 200))
	seen := make(map[string]struct{}, min(c.channelLimit, 200))
	cursor := ""
	for len(result) < c.channelLimit {
		// users.conversations returns only conversations the authenticated user
		// belongs to. conversations.list can enumerate many irrelevant public
		// channels and exhaust the whole bootstrap deadline.
		channels, nextCursor, err := c.getConversationsForUser(ctx, cursor, min(200, c.channelLimit-len(result)))
		if err != nil {
			return nil, err
		}
		for _, source := range channels {
			if source.IsArchived {
				continue
			}
			// Membership can change while paging. Do not turn a repeated API row
			// into a duplicate sidebar entry.
			if _, exists := seen[source.ID]; exists {
				continue
			}
			seen[source.ID] = struct{}{}
			name := source.Name
			if source.IsIM && name == "" {
				name = source.User
			}
			result = append(result, gack.Conversation{
				ID: source.ID, Name: name, UserID: source.User,
				Topic: source.Topic.Value, IsDM: source.IsIM, IsPrivate: source.IsPrivate,
				// users.conversations is membership-scoped and may omit is_member.
				IsMember: true, IsArchived: source.IsArchived,
				IsFavorite: source.IsStarred, Unread: source.UnreadCountDisplay, LastRead: source.LastRead,
			})
			if len(result) == c.channelLimit {
				break
			}
		}
		cursor = strings.TrimSpace(nextCursor)
		if cursor == "" {
			break
		}
	}
	return result, nil
}

// conversation is the official SDK model plus the one per-user field its
// Channel type does not currently expose. Keeping this extension confined to
// users.conversations preserves Slack favorites without reviving the generic
// map/JSON transport that previously lost required fields.
type conversation struct {
	slackapi.Channel
	IsStarred bool `json:"is_starred"`
}

type conversationsPage struct {
	OK       bool           `json:"ok"`
	Error    string         `json:"error"`
	Channels []conversation `json:"channels"`
	Metadata struct {
		NextCursor string   `json:"next_cursor"`
		Messages   []string `json:"messages"`
	} `json:"response_metadata"`
}

func (c *Client) getConversationsForUser(ctx context.Context, cursor string, limit int) ([]conversation, string, error) {
	values := url.Values{
		"token":            {c.token},
		"types":            {"public_channel,private_channel,mpim,im"},
		"exclude_archived": {"true"},
		"limit":            {strconv.Itoa(limit)},
	}
	if cursor != "" {
		values.Set("cursor", cursor)
	}
	body := values.Encode()
	for attempt := 0; attempt < 2; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"users.conversations", strings.NewReader(body))
		if err != nil {
			return nil, "", err
		}
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, err := c.http.Do(request)
		if err != nil {
			return nil, "", fmt.Errorf("Slack users.conversations: %w", err)
		}
		if response.StatusCode == http.StatusTooManyRequests && attempt == 0 {
			retryAfter, _ := strconv.Atoi(response.Header.Get("Retry-After"))
			if retryAfter <= 0 {
				retryAfter = 1
			}
			_ = response.Body.Close()
			timer := time.NewTimer(time.Duration(retryAfter) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, "", ctx.Err()
			case <-timer.C:
				continue
			}
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			_ = response.Body.Close()
			return nil, "", fmt.Errorf("Slack users.conversations: HTTP %s", response.Status)
		}
		var page conversationsPage
		decodeErr := json.NewDecoder(response.Body).Decode(&page)
		_ = response.Body.Close()
		if decodeErr != nil {
			return nil, "", fmt.Errorf("Slack users.conversations decode: %w", decodeErr)
		}
		if !page.OK {
			return nil, "", &APIError{Method: "users.conversations", Code: page.Error, Details: page.Metadata.Messages}
		}
		return page.Channels, strings.TrimSpace(page.Metadata.NextCursor), nil
	}
	return nil, "", errors.New("Slack users.conversations: retry exhausted")
}

func resolveConversationUsers(conversations []gack.Conversation, users map[string]gack.User) {
	for index := range conversations {
		conversation := &conversations[index]
		if !conversation.IsDM {
			continue
		}
		user, ok := users[conversation.UserID]
		if !ok {
			continue
		}
		conversation.Name = user.Name
		conversation.DisplayName = "@" + user.DisplayName()
	}
}

func (c *Client) Messages(ctx context.Context, channel string) ([]gack.Message, error) {
	page, err := c.MessagePage(ctx, channel, "")
	return page.Messages, err
}

func (c *Client) MessagePage(ctx context.Context, channel, cursor string) (gack.HistoryPage, error) {
	response, err := c.api.GetConversationHistoryContext(ctx, &slackapi.GetConversationHistoryParameters{
		ChannelID: channel, Cursor: cursor, Limit: c.messageLimit, IncludeAllMetadata: true,
	})
	if err != nil {
		return gack.HistoryPage{}, wrapAPIError("conversations.history", err)
	}
	result := c.convertMessages(channel, response.Messages)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return gack.HistoryPage{Messages: result, NextCursor: strings.TrimSpace(response.ResponseMetaData.NextCursor)}, nil
}

func (c *Client) Thread(ctx context.Context, channel, thread string) ([]gack.Message, error) {
	page, err := c.ThreadPage(ctx, channel, thread, "")
	return page.Messages, err
}

func (c *Client) ThreadPage(ctx context.Context, channel, thread, cursor string) (gack.HistoryPage, error) {
	messages, _, nextCursor, err := c.api.GetConversationRepliesContext(ctx, &slackapi.GetConversationRepliesParameters{
		ChannelID: channel, Timestamp: thread, Cursor: cursor, Limit: c.messageLimit,
	})
	if err != nil {
		return gack.HistoryPage{}, wrapAPIError("conversations.replies", err)
	}
	return gack.HistoryPage{
		Messages: c.convertMessages(channel, messages), NextCursor: strings.TrimSpace(nextCursor),
	}, nil
}

func (c *Client) convertMessages(channel string, messages []slackapi.Message) []gack.Message {
	c.mu.RLock()
	selfID := c.selfID
	users := c.users
	c.mu.RUnlock()
	result := make([]gack.Message, 0, len(messages))
	for _, source := range messages {
		var blocks []gack.Block
		if len(source.Blocks.BlockSet) > 0 {
			// The SDK performs the polymorphic Block Kit decode. Gack then keeps
			// only its compact, renderer-facing representation.
			if raw, err := json.Marshal(source.Blocks); err == nil {
				blocks, _ = gack.ParseBlocks(raw)
			}
		}
		username := source.Username
		if source.BotProfile != nil && source.BotProfile.Name != "" {
			username = source.BotProfile.Name
		}
		if username == "" {
			if user, ok := users[source.User]; ok {
				username = user.DisplayName()
			}
		}
		message := gack.Message{
			TS: source.Timestamp, ThreadTS: source.ThreadTimestamp, ChannelID: channel, UserID: source.User,
			Username: username, Text: source.Text, Time: parseTimestamp(source.Timestamp),
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
	options := []slackapi.MsgOption{slackapi.MsgOptionText(text, false)}
	if thread != "" {
		options = append(options, slackapi.MsgOptionTS(thread))
	}
	responseChannel, timestamp, message, err := c.api.PostMessageWithResponseContext(ctx, channel, options...)
	if err != nil {
		return gack.Message{}, wrapAPIError("chat.postMessage", err)
	}
	if message.Timestamp == "" {
		message.Timestamp = timestamp
	}
	if message.Text == "" {
		message.Text = text
	}
	converted := c.convertMessages(responseChannel, []slackapi.Message{message})
	if len(converted) == 0 {
		return gack.Message{TS: timestamp, ChannelID: responseChannel, Text: text, Time: parseTimestamp(timestamp)}, nil
	}
	return converted[0], nil
}

func (c *Client) EditMessage(ctx context.Context, channel, ts, text string) (gack.Message, error) {
	responseChannel, timestamp, responseText, err := c.api.UpdateMessageContext(ctx, channel, ts, slackapi.MsgOptionText(text, false))
	if err != nil {
		return gack.Message{}, wrapAPIError("chat.update", err)
	}
	if responseChannel == "" {
		responseChannel = channel
	}
	if timestamp == "" {
		timestamp = ts
	}
	if responseText == "" {
		responseText = text
	}
	c.mu.RLock()
	selfID := c.selfID
	self := c.users[selfID]
	c.mu.RUnlock()
	username := ""
	if self.ID != "" {
		username = self.DisplayName()
	}
	return gack.Message{
		TS: timestamp, ChannelID: responseChannel, UserID: selfID,
		Username: username, Text: responseText, Edited: true, Time: parseTimestamp(timestamp),
	}, nil
}

func (c *Client) ToggleReaction(ctx context.Context, channel, ts, emoji string, remove bool) error {
	emoji = strings.Trim(strings.TrimSpace(emoji), ":")
	item := slackapi.NewRefToMessage(channel, ts)
	if remove {
		return wrapAPIError("reactions.remove", c.api.RemoveReactionContext(ctx, emoji, item))
	}
	return wrapAPIError("reactions.add", c.api.AddReactionContext(ctx, emoji, item))
}

func (c *Client) Search(ctx context.Context, query string) ([]gack.SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	parameters := slackapi.NewSearchParameters()
	parameters.Count = min(c.messageLimit, 100)
	parameters.Sort = "timestamp"
	parameters.SortDirection = "desc"
	matches, err := c.api.SearchMessagesContext(ctx, query, parameters)
	if err != nil {
		return nil, wrapAPIError("search.messages", err)
	}
	result := make([]gack.SearchResult, 0, len(matches.Matches))
	for _, match := range matches.Matches {
		message := slackapi.Message{Msg: slackapi.Msg{
			Type: match.Type, User: match.User, Username: match.Username,
			Text: match.Text, Timestamp: match.Timestamp, Blocks: match.Blocks,
		}}
		converted := c.convertMessages(match.Channel.ID, []slackapi.Message{message})
		if len(converted) == 0 {
			continue
		}
		converted[0].ChannelName = match.Channel.Name
		result = append(result, gack.SearchResult{ChannelID: match.Channel.ID, ChannelName: match.Channel.Name, Message: converted[0]})
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

type APIError struct {
	Method  string
	Code    string
	Details []string
}

func (e *APIError) Error() string {
	message := fmt.Sprintf("Slack %s: %s", e.Method, e.Code)
	if len(e.Details) > 0 {
		message += " (" + strings.Join(e.Details, "; ") + ")"
	}
	return message
}

func wrapAPIError(method string, err error) error {
	if err == nil {
		return nil
	}
	var response slackapi.SlackErrorResponse
	if errors.As(err, &response) {
		return &APIError{Method: method, Code: response.Err, Details: response.ResponseMetadata.Messages}
	}
	return fmt.Errorf("Slack %s: %w", method, err)
}

func parseTimestamp(value string) time.Time {
	seconds, fraction, _ := strings.Cut(value, ".")
	sec, _ := strconv.ParseInt(seconds, 10, 64)
	fraction = (fraction + "000000000")[:9]
	nsec, _ := strconv.ParseInt(fraction, 10, 64)
	return time.Unix(sec, nsec)
}
