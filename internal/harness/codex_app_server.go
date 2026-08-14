package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const CodexAppServerProvider = "codex_app_server"

// CodexAppServer owns one local JSON-RPC App Server process. The process is
// shared by OAuth and runs so the browser callback stays alive and a stored
// thread can be resumed across Atlas messages.
type CodexAppServer struct {
	Binary  string
	Workdir string

	mu           sync.Mutex
	client       *rpcClient
	starting     chan struct{}
	closed       bool
	sessions     map[string]*CodexAppSession
	threadGates  map[string]*codexThreadGate
	pendingLogin *codexLogin
}

// codexThreadGate serializes access to one native Codex thread. References are
// held by both the active session and waiters so an idle gate can be discarded
// without racing a newly arriving resume request.
type codexThreadGate struct {
	token chan struct{}
	refs  int
}

type codexLogin struct {
	mode            string
	loginID         string
	verificationURL string
	userCode        string
	startedAt       time.Time
	completed       bool
	success         bool
	err             string
}

type CodexAppSessionOptions struct {
	Workdir               string
	SessionID             string
	Model                 string
	ReasoningEffort       string
	ServiceTier           string
	WebSearch             string
	Config                map[string]any
	MCPServers            []MCPServerSpec
	DeveloperInstructions string
	ApprovalPolicy        string
	OnNotification        func(ACPNotification)
	OnPermission          func(context.Context, PermissionRequest) (PermissionDecision, error)
	AllowWorkspaceWrites  bool
}

type CodexAppSession struct {
	server      *CodexAppServer
	ID          string
	Workdir     string
	model       string
	effort      string
	serviceTier string
	onNotify    func(ACPNotification)
	onApproval  func(context.Context, PermissionRequest) (PermissionDecision, error)

	promptMu sync.Mutex
	turnMu   sync.Mutex
	turnID   string
	turnDone chan codexTurnResult

	closeOnce     sync.Once
	releaseThread func()
}

type codexTurnResult struct {
	Status string
	Error  string
}

func NewCodexAppServer(binary, workdir string) *CodexAppServer {
	if binary == "" {
		binary = "codex"
	}
	return &CodexAppServer{
		Binary:      binary,
		Workdir:     workdir,
		sessions:    make(map[string]*CodexAppSession),
		threadGates: make(map[string]*codexThreadGate),
	}
}

func (s *CodexAppServer) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.closed = true
	client := s.client
	s.client = nil
	s.sessions = make(map[string]*CodexAppSession)
	s.threadGates = make(map[string]*codexThreadGate)
	s.mu.Unlock()
	if client == nil {
		return nil
	}
	return client.close()
}

func (s *CodexAppServer) Status(ctx context.Context) (AuthStatus, error) {
	if s == nil {
		return AuthStatus{Provider: CodexAppServerProvider, Detail: "Codex App Server unavailable"}, errors.New("Codex App Server unavailable")
	}
	if _, err := exec.LookPath(s.Binary); err != nil {
		return AuthStatus{Provider: CodexAppServerProvider, Detail: "codex was not found in PATH", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
	}
	// The CLI status probe is cheap and reads the same provider-owned credential
	// store as app-server. It also avoids showing a false sign-in prompt while a
	// cold app-server process is still starting inside the bounded bootstrap.
	probeContext, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
	command := newIsolatedCommandContext(probeContext, "codex", s.Binary, "login", "status")
	output, probeErr := command.CombinedOutput()
	cancel()
	if probeErr == nil && codexLoginStatusConnected(output) {
		return AuthStatus{
			Provider:      CodexAppServerProvider,
			Available:     true,
			Authenticated: true,
			Mode:          "chatgpt",
			Detail:        "Codex ChatGPT login connected",
			UpdatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		}, nil
	}
	var response struct {
		Account            map[string]any `json:"account"`
		RequiresOpenAIAuth bool           `json:"requiresOpenaiAuth"`
	}
	if err := s.call(ctx, "account/read", map[string]any{}, &response); err != nil {
		return AuthStatus{Provider: CodexAppServerProvider, Available: true, Detail: "Codex App Server is unavailable: " + compactAuthError(err), UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
	}
	status := AuthStatus{Provider: CodexAppServerProvider, Available: true, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if response.Account != nil {
		status.Authenticated = true
		status.Mode, _ = response.Account["type"].(string)
		status.Plan, _ = response.Account["planType"].(string)
		status.Detail = "Codex App Server OAuth connected"
	} else if response.RequiresOpenAIAuth {
		status.LoginRequired = true
		status.Detail = "Connect ChatGPT with OAuth to use Codex App Server"
	} else {
		status.LoginRequired = true
		status.Detail = "No Codex App Server account is connected"
	}
	s.mu.Lock()
	if login := s.pendingLogin; login != nil && !login.completed {
		status.Pending = true
		status.Mode = login.mode
		status.VerificationURL = login.verificationURL
		status.UserCode = login.userCode
		if login.mode == "device-code" {
			status.Detail = "Enter the device code to finish ChatGPT OAuth."
		} else {
			status.Detail = "Browser OAuth is waiting for completion."
		}
	}
	s.mu.Unlock()
	return status, nil
}

func codexLoginStatusConnected(output []byte) bool {
	for _, line := range strings.Split(strings.ToLower(string(output)), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "logged in") {
			return true
		}
	}
	return false
}

func (s *CodexAppServer) StartBrowserLogin(ctx context.Context) (OAuthStart, error) {
	var response struct {
		Type    string `json:"type"`
		LoginID string `json:"loginId"`
		AuthURL string `json:"authUrl"`
	}
	if err := s.call(ctx, "account/login/start", map[string]any{
		"type":                      "chatgpt",
		"useHostedLoginSuccessPage": true,
		"appBrand":                  "codex",
	}, &response); err != nil {
		return OAuthStart{}, fmt.Errorf("start Codex browser OAuth: %w", err)
	}
	if response.LoginID == "" || response.AuthURL == "" {
		return OAuthStart{}, errors.New("Codex App Server returned no OAuth URL")
	}
	status := s.setPendingLogin(codexLogin{mode: "oauth", loginID: response.LoginID, startedAt: time.Now()})
	return OAuthStart{State: status, AuthorizationURL: response.AuthURL}, nil
}

func (s *CodexAppServer) StartDeviceLogin(ctx context.Context) (OAuthStart, error) {
	var response struct {
		Type            string `json:"type"`
		LoginID         string `json:"loginId"`
		VerificationURL string `json:"verificationUrl"`
		UserCode        string `json:"userCode"`
	}
	if err := s.call(ctx, "account/login/start", map[string]any{"type": "chatgptDeviceCode"}, &response); err != nil {
		return OAuthStart{}, fmt.Errorf("start Codex device OAuth: %w", err)
	}
	if response.LoginID == "" || response.VerificationURL == "" || response.UserCode == "" {
		return OAuthStart{}, errors.New("Codex App Server returned an incomplete device-code login")
	}
	status := s.setPendingLogin(codexLogin{mode: "device-code", loginID: response.LoginID, verificationURL: response.VerificationURL, userCode: response.UserCode, startedAt: time.Now()})
	return OAuthStart{State: status, VerificationURL: response.VerificationURL, UserCode: response.UserCode}, nil
}

func (s *CodexAppServer) setPendingLogin(login codexLogin) AuthStatus {
	s.mu.Lock()
	s.pendingLogin = &login
	s.mu.Unlock()
	return AuthStatus{
		Provider:        CodexAppServerProvider,
		Available:       true,
		Pending:         true,
		Mode:            login.mode,
		Detail:          map[bool]string{true: "Enter the device code to finish ChatGPT OAuth.", false: "Open the OAuth page to connect ChatGPT."}[login.mode == "device-code"],
		VerificationURL: login.verificationURL,
		UserCode:        login.userCode,
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func (s *CodexAppServer) OpenSession(ctx context.Context, options CodexAppSessionOptions) (*CodexAppSession, error) {
	if s == nil {
		return nil, errors.New("Codex App Server unavailable")
	}
	mcpServers, err := normalizeMCPServers(options.MCPServers)
	if err != nil {
		return nil, err
	}
	config, err := mergeCodexMCPServers(options.Config, mcpServers)
	if err != nil {
		return nil, err
	}
	var releaseThread func()
	if options.SessionID != "" {
		var err error
		releaseThread, err = s.acquireThread(ctx, options.SessionID)
		if err != nil {
			return nil, fmt.Errorf("wait for Codex App Server thread %q: %w", options.SessionID, err)
		}
		defer func() {
			if releaseThread != nil {
				releaseThread()
			}
		}()
	}
	workdir := options.Workdir
	if workdir == "" {
		workdir = s.Workdir
	}
	if workdir == "" {
		workdir = "."
	}
	absoluteWorkdir, err := filepath.Abs(workdir)
	if err != nil {
		return nil, fmt.Errorf("resolve Codex App Server workdir: %w", err)
	}
	if status, err := s.Status(ctx); err != nil {
		return nil, err
	} else if !status.Authenticated {
		return nil, errors.New("Codex App Server requires ChatGPT OAuth; connect it from OpenAgentFleet first")
	}
	webSearch, err := normalizeWebSearchMode(options.WebSearch)
	if err != nil {
		return nil, err
	}

	approvalPolicy := options.ApprovalPolicy
	if approvalPolicy == "" {
		approvalPolicy = "untrusted"
	}
	params := map[string]any{
		"cwd":            absoluteWorkdir,
		"approvalPolicy": approvalPolicy,
		"sandbox":        codexSandboxMode(options.AllowWorkspaceWrites),
		"serviceName":    "atlas_openagentfleet",
	}
	config["web_search"] = webSearch
	params["config"] = config
	if options.Model != "" {
		params["model"] = options.Model
	}
	if options.ServiceTier != "" && options.ServiceTier != "default" {
		params["serviceTier"] = options.ServiceTier
	}
	if options.DeveloperInstructions != "" {
		params["developerInstructions"] = options.DeveloperInstructions
	}
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	method := "thread/start"
	if options.SessionID != "" {
		method = "thread/resume"
		params["threadId"] = options.SessionID
	}
	if err := s.call(ctx, method, params, &response); err != nil {
		return nil, fmt.Errorf("%s Codex App Server thread: %w", method, err)
	}
	if response.Thread.ID == "" {
		return nil, errors.New("Codex App Server returned no thread id")
	}
	if options.SessionID != "" && response.Thread.ID != options.SessionID {
		return nil, fmt.Errorf("Codex App Server resumed unexpected thread %q (want %q)", response.Thread.ID, options.SessionID)
	}
	session := &CodexAppSession{
		server:        s,
		ID:            response.Thread.ID,
		Workdir:       absoluteWorkdir,
		model:         options.Model,
		effort:        options.ReasoningEffort,
		serviceTier:   options.ServiceTier,
		onNotify:      options.OnNotification,
		onApproval:    options.OnPermission,
		releaseThread: releaseThread,
	}
	if err := s.registerSession(session); err != nil {
		return nil, err
	}
	// The session now owns the gate for its complete lifetime, including its
	// turn and the interrupt issued by Close.
	releaseThread = nil
	return session, nil
}

// acquireThread waits without holding the server mutex, so unrelated native
// threads continue concurrently and a canceled caller cannot leave a waiter
// reference behind.
func (s *CodexAppServer) acquireThread(ctx context.Context, threadID string) (func(), error) {
	s.mu.Lock()
	gate := s.threadGates[threadID]
	if gate == nil {
		gate = &codexThreadGate{token: make(chan struct{}, 1)}
		gate.token <- struct{}{}
		s.threadGates[threadID] = gate
	}
	gate.refs++
	s.mu.Unlock()

	select {
	case <-gate.token:
	case <-ctx.Done():
		s.abandonThreadWait(threadID, gate)
		return nil, ctx.Err()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			s.releaseThread(threadID, gate)
		})
	}, nil
}

func (s *CodexAppServer) releaseThread(threadID string, gate *codexThreadGate) {
	s.mu.Lock()
	gate.refs--
	if gate.refs == 0 && s.threadGates[threadID] == gate {
		delete(s.threadGates, threadID)
	}
	s.mu.Unlock()
	gate.token <- struct{}{}
}

func (s *CodexAppServer) abandonThreadWait(threadID string, gate *codexThreadGate) {
	s.mu.Lock()
	gate.refs--
	if gate.refs == 0 && s.threadGates[threadID] == gate {
		delete(s.threadGates, threadID)
	}
	s.mu.Unlock()
}

func (s *CodexAppServer) registerSession(session *CodexAppSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if active := s.sessions[session.ID]; active != nil && active != session {
		return fmt.Errorf("Codex App Server thread %q already has an active session", session.ID)
	}
	s.sessions[session.ID] = session
	return nil
}

func codexSandboxMode(allowWorkspaceWrites bool) string {
	if allowWorkspaceWrites {
		return "workspace-write"
	}
	return "read-only"
}

func (s *CodexAppSession) Prompt(ctx context.Context, prompt string) (codexTurnResult, error) {
	s.promptMu.Lock()
	defer s.promptMu.Unlock()
	done := make(chan codexTurnResult, 1)
	s.turnMu.Lock()
	s.turnID = ""
	s.turnDone = done
	s.turnMu.Unlock()
	defer func() {
		s.turnMu.Lock()
		if s.turnDone == done {
			s.turnDone = nil
			s.turnID = ""
		}
		s.turnMu.Unlock()
	}()
	params := map[string]any{
		"threadId": s.ID,
		"input":    []map[string]string{{"type": "text", "text": prompt}},
	}
	if s.Workdir != "" {
		params["cwd"] = s.Workdir
	}
	if s.model != "" {
		params["model"] = s.model
	}
	if s.effort != "" {
		params["effort"] = s.effort
	}
	if s.serviceTier != "" && s.serviceTier != "default" {
		params["serviceTier"] = s.serviceTier
	}
	var response struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	if err := s.server.call(ctx, "turn/start", params, &response); err != nil {
		return codexTurnResult{}, fmt.Errorf("start Codex App Server turn: %w", err)
	}
	if response.Turn.ID == "" {
		return codexTurnResult{}, errors.New("Codex App Server returned no turn id")
	}
	s.turnMu.Lock()
	if s.turnDone == done {
		s.turnID = response.Turn.ID
	}
	s.turnMu.Unlock()
	if response.Turn.Status != "" && response.Turn.Status != "inProgress" {
		return codexTurnResult{Status: response.Turn.Status}, nil
	}
	select {
	case result := <-done:
		if result.Error != "" {
			return result, errors.New(result.Error)
		}
		return result, nil
	case <-ctx.Done():
		return codexTurnResult{}, ctx.Err()
	}
}

func (s *CodexAppSession) Close() error {
	if s == nil || s.server == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.turnMu.Lock()
		turnID := s.turnID
		s.turnMu.Unlock()
		if turnID != "" {
			interruptContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = s.server.call(interruptContext, "turn/interrupt", map[string]string{"threadId": s.ID, "turnId": turnID}, nil)
			cancel()
		}
		s.server.mu.Lock()
		// Do not erase a newer route if a caller has already replaced this
		// session (for example during shutdown or recovery).
		if s.server.sessions[s.ID] == s {
			delete(s.server.sessions, s.ID)
		}
		s.server.mu.Unlock()
		if s.releaseThread != nil {
			s.releaseThread()
		}
	})
	return nil
}

func (s *CodexAppSession) handleNotification(notification ACPNotification) {
	if notification.Method == "turn/completed" {
		var params struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"turn"`
		}
		if json.Unmarshal(notification.Params, &params) == nil && params.ThreadID == s.ID {
			s.turnMu.Lock()
			done, expectedID := s.turnDone, s.turnID
			s.turnMu.Unlock()
			if done != nil && (expectedID == "" || expectedID == params.Turn.ID) {
				result := codexTurnResult{Status: params.Turn.Status}
				if params.Turn.Error != nil {
					result.Error = params.Turn.Error.Message
				}
				select {
				case done <- result:
				default:
				}
			}
		}
	}
	if s.onNotify != nil {
		s.onNotify(notification)
	}
}

func (s *CodexAppSession) handleRequest(ctx context.Context, request rpcMessage, params map[string]any) (any, error) {
	approvalMethods := map[string]bool{
		"item/commandExecution/requestApproval": true,
		"item/fileChange/requestApproval":       true,
		"item/permissions/requestApproval":      true,
		"execCommandApproval":                   true,
		"applyPatchApproval":                    true,
	}
	if !approvalMethods[request.Method] {
		return nil, fmt.Errorf("unsupported Codex App Server request: %s", request.Method)
	}
	approved := false
	forSession := false
	if s.onApproval != nil {
		toolCall, _ := json.Marshal(map[string]any{"method": request.Method, "params": params})
		options, _ := json.Marshal([]map[string]string{
			{"optionId": "allow_once", "name": "Allow once", "kind": "allow_once"},
			{"optionId": "allow_session", "name": "Allow for session", "kind": "allow_always"},
		})
		decision, err := s.onApproval(ctx, PermissionRequest{SessionID: s.ID, Options: options, ToolCall: toolCall})
		if err != nil {
			return nil, err
		}
		approved = decision.Outcome == "selected" && strings.HasPrefix(decision.OptionID, "allow")
		forSession = decision.OptionID == "allow_session"
	}
	switch request.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		if approved {
			if forSession {
				return map[string]string{"decision": "acceptForSession"}, nil
			}
			return map[string]string{"decision": "accept"}, nil
		}
		return map[string]string{"decision": "decline"}, nil
	case "item/permissions/requestApproval":
		if approved {
			return map[string]any{"permissions": params["permissions"], "scope": map[bool]string{true: "session", false: "turn"}[forSession]}, nil
		}
		return map[string]any{"permissions": map[string]any{}}, nil
	case "execCommandApproval", "applyPatchApproval":
		if approved {
			if forSession {
				return map[string]string{"decision": "approved_for_session"}, nil
			}
			return map[string]string{"decision": "approved"}, nil
		}
		return map[string]any{"decision": map[string]any{"denied": map[string]string{"rejection": "Denied in OpenAgentFleet"}}}, nil
	default:
		return nil, fmt.Errorf("unsupported Codex App Server approval request: %s", request.Method)
	}
}

func (s *CodexAppServer) call(ctx context.Context, method string, params any, result any) error {
	client, err := s.ensure(ctx)
	if err != nil {
		return err
	}
	return client.call(ctx, method, params, result)
}

func (s *CodexAppServer) ensure(ctx context.Context) (*rpcClient, error) {
	if s == nil {
		return nil, errors.New("Codex App Server unavailable")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("Codex App Server is closed")
	}
	if s.client != nil {
		client := s.client
		s.mu.Unlock()
		select {
		case <-client.done:
			s.mu.Lock()
			if s.client == client {
				s.client = nil
			}
			s.mu.Unlock()
			return s.ensure(ctx)
		default:
			return client, nil
		}
	}
	if s.starting != nil {
		starting := s.starting
		s.mu.Unlock()
		select {
		case <-starting:
			return s.ensure(ctx)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	starting := make(chan struct{})
	s.starting = starting
	s.mu.Unlock()

	client, startErr := s.start(ctx)
	s.mu.Lock()
	if startErr == nil && !s.closed {
		s.client = client
	}
	s.starting = nil
	close(starting)
	closed := s.closed
	s.mu.Unlock()
	if startErr != nil {
		return nil, startErr
	}
	if closed {
		_ = client.close()
		return nil, errors.New("Codex App Server is closed")
	}
	go func() {
		<-client.done
		s.mu.Lock()
		if s.client == client {
			s.client = nil
		}
		s.mu.Unlock()
	}()
	return client, nil
}

func (s *CodexAppServer) start(ctx context.Context) (*rpcClient, error) {
	path, err := exec.LookPath(s.Binary)
	if err != nil {
		return nil, fmt.Errorf("find Codex App Server: %w", err)
	}
	workdir := s.Workdir
	if workdir == "" {
		workdir = "."
	}
	// Keep a successfully initialized server alive across requests/sessions;
	// only the initialization handshake is bounded by the caller's context.
	client, err := startRPC(context.Background(), CodexAppServerProvider, path, []string{"app-server", "--stdio"}, workdir, s.handleRequest, s.handleNotification)
	if err != nil {
		return nil, err
	}
	initializeContext, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	var initialize map[string]any
	err = client.call(initializeContext, "initialize", map[string]any{
		"clientInfo": map[string]string{
			"name":    "atlas_openagentfleet",
			"title":   "OpenAgentFleet",
			"version": "0.1.0",
		},
	}, &initialize)
	if err == nil {
		err = client.send(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})
	}
	if err != nil {
		_ = client.close()
		return nil, fmt.Errorf("initialize Codex App Server: %w", err)
	}
	return client, nil
}

func (s *CodexAppServer) handleNotification(notification ACPNotification) {
	if notification.Method == "account/login/completed" {
		var params struct {
			LoginID string `json:"loginId"`
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			s.mu.Lock()
			if login := s.pendingLogin; login != nil && login.loginID == params.LoginID {
				login.completed = true
				login.success = params.Success
				login.err = params.Error
			}
			s.mu.Unlock()
		}
	}
	var envelope struct {
		ThreadID string `json:"threadId"`
	}
	if json.Unmarshal(notification.Params, &envelope) != nil || envelope.ThreadID == "" {
		return
	}
	s.mu.Lock()
	session := s.sessions[envelope.ThreadID]
	s.mu.Unlock()
	if session != nil {
		session.handleNotification(notification)
	}
}

func (s *CodexAppServer) handleRequest(ctx context.Context, request rpcMessage) (any, error) {
	var params map[string]any
	if len(request.Params) > 0 && json.Unmarshal(request.Params, &params) != nil {
		return nil, errors.New("invalid Codex App Server request params")
	}
	threadID, _ := params["threadId"].(string)
	s.mu.Lock()
	session := s.sessions[threadID]
	s.mu.Unlock()
	if session == nil {
		return s.unroutedRequestResponse(request.Method, params)
	}
	return session.handleRequest(ctx, request, params)
}

func (s *CodexAppServer) unroutedRequestResponse(method string, params map[string]any) (any, error) {
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		return map[string]string{"decision": "decline"}, nil
	case "item/permissions/requestApproval":
		return map[string]any{"permissions": map[string]any{}}, nil
	case "execCommandApproval", "applyPatchApproval":
		return map[string]any{"decision": map[string]any{"denied": map[string]string{"rejection": "No active OpenAgentFleet run"}}}, nil
	case "account/chatgptAuthTokens/refresh":
		return nil, errors.New("OpenAgentFleet does not own ChatGPT OAuth tokens")
	default:
		return nil, fmt.Errorf("unsupported Codex App Server request: %s", method)
	}
}

func (r *Runner) runCodexAppServer(ctx context.Context, prompt, workdir string, options RunOptions) (string, error) {
	if r.CodexAppServer == nil {
		return "", errors.New("Codex App Server harness is unavailable")
	}
	var output bytes.Buffer
	var outputMu sync.Mutex
	appendOutput := func(line OutputLine) {
		line.Text = Redact(line.Text)
		options.OnLine(line)
		encoded, err := json.Marshal(map[string]any{"type": line.Type, "data": line.Text})
		if err == nil {
			outputMu.Lock()
			if output.Len() < maxCapturedOutput {
				_, _ = output.Write(encoded)
				_ = output.WriteByte('\n')
			}
			outputMu.Unlock()
		}
	}
	approvalPolicy, allowWorkspaceWrites, err := codexPermissionSettings(options.PermissionMode, r.AllowWorkspaceWrites)
	if err != nil {
		return "", err
	}
	reasoningEffort := options.ReasoningEffort
	if reasoningEffort == "default" {
		reasoningEffort = ""
	}
	session, err := r.CodexAppServer.OpenSession(ctx, CodexAppSessionOptions{
		Workdir:               workdir,
		SessionID:             options.SessionID,
		Model:                 options.Model,
		ReasoningEffort:       reasoningEffort,
		ServiceTier:           options.ServiceTier,
		WebSearch:             options.WebSearch,
		MCPServers:            options.MCPServers,
		DeveloperInstructions: options.SystemPrompt,
		ApprovalPolicy:        approvalPolicy,
		OnPermission:          options.OnPermission,
		AllowWorkspaceWrites:  allowWorkspaceWrites,
		OnNotification: func(notification ACPNotification) {
			appendCodexNotification(appendOutput, notification)
		},
	})
	if err != nil {
		return "", err
	}
	defer session.Close()
	if options.OnSession != nil {
		options.OnSession(session.ID)
	}
	_, err = session.Prompt(ctx, prompt)
	outputMu.Lock()
	result := output.String()
	outputMu.Unlock()
	return result, err
}

func codexPermissionSettings(mode string, globalWorkspaceWrites bool) (string, bool, error) {
	switch mode {
	case "", "default", "ask":
		return "untrusted", globalWorkspaceWrites, nil
	case "read_only", "plan":
		return "never", false, nil
	case "workspace":
		if !globalWorkspaceWrites {
			return "", false, errors.New("Codex workspace permission requires the host workspace-write gate")
		}
		return "on-request", true, nil
	case "auto":
		return "", false, errors.New("Codex App Server does not accept broad auto approval from an Agent profile")
	default:
		return "", false, fmt.Errorf("unsupported Codex permission mode %q", mode)
	}
}

func appendCodexNotification(appendOutput func(OutputLine), notification ACPNotification) {
	switch notification.Method {
	case "item/agentMessage/delta":
		var params struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal(notification.Params, &params) == nil && params.Delta != "" {
			appendOutput(OutputLine{Stream: "stdout", Type: "text", Text: params.Delta})
		}
	case "item/reasoningText/delta", "item/reasoningSummaryText/delta", "item/reasoningSummaryPart/added":
		// Keep an activity marker while never emitting hidden chain-of-thought.
		appendOutput(OutputLine{Stream: "stdout", Type: "thought", Text: "reasoning update"})
	case "turn/completed":
		// Completion is represented by the durable run lifecycle event.
	default:
		appendOutput(OutputLine{Stream: "stdout", Type: notification.Method, Text: string(notification.Params)})
	}
}
