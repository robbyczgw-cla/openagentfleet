package domain

import "testing"

func TestDeriveAgentPresencePriority(t *testing.T) {
	bot := "bot-andy"
	peer := "bot-cami"
	active := &Run{ID: "run-1", BotID: bot, Status: "running"}
	waiting := &Run{ID: "run-1", BotID: bot, Status: "waiting_for_approval"}
	failed := &Run{ID: "run-1", BotID: bot, Status: "failed", Error: "provider exited"}
	handoff := Handoff{SourceBotID: bot, TargetBotID: peer, Status: HandoffStatusRunning}

	cases := []struct {
		name  string
		input PresenceInput
		want  string
	}{
		{name: "idle when empty", input: PresenceInput{BotID: bot}, want: PresenceIdle},
		{name: "approval beats computer", input: PresenceInput{BotID: bot, Run: waiting, Computer: ComputerPresence{Running: true, AgentControl: true, Takeover: true}}, want: PresenceWaitingApproval},
		{name: "takeover while running", input: PresenceInput{BotID: bot, Run: active, Computer: ComputerPresence{Running: true, Takeover: true}}, want: PresenceWaitingTakeover},
		{name: "using computer", input: PresenceInput{BotID: bot, Run: active, Computer: ComputerPresence{Running: true, AgentControl: true}}, want: PresenceUsingComputer},
		{name: "computer without this agent run stays idle", input: PresenceInput{BotID: bot, Computer: ComputerPresence{Running: true, AgentControl: true}}, want: PresenceIdle},
		{name: "collaborating", input: PresenceInput{BotID: bot, Handoffs: []Handoff{handoff}}, want: PresenceCollaborating},
		{name: "working", input: PresenceInput{BotID: bot, Run: active}, want: PresenceWorking},
		{name: "queued", input: PresenceInput{BotID: bot, Run: &Run{Status: "queued"}}, want: PresenceWorking},
		{name: "failed", input: PresenceInput{BotID: bot, Run: failed}, want: PresenceFailed},
		{name: "completed is idle", input: PresenceInput{BotID: bot, Run: &Run{Status: "completed"}}, want: PresenceIdle},
		{name: "peer handoff does not mark bystander", input: PresenceInput{BotID: "bot-other", Handoffs: []Handoff{handoff}}, want: PresenceIdle},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := DeriveAgentPresence(test.input)
			if got.State != test.want {
				t.Fatalf("state = %q (%s), want %q", got.State, got.Label, test.want)
			}
			if got.Label == "" {
				t.Fatal("presence label is required")
			}
		})
	}
}
