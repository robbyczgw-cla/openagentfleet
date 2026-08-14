package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/compute"
)

func TestRuntimesEndpointReturnsInjectedInventory(t *testing.T) {
	want := []compute.RuntimeInfo{
		{
			ID:                    compute.RuntimeColima,
			Name:                  "Colima + Docker",
			Kind:                  "docker",
			Available:             true,
			Healthy:               true,
			Selected:              true,
			Context:               "colima-openagentfleet",
			SupportsAgentComputer: true,
		},
		{
			ID:                    compute.RuntimeAppleContainer,
			Name:                  "Apple Container",
			Kind:                  "apple_container",
			Experimental:          true,
			SupportsAgentComputer: false,
		},
	}
	response := performRequest((&Server{Runtimes: want}).Handler(), http.MethodGet, "/api/runtimes", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("runtimes GET = %d, body = %s", response.Code, response.Body.String())
	}
	var got []compute.RuntimeInfo
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) || got[0].ID != compute.RuntimeColima || !got[0].Healthy || got[1].SupportsAgentComputer {
		t.Fatalf("runtime inventory = %#v, want %#v", got, want)
	}
}

func TestColimaInstallIsExplicitAndLocalOnly(t *testing.T) {
	installed := false
	docker := compute.NewDocker(t.TempDir(), "", true)
	server := (&Server{
		Docker: docker,
		RuntimeInstaller: func(context.Context) error {
			installed = true
			return nil
		},
		RuntimeResolver: func(_ context.Context, requested string) (compute.RuntimeSelection, error) {
			return compute.RuntimeSelection{ID: requested, Name: "Colima + Docker", Context: "colima-openagentfleet"}, nil
		},
	}).Handler()

	remoteRequest := httptest.NewRequest(http.MethodPost, "/api/runtimes/colima/install", nil)
	remoteRequest.RemoteAddr = "100.64.0.20:4317"
	remoteResponse := httptest.NewRecorder()
	server.ServeHTTP(remoteResponse, remoteRequest)
	if remoteResponse.Code != http.StatusForbidden || installed {
		t.Fatalf("remote install = %d, installed=%v", remoteResponse.Code, installed)
	}

	localRequest := httptest.NewRequest(http.MethodPost, "/api/runtimes/colima/install", nil)
	localRequest.RemoteAddr = "127.0.0.1:4317"
	localResponse := httptest.NewRecorder()
	server.ServeHTTP(localResponse, localRequest)
	if localResponse.Code != http.StatusOK || !installed {
		t.Fatalf("local install = %d, installed=%v, body=%s", localResponse.Code, installed, localResponse.Body.String())
	}
	if docker.Context != "colima-openagentfleet" || docker.RuntimeID != compute.RuntimeColima {
		t.Fatalf("installed runtime was not selected: context=%q runtime=%q", docker.Context, docker.RuntimeID)
	}
}
