import type { PairingBundle } from "./types";
import { RemoteApiError, normalizeTailnetUrl } from "./RemoteClient";

const PAIRING_KEYS = ["version", "base_url", "host_id", "grant_id", "pairing_secret", "expires_at"] as const;
const ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{2,127}$/;

/**
 * Parses the exact V1 pairing payload emitted by the trusted Mac app.
 * It deliberately accepts neither URL wrappers nor extra properties, so a
 * pasted browser URL can never quietly become a pairing credential.
 */
export function parsePairingBundle(value: string, now = Date.now()): PairingBundle {
  let candidate: unknown;
  try {
    candidate = JSON.parse(value);
  } catch {
    throw new RemoteApiError("Paste the complete OpenAgentFleet pairing bundle.");
  }
  if (!candidate || typeof candidate !== "object" || Array.isArray(candidate)) {
    throw new RemoteApiError("The pairing bundle must be an object.");
  }
  const record = candidate as Record<string, unknown>;
  const keys = Object.keys(record).sort();
  if (keys.length !== PAIRING_KEYS.length || keys.some((key, index) => key !== [...PAIRING_KEYS].sort()[index])) {
    throw new RemoteApiError("The pairing bundle has an unsupported shape.");
  }
  if (record.version !== 1) throw new RemoteApiError("This pairing bundle is not compatible with OpenAgentFleet Remote alpha.");
  if (typeof record.base_url !== "string") throw new RemoteApiError("The pairing bundle has no valid Tailnet address.");
  if (typeof record.host_id !== "string" || !ID_PATTERN.test(record.host_id)) throw new RemoteApiError("The pairing bundle has no valid Mac identity.");
  if (typeof record.grant_id !== "string" || !ID_PATTERN.test(record.grant_id)) throw new RemoteApiError("The pairing bundle has no valid pairing grant.");
  if (typeof record.pairing_secret !== "string" || record.pairing_secret.length < 16 || record.pairing_secret.length > 512 || /\s/.test(record.pairing_secret)) {
    throw new RemoteApiError("The pairing bundle has no valid one-time secret.");
  }
  if (typeof record.expires_at !== "string") throw new RemoteApiError("The pairing bundle has no valid expiry.");
  const expiresAt = Date.parse(record.expires_at);
  if (!Number.isFinite(expiresAt) || expiresAt <= now) throw new RemoteApiError("This pairing bundle has expired. Create a new one on your Mac.");

  return {
    version: 1,
    baseUrl: normalizeTailnetUrl(record.base_url),
    hostId: record.host_id,
    grantId: record.grant_id,
    pairingSecret: record.pairing_secret,
    expiresAt: record.expires_at
  };
}

/** A deliberately generic label: it does not expose the phone owner's name. */
export function defaultDeviceName(platform: string): string {
  const normalized = platform.toLowerCase();
  if (normalized === "ios") return "OpenAgentFleet iOS device";
  if (normalized === "android") return "OpenAgentFleet Android device";
  return "OpenAgentFleet mobile device";
}
