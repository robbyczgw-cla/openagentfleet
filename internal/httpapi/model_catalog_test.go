package httpapi

import (
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
)

func TestBuildModelCatalogKeepsProviderAndAuthStateTogether(t *testing.T) {
	capabilities := []domain.Capability{
		{Name: "grok", Available: true},
		{Name: harness.CodexAppServerProvider, Available: true},
		{Name: "opencode", Available: true},
	}
	auth := []harness.AuthStatus{
		{Provider: "grok", Available: true, Authenticated: true},
		{Provider: harness.CodexAppServerProvider, Available: true, LoginRequired: true},
	}
	catalog := buildModelCatalog(capabilities, auth)
	if len(catalog) != 7 {
		t.Fatalf("catalog length = %d, want 7", len(catalog))
	}
	byModel := make(map[string]domain.ModelCatalogEntry, len(catalog))
	for _, entry := range catalog {
		byModel[entry.Harness+":"+entry.Model] = entry
	}
	if entry := byModel["grok_build:grok-4.6"]; !entry.Available || entry.AuthState != "connected" || entry.AuthMode != "oauth" {
		t.Fatalf("Grok catalog entry = %#v", entry)
	}
	if entry := byModel["codex_app_server:"]; entry.Available || entry.AuthState != "sign_in" || entry.DisabledReason == "" {
		t.Fatalf("Codex catalog entry = %#v", entry)
	}
	if entry := byModel["opencode:opencode/deepseek-v4-flash-free"]; !entry.Available || entry.AuthState != "local" {
		t.Fatalf("OpenCode catalog entry = %#v", entry)
	}
	for _, model := range []string{"opencode-go/deepseek-v4-flash", "opencode-go/deepseek-v4-pro"} {
		entry := byModel["opencode:"+model]
		if !entry.Available || entry.Provider != "opencode-go" || entry.AuthState != "local" {
			t.Fatalf("OpenCode Go catalog entry for %s = %#v", model, entry)
		}
	}
}

func TestBuildModelCatalogFailsClosedWhenHarnessIsMissing(t *testing.T) {
	catalog := buildModelCatalog(nil, nil)
	for _, entry := range catalog {
		if entry.Available {
			t.Fatalf("unavailable harness was marked ready: %#v", entry)
		}
		if entry.DisabledReason == "" {
			t.Fatalf("unavailable entry has no user-facing reason: %#v", entry)
		}
	}
}
