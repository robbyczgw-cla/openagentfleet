import EventSource, { type CustomEvent, type MessageEvent } from "react-native-sse";

import type { Bootstrap, ComputerStatus, Conversation, DeviceMetadata, PairingBundle, PairingResponse, RemoteEvent, RemoteMeta, RemoteProfile, StreamEvent } from "./types";

const REQUEST_TIMEOUT_MS = 15_000;
const FRAME_MAX_BYTES = 5 * 1024 * 1024;

type SourcePayload = Pick<MessageEvent | CustomEvent<"ofb.event" | "ofb.reset">, "data" | "lastEventId">;

export class RemoteApiError extends Error {
  constructor(
    message: string,
    readonly status?: number
  ) {
    super(message);
    this.name = "RemoteApiError";
  }
}

export function normalizeTailnetUrl(value: string): string {
  const candidate = value.trim();
  let url: URL;
  try {
    url = new URL(candidate);
  } catch {
    throw new RemoteApiError("Use the HTTPS Tailnet address from a pairing bundle.");
  }
  if (
    url.protocol !== "https:" ||
    !isTailnetHostname(url.hostname) ||
    url.username ||
    url.password ||
    url.hash ||
    url.search ||
    url.pathname !== "/"
  ) {
    throw new RemoteApiError("Pair only with a clean HTTPS *.ts.net address.");
  }
  return url.origin;
}

/** Pure validation helper: IP literals, localhost and lookalike suffixes fail closed. */
export function isTailnetHostname(hostname: string): boolean {
  const normalized = hostname.toLowerCase();
  return normalized.length > ".ts.net".length && normalized.endsWith(".ts.net") && !normalized.includes(":");
}

/** Pure parser for the named V1 `ofb.event` SSE payload. */
export function parseStreamEvent(data: string | null): StreamEvent | null {
  if (!data) return null;
  try {
    const candidate = JSON.parse(data) as Partial<StreamEvent>;
    if (!isEventCursor(candidate.cursor) || typeof candidate.type !== "string" || typeof candidate.created_at !== "string") return null;
    return {
      cursor: candidate.cursor,
      type: candidate.type,
      run_id: typeof candidate.run_id === "string" ? candidate.run_id : undefined,
      conversation_id: typeof candidate.conversation_id === "string" ? candidate.conversation_id : undefined,
      data: candidate.data,
      created_at: candidate.created_at
    };
  } catch {
    return null;
  }
}

/** V1 cursors are persistent positive integers, never timestamps or opaque ids. */
export function isEventCursor(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

/** Testable idempotency helper. It is a retry key, never an authentication secret. */
export function createIdempotencyKey(): string {
  const cryptoApi = (globalThis as unknown as { crypto?: { randomUUID?: () => string } }).crypto;
  if (typeof cryptoApi?.randomUUID === "function") return `ofb-${cryptoApi.randomUUID()}`;
  return `ofb-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 14)}`;
}

function bytesToBase64(bytes: Uint8Array): string | null {
  const encode = (globalThis as unknown as { btoa?: (value: string) => string }).btoa;
  if (typeof encode !== "function") return null;
  const chunkSize = 0x8000;
  let binary = "";
  for (let offset = 0; offset < bytes.length; offset += chunkSize) {
    binary += String.fromCharCode(...bytes.subarray(offset, Math.min(offset + chunkSize, bytes.length)));
  }
  return encode(binary);
}

function normalizeDevice(value: unknown, fallbackName: string, fallbackPlatform: string): DeviceMetadata {
  const candidate = value && typeof value === "object" ? value as Partial<DeviceMetadata> & { display_name?: unknown } : {};
  return {
    id: typeof candidate.id === "string" && candidate.id.length <= 128 ? candidate.id : undefined,
    name: typeof candidate.name === "string" && candidate.name.trim()
      ? candidate.name.trim().slice(0, 96)
      : typeof candidate.display_name === "string" && candidate.display_name.trim()
        ? candidate.display_name.trim().slice(0, 96)
        : fallbackName,
    platform: typeof candidate.platform === "string" && candidate.platform.trim() ? candidate.platform.trim().slice(0, 32) : fallbackPlatform
  };
}

async function requestJson<T>(baseUrl: string, path: string, init: RequestInit = {}, token?: string): Promise<T> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
  try {
    const headers = new Headers(init.headers);
    headers.set("Accept", "application/json");
    if (token) headers.set("Authorization", `Bearer ${token}`);
    const response = await fetch(`${baseUrl}${path}`, { ...init, headers, signal: controller.signal });
    if (!response.ok) {
      let message = `Request failed (${response.status}).`;
      try {
        const body = await response.json() as { error?: unknown };
        if (typeof body.error === "string" && body.error.trim()) message = body.error;
      } catch {
        // A proxy can return HTML. Do not surface or persist it.
      }
      throw new RemoteApiError(message, response.status);
    }
    return await response.json() as T;
  } catch (error) {
    if (error instanceof RemoteApiError) throw error;
    if (error instanceof Error && error.name === "AbortError") throw new RemoteApiError("The Mac did not respond in time.");
    throw new RemoteApiError(error instanceof Error ? error.message : "Network request failed.");
  } finally {
    clearTimeout(timer);
  }
}

export class RemoteClient {
  readonly baseUrl: string;

  constructor(
    readonly profile: RemoteProfile,
    private readonly token: string
  ) {
    this.baseUrl = normalizeTailnetUrl(profile.baseUrl);
    if (!token.trim()) throw new RemoteApiError("This device has no valid pairing credential.");
  }

  static async meta(baseUrl: string): Promise<RemoteMeta> {
    return requestJson<RemoteMeta>(normalizeTailnetUrl(baseUrl), "/api/v1/meta");
  }

  static async pair(bundle: PairingBundle, deviceName: string, platform: string): Promise<{ profile: RemoteProfile; token: string }> {
    const meta = await RemoteClient.meta(bundle.baseUrl);
    if (meta.auth_version !== undefined && meta.auth_version !== 1) throw new RemoteApiError("This Mac does not support OpenAgentFleet Remote alpha V1.");
    if (meta.host_id !== undefined && meta.host_id !== bundle.hostId) throw new RemoteApiError("This pairing bundle belongs to a different Mac.");

    const payload = await requestJson<PairingResponse>(bundle.baseUrl, "/api/v1/pair", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ grant_id: bundle.grantId, pairing_secret: bundle.pairingSecret, device_name: deviceName, platform })
    });
    if (payload.auth_version !== 1 || payload.token_type !== "Bearer" || typeof payload.access_token !== "string" || !payload.access_token.trim()) {
      throw new RemoteApiError("The Mac returned an invalid pairing response.");
    }
    if (payload.host_id !== bundle.hostId) throw new RemoteApiError("The pairing response belongs to a different Mac.");
    if (!Number.isFinite(Date.parse(payload.expires_at)) || Date.parse(payload.expires_at) <= Date.now()) {
      throw new RemoteApiError("The Mac returned an expired device credential.");
    }
    return {
      token: payload.access_token,
      profile: {
        baseUrl: bundle.baseUrl,
        hostId: payload.host_id,
        device: normalizeDevice(payload.device, deviceName, platform),
        authVersion: 1
      }
    };
  }

  private request<T>(path: string, init: RequestInit = {}): Promise<T> {
    return requestJson<T>(this.baseUrl, path, init, this.token);
  }

  async bootstrap(conversationID?: string): Promise<Bootstrap> {
    const suffix = conversationID ? `?conversation_id=${encodeURIComponent(conversationID)}` : "";
    return this.request<Bootstrap>(`/api/v1/bootstrap${suffix}`);
  }

  async conversations(): Promise<Conversation[]> {
    const payload = await this.request<{ conversations: Conversation[] }>("/api/v1/conversations");
    return payload.conversations;
  }

  async sendMessage(conversationID: string, content: string): Promise<void> {
    await this.request("/api/v1/messages", {
      method: "POST",
      headers: { "Content-Type": "application/json", "Idempotency-Key": createIdempotencyKey() },
      body: JSON.stringify({ conversation_id: conversationID, content })
    });
  }

  computer(): Promise<ComputerStatus> {
    return this.request<ComputerStatus>("/api/v1/computer");
  }

  setComputerControl(enabled: boolean): Promise<ComputerStatus> {
    return this.request<ComputerStatus>("/api/v1/computer/control", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ enabled })
    });
  }

  browserAction(action: { action: "click"; x: number; y: number }): Promise<ComputerStatus> {
    return this.request<ComputerStatus>("/api/v1/computer/browser/action", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(action)
    });
  }

  async fetchFrameDataUri(): Promise<string> {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
    try {
      const response = await fetch(`${this.baseUrl}/api/v1/computer/frame`, {
        headers: { Authorization: `Bearer ${this.token}`, Accept: "image/*" },
        signal: controller.signal
      });
      if (!response.ok) throw new RemoteApiError(`Could not load the computer frame (${response.status}).`, response.status);
      const contentType = response.headers.get("content-type")?.split(";", 1)[0]?.trim().toLowerCase() || "image/png";
      if (!contentType.startsWith("image/")) throw new RemoteApiError("The Mac returned an invalid computer frame.");
      const bytes = new Uint8Array(await response.arrayBuffer());
      if (bytes.byteLength === 0 || bytes.byteLength > FRAME_MAX_BYTES) throw new RemoteApiError("The computer frame is unavailable or too large for this mobile preview.");
      const base64 = bytesToBase64(bytes);
      if (!base64) throw new RemoteApiError("This Expo runtime cannot safely render authenticated computer frames.");
      return `data:${contentType};base64,${base64}`;
    } catch (error) {
      if (error instanceof RemoteApiError) throw error;
      if (error instanceof Error && error.name === "AbortError") throw new RemoteApiError("The computer frame timed out.");
      throw new RemoteApiError(error instanceof Error ? error.message : "Could not load the computer frame.");
    } finally {
      clearTimeout(timer);
    }
  }

  async logout(): Promise<void> {
    try {
      await this.request("/api/v1/session/logout", { method: "POST" });
    } catch (error) {
      // Alpha servers may omit this optional route. Local credential removal is
      // still mandatory and happens in the caller's finally-equivalent path.
      if (!(error instanceof RemoteApiError) || error.status !== 404) throw error;
    }
  }

  subscribe(conversationID: string, after: number | undefined, onEvent: (event: RemoteEvent) => void): () => void {
    const query = new URLSearchParams({ conversation_id: conversationID });
    if (after !== undefined) query.set("after", String(after));
    const source = new EventSource<"ofb.event" | "ofb.reset">(`${this.baseUrl}/api/v1/events?${query.toString()}`, {
      headers: { Authorization: `Bearer ${this.token}`, Accept: "text/event-stream" },
      pollingInterval: 0
    });
    source.addEventListener("open", () => onEvent({ kind: "open" }));
    source.addEventListener("ofb.event", (message: SourcePayload) => {
      const event = parseStreamEvent(message.data);
      if (event) onEvent({ kind: "event", event });
    });
    source.addEventListener("ofb.reset", () => onEvent({ kind: "reset" }));
    source.addEventListener("error", (event) => {
      const message = "message" in event ? event.message : "Live connection closed";
      onEvent({ kind: "error", error: new Error(message || "Live connection closed") });
    });
    return () => source.close();
  }
}
