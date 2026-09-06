package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/collaborationmcp"
	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

func TestMemoryProposalHTTPRequiresUserAcceptance(t *testing.T) {
	server, run, _ := openMemoryProposalHTTPTestServer(t)
	server.RemoteToken = "reviewer-only"
	handler := server.Handler()
	request := httptest.NewRequest(http.MethodPost, "/api/collaboration/memory-proposals", strings.NewReader(`{"category":"preference","content":"Use CSV for reports","priority":3}`))
	request.Header.Set("Authorization", "Bearer memory-token")
	request.Header.Set(collaborationmcp.RunIDHeader, run.ID)
	request.Header.Set(collaborationmcp.RunTokenHeader, "memory-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var proposal domain.MemoryProposal
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &proposal) != nil {
		t.Fatalf("create = %d %s", response.Code, response.Body.String())
	}
	path := "/api/memory-proposals/" + proposal.ID + "/accept"
	for _, withHeaders := range []bool{true, false} {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		request.Header.Set("Authorization", "Bearer memory-token")
		if withHeaders {
			request.Header.Set(collaborationmcp.RunIDHeader, run.ID)
			request.Header.Set(collaborationmcp.RunTokenHeader, "memory-token")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("self approval = %d %s", response.Code, response.Body.String())
		}
	}
	stored, err := server.Store.GetMemoryProposal(t.Context(), proposal.ID)
	if err != nil || stored.Status != domain.MemoryProposalStatusPending {
		t.Fatalf("proposal = %#v, %v", stored, err)
	}
	accepted := performRequest(handler, http.MethodPost, path, `{}`, "reviewer-only")
	if accepted.Code != http.StatusOK {
		t.Fatalf("reviewer acceptance = %d %s", accepted.Code, accepted.Body.String())
	}
}
