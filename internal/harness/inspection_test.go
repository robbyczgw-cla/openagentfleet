package harness

import "testing"

func TestReadOnlyInfoKindAllowlist(t *testing.T) {
	if err := ensureKnownInfoKind("inspect"); err != nil {
		t.Fatal(err)
	}
	if err := ensureKnownInfoKind("shell"); err == nil {
		t.Fatal("arbitrary info kind was accepted")
	}
}

func TestSessionIDValidationRejectsArgumentInjection(t *testing.T) {
	if err := validateSessionID("not-a-session"); err == nil {
		t.Fatal("invalid session id was accepted")
	}
	if err := validateSessionID("019ff4eb-ceb8-7b41-84b8-052797452567"); err != nil {
		t.Fatal(err)
	}
}
