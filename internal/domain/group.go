package domain

import (
	"fmt"
	"strings"
)

const (
	MinGroupAgents             = 2
	DefaultGroupContextLimit   = 24
	MaxGroupConversationTitle  = MaxAgentConversationTitleBytes
	MessageKindGroup           = "group"
	GroupRunStatusQueued       = "queued"
	GroupRunStatusRunning      = "running"
	GroupRunStatusCompleted    = "completed"
	GroupRunStatusFailed       = "failed"
	GroupAuthorUserDisplayName = "User"
)

// Group is a durable multi-agent conversation that is not any Agent's
// canonical chat. The user is always present; AgentIDs are the selected Bots.
type Group struct {
	ID        string        `json:"id"`
	Title     string        `json:"title"`
	CreatedAt string        `json:"created_at"`
	UpdatedAt string        `json:"updated_at"`
	AgentIDs  []string      `json:"agent_ids,omitempty"`
	Members   []GroupMember `json:"members,omitempty"`
}

type GroupMember struct {
	GroupID   string `json:"group_id"`
	BotID     string `json:"bot_id"`
	Name      string `json:"name,omitempty"`
	Title     string `json:"title,omitempty"`
	CreatedAt string `json:"created_at"`
}

// GroupMessage lives only in group_messages. It is never an Agent memory
// source and is not a row in the canonical messages table.
type GroupMessage struct {
	ID          string   `json:"id"`
	GroupID     string   `json:"group_id"`
	Role        string   `json:"role"`
	Content     string   `json:"content"`
	CreatedAt   string   `json:"created_at"`
	Kind        string   `json:"kind,omitempty"`
	AuthorBotID string   `json:"author_bot_id,omitempty"`
	Mentions    []string `json:"mentions,omitempty"`
}

// GroupRun is a durable mention-triggered turn for one group member. It is
// not a row in the canonical runs table (that table requires conversations.id).
type GroupRun struct {
	ID        string `json:"id"`
	GroupID   string `json:"group_id"`
	BotID     string `json:"bot_id"`
	MessageID string `json:"message_id"`
	Status    string `json:"status"`
	Prompt    string `json:"prompt"`
	Error     string `json:"error,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func NormalizeGroupTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "Group chat"
	}
	if len(title) > MaxGroupConversationTitle {
		return title[:MaxGroupConversationTitle]
	}
	return title
}

// NormalizeGroupAgentIDs requires at least two distinct Agents. A Manager is
// just another member; there is no superuser flag.
func NormalizeGroupAgentIDs(agentIDs []string) ([]string, error) {
	seen := make(map[string]struct{}, len(agentIDs))
	out := make([]string, 0, len(agentIDs))
	for _, raw := range agentIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) < MinGroupAgents {
		return nil, fmt.Errorf("a group requires at least %d agents", MinGroupAgents)
	}
	return out, nil
}

// UniqueMentionBotIDs keeps mention order and allows multiple distinct Agents.
// Canonical 1:1 chat still uses orchestration.SingleMentionBotID.
func UniqueMentionBotIDs(mentionBotIDs []string) []string {
	seen := make(map[string]struct{}, len(mentionBotIDs))
	out := make([]string, 0, len(mentionBotIDs))
	for _, raw := range mentionBotIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func GroupMemberSet(members []GroupMember) map[string]GroupMember {
	out := make(map[string]GroupMember, len(members))
	for _, member := range members {
		out[strings.TrimSpace(member.BotID)] = member
	}
	return out
}

func GroupAuthorDisplayName(message GroupMessage, members []GroupMember) string {
	if strings.TrimSpace(message.AuthorBotID) == "" {
		return GroupAuthorUserDisplayName
	}
	for _, member := range members {
		if member.BotID == message.AuthorBotID {
			if name := strings.TrimSpace(member.Name); name != "" {
				return name
			}
			return member.BotID
		}
	}
	return message.AuthorBotID
}

func BoundedGroupMessages(messages []GroupMessage, limit int) []GroupMessage {
	if limit <= 0 {
		limit = DefaultGroupContextLimit
	}
	if len(messages) <= limit {
		return messages
	}
	return messages[len(messages)-limit:]
}

func FormatGroupContext(messages []GroupMessage, members []GroupMember) string {
	bounded := BoundedGroupMessages(messages, DefaultGroupContextLimit)
	if len(bounded) == 0 {
		return ""
	}
	lines := make([]string, 0, len(bounded))
	for _, item := range bounded {
		author := GroupAuthorDisplayName(item, members)
		lines = append(lines, fmt.Sprintf("[%s]: %s", author, item.Content))
	}
	return strings.Join(lines, "\n")
}

// GroupSpeakerSystemPrompt names the speaking Agent and forbids impersonation.
func GroupSpeakerSystemPrompt(bot Bot) string {
	name := strings.TrimSpace(bot.Name)
	if name == "" {
		name = bot.ID
	}
	return fmt.Sprintf(
		"You are speaking in a multi-agent group as %s (agent id %s). Answer only as this Agent. Never impersonate the user or any other Agent. Do not claim another member's name, role, or tools. Group messages are not this Agent's private chat and must not be treated as durable memory.",
		name, bot.ID,
	)
}
