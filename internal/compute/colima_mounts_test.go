package compute

import (
	"strings"
	"testing"
)

func TestAddColimaMountsPreservesExistingEntriesAndIsIdempotent(t *testing.T) {
	config := "runtime: docker\nmounts:\n  - location: /Users/test/projects\n    writable: true\n\ndisk: 32\n"
	required := []string{"/Users/test/projects", "/Users/test/openagentfleet-state"}
	updated, changed, err := addColimaMounts(config, required)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected the missing mount to be added")
	}
	if !strings.Contains(updated, "- location: /Users/test/projects\n    writable: true") || !strings.Contains(updated, "- location: /Users/test/openagentfleet-state\n    writable: true") {
		t.Fatalf("updated config lost or omitted mounts:\n%s", updated)
	}
	second, changed, err := addColimaMounts(updated, required)
	if err != nil {
		t.Fatal(err)
	}
	if changed || second != updated {
		t.Fatalf("mount update was not idempotent: changed=%t", changed)
	}
}

func TestAddColimaMountsExpandsEmptyMountList(t *testing.T) {
	config := "runtime: docker\nmounts: []\nvmType: vz\n"
	updated, changed, err := addColimaMounts(config, []string{"/Users/test/openagentfleet-state"})
	if err != nil {
		t.Fatal(err)
	}
	if !changed || strings.Contains(updated, "mounts: []") {
		t.Fatalf("empty mount list was not expanded:\n%s", updated)
	}
	if !strings.Contains(updated, "mounts:\n  - location: /Users/test/openagentfleet-state\n    writable: true") {
		t.Fatalf("expanded mount list is malformed:\n%s", updated)
	}
}

func TestAddColimaMountsFailsClosedForReadOnlyCoverage(t *testing.T) {
	config := "mounts:\n  - location: /Users/test\n    writable: false\n"
	if _, _, err := addColimaMounts(config, []string{"/Users/test/openagentfleet-state"}); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("read-only mount coverage was accepted: %v", err)
	}
}

func TestMountCoversOnlyDescendants(t *testing.T) {
	if !mountCovers("/Users/test", "/Users/test/openagentfleet-state") {
		t.Fatal("ancestor mount did not cover descendant")
	}
	if mountCovers("/Users/test", "/Users/test-other") {
		t.Fatal("prefix-only path was treated as a descendant")
	}
}
