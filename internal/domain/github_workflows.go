package domain

const (
	GitHubWorkflowPreparing     = "preparing"
	GitHubWorkflowRunning       = "running"
	GitHubWorkflowReady         = "ready"
	GitHubWorkflowReviewed      = "reviewed"
	GitHubWorkflowPushed        = "pushed"
	GitHubWorkflowDraftPR       = "draft_pr"
	GitHubWorkflowPublishFailed = "publish_failed"
)

type GitHubWorkflowReview struct {
	Generation     int      `json:"generation"`
	Token          string   `json:"token"`
	Digest         string   `json:"digest"`
	BaseCommit     string   `json:"base_commit"`
	HeadCommit     string   `json:"head_commit"`
	ChangedFiles   []string `json:"changed_files"`
	Diff           string   `json:"diff"`
	TestCommand    []string `json:"test_command"`
	TestOutput     string   `json:"test_output"`
	RunStateDigest string   `json:"run_state_digest"`
	CreatedAt      string   `json:"created_at"`
}

type GitHubWorkflow struct {
	ID             string                `json:"id"`
	Status         string                `json:"status"`
	Repository     string                `json:"repository"`
	IssueNumber    int                   `json:"issue_number"`
	IssueTitle     string                `json:"issue_title"`
	IssueURL       string                `json:"issue_url"`
	AgentID        string                `json:"agent_id"`
	ConversationID string                `json:"conversation_id"`
	TaskRunID      string                `json:"task_run_id,omitempty"`
	LocalRepoPath  string                `json:"local_repo_path"`
	WorktreePath   string                `json:"worktree_path"`
	BaseRef        string                `json:"base_ref"`
	BaseCommit     string                `json:"base_commit"`
	Branch         string                `json:"branch"`
	ExpectedLogin  string                `json:"expected_login"`
	Review         *GitHubWorkflowReview `json:"review,omitempty"`
	Pushed         bool                  `json:"pushed"`
	PullRequestURL string                `json:"pull_request_url,omitempty"`
	PullRequestNum int                   `json:"pull_request_number,omitempty"`
	Error          string                `json:"error,omitempty"`
	CreatedAt      string                `json:"created_at"`
	UpdatedAt      string                `json:"updated_at"`
}
