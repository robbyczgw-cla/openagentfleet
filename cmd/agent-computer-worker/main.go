// Command agent-computer-worker runs the isolated Agent Computer on a host
// other than the OpenAgentFleet controller. Keep its listener on loopback and
// expose it privately with Tailscale Serve.
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
	"strings"
	"syscall"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/compute"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	resourceDefaults := compute.DefaultResourceConfig()
	addr := flag.String("addr", envOr("OPENAGENTFLEET_COMPUTER_WORKER_ADDR", "127.0.0.1:9323"), "loopback worker API address")
	token := flag.String("token", envOr("OPENAGENTFLEET_COMPUTER_WORKER_TOKEN", ""), "worker bearer token; prefer OPENAGENTFLEET_COMPUTER_WORKER_TOKEN")
	dataDir := flag.String("data-dir", envOr("OPENAGENTFLEET_COMPUTER_WORKER_DATA_DIR", ".openagentfleet-computer"), "worker data directory")
	workspace := flag.String("workspace", "", "Agent Computer workspace; defaults below data-dir")
	buildContext := flag.String("build-context", envOr("OPENAGENTFLEET_COMPUTER_WORKER_BUILD_CONTEXT", "runtime/agent-computer"), "Agent Computer image build context")
	runtimeID := flag.String("runtime", envOr("OPENAGENTFLEET_COMPUTER_WORKER_RUNTIME", compute.RuntimeAuto), "Docker-compatible runtime: auto, colima, docker_desktop, or orbstack")
	computerPort := flag.Int("computer-port", envInt("OPENAGENTFLEET_COMPUTER_WORKER_PORT", 9223), "local Agent Computer view-service port")
	containerName := flag.String("container-name", envOr("OPENAGENTFLEET_COMPUTER_WORKER_CONTAINER", "openagentfleet-remote-agent-computer"), "worker-owned container name")
	computerCPUs := flag.Int("cpus", envInt("OPENAGENTFLEET_COMPUTER_WORKER_CPUS", resourceDefaults.CPUs), "Agent Computer CPU count")
	computerMemory := flag.Int("memory-gib", envInt("OPENAGENTFLEET_COMPUTER_WORKER_MEMORY_GIB", resourceDefaults.MemoryGiB), "Agent Computer memory in GiB")
	computerDisk := flag.Int("disk-gib", envInt("OPENAGENTFLEET_COMPUTER_WORKER_DISK_GIB", resourceDefaults.DiskGiB), "Agent Computer Colima disk in GiB")
	computerSwap := flag.Int("swap-gib", envInt("OPENAGENTFLEET_COMPUTER_WORKER_SWAP_GIB", resourceDefaults.SwapGiB), "Agent Computer guest swap in GiB")
	computerOSImage := flag.String("os-image", envOr("OPENAGENTFLEET_COMPUTER_WORKER_OS_IMAGE", resourceDefaults.OSImage), "Agent Computer OS image: ubuntu-24.04, ubuntu-26.04, or debian-13")
	flag.Parse()

	if err := validateLoopbackAddr(*addr); err != nil {
		log.Error("worker address must stay on loopback", "error", err)
		os.Exit(2)
	}
	if *computerPort < 1 || *computerPort > 65535 {
		log.Error("invalid computer port", "port", *computerPort)
		os.Exit(2)
	}
	if *token == "" {
		log.Error("worker token is required", "hint", "set OPENAGENTFLEET_COMPUTER_WORKER_TOKEN")
		os.Exit(2)
	}
	dataPath, err := filepath.Abs(*dataDir)
	if err != nil {
		log.Error("resolve data directory", "error", err)
		os.Exit(2)
	}
	if *workspace == "" {
		*workspace = filepath.Join(dataPath, "agent-workspace")
	}
	workspacePath, err := filepath.Abs(*workspace)
	if err != nil {
		log.Error("resolve workspace", "error", err)
		os.Exit(2)
	}
	contextPath, err := filepath.Abs(*buildContext)
	if err != nil {
		log.Error("resolve build context", "error", err)
		os.Exit(2)
	}
	if err := os.MkdirAll(workspacePath, 0o700); err != nil {
		log.Error("create worker workspace", "error", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	selection, err := compute.ResolveDockerRuntime(ctx, *runtimeID)
	if err != nil {
		log.Error("resolve worker runtime", "error", err)
		os.Exit(2)
	}
	docker := compute.NewDocker(workspacePath, contextPath, true)
	docker.ConfigureRuntime(selection)
	resources := compute.ResourceConfig{CPUs: *computerCPUs, MemoryGiB: *computerMemory, DiskGiB: *computerDisk, SwapGiB: *computerSwap, OSImage: *computerOSImage}
	if err := resources.Validate(); err != nil {
		log.Error("invalid Agent Computer resources", "error", err)
		os.Exit(2)
	}
	docker.ConfigureResources(resources)
	docker.ContainerName = *containerName
	docker.ViewPort = *computerPort
	worker, err := compute.NewRemoteWorker(docker, *token)
	if err != nil {
		log.Error("configure remote computer worker", "error", err)
		os.Exit(2)
	}

	server := &http.Server{
		Addr:              *addr,
		Handler:           worker.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      6 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
		_ = docker.Stop(shutdownContext)
	}()
	log.Info("remote Agent Computer worker ready", "addr", *addr, "runtime", selection.Name, "container", *containerName)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("remote Agent Computer worker stopped", "error", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if value := os.Getenv(key); value != "" {
		var parsed int
		if _, err := fmt.Sscanf(value, "%d", &parsed); err == nil {
			return parsed
		}
	}
	return fallback
}

func validateLoopbackAddr(address string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil || strings.TrimSpace(port) == "" {
		return errors.New("use an explicit loopback address such as 127.0.0.1:9323")
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	parsed := net.ParseIP(strings.Trim(host, "[]"))
	if parsed == nil || !parsed.IsLoopback() {
		return errors.New("only 127.0.0.1, localhost, or ::1 is allowed")
	}
	return nil
}
