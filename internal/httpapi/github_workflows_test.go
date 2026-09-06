package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

type fakeWorkflowGitHub struct {
	mu          sync.Mutex
	prCreated   bool
	createCalls int
	body        string
	headSHA     string
	failCreate  bool
	issueReads  int
}

func (fake *fakeWorkflowGitHub) run(_ context.Context, args ...string) ([]byte, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, " user --jq .login"):
		return []byte("octocat\n"), nil
	case strings.Contains(joined, "repos/acme/widgets/issues/7"):
		fake.issueReads++
		return []byte(`{"number":7,"title":"Fix widget","body":"Do the requested fix.","html_url":"https://github.com/acme/widgets/issues/7"}`), nil
	case strings.Contains(joined, "repos/acme/widgets/pulls?"):
		if !fake.prCreated {
			return []byte(`[]`), nil
		}
		return []byte(`[{"number":11,"html_url":"https://github.com/acme/widgets/pull/11","draft":true,"head":{"sha":"` + fake.headSHA + `"},"base":{"ref":"main"}}]`), nil
	case len(args) > 1 && args[0] == "pr" && args[1] == "create":
		fake.createCalls++
		if fake.failCreate {
			fake.failCreate = false
			return nil, errors.New("temporary create failure")
		}
		bodyIndex := slices.Index(args, "--body-file")
		if bodyIndex < 0 || bodyIndex+1 >= len(args) {
			return nil, errors.New("missing body file")
		}
		body, err := os.ReadFile(args[bodyIndex+1])
		if err != nil {
			return nil, err
		}
		fake.body = string(body)
		fake.prCreated = true
		return []byte("https://github.com/acme/widgets/pull/11\n"), nil
	default:
		return nil, errors.New("unexpected gh call: " + joined)
	}
}

type fakeWorkflowCommands struct {
	mu                   sync.Mutex
	pushCalls            int
	pushedDestination    string
	pushedRefspec        string
	pushCredentialReset  bool
	pushCredentialHelper string
}

func (fake *fakeWorkflowCommands) run(ctx context.Context, dir string, argv ...string) ([]byte, error) {
	if pushIndex := slices.Index(argv, "push"); len(argv) > 0 && argv[0] == "git" && pushIndex > 0 {
		fake.mu.Lock()
		fake.pushCalls++
		upstreamIndex := slices.Index(argv, "--set-upstream")
		if upstreamIndex >= 0 && upstreamIndex+1 < len(argv) {
			fake.pushedDestination = argv[upstreamIndex+1]
		}
		fake.pushedRefspec = argv[len(argv)-1]
		for index := 0; index+1 < pushIndex; index++ {
			if argv[index] != "-c" {
				continue
			}
			switch argv[index+1] {
			case "credential.helper=":
				fake.pushCredentialReset = true
			case "credential.https://github.com.helper=!gh auth git-credential":
				fake.pushCredentialHelper = argv[index+1]
			}
		}
		fake.mu.Unlock()
		return []byte("pushed\n"), nil
	}
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat")
	return command.CombinedOutput()
}

func workflowRequest(server *Server, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if server.RemoteToken != "" {
		request.Header.Set("Authorization", "Bearer "+server.RemoteToken)
	}
	response := httptest.NewRecorder()
	server.handleGitHubWorkflowRoutes(response, request)
	return response
}

func setupWorkflowRepository(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "widgets")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test User"},
		{"remote", "add", "origin", "git@github.com:acme/widgets.git"},
	} {
		if output, err := runTestGit(repo, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README.md"}, {"commit", "-m", "base"}} {
		if output, err := runTestGit(repo, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return repo
}

func runTestGit(dir string, args ...string) ([]byte, error) {
	command := exec.Command("git", args...)
	command.Dir = dir
	return command.CombinedOutput()
}

func commitWorkflowChange(t *testing.T, worktree, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(worktree, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "--", name}, {"commit", "-m", "workflow change"}} {
		if output, err := runTestGit(worktree, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
}

func TestGitHubWorkflowStartReviewStaleCheckAndDraftPublish(t *testing.T) {
	instance, conversation, server := openTasksAPI(t, "")
	repo := setupWorkflowRepository(t)
	if output, err := runTestGit(repo, "remote", "set-url", "--push", "origin", "https://evil.example/acme/widgets.git"); err != nil {
		t.Fatalf("set malicious push URL: %v: %s", err, output)
	}
	if err := instance.SaveGitHubConnection(t.Context(), store.GitHubConnection{Enabled: true, Login: "octocat", Repositories: []string{"acme/widgets"}, AgentIDs: []string{conversation.BotID}}); err != nil {
		t.Fatal(err)
	}
	fakeGH := &fakeWorkflowGitHub{}
	fakeCommands := &fakeWorkflowCommands{}
	server.GitHubRunner = fakeGH.run
	server.GitWorkflowRunner = fakeCommands.run
	server.RemoteToken = "controller"
	server.CollaborationMCPCommand = "/bin/true"
	server.AllowHarnessExecution = true
	server.runExecutorOverride = &recordingHarnessExecutor{}
	startBody, _ := json.Marshal(githubWorkflowStartRequest{LocalRepoPath: repo, Repository: "acme/widgets", IssueNumber: 7, AgentID: conversation.BotID, BaseRef: "main"})
	started := workflowRequest(server, http.MethodPost, "/api/github-workflows", string(startBody))
	if started.Code != http.StatusAccepted || started.Header().Get("X-GitHub-Workflow-ID") == "" {
		t.Fatalf("start = %d %s", started.Code, started.Body.String())
	}
	workflowID := started.Header().Get("X-GitHub-Workflow-ID")
	workflow, err := instance.GetGitHubWorkflow(t.Context(), workflowID)
	if err != nil || workflow.ConversationID == conversation.ID || workflow.TaskRunID == "" {
		t.Fatalf("workflow = %#v, %v", workflow, err)
	}
	run := waitForTerminalRun(t, instance, workflow.ConversationID)
	if run.Status != "completed" {
		t.Fatalf("workflow run = %#v", run)
	}
	taskInput, err := instance.GetTaskInput(t.Context(), run.ID)
	task, _, taskErr := instance.GetTask(t.Context(), run.ID)
	if err != nil || taskErr != nil || !strings.Contains(taskInput.Brief, "Do the requested fix.") || !strings.Contains(taskInput.Brief, "Do not push") || task.Title != "GitHub #7: Fix widget" {
		t.Fatalf("workflow task input=%#v task=%#v errors=%v,%v", taskInput, task, err, taskErr)
	}
	assigned, err := instance.GetRunWorkdir(t.Context(), run.ID)
	if err != nil || assigned != workflow.WorktreePath {
		t.Fatalf("assigned worktree = %q, %v", assigned, err)
	}
	commitWorkflowChange(t, workflow.WorktreePath, "fix.txt", "first\n")
	mutatingReview := workflowRequest(server, http.MethodPost, "/api/github-workflows/"+workflowID+"/review", `{"test_command":["git","commit","--allow-empty","-m","test mutation"]}`)
	if mutatingReview.Code != http.StatusConflict || !strings.Contains(mutatingReview.Body.String(), "changed the reviewed worktree") {
		t.Fatalf("mutating review = %d %s", mutatingReview.Code, mutatingReview.Body.String())
	}
	reviewed := workflowRequest(server, http.MethodPost, "/api/github-workflows/"+workflowID+"/review", `{"test_command":["git","status","--short"]}`)
	if reviewed.Code != http.StatusOK {
		t.Fatalf("review = %d %s", reviewed.Code, reviewed.Body.String())
	}
	if err := json.Unmarshal(reviewed.Body.Bytes(), &workflow); err != nil || workflow.Review == nil || len(workflow.Review.ChangedFiles) != 1 {
		t.Fatalf("review payload = %#v, %v", workflow, err)
	}
	commitWorkflowChange(t, workflow.WorktreePath, "fix.txt", "second\n")
	publishBody, _ := json.Marshal(githubWorkflowPublishRequest{ReviewToken: workflow.Review.Token, ReviewDigest: workflow.Review.Digest, ExpectedLogin: "octocat", Title: "Fix widget", Body: "Exact PR body\n"})
	stale := workflowRequest(server, http.MethodPost, "/api/github-workflows/"+workflowID+"/publish", string(publishBody))
	if stale.Code != http.StatusConflict || fakeCommands.pushCalls != 0 {
		t.Fatalf("stale publish = %d %s, pushes=%d", stale.Code, stale.Body.String(), fakeCommands.pushCalls)
	}
	reviewed = workflowRequest(server, http.MethodPost, "/api/github-workflows/"+workflowID+"/review", `{"test_command":["git","status","--short"]}`)
	if reviewed.Code != http.StatusOK || json.Unmarshal(reviewed.Body.Bytes(), &workflow) != nil || workflow.Review == nil {
		t.Fatalf("second review = %d %s", reviewed.Code, reviewed.Body.String())
	}
	publishBody, _ = json.Marshal(githubWorkflowPublishRequest{ReviewToken: workflow.Review.Token, ReviewDigest: workflow.Review.Digest, ExpectedLogin: "octocat", Title: "Fix widget", Body: "Exact PR body\n"})
	fakeGH.mu.Lock()
	fakeGH.headSHA = workflow.Review.HeadCommit
	fakeGH.failCreate = true
	fakeGH.mu.Unlock()
	if output, err := runTestGit(workflow.WorktreePath, "config", "--local", "url.https://evil.example/.insteadOf", "https://github.com/"); err != nil {
		t.Fatalf("set URL rewrite: %v: %s", err, output)
	}
	rewritten := workflowRequest(server, http.MethodPost, "/api/github-workflows/"+workflowID+"/publish", string(publishBody))
	if rewritten.Code != http.StatusConflict || fakeCommands.pushCalls != 0 {
		t.Fatalf("URL rewrite publish = %d %s, pushes=%d", rewritten.Code, rewritten.Body.String(), fakeCommands.pushCalls)
	}
	if output, err := runTestGit(workflow.WorktreePath, "config", "--local", "--unset-all", "url.https://evil.example/.insteadOf"); err != nil {
		t.Fatalf("clear URL rewrite: %v: %s", err, output)
	}
	partial := workflowRequest(server, http.MethodPost, "/api/github-workflows/"+workflowID+"/publish", string(publishBody))
	if partial.Code != http.StatusBadGateway || fakeCommands.pushCalls != 1 || fakeGH.createCalls != 1 {
		t.Fatalf("partial publish = %d %s, pushes=%d creates=%d", partial.Code, partial.Body.String(), fakeCommands.pushCalls, fakeGH.createCalls)
	}
	partialState, err := instance.GetGitHubWorkflow(t.Context(), workflowID)
	if err != nil || !partialState.Pushed || partialState.Status != domain.GitHubWorkflowPublishFailed {
		t.Fatalf("partial state = %#v, %v", partialState, err)
	}
	published := workflowRequest(server, http.MethodPost, "/api/github-workflows/"+workflowID+"/publish", string(publishBody))
	if published.Code != http.StatusOK || json.Unmarshal(published.Body.Bytes(), &workflow) != nil || workflow.Status != domain.GitHubWorkflowDraftPR || workflow.PullRequestNum != 11 {
		t.Fatalf("publish = %d %s", published.Code, published.Body.String())
	}
	wantRefspec := workflow.Review.HeadCommit + ":refs/heads/" + workflow.Branch
	wantDestination := "https://github.com/acme/widgets.git"
	if fakeCommands.pushCalls != 1 || fakeCommands.pushedDestination != wantDestination || fakeCommands.pushedRefspec != wantRefspec || !fakeCommands.pushCredentialReset || fakeCommands.pushCredentialHelper != "credential.https://github.com.helper=!gh auth git-credential" || fakeGH.createCalls != 2 || fakeGH.body != "Exact PR body\n" {
		t.Fatalf("writes: pushes=%d destination=%q refspec=%q credential_reset=%t credential_helper=%q creates=%d body=%q", fakeCommands.pushCalls, fakeCommands.pushedDestination, fakeCommands.pushedRefspec, fakeCommands.pushCredentialReset, fakeCommands.pushCredentialHelper, fakeGH.createCalls, fakeGH.body)
	}
	again := workflowRequest(server, http.MethodPost, "/api/github-workflows/"+workflowID+"/publish", string(publishBody))
	if again.Code != http.StatusOK || fakeCommands.pushCalls != 1 || fakeGH.createCalls != 2 {
		t.Fatalf("idempotent publish = %d %s, pushes=%d creates=%d", again.Code, again.Body.String(), fakeCommands.pushCalls, fakeGH.createCalls)
	}
}

func TestGitHubWorkflowRejectsOriginMismatchBeforeWorktree(t *testing.T) {
	instance, conversation, server := openTasksAPI(t, "")
	repo := setupWorkflowRepository(t)
	if output, err := runTestGit(repo, "remote", "set-url", "origin", "https://github.com/acme/other.git"); err != nil {
		t.Fatalf("set origin: %v: %s", err, output)
	}
	if err := instance.SaveGitHubConnection(t.Context(), store.GitHubConnection{Enabled: true, Login: "octocat", Repositories: []string{"acme/widgets"}, AgentIDs: []string{conversation.BotID}}); err != nil {
		t.Fatal(err)
	}
	fakeGH := &fakeWorkflowGitHub{}
	server.GitHubRunner = fakeGH.run
	server.GitWorkflowRunner = (&fakeWorkflowCommands{}).run
	body, _ := json.Marshal(githubWorkflowStartRequest{LocalRepoPath: repo, Repository: "acme/widgets", IssueNumber: 7, AgentID: conversation.BotID, BaseRef: "main"})
	response := workflowRequest(server, http.MethodPost, "/api/github-workflows", string(body))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "origin") {
		t.Fatalf("origin mismatch = %d %s", response.Code, response.Body.String())
	}
	items, err := instance.ListGitHubWorkflows(t.Context(), 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("workflows after rejection = %#v, %v", items, err)
	}
	if fakeGH.issueReads != 0 {
		t.Fatalf("origin mismatch reached issue API %d times", fakeGH.issueReads)
	}
}

func TestGitHubWorkflowCommandsStripCredentialsAndDisableHooks(t *testing.T) {
	t.Setenv("OPENAGENTFLEET_REMOTE_TOKEN", "sentinel-controller-token")
	t.Setenv("GH_TOKEN", "sentinel-github-token")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	server := &Server{}
	environment, err := server.runGitWorkflow(t.Context(), githubWorkflowCommandTimeout, t.TempDir(), "env")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"OPENAGENTFLEET_REMOTE_TOKEN=", "GH_TOKEN=", "GH_CONFIG_DIR="} {
		if strings.Contains(string(environment), forbidden) {
			t.Fatalf("workflow command inherited %s", forbidden)
		}
	}
	publicationEnvironment := strings.Join(githubWorkflowCommandEnv(true, true), "\n")
	if !strings.Contains(publicationEnvironment, "GH_TOKEN=sentinel-github-token") {
		t.Fatal("workflow publication did not receive the GitHub CLI token")
	}
	if !strings.Contains(publicationEnvironment, "GH_CONFIG_DIR=") {
		t.Fatal("workflow publication did not receive the GitHub CLI config directory")
	}
	if strings.Contains(publicationEnvironment, "OPENAGENTFLEET_REMOTE_TOKEN=") {
		t.Fatal("workflow publication inherited the controller bearer token")
	}
	var testGitArgs []string
	testGitServer := &Server{GitWorkflowRunner: func(_ context.Context, _ string, argv ...string) ([]byte, error) {
		testGitArgs = append([]string(nil), argv...)
		return nil, nil
	}}
	if _, err := testGitServer.runGitWorkflow(t.Context(), githubWorkflowCommandTimeout, t.TempDir(), "git", "push", "https://github.com/acme/widgets.git", "HEAD:refs/heads/test"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(testGitArgs, "\n"), "credential") {
		t.Fatalf("user test git command received a credential helper: %q", testGitArgs)
	}
	repo := setupWorkflowRepository(t)
	hookMarker := filepath.Join(t.TempDir(), "hook-ran")
	hookDir := filepath.Join(repo, ".git", "hooks")
	if err := os.MkdirAll(hookDir, 0o700); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(hookDir, "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf ran > \""+hookMarker+"\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "hook.txt"), []byte("change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := server.runGitWorkflow(t.Context(), githubWorkflowCommandTimeout, repo, "git", "add", "hook.txt"); err != nil {
		t.Fatalf("git add: %v: %s", err, output)
	}
	if output, err := server.runGitWorkflow(t.Context(), githubWorkflowCommandTimeout, repo, "git", "commit", "-m", "hook check"); err != nil {
		t.Fatalf("git commit: %v: %s", err, output)
	}
	if _, err := os.Stat(hookMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository hook ran or marker check failed: %v", err)
	}
}
