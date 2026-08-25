package coordinator

import (
	"errors"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/orchestration"
)

func TestPlanDelegationAcceptsValidatedHop(t *testing.T) {
	delegation, err := PlanDelegation(orchestration.AgentTask{
		SourceBotID: "bot-source", TargetBotID: "bot-target",
		SourceRunID: "run-1", OriginRunID: "run-1", Depth: 1, TimeoutSeconds: 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	if delegation.SourceAgentID != "bot-source" || delegation.TargetAgentID != "bot-target" || delegation.OriginTurnID != "run-1" {
		t.Fatalf("delegation = %+v", delegation)
	}
}

func TestPlanDelegationRejectsSameAgent(t *testing.T) {
	_, err := PlanDelegation(orchestration.AgentTask{SourceBotID: "bot-a", TargetBotID: "bot-a", Depth: 1})
	if !errors.Is(err, ErrDelegationSameAgent) {
		t.Fatalf("err = %v", err)
	}
}

func TestPlanDelegationRejectsMissingAgentsOrDepth(t *testing.T) {
	if _, err := PlanDelegation(orchestration.AgentTask{TargetBotID: "bot-b", Depth: 1}); !errors.Is(err, ErrDelegationSourceRequired) {
		t.Fatalf("missing source: %v", err)
	}
	if _, err := PlanDelegation(orchestration.AgentTask{SourceBotID: "bot-a", Depth: 1}); !errors.Is(err, ErrDelegationTargetRequired) {
		t.Fatalf("missing target: %v", err)
	}
	if _, err := PlanDelegation(orchestration.AgentTask{SourceBotID: "bot-a", TargetBotID: "bot-b"}); !errors.Is(err, ErrDelegationDepth) {
		t.Fatalf("missing depth: %v", err)
	}
}

func TestCoordinatorPlanDelegationLogsWithoutOwningPolicy(t *testing.T) {
	c := New(nil, nil, nil)
	delegation, err := c.PlanDelegation(orchestration.AgentTask{
		SourceBotID: "bot-source", TargetBotID: "bot-target", SourceRunID: "run-1", Depth: 1,
	})
	if err != nil || delegation.TargetAgentID != "bot-target" {
		t.Fatalf("coordinator plan = %+v err=%v", delegation, err)
	}
}
