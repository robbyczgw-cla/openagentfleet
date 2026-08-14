package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/compute"
	"github.com/robbyczgw-cla/openagentfleet/internal/events"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
	"github.com/robbyczgw-cla/openagentfleet/internal/httpapi"
	"github.com/robbyczgw-cla/openagentfleet/internal/integrations"
	"github.com/robbyczgw-cla/openagentfleet/internal/secrethandoff"
	"github.com/robbyczgw-cla/openagentfleet/internal/skillworkshop"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
	"github.com/robbyczgw-cla/openagentfleet/internal/stt"
	"github.com/robbyczgw-cla/openagentfleet/internal/teach"
	"github.com/robbyczgw-cla/openagentfleet/internal/websearchplus"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	addr := flag.String("addr", envOr("OPENAGENTFLEET_ADDR", "127.0.0.1:4317"), "local API address")
	mobileAddr := flag.String("mobile-addr", envOr("OPENAGENTFLEET_MOBILE_ADDR", "127.0.0.1:4318"), "private mobile API address (loopback only)")
	dataDir := flag.String("data-dir", envOr("OPENAGENTFLEET_DATA_DIR", ".openagentfleet-data"), "durable local data directory")
	workspace := flag.String("workspace", "", "Agent Computer workspace; defaults below data-dir")
	buildContext := flag.String("build-context", "runtime/agent-computer", "Docker build context for the Agent Computer image")
	teachRoot := flag.String("teach-root", envOr("OPENAGENTFLEET_TEACH_ROOT", ""), "Teach a Task trace directory; defaults below data-dir")
	computerPortText := flag.String("computer-port", envOr("OPENAGENTFLEET_COMPUTER_PORT", "9223"), "host port for the Agent Computer control proxy")
	flag.Parse()
	remoteToken := strings.TrimSpace(os.Getenv("OPENAGENTFLEET_REMOTE_TOKEN"))
	if err := requireRemoteTokenForNonLoopbackAddr(*addr, remoteToken); err != nil {
		log.Error("validate API address", "addr", *addr, "error", err)
		os.Exit(1)
	}
	computerPort, err := strconv.Atoi(*computerPortText)
	if err != nil || computerPort < 1 || computerPort > 65535 {
		log.Error("validate computer port", "port", *computerPortText)
		os.Exit(1)
	}

	mobileListenAddr, err := resolveLoopbackTCPAddress(*mobileAddr)
	if err != nil {
		log.Error("validate mobile API address", "error", err)
		os.Exit(1)
	}
	dataRoot, err := filepath.Abs(*dataDir)
	if err != nil {
		log.Error("resolve data directory", "error", err)
		os.Exit(1)
	}
	*dataDir = dataRoot
	if *workspace == "" {
		*workspace = filepath.Join(*dataDir, "agent-workspace")
	}
	if *teachRoot == "" {
		*teachRoot = filepath.Join(*dataDir, "teach-traces")
	}
	dataPath := filepath.Join(*dataDir, "botd.sqlite")
	contextPath, err := filepath.Abs(*buildContext)
	if err != nil {
		log.Error("resolve build context", "error", err)
		os.Exit(1)
	}
	workspacePath, err := filepath.Abs(*workspace)
	if err != nil {
		log.Error("resolve workspace", "error", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(workspacePath, 0o700); err != nil {
		log.Error("create agent workspace", "error", err)
		os.Exit(1)
	}
	dataPath, err = filepath.Abs(dataPath)
	if err != nil {
		log.Error("resolve data path", "error", err)
		os.Exit(1)
	}
	teachPath, err := filepath.Abs(*teachRoot)
	if err != nil {
		log.Error("resolve teach trace path", "error", err)
		os.Exit(1)
	}
	searchConnectors, err := websearchplus.NewController(filepath.Join(*dataDir, "web-search"))
	if err != nil {
		log.Error("initialize search connector controller", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	storeInstance, err := store.Open(dataPath)
	if err != nil {
		log.Error("open store", "error", err)
		os.Exit(1)
	}
	defer storeInstance.Close()
	if err := storeInstance.Seed(ctx); err != nil {
		log.Error("seed store", "error", err)
		os.Exit(1)
	}
	if recovered, err := storeInstance.RecoverInterruptedRuns(ctx); err != nil {
		log.Error("recover interrupted runs", "error", err)
		os.Exit(1)
	} else if recovered > 0 {
		log.Warn("recovered interrupted runs", "count", recovered)
	}
	configuredPreferences, err := storeInstance.GetPreferences(ctx)
	if err != nil {
		log.Error("load preferences for runtime selection", "error", err)
		os.Exit(1)
	}

	capabilities := harness.ProbeAll(ctx)
	if err := storeInstance.UpsertCapabilities(ctx, capabilities); err != nil {
		log.Error("save capabilities", "error", err)
		os.Exit(1)
	}
	for _, capability := range capabilities {
		log.Info("capability", "name", capability.Name, "available", capability.Available, "version", capability.Version, "detail", capability.Detail)
	}
	allowComputer := os.Getenv("OPENAGENTFLEET_ALLOW_COMPUTER_EXECUTION") == "1"
	allowHarness := os.Getenv("OPENAGENTFLEET_ALLOW_HARNESS_EXECUTION") == "1"
	remoteComputerURL := configuredPreferences.Computer.RemoteURL
	if envRemoteComputerURL := strings.TrimSpace(os.Getenv("OPENAGENTFLEET_COMPUTER_REMOTE_URL")); envRemoteComputerURL != "" {
		remoteComputerURL = envRemoteComputerURL
	}
	requestedRuntime := configuredPreferences.Computer.Runtime
	if envRuntime := strings.TrimSpace(os.Getenv("OPENAGENTFLEET_RUNTIME")); envRuntime != "" {
		requestedRuntime = envRuntime
	}
	runtimeSelection, runtimeErr := compute.ResolveDockerRuntime(ctx, requestedRuntime)
	runtimeDetail := runtimeSelection.Detail
	if runtimeErr != nil {
		log.Warn("requested runtime unavailable; falling back to automatic Docker runtime", "requested", requestedRuntime, "error", runtimeErr)
		runtimeSelection, runtimeErr = compute.ResolveDockerRuntime(ctx, compute.RuntimeAuto)
		if runtimeErr != nil {
			log.Warn("automatic Docker runtime selection failed", "error", runtimeErr)
			runtimeSelection = compute.RuntimeSelection{ID: compute.RuntimeDocker, Name: "Docker Engine", Detail: "Docker runtime selection failed; using CLI defaults"}
		}
		if runtimeDetail == "" {
			runtimeDetail = fmt.Sprintf("requested runtime %q is not available", requestedRuntime)
		}
	}
	docker := compute.NewDocker(workspacePath, contextPath, allowComputer)
	runtimeSelection.Detail = runtimeDetail
	docker.ConfigureRuntime(runtimeSelection)
	docker.ConfigureResources(compute.ResourceConfigFromPreferences(configuredPreferences.Computer))
	docker.ViewPort = computerPort
	if strings.TrimSpace(remoteComputerURL) != "" {
		remoteComputerToken := strings.TrimSpace(os.Getenv("OPENAGENTFLEET_COMPUTER_REMOTE_TOKEN"))
		if err := docker.ConfigureRemote(remoteComputerURL, remoteComputerToken); err != nil {
			// A configured remote target must fail closed. Do not silently fall
			// back to a local computer when the optional remote credential is
			// missing or malformed.
			log.Warn("remote Agent Computer is disabled", "error", err)
			docker.AllowExecution = false
		}
	}
	// Keep the controller capability outside the bind-mounted workspace even
	// when the user selects a custom workspace path.
	docker.ControlTokenPath = filepath.Join(*dataDir, "agent-computer-control-token")
	// Chromium cookies and authenticated browser state are controller-owned as
	// well. The Agent Computer gets this as a dedicated mount, never via its
	// writable /workspace bind mount.
	docker.BrowserProfilePath = filepath.Join(*dataDir, "agent-computer-browser-profile")
	codexAppServer := harness.NewCodexAppServer("codex", workspacePath)
	defer codexAppServer.Close()
	runner := harness.NewRunner(allowHarness)
	runner.CodexAppServer = codexAppServer
	transcriber := stt.New(stt.Config{Endpoint: os.Getenv("OPENAGENTFLEET_STT_URL"), APIKey: os.Getenv("OPENAGENTFLEET_STT_API_KEY"), Model: os.Getenv("OPENAGENTFLEET_STT_MODEL")})
	teachRecorder, err := teach.New(teach.Config{Root: teachPath})
	if err != nil {
		log.Error("initialize Teach a Task recorder", "error", err)
		os.Exit(1)
	}
	workshopRoot := filepath.Join(*dataDir, "skill-workshop")
	workshop, err := skillworkshop.New(workshopRoot)
	if err != nil {
		log.Error("initialize Skill Workshop", "error", err)
		os.Exit(1)
	}
	secretHandoffs, err := secrethandoff.New(secrethandoff.Config{})
	if err != nil {
		log.Error("initialize secure handoff manager", "error", err)
		os.Exit(1)
	}
	defer secretHandoffs.Close()
	go cleanupSecretHandoffs(ctx, secretHandoffs)
	api := &httpapi.Server{
		Store:                 storeInstance,
		Docker:                docker,
		Capabilities:          capabilities,
		AllowHarnessExecution: allowHarness,
		RemoteToken:           remoteToken,
		Broker:                events.New(),
		Runner:                runner,
		GrokOAuth:             harness.NewGrokOAuthManager("grok", workspacePath),
		CodexAppServer:        codexAppServer,
		HarnessWorkdir:        workspacePath,
		UploadDir:             filepath.Join(workspacePath, ".openagentfleet", "uploads"),
		STT:                   transcriber,
		Teach:                 teachRecorder,
		TeachRoot:             teachPath,
		Workshop:              workshop,
		EnabledSkillsRoot:     filepath.Join(*dataDir, "enabled-skills"),
		IntegrationRunner:     integrations.ExecRunner{},
		SecretHandoffs:        secretHandoffs,
		SearchConnectors:      searchConnectors,
	}
	if cleaned, cleanupErr := api.CleanupStaleAttachments(ctx); cleanupErr != nil {
		log.Warn("clean up stale attachment drafts", "error", cleanupErr, "database_rows", cleaned)
	} else if cleaned > 0 {
		log.Info("cleaned stale attachment drafts", "count", cleaned)
	}
	handoffSocketPath := filepath.Join(*dataDir, "secure-handoff.sock")
	nativeHandoffSocket, err := secrethandoff.NewNativeSocketServer(secrethandoff.NativeSocketConfig{
		Path:    handoffSocketPath,
		Manager: secretHandoffs,
		OnAccepted: func(handoffContext context.Context, handoffID string) error {
			return api.DeliverSecretHandoff(handoffContext, handoffID)
		},
	})
	if err != nil {
		log.Error("initialize native secure handoff socket", "error", err)
		os.Exit(1)
	}
	defer nativeHandoffSocket.Close()
	api.NativeHandoffSocketPath = nativeHandoffSocket.Path()

	legacyListener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Error("listen legacy API", "addr", *addr, "error", err)
		os.Exit(1)
	}
	mobileListener, err := net.Listen("tcp", mobileListenAddr)
	if err != nil {
		_ = legacyListener.Close()
		log.Error("listen mobile API", "addr", mobileListenAddr, "error", err)
		os.Exit(1)
	}

	legacyServer := &http.Server{Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second}
	mobileServer := &http.Server{Handler: api.MobileHandler(), ReadHeaderTimeout: 5 * time.Second}
	log.Info("botd starting", "addr", legacyListener.Addr().String(), "mobile_addr", mobileListener.Addr().String(), "mobile_remote_enabled", true)
	if err := serveBoth(ctx, legacyListener, legacyServer, mobileListener, mobileServer); err != nil {
		log.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func resolveLoopbackTCPAddress(addr string) (string, error) {
	host, portString, err := net.SplitHostPort(addr)
	if err != nil || host == "" || portString == "" {
		return "", fmt.Errorf("mobile address must include a host and port")
	}
	port, err := strconv.Atoi(portString)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("mobile address has an invalid port")
	}

	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		ips, err = net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return "", fmt.Errorf("mobile address host does not resolve")
		}
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return "", fmt.Errorf("mobile address must resolve only to loopback")
		}
	}
	return net.JoinHostPort(ips[0].String(), strconv.Itoa(port)), nil
}

// requireRemoteTokenForNonLoopbackAddr keeps the desktop application's normal
// loopback setup frictionless while refusing to accidentally publish the full
// controller API without authentication. Packaged Tauri and local development
// use 127.0.0.1 and therefore do not require OPENAGENTFLEET_REMOTE_TOKEN.
func requireRemoteTokenForNonLoopbackAddr(addr, remoteToken string) error {
	if strings.TrimSpace(remoteToken) != "" {
		return nil
	}
	if legacyAPIBindIsLoopback(addr) {
		return nil
	}
	return errors.New("OPENAGENTFLEET_REMOTE_TOKEN is required when OPENAGENTFLEET_ADDR is not loopback")
}

func legacyAPIBindIsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || strings.TrimSpace(host) == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return false
		}
	}
	return true
}

type listenerResult struct {
	name string
	err  error
}

func serveBoth(ctx context.Context, legacyListener net.Listener, legacyServer *http.Server, mobileListener net.Listener, mobileServer *http.Server) error {
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan listenerResult, 2)

	serve := func(name string, listener net.Listener, server *http.Server) {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		results <- listenerResult{name: name, err: err}
	}
	go serve("legacy API", legacyListener, legacyServer)
	go serve("mobile API", mobileListener, mobileServer)
	go func() {
		<-serveCtx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = legacyServer.Shutdown(shutdownCtx)
		_ = mobileServer.Shutdown(shutdownCtx)
	}()

	first := <-results
	if first.err != nil {
		cancel()
		<-results
		return fmt.Errorf("%s: %w", first.name, first.err)
	}
	if ctx.Err() == nil {
		cancel()
		<-results
		return fmt.Errorf("%s stopped unexpectedly", first.name)
	}

	second := <-results
	if second.err != nil {
		return fmt.Errorf("%s: %w", second.name, second.err)
	}
	return nil
}

func cleanupSecretHandoffs(ctx context.Context, manager *secrethandoff.Manager) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			manager.CleanupExpired()
		}
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
