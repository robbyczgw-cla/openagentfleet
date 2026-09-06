package computer

import (
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/compute"
)

func TestRegistryKeysBackendsByID(t *testing.T) {
	reg := NewRegistry()
	native := NewNativeBackend("native-1", true)
	docker := NewDockerBackend("docker-1", compute.NewDocker(t.TempDir(), "", false))
	if err := reg.Register(native); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(docker); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(nil); err == nil {
		t.Fatal("expected nil backend error")
	}
	if err := reg.Register(NewNativeBackend("", true)); err == nil {
		t.Fatal("expected empty id error")
	}

	got, ok := reg.Get("native-1")
	if !ok || got.Kind() != KindNative {
		t.Fatalf("get native: ok=%v backend=%v", ok, got)
	}
	got, ok = reg.Get("docker-1")
	if !ok || got.Kind() != KindDocker {
		t.Fatalf("get docker: ok=%v backend=%v", ok, got)
	}
	if _, ok := reg.Get("missing"); ok {
		t.Fatal("missing id should not be found")
	}
	if len(reg.List()) != 2 {
		t.Fatalf("list = %d, want 2", len(reg.List()))
	}
}

func TestNilRegistryGet(t *testing.T) {
	var reg *Registry
	if _, ok := reg.Get("native-1"); ok {
		t.Fatal("nil registry should miss")
	}
	if reg.List() != nil {
		t.Fatal("nil registry list should be nil")
	}
}
