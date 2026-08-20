// Package collaborationmcp exposes OpenAgentFleet Agent collaboration over the
// Model Context Protocol (MCP). It talks only to botd's local HTTP API, so
// orchestration and run policy stay enforced by OpenAgentFleet itself.
package collaborationmcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultAPIURL       = "http://127.0.0.1:4317"
	defaultProtocol     = "2025-11-25"
	maxJSONResponseSize = 2 << 20
)

// These names form the controller-owned stdio MCP contract. The HTTP API
// injects this server for a lead run that may collaborate with other Agents;
// the bridge still relies on botd's authenticated, server-side gate.
const (
	MCPServerName    = "openagentfleet-collaboration-mcp"
	MCPServerCommand = "openagentfleet-collaboration-mcp"
	APIURLEnv        = "OPENAGENTFLEET_API_URL"
	APITokenEnv      = "OPENAGENTFLEET_API_TOKEN"
	RunIDEnv         = "OPENAGENTFLEET_COLLAB_RUN_ID"
	RunTokenEnv      = "OPENAGENTFLEET_COLLAB_RUN_TOKEN"
	RunIDHeader      = "X-OpenAgentFleet-Collab-Run-ID"
	RunTokenHeader   = "X-OpenAgentFleet-Collab-Run-Token"
	DefaultAPIURL    = defaultAPIURL
)

// Config controls how the MCP server reaches botd. APIURL must be loopback
// HTTP(S). APIToken, RunID, and RunToken are required and are sent on every
// botd request; they are never returned in tool results.
type Config struct {
	APIURL     string
	APIToken   string
	RunID      string
	RunToken   string
	HTTPClient *http.Client
}

// Server is a synchronous stdio MCP server. It processes one JSON-RPC request
// per input line, which keeps stdout strictly line-delimited JSON-RPC.
type Server struct {
	apiURL   string
	token    string
	runID    string
	runToken string
	client   *http.Client
}

// New creates a collaboration MCP bridge with a bounded HTTP client.
func New(config Config) (*Server, error) {
	apiURL, err := normalizeAPIURL(config.APIURL)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(config.APIToken)
	if token == "" {
		return nil, fmt.Errorf("%s is required", APITokenEnv)
	}
	runID := strings.TrimSpace(config.RunID)
	if runID == "" {
		return nil, fmt.Errorf("%s is required", RunIDEnv)
	}
	runToken := strings.TrimSpace(config.RunToken)
	if runToken == "" {
		return nil, fmt.Errorf("%s is required", RunTokenEnv)
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	return &Server{
		apiURL:   apiURL,
		token:    token,
		runID:    runID,
		runToken: runToken,
		client:   client,
	}, nil
}

// Serve reads and writes newline-delimited JSON-RPC. Protocol errors are sent
// as JSON-RPC errors on stdout; transport and process errors are returned to
// the caller so the command can report them on stderr.
func (s *Server) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	stopInputCloser := make(chan struct{})
	if closer, ok := input.(io.ReadCloser); ok {
		go func() {
			select {
			case <-ctx.Done():
				_ = closer.Close()
			case <-stopInputCloser:
			}
		}()
		defer close(stopInputCloser)
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		response, respond := s.handleLine(ctx, line)
		if !respond {
			continue
		}
		if err := encoder.Encode(response); err != nil {
			return fmt.Errorf("write JSON-RPC response: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read JSON-RPC request: %w", err)
	}
	return nil
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Server) handleLine(ctx context.Context, line []byte) (rpcResponse, bool) {
	var request rpcRequest
	if err := json.Unmarshal(line, &request); err != nil {
		return errorResponse(json.RawMessage("null"), -32700, "Parse error"), true
	}
	if request.JSONRPC != "2.0" || strings.TrimSpace(request.Method) == "" || !validRPCID(request.ID) {
		return errorResponse(json.RawMessage("null"), -32600, "Invalid Request"), true
	}

	result, callError := s.dispatch(ctx, request)
	if len(request.ID) == 0 {
		// JSON-RPC notifications, including notifications/initialized, never
		// receive a response even when their method is unknown.
		return rpcResponse{}, false
	}
	if callError != nil {
		return errorResponse(request.ID, callError.Code, callError.Message), true
	}
	return rpcResponse{JSONRPC: "2.0", ID: request.ID, Result: result}, true
}

func (s *Server) dispatch(ctx context.Context, request rpcRequest) (any, *rpcError) {
	switch request.Method {
	case "initialize":
		var params initializeParams
		if err := decodeOptionalParams(request.Params, &params); err != nil {
			return nil, invalidParams(err)
		}
		return map[string]any{
			"protocolVersion": supportedProtocol(params.ProtocolVersion),
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]string{
				"name":    MCPServerName,
				"version": "0.1.0",
			},
			"instructions": "Use these tools to list, message, and delegate work to other Agents through OpenAgentFleet. The bridge never bypasses OpenAgentFleet's collaboration policy.",
		}, nil
	case "notifications/initialized":
		return map[string]any{}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": tools()}, nil
	case "tools/call":
		var params toolCallParams
		if err := decodeRequiredParams(request.Params, &params); err != nil {
			return nil, invalidParams(err)
		}
		if strings.TrimSpace(params.Name) == "" {
			return nil, invalidParams(fmt.Errorf("tools/call requires a tool name"))
		}
		return s.callTool(ctx, params.Name, params.Arguments), nil
	default:
		return nil, &rpcError{Code: -32601, Message: "Method not found"}
	}
}

type initializeParams struct {
	ProtocolVersion string `json:"protocolVersion"`
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func validRPCID(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	switch value.(type) {
	case nil, string, float64:
		return true
	default:
		return false
	}
}

func errorResponse(id json.RawMessage, code int, message string) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}
}

func invalidParams(err error) *rpcError {
	return &rpcError{Code: -32602, Message: "Invalid params: " + err.Error()}
}

func decodeOptionalParams(raw json.RawMessage, destination any) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	return json.Unmarshal(raw, destination)
}

func decodeRequiredParams(raw json.RawMessage, destination any) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("params are required")
	}
	return json.Unmarshal(raw, destination)
}

func supportedProtocol(requested string) string {
	switch requested {
	case "2025-11-25", "2025-06-18", "2025-03-26":
		return requested
	default:
		return defaultProtocol
	}
}

type toolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func tools() []toolDefinition {
	return []toolDefinition{
		{
			Name:        "list_agents",
			Description: "List Agents available for collaboration through OpenAgentFleet.",
			InputSchema: objectSchema(nil),
		},
		{
			Name:        "message_agent",
			Description: "Send a short message to another Agent. OpenAgentFleet delivers it; this bridge does not talk to the other Agent directly.",
			InputSchema: objectSchema(map[string]any{
				"agent_id": map[string]any{"type": "string", "description": "ID of the Agent to message."},
				"content":  map[string]any{"type": "string", "description": "Message to send."},
			}, "agent_id", "content"),
		},
		{
			Name:        "delegate_to_agent",
			Description: "Ask another Agent to take on a task. OpenAgentFleet queues the work and returns a task ID.",
			InputSchema: objectSchema(map[string]any{
				"agent_id": map[string]any{"type": "string", "description": "ID of the Agent that should do the work."},
				"task":     map[string]any{"type": "string", "description": "Task to delegate."},
			}, "agent_id", "task"),
		},
		{
			Name:        "get_agent_task_status",
			Description: "Read the status of a delegated Agent task.",
			InputSchema: objectSchema(map[string]any{
				"task_id": map[string]any{"type": "string", "description": "ID of the delegated task."},
			}, "task_id"),
		},
	}
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

type messageArguments struct {
	AgentID *string `json:"agent_id"`
	Content *string `json:"content"`
}

type delegateArguments struct {
	AgentID *string `json:"agent_id"`
	Task    *string `json:"task"`
}

type taskStatusArguments struct {
	TaskID *string `json:"task_id"`
}

func (s *Server) callTool(ctx context.Context, name string, arguments json.RawMessage) toolResult {
	switch name {
	case "list_agents":
		return s.listAgents(ctx)
	case "message_agent":
		var input messageArguments
		if err := decodeToolArguments(arguments, &input); err != nil {
			return toolFailure(err)
		}
		agentID := requiredString(input.AgentID)
		content := requiredString(input.Content)
		if agentID == "" {
			return toolFailure(fmt.Errorf("agent_id is required"))
		}
		if content == "" {
			return toolFailure(fmt.Errorf("content is required"))
		}
		return s.messageAgent(ctx, agentID, content)
	case "delegate_to_agent":
		var input delegateArguments
		if err := decodeToolArguments(arguments, &input); err != nil {
			return toolFailure(err)
		}
		agentID := requiredString(input.AgentID)
		task := requiredString(input.Task)
		if agentID == "" {
			return toolFailure(fmt.Errorf("agent_id is required"))
		}
		if task == "" {
			return toolFailure(fmt.Errorf("task is required"))
		}
		return s.delegateToAgent(ctx, agentID, task)
	case "get_agent_task_status":
		var input taskStatusArguments
		if err := decodeToolArguments(arguments, &input); err != nil {
			return toolFailure(err)
		}
		taskID := requiredString(input.TaskID)
		if taskID == "" {
			return toolFailure(fmt.Errorf("task_id is required"))
		}
		return s.getAgentTaskStatus(ctx, taskID)
	default:
		return toolFailure(fmt.Errorf("unknown OpenAgentFleet tool %q", name))
	}
}

func requiredString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func (s *Server) listAgents(ctx context.Context) toolResult {
	body, err := s.jsonRequest(ctx, http.MethodGet, "/api/collaboration/agents", nil)
	if err != nil {
		return toolFailure(err)
	}
	var payload struct {
		Agents []collabAgent `json:"agents"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return toolFailure(fmt.Errorf("decode OpenAgentFleet response: %w", err))
	}
	if len(payload.Agents) == 0 {
		return toolText("No agents are available.")
	}
	parts := make([]string, 0, len(payload.Agents))
	for _, agent := range payload.Agents {
		label := agentLabel(agent)
		if label == "" {
			continue
		}
		parts = append(parts, label)
	}
	if len(parts) == 0 {
		return toolText("No agents are available.")
	}
	noun := "agents"
	if len(parts) == 1 {
		noun = "agent"
	}
	return toolText(fmt.Sprintf("Available %s: %s.", noun, strings.Join(parts, ", ")))
}

func (s *Server) messageAgent(ctx context.Context, agentID, content string) toolResult {
	body, err := s.jsonRequest(ctx, http.MethodPost, "/api/collaboration/message", map[string]any{
		"agent_id": agentID,
		"content":  content,
	})
	if err != nil {
		return toolFailure(err)
	}
	var payload collabAgent
	_ = json.Unmarshal(body, &payload)
	name := firstNonEmpty(payload.Name, payload.Title, payload.AgentName, payload.ID, agentID)
	return toolText(fmt.Sprintf("Sent a message to %s.", name))
}

func (s *Server) delegateToAgent(ctx context.Context, agentID, task string) toolResult {
	body, err := s.jsonRequest(ctx, http.MethodPost, "/api/collaboration/delegate", map[string]any{
		"agent_id": agentID,
		"task":     task,
	})
	if err != nil {
		return toolFailure(err)
	}
	var payload collabTask
	if err := json.Unmarshal(body, &payload); err != nil {
		return toolFailure(fmt.Errorf("decode OpenAgentFleet response: %w", err))
	}
	name := firstNonEmpty(payload.AgentName, payload.Name, payload.Title, payload.AgentID, agentID)
	taskText := firstNonEmpty(payload.Task, payload.Content, task)
	taskID := firstNonEmpty(payload.TaskID, payload.ID)
	status := firstNonEmpty(payload.Status, "queued")
	asked := fmt.Sprintf("Asked %s to %s.", name, trimTask(taskText))
	if taskID == "" {
		return toolText(asked)
	}
	return toolText(fmt.Sprintf("%s Task %s is %s.", asked, taskID, status))
}

func (s *Server) getAgentTaskStatus(ctx context.Context, taskID string) toolResult {
	path := "/api/collaboration/tasks/" + url.PathEscape(taskID)
	body, err := s.jsonRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return toolFailure(err)
	}
	var payload collabTask
	if err := json.Unmarshal(body, &payload); err != nil {
		return toolFailure(fmt.Errorf("decode OpenAgentFleet response: %w", err))
	}
	id := firstNonEmpty(payload.TaskID, payload.ID, taskID)
	status := firstNonEmpty(payload.Status, "unknown")
	name := firstNonEmpty(payload.AgentName, payload.Name, payload.Title)
	if name != "" {
		return toolText(fmt.Sprintf("Task %s is %s for %s.", id, status, name))
	}
	return toolText(fmt.Sprintf("Task %s is %s.", id, status))
}

type collabAgent struct {
	ID        string `json:"id"`
	AgentID   string `json:"agent_id"`
	Name      string `json:"name"`
	Title     string `json:"title"`
	AgentName string `json:"agent_name"`
}

type collabTask struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	AgentID   string `json:"agent_id"`
	Name      string `json:"name"`
	Title     string `json:"title"`
	AgentName string `json:"agent_name"`
	Task      string `json:"task"`
	Content   string `json:"content"`
	Status    string `json:"status"`
}

func agentLabel(agent collabAgent) string {
	id := firstNonEmpty(agent.ID, agent.AgentID)
	name := firstNonEmpty(agent.Name, agent.Title, agent.AgentName)
	switch {
	case name != "" && id != "" && name != id:
		return fmt.Sprintf("%s (%s)", name, id)
	case name != "":
		return name
	default:
		return id
	}
}

func trimTask(task string) string {
	task = strings.TrimSpace(task)
	task = strings.TrimSuffix(task, ".")
	return task
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

type toolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func toolText(text string) toolResult {
	return toolResult{Content: []toolContent{{Type: "text", Text: text}}}
}

func toolFailure(err error) toolResult {
	return toolResult{IsError: true, Content: []toolContent{{Type: "text", Text: err.Error()}}}
}

func decodeToolArguments(raw json.RawMessage, destination any) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		raw = []byte("{}")
	}
	if err := json.Unmarshal(raw, destination); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	return nil
}

func (s *Server) jsonRequest(ctx context.Context, method, path string, payload any) ([]byte, error) {
	body, err := s.request(ctx, method, path, payload, maxJSONResponseSize)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func (s *Server) request(ctx context.Context, method, path string, payload any, limit int) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encode OpenAgentFleet request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, s.apiURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("create OpenAgentFleet request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+s.token)
	request.Header.Set(RunIDHeader, s.runID)
	request.Header.Set(RunTokenHeader, s.runToken)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := s.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call OpenAgentFleet API: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := readLimited(response.Body, limit)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, apiResponseError(response.StatusCode, responseBody)
	}
	return responseBody, nil
}

func readLimited(reader io.Reader, limit int) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("response size limit must be positive")
	}
	result, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("read OpenAgentFleet response: %w", err)
	}
	if len(result) > limit {
		return nil, fmt.Errorf("OpenAgentFleet response exceeds %d byte limit", limit)
	}
	return result, nil
}

func apiResponseError(status int, body []byte) error {
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && strings.TrimSpace(payload.Error) != "" {
		return fmt.Errorf("OpenAgentFleet API returned HTTP %d: %s", status, payload.Error)
	}
	return fmt.Errorf("OpenAgentFleet API returned HTTP %d", status)
}

func normalizeAPIURL(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		raw = defaultAPIURL
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("OPENAGENTFLEET_API_URL must be an absolute http(s) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("OPENAGENTFLEET_API_URL must use http or https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("OPENAGENTFLEET_API_URL must not contain credentials, a query, or a fragment")
	}
	if !isLoopbackHost(parsed.Hostname()) {
		return "", fmt.Errorf("OPENAGENTFLEET_API_URL must be a loopback http(s) URL")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	parsed := net.ParseIP(host)
	return parsed != nil && parsed.IsLoopback()
}
