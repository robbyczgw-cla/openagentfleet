package httpapi

import (
	"strings"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
)

// buildModelCatalog keeps provider discovery in the controller. The UI gets a
// coherent list with auth/availability explanations instead of guessing from
// a hard-coded provider/model matrix.
func buildModelCatalog(capabilities []domain.Capability, auth []harness.AuthStatus) []domain.ModelCatalogEntry {
	capabilityAvailable := func(name string) bool {
		for _, item := range capabilities {
			if item.Name == name {
				return item.Available
			}
		}
		return false
	}
	authFor := func(provider string) (harness.AuthStatus, bool) {
		for _, item := range auth {
			if item.Provider == provider {
				return item, true
			}
		}
		return harness.AuthStatus{}, false
	}
	authState := func(status harness.AuthStatus, found bool, required bool) (string, string) {
		if !required {
			return "local", ""
		}
		if !found || !status.Available {
			return "unavailable", "Provider sign-in status is unavailable"
		}
		if status.Authenticated {
			return "connected", ""
		}
		if status.Pending {
			return "pending", "Finish the provider sign-in to use this model"
		}
		if status.LoginRequired {
			return "sign_in", "Sign in with the provider before running this model"
		}
		return "unavailable", firstNonEmpty(status.Detail, "Provider authentication is not ready")
	}
	ready := func(binaryAvailable bool, status harness.AuthStatus, found, required bool) (bool, string, string) {
		if !binaryAvailable {
			return false, "unavailable", "Harness is not installed or was not found in PATH"
		}
		state, reason := authState(status, found, required)
		return !required || state == "connected" || state == "local", state, reason
	}

	grokAuth, grokFound := authFor("grok")
	grokReady, grokState, grokReason := ready(capabilityAvailable("grok"), grokAuth, grokFound, true)
	codexAuth, codexFound := authFor(harness.CodexAppServerProvider)
	codexReady, codexState, codexReason := ready(capabilityAvailable(harness.CodexAppServerProvider), codexAuth, codexFound, true)
	openCodeReady, openCodeState, openCodeReason := ready(capabilityAvailable("opencode"), harness.AuthStatus{}, false, false)
	piReady, piState, piReason := ready(capabilityAvailable("pi"), harness.AuthStatus{}, false, false)

	return []domain.ModelCatalogEntry{
		{
			Harness: "grok_build", Provider: "grok", Model: "grok-4.6", Label: "Grok 4.6",
			Detail: "Explicit Grok model", Billing: "Uses your connected Grok account",
			AuthMode: "oauth", AuthLabel: "Grok Build OAuth", AuthState: grokState,
			Subscription: "Provider subscription or API access", Available: grokReady,
			DisabledReason: grokReason, ReasoningEfforts: []string{"low", "medium", "high"},
			ServiceTiers: []string{"default"},
		},
		{
			Harness: "grok_build", Provider: "grok", Model: "", Label: "Grok automatic",
			Detail: "Let Grok choose the current model", Billing: "Uses your connected Grok account",
			AuthMode: "oauth", AuthLabel: "Grok Build OAuth", AuthState: grokState,
			Subscription: "Provider subscription or API access", Available: grokReady,
			DisabledReason: grokReason, ReasoningEfforts: []string{"low", "medium", "high"},
			ServiceTiers: []string{"default"},
		},
		{
			Harness: "codex_app_server", Provider: "openai", Model: "", Label: "Codex automatic",
			Detail: "Use the model selected by your connected Codex account", Billing: "Uses your ChatGPT/Codex account",
			AuthMode: "oauth", AuthLabel: "ChatGPT OAuth", AuthState: codexState,
			Subscription: "Uses the connected ChatGPT plan where applicable", Available: codexReady,
			DisabledReason: codexReason, ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"},
			ServiceTiers: []string{"default", "priority", "flex"},
		},
		{
			Harness: "codex_app_server", Provider: "openai", Model: "gpt-5.6-sol", Label: "GPT-5.6 Sol",
			Detail: "Explicit model ID, if enabled for this account", Billing: "Uses your ChatGPT/Codex account",
			AuthMode: "oauth", AuthLabel: "ChatGPT OAuth", AuthState: codexState,
			Subscription: "Model availability is account-dependent", Available: codexReady,
			DisabledReason: codexReason, ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"},
			ServiceTiers: []string{"default", "priority", "flex"},
		},
		{
			Harness: "opencode", Provider: "opencode", Model: "opencode/deepseek-v4-flash-free", Label: "DeepSeek V4 Flash Free",
			Detail: "Bundled OpenCode starter route; provider availability may change", Billing: "Provider-defined; observed free route is not guaranteed",
			AuthMode: "local", AuthLabel: "OpenCode provider configuration", AuthState: openCodeState,
			Subscription: "Bring your own OpenCode provider when the starter route is unavailable", Available: openCodeReady,
			DisabledReason: openCodeReason, ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"},
			ServiceTiers: []string{"default"},
		},
		{
			Harness: "opencode", Provider: "opencode-go", Model: "opencode-go/deepseek-v4-flash", Label: "DeepSeek V4 Flash",
			Detail: "OpenCode Go provider model", Billing: "Uses your local OpenCode Go provider configuration",
			AuthMode: "local", AuthLabel: "OpenCode provider configuration", AuthState: openCodeState,
			Subscription: "Configure the OpenCode Go provider locally", Available: openCodeReady,
			DisabledReason: openCodeReason, ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"},
			ServiceTiers: []string{"default"},
		},
		{
			Harness: "opencode", Provider: "opencode-go", Model: "opencode-go/deepseek-v4-pro", Label: "DeepSeek V4 Pro",
			Detail: "OpenCode Go provider model", Billing: "Uses your local OpenCode Go provider configuration",
			AuthMode: "local", AuthLabel: "OpenCode provider configuration", AuthState: openCodeState,
			Subscription: "Configure the OpenCode Go provider locally", Available: openCodeReady,
			DisabledReason: openCodeReason, ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"},
			ServiceTiers: []string{"default"},
		},
		{
			Harness: "pi", Provider: "pi", Model: "", Label: "Pi automatic",
			Detail: "Use the model already configured in Pi", Billing: "Uses your local Pi login and provider configuration",
			AuthMode: "local", AuthLabel: "Pi login (`pi /login`)", AuthState: piState,
			Subscription: "Configure providers locally with Pi", Available: piReady,
			DisabledReason: piReason, ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"},
			ServiceTiers: []string{"default"},
		},
		{
			Harness: "pi", Provider: "pi", Model: "xai/grok-4.3", Label: "Pi · xAI Grok 4.3",
			Detail: "Runs through Pi RPC as xai/grok-4.3, not the Grok Build harness", Billing: "Uses the xAI key stored by Pi (`pi /login` or XAI_API_KEY)",
			AuthMode: "local", AuthLabel: "Pi login (`pi /login`)", AuthState: piState,
			Subscription: "Pi xAI provider; this is not Grok Build OAuth", Available: piReady,
			DisabledReason: piReason, ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"},
			ServiceTiers: []string{"default"},
		},
		{
			Harness: "pi", Provider: "pi", Model: "anthropic/claude-sonnet-4.6", Label: "Pi · Claude Sonnet 4.6",
			Detail: "Runs through Pi RPC as anthropic/claude-sonnet-4.6", Billing: "Uses the Anthropic key stored by Pi",
			AuthMode: "local", AuthLabel: "Pi login (`pi /login`)", AuthState: piState,
			Subscription: "Pi Anthropic provider", Available: piReady,
			DisabledReason: piReason, ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"},
			ServiceTiers: []string{"default"},
		},
		{
			Harness: "pi", Provider: "pi", Model: "openai/gpt-5.5", Label: "Pi · GPT-5.5",
			Detail: "Runs through Pi RPC as openai/gpt-5.5, not Codex App Server", Billing: "Uses the OpenAI key stored by Pi",
			AuthMode: "local", AuthLabel: "Pi login (`pi /login`)", AuthState: piState,
			Subscription: "Pi OpenAI provider; this is not ChatGPT OAuth", Available: piReady,
			DisabledReason: piReason, ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"},
			ServiceTiers: []string{"default"},
		},
		{
			Harness: "pi", Provider: "pi", Model: "deepseek/deepseek-v4-flash", Label: "Pi · DeepSeek V4 Flash",
			Detail: "Runs through Pi RPC as deepseek/deepseek-v4-flash, not bundled OpenCode", Billing: "Uses the DeepSeek key stored by Pi",
			AuthMode: "local", AuthLabel: "Pi login (`pi /login`)", AuthState: piState,
			Subscription: "Pi DeepSeek provider", Available: piReady,
			DisabledReason: piReason, ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"},
			ServiceTiers: []string{"default"},
		},
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
