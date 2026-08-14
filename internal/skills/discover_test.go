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
