package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestClientBootstrapMessagesSearchAndReaction(t *testing.T) {
	var mu sync.Mutex
	called := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer xoxp-test" {
			t.Errorf("missing authorization header")
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
			writeJSON(writer, `{"ok":true,"channels":[{"id":"C1","name":"general","topic":{"value":"Hello"}},{"id":"D1","user":"U2","is_im":true,"unread_count_display":2},{"id":"C1","name":"general"}]}`)
		case "conversations.history":
			writeJSON(writer, `{"ok":true,"messages":[
          {"type":"message","ts":"200.000002","user":"U2","text":"newer"},
          {"type":"message","ts":"100.000001","user":"U1","text":"older","blocks":[{"type":"actions","block_id":"b","elements":[{"type":"button","action_id":"go","text":{"type":"plain_text","text":"Go"},"value":"1"}]}]}
        ]}`)
		case "search.messages":
			writeJSON(writer, `{"ok":true,"messages":{"matches":[{"ts":"200.000002","user":"U2","text":"needle","channel":{"id":"C1","name":"general"}}]}}`)
		case "reactions.add":
			writeJSON(writer, `{"ok":true}`)
		case "chat.update":
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

func TestHistoryPagingSendsCursorAndPreservesDisplayOrder(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var parameters map[string]any
		if err := json.NewDecoder(request.Body).Decode(&parameters); err != nil {
			t.Fatal(err)
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
	if requests[0]["cursor"] != "cursor-1" || requests[0]["channel"] != "C1" || requests[1]["cursor"] != "cursor-2" || requests[1]["ts"] != "root.1" {
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
