package main

import (
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/compute"
)

func TestResolveLoopbackTCPAddressRejectsNonPrivateMobileListeners(t *testing.T) {
	valid := []string{
		"127.0.0.1:4318",
		"[::1]:4318",
	}
	for _, value := range valid {
		resolved, err := resolveLoopbackTCPAddress(value)
		if err != nil {
			t.Fatalf("resolveLoopbackTCPAddress(%q): %v", value, err)
		}
		if resolved == "" {
			t.Fatalf("resolveLoopbackTCPAddress(%q) returned an empty address", value)
		}
	}

	invalid := []string{
		"",
		":4318",
		"127.0.0.1",
		"127.0.0.1:0",
		"127.0.0.1:99999",
		"0.0.0.0:4318",
		"192.168.1.10:4318",
	}
	for _, value := range invalid {
		if _, err := resolveLoopbackTCPAddress(value); err == nil {
			t.Fatalf("resolveLoopbackTCPAddress(%q) unexpectedly succeeded", value)
		}
	}
}

func TestRequireRemoteTokenForNonLoopbackAddr(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:4317", "127.0.0.1:0", "[::1]:4317", "localhost:4317"} {
		if err := requireRemoteTokenForNonLoopbackAddr(addr, ""); err != nil {
			t.Fatalf("loopback addr %q unexpectedly requires a token: %v", addr, err)
		}
	}
	for _, addr := range []string{"0.0.0.0:4317", "192.168.1.10:4317"} {
		if err := requireRemoteTokenForNonLoopbackAddr(addr, ""); err == nil {
			t.Fatalf("non-loopback addr %q was accepted without a token", addr)
		}
		if err := requireRemoteTokenForNonLoopbackAddr(addr, " \t "); err == nil {
			t.Fatalf("non-loopback addr %q was accepted with a blank token", addr)
		}
		if err := requireRemoteTokenForNonLoopbackAddr(addr, "test-remote-token"); err != nil {
			t.Fatalf("non-loopback addr %q was rejected with a token: %v", addr, err)
		}
	}
}

func TestDockerPublishesSeparateHostAndContainerPorts(t *testing.T) {
	docker := compute.NewDocker(t.TempDir(), "", true)
	docker.ViewPort = 19224
	if docker.ContainerPort != 9223 {
		t.Fatalf("container port = %d, want 9223", docker.ContainerPort)
	}
}
