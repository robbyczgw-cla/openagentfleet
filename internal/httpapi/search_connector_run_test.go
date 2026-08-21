package httpapi

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/testexe"
	"github.com/robbyczgw-cla/openagentfleet/internal/websearchplus"
)

func TestSearchMCPServerSpecsResolveOnlySelectedBuiltins(t *testing.T) {
	binDir := t.TempDir()
	uvx := testexe.WriteEcho(t, binDir, "uvx", "uvx 0.9.18")
	npx := testexe.WriteEcho(t, binDir, "npx", "10.9.2")
	t.Setenv("PATH", binDir)
	t.Setenv("OPENAGENTFLEET_OPENCODE_BINARY", "")
	controller, err := websearchplus.NewController(filepath.Join(t.TempDir(), "web-search"))
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := controller.Patch(t.Context(), websearchplus.ConnectorPatch{
		WebSearchPlusEnabled: &enabled,
		HoundEnabled:         &enabled,
		DonsetchEnabled:      &enabled,
	}); err != nil {
		t.Fatal(err)
	}

	server := &Server{SearchConnectors: controller}
	specs, err := server.searchMCPServerSpecs(t.Context(), []string{"hound", "web-search-plus", "donsetch", "hound"})
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 3 || specs[0].Name != "hound" || specs[1].Name != "web-search-plus" || specs[2].Name != "donsetch" {
		t.Fatalf("specs = %#v", specs)
	}
	if specs[0].Command != uvx || specs[1].Command != uvx {
		t.Fatalf("uvx commands = %q, %q; want resolved %q", specs[0].Command, specs[1].Command, uvx)
	}
	if specs[2].Command != npx {
		t.Fatalf("donsetch command = %q; want resolved %q", specs[2].Command, npx)
	}
	if !reflect.DeepEqual(specs[0].Args, []string{"--from", "hound-mcp==13.1.2", "hound"}) {
		t.Fatalf("hound args = %#v", specs[0].Args)
	}
	if !reflect.DeepEqual(specs[1].Args, []string{"--from", "web-search-plus-mcp==3.6.0", "web-search-plus-mcp", "serve"}) {
		t.Fatalf("WebSearchPlus args = %#v", specs[1].Args)
	}
	if !reflect.DeepEqual(specs[2].Args, []string{"--yes", "donsetch@2.1.0", "mcp"}) {
		t.Fatalf("donsetch args = %#v", specs[2].Args)
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
	if _, err := server.searchMCPServerSpecs(t.Context(), []string{"donsetch"}); err == nil || !strings.Contains(err.Error(), "disabled or its bundled launcher is not ready") {
		t.Fatalf("donsetch error = %v", err)
	}
	if _, err := server.searchMCPServerSpecs(t.Context(), []string{"user-global-mcp"}); err == nil || !strings.Contains(err.Error(), "no explicit OpenAgentFleet runtime resolver") {
		t.Fatalf("unknown MCP error = %v", err)
	}
	if _, err := (&Server{}).searchMCPServerSpecs(t.Context(), []string{"hound"}); err == nil {
		t.Fatal("missing controller was accepted")
	}
}
