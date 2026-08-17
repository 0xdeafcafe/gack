package agentcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/0xdeafcafe/gack/internal/demo"
	"github.com/0xdeafcafe/gack/internal/gack"
)

type testEnvelope struct {
	OK      bool            `json:"ok"`
	Command string          `json:"command"`
	Data    json.RawMessage `json:"data"`
	Error   *errorBody      `json:"error"`
}

func runForTest(t *testing.T, backend gack.Backend, options Options, args ...string) (int, testEnvelope, string, string) {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	status := Run(context.Background(), backend, args, &stdout, &stderr, options)
	output := stdout.String()
	errorOutput := stderr.String()
	encoded := output
	if status != ExitOK {
		encoded = errorOutput
	}
	var response testEnvelope
	if err := json.Unmarshal([]byte(encoded), &response); err != nil {
		t.Fatalf("decode response for %v: %v\nstdout: %s\nstderr: %s", args, err, output, errorOutput)
	}
	return status, response, output, errorOutput
}

type snapshotBackend struct {
	gack.Backend
	snapshot gack.Snapshot
}

type activityBootstrapBackend struct {
	gack.Backend
	bootstrapped bool
}

func (b *activityBootstrapBackend) Bootstrap(ctx context.Context) (gack.Snapshot, error) {
	b.bootstrapped = true
	return b.Backend.Bootstrap(ctx)
}

func (b *activityBootstrapBackend) Activity(ctx context.Context) ([]gack.ActivityItem, error) {
	if !b.bootstrapped {
		return nil, errors.New("activity called before bootstrap")
	}
	return b.Backend.Activity(ctx)
}

func (b snapshotBackend) Bootstrap(context.Context) (gack.Snapshot, error) {
	return b.snapshot, nil
}

func TestChannelsCanFilterUnread(t *testing.T) {
	base := demo.New()
	snapshot, err := base.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := range snapshot.Conversations {
		if snapshot.Conversations[i].ID == "C_PLATFORM" {
			snapshot.Conversations[i].Unread = 0
			snapshot.Conversations[i].Mentions = 0
		}
	}
	status, response, _, stderr := runForTest(t, snapshotBackend{Backend: base, snapshot: snapshot}, Options{}, "channels", "--unread")
	if status != ExitOK || !response.OK || stderr != "" {
		t.Fatalf("channels failed: status=%d response=%#v stderr=%s", status, response, stderr)
	}
	var data channelsData
	if err := json.Unmarshal(response.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Team != "Acme Engineering" || len(data.Channels) != len(snapshot.Conversations)-1 {
		t.Fatalf("unexpected channels data: %#v", data)
	}
	for _, channel := range data.Channels {
		if channel.ID == "C_PLATFORM" {
			t.Fatal("read channel was not filtered")
		}
	}
}

func TestReadCommandsUseNamesAndReturnStructuredData(t *testing.T) {
	backend := demo.New()

	t.Run("messages", func(t *testing.T) {
		status, response, _, _ := runForTest(t, backend, Options{}, "messages", "#general")
		if status != ExitOK || response.Command != "messages" {
			t.Fatalf("status=%d response=%#v", status, response)
		}
		var data messagesData
		if err := json.Unmarshal(response.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.Channel.ID != "C_GENERAL" || len(data.Messages) != 3 || data.Messages[0].Username == "" {
			t.Fatalf("unexpected messages: %#v", data)
		}
	})

	t.Run("thread", func(t *testing.T) {
		messages, err := backend.Messages(context.Background(), "C_GENERAL")
		if err != nil {
			t.Fatal(err)
		}
		threadTS := messages[1].TS
		status, response, _, _ := runForTest(t, backend, Options{}, "thread", "general", threadTS)
		if status != ExitOK {
			t.Fatalf("status=%d response=%#v", status, response)
		}
		var data threadData
		if err := json.Unmarshal(response.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.ThreadTS != threadTS || len(data.Messages) != 4 {
			t.Fatalf("unexpected thread: %#v", data)
		}
	})

	t.Run("search", func(t *testing.T) {
		status, response, _, _ := runForTest(t, backend, Options{}, "search", "release", "candidate")
		if status != ExitOK {
			t.Fatalf("status=%d response=%#v", status, response)
		}
		var data searchData
		if err := json.Unmarshal(response.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.Query != "release candidate" || len(data.Results) != 1 || data.Results[0].ChannelID != "C_PLATFORM" {
			t.Fatalf("unexpected search: %#v", data)
		}
	})

	t.Run("activity", func(t *testing.T) {
		activityBackend := &activityBootstrapBackend{Backend: backend}
		status, response, _, _ := runForTest(t, activityBackend, Options{}, "activity", "--unread")
		if status != ExitOK {
			t.Fatalf("status=%d response=%#v", status, response)
		}
		if !activityBackend.bootstrapped {
			t.Fatal("activity command did not bootstrap workspace identity")
		}
		var data activityData
		if err := json.Unmarshal(response.Data, &data); err != nil {
			t.Fatal(err)
		}
		if len(data.Activity) != 2 {
			t.Fatalf("unexpected activity: %#v", data)
		}
	})
}

func TestMutationCommandsAreExplicitAndStructured(t *testing.T) {
	backend := demo.New()

	status, sentResponse, _, _ := runForTest(t, backend, Options{}, "send", "platform", "agent", "status", "update")
	if status != ExitOK {
		t.Fatalf("send failed: %#v", sentResponse)
	}
	var sent mutationData
	if err := json.Unmarshal(sentResponse.Data, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Action != "send" || sent.Channel != "C_PLATFORM" || sent.Message == nil || sent.Message.Text != "agent status update" {
		t.Fatalf("unexpected send result: %#v", sent)
	}

	status, editedResponse, _, _ := runForTest(t, backend, Options{}, "edit", "--channel", "C_PLATFORM", "--ts", sent.Message.TS, "--text", "corrected update")
	if status != ExitOK {
		t.Fatalf("edit failed: %#v", editedResponse)
	}
	var edited mutationData
	if err := json.Unmarshal(editedResponse.Data, &edited); err != nil {
		t.Fatal(err)
	}
	if edited.Message == nil || edited.Message.Text != "corrected update" || !edited.Message.Edited {
		t.Fatalf("unexpected edit result: %#v", edited)
	}

	status, reactedResponse, _, _ := runForTest(t, backend, Options{}, "react", "platform", sent.Message.TS, ":eyes:")
	if status != ExitOK {
		t.Fatalf("react failed: %#v", reactedResponse)
	}
	var reacted mutationData
	if err := json.Unmarshal(reactedResponse.Data, &reacted); err != nil {
		t.Fatal(err)
	}
	if reacted.Action != "react" || reacted.Emoji != "eyes" || reacted.Removed {
		t.Fatalf("unexpected reaction result: %#v", reacted)
	}
}

type mutationSpy struct {
	gack.Backend
	posts int
}

func (b *mutationSpy) PostMessage(ctx context.Context, channel, thread, text string) (gack.Message, error) {
	b.posts++
	return b.Backend.PostMessage(ctx, channel, thread, text)
}

func TestReadOnlyGuardsMutationsBeforeBackendCalls(t *testing.T) {
	tests := []struct {
		name    string
		options Options
	}{
		{name: "option", options: Options{ReadOnly: true}},
		{name: "environment", options: Options{Getenv: func(name string) string {
			if name == "GACK_READ_ONLY" {
				return "1"
			}
			return ""
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := &mutationSpy{Backend: demo.New()}
			status, response, stdout, _ := runForTest(t, backend, test.options, "send", "general", "must not send")
			if status != ExitReadOnly || response.OK || response.Error == nil || response.Error.Code != "read_only" {
				t.Fatalf("unexpected response: status=%d response=%#v", status, response)
			}
			if stdout != "" || backend.posts != 0 {
				t.Fatalf("mutation leaked: stdout=%q posts=%d", stdout, backend.posts)
			}
		})
	}
}

func TestErrorsAreJSONAndUseful(t *testing.T) {
	status, response, stdout, _ := runForTest(t, demo.New(), Options{}, "messages", "does-not-exist")
	if status != ExitError || stdout != "" || response.Error == nil || response.Error.Code != "channel_not_found" {
		t.Fatalf("unexpected not-found response: status=%d response=%#v stdout=%q", status, response, stdout)
	}
	if response.Error.Message == "" {
		t.Fatal("error message is empty")
	}

	status, response, stdout, _ = runForTest(t, demo.New(), Options{}, "wat")
	if status != ExitUsage || stdout != "" || response.Error == nil || response.Error.Usage == "" {
		t.Fatalf("unexpected usage response: status=%d response=%#v stdout=%q", status, response, stdout)
	}
}
