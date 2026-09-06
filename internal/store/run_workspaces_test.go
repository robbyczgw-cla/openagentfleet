package store

import "testing"

func TestRunWorkdirCannotChangeAfterAssignmentOrExecution(t *testing.T) {
	s, conversation := openTaskStore(t)
	run := createTaskRun(t, s, conversation, "isolated task", "work here")
	first, second := t.TempDir(), t.TempDir()
	if err := s.SetRunWorkdir(t.Context(), run.ID, first); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunWorkdir(t.Context(), run.ID, first); err != nil {
		t.Fatalf("idempotent assignment: %v", err)
	}
	if err := s.SetRunWorkdir(t.Context(), run.ID, second); err == nil {
		t.Fatal("changed an assigned working directory")
	}
	if _, err := s.db.ExecContext(t.Context(), `UPDATE runs SET status = 'running' WHERE id = ?`, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunWorkdir(t.Context(), run.ID, first); err == nil {
		t.Fatal("accepted assignment after execution started")
	}
	got, err := s.GetRunWorkdir(t.Context(), run.ID)
	if err != nil || got != first {
		t.Fatalf("stored directory = %q, %v", got, err)
	}
}
