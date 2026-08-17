package ui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/0xdeafcafe/gack/internal/demo"
	"github.com/0xdeafcafe/gack/internal/gack"
)

type countingBackend struct {
	gack.Backend
	bootstrapCalls int
	messageCalls   int
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
