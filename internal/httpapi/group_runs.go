package httpapi

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
)

// launchGroupRuns starts mention-triggered group turns concurrently. GroupRun
// is not a canonical runs row; this path must not call CreateRun or
// executeRunWithContext.
func (s *Server) launchGroupRuns(runs []domain.GroupRun) {
	if !s.AllowHarnessExecution || s.harnessRunExecutor() == nil || s.Store == nil {
		return
	}
	for _, run := range runs {
		groupRun := run
		s.launchRun(groupRun.ID, func(runContext context.Context) {
			s.executeGroupRun(runContext, groupRun)
		})
	}
}

func (s *Server) executeGroupRun(baseContext context.Context, groupRun domain.GroupRun) {
	storeCtx := context.Background()
	fail := func(err error) {
		if err == nil {
			return
		}
		_, _ = s.Store.UpdateGroupRunStatus(storeCtx, groupRun.ID, domain.GroupRunStatusFailed, err.Error())
	}
	if _, err := s.Store.UpdateGroupRunStatus(storeCtx, groupRun.ID, domain.GroupRunStatusRunning, ""); err != nil {
		return
	}
	bot, err := s.Store.GetBot(storeCtx, groupRun.BotID)
	if err != nil {
		fail(err)
		return
	}
	agent, hasAgent, err := s.agentForBot(storeCtx, groupRun.BotID)
	if err != nil {
		fail(err)
		return
	}
	systemPrompt := domain.GroupSpeakerSystemPrompt(bot)
	if hasAgent {
		systemPrompt = appendSystemPrompt(systemPrompt, agentSystemPrompt(agent.Bot))
	}
	provider, model, reasoningEffort, serviceTier, permissionMode, webSearch, timeoutSeconds, _, err := s.leadRunSettings(storeCtx, agent, hasAgent)
	if err != nil {
		fail(err)
		return
	}
	timeout := s.RunTimeout
	if timeoutSeconds != 0 {
		timeout = time.Duration(timeoutSeconds) * time.Second
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	runContext, timeoutCancel := context.WithTimeout(baseContext, timeout)
	defer timeoutCancel()

	leadOptions := harness.RunOptions{
		SystemPrompt:    systemPrompt,
		Model:           model,
		ReasoningEffort: reasoningEffort,
		ServiceTier:     serviceTier,
		PermissionMode:  permissionMode,
		WebSearch:       webSearch,
	}
	applyPiLeadRole(provider, &leadOptions)
	output, err := s.harnessRunExecutor().RunWithOptions(runContext, provider, groupRun.Prompt, s.HarnessWorkdir, leadOptions)
	if err != nil {
		fail(err)
		return
	}
	answer := strings.TrimSpace(harness.AssistantText(provider, output))
	if answer == "" {
		answer = strings.TrimSpace(output)
	}
	if answer == "" {
		fail(errors.New("group run produced no assistant text"))
		return
	}
	if _, err := s.Store.CreateGroupAgentReply(storeCtx, groupRun.GroupID, groupRun.BotID, answer, nil); err != nil {
		fail(err)
		return
	}
	_, _ = s.Store.UpdateGroupRunStatus(storeCtx, groupRun.ID, domain.GroupRunStatusCompleted, "")
}
