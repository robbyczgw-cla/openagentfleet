package harness

import (
	"errors"
	"os"
	"path/filepath"
)

var ErrExecutionDisabled = errors.New("harness execution is disabled; set OPENAGENTFLEET_ALLOW_HARNESS_EXECUTION=1 to enable it")

type Command struct {
	Program string   `json:"program"`
	Args    []string `json:"args"`
	Dir     string   `json:"dir"`
}

// CommandOptions carries the small, portable subset of execution choices that
// the headless worker CLIs support. It deliberately does not contain any
// auto-approval switch: OpenAgentFleet never turns on a worker's broad "yolo" mode.
type CommandOptions struct {
	SessionID       string
	Model           string
	ReasoningEffort string
	PermissionMode  string
	Role            string // "" or "worker" = worker sandbox; "lead" = workspace engine
}

func BuildCommand(provider, prompt, workdir string) (Command, error) {
	return BuildCommandWithOptions(provider, prompt, workdir, CommandOptions{})
}

func BuildCommandWithOptions(provider, prompt, workdir string, options CommandOptions) (Command, error) {
	if workdir == "" {
		workdir = "."
	}
	workdir, _ = filepath.Abs(workdir)
	switch provider {
	case "pi":
		return buildPiRPCCommand(workdir, options)
	case "claude":
		return Command{Program: "claude", Args: []string{"--print", "--output-format", "stream-json", "--input-format", "text", prompt}, Dir: workdir}, nil
	case "codex":
		return Command{Program: "codex", Args: []string{"exec", "--json", "--cd", workdir, prompt}, Dir: workdir}, nil
	case "grok":
		return Command{Program: "grok", Args: []string{"--single", prompt, "--output-format", "streaming-json", "--cwd", workdir}, Dir: workdir}, nil
	case OpenCodeProvider:
		if err := ValidateOpenCodeOptions(options.Model, options.ReasoningEffort, "", ""); err != nil {
			return Command{}, err
		}
		args := []string{"run", "--pure", "--format", "json", "--dir", workdir}
		if options.SessionID != "" {
			args = append(args, "--session", options.SessionID)
		}
		if options.Model != "" {
			args = append(args, "--model", options.Model)
		}
		if options.ReasoningEffort != "" && options.ReasoningEffort != "default" {
			args = append(args, "--variant", options.ReasoningEffort)
		}
		args = append(args, prompt)
		program := OpenCodeProvider
		if bundled := os.Getenv("OPENAGENTFLEET_OPENCODE_BINARY"); bundled != "" {
			program = bundled
		}
		return Command{Program: program, Args: args, Dir: workdir}, nil
	case "cursor":
		// Cursor's headless `--print` mode is intentionally left in its default
		// approval posture. Do not add --force, --trust, --auto-review, or
		// --yolo here: those would bypass a deliberate user safety decision.
		args := []string{"--print", "--output-format", "stream-json", "--stream-partial-output", "--workspace", workdir}
		if options.SessionID != "" {
			args = append(args, "--resume", options.SessionID)
		}
		if options.Model != "" {
			args = append(args, "--model", options.Model)
		}
		args = append(args, prompt)
		return Command{Program: "cursor-agent", Args: args, Dir: workdir}, nil
	default:
		return Command{}, errors.New("unknown worker provider: " + provider)
	}
}
