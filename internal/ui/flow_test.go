package ui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/0xdeafcafe/gack/internal/demo"
	"github.com/0xdeafcafe/gack/internal/gack"
)

type countingBackend struct {
	gack.Backend
	bootstrapCalls int
	messageCalls   int
}

type progressiveBackend struct {
	gack.Backend
	mutex          sync.Mutex
	coreCalls      int
	usersCalls     int
	messageCalls   int
	users          map[string]gack.User
	userHydrateErr error
}

func (backend *progressiveBackend) BootstrapCore(ctx context.Context) (gack.Snapshot, error) {
	backend.mutex.Lock()
	backend.coreCalls++
	backend.mutex.Unlock()
	snapshot, err := backend.Backend.Bootstrap(ctx)
	snapshot.Users = nil
	snapshot.Self = gack.User{ID: snapshot.Self.ID, Name: snapshot.Self.Name}
	for index := range snapshot.Conversations {
		if snapshot.Conversations[index].IsDM {
			snapshot.Conversations[index].DisplayName = ""
			snapshot.Conversations[index].Name = snapshot.Conversations[index].UserID
		}
	}
	return snapshot, err
}

func (backend *progressiveBackend) HydrateUsers(context.Context) (map[string]gack.User, error) {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	backend.usersCalls++
	return backend.users, backend.userHydrateErr
}

func (backend *progressiveBackend) Messages(ctx context.Context, channel string) ([]gack.Message, error) {
	backend.mutex.Lock()
	backend.messageCalls++
	backend.mutex.Unlock()
	return backend.Backend.Messages(ctx, channel)
}

func (backend *countingBackend) Bootstrap(ctx context.Context) (gack.Snapshot, error) {
	backend.bootstrapCalls++
	return backend.Backend.Bootstrap(ctx)
}

func (backend *countingBackend) Messages(ctx context.Context, channel string) ([]gack.Message, error) {
	backend.messageCalls++
	return backend.Backend.Messages(ctx, channel)
}

func TestReactorsRunOutsideReducerAndReturnApplicationEvents(t *testing.T) {
	backend := &countingBackend{Backend: demo.New()}
	model := New(backend, nil, nil)
	model.startConnecting()

	bootstrap := bootstrapCmd(backend)
	if backend.bootstrapCalls != 0 {
		t.Fatal("creating an effect performed I/O")
	}
	message := bootstrap()
	event, ok := message.(applicationEvent)
	if !ok || backend.bootstrapCalls != 1 {
		t.Fatalf("reactor returned %T after %d calls", message, backend.bootstrapCalls)
	}

	next := model.reduce(event)
	if next == nil || !model.ready || backend.messageCalls != 0 {
		t.Fatalf("reducer performed I/O or failed to describe next effect: ready=%v calls=%d", model.ready, backend.messageCalls)
	}
	nextEvent, ok := next().(applicationEvent)
	if !ok || backend.messageCalls != 1 {
		t.Fatalf("message reactor returned %T after %d calls", nextEvent, backend.messageCalls)
	}
	model.reduce(nextEvent)
	if len(model.messages) == 0 || model.busy != "" {
		t.Fatal("message event was not reduced into state")
	}
}

func TestApplicationEffectOwnsItsTimeout(t *testing.T) {
	effect := applicationEffect{timeout: time.Millisecond, run: func(ctx context.Context) applicationEvent {
		<-ctx.Done()
		return versionResult{err: ctx.Err()}
	}}
	event, ok := effect.command()().(versionResult)
	if !ok || !errors.Is(event.err, context.DeadlineExceeded) {
		t.Fatalf("timeout event = %#v", event)
	}
}

func TestProgressiveBootstrapReducesOutOfOrderAndLoadsMessagesOnce(t *testing.T) {
	demoBackend := demo.New()
	full, err := demoBackend.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	backend := &progressiveBackend{Backend: demoBackend, users: full.Users}
	model := New(backend, nil, nil)
	model.startConnecting()
	model.bootstrapRequest = 1
	model.usersRequest = 1
	model.usersLoading = true

	// A fast users.list response may reach Update before the core workspace.
	if command := model.reduce(usersResult{request: 1, users: full.Users}); command != nil {
		t.Fatal("user hydration unexpectedly started another effect")
	}
	if model.ready || !model.usersReady {
		t.Fatalf("early users event made workspace ready=%v, users ready=%v", model.ready, model.usersReady)
	}

	core, err := backend.BootstrapCore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	load := model.reduce(bootstrapResult{request: 1, progressive: true, snapshot: core})
	if load == nil || !model.ready || model.snapshot.Self.DisplayName() != full.Self.DisplayName() {
		t.Fatalf("core did not merge hydrated identity: ready=%v self=%#v", model.ready, model.snapshot.Self)
	}
	dm := ""
	for _, channel := range model.channels {
		if channel.IsDM {
			dm = channel.Label()
		}
	}
	if dm != "@Maya Chen" {
		t.Fatalf("hydrated DM label = %q", dm)
	}
	if duplicate := model.reduce(bootstrapResult{request: 1, progressive: true, snapshot: core}); duplicate != nil {
		t.Fatal("duplicate core event started a second history load")
	}
	model.reduce(load().(applicationEvent))
	if backend.messageCalls != 1 {
		t.Fatalf("message loads = %d, want 1", backend.messageCalls)
	}
}

func TestUserHydrationFailureIsNonfatalAndRetryIsDeduplicated(t *testing.T) {
	demoBackend := demo.New()
	full, _ := demoBackend.Bootstrap(context.Background())
	backend := &progressiveBackend{
		Backend:        demoBackend,
		users:          full.Users,
		userHydrateErr: errors.New("Slack users.list: ratelimited"),
	}
	model := New(backend, nil, nil)
	model.Update(structWindowSize(100, 30))
	model.startConnecting()
	model.bootstrapRequest = 1
	model.usersRequest = 1
	model.usersLoading = true
	core, _ := backend.BootstrapCore(context.Background())
	load := model.reduce(bootstrapResult{request: 1, progressive: true, snapshot: core})
	model.reduce(load().(applicationEvent))
	model.reduce(usersResult{request: 1, err: backend.userHydrateErr})

	if !model.ready || model.err != "" || !strings.Contains(model.usersErr, "ratelimited") {
		t.Fatalf("hydration failure poisoned workspace: ready=%v err=%q usersErr=%q", model.ready, model.err, model.usersErr)
	}
	if view := model.View(); !strings.Contains(view, "U retry") {
		t.Fatalf("hydration retry is not visible:\n%s", view)
	}

	backend.userHydrateErr = nil
	retry := model.beginUserHydration()
	if retry == nil || model.beginUserHydration() != nil {
		t.Fatal("hydration retry was missing or duplicated while already in flight")
	}
	model.reduce(usersResult{request: 1, users: full.Users})
	if !model.usersLoading || model.usersReady {
		t.Fatal("stale hydration result superseded the active retry")
	}
	event := retry().(applicationEvent)
	model.reduce(event)
	if model.usersErr != "" || !model.usersReady || backend.usersCalls != 1 {
		t.Fatalf("retry result: err=%q ready=%v calls=%d", model.usersErr, model.usersReady, backend.usersCalls)
	}
}

func structWindowSize(width, height int) tea.WindowSizeMsg {
	return tea.WindowSizeMsg{Width: width, Height: height}
}

func TestReducerIgnoresStaleConversationEvents(t *testing.T) {
	model := readyDemoModel(t, 100, 30)
	original := append([]gack.Message(nil), model.messages...)
	model.reduce(messagesResult{channel: "a-channel-that-is-no-longer-open", messages: []gack.Message{{Text: "stale"}}})
	if len(model.messages) != len(original) || model.messages[0].Text != original[0].Text {
		t.Fatal("stale event mutated the current conversation")
	}

	model.reduce(messagesResult{channel: model.currentChannelID(), messages: []gack.Message{{Text: "current"}}})
	if len(model.messages) != 1 || model.messages[0].Text != "current" {
		t.Fatal("current event was not reduced")
	}
}

func TestBackgroundActivityNotifiesOnlyAfterBaseline(t *testing.T) {
	model := readyDemoModel(t, 100, 30)
	var titles, bodies []string
	model.SetNotifier(func(_ context.Context, title, body string) error {
		titles = append(titles, title)
		bodies = append(bodies, body)
		return nil
	})
	baseline := gack.ActivityItem{ID: "old", ChannelName: "alerts", Actor: "Bot", Text: "old", Unread: true}
	model.reduce(activityResult{items: []gack.ActivityItem{baseline}, background: true})
	if len(titles) != 0 || !model.activityPrimed {
		t.Fatalf("baseline sent notifications: %v", titles)
	}

	newItem := gack.ActivityItem{ID: "new", ChannelName: "alerts", Actor: "Deploy bot", Text: "Production needs attention", Unread: true}
	command := model.reduce(activityResult{items: []gack.ActivityItem{newItem, baseline}, background: true})
	event, ok := command().(applicationEvent)
	if !ok {
		t.Fatalf("notification command returned %T", command())
	}
	model.reduce(event)
	if len(titles) != 1 || titles[0] != "gack · #alerts · Deploy bot" || bodies[0] != "Production needs attention" {
		t.Fatalf("notifications = %v %v", titles, bodies)
	}
}
