package harness

import "testing"

func TestNativeGrokCommandUsesFixedWorkspaceAndSession(t *testing.T) {
	command := NativeGrokCommand("/tmp/Atlas workspace", "019ff4eb-ceb8-7b41-84b8-052797452567")
	if command != "cd '/tmp/Atlas workspace' && grok --cwd '/tmp/Atlas workspace' --resume '019ff4eb-ceb8-7b41-84b8-052797452567'" {
		t.Fatalf("command = %q", command)
	}
}

func TestNativeGrokCommandSupportsSafeSessionControls(t *testing.T) {
	command, err := NativeGrokCommandWithOptions("/tmp/Atlas workspace", NativeGrokOptions{
		SessionID:       "019ff4eb-ceb8-7b41-84b8-052797452567",
		Fork:            true,
		Dashboard:       true,
		Fullscreen:      true,
		Model:           "grok-4.5",
		ReasoningEffort: "high",
		PermissionMode:  "plan",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "cd '/tmp/Atlas workspace' && grok --cwd '/tmp/Atlas workspace' --resume '019ff4eb-ceb8-7b41-84b8-052797452567' --fork-session --dashboard --fullscreen --model 'grok-4.5' --reasoning-effort 'high' --permission-mode 'plan'"
	if command != want {
		t.Fatalf("command = %q, want %q", command, want)
	}
}
