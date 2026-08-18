package compute

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotCatalogRoundTripMarksActiveCheckpoint(t *testing.T) {
	docker := NewDocker(filepath.Join(t.TempDir(), "workspace"), "", true)
	catalog := snapshotCatalog{
		ActiveID:    "abc123",
		ActiveImage: snapshotImagePrefix + "abc123",
		Snapshots: []ComputerSnapshot{
			{ID: "abc123", Name: "before login", Image: snapshotImagePrefix + "abc123"},
			{ID: "def456", Name: "later", Image: snapshotImagePrefix + "def456"},
		},
	}
	if err := docker.saveSnapshotCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	if got := docker.runImage(); got != catalog.ActiveImage {
		t.Fatalf("runImage = %q, want restored snapshot image", got)
	}
	items, err := docker.ListSnapshots()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || !items[0].Active || items[1].Active || items[0].Name != "before login" {
		t.Fatalf("list = %#v", items)
	}
}

func TestSnapshotNameAndRefValidation(t *testing.T) {
	if err := validateActionRef("e1"); err != nil {
		t.Fatal(err)
	}
	if err := validateActionRef("w9001"); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"E1", "window", "e-1", "e"} {
		if err := validateActionRef(ref); err == nil {
			t.Fatalf("accepted invalid ref %q", ref)
		}
	}
}

func TestVolumeCopyOverridesImageEntrypoint(t *testing.T) {
	args := volumeCopyArgs("openagentfleet-agent-computer:ubuntu-24.04", "from-vol", "to-vol", "true")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--entrypoint sh") {
		t.Fatalf("volume copy must override the Agent Computer entrypoint: %#v", args)
	}
	if strings.Contains(joined, "computer-server") {
		t.Fatalf("volume copy must not start the view server: %#v", args)
	}
}
