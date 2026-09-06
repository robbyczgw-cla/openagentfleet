package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubConnectionCORSAllowsBrowserSave(t *testing.T) {
	request := httptest.NewRequest(http.MethodOptions, "/api/connections/github", nil)
	request.Header.Set("Origin", "http://127.0.0.1:1420")
	request.Header.Set("Access-Control-Request-Method", http.MethodPut)
	request.Header.Set("Access-Control-Request-Headers", "content-type")
	response := httptest.NewRecorder()
	server := &Server{}
	server.Handler().ServeHTTP(response, request)
	if response.Code < 200 || response.Code >= 300 {
		t.Fatalf("preflight status = %d", response.Code)
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "http://127.0.0.1:1420" {
		t.Fatal("trusted browser origin not allowed")
	}
	for _, method := range strings.Split(response.Header().Get("Access-Control-Allow-Methods"), ",") {
		if strings.TrimSpace(method) == http.MethodPut {
			return
		}
	}
	t.Fatal("GitHub connection PUT is not allowed by the browser preflight")
}
