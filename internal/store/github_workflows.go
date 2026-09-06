package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/id"
)

var (
	ErrGitHubWorkflowNotFound = errors.New("GitHub workflow not found")
	ErrGitHubWorkflowState    = errors.New("GitHub workflow state conflict")
	ErrGitHubPublishClaimed   = errors.New("GitHub workflow publication is already in progress")
)

var githubWorkflowOperationLocks sync.Map

type GitHubWorkflowPublishClaim struct {
	WorkflowID string
	ClaimID    string
}

type GitHubWorkflowTaskCreation struct {
	Message     domain.Message
	Run         domain.Run
	QueuedEvent domain.RunEvent
}

func (s *Store) MigrateGitHubWorkflows(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS github_workflows (
 id TEXT PRIMARY KEY,
 status TEXT NOT NULL,
 repository TEXT NOT NULL,
 issue_number INTEGER NOT NULL,
 issue_title TEXT NOT NULL,
 issue_url TEXT NOT NULL,
 agent_id TEXT NOT NULL REFERENCES bots(id),
 conversation_id TEXT NOT NULL REFERENCES conversations(id),
 task_run_id TEXT REFERENCES runs(id),
 local_repo_path TEXT NOT NULL,
 worktree_path TEXT NOT NULL UNIQUE,
 base_ref TEXT NOT NULL,
 base_commit TEXT NOT NULL,
 branch TEXT NOT NULL UNIQUE,
 expected_login TEXT NOT NULL,
 review_json TEXT NOT NULL DEFAULT '',
 pushed INTEGER NOT NULL DEFAULT 0,
 pull_request_url TEXT NOT NULL DEFAULT '',
 pull_request_number INTEGER NOT NULL DEFAULT 0,
 error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
 );
 CREATE INDEX IF NOT EXISTS github_workflows_created ON github_workflows(created_at DESC,id DESC);
 CREATE INDEX IF NOT EXISTS github_workflows_conversation ON github_workflows(conversation_id);
 CREATE TABLE IF NOT EXISTS github_workflow_publish_claims (
   workflow_id TEXT PRIMARY KEY REFERENCES github_workflows(id) ON DELETE CASCADE,
   review_generation INTEGER NOT NULL,
   review_token TEXT NOT NULL,
   review_digest TEXT NOT NULL,
   claim_id TEXT NOT NULL UNIQUE,
   expires_at TEXT NOT NULL
 );
 CREATE TRIGGER IF NOT EXISTS github_workflow_runs_blocked_during_publish
 BEFORE INSERT ON runs
 WHEN EXISTS (
   SELECT 1 FROM github_workflows w
   JOIN github_workflow_publish_claims p ON p.workflow_id=w.id
   WHERE w.conversation_id=NEW.conversation_id AND julianday(p.expires_at)>julianday('now')
 )
 BEGIN
   SELECT RAISE(ABORT,'GitHub workflow publication is in progress');
 END;`)
	return err
}

func (s *Store) CreateGitHubWorkflow(ctx context.Context, workflow domain.GitHubWorkflow) (domain.GitHubWorkflow, error) {
	if err := s.MigrateGitHubWorkflows(ctx); err != nil {
		return workflow, err
	}
	if workflow.ID == "" || workflow.ConversationID == "" || workflow.AgentID == "" || workflow.WorktreePath == "" || workflow.Branch == "" {
		return workflow, errors.New("GitHub workflow fields are required")
	}
	if workflow.Status == "" {
		workflow.Status = domain.GitHubWorkflowPreparing
	}
	var conversationAgentID string
	if err := s.db.QueryRowContext(ctx, `SELECT bot_id FROM conversations WHERE id=?`, workflow.ConversationID).Scan(&conversationAgentID); err != nil || conversationAgentID != workflow.AgentID {
		return workflow, errors.New("GitHub workflow conversation must belong to its Agent")
	}
	if workflow.CreatedAt == "" {
		workflow.CreatedAt = now()
	}
	workflow.UpdatedAt = workflow.CreatedAt
	_, err := s.db.ExecContext(ctx, `INSERT INTO github_workflows
(id,status,repository,issue_number,issue_title,issue_url,agent_id,conversation_id,task_run_id,local_repo_path,worktree_path,base_ref,base_commit,branch,expected_login,review_json,pushed,pull_request_url,pull_request_number,error,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,NULL,?,?,?,?,?,?, '',0,'',0,'',?,?)`,
		workflow.ID, workflow.Status, workflow.Repository, workflow.IssueNumber, workflow.IssueTitle, workflow.IssueURL,
		workflow.AgentID, workflow.ConversationID, workflow.LocalRepoPath, workflow.WorktreePath, workflow.BaseRef,
		workflow.BaseCommit, workflow.Branch, workflow.ExpectedLogin, workflow.CreatedAt, workflow.UpdatedAt)
	return workflow, err
}

func (s *Store) CreateGitHubWorkflowTask(ctx context.Context, conversationID, agentID, provider, displayTitle, fullInput, providerPrompt string) (GitHubWorkflowTaskCreation, error) {
	var created GitHubWorkflowTaskCreation
	if strings.TrimSpace(displayTitle) == "" || strings.TrimSpace(fullInput) == "" {
		return created, errors.New("GitHub workflow task input is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return created, err
	}
	defer func() { _ = tx.Rollback() }()
	var conversationAgentID string
	if err := tx.QueryRowContext(ctx, `SELECT bot_id FROM conversations WHERE id=?`, conversationID).Scan(&conversationAgentID); err != nil || conversationAgentID != agentID {
		return created, errors.New("GitHub workflow task conversation does not belong to its Agent")
	}
	timestamp := now()
	created.Message = domain.Message{ID: id.New("msg"), ConversationID: conversationID, Role: "user", Content: fullInput, CreatedAt: timestamp}
	created.Run = domain.Run{ID: id.New("run"), ConversationID: conversationID, BotID: agentID, Provider: provider, Status: "queued", Prompt: providerPrompt, CreatedAt: timestamp, UpdatedAt: timestamp}
	created.QueuedEvent = domain.RunEvent{ID: id.New("evt"), RunID: created.Run.ID, Type: "run.queued", Data: `{"status":"queued"}`, CreatedAt: timestamp}
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages(id,conversation_id,role,content,created_at) VALUES(?,?,?,?,?)`, created.Message.ID, created.Message.ConversationID, created.Message.Role, created.Message.Content, created.Message.CreatedAt); err != nil {
		return created, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs(id,conversation_id,bot_id,provider,status,prompt,error,created_at,updated_at) VALUES(?,?,?,?,?,?,'',?,?)`, created.Run.ID, created.Run.ConversationID, created.Run.BotID, created.Run.Provider, created.Run.Status, created.Run.Prompt, created.Run.CreatedAt, created.Run.UpdatedAt); err != nil {
		return created, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_events(id,run_id,type,data,created_at) VALUES(?,?,?,?,?)`, created.QueuedEvent.ID, created.QueuedEvent.RunID, created.QueuedEvent.Type, created.QueuedEvent.Data, created.QueuedEvent.CreatedAt); err != nil {
		return created, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_results(run_id,title) VALUES(?,?)`, created.Run.ID, displayTitle); err != nil {
		return created, err
	}
	if err := tx.Commit(); err != nil {
		return created, err
	}
	return created, nil
}

func (s *Store) LinkGitHubWorkflowRun(ctx context.Context, workflowID, runID string) error {
	if err := s.MigrateGitHubWorkflows(ctx); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE github_workflows SET task_run_id=?,status=?,error='',updated_at=?
 WHERE id=? AND (task_run_id IS NULL OR task_run_id=?) AND EXISTS (
   SELECT 1 FROM runs r WHERE r.id=? AND r.conversation_id=github_workflows.conversation_id AND r.bot_id=github_workflows.agent_id
 )`, runID, domain.GitHubWorkflowRunning, now(), workflowID, runID, runID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrGitHubWorkflowState
	}
	return nil
}

func (s *Store) GetGitHubWorkflow(ctx context.Context, workflowID string) (domain.GitHubWorkflow, error) {
	if err := s.MigrateGitHubWorkflows(ctx); err != nil {
		return domain.GitHubWorkflow{}, err
	}
	return scanGitHubWorkflow(s.db.QueryRowContext(ctx, githubWorkflowSelect+` WHERE id=?`, workflowID))
}

func (s *Store) ListGitHubWorkflows(ctx context.Context, limit int) ([]domain.GitHubWorkflow, error) {
	if err := s.MigrateGitHubWorkflows(ctx); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, githubWorkflowSelect+` ORDER BY created_at DESC,id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.GitHubWorkflow{}
	for rows.Next() {
		item, err := scanGitHubWorkflow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) SaveGitHubWorkflowReview(ctx context.Context, workflowID string, review domain.GitHubWorkflowReview) (domain.GitHubWorkflow, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return domain.GitHubWorkflow{}, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return domain.GitHubWorkflow{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	var currentJSON string
	if err := conn.QueryRowContext(ctx, `SELECT review_json FROM github_workflows WHERE id=?`, workflowID).Scan(&currentJSON); err != nil {
		return domain.GitHubWorkflow{}, err
	}
	if currentJSON != "" {
		var current domain.GitHubWorkflowReview
		if err := json.Unmarshal([]byte(currentJSON), &current); err != nil {
			return domain.GitHubWorkflow{}, err
		}
		review.Generation = current.Generation + 1
	} else {
		review.Generation = 1
	}
	var publishClaimID, publishClaimExpiry string
	claimErr := conn.QueryRowContext(ctx, `SELECT claim_id,expires_at FROM github_workflow_publish_claims WHERE workflow_id=?`, workflowID).Scan(&publishClaimID, &publishClaimExpiry)
	if claimErr == nil {
		expires, parseErr := time.Parse(time.RFC3339Nano, publishClaimExpiry)
		if parseErr == nil && expires.After(time.Now().UTC()) {
			return domain.GitHubWorkflow{}, ErrGitHubPublishClaimed
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM github_workflow_publish_claims WHERE workflow_id=? AND claim_id=?`, workflowID, publishClaimID); err != nil {
			return domain.GitHubWorkflow{}, err
		}
	} else if !errors.Is(claimErr, sql.ErrNoRows) {
		return domain.GitHubWorkflow{}, claimErr
	}
	raw, err := json.Marshal(review)
	if err != nil {
		return domain.GitHubWorkflow{}, err
	}
	result, err := conn.ExecContext(ctx, `UPDATE github_workflows SET status=?,review_json=?,pushed=0,pull_request_url='',pull_request_number=0,error='',updated_at=?
 WHERE id=? AND status IN (?,?,?,?)`, domain.GitHubWorkflowReviewed, string(raw), now(), workflowID,
		domain.GitHubWorkflowRunning, domain.GitHubWorkflowReady, domain.GitHubWorkflowReviewed, domain.GitHubWorkflowPublishFailed)
	if err != nil {
		return domain.GitHubWorkflow{}, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return domain.GitHubWorkflow{}, ErrGitHubWorkflowState
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return domain.GitHubWorkflow{}, err
	}
	committed = true
	return s.GetGitHubWorkflow(ctx, workflowID)
}

func (s *Store) AcquireGitHubWorkflowOperation(ctx context.Context, workflowID string) (func(), error) {
	if strings.TrimSpace(workflowID) == "" {
		return nil, errors.New("GitHub workflow id is required")
	}
	value, _ := githubWorkflowOperationLocks.LoadOrStore(workflowID, make(chan struct{}, 1))
	lock := value.(chan struct{})
	select {
	case lock <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-lock }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Store) ClaimGitHubWorkflowPublish(ctx context.Context, workflowID string, generation int, reviewToken, reviewDigest string) (GitHubWorkflowPublishClaim, error) {
	claim := GitHubWorkflowPublishClaim{WorkflowID: workflowID, ClaimID: id.New("publish")}
	if generation < 1 || reviewToken == "" || reviewDigest == "" {
		return claim, errors.New("review generation, token, and digest are required")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return claim, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return claim, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	var reviewJSON, conversationID string
	if err := conn.QueryRowContext(ctx, `SELECT review_json,conversation_id FROM github_workflows WHERE id=?`, workflowID).Scan(&reviewJSON, &conversationID); err != nil {
		return claim, err
	}
	var review domain.GitHubWorkflowReview
	if err := json.Unmarshal([]byte(reviewJSON), &review); err != nil || review.Generation != generation || review.Token != reviewToken || review.Digest != reviewDigest {
		return claim, ErrGitHubWorkflowState
	}
	runDigest, latest, active, err := githubWorkflowRunState(ctx, conn, conversationID)
	if err != nil || active || latest.Status != "completed" || runDigest != review.RunStateDigest {
		return claim, ErrGitHubWorkflowState
	}
	var existingID, expiresAt string
	err = conn.QueryRowContext(ctx, `SELECT claim_id,expires_at FROM github_workflow_publish_claims WHERE workflow_id=?`, workflowID).Scan(&existingID, &expiresAt)
	if err == nil {
		expires, parseErr := time.Parse(time.RFC3339Nano, expiresAt)
		if parseErr == nil && expires.After(time.Now().UTC()) {
			return claim, ErrGitHubPublishClaimed
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM github_workflow_publish_claims WHERE workflow_id=? AND claim_id=?`, workflowID, existingID); err != nil {
			return claim, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return claim, err
	}
	expiresAt = time.Now().UTC().Add(5 * time.Minute).Format(time.RFC3339Nano)
	if _, err := conn.ExecContext(ctx, `INSERT INTO github_workflow_publish_claims(workflow_id,review_generation,review_token,review_digest,claim_id,expires_at) VALUES(?,?,?,?,?,?)`, workflowID, generation, reviewToken, reviewDigest, claim.ClaimID, expiresAt); err != nil {
		return claim, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return claim, err
	}
	committed = true
	return claim, nil
}

func (s *Store) ReleaseGitHubWorkflowPublishClaim(ctx context.Context, claim GitHubWorkflowPublishClaim) error {
	if claim.WorkflowID == "" || claim.ClaimID == "" {
		return errors.New("GitHub workflow publish claim is invalid")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM github_workflow_publish_claims WHERE workflow_id=? AND claim_id=?`, claim.WorkflowID, claim.ClaimID)
	return err
}

func (s *Store) AssertGitHubWorkflowRunCreationAllowed(ctx context.Context, workflowID string) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	var claimID, expiresAt string
	err = conn.QueryRowContext(ctx, `SELECT claim_id,expires_at FROM github_workflow_publish_claims WHERE workflow_id=?`, workflowID).Scan(&claimID, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return err
		}
		committed = true
		return nil
	}
	if err != nil {
		return err
	}
	expires, parseErr := time.Parse(time.RFC3339Nano, expiresAt)
	if parseErr == nil && expires.After(time.Now().UTC()) {
		return ErrGitHubPublishClaimed
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM github_workflow_publish_claims WHERE workflow_id=? AND claim_id=?`, workflowID, claimID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

func (s *Store) MarkGitHubWorkflowPushed(ctx context.Context, workflowID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE github_workflows SET status=?,pushed=1,error='',updated_at=?
 WHERE id=? AND status IN (?,?,?)`, domain.GitHubWorkflowPushed, now(), workflowID,
		domain.GitHubWorkflowReviewed, domain.GitHubWorkflowPushed, domain.GitHubWorkflowPublishFailed)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrGitHubWorkflowState
	}
	return nil
}

func (s *Store) CompleteGitHubWorkflowPublish(ctx context.Context, workflowID, url string, number int) (domain.GitHubWorkflow, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE github_workflows SET status=?,pushed=1,pull_request_url=?,pull_request_number=?,error='',updated_at=?
 WHERE id=? AND status IN (?,?,?)`, domain.GitHubWorkflowDraftPR, url, number, now(), workflowID,
		domain.GitHubWorkflowReviewed, domain.GitHubWorkflowPushed, domain.GitHubWorkflowPublishFailed)
	if err != nil {
		return domain.GitHubWorkflow{}, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return domain.GitHubWorkflow{}, ErrGitHubWorkflowState
	}
	return s.GetGitHubWorkflow(ctx, workflowID)
}

func (s *Store) FailGitHubWorkflowPublish(ctx context.Context, workflowID string, publishErr error) error {
	message := "publish failed"
	if publishErr != nil {
		message = publishErr.Error()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE github_workflows SET status=?,error=?,updated_at=? WHERE id=? AND status<>?`,
		domain.GitHubWorkflowPublishFailed, message, now(), workflowID, domain.GitHubWorkflowDraftPR)
	return err
}

func (s *Store) GitHubWorkflowRunState(ctx context.Context, conversationID string) (string, domain.Run, bool, error) {
	return githubWorkflowRunState(ctx, s.db, conversationID)
}

type githubWorkflowRunQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func githubWorkflowRunState(ctx context.Context, queryer githubWorkflowRunQueryer, conversationID string) (string, domain.Run, bool, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT id,conversation_id,bot_id,provider,status,prompt,error,created_at,updated_at
 FROM runs WHERE conversation_id=? ORDER BY created_at,id`, conversationID)
	if err != nil {
		return "", domain.Run{}, false, err
	}
	defer rows.Close()
	hash := sha256.New()
	var latest domain.Run
	active := false
	count := 0
	for rows.Next() {
		var run domain.Run
		if err := rows.Scan(&run.ID, &run.ConversationID, &run.BotID, &run.Provider, &run.Status, &run.Prompt, &run.Error, &run.CreatedAt, &run.UpdatedAt); err != nil {
			return "", latest, false, err
		}
		count++
		latest = run
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\n", run.ID, run.Status, run.UpdatedAt)
		switch run.Status {
		case "completed", "failed", "stopped", "blocked":
		default:
			active = true
		}
	}
	if err := rows.Err(); err != nil {
		return "", latest, false, err
	}
	if count == 0 {
		return "", latest, false, sql.ErrNoRows
	}
	return hex.EncodeToString(hash.Sum(nil)), latest, active, nil
}

const githubWorkflowSelect = `SELECT id,status,repository,issue_number,issue_title,issue_url,agent_id,conversation_id,COALESCE(task_run_id,''),local_repo_path,worktree_path,base_ref,base_commit,branch,expected_login,review_json,pushed,pull_request_url,pull_request_number,error,created_at,updated_at FROM github_workflows`

func scanGitHubWorkflow(row interface{ Scan(...any) error }) (domain.GitHubWorkflow, error) {
	var item domain.GitHubWorkflow
	var reviewJSON string
	err := row.Scan(&item.ID, &item.Status, &item.Repository, &item.IssueNumber, &item.IssueTitle, &item.IssueURL,
		&item.AgentID, &item.ConversationID, &item.TaskRunID, &item.LocalRepoPath, &item.WorktreePath,
		&item.BaseRef, &item.BaseCommit, &item.Branch, &item.ExpectedLogin, &reviewJSON, &item.Pushed,
		&item.PullRequestURL, &item.PullRequestNum, &item.Error, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrGitHubWorkflowNotFound
	}
	if err != nil {
		return item, err
	}
	if strings.TrimSpace(reviewJSON) != "" {
		var review domain.GitHubWorkflowReview
		if err := json.Unmarshal([]byte(reviewJSON), &review); err != nil {
			return item, err
		}
		item.Review = &review
	}
	return item, nil
}
