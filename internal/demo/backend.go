package demo

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/0xdeafcafe/gack/internal/gack"
)

type Backend struct {
	mu       sync.Mutex
	now      time.Time
	sequence int
	snapshot gack.Snapshot
	messages map[string][]gack.Message
}

func New() *Backend {
	now := time.Now().Truncate(time.Second)
	users := map[string]gack.User{
		"U_ME":     {ID: "U_ME", Name: "alex", RealName: "Alex", Presence: "active"},
		"U_MAYA":   {ID: "U_MAYA", Name: "maya", RealName: "Maya Chen", Presence: "active"},
		"U_RAF":    {ID: "U_RAF", Name: "raf", RealName: "Raf de Wit", Presence: "away"},
		"B_DEPLOY": {ID: "B_DEPLOY", Name: "shipyard", RealName: "Shipyard", Emoji: "🚢"},
	}
	conversations := []gack.Conversation{
		{ID: "C_GENERAL", Name: "general", Topic: "Company-wide announcements and work-based banter", IsMember: true, Unread: 3, Mentions: 1, IsFavorite: true},
		{ID: "C_PLATFORM", Name: "platform", Topic: "Platform engineering", IsMember: true, Unread: 7, IsFavorite: true},
		{ID: "C_INCIDENTS", Name: "incidents", Topic: "Production incident coordination", IsPrivate: true, IsMember: true, Unread: 1, Mentions: 1},
		{ID: "D_MAYA", Name: "maya", DisplayName: "@maya", IsDM: true, IsMember: true, UserID: "U_MAYA", Unread: 2},
	}
	b := &Backend{
		now:      now,
		snapshot: gack.Snapshot{Team: "Acme Engineering", Self: users["U_ME"], Users: users, Conversations: conversations},
		messages: map[string][]gack.Message{},
	}
	b.messages["C_GENERAL"] = []gack.Message{
		b.message("C_GENERAL", "U_MAYA", -90*time.Minute, "Morning! The release train is leaving at 14:00. Please add anything risky to the checklist."),
		b.message("C_GENERAL", "U_RAF", -44*time.Minute, "The database migration passed on staging. <@U_ME>, can you give the API changes one last look?"),
		b.message("C_GENERAL", "U_ME", -40*time.Minute, "On it — I’ll report back in the thread."),
	}
	b.messages["C_GENERAL"][1].ReplyCount = 3
	b.messages["C_GENERAL"][1].Reactions = []gack.Reaction{{Name: "eyes", Count: 2, Mine: true}, {Name: "+1", Count: 1}}
	b.messages["C_PLATFORM"] = []gack.Message{
		b.message("C_PLATFORM", "U_MAYA", -3*time.Hour, "PR #482 removes the last dependency on the old queue consumer."),
		b.deployMessage(-28 * time.Minute),
		b.message("C_PLATFORM", "U_RAF", -12*time.Minute, "The canary graphs look clean so far."),
	}
	b.messages["C_INCIDENTS"] = []gack.Message{
		b.message("C_INCIDENTS", "U_RAF", -21*time.Minute, "Elevated p99 on checkout in eu-west. Investigating."),
		b.incidentMessage(-18 * time.Minute),
	}
	b.messages["D_MAYA"] = []gack.Message{
		b.message("D_MAYA", "U_MAYA", -13*time.Minute, "Do you have five minutes to look at the deploy workflow?"),
		b.message("D_MAYA", "U_MAYA", -11*time.Minute, "The interactive step is the bit we cannot lose in a terminal client."),
	}
	root := b.messages["C_GENERAL"][1]
	b.messages["thread:"+root.TS] = []gack.Message{
		root,
		b.threadMessage("C_GENERAL", root.TS, "U_MAYA", -38*time.Minute, "API contract tests are green."),
		b.threadMessage("C_GENERAL", root.TS, "U_ME", -34*time.Minute, "Found one stale field in the mobile response; fixing now."),
		b.threadMessage("C_GENERAL", root.TS, "U_RAF", -31*time.Minute, "Thanks — I can hold the train for that."),
	}
	b.snapshot.Activity = []gack.ActivityItem{
		{ID: "A1", Kind: "mention", ChannelID: "C_GENERAL", ChannelName: "general", MessageTS: root.TS, Actor: "Maya Chen", Text: "can you give the API changes one last look?", Time: now.Add(-44 * time.Minute), Unread: true},
		{ID: "A2", Kind: "thread", ChannelID: "C_GENERAL", ChannelName: "general", MessageTS: root.TS, Actor: "Raf de Wit", Text: "replied to a thread you follow", Time: now.Add(-31 * time.Minute), Unread: true},
		{ID: "A3", Kind: "reaction", ChannelID: "C_PLATFORM", ChannelName: "platform", Actor: "Maya Chen", Text: "reacted :raised_hands: to your message", Time: now.Add(-2 * time.Hour)},
	}
	return b
}

func (b *Backend) message(channel, user string, offset time.Duration, text string) gack.Message {
	b.sequence++
	t := b.now.Add(offset)
	return gack.Message{TS: fmt.Sprintf("%d.%06d", t.Unix(), b.sequence), ChannelID: channel, UserID: user, Text: text, Time: t}
}

func (b *Backend) threadMessage(channel, thread, user string, offset time.Duration, text string) gack.Message {
	m := b.message(channel, user, offset, text)
	m.ThreadTS = thread
	return m
}

func (b *Backend) deployMessage(offset time.Duration) gack.Message {
	m := b.message("C_PLATFORM", "B_DEPLOY", offset, "Release 2026.08.17-rc3 is ready to deploy.")
	m.Blocks = []gack.Block{
		{Type: "section", BlockID: "release_summary", Text: "*Release candidate ready*\n`2026.08.17-rc3` · 18 changes · checks passing"},
		{Type: "actions", BlockID: "release_actions", Elements: []gack.Element{
			{Type: "button", ActionID: "deploy_start", Text: "Start deployment", Value: "2026.08.17-rc3"},
			{Type: "button", ActionID: "deploy_runbook", Text: "View runbook", Value: "deployments"},
		}},
	}
	return m
}

func (b *Backend) incidentMessage(offset time.Duration) gack.Message {
	m := b.message("C_INCIDENTS", "B_DEPLOY", offset, "Incident workflow is waiting for an owner.")
	m.Blocks = []gack.Block{
		{Type: "section", BlockID: "incident_summary", Text: "*:warning: Checkout latency*\nSeverity: unassigned · Region: eu-west"},
		{Type: "actions", BlockID: "incident_actions", Elements: []gack.Element{
			{Type: "button", ActionID: "incident_claim", Text: "Claim incident", Value: "INC-1042"},
			{Type: "static_select", ActionID: "incident_severity", Placeholder: "Set severity", Options: []gack.Option{{Text: "SEV-1", Value: "sev1"}, {Text: "SEV-2", Value: "sev2"}, {Text: "SEV-3", Value: "sev3"}}},
		}},
	}
	return m
}

func (b *Backend) Bootstrap(context.Context) (gack.Snapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snapshot, nil
}

func (b *Backend) Messages(_ context.Context, channel string) ([]gack.Message, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]gack.Message(nil), b.messages[channel]...), nil
}

func (b *Backend) Thread(_ context.Context, channel, thread string) ([]gack.Message, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if replies := b.messages["thread:"+thread]; len(replies) > 0 {
		return append([]gack.Message(nil), replies...), nil
	}
	for _, message := range b.messages[channel] {
		if message.TS == thread {
			return []gack.Message{message}, nil
		}
	}
	return nil, fmt.Errorf("thread %s not found", thread)
}

func (b *Backend) PostMessage(_ context.Context, channel, thread, text string) (gack.Message, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.now = b.now.Add(time.Second)
	m := b.message(channel, b.snapshot.Self.ID, 0, strings.TrimSpace(text))
	if thread != "" {
		m.ThreadTS = thread
		key := "thread:" + thread
		if len(b.messages[key]) == 0 {
			for _, root := range b.messages[channel] {
				if root.TS == thread {
					b.messages[key] = append(b.messages[key], root)
					break
				}
			}
		}
		b.messages[key] = append(b.messages[key], m)
		for i := range b.messages[channel] {
			if b.messages[channel][i].TS == thread {
				b.messages[channel][i].ReplyCount++
			}
		}
	} else {
		b.messages[channel] = append(b.messages[channel], m)
	}
	return m, nil
}

func (b *Backend) ToggleReaction(_ context.Context, channel, ts, emoji string, remove bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	update := func(messages []gack.Message) bool {
		for i := range messages {
			if messages[i].TS != ts {
				continue
			}
			for j := range messages[i].Reactions {
				if messages[i].Reactions[j].Name != emoji {
					continue
				}
				if remove && messages[i].Reactions[j].Mine {
					messages[i].Reactions[j].Mine = false
					messages[i].Reactions[j].Count--
				} else if !remove && !messages[i].Reactions[j].Mine {
					messages[i].Reactions[j].Mine = true
					messages[i].Reactions[j].Count++
				}
				return true
			}
			if !remove {
				messages[i].Reactions = append(messages[i].Reactions, gack.Reaction{Name: emoji, Count: 1, Mine: true})
			}
			return true
		}
		return false
	}
	update(b.messages[channel])
	for key, messages := range b.messages {
		if strings.HasPrefix(key, "thread:") {
			update(messages)
		}
	}
	return nil
}

func (b *Backend) Search(_ context.Context, query string) ([]gack.SearchResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	query = strings.ToLower(strings.TrimSpace(query))
	var results []gack.SearchResult
	for _, channel := range b.snapshot.Conversations {
		for _, message := range b.messages[channel.ID] {
			user := b.snapshot.Users[message.UserID]
			searchable := message.Text + " " + user.DisplayName() + " " + channel.Name
			for _, block := range message.Blocks {
				searchable += " " + block.Text
				for _, element := range block.Elements {
					searchable += " " + element.Text
				}
			}
			if query != "" && !strings.Contains(strings.ToLower(searchable), query) {
				continue
			}
			copy := message
			copy.ChannelName = channel.Name
			results = append(results, gack.SearchResult{ChannelID: channel.ID, ChannelName: channel.Name, Message: copy})
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Message.Time.After(results[j].Message.Time) })
	return results, nil
}

func (b *Backend) Activity(context.Context) ([]gack.ActivityItem, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]gack.ActivityItem(nil), b.snapshot.Activity...), nil
}

func (b *Backend) Interact(_ context.Context, in gack.Interaction) (gack.InteractionResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch in.ActionID {
	case "deploy_start":
		return gack.InteractionResult{View: deployView(in.Value)}, nil
	case "deploy_runbook":
		return gack.InteractionResult{Notice: "Runbook: verify canary → deploy one region → hold 10m → continue"}, nil
	case "incident_claim":
		return gack.InteractionResult{Notice: "Incident INC-1042 assigned to you"}, nil
	case "incident_severity":
		return gack.InteractionResult{Notice: "Incident severity set to " + strings.ToUpper(in.Value)}, nil
	}
	if in.Type != "view_submission" {
		return gack.InteractionResult{}, fmt.Errorf("unsupported demo interaction %q", in.ActionID)
	}
	switch in.CallbackID {
	case "deploy_configure":
		version := stateString(in.State, "release", "version")
		environment := stateString(in.State, "target", "environment")
		change := stateString(in.State, "change", "change_id")
		errors := map[string]string{}
		if strings.TrimSpace(version) == "" {
			errors["release"] = "Release is required"
		}
		if strings.TrimSpace(change) == "" {
			errors["change"] = "Change ticket is required"
		}
		if len(errors) > 0 {
			return gack.InteractionResult{Errors: errors}, nil
		}
		return gack.InteractionResult{Replace: confirmDeployView(version, environment, change)}, nil
	case "deploy_confirm":
		version := stateString(in.State, "confirm", "version")
		environment := stateString(in.State, "confirm", "environment")
		b.now = b.now.Add(time.Second)
		m := b.message("C_PLATFORM", "B_DEPLOY", 0, fmt.Sprintf("Deployment of %s to %s started by %s.", version, environment, b.snapshot.Self.DisplayName()))
		m.Reactions = []gack.Reaction{{Name: "rocket", Count: 1, Mine: true}}
		b.messages["C_PLATFORM"] = append(b.messages["C_PLATFORM"], m)
		return gack.InteractionResult{Notice: fmt.Sprintf("Deploying %s to %s", version, environment)}, nil
	}
	return gack.InteractionResult{}, fmt.Errorf("unsupported demo view %q", in.CallbackID)
}

func deployView(version string) *gack.View {
	return &gack.View{
		ID: "V_DEPLOY_CONFIG", CallbackID: "deploy_configure", Title: "Start deployment", Submit: "Review", Close: "Cancel",
		Blocks: []gack.Block{
			{
				Type: "input", BlockID: "release", Label: "Release",
				Elements: []gack.Element{{Type: "plain_text_input", ActionID: "version", InitialValue: version}},
			},
			{
				Type: "input", BlockID: "target", Label: "Environment",
				Elements: []gack.Element{{
					Type: "static_select", ActionID: "environment", InitialOption: "staging",
					Options: []gack.Option{{Text: "Staging", Value: "staging"}, {Text: "Production", Value: "production"}},
				}},
			},
			{
				Type: "input", BlockID: "change", Label: "Change ticket",
				Elements: []gack.Element{{Type: "plain_text_input", ActionID: "change_id", Placeholder: "CHG-1234"}},
			},
			{
				Type: "input", BlockID: "notes", Label: "Notes", Optional: true,
				Elements: []gack.Element{{Type: "plain_text_input", ActionID: "notes", Multiline: true, Placeholder: "Anything the on-call should know"}},
			},
		},
	}
}

func confirmDeployView(version, environment, change string) *gack.View {
	return &gack.View{
		ID: "V_DEPLOY_CONFIRM", CallbackID: "deploy_confirm", Title: "Confirm deployment", Submit: "Deploy", Close: "Back",
		Blocks: []gack.Block{
			{Type: "section", BlockID: "summary", Text: fmt.Sprintf("Deploy *%s* to *%s* under `%s`?", version, environment, change)},
			{Type: "input", BlockID: "confirm", Label: "Confirmation", Elements: []gack.Element{{Type: "checkboxes", ActionID: "approved", Options: []gack.Option{{Text: "I checked the release and want to deploy", Value: "yes"}}, InitialOption: "yes"}, {Type: "hidden", ActionID: "version", InitialValue: version}, {Type: "hidden", ActionID: "environment", InitialValue: environment}}},
		},
	}
}

func stateString(state map[string]map[string]any, block, action string) string {
	value, ok := state[block][action]
	if !ok {
		return ""
	}
	switch value := value.(type) {
	case string:
		return value
	case []string:
		return strings.Join(value, ",")
	default:
		return fmt.Sprint(value)
	}
}
