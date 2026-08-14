package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

type agentRequest struct {
	Name              string                `json:"name"`
	Title             string                `json:"title"`
	Description       string                `json:"description"`
	ConversationTitle string                `json:"conversation_title"`
	Metadata          *agentMetadataRequest `json:"metadata"`
}

type agentPatchRequest struct {
	Name        *string                    `json:"name"`
	Title       *string                    `json:"title"`
	Description *string                    `json:"description"`
	Metadata    *agentMetadataPatchRequest `json:"metadata"`
}

type agentMetadataRequest struct {
	Lead             *domain.AgentExecutionProfile  `json:"lead"`
	Workers          []domain.AgentExecutionProfile `json:"workers"`
	LeadHarness      string                         `json:"lead_harness"`
	Model            string                         `json:"model"`
	Orchestrator     string                         `json:"orchestrator"`
	WorkerIDs        []string                       `json:"worker_ids"`
	PluginIDs        []string                       `json:"plugin_ids"`
	MCPIDs           []string                       `json:"mcp_ids"`
	NotifyFinished   *bool                          `json:"notify_finished"`
	NotifyNeedsInput *bool                          `json:"notify_needs_input"`
	Avatar           *domain.AgentAvatarMetadata    `json:"avatar"`
}

type agentMetadataPatchRequest struct {
	Lead             *agentExecutionProfilePatch     `json:"lead"`
	Workers          *[]domain.AgentExecutionProfile `json:"workers"`
	LeadHarness      *string                         `json:"lead_harness"`
	Model            *string                         `json:"model"`
	Orchestrator     *string                         `json:"orchestrator"`
	WorkerIDs        *[]string                       `json:"worker_ids"`
	PluginIDs        *[]string                       `json:"plugin_ids"`
	MCPIDs           *[]string                       `json:"mcp_ids"`
	NotifyFinished   *bool                           `json:"notify_finished"`
	NotifyNeedsInput *bool                           `json:"notify_needs_input"`
	Avatar           *domain.AgentAvatarMetadata     `json:"avatar"`
}

type agentExecutionProfilePatch struct {
	ID             *string `json:"id"`
	Harness        *string `json:"harness"`
	Model          *string `json:"model"`
	Reasoning      *string `json:"reasoning"`
	ServiceTier    *string `json:"service_tier"`
	Permission     *string `json:"permission"`
	WebSearch      *string `json:"web_search"`
	MaxTurns       *uint16 `json:"max_turns"`
	TimeoutSeconds *uint32 `json:"timeout_seconds"`
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("agent store unavailable"))
		return
	}
	var request agentRequest
	if err := decodeStrictJSON(w, r, &request); err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	draft, err := domain.NormalizeAgentDraft(domain.AgentDraft{
		Name: request.Name, Title: request.Title, Description: request.Description, ConversationTitle: request.ConversationTitle,
	})
	if err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	metadata, err := normalizeAgentMetadataRequest(request.Metadata)
	if err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	agent, err := s.Store.CreateAgentWithMetadata(r.Context(), draft, metadata)
	if err != nil {
		s.writeErrorStatus(w, http.StatusConflict, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, agent)
}

func (s *Server) patchAgent(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("agent store unavailable"))
		return
	}
	botID := strings.TrimPrefix(r.URL.Path, "/api/agents/")
	if botID == "" || strings.Contains(botID, "/") {
		s.writeErrorStatus(w, http.StatusBadRequest, errors.New("agent id is required"))
		return
	}
	var request agentPatchRequest
	if err := decodeStrictJSON(w, r, &request); err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	if request.Name == nil && request.Title == nil && request.Description == nil && request.Metadata == nil {
		s.writeErrorStatus(w, http.StatusBadRequest, errors.New("at least one agent field is required"))
		return
	}
	profileUpdate := domain.AgentProfileUpdate{Name: request.Name, Title: request.Title, Description: request.Description}
	var agent domain.Agent
	var err error
	if request.Metadata != nil {
		agent, err = s.Store.PatchAgent(r.Context(), botID, profileUpdate, func(existing domain.AgentMetadata) (domain.AgentMetadata, error) {
			return normalizeAgentMetadataPatch(&existing, request.Metadata)
		})
	} else {
		agent, err = s.Store.UpdateAgentProfile(r.Context(), botID, profileUpdate)
	}
	if err != nil {
		if errors.Is(err, store.ErrAgentNotFound) {
			s.writeErrorStatus(w, http.StatusNotFound, err)
			return
		}
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	s.writeJSON(w, http.StatusOK, agent)
}

func normalizeAgentMetadataPatch(existing *domain.AgentMetadata, patch *agentMetadataPatchRequest) (domain.AgentMetadata, error) {
	if patch == nil {
		return domain.AgentMetadata{}, errors.New("agent metadata patch is required")
	}
	metadata := domain.DefaultAgentMetadata()
	if existing != nil {
		metadata = *existing
	}
	changed := false
	if patch.Lead != nil {
		lead := domain.AgentExecutionProfile{}
		if metadata.Lead != nil {
			lead = *metadata.Lead
		}
		if patch.Lead.ID != nil {
			lead.ID = *patch.Lead.ID
		}
		if patch.Lead.Harness != nil {
			lead.Harness = *patch.Lead.Harness
		}
		if patch.Lead.Model != nil {
			lead.Model = *patch.Lead.Model
		}
		if patch.Lead.Reasoning != nil {
			lead.Reasoning = *patch.Lead.Reasoning
		}
		if patch.Lead.ServiceTier != nil {
			lead.ServiceTier = *patch.Lead.ServiceTier
		}
		if patch.Lead.Permission != nil {
			lead.Permission = *patch.Lead.Permission
		}
		if patch.Lead.WebSearch != nil {
			lead.WebSearch = *patch.Lead.WebSearch
		}
		if patch.Lead.MaxTurns != nil {
			lead.MaxTurns = *patch.Lead.MaxTurns
		}
		if patch.Lead.TimeoutSeconds != nil {
			lead.TimeoutSeconds = *patch.Lead.TimeoutSeconds
		}
		metadata.Lead = &lead
		changed = true
	}
	if patch.Workers != nil {
		if err := validateRequestedWorkerBounds(*patch.Workers); err != nil {
			return domain.AgentMetadata{}, err
		}
		metadata.Workers = *patch.Workers
		changed = true
	}
	if patch.LeadHarness != nil {
		metadata.LeadHarness = *patch.LeadHarness
		if metadata.Lead != nil {
			lead := *metadata.Lead
			lead.Harness = *patch.LeadHarness
			metadata.Lead = &lead
		}
		changed = true
	}
	if patch.Model != nil {
		metadata.Model = *patch.Model
		if metadata.Lead != nil {
			lead := *metadata.Lead
			lead.Model = *patch.Model
			metadata.Lead = &lead
		}
		changed = true
	}
	if patch.Orchestrator != nil {
		metadata.Orchestrator = *patch.Orchestrator
		changed = true
	}
	if patch.WorkerIDs != nil {
		metadata.WorkerIDs = *patch.WorkerIDs
		changed = true
	}
	if patch.PluginIDs != nil {
		metadata.PluginIDs = *patch.PluginIDs
		changed = true
	}
	if patch.MCPIDs != nil {
		metadata.MCPIDs = *patch.MCPIDs
		changed = true
	}
	if patch.NotifyFinished != nil {
		metadata.NotifyFinished = *patch.NotifyFinished
		changed = true
	}
	if patch.NotifyNeedsInput != nil {
		metadata.NotifyNeedsInput = *patch.NotifyNeedsInput
		changed = true
	}
	if patch.Avatar != nil {
		metadata.Avatar = patch.Avatar
		changed = true
	}
	if !changed {
		return domain.AgentMetadata{}, errors.New("at least one agent metadata field is required")
	}
	return domain.NormalizeAgentMetadata(metadata)
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("agent store unavailable"))
		return
	}
	agents, err := s.Store.ListAgents(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

func normalizeAgentMetadataRequest(request *agentMetadataRequest) (domain.AgentMetadata, error) {
	metadata := domain.DefaultAgentMetadata()
	if request == nil {
		return metadata, nil
	}
	if err := validateRequestedWorkerBounds(request.Workers); err != nil {
		return domain.AgentMetadata{}, err
	}
	metadata.LeadHarness = request.LeadHarness
	metadata.Model = request.Model
	metadata.Lead = request.Lead
	metadata.Workers = request.Workers
	metadata.Orchestrator = request.Orchestrator
	metadata.WorkerIDs = request.WorkerIDs
	metadata.PluginIDs = request.PluginIDs
	metadata.MCPIDs = request.MCPIDs
	metadata.Avatar = request.Avatar
	if request.NotifyFinished != nil {
		metadata.NotifyFinished = *request.NotifyFinished
	}
	if request.NotifyNeedsInput != nil {
		metadata.NotifyNeedsInput = *request.NotifyNeedsInput
	}
	return domain.NormalizeAgentMetadata(metadata)
}

func validateRequestedWorkerBounds(workers []domain.AgentExecutionProfile) error {
	for index, worker := range workers {
		if worker.MaxTurns == 0 {
			return errors.New("workers[" + strconv.Itoa(index) + "]: max_turns must be between 1 and 100")
		}
		if worker.TimeoutSeconds == 0 {
			return errors.New("workers[" + strconv.Itoa(index) + "]: timeout_seconds must be between 30 and 3600")
		}
	}
	return nil
}
