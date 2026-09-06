//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/coordinator"
)

func TestCaptureTaskArtifactsDoesNotBlockQueueOnFIFO(t *testing.T) {
	instance, conversation, server := openTasksAPI(t, "")
	run := createAPITask(t, instance, conversation, "FIFO regression", "capture the linked output")
	server.Turns = coordinator.NewTurnQueue()
	server.prepareTaskDeliverables(run.ID)
	runOutput := filepath.Join(server.HarnessWorkdir, "outputs", run.ID)
	if err := syscall.Mkfifo(filepath.Join(runOutput, "result.pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(server.HarnessWorkdir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	opened := make(chan error, 1)
	go func() {
		file, err := openTaskArtifactFile(root, filepath.ToSlash(filepath.Join("outputs", run.ID, "result.pipe")))
		if err == nil {
			err = file.Close()
		}
		opened <- err
	}()
	select {
	case err := <-opened:
		if err != nil {
			t.Fatalf("nonblocking FIFO open: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("artifact descriptor open blocked on FIFO")
	}

	firstDone := make(chan struct{})
	server.launchAgentTurn(run.BotID, run.ID, func(context.Context) {
		defer close(firstDone)
		server.captureTaskArtifacts(run, "[result](outputs/"+run.ID+"/result.pipe)")
	})
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("FIFO artifact blocked capture")
	}

	secondDone := make(chan struct{})
	server.launchAgentTurn(run.BotID, "queue-followup", func(context.Context) { close(secondDone) })
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("FIFO artifact left the agent queue occupied")
	}

	deadline := time.Now().Add(time.Second)
	for {
		server.activeMu.Lock()
		active := len(server.activeRuns)
		server.activeMu.Unlock()
		if active == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("FIFO artifact left %d active runs", active)
		}
		time.Sleep(time.Millisecond)
	}
}
