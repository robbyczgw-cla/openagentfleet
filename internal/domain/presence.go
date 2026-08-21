package domain

const (
	PresenceIdle              = "idle"
	PresenceWorking           = "working"
	PresenceUsingComputer     = "using_computer"
	PresenceWaitingApproval   = "waiting_for_approval"
	PresenceWaitingTakeover   = "waiting_for_takeover"
	PresenceCollaborating     = "collaborating"
	PresenceFailed            = "failed"
)

// AgentPresence is a computed, non-durable roster state. It is never stored.
type AgentPresence struct {
	State  string `json:"state"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

// ComputerPresence is the workspace computer snapshot used to derive roster
// state. The computer is shared; only an Agent with an active run is shown as
// using it.
type ComputerPresence struct {
	Running      bool
	AgentControl bool
	Takeover     bool
}

// PresenceInput is the exact evidence DeriveAgentPresence may consider.
type PresenceInput struct {
	BotID    string
	Run      *Run
	Computer ComputerPresence
	Handoffs []Handoff
}

// DeriveAgentPresence maps existing run, computer, and handoff evidence onto
// one user-visible roster state. Higher-urgency states win.
func DeriveAgentPresence(input PresenceInput) AgentPresence {
	run := input.Run
	active := run != nil && runActive(run.Status)
	if run != nil && run.Status == "waiting_for_approval" {
		return AgentPresence{State: PresenceWaitingApproval, Label: "Needs approval", Detail: "Waiting for an explicit Allow or Deny"}
	}
	if active && input.Computer.Takeover {
		return AgentPresence{State: PresenceWaitingTakeover, Label: "Needs takeover", Detail: "Human control of the Agent Computer"}
	}
	if active && input.Computer.Running && (input.Computer.AgentControl || input.Computer.Takeover) {
		return AgentPresence{State: PresenceUsingComputer, Label: "Using computer", Detail: "Browser or desktop work on the isolated computer"}
	}
	if collaborating(input.BotID, input.Handoffs) {
		detail := "Asked another Agent"
		if run != nil && runActive(run.Status) {
			detail = "Working with a teammate"
		}
		return AgentPresence{State: PresenceCollaborating, Label: "Collaborating", Detail: detail}
	}
	if run != nil && (run.Status == "queued" || run.Status == "running") {
		label := "Working"
		if run.Status == "queued" {
			label = "Queued"
		}
		return AgentPresence{State: PresenceWorking, Label: label, Detail: "Run " + run.Status}
	}
	if run != nil && (run.Status == "failed" || run.Status == "blocked") {
		detail := run.Error
		if detail == "" {
			detail = "Last run " + run.Status
		}
		return AgentPresence{State: PresenceFailed, Label: "Failed", Detail: detail}
	}
	return AgentPresence{State: PresenceIdle, Label: "Idle"}
}

func runActive(status string) bool {
	switch status {
	case "queued", "running", "waiting_for_approval":
		return true
	default:
		return false
	}
}

func collaborating(botID string, handoffs []Handoff) bool {
	if botID == "" {
		return false
	}
	for _, handoff := range handoffs {
		switch handoff.Status {
		case HandoffStatusQueued, HandoffStatusRunning, HandoffStatusWaiting:
		default:
			continue
		}
		if handoff.SourceBotID == botID || handoff.TargetBotID == botID {
			return true
		}
	}
	return false
}

func PresenceLabel(state string) string {
	switch state {
	case PresenceWaitingApproval:
		return "Needs approval"
	case PresenceWaitingTakeover:
		return "Needs takeover"
	case PresenceUsingComputer:
		return "Using computer"
	case PresenceCollaborating:
		return "Collaborating"
	case PresenceWorking:
		return "Working"
	case PresenceFailed:
		return "Failed"
	default:
		return "Idle"
	}
}
