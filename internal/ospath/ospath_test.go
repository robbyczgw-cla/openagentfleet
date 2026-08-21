package ospath

import (
	"os"
	"runtime"
	"testing"
)

func TestIsAbsAcceptsPOSIXGuestAndWindowsHostPaths(t *testing.T) {
	for _, path := range []string{"/workspace", "/workspace/project", "/tmp/openagentfleet-policy/workspace"} {
		if !IsAbs(path) {
			t.Fatalf("IsAbs(%q) = false", path)
		}
		if !IsCleanAbs(path) {
			t.Fatalf("IsCleanAbs(%q) = false", path)
		}
	}
	if IsAbs("") || IsAbs("workspace") || IsAbs("./workspace") {
		t.Fatal("relative paths must not be absolute")
	}
	if runtime.GOOS == "windows" {
		if !IsAbs(`C:\Users\dev\project`) || !IsCleanAbs(`C:\Users\dev\project`) {
			t.Fatal("windows host path must be absolute")
		}
	}
}

func TestOwnerOnlySkipsPOSIXModesOnWindows(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if POSIXModeEnforced() != (runtime.GOOS != "windows") {
		t.Fatalf("POSIXModeEnforced() = %v, GOOS=%s", POSIXModeEnforced(), runtime.GOOS)
	}
	if runtime.GOOS == "windows" && !OwnerOnlyDir(info) {
		t.Fatal("Windows NTFS directories must not fail closed on 0666")
	}
}

func TestPOSIXGuestPathsStaySlashSeparated(t *testing.T) {
	if CleanPOSIX("/tmp") != "/tmp" || CleanPOSIX("/workspace") != "/workspace" {
		t.Fatalf("CleanPOSIX mutated guest paths: %q %q", CleanPOSIX("/tmp"), CleanPOSIX("/workspace"))
	}
	if !WithinPOSIX("/tmp/cache", "/tmp") || WithinPOSIX("/tmp", "/tmp/cache") {
		t.Fatal("WithinPOSIX guest overlap is wrong")
	}
	if !IsFilesystemRoot("/") {
		t.Fatal("POSIX root must be a filesystem root")
	}
}
