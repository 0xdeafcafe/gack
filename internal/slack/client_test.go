package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0xdeafcafe/gack/internal/gack"
)

var _ gack.ProgressiveBootstrapper = (*Client)(nil)

func TestClientBootstrapMessagesSearchAndReaction(t *testing.T) {
	var mu sync.Mutex
	called := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Errorf("parse Slack SDK request: %v", err)
		}
		if request.Form.Get("token") != "xoxp-test" {
			t.Errorf("missing Slack SDK token")
		}
		method := strings.TrimPrefix(request.URL.Path, "/")
		mu.Lock()
		called[method]++
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		switch method {
		case "auth.test":
			writeJSON(writer, `{"ok":true,"user_id":"U1","user":"alex","team":"Acme"}`)
		case "users.list":
			writeJSON(writer, `{"ok":true,"members":[{"id":"U1","name":"alex","profile":{"display_name":"Alex"}},{"id":"U2","name":"maya","profile":{"real_name":"Maya"}}]}`)
		case "users.conversations":
			if request.Form.Get("types") != "public_channel,private_channel,mpim,im" || request.Form.Get("limit") != "200" {
				t.Errorf("users.conversations form = %q", request.Form.Encode())
			}
			writeJSON(writer, `{"ok":true,"channels":[{"id":"C1","name":"general","topic":{"value":"Hello"},"is_starred":true},{"id":"D1","user":"U2","is_im":true,"unread_count_display":2},{"id":"C1","name":"general"}]}`)
		case "conversations.history":
			if request.Form.Get("channel") != "C1" || request.Form.Get("limit") != "15" || request.Form.Get("include_all_metadata") != "1" {
				t.Errorf("conversations.history form = %q", request.Form.Encode())
			}
			writeJSON(writer, `{"ok":true,"messages":[
          {"type":"message","ts":"200.000002","user":"U2","text":"newer"},
          {"type":"message","ts":"100.000001","user":"U1","text":"older","blocks":[{"type":"actions","block_id":"b","elements":[{"type":"button","action_id":"go","text":{"type":"plain_text","text":"Go"},"value":"1"}]}]}
        ]}`)
		case "search.messages":
			if request.Method != http.MethodPost {
				t.Errorf("search.messages method = %s, want SDK form POST", request.Method)
			}
			if request.Form.Get("query") != "needle" || request.Form.Get("count") != "15" {
				t.Errorf("search.messages form = %q", request.Form.Encode())
			}
			writeJSON(writer, `{"ok":true,"messages":{"matches":[{"ts":"200.000002","user":"U2","text":"needle","channel":{"id":"C1","name":"general"}}]}}`)
		case "reactions.add":
			if request.Form.Get("channel") != "C1" || request.Form.Get("timestamp") != "200.000002" || request.Form.Get("name") != "eyes" {
				t.Errorf("reactions.add form = %q", request.Form.Encode())
			}
			writeJSON(writer, `{"ok":true}`)
		case "chat.update":
			if request.Form.Get("channel") != "C1" || request.Form.Get("ts") != "200.000002" || request.Form.Get("text") != "edited" {
				t.Errorf("chat.update form = %q", request.Form.Encode())
			}
			writeJSON(writer, `{"ok":true,"channel":"C1","ts":"200.000002","text":"edited","message":{"ts":"200.000002","user":"U1","text":"edited","edited":{}}}`)
		default:
			http.Error(writer, `{"ok":false,"error":"unknown_method"}`, http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := New(Config{Token: "xoxp-test", BaseURL: server.URL, MessageLimit: 15})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Team != "Acme" || snapshot.Self.DisplayName() != "Alex" || len(snapshot.Conversations) != 2 || snapshot.Conversations[1].Label() != "@Maya" {
		t.Fatalf("bad snapshot: %#v", snapshot)
	}
	if !snapshot.Conversations[0].IsFavorite {
		t.Fatalf("Slack favorite was not preserved: %#v", snapshot.Conversations[0])
	}
	messages, err := client.Messages(context.Background(), "C1")
	if err != nil {
		t.Fatal(err)
	}
	if messages[0].Text != "older" || len(messages[0].Blocks) != 1 || len(messages[0].Blocks[0].Elements) != 1 {
		t.Fatalf("messages not converted/ordered: %#v", messages)
	}
	results, err := client.Search(context.Background(), "needle")
	if err != nil || len(results) != 1 || results[0].ChannelName != "general" {
		t.Fatalf("search: results=%#v err=%v", results, err)
	}
	if err := client.ToggleReaction(context.Background(), "C1", "200.000002", ":eyes:", false); err != nil {
		t.Fatal(err)
	}
	updated, err := client.EditMessage(context.Background(), "C1", "200.000002", "edited")
	if err != nil || updated.Text != "edited" || !updated.Edited {
		t.Fatalf("edit: message=%#v err=%v", updated, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if called["reactions.add"] != 1 {
		t.Fatalf("reaction endpoint calls: %#v", called)
	}
	if called["chat.update"] != 1 {
		t.Fatalf("edit endpoint calls: %#v", called)
	}
	if called["users.conversations"] != 1 || called["conversations.list"] != 0 {
		t.Fatalf("bootstrap did not use the membership-scoped endpoint: %#v", called)
	}
}

func TestClientReportsSlackAPIErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, `{"ok":false,"error":"missing_scope","response_metadata":{"messages":["[ERROR] required scope: search:read"]}}`)
	}))
	defer server.Close()
	client, _ := New(Config{Token: "test", BaseURL: server.URL})
	_, err := client.Search(context.Background(), "anything")
	if err == nil || !strings.Contains(err.Error(), "missing_scope") || !strings.Contains(err.Error(), "required scope: search:read") {
		t.Fatalf("expected useful Slack API error, got %v", err)
	}
}

func TestBootstrapFetchesIndependentDatasetsConcurrently(t *testing.T) {
	started := make(chan string, 3)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		method := strings.TrimPrefix(request.URL.Path, "/")
		started <- method
		select {
		case <-release:
		case <-request.Context().Done():
			return
		}
		switch method {
		case "auth.test":
			writeJSON(writer, `{"ok":true,"user_id":"U1","user":"alex","team":"Acme"}`)
		case "users.list":
			writeJSON(writer, `{"ok":true,"members":[{"id":"U1","name":"alex","profile":{"display_name":"Alex"}},{"id":"U2","name":"maya","profile":{"real_name":"Maya"}}]}`)
		case "users.conversations":
			writeJSON(writer, `{"ok":true,"channels":[{"id":"C1","name":"general"},{"id":"D1","user":"U2","is_im":true}]}`)
		default:
			http.Error(writer, `{"ok":false,"error":"unknown_method"}`, http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := New(Config{Token: "test", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	type bootstrapResult struct {
		snapshot gack.Snapshot
		err      error
	}
	done := make(chan bootstrapResult, 1)
	go func() {
		snapshot, err := client.Bootstrap(context.Background())
		done <- bootstrapResult{snapshot: snapshot, err: err}
	}()

	methods := make(map[string]bool, 3)
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for len(methods) < 3 {
		select {
		case method := <-started:
			methods[method] = true
		case <-deadline.C:
			t.Fatalf("bootstrap requests did not overlap; started %v", methods)
		}
	}
	unblock()

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.snapshot.Team != "Acme" || result.snapshot.Self.DisplayName() != "Alex" {
			t.Fatalf("bad identity mapping: %#v", result.snapshot)
		}
		if len(result.snapshot.Conversations) != 2 || result.snapshot.Conversations[1].Label() != "@Maya" {
			t.Fatalf("conversation order or DM mapping changed: %#v", result.snapshot.Conversations)
		}
	case <-time.After(time.Second):
		t.Fatal("bootstrap did not finish after all responses were released")
	}
}

func TestProgressiveBootstrapRendersBeforeSlowUsersFinish(t *testing.T) {
	usersStarted := make(chan struct{})
	releaseUsers := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseUsers) }) }
	t.Cleanup(release)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		method := strings.TrimPrefix(request.URL.Path, "/")
		switch method {
		case "auth.test":
			writeJSON(writer, `{"ok":true,"user_id":"U1","user":"alex","team":"Acme"}`)
		case "users.conversations":
			writeJSON(writer, `{"ok":true,"channels":[{"id":"C1","name":"general"},{"id":"D1","user":"U2","is_im":true}]}`)
		case "users.list":
			startOnce.Do(func() { close(usersStarted) })
			select {
			case <-releaseUsers:
				writeJSON(writer, `{"ok":true,"members":[{"id":"U1","name":"alex","profile":{"display_name":"Alex"}},{"id":"U2","name":"maya","profile":{"display_name":"Maya"}}]}`)
			case <-request.Context().Done():
			}
		default:
			http.Error(writer, `{"ok":false,"error":"unknown_method"}`, http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, _ := New(Config{Token: "test", BaseURL: server.URL})

	type usersOutcome struct {
		users map[string]gack.User
		err   error
	}
	usersDone := make(chan usersOutcome, 1)
	go func() {
		users, err := client.HydrateUsers(context.Background())
		usersDone <- usersOutcome{users: users, err: err}
	}()
	select {
	case <-usersStarted:
	case <-time.After(time.Second):
		t.Fatal("users.list did not start")
	}

	coreDone := make(chan struct {
		snapshot gack.Snapshot
		err      error
	}, 1)
	go func() {
		snapshot, err := client.BootstrapCore(context.Background())
		coreDone <- struct {
			snapshot gack.Snapshot
			err      error
		}{snapshot: snapshot, err: err}
	}()
	select {
	case result := <-coreDone:
		if result.err != nil || result.snapshot.Team != "Acme" || len(result.snapshot.Conversations) != 2 {
			t.Fatalf("core result = %#v, %v", result.snapshot, result.err)
		}
		if label := result.snapshot.Conversations[1].Label(); label != "@U2" {
			t.Fatalf("unhydrated DM label = %q, want stable fallback", label)
		}
	case <-time.After(time.Second):
		t.Fatal("workspace core waited for users.list")
	}
	select {
	case <-usersDone:
		t.Fatal("user hydration finished before its response was released")
	default:
	}
	release()
	select {
	case result := <-usersDone:
		if result.err != nil || result.users["U2"].DisplayName() != "Maya" {
			t.Fatalf("users result = %#v, %v", result.users, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("user hydration did not finish")
	}
}

func TestProgressiveUserFailureDoesNotFailWorkspaceCore(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch strings.TrimPrefix(request.URL.Path, "/") {
		case "auth.test":
			writeJSON(writer, `{"ok":true,"user_id":"U1","user":"alex","team":"Acme"}`)
		case "users.conversations":
			writeJSON(writer, `{"ok":true,"channels":[{"id":"C1","name":"general"}]}`)
		case "users.list":
			writeJSON(writer, `{"ok":false,"error":"ratelimited"}`)
		}
	}))
	defer server.Close()
	client, _ := New(Config{Token: "test", BaseURL: server.URL})

	if _, err := client.HydrateUsers(context.Background()); err == nil || !strings.Contains(err.Error(), "ratelimited") {
		t.Fatalf("hydrate error = %v", err)
	}
	if snapshot, err := client.BootstrapCore(context.Background()); err != nil || snapshot.Team != "Acme" || len(snapshot.Conversations) != 1 {
		t.Fatalf("workspace core = %#v, %v", snapshot, err)
	}
}

func TestUserHydrationKeepsDeletedAuthorsForHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch strings.TrimPrefix(request.URL.Path, "/") {
		case "users.list":
			writeJSON(writer, `{"ok":true,"members":[{"id":"U_OLD","name":"former","deleted":true,"profile":{"real_name":"Former Teammate"}}]}`)
		case "conversations.history":
			writeJSON(writer, `{"ok":true,"messages":[{"ts":"1.0","user":"U_OLD","text":"historical context"}]}`)
		}
	}))
	defer server.Close()
	client, _ := New(Config{Token: "test", BaseURL: server.URL})

	users, err := client.HydrateUsers(context.Background())
	if err != nil || users["U_OLD"].DisplayName() != "Former Teammate" {
		t.Fatalf("deleted user metadata = %#v, %v", users["U_OLD"], err)
	}
	messages, err := client.Messages(context.Background(), "C1")
	if err != nil || len(messages) != 1 || messages[0].Username != "Former Teammate" {
		t.Fatalf("historical message = %#v, %v", messages, err)
	}
}

func TestBootstrapCancelsAndJoinsSiblingRequestsOnError(t *testing.T) {
	allStarted := make(chan struct{})
	finished := make(chan string, 2)
	var (
		startMu    sync.Mutex
		startCount int
		startOnce  sync.Once
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		method := strings.TrimPrefix(request.URL.Path, "/api/")
		startMu.Lock()
		startCount++
		if startCount == 3 {
			startOnce.Do(func() { close(allStarted) })
		}
		startMu.Unlock()

		select {
		case <-allStarted:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
		if method == "users.list" {
			return jsonResponse(`{"ok":false,"error":"missing_scope"}`), nil
		}
		<-request.Context().Done()
		finished <- method
		return nil, request.Context().Err()
	})

	client, err := New(Config{Token: "test", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := client.Bootstrap(context.Background())
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "load users") || !strings.Contains(err.Error(), "missing_scope") {
			t.Fatalf("expected initiating users error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bootstrap did not cancel and join sibling requests")
	}

	seen := make(map[string]bool, 2)
	for len(seen) < 2 {
		select {
		case method := <-finished:
			seen[method] = true
		case <-time.After(time.Second):
			t.Fatalf("in-flight sibling request was left behind; finished %v", seen)
		}
	}
	if !seen["auth.test"] || !seen["users.conversations"] {
		t.Fatalf("unexpected canceled siblings: %v", seen)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     http.StatusText(http.StatusOK),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestHistoryPagingSendsCursorAndPreservesDisplayOrder(t *testing.T) {
	var requests []map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("%s method = %s, want SDK form POST", request.URL.Path, request.Method)
		}
		if err := request.ParseForm(); err != nil {
			t.Errorf("parse Slack SDK request: %v", err)
		}
		parameters := make(map[string]string, len(request.Form))
		for key, values := range request.Form {
			parameters[key] = values[0]
		}
		requests = append(requests, parameters)
		writeJSON(writer, `{"ok":true,"messages":[{"ts":"2.0","text":"new"},{"ts":"1.0","text":"old"}],"response_metadata":{"next_cursor":"next-page"}}`)
	}))
	defer server.Close()
	client, _ := New(Config{Token: "test", BaseURL: server.URL, MessageLimit: 15})

	history, err := client.MessagePage(context.Background(), "C1", "cursor-1")
	if err != nil || history.NextCursor != "next-page" || len(history.Messages) != 2 || history.Messages[0].Text != "old" {
		t.Fatalf("history page = %#v, %v", history, err)
	}
	thread, err := client.ThreadPage(context.Background(), "C1", "root.1", "cursor-2")
	if err != nil || len(thread.Messages) != 2 || thread.Messages[0].Text != "new" {
		t.Fatalf("thread page = %#v, %v", thread, err)
	}
	if requests[0]["cursor"] != "cursor-1" || requests[0]["channel"] != "C1" || requests[1]["cursor"] != "cursor-2" || requests[1]["channel"] != "C1" || requests[1]["ts"] != "root.1" || requests[1]["limit"] != "15" {
		t.Fatalf("paging requests = %#v", requests)
	}
}

func writeJSON(writer http.ResponseWriter, value string) {
	var compact json.RawMessage
	if err := json.Unmarshal([]byte(value), &compact); err != nil {
		panic(err)
	}
	writer.Write(compact)
}
