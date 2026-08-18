package store

import (
	"path/filepath"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/policy"
)

func TestCreateAgentHandoffWritesBothConversationsAndOneTargetRun(t *testing.T) {
	ctx := t.Context()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	source, err := instance.ListAgents(ctx)
	if err != nil || len(source) == 0 {
		t.Fatalf("seed agents = %#v, %v", source, err)
	}
	target, err := instance.CreateAgent(ctx, domain.AgentDraft{
		Name: "Reviewer", Title: "Reviews handed-off work", Description: "Second visible agent.", ConversationTitle: "Review",
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceConv := *source[0].Conversation
	targetConv := *target.Conversation
	result, err := instance.CreateAgentHandoff(ctx, CreateAgentHandoffInput{
		SourceConversationID: sourceConv.ID,
		SourceBotID:          source[0].Bot.ID,
		TargetBotID:          target.Bot.ID,
		TargetConversationID: targetConv.ID,
		Content:              "Please review the local notes.",
		TargetProvider:       "grok",
		TargetPrompt:         "handoff prompt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Handoff.SourceBotID == result.Handoff.TargetBotID {
		t.Fatal("handoff used the same agent on both sides")
	}
	if result.Run.BotID != target.Bot.ID || result.Run.ConversationID != targetConv.ID {
		t.Fatalf("target run = %#v", result.Run)
	}
	sourceMessages, err := instance.ListMessages(ctx, sourceConv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sourceMessages) != 1 || sourceMessages[0].Kind != domain.MessageKindHandoff || sourceMessages[0].HandoffID != result.Handoff.ID {
		t.Fatalf("source messages = %#v", sourceMessages)
	}
	targetMessages, err := instance.ListMessages(ctx, targetConv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(targetMessages) != 1 || targetMessages[0].AuthorBotID != source[0].Bot.ID {
		t.Fatalf("target messages = %#v", targetMessages)
	}
}

func TestUpsertPolicyRuleRoundTrip(t *testing.T) {
	ctx := t.Context()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	rule := policy.Rule{
		ID:         "rule-host-shell",
		Principal:  policy.Principal{Kind: policy.PrincipalLead, ID: "bot-1"},
		Effect:     policy.EffectAllow,
		Scope:      policy.Scope{Resource: policy.Resource{Kind: policy.ResourceNativeApp, Target: "host.shell"}, Match: policy.MatchExact},
		Operations: []string{"execute"},
	}
	if err := instance.UpsertPolicyRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	rules, err := instance.ListPolicyRules(ctx)
	if err != nil || len(rules) != 1 || rules[0].ID != rule.ID || rules[0].Effect != policy.EffectAllow {
		t.Fatalf("rules = %#v, %v", rules, err)
	}
}
