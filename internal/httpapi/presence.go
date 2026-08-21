package httpapi

import (
	"context"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

type agentPresenceSnapshot struct {
	runs     []domain.Run
	handoffs []domain.Handoff
	computer domain.ComputerPresence
}

func (s *Server) loadAgentPresenceSnapshot(ctx context.Context) agentPresenceSnapshot {
	snapshot := agentPresenceSnapshot{}
	if s.Store == nil {
		return snapshot
	}
	if runs, err := s.Store.ListLatestRunsByBot(ctx); err == nil {
		snapshot.runs = redactPresenceRuns(runs)
	}
	if handoffs, err := s.Store.ListActiveHandoffs(ctx); err == nil {
		snapshot.handoffs = handoffs
	}
	if s.Docker != nil {
		status := s.computerStatus(ctx)
		snapshot.computer = domain.ComputerPresence{
			Running:      status.Running,
			AgentControl: status.AgentControl,
			Takeover:     status.Takeover,
		}
	}
	return snapshot
}

func redactPresenceRuns(runs []domain.Run) []domain.Run {
	out := make([]domain.Run, len(runs))
	for index, run := range runs {
		run.Prompt = ""
		out[index] = run
	}
	return out
}

func (s *Server) decorateAgentPresence(ctx context.Context, agents []domain.Agent) []domain.Agent {
	return applyAgentPresence(agents, s.loadAgentPresenceSnapshot(ctx))
}

func (s *Server) decorateOneAgent(ctx context.Context, agent domain.Agent) domain.Agent {
	decorated := s.decorateAgentPresence(ctx, []domain.Agent{agent})
	if len(decorated) == 0 {
		return agent
	}
	return decorated[0]
}

func applyAgentPresence(agents []domain.Agent, snapshot agentPresenceSnapshot) []domain.Agent {
	if len(agents) == 0 {
		return agents
	}
	runByBot := make(map[string]domain.Run, len(snapshot.runs))
	for _, run := range snapshot.runs {
		runByBot[run.BotID] = run
	}
	for index := range agents {
		botID := agents[index].Bot.ID
		input := domain.PresenceInput{BotID: botID, Computer: snapshot.computer, Handoffs: snapshot.handoffs}
		if run, ok := runByBot[botID]; ok {
			copy := run
			input.Run = &copy
		}
		presence := domain.DeriveAgentPresence(input)
		agents[index].Presence = &presence
	}
	return agents
}
