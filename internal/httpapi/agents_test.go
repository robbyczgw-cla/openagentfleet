package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
	"github.com/robbyczgw-cla/openagentfleet/internal/testexe"
)

const httpGrokWebSearchHelperEnv = "OPENAGENTFLEET_HTTP_GROK_WEB_SEARCH_TEST_HELPER"

func TestAgentsAPICreatesAtomicAgentAndPersistsMetadata(t *testing.T) {
	instance, handler := openAgentsAPIServer(t)
	response := agentsAPIRequest(handler, http.MethodPost, "/api/agents", `{
		"name":"Research","title":"Research teammate","description":"Find primary sources.",
		"metadata":{"lead":{"harness":"codex_app_server","model":"gpt-5","reasoning":"high","service_tier":"priority","permission":"ask"},"workers":[{"id":"worker-a","harness":"claude","model":"opus","reasoning":"xhigh","service_tier":"default","permission":"read_only","max_turns":12,"timeout_seconds":600}],"orchestrator":"local","plugin_ids":["web"],"mcp_ids":["browser"]}
	}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("POST /api/agents = %d, body = %s", response.Code, response.Body.String())
	}
	var created domain.Agent
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Conversation == nil || created.Conversation.BotID != created.Bot.ID || len(created.Conversations) != 1 {
		t.Fatalf("created agent = %#v", created)
	}
	if created.Metadata == nil || created.Metadata.Lead == nil || created.Metadata.Lead.Model != "gpt-5" || created.Metadata.Lead.WebSearch != domain.AgentWebSearchLive || len(created.Metadata.Workers) != 1 || created.MetadataPersistence != domain.AgentMetadataPersisted {
		t.Fatalf("metadata contract = %#v", created)
	}

	stored, err := instance.ListAgents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Metadata == nil || stored[0].Metadata.Lead == nil || stored[0].Metadata.Lead.Model != "gpt-5" || stored[0].Metadata.Lead.WebSearch != domain.AgentWebSearchLive || stored[0].Metadata.Workers[0].Reasoning != "xhigh" || !stored[0].Metadata.NotifyFinished || !stored[0].Metadata.NotifyNeedsInput || stored[0].MetadataPersistence != domain.AgentMetadataPersisted {
		t.Fatalf("metadata was not persisted: %#v", stored)
	}
}

func TestAgentsAPIComputerBindingSurvivesEngineSwitch(t *testing.T) {
	instance, handler := openAgentsAPIServer(t)
	createdResponse := agentsAPIRequest(handler, http.MethodPost, "/api/agents", `{
		"name":"Scout","title":"Research teammate","description":"Persistent identity.",
		"metadata":{"lead":{"harness":"codex_app_server","model":"gpt-5"},"computer":{"id":"desk-1","backend":"docker"}}
	}`)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create = %d, body = %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created domain.Agent
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Bot.ID == "" || created.Metadata == nil || created.Metadata.Computer == nil || created.Metadata.Computer.ID != "desk-1" {
		t.Fatalf("created computer binding = %#v", created)
	}
	botID := created.Bot.ID
	patchedResponse := agentsAPIRequest(handler, http.MethodPatch, "/api/agents/"+botID, `{
		"metadata":{"lead":{"harness":"grok_build","model":"grok-4.6"}}
	}`)
	if patchedResponse.Code != http.StatusOK {
		t.Fatalf("patch engine = %d, body = %s", patchedResponse.Code, patchedResponse.Body.String())
	}
	var patched domain.Agent
	if err := json.NewDecoder(patchedResponse.Body).Decode(&patched); err != nil {
		t.Fatal(err)
	}
	if patched.Bot.ID != botID {
		t.Fatalf("engine switch changed identity: %q -> %q", botID, patched.Bot.ID)
	}
	if patched.Metadata == nil || patched.Metadata.Lead == nil || patched.Metadata.Lead.Harness != "grok_build" {
		t.Fatalf("engine did not switch: %#v", patched.Metadata)
	}
	if patched.Metadata.Computer == nil || patched.Metadata.Computer.ID != "desk-1" || patched.Metadata.Computer.Backend != "docker" {
		t.Fatalf("computer binding lost: %#v", patched.Metadata.Computer)
	}
	listed, err := instance.ListAgents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Bot.ID != botID || listed[0].Metadata == nil || listed[0].Metadata.Computer == nil || listed[0].Metadata.Computer.ID != "desk-1" {
		t.Fatalf("stored computer = %#v", listed)
	}
}

func TestAgentsAPIPatchesDurableProfileAndConfiguration(t *testing.T) {
	instance, handler := openAgentsAPIServer(t)
	createdResponse := agentsAPIRequest(handler, http.MethodPost, "/api/agents", `{"name":"Original","title":"Original teammate","description":"Before."}`)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create = %d, body = %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created domain.Agent
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	response := agentsAPIRequest(handler, http.MethodPatch, "/api/agents/"+created.Bot.ID, `{
		"name":"Updated","description":"After.",
		"metadata":{"lead":{"harness":"codex_app_server","model":"gpt-5.6-sol","reasoning":"max","service_tier":"flex","permission":"workspace"},"notify_finished":false,"notify_needs_input":false,"avatar":{"emoji":"🤖"}}
	}`)
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH /api/agents = %d, body = %s", response.Code, response.Body.String())
	}
	var patched domain.Agent
	if err := json.NewDecoder(response.Body).Decode(&patched); err != nil {
		t.Fatal(err)
	}
	if patched.Bot.Name != "Updated" || patched.Bot.Title != "Original teammate" || patched.Bot.Description != "After." {
		t.Fatalf("patched profile = %#v", patched.Bot)
	}
	if patched.Metadata == nil || patched.Metadata.Lead == nil || patched.Metadata.Lead.Model != "gpt-5.6-sol" || patched.Metadata.Lead.Reasoning != "max" || patched.Metadata.NotifyFinished || patched.Metadata.NotifyNeedsInput || patched.Metadata.Avatar == nil || patched.Metadata.Avatar.Emoji != "🤖" || patched.MetadataPersistence != domain.AgentMetadataPersisted {
		t.Fatalf("patched configuration contract = %#v", patched)
	}

	listed, err := instance.ListAgents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Bot.Name != "Updated" || listed[0].Metadata == nil || listed[0].Metadata.Lead == nil || listed[0].Metadata.Lead.Model != "gpt-5.6-sol" || listed[0].Metadata.NotifyFinished || listed[0].Metadata.NotifyNeedsInput || listed[0].Metadata.Avatar == nil {
		t.Fatalf("PATCH did not persist configuration: %#v", listed)
	}
	if badAvatar := agentsAPIRequest(handler, http.MethodPatch, "/api/agents/"+created.Bot.ID, `{"metadata":{"avatar":{"url":"file:///tmp/avatar.png"}}}`); badAvatar.Code != http.StatusBadRequest {
		t.Fatalf("file avatar status = %d, body = %s", badAvatar.Code, badAvatar.Body.String())
	}
	if missing := agentsAPIRequest(handler, http.MethodPatch, "/api/agents/missing", `{"title":"Missing"}`); missing.Code != http.StatusNotFound {
		t.Fatalf("missing agent status = %d, body = %s", missing.Code, missing.Body.String())
	}
}

func TestAgentsAPIListsOneAgentPerBotAndAllAdvancedConversations(t *testing.T) {
	instance, handler := openAgentsAPIServer(t)
	createdResponse := agentsAPIRequest(handler, http.MethodPost, "/api/agents", `{"name":"Planner","title":"Planning teammate","description":""}`)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create = %d, body = %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created domain.Agent
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	second, err := instance.CreateConversation(t.Context(), created.Bot.ID, "Advanced thread")
	if err != nil {
		t.Fatal(err)
	}

	response := agentsAPIRequest(handler, http.MethodGet, "/api/agents", "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/agents = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		Agents []domain.Agent `json:"agents"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Agents) != 1 || body.Agents[0].Conversation == nil || body.Agents[0].Conversation.ID != created.Conversation.ID {
		t.Fatalf("agents = %#v", body.Agents)
	}
	if body.Agents[0].ConversationMode != domain.AgentConversationModeAdvancedMulti || len(body.Agents[0].Conversations) != 2 || body.Agents[0].Conversations[1].ID != second.ID {
		t.Fatalf("advanced conversation mapping = %#v", body.Agents[0])
	}
}

func TestAgentsAPIRejectsInvalidDurableAndMetadataFields(t *testing.T) {
	_, handler := openAgentsAPIServer(t)
	for _, body := range []string{
		`{"name":"","title":"Title","description":""}`,
		`{"name":"Name","title":"` + strings.Repeat("x", domain.MaxAgentTitleBytes+1) + `","description":""}`,
		`{"name":"Name","title":"Title","description":"","metadata":{"worker_ids":["same","same"]}}`,
		`{"name":"Name","title":"Title","description":"","metadata":{"lead":{"harness":"claude","reasoning":"high","service_tier":"default","permission":"ask"}}}`,
		`{"name":"Name","title":"Title","description":"","metadata":{"lead":{"harness":"codex_app_server","web_search":"cached"}}}`,
		`{"name":"Name","title":"Title","description":"","metadata":{"workers":[{"id":"one","harness":"claude","web_search":"live","max_turns":8,"timeout_seconds":300}]}}`,
		`{"name":"Name","title":"Title","description":"","metadata":{"workers":[{"id":"one","harness":"claude","reasoning":"turbo","service_tier":"default","permission":"ask"}]}}`,
		`{"name":"Name","title":"Title","description":"","metadata":{"workers":[{"id":"one","harness":"claude","reasoning":"high","service_tier":"enterprise","permission":"ask"}]}}`,
		`{"name":"Name","title":"Title","description":"","metadata":{"workers":[{"id":"one","harness":"claude","reasoning":"high","service_tier":"default","permission":"ask","timeout_seconds":300}]}}`,
		`{"name":"Name","title":"Title","description":"","metadata":{"workers":[{"id":"one","harness":"claude","reasoning":"high","service_tier":"default","permission":"ask","max_turns":8}]}}`,
		`{"name":"Name","title":"Title","description":"","unknown":true}`,
	} {
		response := agentsAPIRequest(handler, http.MethodPost, "/api/agents", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid request status = %d, body = %s", response.Code, response.Body.String())
		}
	}
}

func TestConfiguredAgentLeadOverridesLegacyPerMessageProviderAndAddsProfileContext(t *testing.T) {
	_, handler := openAgentsAPIServer(t)
	createdResponse := agentsAPIRequest(handler, http.MethodPost, "/api/agents", `{
		"name":"Release Captain","title":"Owns safe release preparation","description":"Checks evidence before changing release state.",
		"metadata":{"lead":{"harness":"codex_app_server","model":"gpt-5.6-sol","reasoning":"xhigh","service_tier":"priority","permission":"ask"}}
	}`)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create = %d, body = %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created domain.Agent
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	response := agentsAPIRequest(handler, http.MethodPost, "/api/messages", `{
		"conversation_id":"`+created.Conversation.ID+`","content":"Prepare a release checklist.",
		"provider":"pi","model":"untrusted-override","reasoning_effort":"low","permission_mode":"auto"
	}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("message = %d, body = %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Run domain.Run `json:"run"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Run.Provider != "codex_app_server" {
		t.Fatalf("provider = %q, want configured lead", envelope.Run.Provider)
	}
	if !strings.Contains(envelope.Run.Prompt, "Prepare a release checklist.") {
		t.Fatalf("run prompt is missing the task: %s", envelope.Run.Prompt)
	}
	systemPrompt := agentSystemPrompt(created.Bot)
	for _, expected := range []string{"Release Captain", "Owns safe release preparation", "Checks evidence before changing release state."} {
		if !strings.Contains(systemPrompt, expected) {
			t.Fatalf("agent system prompt missing %q: %s", expected, systemPrompt)
		}
	}
	if strings.Contains(envelope.Run.Prompt, "untrusted-override") {
		t.Fatalf("request-level execution override leaked into prompt: %s", envelope.Run.Prompt)
	}
}

func TestConfiguredLeadProviderRoutesAllThreeLeadHarnesses(t *testing.T) {
	for configured, want := range map[string]string{
		"grok_build":             "grok",
		"codex_app_server":       harness.CodexAppServerProvider,
		harness.OpenCodeProvider: harness.OpenCodeProvider,
		"pi":                     "pi",
	} {
		if got := configuredLeadProvider(configured); got != want {
			t.Fatalf("configuredLeadProvider(%q) = %q, want %q", configured, got, want)
		}
	}
}

func TestConfiguredOpenCodeLeadExecutesJSONStreamAndResumesSession(t *testing.T) {
	directory := t.TempDir()
	argumentsPath := filepath.Join(directory, "opencode-arguments")
	stdoutPath := filepath.Join(directory, "opencode-stdout.txt")
	if err := os.WriteFile(stdoutPath, []byte("{\"type\":\"step_start\",\"sessionID\":\"ses_http_opencode\",\"part\":{\"type\":\"step-start\"}}\n{\"type\":\"text\",\"sessionID\":\"ses_http_opencode\",\"part\":{\"type\":\"text\",\"text\":\"OpenCode answer\"}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wrapper := testexe.Path(directory, harness.OpenCodeProvider)
	testexe.WritePowerShell(t, wrapper,
		"#!/bin/sh\n{\nprintf 'CALL\\n'\nfor argument in \"$@\"; do printf 'ARG=%s\\n' \"$argument\"; done\n} >> "+shellQuoteHTTPTest(argumentsPath)+"\ncat "+shellQuoteHTTPTest(stdoutPath)+"\n",
		"[IO.File]::AppendAllText('"+powershellHTTPPath(argumentsPath)+"', \"CALL`n\")\nforeach ($argument in $args) { [IO.File]::AppendAllText('"+powershellHTTPPath(argumentsPath)+"', \"ARG=$argument`n\") }\nGet-Content -LiteralPath '"+powershellHTTPPath(stdoutPath)+"'\n",
	)
	t.Setenv("OPENAGENTFLEET_OPENCODE_BINARY", wrapper)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))

	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	handler := (&Server{
		Store:                 instance,
		HarnessWorkdir:        directory,
		AllowHarnessExecution: true,
		Runner:                harness.NewRunner(true),
		RunTimeout:            5 * time.Second,
	}).Handler()
	createdResponse := agentsAPIRequest(handler, http.MethodPost, "/api/agents", `{
		"name":"OpenCode Lead","title":"Runs OpenCode safely",
		"metadata":{"lead":{"harness":"opencode","model":"openai/gpt-5","reasoning":"high","service_tier":"default","permission":"provider_default"}}
	}`)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create = %d, body = %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created domain.Agent
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	waitForRuns := func(want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			runs, listErr := instance.ListRuns(t.Context(), created.Conversation.ID)
			if listErr != nil {
				t.Fatal(listErr)
			}
			if len(runs) == want && runs[want-1].Status == "completed" {
				return
			}
			if len(runs) > 0 && runs[len(runs)-1].Status == "failed" {
				t.Fatalf("OpenCode run failed: %s", runs[len(runs)-1].Error)
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %d OpenCode runs: %#v", want, runs)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	for index, prompt := range []string{"Start the lead.", "Resume the lead."} {
		response := agentsAPIRequest(handler, http.MethodPost, "/api/messages", `{"conversation_id":"`+created.Conversation.ID+`","content":"`+prompt+`","provider":"codex"}`)
		if response.Code != http.StatusAccepted {
			t.Fatalf("message %d = %d, body = %s", index+1, response.Code, response.Body.String())
		}
		var envelope struct {
			Run domain.Run `json:"run"`
		}
		if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Run.Provider != harness.OpenCodeProvider {
			t.Fatalf("run provider = %q, want OpenCode", envelope.Run.Provider)
		}
		waitForRuns(index + 1)
	}

	session, err := instance.GetHarnessSession(t.Context(), created.Conversation.ID, harness.OpenCodeProvider)
	if err != nil {
		t.Fatal(err)
	}
	if session.NativeSessionID != "ses_http_opencode" {
		t.Fatalf("native session = %q", session.NativeSessionID)
	}
	arguments, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	argumentText := string(arguments)
	for _, expected := range []string{
		"ARG=run\nARG=--pure\nARG=--format\nARG=json\nARG=--dir\nARG=" + directory,
		"ARG=--model\nARG=openai/gpt-5",
		"ARG=--variant\nARG=high",
		"ARG=--session\nARG=ses_http_opencode",
	} {
		if !strings.Contains(argumentText, expected) {
			t.Fatalf("OpenCode arguments missing %q: %s", expected, argumentText)
		}
	}
	if strings.Contains(argumentText, "ARG=--auto") {
		t.Fatalf("OpenCode arguments enabled dangerous auto approval: %s", argumentText)
	}
	messages, err := instance.ListMessages(t.Context(), created.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) == 0 || messages[len(messages)-1].Role != "assistant" || messages[len(messages)-1].Content != "OpenCode answer" {
		t.Fatalf("OpenCode assistant output was not persisted: %#v", messages)
	}
}

func TestConfiguredAgentWebSearchReachesNativeLeadHarness(t *testing.T) {
	directory := t.TempDir()
	argumentsPath := filepath.Join(directory, "arguments")
	testexe.WriteReexec(t, directory, "grok", "^TestHTTPGrokWebSearchHelper$", argumentsPath, map[string]string{
		httpGrokWebSearchHelperEnv: "1",
	})
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))

	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	handler := (&Server{
		Store:                 instance,
		HarnessWorkdir:        directory,
		AllowHarnessExecution: true,
		Runner:                harness.NewRunner(true),
		RunTimeout:            5 * time.Second,
	}).Handler()
	createdResponse := agentsAPIRequest(handler, http.MethodPost, "/api/agents", `{
		"name":"Search Contract","title":"Tests native search",
		"metadata":{"lead":{"harness":"grok_build","web_search":"disabled"}}
	}`)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create = %d, body = %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created domain.Agent
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	messageResponse := agentsAPIRequest(handler, http.MethodPost, "/api/messages", `{"conversation_id":"`+created.Conversation.ID+`","content":"Verify native web search."}`)
	if messageResponse.Code != http.StatusAccepted {
		t.Fatalf("message = %d, body = %s", messageResponse.Code, messageResponse.Body.String())
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		runs, err := instance.ListRuns(t.Context(), created.Conversation.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) == 1 && (runs[0].Status == "completed" || runs[0].Status == "failed") {
			if runs[0].Status != "completed" {
				t.Fatalf("run failed: %s", runs[0].Error)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for configured lead run")
		}
		time.Sleep(10 * time.Millisecond)
	}
	arguments, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.ReplaceAll(string(arguments), "\r\n", "\n")
	if !strings.Contains("\n"+normalized, "\n--disable-web-search\n") {
		t.Fatalf("configured web_search did not reach Grok: %q", arguments)
	}
}

func TestHTTPGrokWebSearchHelper(t *testing.T) {
	if os.Getenv(httpGrokWebSearchHelperEnv) != "1" {
		return
	}
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := decoder.Decode(&request); err != nil {
			return
		}
		if len(request.ID) == 0 {
			continue
		}
		result := any(map[string]any{})
		switch request.Method {
		case "initialize":
			result = map[string]any{"authMethods": []any{}}
		case "session/new":
			result = map[string]string{"sessionId": "http-web-search-test"}
		case "session/prompt":
			result = map[string]string{"stopReason": "end_turn"}
		}
		if err := encoder.Encode(struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  any             `json:"result"`
		}{JSONRPC: "2.0", ID: request.ID, Result: result}); err != nil {
			return
		}
	}
}

func shellQuoteHTTPTest(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func powershellHTTPPath(path string) string {
	return strings.ReplaceAll(filepath.ToSlash(path), "'", "''")
}

func TestGrokAgentPermissionMappingNeverBroadensWorkspaceToAuto(t *testing.T) {
	for input, want := range map[string]string{
		"ask": "default", "read_only": "plan", "workspace": "default",
	} {
		got, err := grokAgentPermissionMode(input)
		if err != nil {
			t.Fatalf("%s: %v", input, err)
		}
		if got != want {
			t.Fatalf("%s mapped to %q, want %q", input, got, want)
		}
	}
}

func TestPiLeadPermissionMappingUsesWorkspaceForUsageDefaults(t *testing.T) {
	for input, want := range map[string]string{
		"": "workspace", "default": "workspace", "plan": "workspace",
		"read_only": "read_only", "workspace": "workspace", "ask": "ask",
	} {
		got, err := piLeadPermissionMode(input)
		if err != nil {
			t.Fatalf("%s: %v", input, err)
		}
		if got != want {
			t.Fatalf("%s mapped to %q, want %q", input, got, want)
		}
	}
	if _, err := piLeadPermissionMode("auto"); err == nil {
		t.Fatal("Pi lead auto permission was accepted")
	}
}

func TestConfiguredPiLeadIsAccepted(t *testing.T) {
	_, handler := openAgentsAPIServer(t)
	createdResponse := agentsAPIRequest(handler, http.MethodPost, "/api/agents", `{
		"name":"Pi Lead","title":"Runs Pi as the workspace engine",
		"metadata":{"lead":{"harness":"pi","model":"openai/gpt-4o","reasoning":"high","service_tier":"default","permission":"workspace"}}
	}`)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create Pi lead = %d, body = %s", createdResponse.Code, createdResponse.Body.String())
	}
}

func TestConfiguredGrokLeadRejectsUnsupportedServiceTierAtConfigurationBoundary(t *testing.T) {
	_, handler := openAgentsAPIServer(t)
	createdResponse := agentsAPIRequest(handler, http.MethodPost, "/api/agents", `{
		"name":"Grok Lead","title":"Runs Grok safely",
		"metadata":{"lead":{"harness":"grok_build","model":"grok-4.5","reasoning":"high","service_tier":"priority","permission":"ask"}}
	}`)
	if createdResponse.Code != http.StatusBadRequest || !strings.Contains(createdResponse.Body.String(), "service-tier") {
		t.Fatalf("unsupported tier = %d, body = %s", createdResponse.Code, createdResponse.Body.String())
	}
}

func TestAgentMetadataPatchMergesWithoutErasingExecutionConfiguration(t *testing.T) {
	_, handler := openAgentsAPIServer(t)
	createdResponse := agentsAPIRequest(handler, http.MethodPost, "/api/agents", `{
		"name":"Merge Test","title":"Keeps configuration",
		"metadata":{
			"lead":{"harness":"codex_app_server","model":"gpt-5.6-sol","reasoning":"high","service_tier":"priority","permission":"ask","web_search":"live"},
			"workers":[{"id":"reviewer","harness":"claude","model":"opus","reasoning":"high","service_tier":"default","permission":"read_only","max_turns":8,"timeout_seconds":300}],
			"plugin_ids":["github"],"mcp_ids":["browser"]
		}
	}`)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create = %d, body = %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created domain.Agent
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	patchedResponse := agentsAPIRequest(handler, http.MethodPatch, "/api/agents/"+created.Bot.ID, `{"metadata":{"notify_finished":false}}`)
	if patchedResponse.Code != http.StatusOK {
		t.Fatalf("patch = %d, body = %s", patchedResponse.Code, patchedResponse.Body.String())
	}
	var patched domain.Agent
	if err := json.NewDecoder(patchedResponse.Body).Decode(&patched); err != nil {
		t.Fatal(err)
	}
	if patched.Metadata == nil || patched.Metadata.Lead == nil || patched.Metadata.Lead.Model != "gpt-5.6-sol" || len(patched.Metadata.Workers) != 1 || len(patched.Metadata.PluginIDs) != 1 || len(patched.Metadata.MCPIDs) != 1 || patched.Metadata.NotifyFinished {
		t.Fatalf("partial patch erased metadata: %#v", patched.Metadata)
	}
	if patched.Bot.UpdatedAt == created.Bot.UpdatedAt {
		t.Fatalf("metadata-only patch left bot updated_at stale: %q", patched.Bot.UpdatedAt)
	}
	leadPatchResponse := agentsAPIRequest(handler, http.MethodPatch, "/api/agents/"+created.Bot.ID, `{"metadata":{"lead":{"model":"gpt-5.6-sol-patched","web_search":"disabled"}}}`)
	if leadPatchResponse.Code != http.StatusOK {
		t.Fatalf("lead patch = %d, body = %s", leadPatchResponse.Code, leadPatchResponse.Body.String())
	}
	var leadPatched domain.Agent
	if err := json.NewDecoder(leadPatchResponse.Body).Decode(&leadPatched); err != nil {
		t.Fatal(err)
	}
	if leadPatched.Metadata == nil || leadPatched.Metadata.Lead == nil || leadPatched.Metadata.Lead.Model != "gpt-5.6-sol-patched" || leadPatched.Metadata.Model != "gpt-5.6-sol-patched" || leadPatched.Metadata.LeadHarness != "codex_app_server" || leadPatched.Metadata.Lead.Reasoning != "high" || leadPatched.Metadata.Lead.ServiceTier != "priority" || leadPatched.Metadata.Lead.Permission != "ask" || leadPatched.Metadata.Lead.WebSearch != domain.AgentWebSearchDisabled {
		t.Fatalf("partial lead patch erased fields: %#v", leadPatched.Metadata)
	}

	clearedResponse := agentsAPIRequest(handler, http.MethodPatch, "/api/agents/"+created.Bot.ID, `{"metadata":{"workers":[]}}`)
	if clearedResponse.Code != http.StatusOK {
		t.Fatalf("clear workers = %d, body = %s", clearedResponse.Code, clearedResponse.Body.String())
	}
	var cleared domain.Agent
	if err := json.NewDecoder(clearedResponse.Body).Decode(&cleared); err != nil {
		t.Fatal(err)
	}
	if cleared.Metadata == nil || len(cleared.Metadata.Workers) != 0 || cleared.Metadata.Lead == nil {
		t.Fatalf("explicit worker clear = %#v", cleared.Metadata)
	}
}

func TestConcurrentAgentMetadataPatchesDoNotLoseIndependentUpdates(t *testing.T) {
	instance, handler := openAgentsAPIServer(t)
	createdResponse := agentsAPIRequest(handler, http.MethodPost, "/api/agents", `{"name":"Concurrent","title":"Serializes patches"}`)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create = %d, body = %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created domain.Agent
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	bodies := []string{`{"metadata":{"plugin_ids":["github"]}}`, `{"metadata":{"mcp_ids":["browser"]}}`}
	responses := make([]*httptest.ResponseRecorder, len(bodies))
	var wait sync.WaitGroup
	for index, body := range bodies {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			responses[index] = agentsAPIRequest(handler, http.MethodPatch, "/api/agents/"+created.Bot.ID, body)
		}()
	}
	close(start)
	wait.Wait()
	for index, response := range responses {
		if response.Code != http.StatusOK {
			t.Fatalf("patch %d = %d, body = %s", index, response.Code, response.Body.String())
		}
	}
	agents, err := instance.ListAgents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Metadata == nil || len(agents[0].Metadata.PluginIDs) != 1 || len(agents[0].Metadata.MCPIDs) != 1 {
		t.Fatalf("concurrent patch lost an update: %#v", agents)
	}
}

func TestAgentsAPIPresenceAndClearLead(t *testing.T) {
	instance, handler := openAgentsAPIServer(t)
	createdResponse := agentsAPIRequest(handler, http.MethodPost, "/api/agents", `{
		"name":"Builder","title":"Builder","description":"Builds.",
		"metadata":{"lead":{"harness":"codex_app_server","model":"gpt-5.5","reasoning":"high","service_tier":"default","permission":"ask"}}
	}`)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create = %d, body = %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created domain.Agent
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Metadata == nil || created.Metadata.Lead == nil || created.Metadata.Lead.Harness != "codex_app_server" {
		t.Fatalf("lead override not persisted: %#v", created.Metadata)
	}
	run, err := instance.CreateRun(t.Context(), created.Conversation.ID, created.Bot.ID, "codex_app_server", "ship it")
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.UpdateRun(t.Context(), run.ID, "waiting_for_approval", ""); err != nil {
		t.Fatal(err)
	}

	listed := agentsAPIRequest(handler, http.MethodGet, "/api/agents", "")
	if listed.Code != http.StatusOK {
		t.Fatalf("list = %d, body = %s", listed.Code, listed.Body.String())
	}
	var body struct {
		Agents []domain.Agent `json:"agents"`
	}
	if err := json.NewDecoder(listed.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Agents) != 1 || body.Agents[0].Presence == nil || body.Agents[0].Presence.State != domain.PresenceWaitingApproval {
		t.Fatalf("presence = %#v", body.Agents)
	}

	cleared := agentsAPIRequest(handler, http.MethodPatch, "/api/agents/"+created.Bot.ID, `{"metadata":{"clear_lead":true}}`)
	if cleared.Code != http.StatusOK {
		t.Fatalf("clear lead = %d, body = %s", cleared.Code, cleared.Body.String())
	}
	var after domain.Agent
	if err := json.NewDecoder(cleared.Body).Decode(&after); err != nil {
		t.Fatal(err)
	}
	if after.Metadata == nil || after.Metadata.Lead != nil || after.Metadata.LeadHarness != "" {
		t.Fatalf("cleared lead = %#v", after.Metadata)
	}

	stored, err := instance.ListAgents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Metadata == nil || stored[0].Metadata.Lead != nil {
		t.Fatalf("cleared lead was not persisted: %#v", stored)
	}
}

func TestAgentsAPIRosterPatchAndUnreadOnAttentionRun(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	server := &Server{Store: instance, HarnessWorkdir: t.TempDir()}
	handler := server.Handler()

	createdResponse := agentsAPIRequest(handler, http.MethodPost, "/api/agents", `{"name":"Scout","title":"Research teammate"}`)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create = %d, body = %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created domain.Agent
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Pinned || created.Hidden || created.Unread {
		t.Fatalf("created roster = %#v", created)
	}

	listed := agentsAPIRequest(handler, http.MethodGet, "/api/agents", "")
	if listed.Code != http.StatusOK {
		t.Fatalf("list = %d, body = %s", listed.Code, listed.Body.String())
	}
	var body struct {
		Agents []domain.Agent `json:"agents"`
	}
	if err := json.NewDecoder(listed.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Agents) != 1 || body.Agents[0].Pinned || body.Agents[0].Unread || body.Agents[0].Hidden {
		t.Fatalf("listed roster = %#v", body.Agents)
	}

	patched := agentsAPIRequest(handler, http.MethodPatch, "/api/agents/"+created.Bot.ID+"/roster", `{"pinned":true,"unread":true}`)
	if patched.Code != http.StatusOK {
		t.Fatalf("PATCH roster = %d, body = %s", patched.Code, patched.Body.String())
	}
	var after domain.Agent
	if err := json.NewDecoder(patched.Body).Decode(&after); err != nil {
		t.Fatal(err)
	}
	if !after.Pinned || !after.Unread || after.Hidden {
		t.Fatalf("patched roster = %#v", after)
	}

	listed = agentsAPIRequest(handler, http.MethodGet, "/api/agents", "")
	if err := json.NewDecoder(listed.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Agents) != 1 || !body.Agents[0].Pinned || !body.Agents[0].Unread {
		t.Fatalf("GET roster = %#v", body.Agents)
	}

	hidden := agentsAPIRequest(handler, http.MethodPatch, "/api/agents/"+created.Bot.ID+"/roster", `{"hidden":true,"unread":false}`)
	if hidden.Code != http.StatusOK {
		t.Fatalf("PATCH hidden = %d, body = %s", hidden.Code, hidden.Body.String())
	}
	if empty := agentsAPIRequest(handler, http.MethodPatch, "/api/agents/"+created.Bot.ID+"/roster", `{}`); empty.Code != http.StatusBadRequest {
		t.Fatalf("empty roster status = %d, body = %s", empty.Code, empty.Body.String())
	}
	if missing := agentsAPIRequest(handler, http.MethodPatch, "/api/agents/missing/roster", `{"pinned":true}`); missing.Code != http.StatusNotFound {
		t.Fatalf("missing roster status = %d, body = %s", missing.Code, missing.Body.String())
	}

	run, err := instance.CreateRun(t.Context(), created.Conversation.ID, created.Bot.ID, "codex_app_server", "ship it")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.commitRunLifecycleEvent(t.Context(), run, "failed", "boom", "run.failed", `{"status":"failed"}`); err != nil {
		t.Fatalf("commit failed run: %v", err)
	}
	listed = agentsAPIRequest(handler, http.MethodGet, "/api/agents", "")
	if err := json.NewDecoder(listed.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Agents) != 1 || !body.Agents[0].Unread || !body.Agents[0].Pinned || !body.Agents[0].Hidden {
		t.Fatalf("failed run unread = %#v", body.Agents)
	}
}

func openAgentsAPIServer(t *testing.T) (*store.Store, http.Handler) {
	t.Helper()
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	return instance, (&Server{Store: instance, HarnessWorkdir: t.TempDir()}).Handler()
}

func agentsAPIRequest(handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
