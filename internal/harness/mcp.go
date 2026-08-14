package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const (
	maxMCPServers          = 16
	maxMCPServerNameBytes  = 64
	maxMCPCommandBytes     = 4 * 1024
	maxMCPArguments        = 128
	maxMCPArgumentBytes    = 8 * 1024
	maxMCPEnvironment      = 128
	maxMCPEnvNameBytes     = 256
	maxMCPEnvValueBytes    = 32 * 1024
	maxMCPInputBytes       = 64 * 1024
	maxOpenCodeConfigBytes = 512 * 1024
)

// MCPServerSpec is a credential-free, per-run stdio MCP server description.
// It deliberately has no persistence, authentication, or global-config fields.
type MCPServerSpec struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
}

type acpMCPServer struct {
	Name    string           `json:"name"`
	Command string           `json:"command"`
	Args    []string         `json:"args"`
	Env     []acpEnvironment `json:"env"`
}

type acpEnvironment struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type codexMCPServer struct {
	Command                  string            `json:"command"`
	Args                     []string          `json:"args"`
	Env                      map[string]string `json:"env"`
	Enabled                  bool              `json:"enabled"`
	Required                 bool              `json:"required"`
	DefaultToolsApprovalMode string            `json:"default_tools_approval_mode"`
}

type openCodeMCPConfig struct {
	MCP        map[string]openCodeMCPServer `json:"mcp,omitempty"`
	Permission map[string]string            `json:"permission,omitempty"`
}

type openCodeMCPServer struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command"`
	Environment map[string]string `json:"environment"`
	Enabled     bool              `json:"enabled"`
}

func normalizeMCPServers(input []MCPServerSpec) ([]MCPServerSpec, error) {
	if len(input) == 0 {
		return nil, nil
	}
	if len(input) > maxMCPServers {
		return nil, fmt.Errorf("MCP server count %d exceeds limit %d", len(input), maxMCPServers)
	}

	servers := make([]MCPServerSpec, len(input))
	seen := make(map[string]struct{}, len(input))
	totalBytes := 0
	for index, server := range input {
		if !safeMCPServerName(server.Name) {
			return nil, fmt.Errorf("MCP server %d name %q must be 1-%d ASCII letters, digits, underscores, or hyphens", index, server.Name, maxMCPServerNameBytes)
		}
		if _, exists := seen[server.Name]; exists {
			return nil, fmt.Errorf("MCP server name %q is duplicated", server.Name)
		}
		seen[server.Name] = struct{}{}
		if strings.TrimSpace(server.Command) == "" {
			return nil, fmt.Errorf("MCP server %q command is required", server.Name)
		}
		if len(server.Command) > maxMCPCommandBytes {
			return nil, fmt.Errorf("MCP server %q command exceeds %d bytes", server.Name, maxMCPCommandBytes)
		}
		for _, character := range server.Command {
			if unicode.IsControl(character) {
				return nil, fmt.Errorf("MCP server %q command contains a control character", server.Name)
			}
		}
		if len(server.Args) > maxMCPArguments {
			return nil, fmt.Errorf("MCP server %q argument count exceeds %d", server.Name, maxMCPArguments)
		}
		if len(server.Env) > maxMCPEnvironment {
			return nil, fmt.Errorf("MCP server %q environment count exceeds %d", server.Name, maxMCPEnvironment)
		}

		copied := MCPServerSpec{
			Name:    server.Name,
			Command: server.Command,
			Args:    make([]string, len(server.Args)),
			Env:     make(map[string]string, len(server.Env)),
		}
		copy(copied.Args, server.Args)
		totalBytes += len(server.Name) + len(server.Command)
		for argumentIndex, argument := range server.Args {
			if len(argument) > maxMCPArgumentBytes {
				return nil, fmt.Errorf("MCP server %q argument %d exceeds %d bytes", server.Name, argumentIndex, maxMCPArgumentBytes)
			}
			if strings.ContainsRune(argument, '\x00') {
				return nil, fmt.Errorf("MCP server %q argument %d contains NUL", server.Name, argumentIndex)
			}
			totalBytes += len(argument)
		}
		for name, value := range server.Env {
			if !safeMCPEnvironmentName(name) {
				return nil, fmt.Errorf("MCP server %q environment name %q must be a portable ASCII identifier", server.Name, name)
			}
			if len(name) > maxMCPEnvNameBytes {
				return nil, fmt.Errorf("MCP server %q environment name exceeds %d bytes", server.Name, maxMCPEnvNameBytes)
			}
			if len(value) > maxMCPEnvValueBytes {
				return nil, fmt.Errorf("MCP server %q environment value for %q exceeds %d bytes", server.Name, name, maxMCPEnvValueBytes)
			}
			if strings.ContainsRune(name, '\x00') || strings.ContainsRune(value, '\x00') {
				return nil, fmt.Errorf("MCP server %q environment entry %q contains NUL", server.Name, name)
			}
			copied.Env[name] = value
			totalBytes += len(name) + len(value)
		}
		if totalBytes > maxMCPInputBytes {
			return nil, fmt.Errorf("MCP server input exceeds %d bytes", maxMCPInputBytes)
		}
		servers[index] = copied
	}
	return servers, nil
}

func safeMCPServerName(name string) bool {
	if len(name) == 0 || len(name) > maxMCPServerNameBytes {
		return false
	}
	for _, character := range []byte(name) {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func safeMCPEnvironmentName(name string) bool {
	if len(name) == 0 || len(name) > maxMCPEnvNameBytes {
		return false
	}
	for index, character := range []byte(name) {
		if (character >= 'A' && character <= 'Z') ||
			(character >= 'a' && character <= 'z') ||
			character == '_' ||
			(index > 0 && character >= '0' && character <= '9') {
			continue
		}
		return false
	}
	return true
}

func acpMCPServers(servers []MCPServerSpec) []acpMCPServer {
	result := make([]acpMCPServer, 0, len(servers))
	for _, server := range servers {
		names := make([]string, 0, len(server.Env))
		for name := range server.Env {
			names = append(names, name)
		}
		sort.Strings(names)
		environment := make([]acpEnvironment, 0, len(names))
		for _, name := range names {
			environment = append(environment, acpEnvironment{Name: name, Value: server.Env[name]})
		}
		result = append(result, acpMCPServer{
			Name:    server.Name,
			Command: server.Command,
			Args:    append([]string{}, server.Args...),
			Env:     environment,
		})
	}
	return result
}

func mergeCodexMCPServers(config map[string]any, servers []MCPServerSpec) (map[string]any, error) {
	result := cloneStringAnyMap(config)
	if len(servers) == 0 {
		return result, nil
	}

	mcpServers := make(map[string]any, len(servers))
	if existing, exists := result["mcp_servers"]; exists && existing != nil {
		var ok bool
		mcpServers, ok = existing.(map[string]any)
		if !ok {
			return nil, errors.New("Codex config mcp_servers must be an object")
		}
	}
	for _, server := range servers {
		mcpServers[server.Name] = codexMCPServer{
			Command:                  server.Command,
			Args:                     append([]string{}, server.Args...),
			Env:                      cloneStringMap(server.Env),
			Enabled:                  true,
			Required:                 false,
			DefaultToolsApprovalMode: "prompt",
		}
	}
	result["mcp_servers"] = mcpServers
	return result, nil
}

func marshalOpenCodeMCPConfig(servers []MCPServerSpec) (string, error) {
	return marshalOpenCodeRunConfig(servers, "")
}

func marshalOpenCodeRunConfig(servers []MCPServerSpec, webSearch string) (string, error) {
	config := openCodeMCPConfig{MCP: make(map[string]openCodeMCPServer, len(servers))}
	for _, server := range servers {
		command := make([]string, 1, len(server.Args)+1)
		command[0] = server.Command
		command = append(command, server.Args...)
		config.MCP[server.Name] = openCodeMCPServer{
			Type:        "local",
			Command:     command,
			Environment: cloneStringMap(server.Env),
			Enabled:     true,
		}
	}
	if webSearch == "disabled" {
		config.Permission = map[string]string{"webfetch": "deny", "websearch": "deny"}
	} else if webSearch != "" && webSearch != "live" {
		return "", fmt.Errorf("unsupported OpenCode web search mode %q", webSearch)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("encode OpenCode MCP config: %w", err)
	}
	if len(encoded) > maxOpenCodeConfigBytes {
		return "", fmt.Errorf("OpenCode MCP config exceeds %d bytes", maxOpenCodeConfigBytes)
	}
	return string(encoded), nil
}

func cloneStringAnyMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = cloneConfigValue(value)
	}
	return result
}

func cloneConfigValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneStringAnyMap(typed)
	case map[string]string:
		return cloneStringMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneConfigValue(item)
		}
		return result
	case []string:
		return append([]string{}, typed...)
	default:
		return value
	}
}

func cloneStringMap(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
