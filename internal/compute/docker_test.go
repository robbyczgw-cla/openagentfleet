package compute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDisabledComputerDoesNotMutateDocker(t *testing.T) {
	docker := NewDocker(t.TempDir(), "", false)
	_, err := docker.Ensure(context.Background())
	if err != ErrExecutionDisabled {
		t.Fatalf("expected disabled error, got %v", err)
	}
	if err := docker.Stop(context.Background()); err != ErrExecutionDisabled {
		t.Fatalf("expected disabled stop error, got %v", err)
	}
	if _, err := docker.Exec(context.Background(), "id"); err != ErrExecutionDisabled {
		t.Fatalf("expected disabled exec error, got %v", err)
	}
}

func TestContainerRunUsesSeparateHostAndContainerPorts(t *testing.T) {
	workspace := "/tmp/openagentfleet-workspace"
	docker := NewDocker(workspace, "", true)
	docker.ViewPort = 19224
	docker.ContainerPort = 9223

	args := docker.containerRunArgs("control-token")
	var published string
	for index, arg := range args {
		if arg == "--publish" && index+1 < len(args) {
			published = args[index+1]
			break
		}
	}
	if published != "127.0.0.1:19224:9223" {
		t.Fatalf("published port = %q, want 127.0.0.1:19224:9223", published)
	}
	profileMount := "type=bind,source=" + docker.BrowserProfilePath + ",target=/home/agent/.chromium-profile"
	workspaceMount := "type=bind,source=" + workspace + ",target=/workspace"
	if docker.BrowserProfilePath == filepath.Join(workspace, ".browser-profile") {
		t.Fatalf("browser profile must not live inside workspace: %q", docker.BrowserProfilePath)
	}
	if !containsArg(args, profileMount) {
		t.Fatalf("container args do not mount controller-owned browser profile: %q", profileMount)
	}
	if !containsArg(args, workspaceMount) {
		t.Fatalf("container args do not mount workspace: %q", workspaceMount)
	}
}

func TestBrowserProfileIsPersistentAndOutsideWorkspaceMount(t *testing.T) {
	dataDir := t.TempDir()
	workspace := filepath.Join(dataDir, "agent-workspace")
	first := NewDocker(workspace, "", true)
	restarted := NewDocker(workspace, "", true)

	if first.BrowserProfilePath != restarted.BrowserProfilePath {
		t.Fatalf("browser profile changed across restart: %q != %q", first.BrowserProfilePath, restarted.BrowserProfilePath)
	}
	relative, err := filepath.Rel(workspace, first.BrowserProfilePath)
	if err != nil {
		t.Fatal(err)
	}
	if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("browser profile is inside bind-mounted workspace: workspace=%q profile=%q", workspace, first.BrowserProfilePath)
	}
}

func TestPrepareBrowserProfileMigratesLegacyProfileOutOfWorkspace(t *testing.T) {
	dataDir := t.TempDir()
	workspace := filepath.Join(dataDir, "agent-workspace")
	legacy := filepath.Join(workspace, ".browser-profile")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "Cookies"), []byte("persisted-session"), 0o600); err != nil {
		t.Fatal(err)
	}
	docker := NewDocker(workspace, "", true)
	profile, err := docker.prepareBrowserProfile()
	if err != nil {
		t.Fatal(err)
	}
	if profile != docker.BrowserProfilePath {
		t.Fatalf("profile path = %q, want %q", profile, docker.BrowserProfilePath)
	}
	if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy workspace profile still exists: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(profile, "Cookies"))
	if err != nil || string(contents) != "persisted-session" {
		t.Fatalf("migrated profile contents = %q, err=%v", contents, err)
	}
	info, err := os.Stat(profile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("browser profile permissions = %o, want 700", info.Mode().Perm())
	}
}

func TestPrepareBrowserProfileRejectsWorkspacePath(t *testing.T) {
	workspace := t.TempDir()
	docker := NewDocker(workspace, "", true)
	docker.BrowserProfilePath = filepath.Join(workspace, "browser-profile")
	if _, err := docker.prepareBrowserProfile(); err == nil {
		t.Fatal("workspace browser profile path was accepted")
	}
}

func TestControllerBrowserProfileMountInspectionMatchesOnlyExpectedPath(t *testing.T) {
	stateDir := t.TempDir()
	profilePath := filepath.Join(stateDir, "browser-profile")
	scriptPath := filepath.Join(t.TempDir(), "fake-docker")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nprintf '%s' \"$OPENAGENTFLEET_TEST_PROFILE_PATH\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{Binary: scriptPath, ContainerName: "agent-computer"}
	t.Setenv("OPENAGENTFLEET_TEST_PROFILE_PATH", filepath.Join(stateDir, "nested", "..", "browser-profile"))
	if !docker.usesControllerBrowserProfile(context.Background(), profilePath) {
		t.Fatal("normalized controller browser profile mount was not recognized")
	}
	t.Setenv("OPENAGENTFLEET_TEST_PROFILE_PATH", filepath.Join(stateDir, "other-profile"))
	if docker.usesControllerBrowserProfile(context.Background(), profilePath) {
		t.Fatal("unexpected browser profile mount was accepted")
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func TestBrowserActionValidationKeepsNavigationAndInputBounded(t *testing.T) {
	valid := []BrowserAction{
		{Action: "navigate", URL: "https://example.com/path"},
		{Action: "click", X: 12, Y: 24},
		{Action: "type", Text: "hello", Sensitive: true},
		{Action: "press", Key: "Enter"},
		{Action: "scroll", DeltaY: 600},
		{Action: "reload"},
	}
	for _, action := range valid {
		if err := validateBrowserAction(action); err != nil {
			t.Errorf("valid action %#v rejected: %v", action, err)
		}
	}
	invalid := []BrowserAction{
		{Action: "navigate", URL: "file:///etc/passwd"},
		{Action: "navigate", URL: "javascript:alert(1)"},
		{Action: "click", X: -1, Y: 4},
		{Action: "press"},
		{Action: "scroll", DeltaY: 10001},
		{Action: "unknown"},
	}
	for _, action := range invalid {
		if err := validateBrowserAction(action); err == nil {
			t.Errorf("invalid action %#v was accepted", action)
		}
	}
}

func TestDesktopActionValidationStaysWithinTheVirtualDisplay(t *testing.T) {
	if err := validateDesktopAction(BrowserAction{Action: "click", X: 50, Y: 50}); err != nil {
		t.Fatalf("valid desktop click rejected: %v", err)
	}
	for _, action := range []BrowserAction{{Action: "navigate", URL: "https://example.com"}, {Action: "click", X: -1, Y: 5}, {Action: "type", Text: string(make([]byte, 4097))}} {
		if err := validateDesktopAction(action); err == nil {
			t.Errorf("invalid desktop action accepted: %#v", action)
		}
	}
}

func TestSensitiveTypeUsesBoundedInternalActionPayload(t *testing.T) {
	var received struct {
		Action        string `json:"action"`
		Text          string `json:"text"`
		Sensitive     bool   `json:"sensitive"`
		NativeHandoff bool   `json:"native_handoff"`
		ComputerID    string `json:"computer_id"`
		TargetID      string `json:"target_id"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/action" {
			t.Errorf("action path = %q", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &received); err != nil {
			t.Fatalf("decode action: %v, body=%q", err, body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ready":true,"url":"https://example.test","title":"Ready","viewport":{"width":1440,"height":900}}`))
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port := parsed.Port()
	if port == "" {
		t.Fatal("test server has no port")
	}
	var viewPort int
	if _, err := fmt.Sscanf(port, "%d", &viewPort); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{ViewPort: viewPort}
	secret := []byte("safe\\\" unicode ✓\n")
	binding := TargetBinding{ComputerID: "computer-123", TargetID: "window-456"}
	view, err := docker.SensitiveType(context.Background(), "browser", binding, secret)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Ready || received.Action != "type" || !received.Sensitive || !received.NativeHandoff || received.Text != string(secret) || received.ComputerID != binding.ComputerID || received.TargetID != binding.TargetID {
		t.Fatalf("sensitive action view=%#v payload=%#v", view, received)
	}
	for _, invalid := range [][]byte{nil, make([]byte, 4097), {0xff}} {
		if _, err := docker.SensitiveType(context.Background(), "browser", binding, invalid); err == nil {
			t.Errorf("invalid sensitive input was accepted: %q", invalid)
		}
	}
	if _, err := docker.SensitiveType(context.Background(), "desktop", binding, []byte("safe")); err == nil {
		t.Error("desktop sensitive input was accepted")
	}
	if _, err := docker.SensitiveType(context.Background(), "browser", TargetBinding{}, []byte("safe")); err == nil {
		t.Error("missing target binding was accepted")
	}
}

func TestTargetBindingReadsOnlyNonSecretRuntimeMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/target" || r.URL.Query().Get("surface") != "browser" {
			t.Fatalf("target request = %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"computer_id":"computer-123","target_id":"tab-456"}`))
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	var viewPort int
	if _, err := fmt.Sscanf(parsed.Port(), "%d", &viewPort); err != nil {
		t.Fatal(err)
	}
	binding, err := (&Docker{ViewPort: viewPort}).TargetBinding(context.Background(), "browser")
	if err != nil || binding != (TargetBinding{ComputerID: "computer-123", TargetID: "tab-456"}) {
		t.Fatalf("TargetBinding = %#v, %v", binding, err)
	}
}

func TestControlRequestsCarryPerComputerCapabilityToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-control-token" {
			t.Fatalf("control authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ready":true,"url":"https://example.test","title":"Ready","viewport":{"width":1440,"height":900}}`))
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	var viewPort int
	if _, err := fmt.Sscanf(parsed.Port(), "%d", &viewPort); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{ViewPort: viewPort, controlToken: "test-control-token"}
	if _, err := docker.ViewStatus(context.Background()); err != nil {
		t.Fatalf("ViewStatus: %v", err)
	}
}

func TestConfigureRemoteRequiresHTTPSOutsideLoopback(t *testing.T) {
	docker := NewDocker(t.TempDir(), "", true)
	if err := docker.ConfigureRemote("https://worker.example", "short"); err == nil {
		t.Fatal("short remote token was accepted")
	}
	if err := docker.ConfigureRemote("http://worker.example", strings.Repeat("x", 32)); err == nil {
		t.Fatal("non-loopback HTTP remote URL was accepted")
	}
	if err := docker.ConfigureRemote("https://user:secret@worker.example", strings.Repeat("x", 32)); err == nil {
		t.Fatal("credential-bearing remote URL was accepted")
	}
	if err := docker.ConfigureRemote("https://worker.example/computer/", strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
	if docker.RemoteBaseURL != "https://worker.example/computer" {
		t.Fatalf("remote base URL = %q", docker.RemoteBaseURL)
	}
	if err := docker.ConfigureRemote("http://127.0.0.1:9323", strings.Repeat("x", 32)); err != nil {
		t.Fatalf("loopback HTTP test URL rejected: %v", err)
	}
}

func TestRemoteRequestPreservesPathPrefixAndBearerToken(t *testing.T) {
	const token = "0123456789abcdefghijklmnopqrstuvwxyz-remote-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/computer/health" {
			t.Fatalf("remote path = %q", r.URL.Path)
		}
		if r.URL.RawQuery != "" {
			t.Fatalf("unexpected remote query = %q", r.URL.RawQuery)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Fatalf("remote authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ready":true,"url":"https://example.test","title":"Ready","viewport":{"width":1440,"height":900}}`)
	}))
	defer server.Close()
	docker := &Docker{RemoteBaseURL: server.URL + "/computer", RemoteToken: token}
	view, err := docker.ViewStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !view.Ready || view.ViewportWidth != 1440 {
		t.Fatalf("remote view = %#v", view)
	}
}

func TestRemoteStatusMarksTransientWorkerFailuresRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			t.Fatalf("remote status path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	docker := &Docker{RemoteBaseURL: server.URL, RemoteToken: strings.Repeat("x", 32)}
	status := docker.Status(context.Background())
	if status.State != ComputerStateError || !status.CanRetry || status.Available {
		t.Fatalf("remote transient status = %#v, want retryable error", status)
	}
}

func TestRemoteRequestRefusesRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	docker := &Docker{RemoteBaseURL: redirect.URL, RemoteToken: strings.Repeat("x", 32)}
	if _, err := docker.remoteRequest(context.Background(), http.MethodGet, "/status", nil); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("redirect was not refused: %v", err)
	}
}

func TestRemoteSensitiveTypeFailsClosed(t *testing.T) {
	docker := &Docker{RemoteBaseURL: "https://worker.example", RemoteToken: strings.Repeat("x", 32)}
	_, err := docker.SensitiveType(context.Background(), "browser", TargetBinding{ComputerID: "computer", TargetID: "target"}, []byte("secret"))
	if err == nil || !strings.Contains(err.Error(), "local-only") {
		t.Fatalf("remote sensitive handoff was not rejected: %v", err)
	}
}

func TestNewControlTokenIsHighEntropyAndUnique(t *testing.T) {
	first, err := newControlToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newControlToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) < 40 || first == second {
		t.Fatalf("control tokens are invalid: %q, %q", first, second)
	}
}

func TestControlTokenPersistsPrivatelyAndRestoresAfterDaemonRestart(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(dataDir, "agent-workspace")
	first := NewDocker(workspace, "", true)
	token, err := newControlToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := first.persistControlToken(token); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(first.ControlTokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("control token permissions = %o, want 600", info.Mode().Perm())
	}
	if filepath.Dir(first.ControlTokenPath) == workspace {
		t.Fatalf("control token must not be inside bind-mounted workspace: %q", first.ControlTokenPath)
	}

	restarted := NewDocker(workspace, "", true)
	if err := restarted.restoreControlToken(); err != nil {
		t.Fatal(err)
	}
	if got := restarted.controlTokenValue(); got != token {
		t.Fatalf("restored token = %q, want %q", got, token)
	}
	if err := restarted.clearControlToken(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(restarted.ControlTokenPath); !os.IsNotExist(err) {
		t.Fatalf("control token should be removed, stat err = %v", err)
	}
}
