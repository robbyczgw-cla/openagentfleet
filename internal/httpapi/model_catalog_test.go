package httpapi

import (
	"strings"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
)

func TestBuildModelCatalogKeepsProviderAndAuthStateTogether(t *testing.T) {
	capabilities := []domain.Capability{
		{Name: "grok", Available: true},
		{Name: harness.CodexAppServerProvider, Available: true},
		{Name: "opencode", Available: true},
		{Name: "pi", Available: true},
	}
	auth := []harness.AuthStatus{
		{Provider: "grok", Available: true, Authenticated: true},
		{Provider: harness.CodexAppServerProvider, Available: true, LoginRequired: true},
	}
	catalog := buildModelCatalog(capabilities, auth)
	if len(catalog) != 12 {
		t.Fatalf("catalog length = %d, want 12", len(catalog))
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
	automatic := byModel["pi:"]
	if !automatic.Available || automatic.Provider != "pi" || automatic.Harness != "pi" || automatic.Label != "Pi automatic" || automatic.AuthMode != "local" || automatic.AuthState != "local" || automatic.AuthLabel != "Pi login (`pi /login`)" {
		t.Fatalf("Pi automatic catalog entry = %#v", automatic)
	}
	for _, model := range []string{"xai/grok-4.3", "anthropic/claude-sonnet-4.6", "openai/gpt-5.5", "deepseek/deepseek-v4-flash"} {
		entry := byModel["pi:"+model]
		if !entry.Available || entry.Provider != "pi" || entry.Harness != "pi" || entry.AuthMode != "local" || entry.AuthState != "local" {
			t.Fatalf("Pi catalog entry for %s = %#v", model, entry)
		}
		if !strings.Contains(entry.Detail, "Pi RPC") {
			t.Fatalf("Pi catalog entry %s must say it runs through Pi RPC: %#v", model, entry)
		}
	}
	if catalog[0].Harness == "pi" {
		t.Fatal("Pi must not be the default catalog pick")
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
