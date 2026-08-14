//go:build !windows

package compute

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunOutputCancelsTheEntireProcessGroup(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "spawn-child.sh")
	pidFile := filepath.Join(root, "child.pid")
	scriptBody := fmt.Sprintf("#!/bin/sh\nsleep 30 &\nprintf '%%s\\n' $! > %q\nwait\n", pidFile)
	if err := os.WriteFile(script, []byte(scriptBody), 0o700); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := runOutput(ctx, script); err == nil {
		t.Fatal("runOutput unexpectedly completed successfully")
	}
	contents, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(contents)))
	if err != nil {
		t.Fatalf("parse child pid: %v", err)
	}
	defer syscall.Kill(childPID, syscall.SIGKILL)

	deadline := time.Now().Add(2 * time.Second)
	for {
		err = syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("child process %d survived process-group cancellation: %v", childPID, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDockerStatusDoesNotOverlapHealthProbes(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "fake-docker.sh")
	countFile := filepath.Join(root, "count")
	scriptBody := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = info ]; then\n  printf 'probe\\n' >> %q\n  sleep 30\n  exit 1\nfi\n", countFile)
	if err := os.WriteFile(script, []byte(scriptBody), 0o700); err != nil {
		t.Fatal(err)
	}

	docker := &Docker{Binary: script, Image: "test-image"}
	results := make(chan Status, 2)
	go func() { results <- docker.Status(context.Background()) }()
	time.Sleep(50 * time.Millisecond)
	go func() { results <- docker.Status(context.Background()) }()
	first, second := <-results, <-results
	if first.Available || second.Available {
		t.Fatalf("fake Docker unexpectedly available: %#v %#v", first, second)
	}
	contents, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatalf("read probe count: %v", err)
	}
	if got := strings.Count(string(contents), "probe\n"); got != 1 {
		t.Fatalf("health probe count = %d, want 1", got)
	}
}
