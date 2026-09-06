package httpapi

import (
	"os"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
)

func TestRunEngineUsesOnlyAssignedWorkdir(t *testing.T) {
	instance, conversation, server := openTasksAPI(t, "")
	run := createAPITask(t, instance, conversation, "isolated", "work")
	workdir := t.TempDir()
	if err := instance.SetRunWorkdir(t.Context(), run.ID, workdir); err != nil {
		t.Fatal(err)
	}
	executor := &recordingHarnessExecutor{}
	server.runExecutorOverride = executor
	if _, err := server.runEngineTurn(t.Context(), run, harness.RunOptions{}); err != nil {
		t.Fatal(err)
	}
	calls := executor.snapshot()
	if len(calls) != 1 || calls[0].Workdir != workdir {
		t.Fatalf("unexpected executor calls: %#v", calls)
	}
	if err := os.Remove(workdir); err != nil {
		t.Fatal(err)
	}
	if _, err := server.runEngineTurn(t.Context(), run, harness.RunOptions{}); err == nil {
		t.Fatal("missing assigned directory fell back to the default workspace")
	}
	if len(executor.snapshot()) != 1 {
		t.Fatal("executor ran after assigned directory disappeared")
	}
}
