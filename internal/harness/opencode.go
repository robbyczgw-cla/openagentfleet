package harness

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strings"
)

const OpenCodeProvider = "opencode"

// ValidateOpenCodeOptions keeps provider-neutral profile fields honest at the
// process boundary. OpenCode supports an explicit provider/model and model
// variant, but its safe headless command has no service-tier or permission
// override. In particular, this adapter never maps anything to --auto.
func ValidateOpenCodeOptions(model, reasoningEffort, serviceTier, permissionMode string) error {
	if model != "" && !validOpenCodeModel(model) {
		return fmt.Errorf("OpenCode model %q must use provider/model format", model)
	}
	switch reasoningEffort {
	case "", "default", "low", "medium", "high", "xhigh", "max":
	default:
		return fmt.Errorf("OpenCode reasoning variant %q is not supported", reasoningEffort)
	}
	if serviceTier != "" && serviceTier != "default" {
		return fmt.Errorf("OpenCode does not expose a service-tier control through opencode run")
	}
	switch permissionMode {
	case "", "default", "ask", "provider_default":
		return nil
	case "auto":
		return fmt.Errorf("OpenCode auto permission is disabled because opencode --auto is dangerous")
	default:
		return fmt.Errorf("OpenCode permission mode %q is unsupported; use ask so the CLI keeps its safe default", permissionMode)
	}
}

func validOpenCodeModel(value string) bool {
	provider, model, found := strings.Cut(value, "/")
	return found && provider != "" && model != "" &&
		!strings.ContainsAny(provider, " \t\r\n") && !strings.ContainsAny(model, " \t\r\n")
}

func openCodeStartError(cause error) error {
	return fmt.Errorf("start OpenCode CLI; verify `opencode --version` succeeds and opencode is in PATH: %w", cause)
}

func openCodeExitError(cause error, output string) error {
	detail := openCodeErrorDetail(output)
	evidence := strings.ToLower(output + "\n" + detail)
	prefix := "OpenCode run failed"
	switch {
	case containsAny(evidence,
		"providerautherror", "autherror", "auth error", "authentication required",
		"authentication failed", "not authenticated", "unauthorized", "api key",
		"missing credentials", "credentials not found",
		"statuscode\":401", "status\":401"):
		prefix = "OpenCode authentication is unavailable; run `opencode auth login` and verify it with `opencode auth list`"
	case containsAny(evidence,
		"providernotfound", "provider not found", "provider is not configured",
		"no provider configured", "no providers configured", "unknown provider",
		"invalid provider", "modelnotfound", "model not found", "no models found",
		"unknown model"):
		prefix = "OpenCode provider/model is unavailable; run `opencode models` and configure an explicit provider/model"
	}
	if detail != "" {
		return fmt.Errorf("%s: %s: %w", prefix, detail, cause)
	}
	return fmt.Errorf("%s: %w", prefix, cause)
}

func openCodeErrorDetail(output string) string {
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) != nil {
			return compact(Redact(line))
		}
		if value, ok := event["error"]; ok {
			if detail := describeOpenCodeError(value); detail != "" {
				return compact(Redact(detail))
			}
		}
		if event["type"] == "error" {
			if detail := describeOpenCodeError(event); detail != "" {
				return compact(Redact(detail))
			}
		}
	}
	return ""
}

func describeOpenCodeError(value any) string {
	switch item := value.(type) {
	case string:
		return strings.TrimSpace(item)
	case map[string]any:
		var parts []string
		for _, key := range []string{"name", "code", "message"} {
			if text, ok := item[key].(string); ok && strings.TrimSpace(text) != "" {
				parts = append(parts, strings.TrimSpace(text))
			}
		}
		for _, key := range []string{"data", "cause", "error"} {
			if nested, ok := item[key]; ok {
				if text := describeOpenCodeError(nested); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, ": ")
	default:
		return ""
	}
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
