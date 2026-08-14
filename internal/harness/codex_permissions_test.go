package harness

import "testing"

func TestCodexPermissionSettingsRemainBoundedByHostGate(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		hostWrites bool
		approval   string
		writes     bool
		wantError  bool
	}{
		{name: "ask read-only host", mode: "ask", approval: "untrusted"},
		{name: "ask writable host", mode: "ask", hostWrites: true, approval: "untrusted", writes: true},
		{name: "read only", mode: "read_only", hostWrites: true, approval: "never"},
		{name: "workspace denied by host", mode: "workspace", wantError: true},
		{name: "workspace with host gate", mode: "workspace", hostWrites: true, approval: "on-request", writes: true},
		{name: "legacy auto rejected", mode: "auto", hostWrites: true, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			approval, writes, err := codexPermissionSettings(test.mode, test.hostWrites)
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, wantError %v", err, test.wantError)
			}
			if err == nil && (approval != test.approval || writes != test.writes) {
				t.Fatalf("settings = (%q, %v), want (%q, %v)", approval, writes, test.approval, test.writes)
			}
		})
	}
}
