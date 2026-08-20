package orchestration

import (
	"fmt"
	"strings"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

type AgentTaskRequest struct {
	SourceBotID   string
	TargetBotID   string
	SourceRunID   string
	Collaboration *domain.AgentCollaboration
	Parent        *domain.Handoff
	Chain         []domain.Handoff
	ActiveCount   int
}

type AgentTask struct {
	SourceBotID     string
	TargetBotID     string
	SourceRunID     string
	OriginRunID     string
	ParentHandoffID string
	Depth           int
	TimeoutSeconds  int
}

func ValidateAgentTask(req AgentTaskRequest) (AgentTask, error) {
	sourceBotID := strings.TrimSpace(req.SourceBotID)
	targetBotID := strings.TrimSpace(req.TargetBotID)
	if sourceBotID == "" {
		return AgentTask{}, fmt.Errorf("agent task source agent is required")
	}
	if targetBotID == "" {
		return AgentTask{}, fmt.Errorf("agent task target agent is required")
	}
	if sourceBotID == targetBotID {
		return AgentTask{}, fmt.Errorf("agent task target must be a different agent")
	}
	if req.Collaboration == nil || !req.Collaboration.Enabled {
		return AgentTask{}, fmt.Errorf("agent collaboration is not enabled")
	}
	collaboration, err := domain.NormalizeAgentCollaboration(*req.Collaboration)
	if err != nil {
		return AgentTask{}, err
	}
	if !allowsCollaborationTarget(collaboration.AllowAgentIDs, targetBotID) {
		return AgentTask{}, fmt.Errorf("agent task target is not allowed")
	}

	chain := collaborationParentChain(req.Parent, req.Chain)
	if err := rejectCollaborationCycle(sourceBotID, targetBotID, chain); err != nil {
		return AgentTask{}, err
	}

	depth := 1
	parentHandoffID := ""
	originRunID := strings.TrimSpace(req.SourceRunID)
	if len(chain) > 0 {
		parent := chain[len(chain)-1]
		parentHandoffID = parent.ID
		depth = parent.Depth + 1
		if parent.OriginRunID != "" {
			originRunID = parent.OriginRunID
		} else if chain[0].OriginRunID != "" {
			originRunID = chain[0].OriginRunID
		}
	}
	maxDepth := int(collaboration.EffectiveMaxDepth())
	if depth > maxDepth {
		return AgentTask{}, fmt.Errorf("agent task depth exceeds maximum")
	}

	maxActive := int(collaboration.EffectiveMaxActivePeerTasks())
	if req.ActiveCount >= maxActive {
		return AgentTask{}, fmt.Errorf("too many active peer tasks")
	}

	timeoutSeconds := int(collaboration.EffectiveTimeoutSeconds())
	return AgentTask{
		SourceBotID:     sourceBotID,
		TargetBotID:     targetBotID,
		SourceRunID:     strings.TrimSpace(req.SourceRunID),
		OriginRunID:     originRunID,
		ParentHandoffID: parentHandoffID,
		Depth:           depth,
		TimeoutSeconds:  timeoutSeconds,
	}, nil
}

func allowsCollaborationTarget(allowAgentIDs []string, targetBotID string) bool {
	if len(allowAgentIDs) == 0 {
		return true
	}
	for _, id := range allowAgentIDs {
		if strings.TrimSpace(id) == targetBotID {
			return true
		}
	}
	return false
}

func collaborationParentChain(parent *domain.Handoff, chain []domain.Handoff) []domain.Handoff {
	if len(chain) > 0 {
		ordered := make([]domain.Handoff, len(chain))
		copy(ordered, chain)
		if parent != nil && (len(ordered) == 0 || ordered[len(ordered)-1].ID != parent.ID) {
			ordered = append(ordered, *parent)
		}
		return ordered
	}
	if parent == nil {
		return nil
	}
	return []domain.Handoff{*parent}
}

func rejectCollaborationCycle(sourceBotID, targetBotID string, chain []domain.Handoff) error {
	sources := make(map[string]struct{}, len(chain)+1)
	var prevSource, prevTarget string
	for _, item := range chain {
		hopSource := strings.TrimSpace(item.SourceBotID)
		hopTarget := strings.TrimSpace(item.TargetBotID)
		if hopSource != "" {
			sources[hopSource] = struct{}{}
		}
		if hopSource == targetBotID && hopTarget == sourceBotID {
			return fmt.Errorf("agent task would ping-pong")
		}
		if prevSource != "" && prevTarget == hopSource && prevSource == hopTarget {
			return fmt.Errorf("agent task would ping-pong")
		}
		prevSource, prevTarget = hopSource, hopTarget
	}
	if prevSource == targetBotID && prevTarget == sourceBotID {
		return fmt.Errorf("agent task would ping-pong")
	}
	if _, exists := sources[targetBotID]; exists {
		return fmt.Errorf("agent task would cycle")
	}
	return nil
}
