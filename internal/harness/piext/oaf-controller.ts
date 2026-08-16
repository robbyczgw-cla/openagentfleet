// Approvals-only Pi lead extension. Do not register MCP tools.
export default function (pi: {
	on(
		event: "tool_call",
		handler: (
			event: { toolName?: string; name?: string; input?: { command?: string } },
			ctx: { ui: { confirm: (title: string, message: string) => Promise<boolean> } },
		) => Promise<{ block: true; reason: string } | void>,
	): void;
}) {
	pi.on("tool_call", async (event, ctx) => {
		if (!isBashToolCall(event)) {
			return;
		}
		const command = String(event.input?.command ?? "").trim() || "(no command provided)";
		const allowed = await ctx.ui.confirm("Allow bash command?", command);
		if (!allowed) {
			return { block: true, reason: "User declined this bash command" };
		}
	});
}

function isBashToolCall(event: { toolName?: string; name?: string }): boolean {
	const helper = lookupIsToolCallEventType();
	if (helper) {
		try {
			return helper("bash", event);
		} catch {
			// Fall through to the tool-name check.
		}
	}
	const name = String(event.toolName || event.name || "");
	return name === "bash";
}

function lookupIsToolCallEventType(): ((tool: string, event: unknown) => boolean) | undefined {
	const globalHelper = (globalThis as { isToolCallEventType?: unknown }).isToolCallEventType;
	if (typeof globalHelper === "function") {
		return globalHelper as (tool: string, event: unknown) => boolean;
	}
	const requireFn = (globalThis as { require?: (id: string) => { isToolCallEventType?: unknown } }).require;
	if (typeof requireFn !== "function") {
		return undefined;
	}
	for (const id of ["@earendil-works/pi-coding-agent", "@mariozechner/pi-coding-agent"]) {
		try {
			const loaded = requireFn(id);
			if (typeof loaded?.isToolCallEventType === "function") {
				return loaded.isToolCallEventType as (tool: string, event: unknown) => boolean;
			}
		} catch {
			// Extension is loaded from a cache path; the helper is optional.
		}
	}
	return undefined;
}
