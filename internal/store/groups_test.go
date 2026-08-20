package store

import (
	"path/filepath"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

func TestCreateGroupRequiresTwoAgentsAndKeepsCanonicalChatsEmpty(t *testing.T) {
	ctx := t.Context()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	engineer, err := instance.CreateAgent(ctx, domain.AgentDraft{Name: "Engineer", Title: "Code", Description: "Repo."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.CreateGroup(ctx, "Solo", []string{engineer.Bot.ID}); err == nil {
		t.Fatal("one-agent group accepted")
	}
	researcher, err := instance.CreateAgent(ctx, domain.AgentDraft{Name: "Researcher", Title: "Research", Description: "Notes."})
	if err != nil {
		t.Fatal(err)
	}
	group, err := instance.CreateGroup(ctx, "Product Launch", []string{engineer.Bot.ID, researcher.Bot.ID})
	if err != nil {
		t.Fatal(err)
	}
	result, err := instance.CreateGroupMessage(ctx, CreateGroupMessageInput{
		GroupID:       group.ID,
		Content:       "Look at the API.",
		MentionBotIDs: []string{researcher.Bot.ID, engineer.Bot.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Runs) != 2 {
		t.Fatalf("runs = %#v", result.Runs)
	}
	canonical, err := instance.ListMessages(ctx, engineer.Conversation.ID)
	if err != nil || len(canonical) != 0 {
		t.Fatalf("canonical leak = %#v, %v", canonical, err)
	}
}
