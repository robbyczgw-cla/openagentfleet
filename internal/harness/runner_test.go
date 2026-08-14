package harness

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRunnerRequiresExplicitGate(t *testing.T) {
	_, err := NewRunner(false).Run(context.Background(), "grok", "hello", t.TempDir(), nil)
	if err != ErrExecutionDisabled {
		t.Fatalf("error = %v", err)
	}
}

func TestAssistantTextExtractsGrokStream(t *testing.T) {
	output := "{\"type\":\"thinking\",\"data\":\"hidden\"}\n{\"type\":\"text\",\"data\":\"first\"}\n{\"type\":\"text\",\"data\":\"second\"}"
	if got := AssistantText("grok", output); got != "firstsecond" {
		t.Fatalf("assistant text = %q", got)
	}
}

func TestAssistantTextExtractsCodexAppServerStream(t *testing.T) {
	output := "{\"type\":\"text\",\"data\":\"first\"}\n{\"type\":\"thought\",\"data\":\"hidden\"}\n{\"type\":\"text\",\"data\":\"second\"}"
	if got := AssistantText(CodexAppServerProvider, output); got != "firstsecond" {
		t.Fatalf("assistant text = %q", got)
	}
}

func TestAssistantTextPreservesCodexDeltaWhitespace(t *testing.T) {
	output := "{\"type\":\"text\",\"data\":\"Open\"}\n" +
		"{\"type\":\"text\",\"data\":\"Agent\"}\n" +
		"{\"type\":\"text\",\"data\":\"Fleet\"}\n" +
		"{\"type\":\"text\",\"data\":\"\\nready\"}"
	if got := AssistantText(CodexAppServerProvider, output); got != "OpenAgentFleet\nready" {
		t.Fatalf("assistant text = %q", got)
	}
}

func TestAssistantTextExtractsCursorAssistantEvents(t *testing.T) {
	output := "{\"type\":\"system\",\"session_id\":\"chat-1\"}\n{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"first\"}]}}\n{\"type\":\"tool_call\",\"text\":\"hidden\"}\n{\"type\":\"assistant\",\"delta\":\"second\"}"
	if got := AssistantText("cursor", output); got != "first\nsecond" {
		t.Fatalf("assistant text = %q", got)
	}
}

func TestSessionIDFromCursorStream(t *testing.T) {
	if got := sessionIDFromOutput("cursor", `{"type":"system","session_id":"chat-123"}`); got != "chat-123" {
		t.Fatalf("session id = %q", got)
	}
}

func TestAssistantTextAndSessionIDParseOpenCodeEvents(t *testing.T) {
	output := "{\"type\":\"step_start\",\"sessionID\":\"ses_open_123\",\"part\":{\"type\":\"step-start\"}}\n" +
		"{\"type\":\"text\",\"sessionID\":\"ses_open_123\",\"part\":{\"type\":\"text\",\"text\":\"first\"}}\n" +
		"{\"type\":\"tool\",\"sessionID\":\"ses_open_123\",\"part\":{\"type\":\"tool\",\"text\":\"hidden\"}}\n" +
		"{\"type\":\"text\",\"sessionID\":\"ses_open_123\",\"part\":{\"type\":\"text\",\"text\":\"second\"}}"
	if got := AssistantText(OpenCodeProvider, output); got != "first\nsecond" {
		t.Fatalf("assistant text = %q", got)
	}
	if got := sessionIDFromOutput(OpenCodeProvider, strings.Split(output, "\n")[0]); got != "ses_open_123" {
		t.Fatalf("session id = %q", got)
	}
	if got := outputType(strings.Split(output, "\n")[1]); got != "text" {
		t.Fatalf("output type = %q", got)
	}
}

func TestOpenCodeExitErrorsExplainAuthenticationAndProviderSetup(t *testing.T) {
	cause := errors.New("exit status 1")
	auth := openCodeExitError(cause, `{"type":"error","error":{"name":"ProviderAuthError","data":{"message":"API key is missing"}}}`)
	if !strings.Contains(auth.Error(), "opencode auth login") || !errors.Is(auth, cause) {
		t.Fatalf("auth error = %v", auth)
	}
	provider := openCodeExitError(cause, `{"type":"error","error":{"name":"ProviderNotFoundError","data":{"message":"unknown provider local-missing"}}}`)
	if !strings.Contains(provider.Error(), "opencode models") || !errors.Is(provider, cause) {
		t.Fatalf("provider error = %v", provider)
	}
}

func TestRedactRemovesCommonSecrets(t *testing.T) {
	got := Redact(`Authorization: Bearer abcdefghijklmnop api_key="secret-value" xai-123456789012`)
	if got == `Authorization: Bearer abcdefghijklmnop api_key="secret-value" xai-123456789012` || got == "" {
		t.Fatalf("redaction did not change value: %q", got)
	}
}
