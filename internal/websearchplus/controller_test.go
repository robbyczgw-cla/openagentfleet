package websearchplus

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestControllerDefaultsPersistAndReloadIndependentSettings(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	stateDir := filepath.Join(t.TempDir(), "web-search")
	controller, err := NewController(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	initial := controller.Status(t.Context())
	if initial.WebSearchPlusEnabled || initial.HoundEnabled || initial.WebSearchPlus.Enabled || initial.Hound.Enabled {
		t.Fatalf("default status = %#v", initial)
	}
	if initial.WebSearchPlusCredentialStatus != "external/not inspected" {
		t.Fatalf("credential status = %q", initial.WebSearchPlusCredentialStatus)
	}
	if initial.Bridge.Selected || initial.Bridge.Managed || initial.Bridge.OwnedChild {
		t.Fatalf("default bridge status = %#v", initial.Bridge)
	}
	if initial.ConfigPath != filepath.Join(stateDir, configFilename) {
		t.Fatalf("config path = %q", initial.ConfigPath)
	}
	if _, err := os.Stat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only defaults created state: %v", err)
	}

	enabled := true
	status, err := controller.Patch(t.Context(), ConnectorPatch{WebSearchPlusEnabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	if !status.WebSearchPlusEnabled || status.HoundEnabled || !status.WebSearchPlus.Enabled || status.Hound.Enabled {
		t.Fatalf("WSP patch status = %#v", status)
	}
	statePath := filepath.Join(stateDir, connectorStateFilename)
	info, err := os.Lstat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %v", info.Mode())
	}
	payload, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(payload)), "credential") || strings.Contains(string(payload), "unavailable") {
		t.Fatalf("credential metadata was persisted: %s", payload)
	}

	reopened, err := NewController(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := reopened.Status(t.Context())
	if !reloaded.WebSearchPlusEnabled || reloaded.HoundEnabled || !reloaded.WebSearchPlus.Enabled || reloaded.Hound.Enabled {
		t.Fatalf("reloaded status = %#v", reloaded)
	}
	status, err = reopened.Patch(t.Context(), ConnectorPatch{HoundEnabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	if !status.WebSearchPlusEnabled || !status.HoundEnabled || !status.WebSearchPlus.Enabled || !status.Hound.Enabled {
		t.Fatalf("independent Hound patch status = %#v", status)
	}
}

func TestControllerMCPServerSpecsReflectCommittedEnabledSnapshot(t *testing.T) {
	binDir := t.TempDir()
	uvx := filepath.Join(binDir, "uvx")
	if err := os.WriteFile(uvx, []byte("#!/bin/sh\nprintf 'uvx 0.9.18\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	controller, err := NewController(filepath.Join(t.TempDir(), "web-search"))
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := controller.Patch(t.Context(), ConnectorPatch{HoundEnabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	specs, err := controller.MCPServerSpecs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Name != "hound" || specs[0].Command != uvx ||
		!reflect.DeepEqual(specs[0].Args, []string{"--from", "hound-mcp==13.1.2", "hound"}) {
		t.Fatalf("specs = %#v", specs)
	}
	specs[0].Args[0] = "mutated"
	again, err := controller.MCPServerSpecs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Args[0] != "--from" {
		t.Fatalf("controller leaked mutable spec: %#v", again[0])
	}
}

func TestControllerDisableWaitsForInFlightSpecSnapshot(t *testing.T) {
	binDir := t.TempDir()
	started := filepath.Join(binDir, "started")
	release := filepath.Join(binDir, "release")
	uvx := filepath.Join(binDir, "uvx")
	script := "#!/bin/sh\n: > " + started + "\nwhile [ ! -f " + release + " ]; do sleep 0.01; done\nprintf 'uvx 0.9.18\\n'\n"
	if err := os.WriteFile(uvx, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	controller, err := NewController(filepath.Join(t.TempDir(), "web-search"))
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := controller.Patch(t.Context(), ConnectorPatch{HoundEnabled: &enabled}); err != nil {
		t.Fatal(err)
	}

	specDone := make(chan error, 1)
	go func() {
		_, err := controller.MCPServerSpecs(context.Background())
		specDone <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("uvx probe did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}

	disabled := false
	patchDone := make(chan error, 1)
	go func() {
		_, err := controller.Patch(context.Background(), ConnectorPatch{HoundEnabled: &disabled})
		patchDone <- err
	}()
	select {
	case err := <-patchDone:
		t.Fatalf("disable returned before the in-flight grant snapshot completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := os.WriteFile(release, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-specDone; err != nil {
		t.Fatal(err)
	}
	if err := <-patchDone; err != nil {
		t.Fatal(err)
	}
	specs, err := controller.MCPServerSpecs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 0 {
		t.Fatalf("disabled controller returned stale specs: %#v", specs)
	}
}

func TestControllerRejectsSymlinkStateDirectoryAndFile(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	realDirectory := t.TempDir()
	linkedDirectory := filepath.Join(t.TempDir(), "web-search")
	if err := os.Symlink(realDirectory, linkedDirectory); err != nil {
		t.Fatal(err)
	}
	if _, err := NewController(linkedDirectory); err == nil {
		t.Fatal("symlink state directory was accepted")
	}

	stateDir := filepath.Join(t.TempDir(), "web-search")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	controller, err := NewController(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim.json")
	if err := os.WriteFile(victim, []byte("do not replace\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(stateDir, connectorStateFilename)); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := controller.Patch(t.Context(), ConnectorPatch{HoundEnabled: &enabled}); err == nil {
		t.Fatal("symlink state file was accepted")
	}
	payload, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "do not replace\n" {
		t.Fatalf("symlink target changed: %q", payload)
	}
	if _, err := NewController(stateDir); err == nil {
		t.Fatal("persisted symlink state file was accepted on reload")
	}
}

func TestControllerConcurrentPatchAndStatusRemainAtomic(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	stateDir := filepath.Join(t.TempDir(), "web-search")
	controller, err := NewController(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	initial := false
	if _, err := controller.Patch(t.Context(), ConnectorPatch{WebSearchPlusEnabled: &initial, HoundEnabled: &initial}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		worker := worker
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 20; iteration++ {
				value := (worker+iteration)%2 == 0
				patch := ConnectorPatch{WebSearchPlusEnabled: &value}
				if worker%2 == 1 {
					patch = ConnectorPatch{HoundEnabled: &value}
				}
				if _, err := controller.Patch(ctx, patch); err != nil {
					t.Errorf("Patch: %v", err)
					return
				}
			}
		}()
	}
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 40; iteration++ {
				status := controller.Status(ctx)
				if status.WebSearchPlusEnabled != status.WebSearchPlus.Enabled || status.HoundEnabled != status.Hound.Enabled {
					t.Errorf("incoherent status = %#v", status)
					return
				}
				payload, err := os.ReadFile(filepath.Join(stateDir, connectorStateFilename))
				if err != nil {
					t.Errorf("ReadFile: %v", err)
					return
				}
				var stored ConnectorSettings
				if err := json.Unmarshal(payload, &stored); err != nil {
					t.Errorf("observed partial state: %v, payload = %q", err, payload)
					return
				}
			}
		}()
	}
	wait.Wait()

	current := controller.Status(t.Context())
	reopened, err := NewController(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := reopened.Status(t.Context())
	if current.WebSearchPlusEnabled != reloaded.WebSearchPlusEnabled || current.HoundEnabled != reloaded.HoundEnabled {
		t.Fatalf("memory = %#v, disk = %#v", current, reloaded)
	}
}

func TestControllerStatusOnlyRunsVersionProbes(t *testing.T) {
	emptyPath := t.TempDir()
	t.Setenv("PATH", emptyPath)
	stateDir := filepath.Join(t.TempDir(), "web-search")
	controller, err := NewController(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := controller.Patch(t.Context(), ConnectorPatch{WebSearchPlusEnabled: &enabled, HoundEnabled: &enabled}); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	probeLog := filepath.Join(t.TempDir(), "probes.log")
	uvx := filepath.Join(binDir, "uvx")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + probeLog + "\"\nprintf 'uvx 1.2.3\\n'\n"
	if err := os.WriteFile(uvx, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	hound := filepath.Join(binDir, "hound")
	if err := os.WriteFile(hound, []byte("#!/bin/sh\nprintf 'must not execute\\n' >> \""+probeLog+"\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	status := controller.Status(t.Context())
	if !status.UVX.Available || status.UVX.Version != "1.2.3" {
		t.Fatalf("uvx status = %#v", status.UVX)
	}
	if status.Bridge.Selected || status.Bridge.Managed || status.Bridge.OwnedChild {
		t.Fatalf("bridge side effect status = %#v", status.Bridge)
	}
	payload, err := os.ReadFile(probeLog)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "--version\n" {
		t.Fatalf("executed commands = %q", payload)
	}
	if _, err := os.Stat(filepath.Join(stateDir, configFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status prepared WSP config: %v", err)
	}
}
