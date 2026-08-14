package harness

import "testing"

func TestPromptWithSystemFallbackKeepsControllerAndTaskBoundaries(t *testing.T) {
	if got := promptWithSystemFallback("", "task"); got != "task" {
		t.Fatalf("empty system fallback = %q", got)
	}
	got := promptWithSystemFallback("role instruction", "user task")
	want := "OpenAgentFleet controller instructions (higher priority than the task below):\nrole instruction\n\nCurrent user task and approved context:\nuser task"
	if got != want {
		t.Fatalf("system fallback = %q, want %q", got, want)
	}
}
