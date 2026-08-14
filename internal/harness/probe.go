package harness

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

type Definition struct {
	Name        string
	Command     string
	VersionArgs []string
}

var Definitions = []Definition{
	{Name: "pi", Command: "pi", VersionArgs: []string{"--version"}},
	{Name: "claude", Command: "claude", VersionArgs: []string{"--version"}},
	{Name: "codex", Command: "codex", VersionArgs: []string{"--version"}},
	{Name: CodexAppServerProvider, Command: "codex", VersionArgs: []string{"--version"}},
	{Name: "grok", Command: "grok", VersionArgs: []string{"--version"}},
	{Name: "opencode", Command: "opencode", VersionArgs: []string{"--version"}},
	{Name: "cursor", Command: "cursor-agent", VersionArgs: []string{"--version"}},
	{Name: "docker", Command: "docker", VersionArgs: []string{"--version"}},
}

func ProbeAll(ctx context.Context) []domain.Capability {
	result := make([]domain.Capability, 0, len(Definitions))
	for _, definition := range Definitions {
		result = append(result, probe(ctx, definition))
	}
	return result
}

func probe(parent context.Context, definition Definition) domain.Capability {
	item := domain.Capability{Name: definition.Name, Command: definition.Command}
	path, err := exec.LookPath(definition.Command)
	if err != nil {
		item.Detail = "not found in PATH"
		return item
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	args := append([]string{}, definition.VersionArgs...)
	// Installation/version probes never need provider credentials.
	output, err := newIsolatedCommandContext(ctx, "", path, args...).CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		item.Detail = fmt.Sprintf("version probe failed: %v", err)
		if text != "" {
			item.Detail += ": " + compact(text)
		}
		return item
	}
	item.Available = true
	item.Version = compact(text)
	item.Detail = path
	return item
}

func compact(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 240 {
		return value[:240]
	}
	return value
}
