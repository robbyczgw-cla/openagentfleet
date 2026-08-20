package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

func TestDiscoverReadsWorkspaceSkillMetadata(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, ".agents", "skills", "release-notes", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	content := `---
name: Release notes
description: Summarize merged changes for a release.
---

# Release notes

Use the repository history and approved artifacts.
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := Discover(workspace)
	if err != nil {
		t.Fatal(err)
	}
	var item *domain.Skill
	for index := range items {
		candidate := items[index]
		if candidate.Source == "workspace/.agents" {
			item = &items[index]
			break
		}
	}
	if item == nil || item.Name != "Release notes" || item.Description != "Summarize merged changes for a release." || !item.Eligible {
		t.Fatalf("skill metadata = %#v", items)
	}
}

func TestDiscoverEnabledReadsActivePointerOnly(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "release-notes")
	versionDir := filepath.Join(skillDir, "versions", "v000001")
	if err := os.MkdirAll(versionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := `---
name: Enabled notes
description: Copied from Skill Workshop.
---

# Enabled notes
`
	if err := os.WriteFile(filepath.Join(versionDir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "active.json"), []byte(`{"active":true,"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	inactive := filepath.Join(root, "inactive")
	if err := os.MkdirAll(filepath.Join(inactive, "versions", "v000001"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inactive, "versions", "v000001", "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inactive, "active.json"), []byte(`{"active":false,"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	items, err := DiscoverEnabled(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Source != "enabled" || items[0].Name != "Enabled notes" {
		t.Fatalf("enabled skills = %#v", items)
	}
}
