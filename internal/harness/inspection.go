package harness

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ReadOnlyInfo exposes Grok Build's own catalogs and effective configuration
// without accepting arbitrary commands from a remote client.
func ReadOnlyInfo(ctx context.Context, kind, workdir string) (string, error) {
	workdir, err := filepath.Abs(workdir)
	if err != nil {
		return "", err
	}
	if err := ensureKnownInfoKind(kind); err != nil {
		return "", err
	}
	args := []string{}
	switch kind {
	case "inspect":
		args = []string{"inspect", "--json"}
	case "models":
		args = []string{"models"}
	case "plugins":
		args = []string{"plugin", "list", "--json", "--available"}
	case "mcp":
		args = []string{"mcp", "list", "--json"}
	case "sessions":
		args = []string{"sessions", "list", "--limit", "50"}
	}
	return runGrokCLI(ctx, workdir, args...)
}

func SearchGrokSessions(ctx context.Context, workdir, query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", errors.New("session search query is required")
	}
	if len(query) > 200 {
		return "", errors.New("session search query is too long")
	}
	return runGrokCLI(ctx, workdir, "sessions", "search", "--limit", "50", query)
}

func ExportGrokSession(ctx context.Context, workdir, sessionID string) (string, error) {
	if err := validateSessionID(sessionID); err != nil {
		return "", err
	}
	return runGrokCLI(ctx, workdir, "export", sessionID)
}

func DeleteGrokSession(ctx context.Context, workdir, sessionID string) error {
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	_, err := runGrokCLI(ctx, workdir, "sessions", "delete", sessionID)
	return err
}

func runGrokCLI(ctx context.Context, workdir string, args ...string) (string, error) {
	commandArgs := append([]string{"--no-auto-update", "--cwd", workdir}, args...)
	command := newIsolatedCommandContext(ctx, "grok", "grok", commandArgs...)
	command.Dir = workdir
	var stdout boundedBuffer
	stdout.limit = 4 * 1024 * 1024
	var stderr boundedBuffer
	stderr.limit = 256 * 1024
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(Redact(stderr.String()))
		if detail == "" {
			detail = strings.TrimSpace(Redact(stdout.String()))
		}
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("grok command failed: %s", detail)
	}
	return Redact(strings.TrimSpace(stdout.String())), nil
}

func validateSessionID(sessionID string) error {
	if len(sessionID) != 36 {
		return errors.New("invalid Grok session id")
	}
	for index, value := range sessionID {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if value != '-' {
				return errors.New("invalid Grok session id")
			}
			continue
		}
		if !(value >= '0' && value <= '9') && !(value >= 'a' && value <= 'f') && !(value >= 'A' && value <= 'F') {
			return errors.New("invalid Grok session id")
		}
	}
	return nil
}

func ensureKnownInfoKind(kind string) error {
	switch kind {
	case "inspect", "models", "plugins", "mcp", "sessions":
		return nil
	default:
		return errors.New("unsupported Grok read-only info kind")
	}
}
