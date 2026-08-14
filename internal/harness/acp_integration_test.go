package harness

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// This opt-in probe verifies the installed Grok Build ACP handshake without
// sending a model prompt or running a tool. It is intentionally excluded from
// normal CI because authentication is machine-local.
func TestInstalledGrokACPHandshake(t *testing.T) {
	if os.Getenv("OPENAGENTFLEET_RUN_GROK_ACP_SMOKE") != "1" {
		t.Skip("set OPENAGENTFLEET_RUN_GROK_ACP_SMOKE=1 to probe the installed Grok Build runtime")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := OpenGrokSession(ctx, GrokSessionOptions{Workdir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session.ID == "" {
		t.Fatal("ACP session id is empty")
	}
	if os.Getenv("OPENAGENTFLEET_RUN_GROK_ACP_PROMPT_SMOKE") != "1" {
		return
	}
	var assistantText string
	result, err := session.Prompt(ctx, "Reply with exactly ACP_SMOKE_OK. Do not use tools or read files.", func(notification ACPNotification) {
		if notification.Method != "session/update" {
			return
		}
		var params struct {
			Update struct {
				SessionUpdate string `json:"sessionUpdate"`
				Content       struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		}
		if err := json.Unmarshal(notification.Params, &params); err == nil && params.Update.SessionUpdate == "agent_message_chunk" {
			assistantText += params.Update.Content.Text
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("stop_reason=%s assistant=%q", result.StopReason, assistantText)
	if assistantText == "" {
		t.Fatal("ACP prompt returned no assistant stream")
	}
}
