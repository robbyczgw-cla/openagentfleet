package harness

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
)

func NativeGrokCommand(workdir, sessionID string) string {
	command, _ := NativeGrokCommandWithOptions(workdir, NativeGrokOptions{SessionID: sessionID})
	return command
}

type NativeGrokOptions struct {
	SessionID       string
	Fork            bool
	Dashboard       bool
	Fullscreen      bool
	Model           string
	ReasoningEffort string
	PermissionMode  string
}

func NativeGrokCommandWithOptions(workdir string, options NativeGrokOptions) (string, error) {
	if strings.TrimSpace(workdir) == "" {
		return "", errors.New("Grok workspace is required")
	}
	if options.Fork && options.SessionID == "" {
		return "", errors.New("fork requires a Grok session id")
	}
	if options.SessionID != "" {
		if err := validateSessionID(options.SessionID); err != nil {
			return "", err
		}
	}
	if err := ValidateGrokOptions(options.Model, options.ReasoningEffort, options.PermissionMode); err != nil {
		return "", err
	}
	command := "grok --cwd " + shellQuote(workdir)
	if options.SessionID != "" {
		command += " --resume " + shellQuote(options.SessionID)
	}
	if options.Fork {
		command += " --fork-session"
	}
	if options.Dashboard {
		command += " --dashboard"
	}
	if options.Fullscreen {
		command += " --fullscreen"
	}
	if options.Model != "" {
		command += " --model " + shellQuote(options.Model)
	}
	if options.ReasoningEffort != "" {
		command += " --reasoning-effort " + shellQuote(options.ReasoningEffort)
	}
	if options.PermissionMode != "" {
		command += " --permission-mode " + shellQuote(options.PermissionMode)
	}
	return "cd " + shellQuote(workdir) + " && " + command, nil
}

func ValidateGrokOptions(model, reasoningEffort, permissionMode string) error {
	if len(model) > 200 || strings.IndexFunc(model, func(value rune) bool { return value < 0x20 || value == 0x7f }) >= 0 {
		return errors.New("invalid Grok model")
	}
	if reasoningEffort != "" && reasoningEffort != "low" && reasoningEffort != "medium" && reasoningEffort != "high" {
		return errors.New("invalid reasoning effort")
	}
	switch permissionMode {
	case "", "default", "acceptEdits", "auto", "dontAsk", "bypassPermissions", "plan":
		return nil
	default:
		return errors.New("invalid permission mode")
	}
}

// LaunchNativeGrok opens the user's local Terminal only on an explicit POST
// action. It uses the configured workspace/session; it never accepts an
// arbitrary command from the API client.
func LaunchNativeGrok(ctx context.Context, workdir string, options NativeGrokOptions) error {
	if runtime.GOOS != "darwin" {
		return errors.New("native Grok TUI launch is only available on macOS")
	}
	command, err := NativeGrokCommandWithOptions(workdir, options)
	if err != nil {
		return err
	}
	script := `tell application "Terminal" to do script "` + appleScriptQuote(command) + `"`
	if output, err := newIsolatedCommandContext(ctx, "", "/usr/bin/osascript", "-e", script).CombinedOutput(); err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("launch native Grok TUI: %s", detail)
	}
	return nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func appleScriptQuote(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
