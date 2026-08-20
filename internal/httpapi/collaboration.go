package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/collaborationmcp"
	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
	"github.com/robbyczgw-cla/openagentfleet/internal/orchestration"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

type collaborationTaskRequest struct {
	AgentID string `json:"agent_id"`
	Content string `json:"content"`
	Task    string `json:"task"`
}

func (s *Server) listCollaborationAgents(w http.ResponseWriter, r *http.Request) {
	source, err := s.authorizedCollaborationRun(r)
	if err != nil {
		s.writeErrorStatus(w, http.StatusUnauthorized, err)
		return
	}
	agents, err := s.Store.ListAgents(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	sourceAgent, _, _ := s.agentForBot(r.Context(), source.BotID)
	allow := collaborationAllowlist(sourceAgent)
	visible := make([]map[string]string, 0, len(agents))
	for _, agent := range agents {
		if agent.Bot.ID == source.BotID {
			continue
		}
		if !allowsCollaborationTarget(allow, agent.Bot.ID) {
			continue
		}
		visible = append(visible, map[string]string{
			"id":    agent.Bot.ID,
			"name":  agent.Bot.Name,
			"title": agent.Bot.Title,
		})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"agents": visible})
}

func (s *Server) createCollaborationMessage(w http.ResponseWriter, r *http.Request) {
	s.createCollaborationTask(w, r, domain.HandoffModeMessage)
}

func (s *Server) createCollaborationDelegate(w http.ResponseWriter, r *http.Request) {
	s.createCollaborationTask(w, r, domain.HandoffModeDelegate)
}

func (s *Server) createCollaborationTask(w http.ResponseWriter, r *http.Request, mode string) {
	sourceRun, err := s.authorizedCollaborationRun(r)
	if err != nil {
		s.writeErrorStatus(w, http.StatusUnauthorized, err)
		return
	}
	var request collaborationTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	content := strings.TrimSpace(request.Content)
	if content == "" {
		content = strings.TrimSpace(request.Task)
	}
	if content == "" || strings.TrimSpace(request.AgentID) == "" {
		s.writeErrorStatus(w, http.StatusBadRequest, errors.New("agent_id and task content are required"))
		return
	}
	handoff, run, err := s.startAgentCollaboration(r.Context(), sourceRun, strings.TrimSpace(request.AgentID), content, mode)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, context.Canceled) {
			status = http.StatusConflict
		}
		s.writeErrorStatus(w, status, err)
		return
	}
	target, _ := s.Store.GetBot(r.Context(), handoff.TargetBotID)
	s.writeJSON(w, http.StatusAccepted, map[string]any{
		"id":         handoff.ID,
		"task_id":    handoff.ID,
		"agent_id":   handoff.TargetBotID,
		"agent_name": target.Name,
		"name":       target.Name,
		"task":       content,
		"content":    content,
		"status":     handoff.Status,
		"run":        run,
		"handoff":    handoff,
	})
}

func (s *Server) getCollaborationTask(w http.ResponseWriter, r *http.Request) {
	sourceRun, err := s.authorizedCollaborationRun(r)
	if err != nil {
		s.writeErrorStatus(w, http.StatusUnauthorized, err)
		return
	}
	taskID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/collaboration/tasks/"), "/")
	handoff, err := s.Store.GetHandoff(r.Context(), taskID)
	if err != nil {
		s.writeErrorStatus(w, http.StatusNotFound, err)
		return
	}
	if handoff.SourceBotID != sourceRun.BotID && handoff.TargetBotID != sourceRun.BotID {
		s.writeErrorStatus(w, http.StatusNotFound, errors.New("handoff not found"))
		return
	}
	target, _ := s.Store.GetBot(r.Context(), handoff.TargetBotID)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"id":         handoff.ID,
		"task_id":    handoff.ID,
		"agent_id":   handoff.TargetBotID,
		"agent_name": target.Name,
		"status":     handoff.Status,
		"content":    handoff.Content,
		"result":     handoff.Result,
	})
}

func (s *Server) cancelCollaborationTask(w http.ResponseWriter, r *http.Request) {
	sourceRun, err := s.authorizedCollaborationRun(r)
	if err != nil {
		s.writeErrorStatus(w, http.StatusUnauthorized, err)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/collaboration/tasks/")
	taskID := strings.TrimSuffix(strings.TrimSuffix(path, "/cancel"), "/")
	handoff, err := s.Store.GetHandoff(r.Context(), taskID)
	if err != nil {
		s.writeErrorStatus(w, http.StatusNotFound, err)
		return
	}
	if handoff.SourceBotID != sourceRun.BotID {
		s.writeErrorStatus(w, http.StatusNotFound, errors.New("handoff not found"))
		return
	}
	run, err := s.Store.GetRun(r.Context(), handoff.TargetRunID)
	if err != nil {
		s.writeErrorStatus(w, http.StatusConflict, err)
		return
	}
	s.activeMu.Lock()
	cancel := s.activeRuns[run.ID]
	s.activeMu.Unlock()
	if cancel != nil {
		_, _ = s.commitRunLifecycleEvent(r.Context(), run, "stopped", "", "run.stopped", `{"status":"stopped","reason":"collaboration_cancelled"}`)
		s.finishCollaborationHandoff(run, domain.HandoffStatusCancelled, "cancelled")
		cancel()
	} else {
		s.finishCollaborationHandoff(run, domain.HandoffStatusCancelled, "cancelled")
	}
	updated, _ := s.Store.GetHandoff(r.Context(), handoff.ID)
	s.writeJSON(w, http.StatusAccepted, map[string]any{"id": updated.ID, "status": updated.Status})
}

func (s *Server) startAgentCollaboration(ctx context.Context, sourceRun domain.Run, targetBotID, content, mode string) (domain.Handoff, domain.Run, error) {
	sourceAgent, hasSource, err := s.agentForBot(ctx, sourceRun.BotID)
	if err != nil {
		return domain.Handoff{}, domain.Run{}, err
	}
	if !hasSource {
		return domain.Handoff{}, domain.Run{}, errors.New("source agent not found")
	}
	var collaboration *domain.AgentCollaboration
	if sourceAgent.Metadata != nil {
		collaboration = sourceAgent.Metadata.Collaboration
	}
	var parent *domain.Handoff
	var chain []domain.Handoff
	if existing, parentErr := s.Store.GetHandoffByTargetRun(ctx, sourceRun.ID); parentErr == nil {
		parent = &existing
		if loaded, chainErr := s.Store.LoadHandoffChain(ctx, existing.ID); chainErr == nil {
			chain = loaded
		}
	}
	originRunID := sourceRun.ID
	if parent != nil && parent.OriginRunID != "" {
		originRunID = parent.OriginRunID
	}
	active, err := s.Store.CountActiveHandoffsForOriginRun(ctx, originRunID)
	if err != nil {
		return domain.Handoff{}, domain.Run{}, err
	}
	task, err := orchestration.ValidateAgentTask(orchestration.AgentTaskRequest{
		SourceBotID:   sourceRun.BotID,
		TargetBotID:   targetBotID,
		SourceRunID:   sourceRun.ID,
		Collaboration: collaboration,
		Parent:        parent,
		Chain:         chain,
		ActiveCount:   active,
	})
	if err != nil {
		return domain.Handoff{}, domain.Run{}, err
	}
	targetConversation, err := s.Store.CanonicalConversationForBot(ctx, targetBotID)
	if err != nil {
		return domain.Handoff{}, domain.Run{}, err
	}
	sourceConv, err := s.Store.GetConversation(ctx, sourceRun.ConversationID)
	if err != nil {
		return domain.Handoff{}, domain.Run{}, err
	}
	targetAgent, hasTarget, err := s.agentForBot(ctx, targetBotID)
	if err != nil {
		return domain.Handoff{}, domain.Run{}, err
	}
	provider, model, reasoningEffort, serviceTier, permissionMode, webSearch, timeoutSeconds, mcpIDs, err := s.leadRunSettings(ctx, targetAgent, hasTarget)
	if err != nil {
		return domain.Handoff{}, domain.Run{}, err
	}
	mcpServers, err := s.leadMCPServerSpecs(ctx, mcpIDs)
	if err != nil {
		return domain.Handoff{}, domain.Run{}, err
	}
	collabCapability, mcpServers, err := s.appendCollaborationMCP(ctx, mcpServers, targetAgent, hasTarget)
	if err != nil {
		return domain.Handoff{}, domain.Run{}, err
	}
	computerCapability := computerCapabilityFromMCPServers(mcpServers)
	sourceBot, err := s.Store.GetBot(ctx, sourceRun.BotID)
	if err != nil {
		return domain.Handoff{}, domain.Run{}, err
	}
	targetBot, err := s.Store.GetBot(ctx, targetBotID)
	if err != nil {
		return domain.Handoff{}, domain.Run{}, err
	}
	prompt := handoffWorkerPrompt(sourceBot, targetBot, content)
	memories, err := s.Store.RetrieveBotMemories(ctx, targetBotID, memoryPromptMaxCount, memoryPromptMaxBytes)
	if err != nil {
		return domain.Handoff{}, domain.Run{}, err
	}
	prompt = promptWithBotMemory(prompt, memories)
	if task.TimeoutSeconds > 0 {
		timeoutSeconds = uint32(task.TimeoutSeconds)
	}
	result, err := s.Store.CreateAgentHandoff(ctx, store.CreateAgentHandoffInput{
		SourceConversationID: sourceConv.ID,
		SourceBotID:          sourceRun.BotID,
		TargetBotID:          targetBotID,
		TargetConversationID: targetConversation.ID,
		Content:              content,
		TargetProvider:       provider,
		TargetPrompt:         prompt,
		Status:               domain.HandoffStatusQueued,
		Mode:                 mode,
		ParentHandoffID:      task.ParentHandoffID,
		Depth:                task.Depth,
		OriginRunID:          task.OriginRunID,
		SourceRunID:          sourceRun.ID,
		TimeoutSeconds:       task.TimeoutSeconds,
	})
	if err != nil {
		s.revokeComputerCapability(computerCapability)
		s.revokeCollabCapability(collabCapability)
		return domain.Handoff{}, domain.Run{}, err
	}
	run := result.Run
	if computerCapability != "" {
		leaseTTL := time.Duration(timeoutSeconds) * time.Second
		if leaseTTL <= 0 {
			leaseTTL = s.RunTimeout
		}
		s.bindComputerCapability(computerCapability, run.ID, leaseTTL)
		setComputerRunID(mcpServers, run.ID)
	}
	if collabCapability != "" {
		leaseTTL := time.Duration(timeoutSeconds) * time.Second
		if leaseTTL <= 0 {
			leaseTTL = s.RunTimeout
		}
		s.bindCollabCapability(collabCapability, run.ID, leaseTTL)
		setCollabRunID(mcpServers, run.ID)
	}
	s.publishHandoff(result.Handoff, "handoff.created")
	s.publishStoredRunEvent(run, result.QueuedEvent)
	systemPrompt := ""
	if hasTarget {
		systemPrompt = agentSystemPrompt(targetAgent.Bot)
	}
	if computerCapability != "" {
		systemPrompt = appendSystemPrompt(systemPrompt, computerBoundarySystemPrompt())
	}
	if !s.AllowHarnessExecution || s.harnessRunExecutor() == nil {
		s.revokeComputerCapability(computerCapability)
		s.revokeCollabCapability(collabCapability)
		return result.Handoff, run, nil
	}
	s.launchRun(run.ID, func(runContext context.Context) {
		s.executeRunWithContext(runContext, run, systemPrompt, model, reasoningEffort, serviceTier, permissionMode, webSearch, timeoutSeconds, mcpServers)
	})
	return result.Handoff, run, nil
}

func (s *Server) leadRunSettings(ctx context.Context, agent domain.Agent, hasAgent bool) (provider, model, reasoning, tier, permission, webSearch string, timeout uint32, mcpIDs []string, err error) {
	webSearch = domain.AgentWebSearchLive
	preferenceValues, prefErr := s.Store.GetPreferences(ctx)
	if prefErr != nil {
		return "", "", "", "", "", "", 0, nil, prefErr
	}
	hasConfiguredLead := hasAgent && agent.Metadata != nil && agent.Metadata.Lead != nil
	if hasConfiguredLead {
		lead := *agent.Metadata.Lead
		provider = configuredLeadProvider(lead.Harness)
		model = lead.Model
		reasoning = lead.Reasoning
		tier = lead.ServiceTier
		permission = lead.Permission
		webSearch = lead.WebSearch
		timeout = lead.TimeoutSeconds
	} else {
		normalized := preferenceValues.Normalize()
		provider = normalized.Workspace.Engine
		model = normalized.Workspace.Model
		if provider == "" {
			provider = "grok"
		}
	}
	if hasAgent && agent.Metadata != nil {
		mcpIDs = agent.Metadata.MCPIDs
	}
	return provider, model, reasoning, tier, permission, webSearch, timeout, mcpIDs, nil
}

func (s *Server) authorizedCollaborationRun(r *http.Request) (domain.Run, error) {
	runID := strings.TrimSpace(r.Header.Get(collaborationmcp.RunIDHeader))
	token := strings.TrimSpace(r.Header.Get(collaborationmcp.RunTokenHeader))
	if runID == "" || token == "" {
		return domain.Run{}, errors.New("collaboration run authorization required")
	}
	s.collabCapabilityMu.RLock()
	capability, ok := s.collabCapabilities[token]
	s.collabCapabilityMu.RUnlock()
	if !ok || capability.runID != runID || time.Now().UTC().After(capability.expiresAt) {
		return domain.Run{}, errors.New("collaboration run authorization required")
	}
	run, err := s.Store.GetRun(r.Context(), runID)
	if err != nil {
		return domain.Run{}, errors.New("collaboration run authorization required")
	}
	if terminalRunStatus(run.Status) {
		return domain.Run{}, errors.New("collaboration run is no longer active")
	}
	return run, nil
}

func (s *Server) bindCollabCapability(token, runID string, ttl ...time.Duration) {
	token = strings.TrimSpace(token)
	runID = strings.TrimSpace(runID)
	if token == "" || runID == "" {
		return
	}
	duration := defaultComputerCapabilityTTL
	if len(ttl) > 0 && ttl[0] > 0 {
		duration = ttl[0]
	}
	if duration > maxComputerCapabilityTTL {
		duration = maxComputerCapabilityTTL
	}
	s.collabCapabilityMu.Lock()
	defer s.collabCapabilityMu.Unlock()
	if s.collabCapabilities == nil {
		s.collabCapabilities = make(map[string]computerCapability)
	}
	s.collabCapabilities[token] = computerCapability{runID: runID, expiresAt: time.Now().UTC().Add(duration)}
}

func (s *Server) revokeCollabCapability(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	s.collabCapabilityMu.Lock()
	delete(s.collabCapabilities, token)
	s.collabCapabilityMu.Unlock()
}

func setCollabRunID(servers []harness.MCPServerSpec, runID string) {
	for index := range servers {
		if servers[index].Name == collaborationmcp.MCPServerName {
			if servers[index].Env == nil {
				servers[index].Env = map[string]string{}
			}
			servers[index].Env[collaborationmcp.RunIDEnv] = runID
		}
	}
}

func (s *Server) appendCollaborationMCP(ctx context.Context, specs []harness.MCPServerSpec, agent domain.Agent, hasAgent bool) (string, []harness.MCPServerSpec, error) {
	_ = ctx
	if !hasAgent || agent.Metadata == nil || agent.Metadata.Collaboration == nil || !agent.Metadata.Collaboration.Enabled {
		return "", specs, nil
	}
	spec, token, err := s.collaborationMCPServerSpec()
	if err != nil {
		return "", specs, err
	}
	return token, append(specs, spec), nil
}

func (s *Server) collaborationMCPServerSpec() (harness.MCPServerSpec, string, error) {
	token := strings.TrimSpace(s.RemoteToken)
	if token == "" {
		return harness.MCPServerSpec{}, "", errors.New("Agent collaboration MCP requires botd bearer authentication")
	}
	command := strings.TrimSpace(s.CollaborationMCPCommand)
	if command == "" {
		command = collaborationmcp.MCPServerCommand
		if executable, err := os.Executable(); err == nil {
			sibling := filepath.Join(filepath.Dir(executable), collaborationmcp.MCPServerCommand)
			if info, statErr := os.Stat(sibling); statErr == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
				command = sibling
			}
		}
	}
	resolved, err := exec.LookPath(command)
	if err != nil {
		return harness.MCPServerSpec{}, "", fmt.Errorf("Agent collaboration MCP bridge %q is unavailable: %w", command, err)
	}
	apiURL := strings.TrimSpace(s.CollaborationMCPAPIURL)
	if apiURL == "" {
		apiURL = collaborationmcp.DefaultAPIURL
	}
	parsed, err := url.Parse(apiURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return harness.MCPServerSpec{}, "", errors.New("Agent collaboration MCP API URL must be an absolute HTTP(S) URL")
	}
	host := strings.Trim(strings.ToLower(parsed.Hostname()), "[]")
	if host != "localhost" && !net.ParseIP(host).IsLoopback() {
		return harness.MCPServerSpec{}, "", errors.New("Agent collaboration MCP API URL must resolve to the local controller")
	}
	capability, err := newComputerCapability()
	if err != nil {
		return harness.MCPServerSpec{}, "", err
	}
	return harness.MCPServerSpec{
		Name:    collaborationmcp.MCPServerName,
		Command: resolved,
		Env: map[string]string{
			collaborationmcp.APIURLEnv:   strings.TrimRight(apiURL, "/"),
			collaborationmcp.APITokenEnv: token,
			collaborationmcp.RunTokenEnv: capability,
		},
	}, capability, nil
}

func (s *Server) finishCollaborationHandoff(run domain.Run, status, runError string) {
	if s.Store == nil {
		return
	}
	handoff, err := s.Store.GetHandoffByTargetRun(context.Background(), run.ID)
	if err != nil {
		return
	}
	handoffStatus := domain.HandoffStatusCompleted
	result := strings.TrimSpace(runError)
	switch status {
	case "failed":
		handoffStatus = domain.HandoffStatusFailed
		if result == "" {
			result = "failed"
		}
	case "stopped":
		handoffStatus = domain.HandoffStatusCancelled
		if result == "" {
			result = "cancelled"
		}
	case "blocked":
		handoffStatus = domain.HandoffStatusFailed
		if result == "" {
			result = "blocked"
		}
	default:
		if messages, listErr := s.Store.ListMessages(context.Background(), run.ConversationID); listErr == nil {
			for i := len(messages) - 1; i >= 0; i-- {
				if messages[i].Role == "assistant" && messages[i].Kind != domain.MessageKindHandoff {
					result = messages[i].Content
					break
				}
			}
		}
		if result == "" {
			result = "done"
		}
	}
	_ = s.Store.UpdateHandoffStatus(context.Background(), handoff.ID, handoffStatus, result)
	target, _ := s.Store.GetBot(context.Background(), handoff.TargetBotID)
	summary := fmt.Sprintf("%s: %s", target.Name, result)
	if _, err := s.Store.AppendHandoffResultToSource(context.Background(), handoff.ID, summary); err == nil {
		updated, _ := s.Store.GetHandoff(context.Background(), handoff.ID)
		s.publishHandoff(updated, "handoff.completed")
	}
}

func (s *Server) publishHandoff(handoff domain.Handoff, eventType string) {
	if s.Broker == nil {
		return
	}
	payload, _ := json.Marshal(handoff)
	for _, conversationID := range []string{handoff.SourceConversationID, handoff.TargetConversationID} {
		s.Broker.Publish(domain.StreamEvent{
			ConversationID: conversationID,
			Type:           eventType,
			Data:           string(payload),
			CreatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		})
	}
}

func collaborationAllowlist(agent domain.Agent) []string {
	if agent.Metadata == nil || agent.Metadata.Collaboration == nil {
		return nil
	}
	return agent.Metadata.Collaboration.AllowAgentIDs
}

func allowsCollaborationTarget(allow []string, targetBotID string) bool {
	if len(allow) == 0 {
		return true
	}
	for _, id := range allow {
		if strings.TrimSpace(id) == targetBotID {
			return true
		}
	}
	return false
}
