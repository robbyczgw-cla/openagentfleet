package domain

import (
	"fmt"
	"strings"
)

const (
	AgentComputerBackendNative = "native"
	AgentComputerBackendDocker = "docker"
	AgentComputerBackendRemote = "remote"

	DefaultAgentComputerID      = "workspace"
	DefaultAgentComputerBackend = AgentComputerBackendDocker

	MaxAgentComputerWorkspaceBytes = 4 << 10
)

// AgentComputer is an Agent's durable computer binding. Lead and engine
// selection do not rewrite it.
type AgentComputer struct {
	ID        string `json:"id,omitempty"`
	Backend   string `json:"backend,omitempty"` // native|docker|remote
	Workspace string `json:"workspace,omitempty"`
}

func DefaultAgentComputer() AgentComputer {
	return AgentComputer{ID: DefaultAgentComputerID, Backend: DefaultAgentComputerBackend}
}

func NormalizeAgentComputer(value AgentComputer) (AgentComputer, error) {
	var err error
	if value.ID, err = normalizeAgentMetadataIdentifier("computer id", value.ID, false); err != nil {
		return AgentComputer{}, err
	}
	if value.ID == "" {
		value.ID = DefaultAgentComputerID
	}
	if value.Backend, err = normalizeAgentMetadataIdentifier("computer backend", value.Backend, false); err != nil {
		return AgentComputer{}, err
	}
	if value.Backend == "" {
		value.Backend = DefaultAgentComputerBackend
	}
	switch value.Backend {
	case AgentComputerBackendNative, AgentComputerBackendDocker, AgentComputerBackendRemote:
	default:
		return AgentComputer{}, fmt.Errorf("computer backend %q is not supported", value.Backend)
	}
	value.Workspace = strings.TrimSpace(value.Workspace)
	if err := validateAgentText("computer workspace", value.Workspace, MaxAgentComputerWorkspaceBytes, false); err != nil {
		return AgentComputer{}, err
	}
	return value, nil
}

// EffectiveComputer returns the Agent's logical computer, defaulting to the
// shared workspace Docker computer when none is stored.
func (value AgentMetadata) EffectiveComputer() AgentComputer {
	if value.Computer == nil {
		return DefaultAgentComputer()
	}
	return *value.Computer
}
