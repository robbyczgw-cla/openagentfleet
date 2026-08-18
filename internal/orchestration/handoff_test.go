package orchestration

import "testing"

func TestValidateAgentHandoffRejectsSameAgentAndEmptyIDs(t *testing.T) {
	if err := ValidateAgentHandoff("bot-a", "bot-b"); err != nil {
		t.Fatalf("valid handoff error = %v", err)
	}
	if err := ValidateAgentHandoff("bot-a", "bot-a"); err == nil {
		t.Fatal("same-agent handoff was accepted")
	}
	if err := ValidateAgentHandoff("", "bot-b"); err == nil {
		t.Fatal("empty source was accepted")
	}
	if err := ValidateAgentHandoff("bot-a", " "); err == nil {
		t.Fatal("empty target was accepted")
	}
}

func TestSingleMentionBotIDAllowsOneDistinctAgent(t *testing.T) {
	id, err := SingleMentionBotID([]string{"", " bot-b ", "bot-b"})
	if err != nil || id != "bot-b" {
		t.Fatalf("single mention = %q, %v", id, err)
	}
	if _, err := SingleMentionBotID([]string{"bot-b", "bot-c"}); err == nil {
		t.Fatal("two mentions were accepted")
	}
	id, err = SingleMentionBotID(nil)
	if err != nil || id != "" {
		t.Fatalf("empty mentions = %q, %v", id, err)
	}
}
