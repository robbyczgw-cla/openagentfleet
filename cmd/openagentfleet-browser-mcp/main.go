package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/robbyczgw-cla/openagentfleet/internal/browsermcp"
)

func main() {
	apiToken := strings.TrimSpace(os.Getenv(browsermcp.APITokenEnv))
	if apiToken == "" {
		fmt.Fprintln(os.Stderr, "openagentfleet-browser-mcp: OPENAGENTFLEET_API_TOKEN is required")
		os.Exit(1)
	}
	runID := strings.TrimSpace(os.Getenv(browsermcp.RunIDEnv))
	if runID == "" {
		fmt.Fprintln(os.Stderr, "openagentfleet-browser-mcp: OPENAGENTFLEET_COMPUTER_RUN_ID is required")
		os.Exit(1)
	}
	runToken := strings.TrimSpace(os.Getenv(browsermcp.RunTokenEnv))
	if runToken == "" {
		fmt.Fprintln(os.Stderr, "openagentfleet-browser-mcp: OPENAGENTFLEET_COMPUTER_RUN_TOKEN is required")
		os.Exit(1)
	}
	server, err := browsermcp.New(browsermcp.Config{
		APIURL:   os.Getenv(browsermcp.APIURLEnv),
		APIToken: apiToken,
		RunID:    runID,
		RunToken: runToken,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "openagentfleet-browser-mcp:", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := server.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "openagentfleet-browser-mcp:", err)
		os.Exit(1)
	}
}
