package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/browsermcp"
	"github.com/robbyczgw-cla/openagentfleet/internal/compute"
	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/events"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
	"github.com/robbyczgw-cla/openagentfleet/internal/policy"
	"github.com/robbyczgw-cla/openagentfleet/internal/skillworkshop"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
	"github.com/robbyczgw-cla/openagentfleet/internal/teach"
)

func TestAgentHandoffAPICreatesVisibleTransferAndTargetRun(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	sourceConv, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	target, err := instance.CreateAgent(t.Context(), domain.AgentDraft{
		Name: "Reviewer", Title: "Reviews handed-off work", Description: "Second visible agent.",
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Store: instance, Broker: events.New(), HarnessWorkdir: t.TempDir()}).Handler()

	same := performRequest(handler, http.MethodPost, "/api/messages", `{"conversation_id":"`+sourceConv.ID+`","content":"stay here","mention_bot_ids":["`+sourceConv.BotID+`"]}`, "")
	if same.Code != http.StatusBadRequest || !strings.Contains(same.Body.String(), "different agent") {
		t.Fatalf("same-agent handoff = %d %s", same.Code, same.Body.String())
	}
	two := performRequest(handler, http.MethodPost, "/api/messages", `{"conversation_id":"`+sourceConv.ID+`","content":"two","mention_bot_ids":["`+target.Bot.ID+`","bot-other"]}`, "")
	if two.Code != http.StatusBadRequest || !strings.Contains(two.Body.String(), "only one agent mention") {
		t.Fatalf("two mentions = %d %s", two.Code, two.Body.String())
	}

	response := performRequest(handler, http.MethodPost, "/api/messages", `{"conversation_id":"`+sourceConv.ID+`","content":"Please review the notes.","mention_bot_ids":["`+target.Bot.ID+`"]}`, "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("handoff = %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		Message domain.Message `json:"message"`
		Run     domain.Run     `json:"run"`
		Handoff domain.Handoff `json:"handoff"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Message.Kind != domain.MessageKindHandoff || payload.Handoff.TargetBotID != target.Bot.ID {
		t.Fatalf("handoff payload = %#v", payload)
	}
	if payload.Run.BotID != target.Bot.ID || payload.Run.ConversationID != target.Conversation.ID {
		t.Fatalf("target run stayed on the source agent: %#v", payload.Run)
	}
	if !strings.Contains(payload.Run.Prompt, "Do not assume the sender's computer") {
		t.Fatalf("target prompt missing computer isolation: %s", payload.Run.Prompt)
	}
	sourceMessages, err := instance.ListMessages(t.Context(), sourceConv.ID)
	if err != nil || len(sourceMessages) != 1 || sourceMessages[0].Kind != domain.MessageKindHandoff {
		t.Fatalf("source messages = %#v, %v", sourceMessages, err)
	}
	targetMessages, err := instance.ListMessages(t.Context(), target.Conversation.ID)
	if err != nil || len(targetMessages) != 1 || targetMessages[0].AuthorBotID != sourceConv.BotID {
		t.Fatalf("target messages = %#v, %v", targetMessages, err)
	}
}

func TestPersistedApprovalRulesSkipPromptAndStayComputerHostSeparated(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	conversation, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := instance.CreateRunWithQueuedEvent(t.Context(), conversation.ID, conversation.BotID, "grok", "check policy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.UpdateRunWithLifecycleEvent(t.Context(), run.ID, "waiting_for_approval", "", "run.waiting_for_approval", `{"status":"waiting_for_approval"}`); err != nil {
		t.Fatal(err)
	}
	approval, err := instance.CreateApproval(t.Context(), run.ID, "grok", "Run host command", `{"options":[{"optionId":"allow_once","name":"Allow once","kind":"allow_once"}],"tool_call":{"kind":"execute","title":"Run host command"}}`)
	if err != nil {
		t.Fatal(err)
	}
	serverValue := &Server{Store: instance}
	handler := serverValue.Handler()
	response := performRequest(handler, http.MethodPost, "/api/approvals/"+approval.ID, `{"status":"approved","option_id":"allow_once","persist":"always_allow"}`, "")
	if response.Code != http.StatusOK {
		t.Fatalf("persist allow = %d %s", response.Code, response.Body.String())
	}
	rules, err := instance.ListPolicyRules(t.Context())
	if err != nil || len(rules) != 1 || rules[0].Effect != policy.EffectAllow || rules[0].Scope.Resource.Target != hostShellTarget {
		t.Fatalf("persisted rules = %#v, %v", rules, err)
	}

	host, err := serverValue.awaitApproval(t.Context(), run, harness.PermissionRequest{
		Options:  json.RawMessage(`[{"optionId":"allow_once"}]`),
		ToolCall: json.RawMessage(`{"kind":"execute","title":"Run host command"}`),
	})
	if err != nil || host.Outcome != "selected" || host.OptionID != "allow_once" {
		t.Fatalf("host always-allow = %#v, %v", host, err)
	}
	pending, err := instance.ListApprovals(t.Context(), "pending")
	if err != nil || len(pending) != 0 {
		t.Fatalf("always-allow created a new prompt: %#v, %v", pending, err)
	}

	computerAction, err := classifyPermissionAction(run, harness.PermissionRequest{
		ToolCall: json.RawMessage(`{"kind":"computer_click","title":"Click desktop"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := matchPersistedPolicy(t.Context(), rules, computerAction); got.Effect != policy.EffectAsk {
		t.Fatalf("host-shell always-allow leaked onto a computer action: %#v", got)
	}
}

func TestTeachStopSkillDraftContainsRecordedSteps(t *testing.T) {
	root := t.TempDir()
	recorder, err := teach.New(teach.Config{Root: filepath.Join(root, "traces")})
	if err != nil {
		t.Fatal(err)
	}
	workshop, err := skillworkshop.New(filepath.Join(root, "workshop"))
	if err != nil {
		t.Fatal(err)
	}
	serverValue := &Server{Teach: recorder, TeachRoot: recorder.Root(), Workshop: workshop, EnabledSkillsRoot: filepath.Join(root, "enabled")}
	handler := serverValue.Handler()
	start := performRequest(handler, http.MethodPost, "/api/teach/start", `{"goal":"Open the status page"}`, "")
	if start.Code != http.StatusCreated {
		t.Fatalf("teach start = %d %s", start.Code, start.Body.String())
	}
	if _, err := serverValue.Teach.Record(teach.Action{Surface: teach.SurfaceBrowser, Type: teach.ActionNavigate, URL: "https://example.com/status"}); err != nil {
		t.Fatal(err)
	}
	if _, err := serverValue.Teach.Record(teach.Action{Surface: teach.SurfaceBrowser, Type: teach.ActionClick, Point: &teach.Point{X: 12, Y: 40}}); err != nil {
		t.Fatal(err)
	}
	stop := performRequest(handler, http.MethodPost, "/api/teach/stop", "", "")
	if stop.Code != http.StatusOK || !strings.Contains(stop.Body.String(), `"auto_enabled":false`) {
		t.Fatalf("teach stop = %d %s", stop.Code, stop.Body.String())
	}
	drafts, err := workshop.List()
	if err != nil || len(drafts) != 1 {
		t.Fatalf("drafts = %#v, %v", drafts, err)
	}
	inspection, err := workshop.Inspect(drafts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inspection.Skill, "[browser] navigate") || !strings.Contains(inspection.Skill, "[browser] click") {
		t.Fatalf("SKILL.md missing recorded steps:\n%s", inspection.Skill)
	}
	if _, err := workshop.Deployment(drafts[0].ID, serverValue.EnabledSkillsRoot); err == nil {
		t.Fatal("teach stop auto-enabled the skill")
	}
}

func TestAwaitApprovalHonorsAlwaysDenyWithoutNewPrompt(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	conversation, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "run-deny", BotID: conversation.BotID, Provider: "grok"}
	action, err := classifyPermissionAction(run, harness.PermissionRequest{ToolCall: json.RawMessage(`{"kind":"execute","title":"rm"}`)})
	if err != nil {
		t.Fatal(err)
	}
	rule := persistedRuleFromAction(action, policy.EffectDeny)
	if err := instance.UpsertPolicyRule(t.Context(), rule); err != nil {
		t.Fatal(err)
	}
	serverValue := &Server{Store: instance}
	decision, err := serverValue.awaitApproval(context.Background(), run, harness.PermissionRequest{
		Options:  json.RawMessage(`[{"optionId":"allow_once"}]`),
		ToolCall: json.RawMessage(`{"kind":"execute","title":"rm"}`),
	})
	if err != nil || decision.Outcome != "cancelled" {
		t.Fatalf("always-deny = %#v, %v", decision, err)
	}
	pending, err := instance.ListApprovals(t.Context(), "pending")
	if err != nil || len(pending) != 0 {
		t.Fatalf("deny rule still prompted: %#v, %v", pending, err)
	}
}

func TestHandoffDoesNotTransferSourceComputerCapability(t *testing.T) {
	instance, source := openOrchestrationStore(t, false, false)
	target, err := instance.CreateAgent(t.Context(), domain.AgentDraft{Name: "Isolated", Title: "No inherited computer"})
	if err != nil {
		t.Fatal(err)
	}
	executor := &recordingHarnessExecutor{}
	serverValue := &Server{
		Store:                   instance,
		Docker:                  &compute.Docker{},
		RemoteToken:             "controller-secret",
		AgentComputerMCPCommand: executableFixture(t),
		AllowHarnessExecution:   true,
		runExecutorOverride:     executor,
		HarnessWorkdir:          t.TempDir(),
		computerAgentControl:    true,
	}
	response := performRequest(serverValue.Handler(), http.MethodPost, "/api/messages", `{"conversation_id":"`+source.Conversation.ID+`","content":"do not inherit the desktop","mention_bot_ids":["`+target.Bot.ID+`"]}`, "controller-secret")
	if response.Code != http.StatusAccepted {
		t.Fatalf("handoff = %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		Run domain.Run `json:"run"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(executor.snapshot()) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls := executor.snapshot()
	if len(calls) == 0 {
		t.Fatal("target run did not start")
	}
	if payload.Run.BotID != target.Bot.ID {
		t.Fatalf("run stayed on source agent: %#v", payload.Run)
	}
	if len(calls[0].Options.MCPServers) != 1 || calls[0].Options.MCPServers[0].Env[browsermcp.RunIDEnv] != payload.Run.ID {
		t.Fatalf("target computer capability was not bound to the target run: %#v", calls[0].Options.MCPServers)
	}
}
