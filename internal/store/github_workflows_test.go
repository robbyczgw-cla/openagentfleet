package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

func TestGitHubWorkflowPersistsLinkReviewAndPartialPublish(t *testing.T) {
	instance, conversation := openTaskStore(t)
	workflow, err := instance.CreateGitHubWorkflow(t.Context(), domain.GitHubWorkflow{
		ID: "ghwf-test", Repository: "acme/widgets", IssueNumber: 7, IssueTitle: "Fix it",
		AgentID: conversation.BotID, ConversationID: conversation.ID, LocalRepoPath: filepath.Dir(t.TempDir()),
		WorktreePath: t.TempDir(), BaseRef: "main", BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Branch: "oaf/issue-7-test", ExpectedLogin: "octocat",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, run, _, err := instance.CreateMessageWithAttachmentsAndRun(t.Context(), conversation.ID, conversation.BotID, "grok", "issue", "issue", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.LinkGitHubWorkflowRun(t.Context(), workflow.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := instance.LinkGitHubWorkflowRun(t.Context(), workflow.ID, "different"); !errors.Is(err, ErrGitHubWorkflowState) {
		t.Fatalf("relink error = %v", err)
	}
	if err := instance.UpdateRun(t.Context(), run.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	digest, latest, active, err := instance.GitHubWorkflowRunState(t.Context(), conversation.ID)
	if err != nil || active || latest.ID != run.ID || digest == "" {
		t.Fatalf("run state = %q %#v %v, %v", digest, latest, active, err)
	}
	review := domain.GitHubWorkflowReview{Token: "review-token", Digest: "digest", BaseCommit: workflow.BaseCommit, HeadCommit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ChangedFiles: []string{"fix.go"}, Diff: "diff", TestCommand: []string{"go", "test", "./..."}, TestOutput: "ok", RunStateDigest: digest, CreatedAt: "now"}
	stored, err := instance.SaveGitHubWorkflowReview(t.Context(), workflow.ID, review)
	if err != nil || stored.Status != domain.GitHubWorkflowReviewed || stored.Review == nil || stored.Review.Token != review.Token {
		t.Fatalf("stored review = %#v, %v", stored, err)
	}
	claim, err := instance.ClaimGitHubWorkflowPublish(t.Context(), workflow.ID, stored.Review.Generation, stored.Review.Token, stored.Review.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.AssertGitHubWorkflowRunCreationAllowed(t.Context(), workflow.ID); !errors.Is(err, ErrGitHubPublishClaimed) {
		t.Fatalf("active publish claim assertion = %v", err)
	}
	if _, _, _, _, err := instance.CreateMessageWithAttachmentsAndRun(t.Context(), conversation.ID, conversation.BotID, "grok", "racing task", "racing task", nil); err == nil || !strings.Contains(err.Error(), "publication is in progress") {
		t.Fatalf("run insert during publish = %v", err)
	}
	if err := instance.ReleaseGitHubWorkflowPublishClaim(t.Context(), claim); err != nil {
		t.Fatal(err)
	}
	if err := instance.MarkGitHubWorkflowPushed(t.Context(), workflow.ID); err != nil {
		t.Fatal(err)
	}
	if err := instance.FailGitHubWorkflowPublish(t.Context(), workflow.ID, errors.New("gh unavailable")); err != nil {
		t.Fatal(err)
	}
	failed, err := instance.GetGitHubWorkflow(t.Context(), workflow.ID)
	if err != nil || failed.Status != domain.GitHubWorkflowPublishFailed || !failed.Pushed {
		t.Fatalf("partial publish = %#v, %v", failed, err)
	}
	completed, err := instance.CompleteGitHubWorkflowPublish(t.Context(), workflow.ID, "https://github.com/acme/widgets/pull/9", 9)
	if err != nil || completed.Status != domain.GitHubWorkflowDraftPR || completed.PullRequestNum != 9 || !completed.Pushed {
		t.Fatalf("completed publish = %#v, %v", completed, err)
	}
	listed, err := instance.ListGitHubWorkflows(t.Context(), 10)
	if err != nil || len(listed) != 1 || listed[0].ID != workflow.ID {
		t.Fatalf("listed workflows = %#v, %v", listed, err)
	}
}

func TestGitHubWorkflowRunDigestChangesForNewRetry(t *testing.T) {
	instance, conversation := openTaskStore(t)
	first := createTaskRun(t, instance, conversation, "first", "first")
	if err := instance.UpdateRun(t.Context(), first.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	before, _, active, err := instance.GitHubWorkflowRunState(t.Context(), conversation.ID)
	if err != nil || active {
		t.Fatalf("before = %q active=%v err=%v", before, active, err)
	}
	second := createTaskRun(t, instance, conversation, "second", "second")
	during, latest, active, err := instance.GitHubWorkflowRunState(t.Context(), conversation.ID)
	if err != nil || !active || latest.ID != second.ID || during == before {
		t.Fatalf("during = %q latest=%#v active=%v err=%v", during, latest, active, err)
	}
}
