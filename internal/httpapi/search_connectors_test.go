package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/websearchplus"
)

func TestSearchConnectorsAPIGetPatchAndPersistence(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	stateDir := filepath.Join(t.TempDir(), "web-search")
	controller, err := websearchplus.NewController(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&Server{SearchConnectors: controller}).Handler()

	response := searchConnectorRequest(handler, http.MethodGet, "")
	status := decodeSearchConnectorStatus(t, response, http.StatusOK)
	if status.WebSearchPlusEnabled || status.HoundEnabled || status.WebSearchPlus.Enabled || status.Hound.Enabled {
		t.Fatalf("default API status = %#v", status)
	}
	if status.WebSearchPlusCredentialStatus != "external/not inspected" {
		t.Fatalf("credential status = %q", status.WebSearchPlusCredentialStatus)
	}

	response = searchConnectorRequest(handler, http.MethodPatch, `{"web_search_plus_enabled":true}`)
	status = decodeSearchConnectorStatus(t, response, http.StatusOK)
	if !status.WebSearchPlusEnabled || status.HoundEnabled || !status.WebSearchPlus.Enabled || status.Hound.Enabled {
		t.Fatalf("partial WSP PATCH = %#v", status)
	}
	response = searchConnectorRequest(handler, http.MethodPatch, `{"hound_enabled":true}`)
	status = decodeSearchConnectorStatus(t, response, http.StatusOK)
	if !status.WebSearchPlusEnabled || !status.HoundEnabled || !status.WebSearchPlus.Enabled || !status.Hound.Enabled {
		t.Fatalf("independent Hound PATCH = %#v", status)
	}

	reopened, err := websearchplus.NewController(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := reopened.Status(t.Context())
	if !reloaded.WebSearchPlusEnabled || !reloaded.HoundEnabled {
		t.Fatalf("persisted API status = %#v", reloaded)
	}
}

func TestSearchConnectorsAPIStrictPatch(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	controller, err := websearchplus.NewController(filepath.Join(t.TempDir(), "web-search"))
	if err != nil {
		t.Fatal(err)
	}
	handler := (&Server{SearchConnectors: controller}).Handler()
	for _, body := range []string{
		``,
		`null`,
		`[]`,
		`{"web_search_plus_enabled":null}`,
		`{"web_search_plus_enabled":1}`,
		`{"hound_enabled":"true"}`,
		`{"unknown":true}`,
		`{"hound_enabled":true} {"web_search_plus_enabled":true}`,
	} {
		response := searchConnectorRequest(handler, http.MethodPatch, body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("strict PATCH body %q = %d, response = %s", body, response.Code, response.Body.String())
		}
	}
	response := searchConnectorRequest(handler, http.MethodPatch, `{}`)
	status := decodeSearchConnectorStatus(t, response, http.StatusOK)
	if status.WebSearchPlusEnabled || status.HoundEnabled {
		t.Fatalf("empty PATCH changed state = %#v", status)
	}
}

func TestSearchConnectorsAPIUnavailableAndCredentialSafe(t *testing.T) {
	response := searchConnectorRequest((&Server{}).Handler(), http.MethodGet, "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil controller GET = %d, body = %s", response.Code, response.Body.String())
	}

	t.Setenv("PATH", t.TempDir())
	controller, err := websearchplus.NewController(filepath.Join(t.TempDir(), "web-search"))
	if err != nil {
		t.Fatal(err)
	}
	response = searchConnectorRequest((&Server{SearchConnectors: controller}).Handler(), http.MethodGet, "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET = %d, body = %s", response.Code, response.Body.String())
	}
	body := strings.ToLower(response.Body.String())
	if !strings.Contains(body, `"web_search_plus_credential_status":"external/not inspected"`) {
		t.Fatalf("credential metadata missing: %s", body)
	}
	for _, forbidden := range []string{"api_key", "credential_masked", "bearer", "token"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response contains forbidden credential field %q: %s", forbidden, body)
		}
	}
}

func searchConnectorRequest(handler http.Handler, method, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "/api/search-connectors", strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeSearchConnectorStatus(t *testing.T, response *httptest.ResponseRecorder, wantCode int) websearchplus.ControllerStatus {
	t.Helper()
	if response.Code != wantCode {
		t.Fatalf("status = %d, want %d, body = %s", response.Code, wantCode, response.Body.String())
	}
	var status websearchplus.ControllerStatus
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	return status
}
