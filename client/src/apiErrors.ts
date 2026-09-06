// Shared tail for the per-feature error tables. A request that never reached
// the local service throws a browser-worded exception instead of an API error:
// WebKit says "Load failed", Chromium says "Failed to fetch". Neither tells a
// person anything, and both look identical whether the service is down, still
// starting, or refusing the request before it is sent. One sentence covers all
// of those without claiming to know which it was.

const NETWORK_FAILURE =
  /^(load failed|failed to fetch|networkerror.*|network error|terminated|the internet connection appears to be offline\.?|cancell?ed)$/i;

export const NETWORK_FAILURE_TEXT =
  "Could not reach OpenAgentFleet on this computer. Check that it is running, then try again.";

export function isNetworkFailure(message: string): boolean {
  return NETWORK_FAILURE.test(message.trim());
}

// Used when no feature-specific wording matched. A real server message is kept
// as it is, because it says more than any generic sentence could.
export function fallbackErrorText(status: number, message: string): string {
  const text = message.trim();
  if (isNetworkFailure(text)) return NETWORK_FAILURE_TEXT;
  if (status === 0) return text || NETWORK_FAILURE_TEXT;
  return text || `The request failed (${status}).`;
}
