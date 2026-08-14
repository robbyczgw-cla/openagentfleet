package policy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)

const testWorkspaceRoot = "/tmp/openagentfleet-policy/workspace"

func TestBrokerIsOptionalAndDenyByDefault(t *testing.T) {
	action := folderAction(testWorkspaceRoot+"/README.md", "read")
	for _, config := range []Config{{}, {Version: CurrentVersion, Enabled: true}} {
		broker := newTestBroker(t, config)
		decision, err := broker.Evaluate(context.Background(), action)
		if err != nil {
			t.Fatalf("Evaluate() error = %v", err)
		}
		if decision.Effect != EffectDeny {
			t.Fatalf("effect = %q, want deny", decision.Effect)
		}
	}
}

func TestSecurityPrecedenceIsDenyThenAskThenAllow(t *testing.T) {
	action := folderAction(testWorkspaceRoot+"/internal/policy/broker.go", "write")
	rules := []Rule{
		folderRule("z-allow", EffectAllow, MatchExact, action.Resource.Target, "write"),
		folderRule("a-ask", EffectAsk, MatchTree, testWorkspaceRoot, "write"),
	}
	broker := enabledBroker(t, rules...)
	decision, err := broker.Evaluate(context.Background(), action)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Effect != EffectAsk || decision.WinningRuleID != "a-ask" {
		t.Fatalf("decision = %#v, want ask to outrank allow", decision)
	}

	rules = append(rules, folderRule("deny", EffectDeny, MatchTree, "/tmp/openagentfleet-policy", "write"))
	broker = enabledBroker(t, rules...)
	decision, err = broker.Evaluate(context.Background(), action)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Effect != EffectDeny || decision.WinningRuleID != "deny" {
		t.Fatalf("decision = %#v, want deny precedence", decision)
	}

	broker = enabledBroker(t, rules[2], rules[0], rules[1])
	decision, _ = broker.Evaluate(context.Background(), action)
	if decision.Effect != EffectDeny || decision.WinningRuleID != "deny" {
		t.Fatalf("rule order changed precedence: %#v", decision)
	}
}

func TestApprovalIsExactExpiringAndCannotOverrideDeny(t *testing.T) {
	action := mcpAction("github", "create_pull_request", "invoke")
	ask := Rule{ID: "ask-github-pr", Principal: action.Principal, Effect: EffectAsk,
		Scope: Scope{Resource: action.Resource, Match: MatchExact}, Operations: []string{"invoke"}}
	broker := enabledBroker(t, ask)
	hash, err := ActionHash(action)
	if err != nil {
		t.Fatal(err)
	}
	approval := Approval{
		ID: "approval-1", ActionHash: hash, RuleID: ask.ID, Principal: action.Principal,
		RunID: action.RunID, Status: ApprovalApproved,
		CreatedAt: fixedNow.Add(-time.Minute), ExpiresAt: fixedNow.Add(time.Minute),
	}
	decision, err := broker.EvaluateWithApprovals(context.Background(), action, []Approval{approval})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Effect != EffectAllow || decision.Reason != ReasonApprovalGranted || decision.ApprovalID != approval.ID {
		t.Fatalf("decision = %#v, want approval grant", decision)
	}

	approval.ExpiresAt = fixedNow
	decision, err = broker.EvaluateWithApprovals(context.Background(), action, []Approval{approval})
	if err != nil || decision.Effect != EffectAsk {
		t.Fatalf("expiry-boundary decision = %#v, error = %v", decision, err)
	}

	approval.ExpiresAt = fixedNow.Add(time.Minute)
	approval.RunID = "run-other"
	decision, err = broker.EvaluateWithApprovals(context.Background(), action, []Approval{approval})
	if err != nil || decision.Effect != EffectAsk {
		t.Fatalf("cross-run approval decision = %#v, error = %v", decision, err)
	}

	approval.RunID = action.RunID
	deny := ask
	deny.ID, deny.Effect = "deny-github-pr", EffectDeny
	broker = enabledBroker(t, ask, deny)
	decision, err = broker.EvaluateWithApprovals(context.Background(), action, []Approval{approval})
	if err != nil || decision.Effect != EffectDeny || decision.Reason != ReasonRuleDeny {
		t.Fatalf("approval overrode deny: %#v, error = %v", decision, err)
	}
}

func TestUnrelatedMalformedApprovalCannotBlockMatchingGrant(t *testing.T) {
	action := mcpAction("github", "create_issue", "invoke")
	ask := Rule{ID: "ask-create", Principal: action.Principal, Effect: EffectAsk,
		Scope: Scope{Resource: action.Resource, Match: MatchExact}, Operations: []string{"invoke"}}
	broker := enabledBroker(t, ask)
	hash, _ := ActionHash(action)
	valid := Approval{ID: "valid", ActionHash: hash, RuleID: ask.ID, Principal: action.Principal,
		RunID: action.RunID, Status: ApprovalApproved,
		CreatedAt: fixedNow.Add(-time.Minute), ExpiresAt: fixedNow.Add(time.Minute)}
	malformedUnrelated := Approval{ActionHash: "not-a-hash", RuleID: "other"}
	decision, err := broker.EvaluateWithApprovals(context.Background(), action, []Approval{malformedUnrelated, valid})
	if err != nil || decision.Effect != EffectAllow {
		t.Fatalf("unrelated malformed approval blocked grant: %#v, error = %v", decision, err)
	}

	malformedMatching := valid
	malformedMatching.ID = ""
	decision, err = broker.EvaluateWithApprovals(context.Background(), action, []Approval{malformedMatching})
	if err == nil || decision.Effect != EffectDeny || decision.Reason != ReasonInvalidApproval {
		t.Fatalf("matching malformed approval did not fail closed: %#v, error = %v", decision, err)
	}
}

func TestExplicitDeniedApprovalWinsOverApproved(t *testing.T) {
	action := connectorAction("github", "publish")
	ask := Rule{ID: "ask-publish", Principal: action.Principal, Effect: EffectAsk,
		Scope: Scope{Resource: action.Resource, Match: MatchExact}, Operations: []string{"publish"}}
	broker := enabledBroker(t, ask)
	hash, _ := ActionHash(action)
	base := Approval{ActionHash: hash, RuleID: ask.ID, Principal: action.Principal, RunID: action.RunID,
		CreatedAt: fixedNow.Add(-time.Minute), ExpiresAt: fixedNow.Add(time.Minute)}
	approved := base
	approved.ID, approved.Status = "approved", ApprovalApproved
	denied := base
	denied.ID, denied.Status = "denied", ApprovalDenied
	decision, err := broker.EvaluateWithApprovals(context.Background(), action, []Approval{approved, denied})
	if err != nil || decision.Effect != EffectDeny || decision.Reason != ReasonApprovalDenied {
		t.Fatalf("decision = %#v, error = %v", decision, err)
	}
}

func TestExpiredRuleIsIgnoredAtBoundary(t *testing.T) {
	action := connectorAction("github", "read")
	expires := fixedNow
	rule := Rule{ID: "temporary", Principal: action.Principal, Effect: EffectAllow,
		Scope: Scope{Resource: action.Resource, Match: MatchExact}, Operations: []string{"read"}, ExpiresAt: &expires}
	decision, err := enabledBroker(t, rule).Evaluate(context.Background(), action)
	if err != nil || decision.Effect != EffectDeny || decision.Reason != ReasonDefaultDeny {
		t.Fatalf("decision = %#v, error = %v", decision, err)
	}
}

func TestFolderTraversalAndPrefixTrapAreDenied(t *testing.T) {
	bad := []string{testWorkspaceRoot + "/../secret", "workspace", testWorkspaceRoot + "//"}
	for _, path := range bad {
		rule := folderRule("unsafe", EffectAllow, MatchTree, path, "read")
		if _, err := New(Config{Version: CurrentVersion, Enabled: true, Rules: []Rule{rule}}); err == nil {
			t.Fatalf("New() accepted unsafe path %q", path)
		}
	}

	broker := enabledBroker(t, folderRule("workspace", EffectAllow, MatchTree, testWorkspaceRoot, "read"))
	decision, err := broker.Evaluate(context.Background(), folderAction(testWorkspaceRoot+"-evil/file", "read"))
	if err != nil || decision.Effect != EffectDeny {
		t.Fatalf("prefix-trap decision = %#v, error = %v", decision, err)
	}

	decision, err = broker.Evaluate(context.Background(), folderAction(testWorkspaceRoot+"/../secret", "read"))
	if err == nil || decision.Effect != EffectDeny || decision.Reason != ReasonInvalidAction {
		t.Fatalf("unsafe action = %#v, error = %v", decision, err)
	}
}

func TestWildcardsAreRejectedForEveryScope(t *testing.T) {
	cases := []Resource{
		{Kind: ResourceFolder, Target: testWorkspaceRoot + "/*"},
		{Kind: ResourceBrowserProfile, Target: "profile-*"},
		{Kind: ResourceNativeApp, Target: "com.example.*"},
		{Kind: ResourceNetwork, Target: "https://*.example.com"},
		{Kind: ResourceMCP, Target: "github", Qualifier: "*"},
		{Kind: ResourceComputer, Target: "computer-?"},
		{Kind: ResourceConnector, Target: "connector[*]"},
	}
	for _, resource := range cases {
		rule := Rule{ID: "wildcard", Principal: workerPrincipal(), Effect: EffectAllow,
			Scope: Scope{Resource: resource, Match: MatchExact}, Operations: []string{"read"}}
		if _, err := New(Config{Version: CurrentVersion, Enabled: true, Rules: []Rule{rule}}); err == nil || !strings.Contains(err.Error(), "wildcard") {
			t.Fatalf("resource %#v error = %v", resource, err)
		}
	}
}

func TestActionHashIsStableAndSensitive(t *testing.T) {
	action := Action{Principal: workerPrincipal(), RunID: "run-42",
		Resource: Resource{Kind: ResourceComputer, Target: "computer-1"}, Operation: "click",
		Parameters: []Parameter{{Name: "y", Value: "240"}, {Name: "x", Value: "120"}}}
	hash, err := ActionHash(action)
	if err != nil {
		t.Fatal(err)
	}
	const golden = "sha256:48ae47af33b0790e0e6f02ce06c91e3708b998f7352e0d0df0c50ea71da82f4f"
	if hash != golden {
		t.Fatalf("ActionHash() = %q, want %q", hash, golden)
	}
	reordered := action
	reordered.Parameters = []Parameter{{Name: "x", Value: "120"}, {Name: "y", Value: "240"}}
	reorderedHash, _ := ActionHash(reordered)
	if reorderedHash != hash {
		t.Fatalf("parameter order changed hash: %q != %q", reorderedHash, hash)
	}
	changed := action
	changed.RunID = "run-43"
	changedHash, _ := ActionHash(changed)
	if changedHash == hash {
		t.Fatal("changed run id did not change action hash")
	}
	changed = action
	changed.Principal.ID = "worker-other"
	changedHash, _ = ActionHash(changed)
	if changedHash == hash {
		t.Fatal("changed principal did not change action hash")
	}
}

func TestExactScopesCoverAllResourceKinds(t *testing.T) {
	actions := []Action{
		folderAction(testWorkspaceRoot+"/README.md", "read"),
		{Principal: workerPrincipal(), RunID: "run-1", Resource: Resource{Kind: ResourceBrowserProfile, Target: "profile-1"}, Operation: "navigate"},
		{Principal: workerPrincipal(), RunID: "run-1", Resource: Resource{Kind: ResourceNativeApp, Target: "com.apple.Terminal"}, Operation: "open"},
		{Principal: workerPrincipal(), RunID: "run-1", Resource: Resource{Kind: ResourceNetwork, Target: "https://api.github.com"}, Operation: "connect"},
		mcpAction("github", "list_issues", "invoke"),
		{Principal: workerPrincipal(), RunID: "run-1", Resource: Resource{Kind: ResourceComputer, Target: "computer-1"}, Operation: "screenshot"},
		connectorAction("github", "read"),
	}
	for _, action := range actions {
		rule := Rule{ID: "allow-" + string(action.Resource.Kind), Principal: action.Principal, Effect: EffectAllow,
			Scope: Scope{Resource: action.Resource, Match: MatchExact}, Operations: []string{action.Operation}}
		decision, err := enabledBroker(t, rule).Evaluate(context.Background(), action)
		if err != nil || decision.Effect != EffectAllow {
			t.Fatalf("kind %q decision = %#v, error = %v", action.Resource.Kind, decision, err)
		}
	}
}

func TestCanonicalIPv6NetworkOriginIsSupported(t *testing.T) {
	action := Action{Principal: workerPrincipal(), RunID: "run-1",
		Resource: Resource{Kind: ResourceNetwork, Target: "https://[::1]:8443"}, Operation: "connect"}
	rule := Rule{ID: "ipv6", Principal: action.Principal, Effect: EffectAllow,
		Scope: Scope{Resource: action.Resource, Match: MatchExact}, Operations: []string{"connect"}}
	decision, err := enabledBroker(t, rule).Evaluate(context.Background(), action)
	if err != nil || decision.Effect != EffectAllow {
		t.Fatalf("IPv6 decision = %#v, error = %v", decision, err)
	}
}

func TestScopeIsExactOutsideExplicitFolderTree(t *testing.T) {
	action := mcpAction("github", "delete_repo", "invoke")
	serverOnly := action
	serverOnly.Resource.Qualifier = ""
	rule := Rule{ID: "server", Principal: action.Principal, Effect: EffectAllow,
		Scope: Scope{Resource: serverOnly.Resource, Match: MatchExact}, Operations: []string{"invoke"}}
	decision, err := enabledBroker(t, rule).Evaluate(context.Background(), action)
	if err != nil || decision.Effect != EffectDeny {
		t.Fatalf("server scope implicitly granted tool: %#v, error = %v", decision, err)
	}

	network := Action{Principal: workerPrincipal(), RunID: "run-1",
		Resource: Resource{Kind: ResourceNetwork, Target: "https://example.com"}, Operation: "connect"}
	networkRule := Rule{ID: "https-default", Principal: network.Principal, Effect: EffectAllow,
		Scope: Scope{Resource: network.Resource, Match: MatchExact}, Operations: []string{"connect"}}
	network.Resource.Target = "https://example.com:8443"
	decision, err = enabledBroker(t, networkRule).Evaluate(context.Background(), network)
	if err != nil || decision.Effect != EffectDeny {
		t.Fatalf("network origin implicitly granted another port: %#v, error = %v", decision, err)
	}
}

func TestAuditOmitsParametersAndInvalidRawResource(t *testing.T) {
	action := folderAction(testWorkspaceRoot+"/README.md", "read")
	action.Parameters = []Parameter{{Name: "opaque", Value: "do-not-store-me"}}
	broker := enabledBroker(t, folderRule("allow-read", EffectAllow, MatchExact, action.Resource.Target, "read"))
	decision, err := broker.Evaluate(context.Background(), action)
	if err != nil {
		t.Fatal(err)
	}
	records := broker.AuditTrail()
	payload, _ := json.Marshal(records)
	if len(records) != 1 || records[0].ID != decision.AuditID || records[0].ActionHash == "" || strings.Contains(string(payload), "do-not-store-me") {
		t.Fatalf("unsafe audit = %s", payload)
	}

	invalid := action
	invalid.Resource = Resource{Kind: ResourceNetwork, Target: "https://user:secret@example.com"}
	_, err = broker.Evaluate(context.Background(), invalid)
	if err == nil {
		t.Fatal("invalid network action returned no error")
	}
	payload, _ = json.Marshal(broker.AuditTrail())
	if strings.Contains(string(payload), "secret") || strings.Contains(string(payload), "user") {
		t.Fatalf("invalid raw resource leaked into audit: %s", payload)
	}
}

func TestLikelySecretParametersAreRejectedBeforeHashAndAudit(t *testing.T) {
	action := connectorAction("github", "authenticate")
	action.Parameters = []Parameter{{Name: "access_token", Value: "guessable-secret"}}
	broker := enabledBroker(t, Rule{ID: "ask-auth", Principal: action.Principal, Effect: EffectAsk,
		Scope: Scope{Resource: action.Resource, Match: MatchExact}, Operations: []string{"authenticate"}})
	decision, err := broker.Evaluate(context.Background(), action)
	if err == nil || decision.Effect != EffectDeny || decision.Reason != ReasonInvalidAction {
		t.Fatalf("secret parameter decision = %#v, error = %v", decision, err)
	}
	payload, _ := json.Marshal(broker.AuditTrail())
	if strings.Contains(string(payload), "guessable-secret") || strings.Contains(string(payload), "access_token") {
		t.Fatalf("secret parameter reached audit: %s", payload)
	}
}

func TestAllowFailsClosedWhenExternalAuditFails(t *testing.T) {
	action := connectorAction("github", "read")
	rule := Rule{ID: "allow", Principal: action.Principal, Effect: EffectAllow,
		Scope: Scope{Resource: action.Resource, Match: MatchExact}, Operations: []string{"read"}}
	broker, err := New(Config{Version: CurrentVersion, Enabled: true, Rules: []Rule{rule}},
		WithClock(func() time.Time { return fixedNow }),
		WithAuditor(AuditorFunc(func(context.Context, AuditRecord) error { return errors.New("disk unavailable") })))
	if err != nil {
		t.Fatal(err)
	}
	decision, err := broker.Evaluate(context.Background(), action)
	if err == nil || decision.Effect != EffectDeny || decision.Reason != ReasonAuditUnavailable {
		t.Fatalf("decision = %#v, error = %v", decision, err)
	}
	if records := broker.AuditTrail(); len(records) != 1 || records[0].Effect != EffectDeny || records[0].Reason != ReasonAuditUnavailable {
		t.Fatalf("audit = %#v, want only the final fail-closed denial", records)
	}
}

func TestConfigIsDefensivelyCloned(t *testing.T) {
	rule := folderRule("read", EffectAllow, MatchTree, testWorkspaceRoot, "read")
	config := Config{Version: CurrentVersion, Enabled: true, Rules: []Rule{rule}}
	broker := newTestBroker(t, config)
	config.Rules[0].Effect = EffectDeny
	config.Rules[0].Operations[0] = "write"
	decision, err := broker.Evaluate(context.Background(), folderAction(testWorkspaceRoot+"/README.md", "read"))
	if err != nil || decision.Effect != EffectAllow {
		t.Fatalf("caller mutation reached broker: %#v, error = %v", decision, err)
	}
	copyConfig := broker.Config()
	copyConfig.Rules[0].Effect = EffectDeny
	decision, _ = broker.Evaluate(context.Background(), folderAction(testWorkspaceRoot+"/README.md", "read"))
	if decision.Effect != EffectAllow {
		t.Fatal("Config() returned aliased rules")
	}
}

func TestAuditIDsAreNamespacedPerBroker(t *testing.T) {
	action := connectorAction("github", "read")
	one := newTestBroker(t, Config{})
	two := newTestBroker(t, Config{})
	first, _ := one.Evaluate(context.Background(), action)
	second, _ := two.Evaluate(context.Background(), action)
	if first.AuditID == second.AuditID {
		t.Fatalf("audit ID collision across brokers: %q", first.AuditID)
	}
}

func workerPrincipal() Principal {
	return Principal{Kind: PrincipalWorker, ID: "worker-codex-1"}
}

func folderAction(path, operation string) Action {
	return Action{Principal: workerPrincipal(), RunID: "run-1", Resource: Resource{Kind: ResourceFolder, Target: path}, Operation: operation}
}

func mcpAction(server, tool, operation string) Action {
	return Action{Principal: workerPrincipal(), RunID: "run-1", Resource: Resource{Kind: ResourceMCP, Target: server, Qualifier: tool}, Operation: operation}
}

func connectorAction(id, operation string) Action {
	return Action{Principal: workerPrincipal(), RunID: "run-1", Resource: Resource{Kind: ResourceConnector, Target: id}, Operation: operation}
}

func folderRule(id string, effect Effect, match ScopeMatch, path string, operations ...string) Rule {
	return Rule{ID: id, Principal: workerPrincipal(), Effect: effect,
		Scope: Scope{Resource: Resource{Kind: ResourceFolder, Target: path}, Match: match}, Operations: operations}
}

func enabledBroker(t *testing.T, rules ...Rule) *Broker {
	t.Helper()
	return newTestBroker(t, Config{Version: CurrentVersion, Enabled: true, Rules: rules})
}

func newTestBroker(t *testing.T, config Config) *Broker {
	t.Helper()
	broker, err := New(config, WithClock(func() time.Time { return fixedNow }))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return broker
}
