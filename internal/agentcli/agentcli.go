// Package agentcli implements gack's non-interactive, JSON-first interface.
// It deliberately depends only on gack.Backend so agents and scripts exercise
// the same bounded Slack access as the terminal UI.
package agentcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/0xdeafcafe/gack/internal/gack"
)

const (
	ExitOK       = 0
	ExitError    = 1
	ExitUsage    = 2
	ExitReadOnly = 3
)

// Options controls safeguards around the API command runner. Getenv exists so
// callers and tests do not need to mutate the process environment.
type Options struct {
	ReadOnly bool
	Getenv   func(string) string
}

type envelope struct {
	OK      bool       `json:"ok"`
	Command string     `json:"command"`
	Data    any        `json:"data,omitempty"`
	Error   *errorBody `json:"error,omitempty"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Usage   string `json:"usage,omitempty"`
}

type commandError struct {
	code    string
	message string
	usage   string
	exit    int
}

func (e *commandError) Error() string { return e.message }

// Run executes args as a gack API command and returns a process-style exit
// code. Successful responses are emitted as one JSON value on stdout; errors
// are emitted as one JSON value on stderr. Run never starts an interactive UI.
//
// The intended integration is:
//
//	status := agentcli.Run(ctx, backend, os.Args[2:], os.Stdout, os.Stderr, agentcli.Options{})
func Run(ctx context.Context, backend gack.Backend, args []string, stdout, stderr io.Writer, options Options) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if backend == nil {
		return writeError(stderr, "", &commandError{code: "backend_unavailable", message: "Slack backend is unavailable", exit: ExitError})
	}
	if len(args) == 0 {
		return writeError(stderr, "", usageError("missing API command", rootUsage))
	}

	command := strings.ToLower(strings.TrimSpace(args[0]))
	if command == "help" || command == "--help" || command == "-h" {
		if err := writeJSON(stdout, envelope{OK: true, Command: "help", Data: helpData()}); err != nil {
			return ExitError
		}
		return ExitOK
	}

	data, err := execute(ctx, backend, command, args[1:], options)
	if err != nil {
		var commandErr *commandError
		if !errors.As(err, &commandErr) {
			commandErr = &commandError{code: "backend_error", message: err.Error(), exit: ExitError}
		}
		return writeError(stderr, command, commandErr)
	}
	if err := writeJSON(stdout, envelope{OK: true, Command: command, Data: data}); err != nil {
		return ExitError
	}
	return ExitOK
}

func execute(ctx context.Context, backend gack.Backend, command string, args []string, options Options) (any, error) {
	switch command {
	case "channels":
		return runChannels(ctx, backend, args)
	case "messages":
		return runMessages(ctx, backend, args)
	case "thread":
		return runThread(ctx, backend, args)
	case "search":
		return runSearch(ctx, backend, args)
	case "activity":
		return runActivity(ctx, backend, args)
	case "send":
		if err := mutationAllowed(options, "send"); err != nil {
			return nil, err
		}
		return runSend(ctx, backend, args)
	case "edit":
		if err := mutationAllowed(options, "edit"); err != nil {
			return nil, err
		}
		return runEdit(ctx, backend, args)
	case "react":
		if err := mutationAllowed(options, "react"); err != nil {
			return nil, err
		}
		return runReact(ctx, backend, args)
	default:
		return nil, usageError(fmt.Sprintf("unknown API command %q", command), rootUsage)
	}
}

type channelsData struct {
	Team     string              `json:"team"`
	Self     gack.User           `json:"self"`
	Channels []gack.Conversation `json:"channels"`
}

func runChannels(ctx context.Context, backend gack.Backend, args []string) (channelsData, error) {
	parsed, err := parseArgs(args, map[string]optionSpec{
		"unread":      {canonical: "unread", boolean: true},
		"unread-only": {canonical: "unread", boolean: true},
	})
	if err != nil {
		return channelsData{}, usageError(err.Error(), "gack api channels [--unread]")
	}
	if len(parsed.positionals) != 0 {
		return channelsData{}, usageError("channels does not accept positional arguments", "gack api channels [--unread]")
	}
	snapshot, err := backend.Bootstrap(ctx)
	if err != nil {
		return channelsData{}, fmt.Errorf("load channels: %w", err)
	}
	channels := make([]gack.Conversation, 0, len(snapshot.Conversations))
	for _, channel := range snapshot.Conversations {
		if parsed.bools["unread"] && channel.Unread == 0 && channel.Mentions == 0 {
			continue
		}
		channels = append(channels, channel)
	}
	return channelsData{Team: snapshot.Team, Self: snapshot.Self, Channels: channels}, nil
}

type messagesData struct {
	Channel  gack.Conversation `json:"channel"`
	Messages []gack.Message    `json:"messages"`
}

func runMessages(ctx context.Context, backend gack.Backend, args []string) (messagesData, error) {
	parsed, err := parseArgs(args, map[string]optionSpec{"channel": {canonical: "channel"}})
	if err != nil {
		return messagesData{}, usageError(err.Error(), "gack api messages <channel>")
	}
	channelRef, rest, err := valueOrPosition(parsed, "channel")
	if err != nil || channelRef == "" || len(rest) != 0 {
		return messagesData{}, usageError("messages needs exactly one channel name or ID", "gack api messages <channel>")
	}
	snapshot, channel, err := resolveChannel(ctx, backend, channelRef)
	if err != nil {
		return messagesData{}, err
	}
	messages, err := backend.Messages(ctx, channel.ID)
	if err != nil {
		return messagesData{}, fmt.Errorf("load messages for %s: %w", channel.Label(), err)
	}
	enrichMessages(messages, snapshot, channel)
	if messages == nil {
		messages = []gack.Message{}
	}
	return messagesData{Channel: channel, Messages: messages}, nil
}

type threadData struct {
	Channel  gack.Conversation `json:"channel"`
	ThreadTS string            `json:"thread_ts"`
	Messages []gack.Message    `json:"messages"`
}

func runThread(ctx context.Context, backend gack.Backend, args []string) (threadData, error) {
	parsed, err := parseArgs(args, map[string]optionSpec{
		"channel": {canonical: "channel"},
		"ts":      {canonical: "ts"},
		"thread":  {canonical: "ts"},
	})
	if err != nil {
		return threadData{}, usageError(err.Error(), "gack api thread <channel> <thread-ts>")
	}
	channelRef, rest, err := valueOrPosition(parsed, "channel")
	if err != nil {
		return threadData{}, usageError(err.Error(), "gack api thread <channel> <thread-ts>")
	}
	threadTS, rest, err := optionOrNext(parsed, "ts", rest)
	if err != nil || channelRef == "" || threadTS == "" || len(rest) != 0 {
		return threadData{}, usageError("thread needs a channel name or ID and a thread timestamp", "gack api thread <channel> <thread-ts>")
	}
	snapshot, channel, err := resolveChannel(ctx, backend, channelRef)
	if err != nil {
		return threadData{}, err
	}
	messages, err := backend.Thread(ctx, channel.ID, threadTS)
	if err != nil {
		return threadData{}, fmt.Errorf("load thread %s in %s: %w", threadTS, channel.Label(), err)
	}
	enrichMessages(messages, snapshot, channel)
	if messages == nil {
		messages = []gack.Message{}
	}
	return threadData{Channel: channel, ThreadTS: threadTS, Messages: messages}, nil
}

type searchData struct {
	Query   string              `json:"query"`
	Results []gack.SearchResult `json:"results"`
}

func runSearch(ctx context.Context, backend gack.Backend, args []string) (searchData, error) {
	parsed, err := parseArgs(args, map[string]optionSpec{"query": {canonical: "query"}})
	if err != nil {
		return searchData{}, usageError(err.Error(), "gack api search <query>")
	}
	query := strings.TrimSpace(parsed.values["query"])
	if query != "" && len(parsed.positionals) != 0 {
		return searchData{}, usageError("search query was provided twice", "gack api search <query>")
	}
	if query == "" {
		query = strings.TrimSpace(strings.Join(parsed.positionals, " "))
	}
	if query == "" {
		return searchData{}, usageError("search needs a non-empty query", "gack api search <query>")
	}
	results, err := backend.Search(ctx, query)
	if err != nil {
		return searchData{}, fmt.Errorf("search Slack: %w", err)
	}
	if results == nil {
		results = []gack.SearchResult{}
	}
	return searchData{Query: query, Results: results}, nil
}

type activityData struct {
	Activity []gack.ActivityItem `json:"activity"`
}

func runActivity(ctx context.Context, backend gack.Backend, args []string) (activityData, error) {
	parsed, err := parseArgs(args, map[string]optionSpec{
		"unread":      {canonical: "unread", boolean: true},
		"unread-only": {canonical: "unread", boolean: true},
	})
	if err != nil {
		return activityData{}, usageError(err.Error(), "gack api activity [--unread]")
	}
	if len(parsed.positionals) != 0 {
		return activityData{}, usageError("activity does not accept positional arguments", "gack api activity [--unread]")
	}
	// Activity may derive mentions from the authenticated user's ID. Backends
	// such as Slack populate that identity during bootstrap, so the standalone
	// agent command must not depend on a prior command having run in-process.
	if _, err := backend.Bootstrap(ctx); err != nil {
		return activityData{}, fmt.Errorf("load workspace identity: %w", err)
	}
	activity, err := backend.Activity(ctx)
	if err != nil {
		return activityData{}, fmt.Errorf("load activity: %w", err)
	}
	filtered := make([]gack.ActivityItem, 0, len(activity))
	for _, item := range activity {
		if parsed.bools["unread"] && !item.Unread {
			continue
		}
		filtered = append(filtered, item)
	}
	return activityData{Activity: filtered}, nil
}

type mutationData struct {
	Action  string        `json:"action"`
	Channel string        `json:"channel"`
	Message *gack.Message `json:"message,omitempty"`
	TS      string        `json:"ts,omitempty"`
	Emoji   string        `json:"emoji,omitempty"`
	Removed bool          `json:"removed,omitempty"`
}

func runSend(ctx context.Context, backend gack.Backend, args []string) (mutationData, error) {
	parsed, err := parseArgs(args, map[string]optionSpec{
		"channel": {canonical: "channel"},
		"text":    {canonical: "text"},
		"thread":  {canonical: "thread"},
	})
	if err != nil {
		return mutationData{}, usageError(err.Error(), "gack api send <channel> <text> [--thread <thread-ts>]")
	}
	channelRef, rest, err := valueOrPosition(parsed, "channel")
	if err != nil {
		return mutationData{}, usageError(err.Error(), "gack api send <channel> <text> [--thread <thread-ts>]")
	}
	text, err := textOrRemaining(parsed, rest)
	if err != nil || channelRef == "" || text == "" {
		return mutationData{}, usageError("send needs a channel name or ID and non-empty text", "gack api send <channel> <text> [--thread <thread-ts>]")
	}
	_, channel, err := resolveChannel(ctx, backend, channelRef)
	if err != nil {
		return mutationData{}, err
	}
	message, err := backend.PostMessage(ctx, channel.ID, parsed.values["thread"], text)
	if err != nil {
		return mutationData{}, fmt.Errorf("send message to %s: %w", channel.Label(), err)
	}
	if message.ChannelName == "" {
		message.ChannelName = channel.Name
	}
	return mutationData{Action: "send", Channel: channel.ID, Message: &message}, nil
}

func runEdit(ctx context.Context, backend gack.Backend, args []string) (mutationData, error) {
	parsed, err := parseArgs(args, map[string]optionSpec{
		"channel": {canonical: "channel"},
		"ts":      {canonical: "ts"},
		"text":    {canonical: "text"},
	})
	if err != nil {
		return mutationData{}, usageError(err.Error(), "gack api edit <channel> <ts> <text>")
	}
	channelRef, rest, err := valueOrPosition(parsed, "channel")
	if err != nil {
		return mutationData{}, usageError(err.Error(), "gack api edit <channel> <ts> <text>")
	}
	ts, rest, err := optionOrNext(parsed, "ts", rest)
	if err != nil {
		return mutationData{}, usageError(err.Error(), "gack api edit <channel> <ts> <text>")
	}
	text, err := textOrRemaining(parsed, rest)
	if err != nil || channelRef == "" || ts == "" || text == "" {
		return mutationData{}, usageError("edit needs a channel name or ID, timestamp, and non-empty text", "gack api edit <channel> <ts> <text>")
	}
	_, channel, err := resolveChannel(ctx, backend, channelRef)
	if err != nil {
		return mutationData{}, err
	}
	message, err := backend.EditMessage(ctx, channel.ID, ts, text)
	if err != nil {
		return mutationData{}, fmt.Errorf("edit message %s in %s: %w", ts, channel.Label(), err)
	}
	if message.ChannelName == "" {
		message.ChannelName = channel.Name
	}
	return mutationData{Action: "edit", Channel: channel.ID, Message: &message}, nil
}

func runReact(ctx context.Context, backend gack.Backend, args []string) (mutationData, error) {
	parsed, err := parseArgs(args, map[string]optionSpec{
		"channel": {canonical: "channel"},
		"ts":      {canonical: "ts"},
		"emoji":   {canonical: "emoji"},
		"remove":  {canonical: "remove", boolean: true},
	})
	if err != nil {
		return mutationData{}, usageError(err.Error(), "gack api react <channel> <ts> <emoji> [--remove]")
	}
	channelRef, rest, err := valueOrPosition(parsed, "channel")
	if err != nil {
		return mutationData{}, usageError(err.Error(), "gack api react <channel> <ts> <emoji> [--remove]")
	}
	ts, rest, err := optionOrNext(parsed, "ts", rest)
	if err != nil {
		return mutationData{}, usageError(err.Error(), "gack api react <channel> <ts> <emoji> [--remove]")
	}
	emoji, rest, err := optionOrNext(parsed, "emoji", rest)
	emoji = strings.Trim(strings.TrimSpace(emoji), ":")
	if err != nil || channelRef == "" || ts == "" || emoji == "" || len(rest) != 0 {
		return mutationData{}, usageError("react needs a channel name or ID, timestamp, and emoji name", "gack api react <channel> <ts> <emoji> [--remove]")
	}
	_, channel, err := resolveChannel(ctx, backend, channelRef)
	if err != nil {
		return mutationData{}, err
	}
	remove := parsed.bools["remove"]
	if err := backend.ToggleReaction(ctx, channel.ID, ts, emoji, remove); err != nil {
		verb := "add"
		if remove {
			verb = "remove"
		}
		return mutationData{}, fmt.Errorf("%s reaction :%s: on %s: %w", verb, emoji, ts, err)
	}
	return mutationData{Action: "react", Channel: channel.ID, TS: ts, Emoji: emoji, Removed: remove}, nil
}

func mutationAllowed(options Options, command string) error {
	getenv := options.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	value := strings.ToLower(strings.TrimSpace(getenv("GACK_READ_ONLY")))
	readOnlyFromEnvironment := value != "" && value != "0" && value != "false" && value != "no" && value != "off"
	if !options.ReadOnly && !readOnlyFromEnvironment {
		return nil
	}
	return &commandError{
		code: "read_only", message: fmt.Sprintf("%s is disabled because gack is running in read-only mode", command),
		exit: ExitReadOnly,
	}
}

func resolveChannel(ctx context.Context, backend gack.Backend, reference string) (gack.Snapshot, gack.Conversation, error) {
	snapshot, err := backend.Bootstrap(ctx)
	if err != nil {
		return gack.Snapshot{}, gack.Conversation{}, fmt.Errorf("load channels: %w", err)
	}
	reference = strings.TrimSpace(reference)
	for _, channel := range snapshot.Conversations {
		if channel.ID == reference {
			return snapshot, channel, nil
		}
	}
	wanted := strings.ToLower(strings.TrimLeft(reference, "#@"))
	matches := make([]gack.Conversation, 0, 1)
	for _, channel := range snapshot.Conversations {
		name := strings.ToLower(strings.TrimLeft(channel.Name, "#@"))
		display := strings.ToLower(strings.TrimLeft(channel.DisplayName, "#@"))
		if wanted == name || (display != "" && wanted == display) {
			matches = append(matches, channel)
		}
	}
	if len(matches) == 1 {
		return snapshot, matches[0], nil
	}
	if len(matches) > 1 {
		ids := make([]string, 0, len(matches))
		for _, channel := range matches {
			ids = append(ids, channel.ID)
		}
		sort.Strings(ids)
		return gack.Snapshot{}, gack.Conversation{}, &commandError{
			code: "ambiguous_channel", message: fmt.Sprintf("channel %q is ambiguous; use one of these IDs: %s", reference, strings.Join(ids, ", ")), exit: ExitUsage,
		}
	}
	return gack.Snapshot{}, gack.Conversation{}, &commandError{
		code: "channel_not_found", message: fmt.Sprintf("channel %q was not found; run `gack api channels` to list available channels", reference), exit: ExitError,
	}
}

func enrichMessages(messages []gack.Message, snapshot gack.Snapshot, channel gack.Conversation) {
	for i := range messages {
		if messages[i].ChannelID == "" {
			messages[i].ChannelID = channel.ID
		}
		if messages[i].ChannelName == "" {
			messages[i].ChannelName = channel.Name
		}
		if messages[i].Username == "" {
			messages[i].Username = snapshot.Users[messages[i].UserID].DisplayName()
		}
	}
}

type optionSpec struct {
	canonical string
	boolean   bool
}

type parsedArgs struct {
	positionals []string
	values      map[string]string
	bools       map[string]bool
}

func parseArgs(args []string, specs map[string]optionSpec) (parsedArgs, error) {
	result := parsedArgs{values: map[string]string{}, bools: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		argument := args[i]
		if argument == "--" {
			result.positionals = append(result.positionals, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(argument, "--") || argument == "--" {
			result.positionals = append(result.positionals, argument)
			continue
		}
		nameValue := strings.SplitN(strings.TrimPrefix(argument, "--"), "=", 2)
		spec, ok := specs[nameValue[0]]
		if !ok {
			return parsedArgs{}, fmt.Errorf("unknown option --%s", nameValue[0])
		}
		if spec.boolean {
			value := true
			if len(nameValue) == 2 {
				parsed, err := strconv.ParseBool(nameValue[1])
				if err != nil {
					return parsedArgs{}, fmt.Errorf("--%s expects true or false", nameValue[0])
				}
				value = parsed
			}
			result.bools[spec.canonical] = value
			continue
		}
		value := ""
		if len(nameValue) == 2 {
			value = nameValue[1]
		} else {
			i++
			if i >= len(args) {
				return parsedArgs{}, fmt.Errorf("--%s needs a value", nameValue[0])
			}
			value = args[i]
		}
		if _, exists := result.values[spec.canonical]; exists {
			return parsedArgs{}, fmt.Errorf("--%s was provided more than once", nameValue[0])
		}
		result.values[spec.canonical] = value
	}
	return result, nil
}

func valueOrPosition(parsed parsedArgs, option string) (string, []string, error) {
	rest := parsed.positionals
	if value := strings.TrimSpace(parsed.values[option]); value != "" {
		return value, rest, nil
	}
	if len(rest) == 0 {
		return "", rest, nil
	}
	return rest[0], rest[1:], nil
}

func optionOrNext(parsed parsedArgs, option string, positionals []string) (string, []string, error) {
	if value := strings.TrimSpace(parsed.values[option]); value != "" {
		return value, positionals, nil
	}
	if len(positionals) == 0 {
		return "", positionals, nil
	}
	return positionals[0], positionals[1:], nil
}

func textOrRemaining(parsed parsedArgs, positionals []string) (string, error) {
	if value, exists := parsed.values["text"]; exists {
		if len(positionals) != 0 {
			return "", errors.New("text was provided both positionally and with --text")
		}
		return strings.TrimSpace(value), nil
	}
	return strings.TrimSpace(strings.Join(positionals, " ")), nil
}

func usageError(message, usage string) *commandError {
	return &commandError{code: "invalid_arguments", message: message, usage: usage, exit: ExitUsage}
}

func writeError(writer io.Writer, command string, err *commandError) int {
	if err.exit == 0 {
		err.exit = ExitError
	}
	_ = writeJSON(writer, envelope{OK: false, Command: command, Error: &errorBody{Code: err.code, Message: err.message, Usage: err.usage}})
	return err.exit
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

const rootUsage = "gack api <channels|messages|thread|search|activity|send|edit|react> [arguments]"

func helpData() map[string]any {
	return map[string]any{
		"usage": rootUsage,
		"commands": []map[string]string{
			{"name": "channels", "usage": "gack api channels [--unread]"},
			{"name": "messages", "usage": "gack api messages <channel>"},
			{"name": "thread", "usage": "gack api thread <channel> <thread-ts>"},
			{"name": "search", "usage": "gack api search <query>"},
			{"name": "activity", "usage": "gack api activity [--unread]"},
			{"name": "send", "usage": "gack api send <channel> <text> [--thread <thread-ts>]"},
			{"name": "edit", "usage": "gack api edit <channel> <ts> <text>"},
			{"name": "react", "usage": "gack api react <channel> <ts> <emoji> [--remove]"},
		},
		"safety": "Set GACK_READ_ONLY=1 to reject send, edit, and react.",
	}
}
