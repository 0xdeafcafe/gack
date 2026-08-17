package gack

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseBlocksPreservesTextAndInteractions(t *testing.T) {
	raw := json.RawMessage(`[
  {"type":"section","block_id":"summary","text":{"type":"mrkdwn","text":"*Ready*"},
   "fields":[{"type":"mrkdwn","text":"Env: prod"}],
   "accessory":{"type":"button","action_id":"approve","text":{"type":"plain_text","text":"Approve"},"value":"yes"}},
  {"type":"context","block_id":"context","elements":[{"type":"mrkdwn","text":"Requested by <@U123>"}]},
  {"type":"rich_text","block_id":"rich","elements":[{"type":"rich_text_section","elements":[
    {"type":"text","text":"Hello "},{"type":"user","user_id":"U123"},{"type":"text","text":" "},{"type":"emoji","name":"wave"}
  ]}]},
  {"type":"actions","block_id":"actions","elements":[{"type":"static_select","action_id":"region","placeholder":{"type":"plain_text","text":"Region"},
    "options":[{"text":{"type":"plain_text","text":"Europe"},"value":"eu"}]}]},
  {"type":"future_block","block_id":"future"}
]`)
	blocks, err := ParseBlocks(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 5 {
		t.Fatalf("got %d blocks", len(blocks))
	}
	if blocks[0].Text != "*Ready*" || blocks[0].Elements[0].Text != "Env: prod" {
		t.Fatalf("section content lost: %#v", blocks[0])
	}
	if blocks[1].Elements[0].Text != "Requested by <@U123>" {
		t.Fatalf("context text lost: %#v", blocks[1])
	}
	if blocks[2].Text != "Hello <@U123> :wave:" {
		t.Fatalf("rich text flattened incorrectly: %q", blocks[2].Text)
	}
	interactions := InteractiveElements(blocks)
	if len(interactions) != 2 || interactions[0].Element.ActionID != "approve" || interactions[1].Element.Options[0].Value != "eu" {
		t.Fatalf("interactive elements lost: %#v", interactions)
	}
	if !strings.Contains(blocks[4].Text, "Unsupported") {
		t.Fatalf("unknown block was hidden: %#v", blocks[4])
	}
}

func TestParseBlocksRejectsMalformedJSON(t *testing.T) {
	if _, err := ParseBlocks(json.RawMessage(`[{`)); err == nil {
		t.Fatal("expected malformed Block Kit error")
	}
}

func TestParseBlocksPreservesRichTextLinkDestination(t *testing.T) {
	raw := json.RawMessage(`[{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[
  {"type":"text","text":"Read "},
  {"type":"link","url":"https://example.com/runbook","text":"the runbook"}
]}]}]`)
	blocks, err := ParseBlocks(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := blocks[0].Text, "Read <https://example.com/runbook|the runbook>"; got != want {
		t.Fatalf("rich text = %q, want %q", got, want)
	}
}
