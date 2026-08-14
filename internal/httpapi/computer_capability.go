package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/browsermcp"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
)

const computerCapabilityBytes = 32

const (
	defaultComputerCapabilityTTL = 15 * time.Minute
	maxComputerCapabilityTTL     = time.Hour + time.Minute
)

type computerCapability struct {
	runID     string
	expiresAt time.Time
}

// newComputerCapability is deliberately independent from botd's long-lived
// bearer token. The returned value is injected into one MCP child only and is
// never persisted or included in a prompt.
func newComputerCapability() (string, error) {
	var raw [computerCapabilityBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func (s *Server) bindComputerCapability(token, runID string, ttl ...time.Duration) {
	token = strings.TrimSpace(token)
	runID = strings.TrimSpace(runID)
	if token == "" || runID == "" {
		return
	}
	duration := defaultComputerCapabilityTTL
	if len(ttl) > 0 && ttl[0] > 0 {
		duration = ttl[0]
	}
	if duration > maxComputerCapabilityTTL {
		duration = maxComputerCapabilityTTL
	}
	s.computerCapabilityMu.Lock()
	defer s.computerCapabilityMu.Unlock()
	if s.computerCapabilities == nil {
		s.computerCapabilities = make(map[string]computerCapability)
	}
	s.computerCapabilities[token] = computerCapability{runID: runID, expiresAt: time.Now().UTC().Add(duration)}
}

func (s *Server) revokeComputerCapability(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	s.computerCapabilityMu.Lock()
	delete(s.computerCapabilities, token)
	s.computerCapabilityMu.Unlock()
}

func computerCapabilityFromMCPServers(servers []harness.MCPServerSpec) string {
	for _, server := range servers {
		if server.Name != browsermcp.MCPServerName {
			continue
		}
		return strings.TrimSpace(server.Env[browsermcp.RunTokenEnv])
	}
	return ""
}

func setComputerRunID(servers []harness.MCPServerSpec, runID string) {
	for index := range servers {
		if servers[index].Name == browsermcp.MCPServerName {
			if servers[index].Env == nil {
				servers[index].Env = make(map[string]string)
			}
			servers[index].Env[browsermcp.RunIDEnv] = runID
		}
	}
}

func (s *Server) computerCapabilityValid(r *http.Request) bool {
	token := strings.TrimSpace(r.Header.Get(browsermcp.RunTokenHeader))
	runID := strings.TrimSpace(r.Header.Get(browsermcp.RunIDHeader))
	if token == "" || runID == "" {
		return false
	}
	s.computerCapabilityMu.Lock()
	capability, ok := s.computerCapabilities[token]
	if !ok || capability.runID != runID || !time.Now().UTC().Before(capability.expiresAt) {
		if ok {
			delete(s.computerCapabilities, token)
		}
		s.computerCapabilityMu.Unlock()
		return false
	}
	s.computerCapabilityMu.Unlock()
	if s.Store != nil {
		current, err := s.Store.GetRun(r.Context(), runID)
		if err != nil || terminalRunStatus(current.Status) {
			return false
		}
	}
	return true
}

func (s *Server) releaseComputerCapability(servers []harness.MCPServerSpec) {
	s.computerActionMu.Lock()
	defer s.computerActionMu.Unlock()
	s.revokeComputerCapability(computerCapabilityFromMCPServers(servers))
}
