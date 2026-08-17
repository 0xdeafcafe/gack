package demo

import (
	"context"
	"testing"

	"github.com/0xdeafcafe/gack/internal/gack"
)

func TestDeploymentDialogue(t *testing.T) {
	backend := New()
	opened, err := backend.Interact(context.Background(), gack.Interaction{Type: "block_actions", ActionID: "deploy_start", Value: "2026.08.17-rc3"})
	if err != nil || opened.View == nil || opened.View.CallbackID != "deploy_configure" {
		t.Fatalf("open deploy view: result=%#v err=%v", opened, err)
	}

	invalid, err := backend.Interact(context.Background(), gack.Interaction{Type: "view_submission", CallbackID: "deploy_configure", State: map[string]map[string]any{
		"release": {"version": "2026.08.17-rc3"}, "target": {"environment": "production"}, "change": {"change_id": ""},
	}})
	if err != nil || invalid.Errors["change"] == "" {
		t.Fatalf("expected field validation error: result=%#v err=%v", invalid, err)
	}

	review, err := backend.Interact(context.Background(), gack.Interaction{Type: "view_submission", CallbackID: "deploy_configure", State: map[string]map[string]any{
		"release": {"version": "2026.08.17-rc3"}, "target": {"environment": "production"}, "change": {"change_id": "CHG-42"},
	}})
	if err != nil || review.Replace == nil || review.Replace.CallbackID != "deploy_confirm" {
		t.Fatalf("review deploy: result=%#v err=%v", review, err)
	}

	confirmed, err := backend.Interact(context.Background(), gack.Interaction{Type: "view_submission", CallbackID: "deploy_confirm", State: map[string]map[string]any{
		"confirm": {"version": "2026.08.17-rc3", "environment": "production", "approved": []string{"yes"}},
	}})
	if err != nil || confirmed.Notice == "" {
		t.Fatalf("confirm deploy: result=%#v err=%v", confirmed, err)
	}
	messages, err := backend.Messages(context.Background(), "C_PLATFORM")
	if err != nil || len(messages) != 4 || messages[len(messages)-1].UserID != "B_DEPLOY" {
		t.Fatalf("deployment did not update conversation: messages=%#v err=%v", messages, err)
	}
}

func TestDemoSearchAndReaction(t *testing.T) {
	backend := New()
	results, err := backend.Search(context.Background(), "release candidate")
	if err != nil || len(results) != 1 || results[0].ChannelID != "C_PLATFORM" {
		t.Fatalf("search: results=%#v err=%v", results, err)
	}
	messages, _ := backend.Messages(context.Background(), "C_PLATFORM")
	ts := messages[0].TS
	if err := backend.ToggleReaction(context.Background(), "C_PLATFORM", ts, "heart", false); err != nil {
		t.Fatal(err)
	}
	messages, _ = backend.Messages(context.Background(), "C_PLATFORM")
	if len(messages[0].Reactions) != 1 || !messages[0].Reactions[0].Mine {
		t.Fatalf("reaction not applied: %#v", messages[0].Reactions)
	}
}

func TestDemoEditsMessageEverywhereItAppears(t *testing.T) {
	backend := New()
	messages, _ := backend.Messages(context.Background(), "C_GENERAL")
	root := messages[1]
	updated, err := backend.EditMessage(context.Background(), "C_GENERAL", root.TS, "A clearer migration note")
	if err != nil || !updated.Edited || updated.Text != "A clearer migration note" {
		t.Fatalf("edit: message=%#v err=%v", updated, err)
	}
	replies, _ := backend.Thread(context.Background(), "C_GENERAL", root.TS)
	if replies[0].Text != updated.Text || !replies[0].Edited {
		t.Fatalf("thread root was not updated: %#v", replies[0])
	}
}
