package compute

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultResourceConfigUsesUbuntuAndSmallSafetyBuffer(t *testing.T) {
	resource := DefaultResourceConfig()
	if resource.CPUs != 4 || resource.MemoryGiB != 4 || resource.DiskGiB != 25 || resource.SwapGiB != 1 {
		t.Fatalf("default resources = %#v", resource)
	}
	if resource.OSImage != "ubuntu-24.04" || resource.BaseImage() != "ubuntu:24.04" {
		t.Fatalf("default image = %#v / %q", resource, resource.BaseImage())
	}
	if got := resource.ImageTag(); got != "openagentfleet-agent-computer:ubuntu-24.04" {
		t.Fatalf("default image tag = %q", got)
	}
}

func TestResourceConfigMapsExplicitImagesWithoutCrossTagReuse(t *testing.T) {
	for _, test := range []struct {
		osImage string
		base    string
		tag     string
	}{
		{osImage: "ubuntu-24.04", base: "ubuntu:24.04", tag: "openagentfleet-agent-computer:ubuntu-24.04"},
		{osImage: "ubuntu-26.04", base: "ubuntu:26.04", tag: "openagentfleet-agent-computer:ubuntu-26.04"},
		{osImage: "debian-13", base: "debian:13", tag: "openagentfleet-agent-computer:debian-13"},
	} {
		resource := DefaultResourceConfig()
		resource.OSImage = test.osImage
		if resource.BaseImage() != test.base || resource.ImageTag() != test.tag {
			t.Fatalf("%s mapped to base=%q tag=%q", test.osImage, resource.BaseImage(), resource.ImageTag())
		}
	}
	if err := (ResourceConfig{OSImage: "debian-12", CPUs: 2, MemoryGiB: 2, DiskGiB: 25}).Validate(); err == nil {
		t.Fatal("unsupported Debian 12 image was accepted")
	}
}

func TestUpdateColimaResourcesIsGrowOnlyForDisk(t *testing.T) {
	contents := "cpu: 2\ndisk: 100\nmemory: 2\n"
	updated, changed, currentDisk, err := updateColimaResources(contents, ResourceConfig{CPUs: 4, MemoryGiB: 4, DiskGiB: 25, SwapGiB: 1, OSImage: "ubuntu-24.04"})
	if err != nil {
		t.Fatal(err)
	}
	if !changed || currentDisk != 100 {
		t.Fatalf("changed=%t currentDisk=%d", changed, currentDisk)
	}
	if !strings.Contains(updated, "cpu: 4") || !strings.Contains(updated, "memory: 4") || !strings.Contains(updated, "disk: 100") || strings.Contains(updated, "disk: 25") {
		t.Fatalf("updated Colima config = %q", updated)
	}

	updated, changed, currentDisk, err = updateColimaResources("cpu: 2\ndisk: 20\nmemory: 2\n", ResourceConfig{CPUs: 4, MemoryGiB: 4, DiskGiB: 25, SwapGiB: 1, OSImage: "ubuntu-24.04"})
	if err != nil {
		t.Fatal(err)
	}
	if !changed || currentDisk != 20 || !strings.Contains(updated, "disk: 25") {
		t.Fatalf("disk growth result changed=%t currentDisk=%d config=%q", changed, currentDisk, updated)
	}
}

func TestHostStoragePreflightFailsBeforeProvisioning(t *testing.T) {
	t.Setenv("COLIMA_HOME", filepath.Join(t.TempDir(), "colima"))
	workspace := filepath.Join(t.TempDir(), "workspace")
	old := diskFreeBytes
	diskFreeBytes = func(path string) (uint64, string, error) {
		return 3 * 1024 * 1024 * 1024, path, nil
	}
	t.Cleanup(func() { diskFreeBytes = old })

	docker := NewDocker(workspace, "", true)
	docker.RuntimeID = RuntimeDocker
	err := docker.checkHostStorage(ResourceConfig{CPUs: 4, MemoryGiB: 4, DiskGiB: 25, SwapGiB: 1, OSImage: "ubuntu-24.04"})
	if err == nil {
		t.Fatal("low host storage was accepted")
	}
	var storageErr *HostStorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("error = %T %v, want HostStorageError", err, err)
	}
	if storageErr.NeedBytes <= storageErr.FreeBytes || !strings.Contains(err.Error(), "not enough free host storage") {
		t.Fatalf("storage error = %#v / %v", storageErr, err)
	}
}

func TestHostStoragePreflightAcceptsSeparateWorkspaceVolume(t *testing.T) {
	t.Setenv("COLIMA_HOME", filepath.Join(t.TempDir(), "colima"))
	workspace := filepath.Join(t.TempDir(), "workspace")
	old := diskFreeBytes
	diskFreeBytes = func(path string) (uint64, string, error) {
		if strings.Contains(path, "workspace") {
			return 3 * 1024 * 1024 * 1024, path, nil
		}
		return 40 * 1024 * 1024 * 1024, path, nil
	}
	t.Cleanup(func() { diskFreeBytes = old })

	docker := NewDocker(workspace, "", true)
	docker.RuntimeID = RuntimeColima
	if err := docker.checkHostStorage(DefaultResourceConfig()); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxDockerEngineStorageIgnoresColimaHome(t *testing.T) {
	colimaHome := filepath.Join(t.TempDir(), "colima")
	t.Setenv("COLIMA_HOME", colimaHome)
	workspace := filepath.Join(t.TempDir(), "workspace")
	old := diskFreeBytes
	diskFreeBytes = func(path string) (uint64, string, error) {
		if strings.Contains(path, "colima") {
			return 1 * 1024 * 1024 * 1024, path, nil
		}
		return 20 * 1024 * 1024 * 1024, path, nil
	}
	t.Cleanup(func() { diskFreeBytes = old })

	docker := NewDocker(workspace, "", true)
	docker.RuntimeID = RuntimeDocker
	if err := docker.checkHostStorage(DefaultResourceConfig()); err != nil {
		t.Fatal(err)
	}
}
