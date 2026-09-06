package domain

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	ProjectNameMaxBytes  = 160
	ProjectBriefMaxBytes = 64 << 10
	ProjectMembersMax    = 128
)

type ProjectStatus string

const (
	ProjectStatusActive   ProjectStatus = "active"
	ProjectStatusArchived ProjectStatus = "archived"
)

type ProjectSubjectType string

const (
	ProjectSubjectConversation ProjectSubjectType = "conversation"
	ProjectSubjectRoutine      ProjectSubjectType = "routine"
)

type ProjectDraft struct {
	Name     string   `json:"name"`
	Brief    string   `json:"brief"`
	AgentIDs []string `json:"agent_ids"`
}

type ProjectUpdate struct {
	Name            string   `json:"name"`
	Brief           string   `json:"brief"`
	AgentIDs        []string `json:"agent_ids"`
	ExpectedVersion int64    `json:"expected_version"`
}

type Project struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Brief         string          `json:"brief"`
	Status        ProjectStatus   `json:"status"`
	Version       int64           `json:"version"`
	BriefRevision int64           `json:"brief_revision"`
	Members       []ProjectMember `json:"members"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
	ArchivedAt    string          `json:"archived_at,omitempty"`
}

type ProjectMember struct {
	AgentID string `json:"agent_id"`
	Name    string `json:"name"`
	Title   string `json:"title"`
}

type ProjectBriefRevision struct {
	ProjectID string `json:"project_id"`
	Revision  int64  `json:"revision"`
	Brief     string `json:"brief"`
	CreatedAt string `json:"created_at"`
}

type ProjectAssociation struct {
	ProjectID   string             `json:"project_id"`
	ProjectName string             `json:"project_name"`
	SubjectType ProjectSubjectType `json:"subject_type"`
	SubjectID   string             `json:"subject_id"`
	CreatedAt   string             `json:"created_at"`
	UpdatedAt   string             `json:"updated_at"`
}

// ProjectTaskSnapshot is the only project content supplied to a run. It is
// copied at queue time and does not follow later project edits.
type ProjectTaskSnapshot struct {
	RunID         string `json:"run_id"`
	ProjectID     string `json:"project_id"`
	ProjectName   string `json:"project_name"`
	BriefRevision int64  `json:"brief_revision"`
	Brief         string `json:"brief,omitempty"`
	CreatedAt     string `json:"created_at"`
}

func NormalizeProjectDraft(value ProjectDraft) (ProjectDraft, error) {
	value.Name = strings.TrimSpace(value.Name)
	if value.Name == "" {
		return ProjectDraft{}, errors.New("project name is required")
	}
	if !utf8.ValidString(value.Name) || len(value.Name) > ProjectNameMaxBytes || strings.ContainsRune(value.Name, '\x00') {
		return ProjectDraft{}, fmt.Errorf("project name must be valid text no longer than %d bytes", ProjectNameMaxBytes)
	}
	if !utf8.ValidString(value.Brief) || len(value.Brief) > ProjectBriefMaxBytes || strings.ContainsRune(value.Brief, '\x00') {
		return ProjectDraft{}, fmt.Errorf("project brief must be valid text no longer than %d bytes", ProjectBriefMaxBytes)
	}
	if len(value.AgentIDs) == 0 {
		return ProjectDraft{}, errors.New("project requires at least one agent")
	}
	if len(value.AgentIDs) > ProjectMembersMax {
		return ProjectDraft{}, fmt.Errorf("project may have at most %d agents", ProjectMembersMax)
	}
	seen := make(map[string]struct{}, len(value.AgentIDs))
	ids := make([]string, 0, len(value.AgentIDs))
	for _, raw := range value.AgentIDs {
		agentID := strings.TrimSpace(raw)
		if agentID == "" || strings.ContainsRune(agentID, '\x00') {
			return ProjectDraft{}, errors.New("project agent id is required")
		}
		if _, exists := seen[agentID]; exists {
			return ProjectDraft{}, fmt.Errorf("duplicate project agent %q", agentID)
		}
		seen[agentID] = struct{}{}
		ids = append(ids, agentID)
	}
	value.AgentIDs = ids
	return value, nil
}

func ValidProjectSubjectType(value ProjectSubjectType) bool {
	return value == ProjectSubjectConversation || value == ProjectSubjectRoutine
}
