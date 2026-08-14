package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const acpCloseTimeout = 3 * time.Second

type PermissionRequest struct {
	SessionID string
	Options   json.RawMessage
	ToolCall  json.RawMessage
}

type PermissionDecision struct {
	Outcome  string
	OptionID string
}

type RunOptions struct {
	OnLine          func(OutputLine)
	OnPermission    func(context.Context, PermissionRequest) (PermissionDecision, error)
	SessionID       string
	OnSession       func(string)
	SystemPrompt    string
	Model           string
	ReasoningEffort string
	ServiceTier     string
	PermissionMode  string
	WebSearch       string
	MCPServers      []MCPServerSpec
}

type ACPNotification struct {
	Method string
	Params json.RawMessage
}

type GrokSession struct {
	client    *rpcClient
	ID        string
	Workdir   string
	terminals *terminalManager

	promptMu sync.Mutex
}

type GrokSessionOptions struct {
	Binary                  string
	Workdir                 string
	SessionID               string
	Model                   string
	ReasoningEffort         string
	PermissionMode          string
	WebSearch               string
	MCPServers              []MCPServerSpec
	OnNotification          func(ACPNotification)
	OnPermission            func(context.Context, PermissionRequest) (PermissionDecision, error)
	AllowWorkspaceWrites    bool
	AllowWorkspaceExecution bool
}

type promptResult struct {
	StopReason string `json:"stopReason"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcResponse struct {
	result json.RawMessage
	err    error
}

type rpcClient struct {
	process *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stderr  io.ReadCloser
	ctx     context.Context
	done    chan struct{}

	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[string]chan rpcResponse
	nextID    atomic.Uint64

	onRequest      func(context.Context, rpcMessage) (any, error)
	onNotification func(ACPNotification)
	finishOnce     sync.Once
	terminateOnce  sync.Once
}

func startRPC(ctx context.Context, provider, program string, args []string, workdir string, onRequest func(context.Context, rpcMessage) (any, error), onNotification func(ACPNotification)) (*rpcClient, error) {
	process := newIsolatedCommandContext(ctx, provider, program, args...)
	process.Dir = workdir
	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("open ACP stdin: %w", err)
	}
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		_ = stdinReader.Close()
		_ = stdinWriter.Close()
		return nil, fmt.Errorf("capture ACP stdout: %w", err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdinReader.Close()
		_ = stdinWriter.Close()
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		return nil, fmt.Errorf("capture ACP stderr: %w", err)
	}
	process.Stdin = stdinReader
	process.Stdout = stdoutWriter
	process.Stderr = stderrWriter
	if err := process.Start(); err != nil {
		_ = stdinReader.Close()
		_ = stdinWriter.Close()
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		_ = stderrReader.Close()
		_ = stderrWriter.Close()
		return nil, fmt.Errorf("start ACP process: %w", err)
	}
	_ = stdinReader.Close()
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	client := &rpcClient{
		process:        process,
		stdin:          stdinWriter,
		stdout:         stdoutReader,
		stderr:         stderrReader,
		ctx:            ctx,
		done:           make(chan struct{}),
		pending:        make(map[string]chan rpcResponse),
		onRequest:      onRequest,
		onNotification: onNotification,
	}
	go func() {
		_, _ = io.Copy(io.Discard, stderrReader)
	}()
	go client.readLoop(stdoutReader)
	go client.waitLoop()
	return client, nil
}

func (c *rpcClient) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 32*1024), 8*1024*1024)
	for scanner.Scan() {
		var message rpcMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			continue
		}
		if message.Method != "" {
			if len(message.ID) == 0 {
				if c.onNotification != nil {
					c.onNotification(ACPNotification{Method: message.Method, Params: message.Params})
				}
				continue
			}
			go c.handleRequest(message)
			continue
		}
		if len(message.ID) == 0 {
			continue
		}
		key := string(message.ID)
		c.pendingMu.Lock()
		responseChannel, ok := c.pending[key]
		if ok {
			delete(c.pending, key)
		}
		c.pendingMu.Unlock()
		if !ok {
			continue
		}
		var responseError error
		if message.Error != nil {
			responseError = fmt.Errorf("ACP %d: %s", message.Error.Code, message.Error.Message)
		}
		select {
		case responseChannel <- rpcResponse{result: message.Result, err: responseError}:
		default:
		}
	}
	// ACP cannot continue after stdout closes, even if the provider process is
	// still alive. Termination is idempotent and waitLoop remains the sole owner
	// of Cmd.Wait.
	c.terminate()
}

func (c *rpcClient) waitLoop() {
	err := c.process.Wait()
	if err == nil {
		err = errors.New("ACP process exited")
	}
	c.finish(err)
}

func (c *rpcClient) handleRequest(message rpcMessage) {
	var result any
	var err error
	if c.onRequest == nil {
		err = errors.New("ACP request handler unavailable")
	} else {
		result, err = c.onRequest(c.ctx, message)
	}
	if err != nil {
		_ = c.respondError(message.ID, -32000, err.Error())
		return
	}
	_ = c.respondResult(message.ID, result)
}

func (c *rpcClient) call(ctx context.Context, method string, params any, result any) error {
	id := strconv.FormatUint(c.nextID.Add(1), 10)
	responseChannel := make(chan rpcResponse, 1)
	c.pendingMu.Lock()
	c.pending[id] = responseChannel
	c.pendingMu.Unlock()
	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "method": method, "params": params}); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return err
	}
	select {
	case response := <-responseChannel:
		if response.err != nil {
			return response.err
		}
		if result == nil || len(response.result) == 0 || string(response.result) == "null" {
			return nil
		}
		if err := json.Unmarshal(response.result, result); err != nil {
			return fmt.Errorf("decode ACP %s response: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return ctx.Err()
	case <-c.done:
		return errors.New("ACP process stopped")
	}
}

func (c *rpcClient) respondResult(id json.RawMessage, result any) error {
	return c.send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
}

func (c *rpcClient) respondError(id json.RawMessage, code int, message string) error {
	return c.send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "error": rpcError{Code: code, Message: message}})
}

func (c *rpcClient) send(value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.stdin.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("write ACP message: %w", err)
	}
	return nil
}

func (c *rpcClient) finish(err error) {
	c.finishOnce.Do(func() {
		close(c.done)
		c.pendingMu.Lock()
		defer c.pendingMu.Unlock()
		for id, responseChannel := range c.pending {
			select {
			case responseChannel <- rpcResponse{err: err}:
			default:
			}
			delete(c.pending, id)
		}
	})
}

func (c *rpcClient) terminate() {
	c.terminateOnce.Do(func() {
		if c.process.Process != nil {
			_ = c.process.Process.Kill()
		}
		_ = c.stdin.Close()
		_ = c.stdout.Close()
		_ = c.stderr.Close()
	})
}

func (c *rpcClient) close() error {
	if c == nil {
		return nil
	}
	c.terminate()
	timer := time.NewTimer(acpCloseTimeout)
	defer timer.Stop()
	select {
	case <-c.done:
		return nil
	case <-timer.C:
		return errors.New("timed out waiting for ACP process termination")
	}
}

func OpenGrokSession(ctx context.Context, options GrokSessionOptions) (*GrokSession, error) {
	if strings.EqualFold(strings.TrimSpace(options.PermissionMode), "yolo") || strings.EqualFold(strings.TrimSpace(options.PermissionMode), "--yolo") {
		return nil, errors.New("Grok yolo mode is disabled by OpenAgentFleet policy")
	}
	mcpServers, err := normalizeMCPServers(options.MCPServers)
	if err != nil {
		return nil, err
	}
	webSearch, err := normalizeWebSearchMode(options.WebSearch)
	if err != nil {
		return nil, err
	}
	workdir, err := filepath.Abs(options.Workdir)
	if err != nil {
		return nil, fmt.Errorf("resolve Grok workdir: %w", err)
	}
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		return nil, fmt.Errorf("create Grok workdir: %w", err)
	}
	if options.Binary == "" {
		options.Binary = "grok"
	}
	options.AllowWorkspaceWrites = options.AllowWorkspaceWrites && !isReadOnlyPermissionMode(options.PermissionMode)
	options.AllowWorkspaceExecution = options.AllowWorkspaceExecution && !isReadOnlyPermissionMode(options.PermissionMode)
	session := &GrokSession{Workdir: workdir}
	args := []string{}
	if options.Model != "" {
		args = append(args, "--model", options.Model)
	}
	if options.ReasoningEffort != "" {
		args = append(args, "--reasoning-effort", options.ReasoningEffort)
	}
	if options.PermissionMode != "" {
		args = append(args, "--permission-mode", options.PermissionMode)
	}
	if webSearch == "disabled" {
		args = append(args, "--disable-web-search")
	}
	args = append(args, "agent", "--no-leader", "stdio")
	client, err := startRPC(ctx, "grok", options.Binary, args, workdir, func(requestContext context.Context, request rpcMessage) (any, error) {
		return session.handleRequest(requestContext, request, options)
	}, options.OnNotification)
	if err != nil {
		return nil, err
	}
	session.client = client
	session.terminals = newTerminalManager(workdir)

	var initialize struct {
		AuthMethods []struct {
			ID string `json:"id"`
		} `json:"authMethods"`
	}
	if err := client.call(ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": true, "writeTextFile": options.AllowWorkspaceWrites},
			"terminal": options.AllowWorkspaceExecution,
		},
	}, &initialize); err != nil {
		_ = client.close()
		return nil, fmt.Errorf("initialize Grok ACP: %w", err)
	}
	if err := authenticateGrok(ctx, client, initialize.AuthMethods); err != nil {
		_ = client.close()
		return nil, err
	}

	var created struct {
		SessionID string `json:"sessionId"`
	}
	if options.SessionID == "" {
		err = client.call(ctx, "session/new", map[string]any{
			"cwd":        workdir,
			"mcpServers": acpMCPServers(mcpServers),
		}, &created)
	} else {
		err = client.call(ctx, "session/load", map[string]any{
			"sessionId":  options.SessionID,
			"cwd":        workdir,
			"mcpServers": acpMCPServers(mcpServers),
		}, &created)
		created.SessionID = options.SessionID
	}
	if err != nil {
		_ = client.close()
		return nil, fmt.Errorf("open Grok session: %w", err)
	}
	if created.SessionID == "" {
		_ = client.close()
		return nil, errors.New("Grok ACP returned no session id")
	}
	session.ID = created.SessionID
	return session, nil
}

func authenticateGrok(ctx context.Context, client *rpcClient, methods []struct {
	ID string `json:"id"`
}) error {
	if len(methods) == 0 {
		return nil
	}
	methodID := ""
	if os.Getenv("XAI_API_KEY") != "" {
		for _, method := range methods {
			if method.ID == "xai.api_key" {
				methodID = method.ID
				break
			}
		}
	}
	if methodID == "" {
		for _, method := range methods {
			if method.ID == "cached_token" {
				methodID = method.ID
				break
			}
		}
	}
	if methodID == "" {
		return errors.New("Grok authentication required; run `grok login` or set XAI_API_KEY")
	}
	if err := client.call(ctx, "authenticate", map[string]any{"methodId": methodID, "_meta": map[string]any{"headless": true}}, nil); err != nil {
		return fmt.Errorf("authenticate Grok ACP: %w", err)
	}
	return nil
}

func (s *GrokSession) Prompt(ctx context.Context, prompt string, onNotification func(ACPNotification)) (promptResult, error) {
	s.promptMu.Lock()
	defer s.promptMu.Unlock()
	var result promptResult
	previous := s.client.onNotification
	if onNotification != nil {
		s.client.onNotification = onNotification
	}
	err := s.client.call(ctx, "session/prompt", map[string]any{
		"sessionId": s.ID,
		"prompt":    []map[string]string{{"type": "text", "text": prompt}},
	}, &result)
	s.client.onNotification = previous
	return result, err
}

func (s *GrokSession) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.close()
}

func (s *GrokSession) handleRequest(ctx context.Context, request rpcMessage, options GrokSessionOptions) (any, error) {
	var params map[string]any
	if len(request.Params) > 0 && json.Unmarshal(request.Params, &params) != nil {
		return nil, errors.New("invalid ACP request params")
	}
	switch request.Method {
	case "session/request_permission":
		permission := PermissionRequest{Options: paramsRaw(params, "options"), ToolCall: paramsRaw(params, "toolCall")}
		if value, ok := params["sessionId"].(string); ok {
			permission.SessionID = value
		}
		if options.OnPermission == nil {
			return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
		}
		decision, err := options.OnPermission(ctx, permission)
		if err != nil {
			return nil, err
		}
		if decision.Outcome != "selected" || decision.OptionID == "" {
			return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
		}
		return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": decision.OptionID}}, nil
	case "fs/read_text_file":
		path, err := s.workspacePath(params["path"])
		if err != nil {
			return nil, err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return map[string]string{"content": string(content)}, nil
	case "fs/write_text_file":
		if !options.AllowWorkspaceWrites || isReadOnlyPermissionMode(options.PermissionMode) {
			return nil, errors.New("workspace writes are disabled by OpenAgentFleet policy")
		}
		path, err := s.workspacePathForCreate(params["path"])
		if err != nil {
			return nil, err
		}
		content, _ := params["content"].(string)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return nil, err
		}
		return map[string]any{}, nil
	case "terminal/create", "terminal/output", "terminal/wait_for_exit", "terminal/kill", "terminal/release":
		if !acpTerminalExecutionAllowed(options) {
			return nil, errors.New("terminal execution is disabled by OpenAgentFleet policy")
		}
		switch request.Method {
		case "terminal/create":
			return s.terminals.create(ctx, params)
		case "terminal/output":
			return s.terminals.output(params)
		case "terminal/wait_for_exit":
			return s.terminals.wait(ctx, params)
		case "terminal/kill":
			return s.terminals.kill(params)
		default:
			return s.terminals.release(params)
		}
	default:
		return nil, fmt.Errorf("unsupported Grok ACP client request: %s", request.Method)
	}
}

func isReadOnlyPermissionMode(mode string) bool {
	switch strings.TrimSpace(mode) {
	case "plan", "read_only":
		return true
	default:
		return false
	}
}

func acpTerminalExecutionAllowed(options GrokSessionOptions) bool {
	return options.AllowWorkspaceExecution && !isReadOnlyPermissionMode(options.PermissionMode)
}

func (s *GrokSession) workspacePath(raw any) (string, error) {
	return s.resolveWorkspacePath(raw, false)
}

func (s *GrokSession) workspacePathForCreate(raw any) (string, error) {
	return s.resolveWorkspacePath(raw, true)
}

func (s *GrokSession) resolveWorkspacePath(raw any, allowCreate bool) (string, error) {
	requested, ok := raw.(string)
	if !ok || requested == "" {
		return "", errors.New("ACP path is required")
	}
	workspace, err := filepath.Abs(s.Workdir)
	if err != nil {
		return "", fmt.Errorf("resolve ACP workspace: %w", err)
	}
	resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve ACP workspace symlinks: %w", err)
	}
	path := requested
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspace, path)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if !pathWithin(workspace, path) {
		return "", errors.New("ACP path is outside the configured workspace")
	}

	if _, err := os.Lstat(path); err == nil {
		resolved, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil {
			return "", fmt.Errorf("resolve ACP path symlinks: %w", resolveErr)
		}
		if !pathWithin(resolvedWorkspace, resolved) {
			return "", errors.New("ACP path resolves outside the configured workspace")
		}
		return resolved, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect ACP path: %w", err)
	} else if !allowCreate {
		return "", err
	}

	// For a create, resolve the deepest ancestor that currently exists. Joining
	// the missing suffix beneath that resolved ancestor rejects known symlink
	// escapes without claiming protection against a later symlink swap.
	ancestor := filepath.Dir(path)
	for {
		if _, statErr := os.Lstat(ancestor); statErr == nil {
			resolvedAncestor, resolveErr := filepath.EvalSymlinks(ancestor)
			if resolveErr != nil {
				return "", fmt.Errorf("resolve ACP parent symlinks: %w", resolveErr)
			}
			if !pathWithin(resolvedWorkspace, resolvedAncestor) {
				return "", errors.New("ACP path resolves outside the configured workspace")
			}
			suffix, relativeErr := filepath.Rel(ancestor, path)
			if relativeErr != nil {
				return "", relativeErr
			}
			resolved := filepath.Join(resolvedAncestor, suffix)
			if !pathWithin(resolvedWorkspace, resolved) {
				return "", errors.New("ACP path resolves outside the configured workspace")
			}
			return resolved, nil
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("inspect ACP parent: %w", statErr)
		}
		next := filepath.Dir(ancestor)
		if next == ancestor {
			return "", errors.New("ACP path has no existing parent")
		}
		ancestor = next
	}
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

type terminalManager struct {
	workdir string
	mu      sync.Mutex
	nextID  uint64
	items   map[string]*terminalProcess
}

type terminalProcess struct {
	command string
	process *exec.Cmd
	output  *boundedBuffer
	done    chan struct{}

	mu      sync.Mutex
	waitErr error
}

func newTerminalManager(workdir string) *terminalManager {
	return &terminalManager{workdir: workdir, items: make(map[string]*terminalProcess)}
}

func (m *terminalManager) create(ctx context.Context, params map[string]any) (map[string]any, error) {
	command, ok := params["command"].(string)
	if !ok || strings.TrimSpace(command) == "" {
		return nil, errors.New("terminal command is required")
	}
	args := make([]string, 0)
	if rawArgs, ok := params["args"]; ok {
		encoded, _ := json.Marshal(rawArgs)
		if err := json.Unmarshal(encoded, &args); err != nil {
			return nil, errors.New("terminal args must be strings")
		}
	}
	cwd := m.workdir
	if rawCwd, ok := params["cwd"].(string); ok && rawCwd != "" {
		path, err := (&GrokSession{Workdir: m.workdir}).workspacePath(rawCwd)
		if err != nil {
			return nil, err
		}
		cwd = path
	}
	requestedEnvironment := make(map[string]string)
	if rawEnv, ok := params["env"].(map[string]any); ok {
		for key, value := range rawEnv {
			if text, ok := value.(string); ok {
				requestedEnvironment[key] = text
			}
		}
	}
	process := newToolCommandContext(ctx, command, args, requestedEnvironment)
	process.Dir = cwd
	buffer := &boundedBuffer{limit: maxCapturedOutput}
	process.Stdout = buffer
	process.Stderr = buffer
	if err := process.Start(); err != nil {
		return nil, fmt.Errorf("start terminal command: %w", err)
	}
	terminal := &terminalProcess{command: command, process: process, output: buffer, done: make(chan struct{})}
	m.mu.Lock()
	m.nextID++
	id := "term-" + strconv.FormatUint(m.nextID, 10)
	m.items[id] = terminal
	m.mu.Unlock()
	go func() {
		err := process.Wait()
		terminal.mu.Lock()
		terminal.waitErr = err
		terminal.mu.Unlock()
		close(terminal.done)
	}()
	return map[string]any{"terminalId": id}, nil
}

func (m *terminalManager) get(params map[string]any) (*terminalProcess, error) {
	id, _ := params["terminalId"].(string)
	if id == "" {
		id, _ = params["terminal_id"].(string)
	}
	m.mu.Lock()
	terminal := m.items[id]
	m.mu.Unlock()
	if terminal == nil {
		return nil, errors.New("terminal not found")
	}
	return terminal, nil
}

func (m *terminalManager) output(params map[string]any) (map[string]any, error) {
	terminal, err := m.get(params)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"output": Redact(terminal.output.String())}
	select {
	case <-terminal.done:
		result["exitStatus"] = map[string]any{"exitCode": terminal.exitCode()}
	default:
	}
	return result, nil
}

func (m *terminalManager) wait(ctx context.Context, params map[string]any) (map[string]any, error) {
	terminal, err := m.get(params)
	if err != nil {
		return nil, err
	}
	select {
	case <-terminal.done:
		return map[string]any{"exitCode": terminal.exitCode()}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *terminalManager) kill(params map[string]any) (map[string]any, error) {
	terminal, err := m.get(params)
	if err != nil {
		return nil, err
	}
	if terminal.process.Process != nil {
		if err := terminal.process.Process.Kill(); err != nil && !strings.Contains(err.Error(), "already finished") {
			return nil, err
		}
	}
	return map[string]any{}, nil
}

func (m *terminalManager) release(params map[string]any) (map[string]any, error) {
	terminal, err := m.get(params)
	if err != nil {
		return nil, err
	}
	select {
	case <-terminal.done:
	default:
		return nil, errors.New("terminal is still running")
	}
	id, _ := params["terminalId"].(string)
	if id == "" {
		id, _ = params["terminal_id"].(string)
	}
	m.mu.Lock()
	delete(m.items, id)
	m.mu.Unlock()
	return map[string]any{}, nil
}

func (t *terminalProcess) exitCode() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.waitErr == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(t.waitErr, &exitError) && exitError.ProcessState != nil {
		return exitError.ProcessState.ExitCode()
	}
	return -1
}

type boundedBuffer struct {
	mu    sync.Mutex
	data  bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	originalLength := len(value)
	remaining := b.limit - b.data.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = b.data.Write(value)
	}
	return originalLength, nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

func paramsRaw(params map[string]any, key string) json.RawMessage {
	value, ok := params[key]
	if !ok {
		return nil
	}
	encoded, _ := json.Marshal(value)
	return encoded
}

func (r *Runner) RunWithOptions(ctx context.Context, provider, prompt, workdir string, options RunOptions) (string, error) {
	if !r.AllowExecution {
		return "", ErrExecutionDisabled
	}
	if options.OnLine == nil {
		options.OnLine = func(OutputLine) {}
	}
	mcpServers, err := normalizeMCPServers(options.MCPServers)
	if err != nil {
		return "", err
	}
	options.MCPServers = mcpServers
	if len(mcpServers) > 0 && provider != "grok" && provider != CodexAppServerProvider && provider != OpenCodeProvider {
		return "", fmt.Errorf("MCP server injection is unsupported for provider %q", provider)
	}
	if provider == "grok" {
		return r.runGrokACP(ctx, prompt, workdir, options)
	}
	if provider == CodexAppServerProvider {
		return r.runCodexAppServer(ctx, prompt, workdir, options)
	}
	if provider == OpenCodeProvider {
		if err := ValidateOpenCodeOptions(options.Model, options.ReasoningEffort, options.ServiceTier, options.PermissionMode); err != nil {
			return "", err
		}
	}
	prompt = promptWithSystemFallback(options.SystemPrompt, prompt)
	return r.runCommand(ctx, provider, prompt, workdir, options)
}

func (r *Runner) runGrokACP(ctx context.Context, prompt, workdir string, options RunOptions) (string, error) {
	prompt = promptWithSystemFallback(options.SystemPrompt, prompt)
	var output bytes.Buffer
	appendOutput := func(line OutputLine) {
		options.OnLine(line)
		encoded, err := json.Marshal(map[string]any{"type": line.Type, "data": line.Text})
		if err == nil {
			output.Write(encoded)
			output.WriteByte('\n')
		}
	}
	session, err := OpenGrokSession(ctx, GrokSessionOptions{
		Workdir:                 workdir,
		SessionID:               options.SessionID,
		Model:                   options.Model,
		ReasoningEffort:         options.ReasoningEffort,
		PermissionMode:          options.PermissionMode,
		WebSearch:               options.WebSearch,
		MCPServers:              options.MCPServers,
		OnPermission:            options.OnPermission,
		AllowWorkspaceWrites:    r.AllowWorkspaceWrites && !isReadOnlyPermissionMode(options.PermissionMode),
		AllowWorkspaceExecution: r.AllowExecution && !isReadOnlyPermissionMode(options.PermissionMode),
	})
	if err != nil {
		return output.String(), err
	}
	defer session.Close()
	if options.OnSession != nil {
		options.OnSession(session.ID)
	}
	result, err := session.Prompt(ctx, prompt, func(notification ACPNotification) {
		if notification.Method != "session/update" {
			appendOutput(OutputLine{Stream: "stdout", Type: notification.Method, Text: Redact(string(notification.Params))})
			return
		}
		var params struct {
			Update map[string]any `json:"update"`
		}
		if json.Unmarshal(notification.Params, &params) != nil {
			return
		}
		kind, _ := params.Update["sessionUpdate"].(string)
		switch kind {
		case "agent_message_chunk":
			content, _ := params.Update["content"].(map[string]any)
			text, _ := content["text"].(string)
			if text != "" {
				appendOutput(OutputLine{Stream: "stdout", Type: "text", Text: Redact(text)})
			}
		case "agent_thought_chunk":
			// Do not expose or persist hidden chain-of-thought. Keep only a
			// coarse activity marker that the UI may render as progress.
			appendOutput(OutputLine{Stream: "stdout", Type: "thought", Text: "reasoning update"})
		default:
			encoded, marshalErr := json.Marshal(params.Update)
			if marshalErr == nil {
				appendOutput(OutputLine{Stream: "stdout", Type: kind, Text: Redact(string(encoded))})
			}
		}
	})
	if err == nil && result.StopReason == "cancelled" {
		err = errors.New("Grok prompt cancelled")
	}
	return output.String(), err
}

func normalizeWebSearchMode(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "", "live":
		return "live", nil
	case "disabled":
		return "disabled", nil
	default:
		return "", fmt.Errorf("web_search %q is not supported; use live or disabled", strings.TrimSpace(value))
	}
}

func promptWithSystemFallback(systemPrompt, prompt string) string {
	if strings.TrimSpace(systemPrompt) == "" {
		return prompt
	}
	return "OpenAgentFleet controller instructions (higher priority than the task below):\n" +
		systemPrompt + "\n\nCurrent user task and approved context:\n" + prompt
}
