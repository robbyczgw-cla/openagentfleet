package httpapi

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

func (s *Server) selectRunSession(ctx context.Context, run domain.Run, requested string, fresh bool) (string, error) {
	if fresh {
		return "", nil
	}
	workdir, err := s.runWorkdir(ctx, run.ID)
	if err != nil {
		return "", err
	}
	selected := requested
	if selected == "" {
		if existing, err := s.Store.GetHarnessSession(ctx, run.ConversationID, run.Provider); err == nil {
			selected = existing.NativeSessionID
		}
	}
	if selected == "" {
		return "", nil
	}
	matches, err := s.Store.HarnessSessionMatchesRun(ctx, run.ID, run.Provider, selected, workdir)
	if err != nil {
		return "", err
	}
	if !matches {
		if requested != "" {
			return "", errors.New("selected session belongs to a different conversation, project revision, or working directory")
		}
		return "", nil
	}
	return selected, nil
}

type projectRunExecutor struct {
	server *Server
	run    domain.Run
	next   harnessRunExecutor
}

func (e projectRunExecutor) RunWithOptions(ctx context.Context, provider, prompt, workdir string, options harness.RunOptions) (string, error) {
	if _, err := e.server.Store.ProjectBriefPromptForRun(ctx, e.run.ID, e.run.BotID); err != nil {
		return "", err
	}
	return e.next.RunWithOptions(ctx, provider, prompt, workdir, options)
}

func (s *Server) failRunPreparation(run domain.Run, err error) {
	status := "failed"
	fields := map[string]string{"error": err.Error()}
	if store.IsProjectAccessBlocked(err) {
		status = "blocked"
		fields["reason"] = "project_access_revoked"
	}
	payload, _ := json.Marshal(fields)
	_ = s.commitTerminalRunLifecycleEvent(run, status, err.Error(), "run."+status, string(payload))
}
