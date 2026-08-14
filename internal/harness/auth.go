package harness

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// AuthStatus is deliberately metadata-only. OAuth credentials remain owned by
// the provider CLI or App Server and are never returned to Atlas.
type AuthStatus struct {
	Provider        string `json:"provider"`
	Available       bool   `json:"available"`
	Authenticated   bool   `json:"authenticated"`
	LoginRequired   bool   `json:"login_required"`
	Pending         bool   `json:"pending"`
	Mode            string `json:"mode,omitempty"`
	Plan            string `json:"plan,omitempty"`
	Detail          string `json:"detail,omitempty"`
	VerificationURL string `json:"verification_url,omitempty"`
	UserCode        string `json:"user_code,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

type OAuthStart struct {
	State            AuthStatus `json:"state"`
	AuthorizationURL string     `json:"authorization_url,omitempty"`
	VerificationURL  string     `json:"verification_url,omitempty"`
	UserCode         string     `json:"user_code,omitempty"`
}

type GrokOAuthManager struct {
	Binary  string
	Workdir string

	mu          sync.Mutex
	login       *grokLogin
	lastStatus  AuthStatus
	lastChecked time.Time
}

type grokLogin struct {
	mode            string
	startedAt       time.Time
	process         *exec.Cmd
	verificationURL string
	userCode        string
	finished        bool
	err             error
}

var (
	httpsURLPattern       = regexp.MustCompile(`https?://[^\s"<>]+`)
	grokDeviceCodePattern = regexp.MustCompile(`(?i)(?:code|enter)\s*(?:is|:)?\s*([A-Z0-9]{4,}(?:-[A-Z0-9]{3,})+)`)
)

func NewGrokOAuthManager(binary, workdir string) *GrokOAuthManager {
	if binary == "" {
		binary = "grok"
	}
	return &GrokOAuthManager{Binary: binary, Workdir: workdir}
}

func (m *GrokOAuthManager) Status(ctx context.Context) (AuthStatus, error) {
	if m == nil {
		return AuthStatus{Provider: "grok", Detail: "Grok OAuth manager unavailable"}, errors.New("Grok OAuth manager unavailable")
	}
	if _, err := exec.LookPath(m.Binary); err != nil {
		return AuthStatus{Provider: "grok", Detail: "Grok Build was not found in PATH", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
	}

	m.mu.Lock()
	if m.login != nil && !m.login.finished {
		status := m.loginStatusLocked()
		m.mu.Unlock()
		return status, nil
	}
	if !m.lastChecked.IsZero() && time.Since(m.lastChecked) < 8*time.Second {
		status := m.lastStatus
		m.mu.Unlock()
		return status, nil
	}
	m.mu.Unlock()

	probeContext, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	connected, err := grokCachedOAuth(probeContext, m.Binary, m.Workdir)
	status := AuthStatus{
		Provider:      "grok",
		Available:     true,
		Authenticated: connected,
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err != nil {
		status.Detail = "OAuth status could not be verified: " + compactAuthError(err)
	} else if connected {
		status.Mode = "oauth"
		status.Detail = "Grok Build OAuth connected"
	} else {
		status.LoginRequired = true
		status.Detail = "Connect Grok Build with OAuth"
	}

	m.mu.Lock()
	m.lastStatus = status
	m.lastChecked = time.Now()
	m.mu.Unlock()
	return status, nil
}

func (m *GrokOAuthManager) StartBrowserLogin(ctx context.Context) (OAuthStart, error) {
	return m.start(ctx, "browser")
}

func (m *GrokOAuthManager) StartDeviceLogin(ctx context.Context) (OAuthStart, error) {
	return m.start(ctx, "device")
}

func (m *GrokOAuthManager) start(ctx context.Context, mode string) (OAuthStart, error) {
	if m == nil {
		return OAuthStart{}, errors.New("Grok OAuth manager unavailable")
	}
	path, err := exec.LookPath(m.Binary)
	if err != nil {
		return OAuthStart{}, fmt.Errorf("find Grok Build: %w", err)
	}
	m.mu.Lock()
	if m.login != nil && !m.login.finished {
		status := m.loginStatusLocked()
		m.mu.Unlock()
		return OAuthStart{State: status, VerificationURL: status.VerificationURL, UserCode: status.UserCode}, nil
	}
	args := []string{"login"}
	if mode == "device" {
		args = append(args, "--device-auth")
	} else {
		args = append(args, "--oauth")
	}
	// Login helpers intentionally receive no ambient API key. OAuth state and
	// provider configuration remain available through the inherited HOME.
	process := newIsolatedCommand("", path, args...)
	if m.Workdir != "" {
		process.Dir = m.Workdir
	}
	stdout, err := process.StdoutPipe()
	if err != nil {
		m.mu.Unlock()
		return OAuthStart{}, fmt.Errorf("capture Grok OAuth stdout: %w", err)
	}
	stderr, err := process.StderrPipe()
	if err != nil {
		m.mu.Unlock()
		return OAuthStart{}, fmt.Errorf("capture Grok OAuth stderr: %w", err)
	}
	if err := process.Start(); err != nil {
		m.mu.Unlock()
		return OAuthStart{}, fmt.Errorf("start Grok OAuth: %w", err)
	}
	login := &grokLogin{mode: mode, startedAt: time.Now(), process: process}
	m.login = login
	m.lastChecked = time.Time{}
	m.mu.Unlock()

	go m.captureLoginOutput(login, stdout)
	go m.captureLoginOutput(login, stderr)
	go func() {
		err := process.Wait()
		m.mu.Lock()
		if m.login == login {
			login.finished = true
			login.err = err
			m.lastChecked = time.Time{}
		}
		m.mu.Unlock()
	}()

	// Device code output is normally available immediately, but the endpoint
	// remains useful even if a slow network delays it: Atlas polls Status.
	if mode == "device" {
		waitContext, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
		defer cancel()
		for {
			m.mu.Lock()
			status := m.loginStatusLocked()
			m.mu.Unlock()
			if status.VerificationURL != "" || status.UserCode != "" {
				return OAuthStart{State: status, VerificationURL: status.VerificationURL, UserCode: status.UserCode}, nil
			}
			select {
			case <-waitContext.Done():
				return OAuthStart{State: status}, nil
			case <-time.After(75 * time.Millisecond):
			}
		}
	}

	status, _ := m.Status(ctx)
	if status.Pending {
		return OAuthStart{State: status}, nil
	}
	return OAuthStart{State: AuthStatus{Provider: "grok", Available: true, Pending: true, Mode: "oauth", Detail: "Grok Build opened OAuth in the default browser on this Mac.", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}}, nil
}

func (m *GrokOAuthManager) captureLoginOutput(login *grokLogin, reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 256*1024)
	for scanner.Scan() {
		line := scanner.Text()
		url := httpsURLPattern.FindString(line)
		codeMatch := grokDeviceCodePattern.FindStringSubmatch(line)
		m.mu.Lock()
		if m.login != login {
			m.mu.Unlock()
			return
		}
		if url != "" && (login.mode == "device" || strings.Contains(strings.ToLower(line), "device")) {
			login.verificationURL = url
		}
		if len(codeMatch) == 2 {
			login.userCode = strings.ToUpper(codeMatch[1])
		}
		m.mu.Unlock()
	}
}

func (m *GrokOAuthManager) loginStatusLocked() AuthStatus {
	status := AuthStatus{
		Provider:        "grok",
		Available:       true,
		Pending:         true,
		Mode:            "oauth",
		VerificationURL: m.login.verificationURL,
		UserCode:        m.login.userCode,
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
	}
	if m.login.mode == "device" {
		status.Mode = "device-code"
		if status.UserCode != "" {
			status.Detail = "Enter the device code to finish Grok Build OAuth."
		} else {
			status.Detail = "Waiting for Grok Build device code…"
		}
	} else {
		status.Detail = "Grok Build OAuth is open in the default browser on this Mac."
	}
	if m.login.finished {
		status.Pending = false
		if m.login.err != nil {
			status.Detail = "Grok Build OAuth ended: " + compactAuthError(m.login.err)
		} else {
			status.Detail = "Grok Build OAuth completed; verifying saved session…"
		}
	}
	return status
}

func grokCachedOAuth(ctx context.Context, binary, workdir string) (bool, error) {
	path, err := exec.LookPath(binary)
	if err != nil {
		return false, err
	}
	// This probe verifies the cached OAuth method specifically, so do not let an
	// ambient XAI_API_KEY influence the child process.
	client, err := startRPC(ctx, "", path, []string{"agent", "--no-leader", "stdio"}, workdir, nil, nil)
	if err != nil {
		return false, err
	}
	defer client.close()
	var initialize struct {
		AuthMethods []struct {
			ID string `json:"id"`
		} `json:"authMethods"`
	}
	if err := client.call(ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
	}, &initialize); err != nil {
		return false, fmt.Errorf("initialize Grok ACP: %w", err)
	}
	for _, method := range initialize.AuthMethods {
		if method.ID != "cached_token" {
			continue
		}
		if err := client.call(ctx, "authenticate", map[string]any{"methodId": method.ID, "_meta": map[string]any{"headless": true}}, nil); err != nil {
			return false, fmt.Errorf("authenticate cached Grok OAuth token: %w", err)
		}
		return true, nil
	}
	return false, nil
}

func compactAuthError(err error) string {
	if err == nil {
		return ""
	}
	value := Redact(err.Error())
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 180 {
		return value[:180]
	}
	return value
}
