package httpapi

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/websearchplus"
)

func TestSearchMCPServerSpecsResolveOnlySelectedBuiltins(t *testing.T) {
	binDir := t.TempDir()
	uvx := filepath.Join(binDir, "uvx")
	if err := os.WriteFile(uvx, []byte("#!/bin/sh\nprintf 'uvx 0.9.18\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	controller, err := websearchplus.NewController(filepath.Join(t.TempDir(), "web-search"))
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := controller.Patch(t.Context(), websearchplus.ConnectorPatch{
		WebSearchPlusEnabled: &enabled,
		HoundEnabled:         &enabled,
	}); err != nil {
		t.Fatal(err)
	}

	server := &Server{SearchConnectors: controller}
	specs, err := server.searchMCPServerSpecs(t.Context(), []string{"hound", "web-search-plus", "hound"})
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 || specs[0].Name != "hound" || specs[1].Name != "web-search-plus" {
		t.Fatalf("specs = %#v", specs)
	}
	if specs[0].Command != uvx || specs[1].Command != uvx {
		t.Fatalf("commands = %q, %q; want resolved %q", specs[0].Command, specs[1].Command, uvx)
	}
	if !reflect.DeepEqual(specs[0].Args, []string{"--from", "hound-mcp==13.1.2", "hound"}) {
		t.Fatalf("hound args = %#v", specs[0].Args)
	}
	if !reflect.DeepEqual(specs[1].Args, []string{"--from", "web-search-plus-mcp==3.6.0", "web-search-plus-mcp", "serve"}) {
		t.Fatalf("WebSearchPlus args = %#v", specs[1].Args)
	}
	if configPath := specs[1].Env["WEB_SEARCH_PLUS_CONFIG"]; !filepath.IsAbs(configPath) {
		t.Fatalf("config path = %q", configPath)
	}
}

func TestSearchMCPServerSpecsRejectDisabledOrUnavailableBuiltin(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	controller, err := websearchplus.NewController(filepath.Join(t.TempDir(), "web-search"))
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{SearchConnectors: controller}
	if _, err := server.searchMCPServerSpecs(t.Context(), []string{"hound"}); err == nil || !strings.Contains(err.Error(), "disabled or its bundled launcher is not ready") {
		t.Fatalf("error = %v", err)
	}
	if _, err := server.searchMCPServerSpecs(t.Context(), []string{"user-global-mcp"}); err == nil || !strings.Contains(err.Error(), "no explicit OpenAgentFleet runtime resolver") {
		t.Fatalf("unknown MCP error = %v", err)
	}
	if _, err := (&Server{}).searchMCPServerSpecs(t.Context(), []string{"hound"}); err == nil {
		t.Fatal("missing controller was accepted")
	}
}
