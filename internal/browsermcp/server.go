// Package browsermcp exposes the OpenAgentFleet Agent Computer over the Model
// Context Protocol (MCP). It intentionally talks only to botd's local HTTP
// API, so all action policy stays enforced by OpenAgentFleet itself.
package browsermcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultAPIURL        = "http://127.0.0.1:4317"
	defaultProtocol      = "2025-11-25"
	maxJSONResponseSize  = 2 << 20
	maxImageResponseSize = 12 << 20
)

// These names form the controller-owned stdio MCP contract. The HTTP API
// injects this server only for a lead run that currently holds Agent Control;
// the bridge still relies on botd's authenticated, server-side action gate.
const (
	MCPServerName    = "openagentfleet-browser-mcp"
	MCPServerCommand = "openagentfleet-browser-mcp"
	// OPENAGENTFLEET_BROWSER_MCP_BINARY is set by the native shell to the bundled
	// sidecar. Keep the old local-development override as a separate fallback
	// rather than making the packaged path depend on PATH lookup.
	MCPServerCommandEnv       = "OPENAGENTFLEET_BROWSER_MCP_BINARY"
	MCPServerCommandLegacyEnv = "OPENAGENTFLEET_BROWSER_MCP_COMMAND"
	APIURLEnv                 = "OPENAGENTFLEET_API_URL"
	APITokenEnv               = "OPENAGENTFLEET_API_TOKEN"
	RunIDEnv                  = "OPENAGENTFLEET_COMPUTER_RUN_ID"
	RunTokenEnv               = "OPENAGENTFLEET_COMPUTER_RUN_TOKEN"
	RunIDHeader               = "X-OpenAgentFleet-Computer-Run-ID"
	RunTokenHeader            = "X-OpenAgentFleet-Computer-Run-Token"
	DefaultAPIURL             = defaultAPIURL
)

// Config controls how the MCP server reaches botd. APIURL defaults to the
// loopback-only OpenAgentFleet daemon and APIToken is sent only when configured.
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

// New creates a browser MCP bridge with a bounded HTTP client.
func New(config Config) (*Server, error) {
	apiURL, err := normalizeAPIURL(config.APIURL)
	if err != nil {
		return nil, err
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	return &Server{
		apiURL:   apiURL,
		token:    strings.TrimSpace(config.APIToken),
		runID:    strings.TrimSpace(config.RunID),
		runToken: strings.TrimSpace(config.RunToken),
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
				"name":    "openagentfleet-browser-mcp",
				"version": "0.1.0",
			},
			"instructions": "Use these tools only after the user has enabled Agent control in OpenAgentFleet. The bridge never bypasses OpenAgentFleet's takeover or action policy.",
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
			Name:        "browser_status",
			Description: "Read the Chromium browser and Agent Computer status from OpenAgentFleet.",
			InputSchema: objectSchema(nil),
		},
		{
			Name:        "browser_start",
			Description: "Start or ensure the isolated Agent Computer and its Chromium browser. This does not enable Agent control.",
			InputSchema: objectSchema(nil),
		},
		{
			Name:        "browser_navigate",
			Description: "Navigate the Agent Computer's Chromium browser to an http or https URL. Requires Agent control in OpenAgentFleet.",
			InputSchema: objectSchema(map[string]any{
				"url": map[string]any{"type": "string", "description": "The http or https URL to open."},
			}, "url"),
		},
		{
			Name:        "browser_click",
			Description: "Click a coordinate in the Chromium browser viewport. Requires Agent control in OpenAgentFleet.",
			InputSchema: clickSchema(),
		},
		{
			Name:        "browser_type",
			Description: "Type text into the active Chromium browser element. Set sensitive for password-like text. Requires Agent control in OpenAgentFleet.",
			InputSchema: typeSchema(),
		},
		{
			Name:        "browser_press",
			Description: "Press a key in Chromium, for example Enter, Tab, or Control+L. Requires Agent control in OpenAgentFleet.",
			InputSchema: keySchema(),
		},
		{
			Name:        "browser_scroll",
			Description: "Scroll inside the Chromium browser viewport. Requires Agent control in OpenAgentFleet.",
			InputSchema: browserScrollSchema(),
		},
		{
			Name:        "browser_screenshot",
			Description: "Capture the visible Chromium browser viewport as an image.",
			InputSchema: objectSchema(nil),
		},
		{
			Name:        "computer_screenshot",
			Description: "Capture the full Xfce Agent Computer desktop as an image, including Chromium, terminal, and file manager.",
			InputSchema: objectSchema(nil),
		},
		{
			Name:        "computer_click",
			Description: "Click a coordinate on the full Agent Computer desktop. Requires Agent control in OpenAgentFleet.",
			InputSchema: clickSchema(),
		},
		{
			Name:        "computer_type",
			Description: "Type text into the focused desktop application. Set sensitive for password-like text. Requires Agent control in OpenAgentFleet.",
			InputSchema: typeSchema(),
		},
		{
			Name:        "computer_press",
			Description: "Press a key in the focused desktop application. Requires Agent control in OpenAgentFleet.",
			InputSchema: keySchema(),
		},
		{
			Name:        "computer_scroll",
			Description: "Scroll the focused desktop application vertically. Requires Agent control in OpenAgentFleet.",
			InputSchema: objectSchema(map[string]any{
				"delta_y": map[string]any{"type": "number", "description": "Vertical scroll delta."},
			}, "delta_y"),
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

func clickSchema() map[string]any {
	return objectSchema(map[string]any{
		"x": map[string]any{"type": "number", "description": "Horizontal coordinate in pixels."},
		"y": map[string]any{"type": "number", "description": "Vertical coordinate in pixels."},
	}, "x", "y")
}

func typeSchema() map[string]any {
	return objectSchema(map[string]any{
		"text":      map[string]any{"type": "string", "description": "Text to type."},
		"sensitive": map[string]any{"type": "boolean", "description": "Mark password-like text as sensitive."},
	}, "text")
}

func keySchema() map[string]any {
	return objectSchema(map[string]any{
		"key": map[string]any{"type": "string", "description": "Key or key chord to press."},
	}, "key")
}

func browserScrollSchema() map[string]any {
	return objectSchema(map[string]any{
		"delta_x": map[string]any{"type": "number", "description": "Horizontal scroll delta."},
		"delta_y": map[string]any{"type": "number", "description": "Vertical scroll delta."},
	}, "delta_y")
}

type clickArguments struct {
	X *float64 `json:"x"`
	Y *float64 `json:"y"`
}

type typeArguments struct {
	Text      *string `json:"text"`
	Sensitive bool    `json:"sensitive"`
}

type keyArguments struct {
	Key *string `json:"key"`
}

type browserScrollArguments struct {
	DeltaX *float64 `json:"delta_x"`
	DeltaY *float64 `json:"delta_y"`
}

type computerScrollArguments struct {
	DeltaY *float64 `json:"delta_y"`
}

type navigateArguments struct {
	URL *string `json:"url"`
}

func (s *Server) callTool(ctx context.Context, name string, arguments json.RawMessage) toolResult {
	switch name {
	case "browser_status":
		return s.jsonTool(ctx, http.MethodGet, "/api/computer", nil)
	case "browser_start":
		return s.jsonTool(ctx, http.MethodPost, "/api/computer/ensure", nil)
	case "browser_navigate":
		var input navigateArguments
		if err := decodeToolArguments(arguments, &input); err != nil {
			return toolFailure(err)
		}
		if input.URL == nil || strings.TrimSpace(*input.URL) == "" {
			return toolFailure(fmt.Errorf("url is required"))
		}
		return s.browserAction(ctx, map[string]any{"action": "navigate", "url": strings.TrimSpace(*input.URL)})
	case "browser_click":
		return s.clickTool(ctx, "/api/computer/action", arguments)
	case "browser_type":
		return s.typeTool(ctx, "/api/computer/action", arguments)
	case "browser_press":
		return s.pressTool(ctx, "/api/computer/action", arguments)
	case "browser_scroll":
		var input browserScrollArguments
		if err := decodeToolArguments(arguments, &input); err != nil {
			return toolFailure(err)
		}
		if input.DeltaY == nil {
			return toolFailure(fmt.Errorf("delta_y is required"))
		}
		action := map[string]any{"action": "scroll", "delta_y": *input.DeltaY}
		if input.DeltaX != nil {
			action["delta_x"] = *input.DeltaX
		}
		return s.browserAction(ctx, action)
	case "browser_screenshot":
		return s.imageTool(ctx, "/api/computer/frame")
	case "computer_screenshot":
		return s.imageTool(ctx, "/api/computer/desktop/frame")
	case "computer_click":
		return s.clickTool(ctx, "/api/computer/desktop/action", arguments)
	case "computer_type":
		return s.typeTool(ctx, "/api/computer/desktop/action", arguments)
	case "computer_press":
		return s.pressTool(ctx, "/api/computer/desktop/action", arguments)
	case "computer_scroll":
		var input computerScrollArguments
		if err := decodeToolArguments(arguments, &input); err != nil {
			return toolFailure(err)
		}
		if input.DeltaY == nil {
			return toolFailure(fmt.Errorf("delta_y is required"))
		}
		return s.desktopAction(ctx, map[string]any{"action": "scroll", "delta_y": *input.DeltaY})
	default:
		return toolFailure(fmt.Errorf("unknown OpenAgentFleet tool %q", name))
	}
}

func (s *Server) clickTool(ctx context.Context, path string, arguments json.RawMessage) toolResult {
	var input clickArguments
	if err := decodeToolArguments(arguments, &input); err != nil {
		return toolFailure(err)
	}
	if input.X == nil || input.Y == nil {
		return toolFailure(fmt.Errorf("x and y are required"))
	}
	action := map[string]any{"action": "click", "x": *input.X, "y": *input.Y}
	if path == "/api/computer/desktop/action" {
		return s.desktopAction(ctx, action)
	}
	return s.browserAction(ctx, action)
}

func (s *Server) typeTool(ctx context.Context, path string, arguments json.RawMessage) toolResult {
	var input typeArguments
	if err := decodeToolArguments(arguments, &input); err != nil {
		return toolFailure(err)
	}
	if input.Text == nil {
		return toolFailure(fmt.Errorf("text is required"))
	}
	action := map[string]any{"action": "type", "text": *input.Text, "sensitive": input.Sensitive}
	if path == "/api/computer/desktop/action" {
		return s.desktopAction(ctx, action)
	}
	return s.browserAction(ctx, action)
}

func (s *Server) pressTool(ctx context.Context, path string, arguments json.RawMessage) toolResult {
	var input keyArguments
	if err := decodeToolArguments(arguments, &input); err != nil {
		return toolFailure(err)
	}
	if input.Key == nil || strings.TrimSpace(*input.Key) == "" {
		return toolFailure(fmt.Errorf("key is required"))
	}
	action := map[string]any{"action": "press", "key": strings.TrimSpace(*input.Key)}
	if path == "/api/computer/desktop/action" {
		return s.desktopAction(ctx, action)
	}
	return s.browserAction(ctx, action)
}

func (s *Server) browserAction(ctx context.Context, action map[string]any) toolResult {
	return s.jsonTool(ctx, http.MethodPost, "/api/computer/action", action)
}

func (s *Server) desktopAction(ctx context.Context, action map[string]any) toolResult {
	return s.jsonTool(ctx, http.MethodPost, "/api/computer/desktop/action", action)
}

func (s *Server) jsonTool(ctx context.Context, method, path string, payload any) toolResult {
	response, err := s.jsonRequest(ctx, method, path, payload)
	if err != nil {
		return toolFailure(err)
	}
	encoded, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return toolFailure(fmt.Errorf("encode OpenAgentFleet response: %w", err))
	}
	return toolResult{Content: []toolContent{{Type: "text", Text: string(encoded)}}}
}

func (s *Server) imageTool(ctx context.Context, path string) toolResult {
	image, contentType, err := s.request(ctx, http.MethodGet, path, nil, maxImageResponseSize)
	if err != nil {
		return toolFailure(err)
	}
	mediaType, _, parseErr := mime.ParseMediaType(contentType)
	if parseErr != nil || mediaType == "" {
		mediaType = "image/png"
	}
	if !strings.HasPrefix(strings.ToLower(mediaType), "image/") {
		return toolFailure(fmt.Errorf("OpenAgentFleet screenshot returned %q instead of an image", mediaType))
	}
	return toolResult{Content: []toolContent{{
		Type:     "image",
		Data:     base64.StdEncoding.EncodeToString(image),
		MIMEType: mediaType,
	}}}
}

type toolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type toolContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
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

func (s *Server) jsonRequest(ctx context.Context, method, path string, payload any) (any, error) {
	body, _, err := s.request(ctx, method, path, payload, maxJSONResponseSize)
	if err != nil {
		return nil, err
	}
	var response any
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode OpenAgentFleet response: %w", err)
	}
	return response, nil
}

func (s *Server) request(ctx context.Context, method, path string, payload any, limit int) ([]byte, string, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, "", fmt.Errorf("encode OpenAgentFleet request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, s.apiURL+path, body)
	if err != nil {
		return nil, "", fmt.Errorf("create OpenAgentFleet request: %w", err)
	}
	request.Header.Set("X-OpenAgentFleet-Computer-Use", "agent")
	if s.runID != "" {
		request.Header.Set(RunIDHeader, s.runID)
	}
	if s.runToken != "" {
		request.Header.Set(RunTokenHeader, s.runToken)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if s.token != "" {
		request.Header.Set("Authorization", "Bearer "+s.token)
	}

	response, err := s.client.Do(request)
	if err != nil {
		return nil, "", fmt.Errorf("call OpenAgentFleet API: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := readLimited(response.Body, limit)
	if err != nil {
		return nil, response.Header.Get("Content-Type"), err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, response.Header.Get("Content-Type"), apiResponseError(response.StatusCode, responseBody)
	}
	return responseBody, response.Header.Get("Content-Type"), nil
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
	return strings.TrimRight(parsed.String(), "/"), nil
}
