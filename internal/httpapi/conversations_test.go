package httpapi

import (
	"path/filepath"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

func TestVisibleConversationsKeepsOneCanonicalChatPerAgent(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}

	first, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.CreateConversation(t.Context(), first.BotID, "Advanced chat"); err != nil {
		t.Fatal(err)
	}
	secondAgent, err := instance.CreateAgent(t.Context(), domain.AgentDraft{
		Name: "Second agent", Title: "Second agent", ConversationTitle: "Second agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.CreateConversation(t.Context(), secondAgent.Bot.ID, "Second advanced chat"); err != nil {
		t.Fatal(err)
	}

	server := &Server{Store: instance}
	visible, err := server.visibleConversations(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 2 {
		t.Fatalf("visible conversations = %#v, want one per agent", visible)
	}
	byBot := make(map[string]domain.Conversation, len(visible))
	for _, conversation := range visible {
		if _, exists := byBot[conversation.BotID]; exists {
			t.Fatalf("duplicate canonical conversation for bot %q: %#v", conversation.BotID, visible)
		}
		byBot[conversation.BotID] = conversation
	}
	if got := byBot[first.BotID].ID; got != first.ID {
		t.Fatalf("first agent canonical conversation = %q, want %q", got, first.ID)
	}
	if got := byBot[secondAgent.Bot.ID].ID; got != secondAgent.Conversations[0].ID {
		t.Fatalf("second agent canonical conversation = %q, want %q", got, secondAgent.Conversations[0].ID)
	}
}
