package tools

import "encoding/json"

const (
	CapComputerView     = "computer.view"
	CapComputerControl  = "computer.control"
	CapAgentCollaborate = "agent.collaborate"
)

func objectSchema(properties map[string]any, required ...string) json.RawMessage {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return encoded
}

func clickSchema() json.RawMessage {
	return objectSchema(map[string]any{
		"ref": map[string]any{"type": "string", "description": "Element or window ref from a snapshot. Prefer this over coordinates."},
		"x":   map[string]any{"type": "number", "description": "Horizontal coordinate in pixels. Used when ref is missing or stale."},
		"y":   map[string]any{"type": "number", "description": "Vertical coordinate in pixels. Used when ref is missing or stale."},
	})
}

func typeSchema() json.RawMessage {
	return objectSchema(map[string]any{
		"text":      map[string]any{"type": "string", "description": "Text to type."},
		"ref":       map[string]any{"type": "string", "description": "Optional snapshot ref to focus first."},
		"sensitive": map[string]any{"type": "boolean", "description": "Mark password-like text as sensitive."},
	}, "text")
}

func keySchema() json.RawMessage {
	return objectSchema(map[string]any{
		"key": map[string]any{"type": "string", "description": "Key or key chord to press."},
	}, "key")
}

func def(name, alias, description string, schema json.RawMessage, caps ...string) Tool {
	return Tool{
		Name:                 name,
		Aliases:              []string{alias},
		Description:          description,
		InputSchema:          schema,
		RequiredCapabilities: caps,
	}
}

// DefaultCatalog migrates the existing browser, computer, and collaboration
// MCP tools into one registry. Execute stays unbound so botd remains the
// policy/HTTP authority; engines receive the same definitions through adapters.
func DefaultCatalog() []Tool {
	empty := objectSchema(nil)
	return []Tool{
		def("browser.status", "browser_status", "Read the Chromium browser and Agent Computer status from OpenAgentFleet.", empty, CapComputerView),
		def("browser.start", "browser_start", "Start or ensure the isolated Agent Computer and its Chromium browser. This does not enable Agent control.", empty, CapComputerView),
		def("browser.navigate", "browser_navigate", "Navigate the Agent Computer's Chromium browser to an http or https URL. Requires Agent control in OpenAgentFleet.", objectSchema(map[string]any{
			"url": map[string]any{"type": "string", "description": "The http or https URL to open."},
		}, "url"), CapComputerControl),
		def("browser.snapshot", "browser_snapshot", "Read interactive browser elements with stable refs. Prefer acting by ref, then fall back to coordinates from a screenshot. Requires Agent control.", empty, CapComputerControl),
		def("browser.click", "browser_click", "Click a coordinate in the Chromium browser viewport. Requires Agent control in OpenAgentFleet.", clickSchema(), CapComputerControl),
		def("browser.type", "browser_type", "Type text into the active Chromium browser element. Set sensitive for password-like text. Requires Agent control in OpenAgentFleet.", typeSchema(), CapComputerControl),
		def("browser.press", "browser_press", "Press a key in Chromium, for example Enter, Tab, or Control+L. Requires Agent control in OpenAgentFleet.", keySchema(), CapComputerControl),
		def("browser.scroll", "browser_scroll", "Scroll inside the Chromium browser viewport. Requires Agent control in OpenAgentFleet.", objectSchema(map[string]any{
			"delta_x": map[string]any{"type": "number", "description": "Horizontal scroll delta."},
			"delta_y": map[string]any{"type": "number", "description": "Vertical scroll delta."},
		}, "delta_y"), CapComputerControl),
		def("browser.screenshot", "browser_screenshot", "Capture the visible Chromium browser viewport as an image.", empty, CapComputerView),
		def("computer.snapshot", "computer_snapshot", "Read visible desktop windows with stable refs. Prefer acting by ref, then fall back to coordinates from a screenshot. Requires Agent control.", empty, CapComputerControl),
		def("computer.screenshot", "computer_screenshot", "Capture the full Xfce Agent Computer desktop as an image, including Chromium, terminal, and file manager.", empty, CapComputerView),
		def("computer.click", "computer_click", "Click a coordinate on the full Agent Computer desktop. Requires Agent control in OpenAgentFleet.", clickSchema(), CapComputerControl),
		def("computer.type", "computer_type", "Type text into the focused desktop application. Set sensitive for password-like text. Requires Agent control in OpenAgentFleet.", typeSchema(), CapComputerControl),
		def("computer.press", "computer_press", "Press a key in the focused desktop application. Requires Agent control in OpenAgentFleet.", keySchema(), CapComputerControl),
		def("computer.scroll", "computer_scroll", "Scroll the focused desktop application vertically. Requires Agent control in OpenAgentFleet.", objectSchema(map[string]any{
			"delta_y": map[string]any{"type": "number", "description": "Vertical scroll delta."},
		}, "delta_y"), CapComputerControl),
		def("agent.list", "list_agents", "List Agents available for collaboration through OpenAgentFleet.", empty, CapAgentCollaborate),
		def("agent.message", "message_agent", "Send a short message to another Agent. OpenAgentFleet delivers it; this bridge does not talk to the other Agent directly.", objectSchema(map[string]any{
			"agent_id": map[string]any{"type": "string", "description": "ID of the Agent to message."},
			"content":  map[string]any{"type": "string", "description": "Message to send."},
		}, "agent_id", "content"), CapAgentCollaborate),
		def("agent.delegate", "delegate_to_agent", "Ask another Agent to take on a task. OpenAgentFleet queues the work and returns a task ID.", objectSchema(map[string]any{
			"agent_id": map[string]any{"type": "string", "description": "ID of the Agent that should do the work."},
			"task":     map[string]any{"type": "string", "description": "Task to delegate."},
		}, "agent_id", "task"), CapAgentCollaborate),
		def("agent.task_status", "get_agent_task_status", "Read the status of a delegated Agent task.", objectSchema(map[string]any{
			"task_id": map[string]any{"type": "string", "description": "ID of the delegated task."},
		}, "task_id"), CapAgentCollaborate),
	}
}
