package websearchplus

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const connectorStateFilename = "search-connectors.json"

// ConnectorSettings is the durable, credential-free optional connector state.
// Web Search Plus and Hound are independent and both default to disabled.
type ConnectorSettings struct {
	WebSearchPlusEnabled bool `json:"web_search_plus_enabled"`
	HoundEnabled         bool `json:"hound_enabled"`
}

// ConnectorPatch applies only fields that are non-nil.
type ConnectorPatch struct {
	WebSearchPlusEnabled *bool
	HoundEnabled         *bool
}

// ControllerStatus preserves Manager's status shape and adds the explicit
// persisted toggles consumed by the HTTP API. CredentialStatus is metadata
// only; this controller never accepts or stores credentials.
type ControllerStatus struct {
	Status
	WebSearchPlusEnabled          bool   `json:"web_search_plus_enabled"`
	HoundEnabled                  bool   `json:"hound_enabled"`
	WebSearchPlusCredentialStatus string `json:"web_search_plus_credential_status"`
}

// Controller owns the durable connector toggles and an immutable Manager
// configured from their latest committed snapshot. Replacing the Manager under
// the lock keeps concurrent status probes and patches race-free without making
// runtime configuration mutable.
type Controller struct {
	mu       sync.RWMutex
	stateDir string
	settings ConnectorSettings
	manager  *Manager
}

// NewController loads persisted connector settings without probing, preparing
// configuration, downloading packages, or starting processes.
func NewController(stateDir string) (*Controller, error) {
	if !filepath.IsAbs(stateDir) {
		return nil, errors.New("websearchplus: controller state directory must be absolute")
	}
	stateDir = filepath.Clean(stateDir)
	settings, err := loadConnectorSettings(stateDir)
	if err != nil {
		return nil, err
	}
	manager, err := managerForSettings(stateDir, settings)
	if err != nil {
		return nil, err
	}
	return &Controller{stateDir: stateDir, settings: settings, manager: manager}, nil
}

// Status returns one coherent settings and Manager snapshot. Manager.Status
// performs bounded local probes only and never launches a pinned package.
func (c *Controller) Status(ctx context.Context) ControllerStatus {
	if c == nil {
		return ControllerStatus{WebSearchPlusCredentialStatus: "external/not inspected"}
	}
	c.mu.RLock()
	settings := c.settings
	manager := c.manager
	c.mu.RUnlock()
	return controllerStatus(ctx, manager, settings)
}

// MCPServerSpecs returns a defensive snapshot of the enabled connectors that
// are locally ready to be injected into one lead-harness run. Package startup
// and the MCP initialize handshake remain the responsibility of that harness.
func (c *Controller) MCPServerSpecs(ctx context.Context) ([]MCPServerSpec, error) {
	if c == nil {
		return nil, errors.New("websearchplus: nil controller")
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	manager := c.manager
	if manager == nil {
		return nil, errors.New("websearchplus: connector manager unavailable")
	}
	return manager.MCPServerSpecs(ctx)
}

// Patch atomically persists a merged settings snapshot before publishing its
// replacement Manager in memory. An empty patch is a read-only status request.
func (c *Controller) Patch(ctx context.Context, patch ConnectorPatch) (ControllerStatus, error) {
	if c == nil {
		return ControllerStatus{}, errors.New("websearchplus: nil controller")
	}
	c.mu.Lock()
	if patch.WebSearchPlusEnabled == nil && patch.HoundEnabled == nil {
		settings := c.settings
		manager := c.manager
		c.mu.Unlock()
		return controllerStatus(ctx, manager, settings), nil
	}

	next := c.settings
	if patch.WebSearchPlusEnabled != nil {
		next.WebSearchPlusEnabled = *patch.WebSearchPlusEnabled
	}
	if patch.HoundEnabled != nil {
		next.HoundEnabled = *patch.HoundEnabled
	}
	manager, err := managerForSettings(c.stateDir, next)
	if err != nil {
		c.mu.Unlock()
		return ControllerStatus{}, err
	}
	if err := persistConnectorSettings(c.stateDir, next); err != nil {
		c.mu.Unlock()
		return ControllerStatus{}, err
	}
	c.settings = next
	c.manager = manager
	c.mu.Unlock()
	return controllerStatus(ctx, manager, next), nil
}

func controllerStatus(ctx context.Context, manager *Manager, settings ConnectorSettings) ControllerStatus {
	status := Status{}
	if manager != nil {
		status = manager.Status(ctx)
	}
	return ControllerStatus{
		Status:                        status,
		WebSearchPlusEnabled:          settings.WebSearchPlusEnabled,
		HoundEnabled:                  settings.HoundEnabled,
		WebSearchPlusCredentialStatus: "external/not inspected",
	}
}

func managerForSettings(stateDir string, settings ConnectorSettings) (*Manager, error) {
	return New(Config{
		StateDir:            stateDir,
		EnableWebSearchPlus: settings.WebSearchPlusEnabled,
		EnableHound:         settings.HoundEnabled,
	})
}

func loadConnectorSettings(stateDir string) (ConnectorSettings, error) {
	root, exists, err := openStateRoot(stateDir, false)
	if err != nil || !exists {
		return ConnectorSettings{}, err
	}
	defer root.Close()

	info, err := root.Lstat(connectorStateFilename)
	if errors.Is(err, os.ErrNotExist) {
		return ConnectorSettings{}, nil
	}
	if err != nil {
		return ConnectorSettings{}, fmt.Errorf("websearchplus: inspect connector state: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ConnectorSettings{}, errors.New("websearchplus: connector state must be a regular file, not a symlink")
	}
	file, err := root.Open(connectorStateFilename)
	if err != nil {
		return ConnectorSettings{}, fmt.Errorf("websearchplus: open connector state: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return ConnectorSettings{}, fmt.Errorf("websearchplus: inspect opened connector state: %w", err)
	}
	if !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() {
		return ConnectorSettings{}, errors.New("websearchplus: connector state changed while opening")
	}
	payload, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil {
		return ConnectorSettings{}, fmt.Errorf("websearchplus: read connector state: %w", err)
	}
	if len(payload) > 64<<10 {
		return ConnectorSettings{}, errors.New("websearchplus: connector state exceeds size limit")
	}
	var settings ConnectorSettings
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return ConnectorSettings{}, fmt.Errorf("websearchplus: decode connector state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ConnectorSettings{}, errors.New("websearchplus: connector state contains an additional JSON value")
	}
	return settings, nil
}

func persistConnectorSettings(stateDir string, settings ConnectorSettings) error {
	payload, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("websearchplus: encode connector state: %w", err)
	}
	payload = append(payload, '\n')
	root, _, err := openStateRoot(stateDir, true)
	if err != nil {
		return err
	}
	defer root.Close()

	if info, inspectErr := root.Lstat(connectorStateFilename); inspectErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("websearchplus: connector state must be a regular file, not a symlink")
		}
	} else if !errors.Is(inspectErr, os.ErrNotExist) {
		return fmt.Errorf("websearchplus: inspect connector state: %w", inspectErr)
	}

	temporaryName, err := randomTemporaryName()
	if err != nil {
		return fmt.Errorf("websearchplus: create connector state temporary name: %w", err)
	}
	temporary, err := root.OpenFile(temporaryName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("websearchplus: create connector state temporary file: %w", err)
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = root.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("websearchplus: protect connector state temporary file: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("websearchplus: write connector state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("websearchplus: sync connector state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("websearchplus: close connector state: %w", err)
	}
	if err := root.Rename(temporaryName, connectorStateFilename); err != nil {
		return fmt.Errorf("websearchplus: replace connector state: %w", err)
	}
	removeTemporary = false
	info, err := root.Lstat(connectorStateFilename)
	if err != nil {
		return fmt.Errorf("websearchplus: inspect persisted connector state: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return errors.New("websearchplus: persisted connector state is not a private regular file")
	}
	if directory, openErr := root.Open("."); openErr == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func openStateRoot(stateDir string, create bool) (*os.Root, bool, error) {
	info, err := os.Lstat(stateDir)
	if errors.Is(err, os.ErrNotExist) && !create {
		return nil, false, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(stateDir, 0o700); err != nil {
			return nil, false, fmt.Errorf("websearchplus: create controller state directory: %w", err)
		}
		info, err = os.Lstat(stateDir)
	}
	if err != nil {
		return nil, false, fmt.Errorf("websearchplus: inspect controller state directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, false, errors.New("websearchplus: controller state directory must be a directory, not a symlink")
	}
	root, err := os.OpenRoot(stateDir)
	if err != nil {
		return nil, false, fmt.Errorf("websearchplus: open controller state directory: %w", err)
	}
	openedInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(info, openedInfo) {
		_ = root.Close()
		return nil, false, errors.New("websearchplus: controller state directory changed while opening")
	}
	return root, true, nil
}

func randomTemporaryName() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return ".search-connectors-" + hex.EncodeToString(value[:]), nil
}
