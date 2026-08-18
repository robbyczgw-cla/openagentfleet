package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"unicode"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
	"github.com/robbyczgw-cla/openagentfleet/internal/id"
	"github.com/robbyczgw-cla/openagentfleet/internal/policy"
)

const (
	persistAlwaysAllow = "always_allow"
	persistAlwaysDeny  = "always_deny"
	hostShellTarget    = "host.shell"
	computerTarget     = "agent-computer"
)

func classifyPermissionAction(run domain.Run, request harness.PermissionRequest) (policy.Action, error) {
	var toolCall map[string]any
	_ = json.Unmarshal(request.ToolCall, &toolCall)
	kind := stringField(toolCall, "kind")
	title := stringField(toolCall, "title")
	name := stringField(toolCall, "name")
	haystack := strings.ToLower(strings.Join([]string{kind, title, name}, " "))
	operation := policyOperation(kind, title, name)
	resource := policy.Resource{Kind: policy.ResourceNativeApp, Target: hostShellTarget}
	if looksLikeComputerPermission(haystack) {
		resource = policy.Resource{Kind: policy.ResourceComputer, Target: computerTarget}
	}
	return policy.Action{
			Principal: policy.Principal{Kind: policy.PrincipalLead, ID: run.BotID},
			RunID:     run.ID,
			Resource:  resource,
			Operation: operation,
		}, policy.ValidateAction(policy.Action{
			Principal: policy.Principal{Kind: policy.PrincipalLead, ID: run.BotID},
			RunID:     run.ID,
			Resource:  resource,
			Operation: operation,
		})
}

func looksLikeComputerPermission(haystack string) bool {
	markers := []string{
		"computer_", "browser_", "agent computer", "desktop", "screenshot",
		"vnc", "mouse", "keyboard", "openagentfleet-browser",
	}
	for _, marker := range markers {
		if strings.Contains(haystack, marker) {
			return true
		}
	}
	return false
}

func policyOperation(values ...string) string {
	for _, value := range values {
		token := sanitizePolicyToken(value)
		if token != "" {
			return token
		}
	}
	return "tool"
}

func sanitizePolicyToken(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			builder.WriteRune(r)
			lastDash = false
		case r == '.' || r == '_' || r == '-' || r == ':' || r == '@':
			builder.WriteRune(r)
			lastDash = false
		default:
			if builder.Len() > 0 && !lastDash {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}
	token := strings.Trim(builder.String(), "-")
	if len(token) > 96 {
		token = strings.TrimRight(token[:96], "-")
	}
	return token
}

func matchPersistedPolicy(ctx context.Context, rules []policy.Rule, action policy.Action) policy.Decision {
	if len(rules) == 0 {
		return policy.Decision{Effect: policy.EffectAsk, Reason: policy.ReasonApprovalRequired}
	}
	broker, err := policy.New(policy.Config{Version: policy.CurrentVersion, Enabled: true, Rules: rules})
	if err != nil {
		return policy.Decision{Effect: policy.EffectAsk, Reason: policy.ReasonInvalidAction}
	}
	decision, err := broker.Evaluate(ctx, action)
	if err != nil {
		return policy.Decision{Effect: policy.EffectAsk, Reason: policy.ReasonInvalidAction}
	}
	switch decision.Reason {
	case policy.ReasonRuleAllow, policy.ReasonRuleDeny:
		return decision
	default:
		decision.Effect = policy.EffectAsk
		if decision.Reason == "" {
			decision.Reason = policy.ReasonApprovalRequired
		}
		return decision
	}
}

func persistedRuleFromAction(action policy.Action, effect policy.Effect) policy.Rule {
	return policy.Rule{
		ID:         id.New("rule"),
		Principal:  action.Principal,
		Effect:     effect,
		Scope:      policy.Scope{Resource: action.Resource, Match: policy.MatchExact},
		Operations: []string{action.Operation},
	}
}

func reuseOrCreatePersistedRule(rules []policy.Rule, action policy.Action, effect policy.Effect) policy.Rule {
	for _, rule := range rules {
		if rule.Principal == action.Principal && rule.Scope.Resource == action.Resource &&
			len(rule.Operations) == 1 && rule.Operations[0] == action.Operation {
			rule.Effect = effect
			return rule
		}
	}
	return persistedRuleFromAction(action, effect)
}

func firstPermissionOptionID(options json.RawMessage) string {
	var items []struct {
		OptionID string `json:"optionId"`
	}
	if err := json.Unmarshal(options, &items); err == nil {
		for _, item := range items {
			if item.OptionID != "" {
				return item.OptionID
			}
		}
	}
	return ""
}

func stringField(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}
