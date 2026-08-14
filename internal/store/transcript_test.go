package store

import (
	"path/filepath"
	"testing"
)

func TestListTranscriptBlocksProjectsSafeApprovalHistory(t *testing.T) {
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	conversation, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	run, err := instance.CreateRun(t.Context(), conversation.ID, conversation.BotID, "grok", "approval")
	if err != nil {
		t.Fatal(err)
	}
	approval, err := instance.CreateApproval(t.Context(), run.ID, "grok", "terminal", `{"options":[{"optionId":"allow_once","name":"Allow once","kind":"allow_once"}],"tool_call":{"title":"Run command","token":"should-not-be-exposed"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.ResolveApproval(t.Context(), approval.ID, "approved", "allow_once"); err != nil {
		t.Fatal(err)
	}
	blocks, err := instance.ListTranscriptBlocks(t.Context(), conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %#v, want one block", blocks)
	}
	block := blocks[0]
	if block.Kind != "approval" || block.Status != "approved" || block.SelectedOptionID != "allow_once" {
		t.Fatalf("block = %#v", block)
	}
	if len(block.Options) != 1 || block.Options[0].Name != "Allow once" {
		t.Fatalf("block options = %#v", block.Options)
	}
	if block.ID != "approval-block:"+approval.ID || block.Action != "terminal" {
		t.Fatalf("block identity = %#v", block)
	}
}

func TestListTranscriptBlocksIsConversationScoped(t *testing.T) {
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	conversations, err := instance.ListConversations(t.Context(), "")
	if err != nil || len(conversations) == 0 {
		t.Fatalf("conversations = %#v, err = %v", conversations, err)
	}
	first := conversations[0]
	second, err := instance.CreateConversation(t.Context(), first.BotID, "Second")
	if err != nil {
		t.Fatal(err)
	}
	run, err := instance.CreateRun(t.Context(), first.ID, first.BotID, "grok", "first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.CreateApproval(t.Context(), run.ID, "grok", "first-action", `{}`); err != nil {
		t.Fatal(err)
	}
	firstBlocks, err := instance.ListTranscriptBlocks(t.Context(), first.ID)
	if err != nil || len(firstBlocks) != 1 {
		t.Fatalf("first blocks = %#v, err = %v", firstBlocks, err)
	}
	secondBlocks, err := instance.ListTranscriptBlocks(t.Context(), second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondBlocks) != 0 {
		t.Fatalf("second conversation leaked blocks: %#v", secondBlocks)
	}
}
