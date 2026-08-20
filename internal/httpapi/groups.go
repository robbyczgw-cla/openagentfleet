package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/robbyczgw-cla/openagentfleet/internal/orchestration"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

type groupRequest struct {
	Title    string   `json:"title"`
	AgentIDs []string `json:"agent_ids"`
}

type groupMessageRequest struct {
	Content       string   `json:"content"`
	MentionBotIDs []string `json:"mention_bot_ids"`
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("agent store unavailable"))
		return
	}
	var request groupRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	group, err := s.Store.CreateGroup(r.Context(), request.Title, request.AgentIDs)
	if err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"group": group})
}

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("agent store unavailable"))
		return
	}
	groups, err := s.Store.ListGroups(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

func (s *Server) getGroup(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("agent store unavailable"))
		return
	}
	groupID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/groups/"), "/")
	if strings.Contains(groupID, "/") {
		s.writeErrorStatus(w, http.StatusNotFound, errors.New("group not found"))
		return
	}
	group, err := s.Store.GetGroup(r.Context(), groupID)
	if errors.Is(err, store.ErrGroupNotFound) {
		s.writeErrorStatus(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"group": group})
}

func (s *Server) listGroupMessages(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("agent store unavailable"))
		return
	}
	groupID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/groups/"), "/messages")
	groupID = strings.Trim(groupID, "/")
	messages, err := s.Store.ListGroupMessages(r.Context(), groupID)
	if errors.Is(err, store.ErrGroupNotFound) {
		s.writeErrorStatus(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.writeError(w, err)
		return
	}
	runs, err := s.Store.ListGroupRuns(r.Context(), groupID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"messages": messages, "runs": runs})
}

func (s *Server) createGroupMessage(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("agent store unavailable"))
		return
	}
	groupID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/groups/"), "/messages")
	groupID = strings.Trim(groupID, "/")
	var request groupMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	group, err := s.Store.GetGroup(r.Context(), groupID)
	if errors.Is(err, store.ErrGroupNotFound) {
		s.writeErrorStatus(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.writeError(w, err)
		return
	}
	members := make([]orchestration.GroupMemberProfile, 0, len(group.Members))
	for _, member := range group.Members {
		bot, botErr := s.Store.GetBot(r.Context(), member.BotID)
		if botErr != nil {
			s.writeError(w, botErr)
			return
		}
		members = append(members, orchestration.GroupMemberProfile{Bot: bot})
	}
	recent, err := s.Store.ListGroupMessages(r.Context(), groupID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	if _, err := orchestration.RouteUserGroupMentions(members, request.MentionBotIDs, recent, request.Content); err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.Store.CreateGroupMessage(r.Context(), store.CreateGroupMessageInput{
		GroupID:       groupID,
		Role:          "user",
		Content:       request.Content,
		MentionBotIDs: request.MentionBotIDs,
	})
	if errors.Is(err, store.ErrGroupMemberMissing) {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	s.launchGroupRuns(result.Runs)
	s.writeJSON(w, http.StatusAccepted, map[string]any{"message": result.Message, "runs": result.Runs})
}

func (s *Server) handleGroupRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/groups")
	switch {
	case path == "" || path == "/":
		if r.Method == http.MethodGet {
			s.listGroups(w, r)
			return
		}
		if r.Method == http.MethodPost {
			s.createGroup(w, r)
			return
		}
	case strings.HasSuffix(path, "/messages") && r.Method == http.MethodGet:
		s.listGroupMessages(w, r)
		return
	case strings.HasSuffix(path, "/messages") && r.Method == http.MethodPost:
		s.createGroupMessage(w, r)
		return
	case r.Method == http.MethodGet && strings.Count(strings.Trim(path, "/"), "/") == 0:
		s.getGroup(w, r)
		return
	}
	s.writeErrorStatus(w, http.StatusNotFound, errors.New("unknown group endpoint"))
}
