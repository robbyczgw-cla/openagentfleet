export type ConnectionState = "disconnected" | "connecting" | "connected" | "degraded";

export interface DeviceMetadata {
  id?: string;
  name: string;
  platform: string;
  scope_profile?: string;
}

export interface RemoteProfile {
  baseUrl: string;
  hostId: string;
  device: DeviceMetadata;
  authVersion: 1;
}

/** The only data accepted from a QR/share bundle in the alpha mobile flow. */
export interface PairingBundle {
  version: 1;
  baseUrl: string;
  hostId: string;
  grantId: string;
  pairingSecret: string;
  expiresAt: string;
}

export interface PairingResponse {
  auth_version: 1;
  token_type: "Bearer";
  access_token: string;
  expires_at: string;
  device: DeviceMetadata;
  host_id: string;
}

export interface RemoteMeta {
  auth_version?: number;
  host_id?: string;
}

export interface Conversation {
  id: string;
  bot_id: string;
  title: string;
  created_at: string;
  updated_at: string;
}

export interface ChatMessage {
  id: string;
  conversation_id: string;
  role: "user" | "assistant" | "system" | string;
  content: string;
  created_at: string;
}

export interface ComputerStatus {
  available: boolean;
  running: boolean;
  browser_ready: boolean;
  desktop_ready: boolean;
  control_held?: boolean;
  control_lease_expires_at?: string;
  viewport_width?: number;
  viewport_height?: number;
  title?: string;
  detail?: string;
}

export interface MobileRun {
  id: string;
  conversation_id: string;
  status: string;
  created_at: string;
  updated_at: string;
}

export interface MobileApprovalOption {
  optionId: string;
  name: string;
  kind?: string;
}

export interface MobileApproval {
  id: string;
  run_id: string;
  conversation_id: string;
  bot_id: string;
  action: string;
  status: string;
  created_at: string;
  options?: MobileApprovalOption[];
}

export interface MobileRoutine {
  id: string;
  bot_id: string;
  name: string;
  status: string;
  kind: string;
  next_run_at?: string;
  last_run_at?: string;
  attention_reason?: string;
}

export interface Bootstrap {
  conversations: Conversation[];
  conversation: Conversation;
  messages: ChatMessage[];
  runs?: MobileRun[];
  approvals?: MobileApproval[];
  computer: ComputerStatus;
  device?: DeviceMetadata;
  /** Durable V1 cursor returned with the snapshot when the server supports it. */
  event_cursor?: number;
}

export interface StreamEvent {
  cursor: number;
  type: string;
  run_id?: string;
  conversation_id?: string;
  data: unknown;
  created_at: string;
}

export type RemoteEvent =
  | { kind: "open" }
  | { kind: "event"; event: StreamEvent }
  | { kind: "reset" }
  | { kind: "error"; error: Error };
