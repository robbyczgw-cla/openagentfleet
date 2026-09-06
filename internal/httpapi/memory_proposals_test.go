package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/collaborationmcp"
	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
	"github.com/robbyczgw-cla/openagentfleet/internal/testexe"
)

func TestAgentMemoryProposalUsesCapabilityRunAndMessage(t *testing.T) {
	server, run, message := openMemoryProposalHTTPTestServer(t)
	request := httptest.NewRequest(http.MethodPost, "/api/collaboration/memory-proposals", strings.NewReader(`{"category":"fact","content":"The project uses Go.","priority":4}`))
	request.Header.Set(collaborationmcp.RunIDHeader, run.ID)
	request.Header.Set(collaborationmcp.RunTokenHeader, "memory-token")
	request.Header.Set("Authorization", "Bearer memory-token")
	response := httptest.NewRecorder()
	server.createAgentMemoryProposal(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create proposal = %d %s", response.Code, response.Body.String())
	}
	var proposal domain.MemoryProposal
	if err := json.Unmarshal(response.Body.Bytes(), &proposal); err != nil {
		t.Fatal(err)
	}
	if proposal.BotID != run.BotID || proposal.SourceRunID != run.ID || proposal.SourceMessageID != message.ID || proposal.Status != domain.MemoryProposalStatusPending {
		t.Fatalf("server-bound provenance = %#v", proposal)
	}

	forgedRun := httptest.NewRequest(http.MethodPost, "/api/collaboration/memory-proposals", strings.NewReader(`{"category":"fact","content":"forged"}`))
	forgedRun.Header.Set(collaborationmcp.RunIDHeader, "another-run")
	forgedRun.Header.Set(collaborationmcp.RunTokenHeader, "memory-token")
	forgedRun.Header.Set("Authorization", "Bearer memory-token")
	forgedResponse := httptest.NewRecorder()
	server.createAgentMemoryProposal(forgedResponse, forgedRun)
	if forgedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("forged run scope = %d %s", forgedResponse.Code, forgedResponse.Body.String())
	}

	server.revokeCollabCapability("memory-token")
	revoked := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/collaboration/memory-proposals", strings.NewReader(`{"category":"fact","content":"revoked"}`))
	request.Header.Set(collaborationmcp.RunIDHeader, run.ID)
	request.Header.Set(collaborationmcp.RunTokenHeader, "memory-token")
	request.Header.Set("Authorization", "Bearer memory-token")
	server.createAgentMemoryProposal(revoked, request)
	if revoked.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token = %d %s", revoked.Code, revoked.Body.String())
	}
}

func TestMemoryCapabilityCannotReadCollaborationOrGitHub(t *testing.T) {
	server, run, _ := openMemoryProposalHTTPTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "/api/collaboration/agents", nil)
	request.Header.Set(collaborationmcp.RunIDHeader, run.ID)
	request.Header.Set(collaborationmcp.RunTokenHeader, "memory-token")
	request.Header.Set("Authorization", "Bearer memory-token")
	if _, err := server.authorizedCollaborationRun(request); err == nil {
		t.Fatal("memory capability authorized collaboration")
	}
	if _, err := server.authorizedBridgeRun(request); err == nil {
		t.Fatal("memory capability authorized GitHub bridge access")
	}
	server.collabCapabilityMu.Lock()
	capability := server.collabCapabilities["memory-token"]
	capability.expiresAt = time.Now().UTC().Add(-time.Second)
	server.collabCapabilities["memory-token"] = capability
	server.collabCapabilityMu.Unlock()
	if _, err := server.authorizedMemoryProposalRun(request); err == nil {
		t.Fatal("stale memory capability remained valid")
	}
}

func TestAPIGateSeparatesLocalReviewerAndBridgeCredentials(t *testing.T) {
	server, run, _ := openMemoryProposalHTTPTestServer(t)
	server.RemoteToken = "local-reviewer-token"

	local := httptest.NewRequest(http.MethodPost, "/api/memory-proposals/proposal-1/accept", nil)
	local.Header.Set("Authorization", "Bearer local-reviewer-token")
	if !server.authorizeAPIRequest(local) {
		t.Fatal("global local credential could not reach reviewer route")
	}

	bridge := func(method, path, bearer, runID, runToken string) *http.Request {
		request := httptest.NewRequest(method, path, nil)
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		if runID != "" {
			request.Header.Set(collaborationmcp.RunIDHeader, runID)
		}
		if runToken != "" {
			request.Header.Set(collaborationmcp.RunTokenHeader, runToken)
		}
		return request
	}
	if !server.authorizeAPIRequest(bridge(http.MethodPost, "/api/collaboration/memory-proposals", "memory-token", run.ID, "memory-token")) {
		t.Fatal("scoped bridge credential could not reach proposal creation route")
	}
	for _, request := range []*http.Request{
		bridge(http.MethodPost, "/api/memory-proposals/proposal-1/accept", "memory-token", run.ID, "memory-token"),
		bridge(http.MethodPost, "/api/memory-proposals/proposal-1/accept", "memory-token", "", ""),
		bridge(http.MethodPost, "/api/collaboration/memory-proposals", "memory-token", "forged-run", "memory-token"),
		bridge(http.MethodPost, "/api/collaboration/memory-proposals", "memory-token", run.ID, "forged-token"),
		bridge(http.MethodPost, "/api/connections/github/read", "memory-token", run.ID, "memory-token"),
	} {
		if server.authorizeAPIRequest(request) {
			t.Fatalf("bridge credential crossed API scope: %s %s", request.Method, request.URL.Path)
		}
	}
	server.revokeCollabCapability("memory-token")
	if server.authorizeAPIRequest(bridge(http.MethodPost, "/api/collaboration/memory-proposals", "memory-token", run.ID, "memory-token")) {
		t.Fatal("revoked bridge bearer remained valid")
	}

	server.setCollabCapabilityScope("github-token", collaborationCapabilityScopeGitHub)
	server.bindCollabCapability("github-token", run.ID)
	if !server.authorizeAPIRequest(bridge(http.MethodPost, "/api/connections/github/read", "github-token", run.ID, "github-token")) {
		t.Fatal("GitHub-only bearer could not reach its read route")
	}
	for _, path := range []string{"/api/collaboration/memory-proposals", "/api/memory-proposals/proposal-1/accept"} {
		if server.authorizeAPIRequest(bridge(http.MethodPost, path, "github-token", run.ID, "github-token")) {
			t.Fatalf("GitHub-only bearer reached %s", path)
		}
	}
	server.revokeCollabCapability("github-token")
}

func TestMemoryBridgeIssuanceIsFeatureBoundAndExcludesPi(t *testing.T) {
	server, run, _ := openMemoryProposalHTTPTestServer(t)
	server.RemoteToken = "local-reviewer-token"
	server.CollaborationMCPCommand = testexe.WriteEcho(t, t.TempDir(), "collaboration-mcp", "ok")
	agent, found, err := server.agentForBot(t.Context(), run.BotID)
	if err != nil || !found {
		t.Fatalf("agent = %#v, %t, %v", agent, found, err)
	}
	token, specs, err := server.appendCollaborationMCP(t.Context(), nil, agent, true, "grok")
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || len(specs) != 1 || specs[0].Env[collaborationmcp.MemoryEnabledEnv] != "1" || specs[0].Env[collaborationmcp.GitHubOnlyEnv] != "1" {
		t.Fatalf("memory-only issuance = %q %#v", token, specs)
	}
	if specs[0].Env[collaborationmcp.APITokenEnv] != token || specs[0].Env[collaborationmcp.APITokenEnv] == server.RemoteToken {
		t.Fatal("memory bridge received the global reviewer credential")
	}
	server.revokeCollabCapability(token)

	token, specs, err = server.appendCollaborationMCP(t.Context(), nil, agent, true, "pi")
	if err != nil || token != "" || len(specs) != 0 {
		t.Fatalf("Pi memory issuance = %q %#v, %v", token, specs, err)
	}
	if _, err := server.Store.PatchPreferences(t.Context(), []byte(`{"features":{"memory_proposals":false}}`)); err != nil {
		t.Fatal(err)
	}
	token, specs, err = server.appendCollaborationMCP(t.Context(), nil, agent, true, "grok")
	if err != nil || token != "" || len(specs) != 0 {
		t.Fatalf("disabled memory issuance = %q %#v, %v", token, specs, err)
	}
}

func TestTokenlessServerRejectsMemoryBridgeAndReview(t *testing.T) {
	server, run, message := openMemoryProposalHTTPTestServer(t)
	server.RemoteToken = ""
	server.CollaborationMCPCommand = testexe.WriteEcho(t, t.TempDir(), "collaboration-mcp", "ok")
	agent, found, err := server.agentForBot(t.Context(), run.BotID)
	if err != nil || !found {
		t.Fatalf("agent = %#v, %t, %v", agent, found, err)
	}
	if token, specs, err := server.appendCollaborationMCP(t.Context(), nil, agent, true, "grok"); err == nil || token != "" || len(specs) != 0 {
		t.Fatalf("tokenless bridge issuance = %q %#v, %v", token, specs, err)
	}

	create := httptest.NewRequest(http.MethodPost, "/api/collaboration/memory-proposals", strings.NewReader("{\"category\":\"fact\",\"content\":\"tokenless\"}"))
	create.Header.Set("Authorization", "Bearer memory-token")
	create.Header.Set(collaborationmcp.RunIDHeader, run.ID)
	create.Header.Set(collaborationmcp.RunTokenHeader, "memory-token")
	createResponse := httptest.NewRecorder()
	server.createAgentMemoryProposal(createResponse, create)
	if createResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("tokenless proposal creation = %d %s", createResponse.Code, createResponse.Body.String())
	}

	proposal, err := server.Store.CreateMemoryProposal(t.Context(), domain.MemoryProposalDraft{
		BotID: run.BotID, SourceRunID: run.ID, SourceMessageID: message.ID,
		Category: domain.MemoryCategoryFact, Content: "review requires controller auth", Priority: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	accept := httptest.NewRequest(http.MethodPost, "/api/memory-proposals/"+proposal.ID+"/accept", nil)
	acceptResponse := httptest.NewRecorder()
	server.acceptMemoryProposal(acceptResponse, accept)
	if acceptResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("tokenless proposal acceptance = %d %s", acceptResponse.Code, acceptResponse.Body.String())
	}
}

func TestMemoryReviewRejectsBridgeBearerAndRequiresControllerBearer(t *testing.T) {
	server, run, message := openMemoryProposalHTTPTestServer(t)
	proposal, err := server.Store.CreateMemoryProposal(t.Context(), domain.MemoryProposalDraft{
		BotID: run.BotID, SourceRunID: run.ID, SourceMessageID: message.ID,
		Category: domain.MemoryCategoryFact, Content: "controller review only", Priority: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, bearer := range []string{"", "memory-token", "forged"} {
		request := httptest.NewRequest(http.MethodPost, "/api/memory-proposals/"+proposal.ID+"/accept", nil)
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		request.Header.Set(collaborationmcp.RunIDHeader, run.ID)
		request.Header.Set(collaborationmcp.RunTokenHeader, "memory-token")
		response := httptest.NewRecorder()
		server.acceptMemoryProposal(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("review bearer %q = %d %s", bearer, response.Code, response.Body.String())
		}
	}
	loaded, err := server.Store.GetMemoryProposal(t.Context(), proposal.ID)
	if err != nil || loaded.Status != domain.MemoryProposalStatusPending {
		t.Fatalf("unauthorized review changed proposal = %#v, %v", loaded, err)
	}
}

func TestReviewerCanEditAcceptAndRejectMemoryProposals(t *testing.T) {
	server, run, message := openMemoryProposalHTTPTestServer(t)
	first, err := server.Store.CreateMemoryProposal(t.Context(), domain.MemoryProposalDraft{
		BotID: run.BotID, SourceRunID: run.ID, SourceMessageID: message.ID,
		Category: domain.MemoryCategoryFact, Content: "Initial wording", Priority: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	accept := httptest.NewRequest(http.MethodPost, "/api/memory-proposals/"+first.ID+"/accept", strings.NewReader(`{"category":"project","content":"Reviewer wording","priority":5}`))
	accept.Header.Set("Authorization", "Bearer controller")
	acceptedResponse := httptest.NewRecorder()
	server.acceptMemoryProposal(acceptedResponse, accept)
	if acceptedResponse.Code != http.StatusOK {
		t.Fatalf("accept = %d %s", acceptedResponse.Code, acceptedResponse.Body.String())
	}
	var accepted struct {
		Proposal domain.MemoryProposal `json:"proposal"`
		Memory   domain.BotMemory      `json:"memory"`
	}
	if err := json.Unmarshal(acceptedResponse.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.Memory.Content != "Reviewer wording" || accepted.Memory.Source != domain.MemorySourceAgentProposal || accepted.Proposal.MemoryID != accepted.Memory.ID {
		t.Fatalf("accepted payload = %#v", accepted)
	}

	second, err := server.Store.CreateMemoryProposal(t.Context(), domain.MemoryProposalDraft{
		BotID: run.BotID, SourceRunID: run.ID, SourceMessageID: message.ID,
		Category: domain.MemoryCategoryFact, Content: "Reject this", Priority: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	reject := httptest.NewRequest(http.MethodPost, "/api/memory-proposals/"+second.ID+"/reject", nil)
	reject.Header.Set("Authorization", "Bearer controller")
	rejectedResponse := httptest.NewRecorder()
	server.rejectMemoryProposal(rejectedResponse, reject)
	if rejectedResponse.Code != http.StatusOK || !strings.Contains(rejectedResponse.Body.String(), `"status":"rejected"`) {
		t.Fatalf("reject = %d %s", rejectedResponse.Code, rejectedResponse.Body.String())
	}
	memories, err := server.Store.RetrieveBotMemories(t.Context(), run.BotID, 20, 12*1024)
	if err != nil || len(memories) != 1 || memories[0].ID != accepted.Memory.ID {
		t.Fatalf("review retrieval boundary = %#v, %v", memories, err)
	}
}

func openMemoryProposalHTTPTestServer(t *testing.T) (*Server, domain.Run, domain.Message) {
	t.Helper()
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := instance.EnsureMemoryProposalsSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.PatchPreferences(t.Context(), []byte(`{"features":{"memory_proposals":true}}`)); err != nil {
		t.Fatal(err)
	}
	conversation, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	message, _, run, _, err := instance.CreateMessageWithAttachmentsAndRun(t.Context(), conversation.ID, conversation.BotID, "grok", "Remember useful context", "Remember useful context", nil)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: instance, RemoteToken: "controller"}
	server.setCollabCapabilityScope("memory-token", collaborationCapabilityScopeMemory)
	server.bindCollabCapability("memory-token", run.ID)
	return server, run, message
}
