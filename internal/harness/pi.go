package harness

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	piProvider       = "pi"
	piReadOnlyTools  = "read,grep,find,ls"
	piWorkspaceTools = "read,grep,find,ls,write,edit"
	piLeadAskTools   = "read,grep,find,ls,write,edit,bash"
	piLeadRole       = "lead"
)

//go:embed piext/oaf-controller.ts
var piLeadControllerExtension embed.FS

// ValidatePiOptions keeps the Pi worker honest at the process boundary.
// Permission is enforced by an exact --tools allowlist. Pi has no approval
// callback and no MCP injection, so ask/auto/provider_default are rejected
// instead of being silently widened to bash.
func ValidatePiOptions(model, reasoningEffort, serviceTier, permissionMode string) error {
	if err := validatePiModelControls(model, reasoningEffort, serviceTier); err != nil {
		return err
	}
	if _, err := PiToolsForPermission(permissionMode); err != nil {
		return err
	}
	return nil
}

// ValidatePiLeadOptions keeps the Pi lead honest at the process boundary.
// Model/reasoning/tier match the worker. Permission may be read_only,
// workspace, or ask. ask is the only mode that may grant bash, and only
// together with the bundled approvals extension.
func ValidatePiLeadOptions(model, reasoningEffort, serviceTier, permissionMode string) error {
	if err := validatePiModelControls(model, reasoningEffort, serviceTier); err != nil {
		return err
	}
	if _, err := PiLeadToolsForPermission(permissionMode); err != nil {
		return err
	}
	return nil
}

func validatePiModelControls(model, reasoningEffort, serviceTier string) error {
	if strings.TrimSpace(model) != model || strings.ContainsAny(model, " \t\r\n") {
		return fmt.Errorf("Pi model %q must not contain whitespace", model)
	}
	if len(model) > 128 {
		return fmt.Errorf("Pi model must be at most 128 characters")
	}
	switch reasoningEffort {
	case "", "default", "low", "medium", "high", "xhigh", "max":
	default:
		return fmt.Errorf("Pi reasoning effort %q is not supported", reasoningEffort)
	}
	if serviceTier != "" && serviceTier != "default" {
		return fmt.Errorf("Pi does not expose a service-tier control")
	}
	return nil
}

// PiToolsForPermission returns the exact Pi --tools allowlist for a stored
// worker permission mode. bash is never granted: it can leave the workspace.
func PiToolsForPermission(permissionMode string) (string, error) {
	switch permissionMode {
	case "read_only":
		return piReadOnlyTools, nil
	case "workspace":
		return piWorkspaceTools, nil
	case "auto", "yolo":
		return "", fmt.Errorf("Pi auto permission is disabled because it would require unrestricted tools")
	case "", "ask", "provider_default", "default":
		return "", fmt.Errorf("Pi permission mode %q is unsupported; use read_only or workspace so --tools can enforce the sandbox", permissionMode)
	default:
		return "", fmt.Errorf("Pi permission mode %q is unsupported; use read_only or workspace", permissionMode)
	}
}

// PiLeadToolsForPermission returns the exact Pi --tools allowlist for a lead
// permission mode. bash is granted only for ask, and only when the lead
// controller extension can gate it.
func PiLeadToolsForPermission(permissionMode string) (string, error) {
	switch permissionMode {
	case "read_only":
		return piReadOnlyTools, nil
	case "workspace":
		return piWorkspaceTools, nil
	case "ask":
		return piLeadAskTools, nil
	case "auto", "yolo":
		return "", fmt.Errorf("Pi lead auto permission is disabled because it would require unrestricted tools")
	case "", "provider_default", "default":
		return "", fmt.Errorf("Pi lead permission mode %q is unsupported; use read_only, workspace, or ask so --tools can enforce the sandbox", permissionMode)
	default:
		return "", fmt.Errorf("Pi lead permission mode %q is unsupported; use read_only, workspace, or ask", permissionMode)
	}
}

func buildPiRPCCommand(workdir string, options CommandOptions) (Command, error) {
	var (
		tools string
		err   error
	)
	if options.Role == piLeadRole {
		if err = ValidatePiLeadOptions(options.Model, options.ReasoningEffort, "", options.PermissionMode); err != nil {
			return Command{}, err
		}
		tools, err = PiLeadToolsForPermission(options.PermissionMode)
	} else {
		if err = ValidatePiOptions(options.Model, options.ReasoningEffort, "", options.PermissionMode); err != nil {
			return Command{}, err
		}
		tools, err = PiToolsForPermission(options.PermissionMode)
	}
	if err != nil {
		return Command{}, err
	}
	args := []string{"--mode", "rpc", "--no-session", "--tools", tools}
	if options.Role == piLeadRole && options.PermissionMode == "ask" {
		extensionPath, extErr := materializePiLeadController()
		if extErr != nil {
			return Command{}, extErr
		}
		args = append(args, "-e", extensionPath)
	}
	if model := piModelFlag(options.Model, options.ReasoningEffort); model != "" {
		args = append(args, "--model", model)
	}
	program := piProvider
	if bundled := os.Getenv("OPENAGENTFLEET_PI_BINARY"); bundled != "" {
		program = bundled
	}
	return Command{Program: program, Args: args, Dir: workdir}, nil
}

func materializePiLeadController() (string, error) {
	source, err := piLeadControllerExtension.ReadFile("piext/oaf-controller.ts")
	if err != nil {
		return "", fmt.Errorf("read bundled Pi lead controller: %w", err)
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(cacheDir) == "" {
		cacheDir = os.TempDir()
	}
	dir := filepath.Join(cacheDir, "openagentfleet", "piext")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create Pi lead controller cache: %w", err)
	}
	path := filepath.Join(dir, "oaf-controller.ts")
	if existing, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(existing, source) {
		return path, nil
	}
	tmp, err := os.CreateTemp(dir, "oaf-controller-*.ts")
	if err != nil {
		return "", fmt.Errorf("stage Pi lead controller: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(source); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("write Pi lead controller: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("close Pi lead controller: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("install Pi lead controller: %w", err)
	}
	return path, nil
}

func piModelFlag(model, reasoningEffort string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if strings.Contains(model, ":") || reasoningEffort == "" || reasoningEffort == "default" {
		return model
	}
	return model + ":" + reasoningEffort
}
