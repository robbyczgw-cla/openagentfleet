package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

func TestFleetHostStatusUsesStableHostIDAndAuthorityRole(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	hostID, err := instance.GetOrCreateRemoteHostID(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/api/host/status", (&Server{Store: instance}).FleetHostHandler())

	first := httptest.NewRequest(http.MethodGet, "/api/host/status", nil)
	firstResponse := httptest.NewRecorder()
	mux.ServeHTTP(firstResponse, first)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", firstResponse.Code, firstResponse.Body.String())
	}
	var payload fleetHostStatus
	if err := json.NewDecoder(firstResponse.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.HostID != hostID || payload.APIVersion != 1 || payload.AuthVersion != domain.RemoteAuthVersionBearer || payload.Role != "authority" {
		t.Fatalf("unexpected host status: %#v", payload)
	}

	again := httptest.NewRequest(http.MethodGet, "/api/host/status", nil)
	againResponse := httptest.NewRecorder()
	mux.ServeHTTP(againResponse, again)
	var second fleetHostStatus
	if err := json.NewDecoder(againResponse.Body).Decode(&second); err != nil {
		t.Fatal(err)
	}
	if second.HostID != payload.HostID {
		t.Fatalf("host id changed: %q != %q", second.HostID, payload.HostID)
	}

	wrongMethod := httptest.NewRequest(http.MethodPost, "/api/host/status", strings.NewReader("{}"))
	wrongResponse := httptest.NewRecorder()
	mux.ServeHTTP(wrongResponse, wrongMethod)
	if wrongResponse.Code != http.StatusNotFound {
		t.Fatalf("POST status = %d, want 404", wrongResponse.Code)
	}
}

func TestFleetHostStatusWithoutStoreIsUnavailable(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/api/host/status", (&Server{}).FleetHostHandler())
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/host/status", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing store status = %d", response.Code)
	}
}
