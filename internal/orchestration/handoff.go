package orchestration

import (
	"fmt"
	"strings"
)

// ValidateAgentHandoff is the side-effect-free preflight for a visible
// Agent-to-Agent mention. It is not ExecuteOneHop: workers stay hidden
// adapters and must not be treated as the target Agent.
func ValidateAgentHandoff(sourceBotID, targetBotID string) error {
	sourceBotID = strings.TrimSpace(sourceBotID)
	targetBotID = strings.TrimSpace(targetBotID)
	if sourceBotID == "" {
		return fmt.Errorf("handoff source agent is required")
	}
	if targetBotID == "" {
		return fmt.Errorf("handoff target agent is required")
	}
	if sourceBotID == targetBotID {
		return fmt.Errorf("handoff target must be a different agent")
	}
	return nil
}

// SingleMentionBotID accepts at most one mentioned Agent. Group chats and
// multi-agent @mentions are out of scope for this slice.
func SingleMentionBotID(mentionBotIDs []string) (string, error) {
	seen := make(map[string]struct{}, len(mentionBotIDs))
	var selected string
	for _, raw := range mentionBotIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		if selected != "" {
			return "", fmt.Errorf("only one agent mention is supported")
		}
		selected = id
	}
	return selected, nil
}
