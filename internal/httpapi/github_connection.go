package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

var githubRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}$`)

type githubConnectionRequest struct {
	Enabled       bool     `json:"enabled"`
	ExpectedLogin string   `json:"expected_login"`
	Repositories  []string `json:"repositories"`
	AgentIDs      []string `json:"agent_ids"`
}

// gh receives fixed commands on github.com. Tokens stay in the controller process.
func (s *Server) runGitHub(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if s.GitHubRunner != nil {
		raw, err := s.GitHubRunner(ctx, args...)
		if len(raw) > 2<<20 {
			return nil, errors.New("GitHub response exceeded 2 MiB")
		}
		return raw, err
	}
	command := exec.CommandContext(ctx, "gh", args...)
	command.Env = append(os.Environ(), "GH_HOST=github.com", "GH_PROMPT_DISABLED=1", "GH_PAGER=cat")
	var output limitedGitHubOutput
	command.Stdout = &output
	if err := command.Run(); err != nil {
		return nil, errors.New("GitHub request failed; check gh auth login on this computer and repository access")
	}
	return output.Bytes(), nil
}

type limitedGitHubOutput struct{ bytes.Buffer }

func (b *limitedGitHubOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 2<<20 {
		return 0, errors.New("GitHub response exceeded 2 MiB")
	}
	return b.Buffer.Write(p)
}

func (s *Server) githubIdentity(ctx context.Context) (string, error) {
	raw, err := s.runGitHub(ctx, "api", "--hostname", "github.com", "user", "--jq", ".login")
	if err != nil {
		return "", err
	}
	login := strings.TrimSpace(string(raw))
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`).MatchString(login) {
		return "", errors.New("GitHub login is unavailable")
	}
	return login, nil
}
func (s *Server) githubConnection(w http.ResponseWriter, r *http.Request) {
	config, err := s.Store.GetGitHubConnection(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	_, lookupErr := exec.LookPath("gh")
	login, authErr := s.githubIdentity(r.Context())
	authenticated := authErr == nil
	if config.Enabled && login != config.Login {
		authenticated = false
		login = config.Login
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"enabled": config.Enabled, "login": login, "repositories": config.Repositories, "agent_ids": config.AgentIDs, "installed": lookupErr == nil || s.GitHubRunner != nil, "authenticated": authenticated})
}
func (s *Server) saveGitHubConnection(w http.ResponseWriter, r *http.Request) {
	var request githubConnectionRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		s.writeErrorStatus(w, 400, errors.New("invalid GitHub connection"))
		return
	}
	if !request.Enabled {
		config := store.GitHubConnection{Repositories: []string{}, AgentIDs: []string{}}
		if err := s.Store.SaveGitHubConnection(r.Context(), config); err != nil {
			s.writeError(w, err)
			return
		}
		s.writeJSON(w, http.StatusOK, config)
		return
	}
	config := store.GitHubConnection{Enabled: request.Enabled, Repositories: request.Repositories, AgentIDs: request.AgentIDs}
	if len(config.Repositories) > 100 || len(config.AgentIDs) > 100 {
		s.writeErrorStatus(w, 400, errors.New("at most 100 repositories and Agents can be selected"))
		return
	}
	config.Login = ""
	repositories := make([]string, 0, len(config.Repositories))
	for _, repo := range config.Repositories {
		repo = strings.TrimSpace(repo)
		if !githubRepositoryPattern.MatchString(repo) || strings.HasSuffix(repo, "/.") || strings.HasSuffix(repo, "/..") {
			s.writeErrorStatus(w, 400, errors.New("repositories must use owner/name"))
			return
		}
		repo = strings.ToLower(repo)
		if !slices.Contains(repositories, repo) {
			repositories = append(repositories, repo)
		}
	}
	config.Repositories = repositories
	agents, err := s.Store.ListAgents(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	preferences, err := s.Store.GetPreferences(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	defaultProvider := preferences.Normalize().Workspace.Engine
	if defaultProvider == "" {
		defaultProvider = "grok"
	}
	agentIDs := make([]string, 0, len(config.AgentIDs))
	for _, id := range config.AgentIDs {
		id = strings.TrimSpace(id)
		agentIndex := slices.IndexFunc(agents, func(a domain.Agent) bool { return a.Bot.ID == id })
		if agentIndex < 0 {
			s.writeErrorStatus(w, 400, errors.New("unknown Agent in GitHub grant"))
			return
		}
		provider := defaultProvider
		if metadata := agents[agentIndex].Metadata; metadata != nil && metadata.Lead != nil {
			provider = configuredLeadProvider(metadata.Lead.Harness)
		}
		if isPiLeadProvider(provider) {
			s.writeErrorStatus(w, 400, fmt.Errorf("Agent %q uses a Pi lead, which does not support GitHub tools", agents[agentIndex].Bot.Name))
			return
		}
		if !slices.Contains(agentIDs, id) {
			agentIDs = append(agentIDs, id)
		}
	}
	config.AgentIDs = agentIDs
	if config.Enabled {
		expectedLogin := strings.TrimSpace(request.ExpectedLogin)
		if expectedLogin == "" {
			s.writeErrorStatus(w, 409, errors.New("GitHub login changed; refresh the connection before saving"))
			return
		}
		login, err := s.githubIdentity(r.Context())
		if err != nil {
			s.writeErrorStatus(w, 409, err)
			return
		}
		current, err := s.Store.GetGitHubConnection(r.Context())
		if err != nil {
			s.writeError(w, err)
			return
		}
		if login != expectedLogin || (current.Enabled && current.Login != login) {
			s.writeErrorStatus(w, 409, errors.New("GitHub login changed; disconnect and reconnect before saving access"))
			return
		}
		config.Login = login
	}
	if config.Repositories == nil {
		config.Repositories = []string{}
	}
	if config.AgentIDs == nil {
		config.AgentIDs = []string{}
	}
	if err := s.Store.SaveGitHubConnection(r.Context(), config); err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, 200, config)
}
func (s *Server) githubRepositories(w http.ResponseWriter, r *http.Request) {
	config, err := s.Store.GetGitHubConnection(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	login, err := s.githubIdentity(r.Context())
	if err != nil {
		s.writeErrorStatus(w, 409, err)
		return
	}
	if config.Enabled && login != config.Login {
		s.writeErrorStatus(w, 409, errors.New("GitHub account changed; reconnect before discovering repositories"))
		return
	}
	raw, err := s.runGitHub(r.Context(), "api", "--hostname", "github.com", "user/repos?per_page=100&sort=updated", "--jq", "[.[] | {name: .full_name, private: .private}]")
	if err != nil {
		s.writeErrorStatus(w, 409, err)
		return
	}
	var repos []struct {
		Name    string `json:"name"`
		Private bool   `json:"private"`
	}
	if err := json.Unmarshal(raw, &repos); err != nil {
		s.writeError(w, errors.New("invalid GitHub repository response"))
		return
	}
	s.writeJSON(w, 200, map[string]any{"repositories": repos})
}

type githubReadRequest struct {
	Operation  string `json:"operation"`
	Repository string `json:"repository"`
	Number     int    `json:"number,omitempty"`
}

func (s *Server) githubRead(w http.ResponseWriter, r *http.Request) {
	run, err := s.authorizedBridgeRun(r)
	if err != nil {
		s.writeErrorStatus(w, 401, err)
		return
	}
	var input githubReadRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeErrorStatus(w, 400, errors.New("invalid GitHub tool arguments"))
		return
	}
	raw, err := s.readGitHub(r.Context(), run.BotID, input)
	if err != nil {
		s.writeErrorStatus(w, 403, err)
		return
	}
	s.writeJSON(w, 200, json.RawMessage(raw))
}
func (s *Server) readGitHub(ctx context.Context, agentID string, input githubReadRequest) ([]byte, error) {
	config, err := s.Store.GetGitHubConnection(ctx)
	if err != nil {
		return nil, err
	}
	if !config.Enabled || !slices.Contains(config.AgentIDs, agentID) {
		return nil, errors.New("GitHub access is not enabled for this Agent")
	}
	repo := strings.ToLower(strings.TrimSpace(input.Repository))
	if !githubRepositoryPattern.MatchString(repo) || !slices.Contains(config.Repositories, repo) {
		return nil, errors.New("repository is not allowed for this connection")
	}
	path := "repos/" + repo
	switch input.Operation {
	case "list_issues":
		path += "/issues?state=open&per_page=30"
	case "list_pull_requests":
		path += "/pulls?state=open&per_page=30"
	case "get_issue", "get_pull_request":
		if input.Number < 1 || input.Number > 2147483647 {
			return nil, errors.New("number must be a positive issue or pull request number")
		}
		if input.Operation == "get_issue" {
			path += "/issues/"
		} else {
			path += "/pulls/"
		}
		path += strconv.Itoa(input.Number)
	default:
		return nil, errors.New("unsupported GitHub read operation")
	}
	login, err := s.githubIdentity(ctx)
	if err != nil {
		return nil, err
	}
	if login != config.Login {
		return nil, errors.New("GitHub account changed; reconnect before running tools")
	}
	// Reload after authentication so a disconnect during that request takes effect.
	current, err := s.Store.GetGitHubConnection(ctx)
	if err != nil {
		return nil, err
	}
	if !current.Enabled || current.Login != login || !slices.Contains(current.AgentIDs, agentID) || !slices.Contains(current.Repositories, repo) {
		return nil, errors.New("GitHub access was revoked")
	}
	raw, err := s.runGitHub(ctx, "api", "--hostname", "github.com", path)
	if err != nil {
		return nil, err
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("invalid GitHub response")
	}
	return raw, nil
}
