package domain

import "testing"

func TestCanonicalEventTypeMapsRunLifecycle(t *testing.T) {
	cases := map[string]string{
		EventRunStarted:            EventAgentTurnStarted,
		EventRunCompleted:          EventAgentTurnCompleted,
		EventRunFailed:             EventAgentTurnFailed,
		EventRunStopped:            EventAgentTurnCancelled,
		EventRunQueued:             EventRunQueued,
		EventRunBlocked:            EventRunBlocked,
		EventRunWaitingForApproval: EventRunWaitingForApproval,
		EventProviderOutput:        EventProviderOutput,
		EventSessionOpened:         EventSessionOpened,
		EventApprovalRequested:     EventApprovalRequested,
		EventApprovalResolved:      EventApprovalResolved,
		EventAgentTurnStarted:      EventAgentTurnStarted,
		EventAgentThinking:         EventAgentThinking,
		"custom.event":             "custom.event",
		"":                         "",
	}
	for existing, want := range cases {
		if got := CanonicalEventType(existing); got != want {
			t.Fatalf("CanonicalEventType(%q) = %q, want %q", existing, got, want)
		}
	}
}
