package store

import (
	"context"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

func (s *Store) GetGitHubWorkflowForConversation(ctx context.Context, conversationID string) (domain.GitHubWorkflow, error) {
	return scanGitHubWorkflow(s.db.QueryRowContext(ctx, githubWorkflowSelect+` WHERE conversation_id=?`, conversationID))
}
