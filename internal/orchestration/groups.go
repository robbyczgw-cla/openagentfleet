package orchestration

import (
	"fmt"
	"strings"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

type GroupMentionTarget struct {
	BotID        string
	SystemPrompt string
	Prompt       string
}

type GroupMemberProfile struct {
	Bot domain.Bot
}

// UniqueMentionBotIDs allows multiple distinct Agents in a group. Canonical
// 1:1 chat remains limited to SingleMentionBotID.
func UniqueMentionBotIDs(mentionBotIDs []string) []string {
	return domain.UniqueMentionBotIDs(mentionBotIDs)
}

// RouteUserGroupMentions starts at most one target per mentioned member.
// Unmentioned Agents are never scheduled.
func RouteUserGroupMentions(members []GroupMemberProfile, mentionBotIDs []string, recent []domain.GroupMessage, userContent string) ([]GroupMentionTarget, error) {
	mentions := UniqueMentionBotIDs(mentionBotIDs)
	if len(mentions) == 0 {
		return nil, nil
	}
	byID := make(map[string]GroupMemberProfile, len(members))
	memberViews := make([]domain.GroupMember, 0, len(members))
	for _, member := range members {
		id := strings.TrimSpace(member.Bot.ID)
		if id == "" {
			continue
		}
		byID[id] = member
		memberViews = append(memberViews, domain.GroupMember{BotID: member.Bot.ID, Name: member.Bot.Name, Title: member.Bot.Title})
	}
	contextText := domain.FormatGroupContext(recent, memberViews)
	targets := make([]GroupMentionTarget, 0, len(mentions))
	seen := make(map[string]struct{}, len(mentions))
	for _, botID := range mentions {
		if _, dup := seen[botID]; dup {
			continue
		}
		member, ok := byID[botID]
		if !ok {
			return nil, fmt.Errorf("mentioned agent %s is not in the group", botID)
		}
		seen[botID] = struct{}{}
		targets = append(targets, GroupMentionTarget{
			BotID:        botID,
			SystemPrompt: domain.GroupSpeakerSystemPrompt(member.Bot),
			Prompt:       groupMentionPrompt(member.Bot, contextText, userContent),
		})
	}
	return targets, nil
}

func groupMentionPrompt(bot domain.Bot, contextText, userContent string) string {
	var b strings.Builder
	b.WriteString("Group conversation (bounded, not this Agent's canonical chat; do not ingest as memory):\n")
	if strings.TrimSpace(contextText) != "" {
		b.WriteString(contextText)
		b.WriteString("\n")
	}
	b.WriteString("\nYou were mentioned. Reply only as ")
	b.WriteString(bot.Name)
	b.WriteString(" to:\n")
	b.WriteString(userContent)
	return b.String()
}

// RouteGroupAgentMention reuses ValidateAgentTask. It does not write handoffs.
// Callers that persist a peer task must use store.CreateAgentHandoff rather
// than a second A2A path. Group UI should still prefer in-thread replies so
// canonical chats stay empty.
func RouteGroupAgentMention(req AgentTaskRequest) (AgentTask, error) {
	return ValidateAgentTask(req)
}
