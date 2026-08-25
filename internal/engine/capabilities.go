package engine

// CapabilitiesFor returns the static capability contract for a known engine.
// MCP is true only for grok, grok_build, codex_app_server, and opencode.
// Unknown IDs get a conservative streaming-only profile so callers never
// invent MCP or computer access from a provider name.
func CapabilitiesFor(id ID) Capabilities {
	switch id {
	case Grok, GrokBuild:
		return Capabilities{
			Tools: true, MCP: true, Reasoning: true, ImageInput: true,
			SessionResume: true, Streaming: true, ComputerMCP: true,
		}
	case Claude:
		return Capabilities{
			Tools: true, MCP: false, Reasoning: true, ImageInput: true,
			SessionResume: true, Streaming: true, ComputerMCP: false,
		}
	case Codex:
		return Capabilities{
			Tools: true, MCP: false, Reasoning: true, ImageInput: false,
			SessionResume: true, Streaming: true, ComputerMCP: false,
		}
	case CodexAppServer:
		return Capabilities{
			Tools: true, MCP: true, Reasoning: true, ImageInput: false,
			SessionResume: true, Streaming: true, ComputerMCP: true,
		}
	case OpenCode:
		return Capabilities{
			Tools: true, MCP: true, Reasoning: true, ImageInput: true,
			SessionResume: true, Streaming: true, ComputerMCP: true,
		}
	case Pi:
		return Capabilities{
			Tools: true, MCP: false, Reasoning: true, ImageInput: false,
			SessionResume: false, Streaming: true, ComputerMCP: false,
		}
	case Cursor:
		return Capabilities{
			Tools: true, MCP: false, Reasoning: true, ImageInput: true,
			SessionResume: true, Streaming: true, ComputerMCP: false,
		}
	default:
		return Capabilities{Streaming: true}
	}
}

// ProviderID maps a stored harness/provider string onto an engine ID.
// grok and grok_build share the Grok Build CLI; they remain distinct IDs
// because existing runs persist "grok".
func ProviderID(provider string) ID {
	switch provider {
	case "grok_build":
		return GrokBuild
	case "grok":
		return Grok
	case "claude":
		return Claude
	case "codex":
		return Codex
	case "codex_app_server":
		return CodexAppServer
	case "opencode":
		return OpenCode
	case "pi":
		return Pi
	case "cursor":
		return Cursor
	default:
		return ID(provider)
	}
}

// HarnessProvider is the identifier passed to harness.Runner. grok_build runs
// as grok because that is the installed CLI adapter.
func HarnessProvider(id ID) string {
	if id == GrokBuild {
		return "grok"
	}
	return string(id)
}
