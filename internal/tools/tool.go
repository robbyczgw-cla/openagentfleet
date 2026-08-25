// Package tools is the canonical OpenAgentFleet tool model. MCP, Claude, and
// Codex shapes are adapters over this registry; they do not own tool identity
// or permission checks.
package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrUnknownTool       = errors.New("unknown tool")
	ErrCapabilityDenied  = errors.New("tool capability denied")
	ErrInvalidInput      = errors.New("invalid tool input")
	ErrExecuteUnbound    = errors.New("tool has no bound executor")
	ErrInvalidTool       = errors.New("invalid tool")
	ErrAlreadyRegistered = errors.New("tool already registered")
)

// Tool is one logical OpenAgentFleet tool. Name is the canonical dotted name.
// Aliases keep the original MCP underscore names pointing at the same tool.
// Execute is optional; nil means definition-only.
type Tool struct {
	Name                 string
	Description          string
	InputSchema          json.RawMessage
	RequiredCapabilities []string
	Aliases              []string
	Execute              func(ctx ExecutionContext, input json.RawMessage) (Result, error)
}

// ExecutionContext is the grant and identity snapshot for one tool call.
// Registry.Execute authorizes only against GrantedCapabilities.
type ExecutionContext struct {
	AgentID             string
	TurnID              string
	EngineID            string
	ComputerID          string
	GrantedCapabilities []string
}

// Result is the canonical tool outcome. Adapters map it onto transport content
// blocks; they do not add policy.
type Result struct {
	Content string
	IsError bool
}

func cloneTool(tool Tool) Tool {
	cloned := tool
	cloned.InputSchema = cloneRaw(tool.InputSchema)
	cloned.RequiredCapabilities = cloneStrings(tool.RequiredCapabilities)
	cloned.Aliases = cloneStrings(tool.Aliases)
	return cloned
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}

func normalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%w: name is required", ErrInvalidTool)
	}
	if strings.ContainsAny(name, " \t\r\n") {
		return "", fmt.Errorf("%w: name %q must not contain whitespace", ErrInvalidTool, name)
	}
	return name, nil
}

func validateTool(tool Tool) (Tool, error) {
	name, err := normalizeName(tool.Name)
	if err != nil {
		return Tool{}, err
	}
	tool.Name = name
	tool.Description = strings.TrimSpace(tool.Description)
	tool.InputSchema = cloneRaw(tool.InputSchema)
	if len(tool.InputSchema) > 0 && !json.Valid(tool.InputSchema) {
		return Tool{}, fmt.Errorf("%w: input schema is not valid JSON", ErrInvalidTool)
	}

	seenCaps := make(map[string]struct{}, len(tool.RequiredCapabilities))
	required := make([]string, 0, len(tool.RequiredCapabilities))
	for _, capability := range tool.RequiredCapabilities {
		capability = strings.TrimSpace(capability)
		if capability == "" {
			return Tool{}, fmt.Errorf("%w: required capability must not be empty", ErrInvalidTool)
		}
		if _, exists := seenCaps[capability]; exists {
			continue
		}
		seenCaps[capability] = struct{}{}
		required = append(required, capability)
	}
	tool.RequiredCapabilities = required

	seenNames := map[string]struct{}{name: {}}
	aliases := make([]string, 0, len(tool.Aliases))
	for _, alias := range tool.Aliases {
		alias, err = normalizeName(alias)
		if err != nil {
			return Tool{}, err
		}
		if _, exists := seenNames[alias]; exists {
			continue
		}
		seenNames[alias] = struct{}{}
		aliases = append(aliases, alias)
	}
	tool.Aliases = aliases
	return tool, nil
}
