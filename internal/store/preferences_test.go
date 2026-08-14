package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/preferences"
)

func TestLegacyPreferencesExposeAndPersistWorkspaceEngine(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "botd.sqlite")
	instance, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.ensurePreferencesTable(t.Context()); err != nil {
		t.Fatal(err)
	}
	legacy := `{"version":1,"onboarding":{"version":1,"completed":false},"appearance":{"theme":"system","density":"comfortable","font_scale":1},"usage":{"default_worker":"claude","reasoning_effort":"high","permission_mode":"default"},"computer":{"default_surface":"desktop","runtime":"colima","auto_takeover":false,"auto_agent_control":false},"safety":{"retain_transcripts":false,"retain_activity":false},"features":{}}`
	if _, err := instance.db.ExecContext(t.Context(), `INSERT INTO local_preferences (singleton, schema_version, document, updated_at) VALUES (?, ?, ?, ?)`, preferencesSingleton, preferences.CurrentVersion, legacy, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	loaded, err := instance.GetPreferences(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Workspace.Engine != preferences.ProviderGrok {
		t.Fatalf("migrated workspace engine = %q, want safe lead fallback %q", loaded.Workspace.Engine, preferences.ProviderGrok)
	}
	updated, err := instance.PatchPreferences(t.Context(), []byte(`{"workspace":{"engine":"codex_app_server"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Workspace.Engine != preferences.ProviderCodexAppServer || updated.Usage.DefaultWorker != preferences.ProviderCodexAppServer {
		t.Fatalf("updated preferences = %#v", updated)
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.GetPreferences(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Workspace.Engine != preferences.ProviderCodexAppServer || persisted.Usage.DefaultWorker != preferences.ProviderCodexAppServer {
		t.Fatalf("persisted preferences = %#v", persisted)
	}
}
