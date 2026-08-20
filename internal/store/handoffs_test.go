package store

import (
	"path/filepath"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

func seedTwoAgents(t *testing.T, instance *Store) (source domain.Agent, target domain.Agent) {
	t.Helper()
	ctx := t.Context()
	if err := instance.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	agents, err := instance.ListAgents(ctx)
	if err != nil || len(agents) == 0 {
		t.Fatalf("seed agents = %#v, %v", agents, err)
	}
	target, err = instance.CreateAgent(ctx, domain.AgentDraft{
		Name: "Reviewer", Title: "Reviews handed-off work", Description: "Second visible agent.", ConversationTitle: "Review",
	})
	if err != nil {
		t.Fatal(err)
	}
	return agents[0], target
}

func createHandoff(t *testing.T, instance *Store, source, target domain.Agent, input CreateAgentHandoffInput) CreateAgentHandoffResult {
	t.Helper()
	input.SourceConversationID = source.Conversation.ID
	input.SourceBotID = source.Bot.ID
	input.TargetBotID = target.Bot.ID
	input.TargetConversationID = target.Conversation.ID
	if input.Content == "" {
		input.Content = "Please review the local notes."
	}
	if input.TargetProvider == "" {
		input.TargetProvider = "grok"
	}
	if input.TargetPrompt == "" {
		input.TargetPrompt = "handoff prompt"
	}
	result, err := instance.CreateAgentHandoff(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCreateAndGetHandoffPersistsTaskFields(t *testing.T) {
	ctx := t.Context()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	source, target := seedTwoAgents(t, instance)
	created := createHandoff(t, instance, source, target, CreateAgentHandoffInput{
		OriginRunID:    "run-origin",
		SourceRunID:    "run-source",
		TimeoutSeconds: 45,
	})
	if created.Handoff.Status != "queued" || created.Handoff.Mode != "user" || created.Handoff.Depth != 0 {
		t.Fatalf("create defaults = %#v", created.Handoff)
	}
	got, err := instance.GetHandoff(ctx, created.Handoff.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "queued" || got.Mode != "user" || got.OriginRunID != "run-origin" || got.SourceRunID != "run-source" || got.TimeoutSeconds != 45 {
		t.Fatalf("get handoff = %#v", got)
	}
	listed, err := instance.ListHandoffsForConversation(ctx, source.Conversation.ID)
	if err != nil || len(listed) != 1 || listed[0].ID != created.Handoff.ID {
		t.Fatalf("list source = %#v, %v", listed, err)
	}
}

func TestLoadHandoffChainRootToLeaf(t *testing.T) {
	ctx := t.Context()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	source, target := seedTwoAgents(t, instance)
	root := createHandoff(t, instance, source, target, CreateAgentHandoffInput{OriginRunID: "origin"})
	child := createHandoff(t, instance, source, target, CreateAgentHandoffInput{
		ParentHandoffID: root.Handoff.ID,
		Depth:           1,
		OriginRunID:     "origin",
		Content:         "child task",
	})
	grandchild := createHandoff(t, instance, source, target, CreateAgentHandoffInput{
		ParentHandoffID: child.Handoff.ID,
		Depth:           2,
		OriginRunID:     "origin",
		Content:         "grandchild task",
	})
	chain, err := instance.LoadHandoffChain(ctx, grandchild.Handoff.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 3 || chain[0].ID != root.Handoff.ID || chain[1].ID != child.Handoff.ID || chain[2].ID != grandchild.Handoff.ID {
		t.Fatalf("chain = %#v", chain)
	}
	if chain[0].Depth != 0 || chain[1].Depth != 1 || chain[2].Depth != 2 {
		t.Fatalf("chain depths = %#v", chain)
	}
}

func TestActiveHandoffsForOriginRun(t *testing.T) {
	ctx := t.Context()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	source, target := seedTwoAgents(t, instance)
	queued := createHandoff(t, instance, source, target, CreateAgentHandoffInput{OriginRunID: "origin-a"})
	running := createHandoff(t, instance, source, target, CreateAgentHandoffInput{OriginRunID: "origin-a", Status: "running", Content: "running"})
	waiting := createHandoff(t, instance, source, target, CreateAgentHandoffInput{OriginRunID: "origin-a", Status: "waiting", Content: "waiting"})
	done := createHandoff(t, instance, source, target, CreateAgentHandoffInput{OriginRunID: "origin-a", Status: "completed", Content: "done"})
	other := createHandoff(t, instance, source, target, CreateAgentHandoffInput{OriginRunID: "origin-b", Content: "other origin"})
	count, err := instance.CountActiveHandoffsForOriginRun(ctx, "origin-a")
	if err != nil || count != 3 {
		t.Fatalf("count = %d, %v", count, err)
	}
	active, err := instance.ListActiveHandoffsForOriginRun(ctx, "origin-a")
	if err != nil || len(active) != 3 {
		t.Fatalf("active = %#v, %v", active, err)
	}
	ids := map[string]bool{}
	for _, item := range active {
		ids[item.ID] = true
	}
	if !ids[queued.Handoff.ID] || !ids[running.Handoff.ID] || !ids[waiting.Handoff.ID] || ids[done.Handoff.ID] || ids[other.Handoff.ID] {
		t.Fatalf("active ids = %#v", ids)
	}
}

func TestUpdateHandoffStatusSetsCompletedAtWhenTerminal(t *testing.T) {
	ctx := t.Context()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	source, target := seedTwoAgents(t, instance)
	created := createHandoff(t, instance, source, target, CreateAgentHandoffInput{OriginRunID: "origin-a"})
	if err := instance.UpdateHandoffStatus(ctx, created.Handoff.ID, "running", ""); err != nil {
		t.Fatal(err)
	}
	running, err := instance.GetHandoff(ctx, created.Handoff.ID)
	if err != nil || running.Status != "running" || running.CompletedAt != "" {
		t.Fatalf("running = %#v, %v", running, err)
	}
	if err := instance.UpdateHandoffStatus(ctx, created.Handoff.ID, "completed", "all good"); err != nil {
		t.Fatal(err)
	}
	done, err := instance.GetHandoff(ctx, created.Handoff.ID)
	if err != nil || done.Status != "completed" || done.Result != "all good" || done.CompletedAt == "" {
		t.Fatalf("completed = %#v, %v", done, err)
	}
	count, err := instance.CountActiveHandoffsForOriginRun(ctx, "origin-a")
	if err != nil || count != 0 {
		t.Fatalf("count after complete = %d, %v", count, err)
	}
}

func TestAppendHandoffResultWritesSourceNotTarget(t *testing.T) {
	ctx := t.Context()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	source, target := seedTwoAgents(t, instance)
	created := createHandoff(t, instance, source, target, CreateAgentHandoffInput{})
	msg, err := instance.AppendHandoffResultToSource(ctx, created.Handoff.ID, "review complete")
	if err != nil {
		t.Fatal(err)
	}
	if msg.ConversationID != source.Conversation.ID || msg.Kind != domain.MessageKindHandoffResult || msg.AuthorBotID != target.Bot.ID || msg.HandoffID != created.Handoff.ID {
		t.Fatalf("result message = %#v", msg)
	}
	sourceMessages, err := instance.ListMessages(ctx, source.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sourceMessages) != 2 || sourceMessages[1].Kind != domain.MessageKindHandoffResult || sourceMessages[1].Content != "review complete" {
		t.Fatalf("source messages = %#v", sourceMessages)
	}
	targetMessages, err := instance.ListMessages(ctx, target.Conversation.ID)
	if err != nil || len(targetMessages) != 1 || targetMessages[0].Kind != domain.MessageKindHandoff {
		t.Fatalf("target messages = %#v, %v", targetMessages, err)
	}
}
