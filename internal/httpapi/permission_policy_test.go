package httpapi

import (
	"context"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
	"github.com/robbyczgw-cla/openagentfleet/internal/policy"
)

func TestClassifyPermissionActionSeparatesComputerFromHostShell(t *testing.T) {
	run := domain.Run{ID: "run-1", BotID: "bot-1"}
	computer, err := classifyPermissionAction(run, harness.PermissionRequest{
		ToolCall: []byte(`{"kind":"computer_click","title":"Click desktop"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if computer.Resource.Kind != policy.ResourceComputer || computer.Resource.Target != computerTarget {
		t.Fatalf("computer action = %#v", computer)
	}
	host, err := classifyPermissionAction(run, harness.PermissionRequest{
		ToolCall: []byte(`{"kind":"execute","title":"Run host command"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if host.Resource.Kind != policy.ResourceNativeApp || host.Resource.Target != hostShellTarget {
		t.Fatalf("host action = %#v", host)
	}
	if computer.Resource == host.Resource {
		t.Fatal("computer and host shell classified as the same resource")
	}
}

func TestMatchPersistedPolicyAllowsExactRuleAndAsksOtherwise(t *testing.T) {
	run := domain.Run{ID: "run-2", BotID: "bot-1"}
	action, err := classifyPermissionAction(run, harness.PermissionRequest{
		ToolCall: []byte(`{"kind":"execute","title":"Run host command"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	allow := persistedRuleFromAction(action, policy.EffectAllow)
	allow.ID = "rule-allow-host"
	decision := matchPersistedPolicy(context.Background(), []policy.Rule{allow}, action)
	if decision.Effect != policy.EffectAllow {
		t.Fatalf("matching allow = %#v", decision)
	}

	computer, err := classifyPermissionAction(run, harness.PermissionRequest{
		ToolCall: []byte(`{"kind":"computer_click","title":"Click desktop"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := matchPersistedPolicy(context.Background(), []policy.Rule{allow}, computer); got.Effect != policy.EffectAsk {
		t.Fatalf("host allow leaked onto computer: %#v", got)
	}
}
