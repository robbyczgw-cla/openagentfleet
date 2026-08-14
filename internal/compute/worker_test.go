package compute

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testRemoteWorkerToken = "0123456789abcdefghijklmnopqrstuvwxyz-remote-token"

func TestNewRemoteWorkerRequiresStrongToken(t *testing.T) {
	docker := NewDocker(t.TempDir(), "", true)
	if _, err := NewRemoteWorker(docker, "short"); err == nil {
		t.Fatal("short worker token was accepted")
	}
	worker, err := NewRemoteWorker(docker, testRemoteWorkerToken)
	if err != nil || worker == nil {
		t.Fatalf("valid worker token rejected: %v", err)
	}
}

func TestRemoteWorkerAuthenticatesEveryEndpoint(t *testing.T) {
	docker := NewDocker(t.TempDir(), "", false)
	worker, err := NewRemoteWorker(docker, testRemoteWorkerToken)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(worker.Handler())
	defer server.Close()

	unauthorized, err := http.Get(server.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.StatusCode)
	}

	request, err := http.NewRequest(http.MethodGet, server.URL+"/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testRemoteWorkerToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authorized status = %d", response.StatusCode)
	}
	var status Status
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Image == "" {
		t.Fatal("worker status omitted image metadata")
	}
}

func TestRemoteWorkerRejectsUnknownActionFields(t *testing.T) {
	docker := NewDocker(t.TempDir(), "", true)
	worker, err := NewRemoteWorker(docker, testRemoteWorkerToken)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(worker.Handler())
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/action", strings.NewReader(`{"action":"click","unexpected":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testRemoteWorkerToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown action field status = %d", response.StatusCode)
	}
}
