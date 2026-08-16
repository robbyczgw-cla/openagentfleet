import {
  DragEvent as ReactDragEvent,
  FormEvent,
  KeyboardEvent as ReactKeyboardEvent,
  PointerEvent as ReactPointerEvent,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";
import { openUrl } from "@tauri-apps/plugin-opener";
import { agentTemplates, type AgentTemplate } from "./agentTemplates";
import "./App.css";

const API_BASE = import.meta.env.VITE_BOTD_URL ?? "http://127.0.0.1:4317";
const API_TOKEN = import.meta.env.VITE_BOTD_TOKEN ?? "";
const OPENCODE_STARTER_MODEL = "opencode/deepseek-v4-flash-free";
const OPENCODE_STARTER_MODEL_LABEL = "DeepSeek V4 Flash · starter route";
const OPENCODE_STARTER_MODEL_DETAIL =
  "Optional OpenCode starter route; availability, limits, and billing are provider-defined and may change. It is not guaranteed to be free.";
const ONBOARDING_STEP_COUNT = 4;
const NATIVE_RUNTIME_AVAILABLE =
  typeof window !== "undefined" &&
  (window.location.protocol === "tauri:" ||
    "__TAURI_INTERNALS__" in window);
const OAUTH_HOST_SUFFIXES = ["openai.com", "chatgpt.com", "x.ai"];
const SAFE_ATTACHMENT_PREVIEW_TYPES = new Set([
  "application/pdf",
  "image/avif",
  "image/gif",
  "image/jpeg",
  "image/png",
  "image/webp",
]);
let runtimeAPIToken = "";

async function readNativeAPIToken(attempts = 8): Promise<LocalAPIAuth | null> {
  if (!NATIVE_RUNTIME_AVAILABLE) return null;
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    try {
      const auth = await invoke<LocalAPIAuth>("local_api_auth");
      if (auth.token || auth.startup_error || attempt === attempts - 1) {
        return auth;
      }
    } catch {
      if (attempt === attempts - 1) return null;
    }
    await new Promise<void>((resolve) => window.setTimeout(resolve, 150));
  }
  return null;
}

function isAllowedOAuthURL(value: string): boolean {
  try {
    const parsed = new URL(value);
    const hostname = parsed.hostname.toLowerCase();
    return (
      parsed.protocol === "https:" &&
      OAUTH_HOST_SUFFIXES.some(
        (suffix) => hostname === suffix || hostname.endsWith(`.${suffix}`),
      )
    );
  } catch {
    return false;
  }
}

function canPreviewAttachment(mediaType: string): boolean {
  const normalized = mediaType.split(";", 1)[0].trim().toLowerCase();
  return (
    SAFE_ATTACHMENT_PREVIEW_TYPES.has(normalized) ||
    normalized.startsWith("audio/") ||
    normalized.startsWith("video/")
  );
}

async function isNearBlankFrame(blob: Blob): Promise<boolean> {
  if (typeof createImageBitmap !== "function") return false;
  let bitmap: ImageBitmap | null = null;
  try {
    bitmap = await createImageBitmap(blob);
    const canvas = document.createElement("canvas");
    canvas.width = 32;
    canvas.height = 32;
    const context = canvas.getContext("2d", { willReadFrequently: true });
    if (!context) return false;
    context.drawImage(bitmap, 0, 0, canvas.width, canvas.height);
    const pixels = context.getImageData(0, 0, canvas.width, canvas.height).data;
    let minimum = 255;
    let maximum = 0;
    for (let index = 0; index < pixels.length; index += 4) {
      const luminance =
        (299 * pixels[index] +
          587 * pixels[index + 1] +
          114 * pixels[index + 2]) /
        1000;
      minimum = Math.min(minimum, luminance);
      maximum = Math.max(maximum, luminance);
    }
    return maximum < 55 && maximum - minimum < 18;
  } catch {
    return false;
  } finally {
    bitmap?.close();
  }
}

async function apiFetch(path: string, init: RequestInit = {}) {
  const request = (token: string) => {
    const headers = new Headers(init.headers);
    if (token) headers.set("Authorization", `Bearer ${token}`);
    return fetch(`${API_BASE}${path}`, { ...init, headers });
  };

  const initialToken = runtimeAPIToken || API_TOKEN;
  const response = await request(initialToken);
  if (response.status !== 401 || API_TOKEN) return response;

  // The native controller credential is deliberately in-memory. A sidecar
  // restart can therefore leave a still-open WebView with one stale token.
  // Refresh through the trusted Tauri bridge and retry this request once.
  try {
    const auth = await readNativeAPIToken();
    const refreshedToken = typeof auth?.token === "string" ? auth.token : "";
    if (refreshedToken) {
      runtimeAPIToken = refreshedToken;
      return request(refreshedToken);
    }
  } catch {
    // Browser builds have no Tauri bridge and keep the original 401.
  }
  return response;
}

type Bot = {
  id: string;
  name: string;
  title: string;
  description: string;
  status: string;
};
type AgentExecutionDraft = {
  id: string;
  harness: string;
  model: string;
  reasoning: string;
  tier: string;
  permission: string;
  maxTurns: string;
  timeout: string;
};

type WorkerHarnessOption = {
  value: string;
  label: string;
  supported: boolean;
  detail: string;
};

// The lead -> worker runtime currently enforces permissions only for these
// two adapters. Keep the future adapters visible for product discoverability,
// but never present them as runnable choices.
const WORKER_HARNESS_OPTIONS: WorkerHarnessOption[] = [
  {
    value: "grok",
    label: "Grok Build",
    supported: true,
    detail: "Supported delegated worker",
  },
  {
    value: "opencode",
    label: "OpenCode",
    supported: true,
    detail: "Supported delegated worker",
  },
  {
    value: "claude",
    label: "Claude",
    supported: false,
    detail: "Future: permission-enforcing worker adapter pending",
  },
  {
    value: "pi",
    label: "Pi",
    supported: false,
    detail: "Future: permission-enforcing worker adapter pending",
  },
  {
    value: "codex",
    label: "Codex CLI",
    supported: false,
    detail: "Future: permission-enforcing worker adapter pending",
  },
  {
    value: "cursor",
    label: "Cursor Agent",
    supported: false,
    detail: "Future: permission-enforcing worker adapter pending",
  },
];

function workerHarnessOption(value: string) {
  return WORKER_HARNESS_OPTIONS.find((option) => option.value === value);
}
type AgentExecutionProfile = {
  id?: string;
  harness: string;
  model?: string;
  reasoning: string;
  service_tier: string;
  permission: string;
  web_search?: "live" | "disabled";
  max_turns?: number;
  timeout_seconds?: number;
};
type Agent = {
  bot: Bot;
  conversation?: Conversation;
  metadata?: {
    lead?: AgentExecutionProfile;
    workers?: AgentExecutionProfile[];
    orchestrator?: string;
    plugin_ids?: string[];
    mcp_ids?: string[];
    notify_finished?: boolean;
    notify_needs_input?: boolean;
  };
};

type Conversation = {
  id: string;
  bot_id: string;
  title: string;
  created_at?: string;
  updated_at?: string;
};
type Message = {
  id: string;
  role: string;
  content: string;
  created_at: string;
};
type Attachment = {
  id: string;
  conversation_id: string;
  message_id?: string;
  name: string;
  media_type: string;
  size: number;
  created_at: string;
};
type Capability = {
  name: string;
  available: boolean;
  version?: string;
  detail?: string;
};
type Computer = {
  state?: "unavailable" | "stopped" | "starting" | "ready" | "error" | string;
  can_retry?: boolean;
  available: boolean;
  running: boolean;
  browser_ready: boolean;
  desktop_ready?: boolean;
  image: string;
  resources?: {
    cpus?: number;
    memory_gib?: number;
    disk_gib?: number;
    swap_gib?: number;
    os_image?: string;
  };
  url?: string;
  title?: string;
  viewport_width?: number;
  viewport_height?: number;
  takeover: boolean;
  agent_control?: boolean;
  runtime_id?: string;
  runtime_name?: string;
  runtime_context?: string;
  runtime_detail?: string;
  detail?: string;
};
type RuntimeInfo = {
  id: string;
  name: string;
  kind: string;
  available: boolean;
  healthy: boolean;
  selected: boolean;
  context?: string;
  endpoint?: string;
  version?: string;
  detail?: string;
  experimental?: boolean;
  open_source?: boolean;
  supports_agent_computer?: boolean;
  installed?: boolean;
  installable?: boolean;
  install_command?: string;
};
type Run = {
  id: string;
  conversation_id?: string;
  provider: string;
  status: string;
  prompt?: string;
  error?: string;
  created_at?: string;
  updated_at?: string;
};
type StreamEvent = {
  id: string;
  run_id?: string;
  conversation_id?: string;
  type: string;
  data: string;
  created_at: string;
};
type ApprovalOption = { optionId: string; name: string; kind: string };
type Approval = {
  id: string;
  run_id: string;
  provider: string;
  action: string;
  payload: string;
  status: string;
  selected_option_id?: string;
  created_at: string;
};
type TranscriptBlock = {
  id: string;
  kind: "approval" | string;
  conversation_id: string;
  run_id: string;
  approval_id: string;
  provider: string;
  action: string;
  status: "pending" | "approved" | "denied" | "cancelled" | string;
  options?: ApprovalOption[];
  selected_option_id?: string;
  created_at: string;
  updated_at: string;
  resolved_at?: string;
};
type HarnessSession = {
  id: string;
  conversation_id: string;
  provider: string;
  native_session_id: string;
  workdir: string;
  title: string;
  status: string;
  created_at: string;
  updated_at: string;
};
type SearchHit = {
  kind: string;
  id: string;
  conversation_id?: string;
  title?: string;
  snippet: string;
  created_at: string;
};
type Skill = {
  id: string;
  name: string;
  description?: string;
  path?: string;
  source: string;
  eligible: boolean;
  detail?: string;
  updated_at?: string;
};
type HarnessAuth = {
  provider: string;
  available: boolean;
  authenticated: boolean;
  login_required: boolean;
  pending: boolean;
  mode?: string;
  plan?: string;
  detail?: string;
  verification_url?: string;
  user_code?: string;
  updated_at?: string;
};
type ModelCatalogEntry = {
  harness: string;
  provider: string;
  model: string;
  label: string;
  detail?: string;
  billing?: string;
  auth_mode?: string;
  auth_label?: string;
  auth_state: "connected" | "sign_in" | "pending" | "local" | "unavailable";
  subscription?: string;
  available: boolean;
  disabled_reason?: string;
  reasoning_efforts?: string[];
  service_tiers?: string[];
};
type OAuthStart = {
  state: HarnessAuth;
  authorization_url?: string;
  verification_url?: string;
  user_code?: string;
  error?: string;
};
type LocalAPIAuth = { token?: string | null; startup_error?: string | null };
type NativeDictationStatus = {
  available: boolean;
  running: boolean;
  detail: string;
};
type NativeDictationEvent = {
  session_id: string;
  state: "started" | "partial" | "final" | "stopped" | "cancelled" | "failed";
  text?: string | null;
  detail?: string | null;
};
type GrokInfoKind = "inspect" | "models" | "plugins" | "mcp" | "sessions";
type STTStatus = { available: boolean; detail?: string };
type BrowserSpeechRecognitionResult = {
  isFinal: boolean;
  [index: number]: { transcript: string };
};
type BrowserSpeechRecognitionEvent = Event & {
  resultIndex: number;
  results: {
    length: number;
    [index: number]: BrowserSpeechRecognitionResult;
  };
};
type BrowserSpeechRecognitionErrorEvent = Event & { error?: string };
type BrowserSpeechRecognition = {
  continuous: boolean;
  interimResults: boolean;
  lang: string;
  onstart: (() => void) | null;
  onresult: ((event: BrowserSpeechRecognitionEvent) => void) | null;
  onerror: ((event: BrowserSpeechRecognitionErrorEvent) => void) | null;
  onend: (() => void) | null;
  start: () => void;
  stop: () => void;
  abort: () => void;
};
type BrowserSpeechRecognitionConstructor = new () => BrowserSpeechRecognition;
declare global {
  interface Window {
    SpeechRecognition?: BrowserSpeechRecognitionConstructor;
    webkitSpeechRecognition?: BrowserSpeechRecognitionConstructor;
  }
}

function browserSpeechRecognitionConstructor(): BrowserSpeechRecognitionConstructor | null {
  if (typeof window === "undefined") return null;
  return window.SpeechRecognition ?? window.webkitSpeechRecognition ?? null;
}
type ComputerViewMode = "desktop" | "browser";
type ComputerFrameStatus = "idle" | "loading" | "ready" | "error";
type ComputerFrameState = {
  surface: ComputerViewMode;
  status: ComputerFrameStatus;
  error?: string;
};
type Theme = "light" | "dark" | "system";
type OnboardingLead = "grok_build" | "codex_app_server" | "opencode";
type SearchConnectorState = {
  web_search_plus_enabled: boolean;
  hound_enabled: boolean;
  donsetch_enabled: boolean;
  web_search_plus_launcher_ready?: boolean;
  web_search_plus_detail?: string;
  hound_launcher_ready?: boolean;
  hound_detail?: string;
  donsetch_launcher_ready?: boolean;
  donsetch_detail?: string;
  web_search_plus_credential_status?: string;
  web_search_plus_credential_masked?: string;
};
type SearchConnectorAvailability =
  | "idle"
  | "loading"
  | "available"
  | "absent"
  | "error";
type SearchConnectorID = "web_search_plus" | "hound" | "donsetch";
type NativeSearchMode = "connected_harness" | "opencode";
type OptionalFeatures = {
  lead_worker_runtime?: boolean;
  worker_isolation?: boolean;
  routines?: boolean;
  heartbeat?: boolean;
  remote_nodes?: boolean;
  remote_control?: boolean;
  extensions?: boolean;
  research_runs?: boolean;
  memory_proposals?: boolean;
  skill_learning?: boolean;
  native_mac_worker?: boolean;
  existing_browser_profile?: boolean;
  multiple_conversations?: boolean;
};
type Preferences = {
  version?: number;
  onboarding?: { version?: number; completed?: boolean };
  workspace?: { engine?: string; model?: string };
  appearance?: {
    theme?: Theme;
    density?: "comfortable" | "compact";
    font_scale?: number;
  };
  usage?: {
    default_worker?: string;
    reasoning_effort?: string;
    permission_mode?: string;
  };
  computer?: {
    default_surface?: ComputerViewMode;
    runtime?: string;
    cpus?: number;
    ram_gib?: number;
    disk_gib?: number;
    swap_gib?: number;
    os_image?: string;
    remote_url?: string;
  };
  safety?: { retain_transcripts?: boolean; retain_activity?: boolean };
  features?: OptionalFeatures;
};
type TeachStatus = {
  state?: "idle" | "recording" | "paused" | "stopped" | "discarded";
  id?: string;
  goal?: string;
  started_at?: string;
  deadline_at?: string;
  step_count?: number;
  saved?: boolean;
  expired?: boolean;
};
type TeachResponse = {
  status?: TeachStatus;
  detail?: string;
  error?: string;
};
type SecretPurpose = "password" | "two_factor_code";
type SecretHandoff = {
  id: string;
  run_id: string;
  conversation_id: string;
  surface: ComputerViewMode;
  purpose: SecretPurpose;
  status: string;
};
type SecretHandoffResponse = { request?: SecretHandoff; error?: string };
type Integration = {
  host?: string;
  kind?: string;
  name?: string;
  status?: "available" | "unavailable";
  source?: string;
  detail?: string;
};
type MobileScopeProfile = "observer" | "controller";
type MobileDevice = {
  id: string;
  display_name: string;
  platform: string;
  scope_profile: MobileScopeProfile;
  status: string;
  created_at: string;
  revoked_at?: string;
  last_used_at?: string;
  auth_version?: number;
};
type MobileDevicesResponse = { devices?: MobileDevice[]; error?: string };
type MobilePairingResponse = {
  host_id?: string;
  grant?: {
    id: string;
    scope_profile: MobileScopeProfile;
    status: string;
    created_at: string;
    expires_at: string;
  };
  pairing_secret?: string;
  error?: string;
};
type MobilePairingBundle = {
  text: string;
  grantID: string;
  expiresAt: string;
};
type MemoryCategory = "fact" | "preference" | "instruction" | "project";
type MemoryStatus = "approved" | "archived";
type Memory = {
  id: string;
  bot_id: string;
  category: MemoryCategory;
  content: string;
  source: "user" | "agent_proposal";
  status: MemoryStatus;
  priority: number;
  expires_at?: string;
  created_at?: string;
  updated_at?: string;
};
type MemoryDraft = {
  category: MemoryCategory;
  content: string;
  priority: string;
  expires_at: string;
};
type Bootstrap = {
  bots: Bot[];
  agents?: Agent[];
  conversations?: Conversation[];
  conversation: Conversation;
  messages: Message[] | null;
  capabilities: Capability[];
  model_catalog?: ModelCatalogEntry[];
  computer: Computer;
  runtimes?: RuntimeInfo[];
  runs?: Run[];
  approvals?: Approval[];
  transcript_blocks?: TranscriptBlock[];
  sessions?: HarnessSession[];
  skills?: Skill[];
  auth?: HarnessAuth[];
  attachments?: Attachment[];
  stt?: STTStatus;
  memories?: Memory[];
  host_os?: string;
};

function isLinuxHost(data?: Bootstrap | null) {
  if (data?.host_os) return data.host_os === "linux";
  return typeof navigator !== "undefined" && /linux/i.test(navigator.userAgent);
}

function hostDeviceName(data?: Bootstrap | null) {
  if (isLinuxHost(data)) return "computer";
  if (data?.host_os === "windows") return "PC";
  return "Mac";
}

function formatTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return new Intl.DateTimeFormat(undefined, {
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}

function formatRelativeDate(value: string | undefined, now: number) {
  if (!value) return "Not yet";
  const timestamp = new Date(value).getTime();
  if (Number.isNaN(timestamp)) return "—";
  const seconds = Math.max(0, Math.round((now - timestamp) / 1000));
  if (seconds < 60) return "Just now";
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} min ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours} hr ago`;
  const days = Math.round(hours / 24);
  if (days < 7) return `${days} day${days === 1 ? "" : "s"} ago`;
  return new Intl.DateTimeFormat(undefined, {
    day: "numeric",
    month: "short",
    year: "numeric",
  }).format(new Date(value));
}

function normalizeTailnetEndpoint(value: string) {
  const endpoint = new URL(value.trim());
  if (
    endpoint.protocol !== "https:" ||
    !endpoint.hostname.toLowerCase().endsWith(".ts.net") ||
    endpoint.username ||
    endpoint.password ||
    endpoint.pathname !== "/" ||
    endpoint.search ||
    endpoint.hash
  ) {
    throw new Error("Enter a Tailnet HTTPS address such as https://mac.tailnet.ts.net");
  }
  return endpoint.origin;
}

function isBootstrap(value: unknown): value is Bootstrap {
  if (!value || typeof value !== "object") return false;
  const payload = value as Record<string, unknown>;
  if (!Array.isArray(payload.bots) || payload.bots.length === 0) return false;
  if (!Array.isArray(payload.capabilities)) return false;
  if (!payload.conversation || typeof payload.conversation !== "object")
    return false;
  if (!payload.computer || typeof payload.computer !== "object") return false;
  return true;
}

function teachStatusFromResponse(response: TeachResponse): TeachStatus | null {
  return response.status ?? null;
}

function harnessLabel(name: string) {
  return (
    (
      {
        grok: "Grok Build",
        grok_build: "Grok Build",
        codex_app_server: "Codex App Server",
        codex: "Codex CLI",
        claude: "Claude",
        pi: "Pi",
        opencode: "OpenCode",
        cursor: "Cursor Agent",
      } as Record<string, string>
    )[name] ?? name
  );
}

type ModelChoice = {
  value: string;
  label: string;
  detail: string;
  available?: boolean;
  disabledReason?: string;
  billing?: string;
  authLabel?: string;
  authState?: ModelCatalogEntry["auth_state"];
};

const CUSTOM_MODEL_VALUE = "__custom_model__";

function leadProviderValue(value: string) {
  switch (value) {
    case "grok_build":
      return "grok";
    case "grok":
      return "grok";
    case "codex_app_server":
      return "codex_app_server";
    case "opencode":
      return "opencode";
    default:
      return "grok";
  }
}

function leadModelChoices(
  value: string,
  catalog: ModelCatalogEntry[] = [],
): ModelChoice[] {
  const catalogHarness = value === "grok" ? "grok_build" : value;
  const catalogChoices = catalog
    .filter((entry) => entry.harness === catalogHarness)
    .map((entry) => {
      const starterRoute = entry.model === OPENCODE_STARTER_MODEL;
      return {
        value: entry.model,
        label: starterRoute
          ? OPENCODE_STARTER_MODEL_LABEL
          : entry.label,
        detail: starterRoute
          ? OPENCODE_STARTER_MODEL_DETAIL
          : entry.detail ?? "Provider model",
        available: entry.available,
        disabledReason: entry.disabled_reason,
        billing: entry.billing,
        authLabel: entry.auth_label,
        authState: entry.auth_state,
      };
    });
  if (catalogChoices.length > 0) return catalogChoices;
  switch (leadProviderValue(value)) {
    case "grok":
      return [
        {
          value: "grok-4.6",
          label: "Grok 4.6",
          detail: "Recommended Grok model",
        },
        {
          value: "",
          label: "Grok automatic",
          detail: "Let Grok choose the current model",
        },
      ];
    case "codex_app_server":
      return [
        {
          value: "",
          label: "Codex automatic",
          detail: "Use the model selected by your connected Codex account",
        },
        {
          value: "gpt-5.6-sol",
          label: "GPT-5.6 Sol",
          detail: "Explicit model ID, if enabled for this account",
        },
      ];
    case "opencode":
      return [
        {
          value: OPENCODE_STARTER_MODEL,
          label: OPENCODE_STARTER_MODEL_LABEL,
          detail: OPENCODE_STARTER_MODEL_DETAIL,
        },
        {
          value: "opencode-go/deepseek-v4-flash",
          label: "DeepSeek V4 Flash",
          detail: "OpenCode Go provider model",
        },
        {
          value: "opencode-go/deepseek-v4-pro",
          label: "DeepSeek V4 Pro",
          detail: "OpenCode Go provider model",
        },
      ];
    default:
      return [{ value: "", label: "Harness automatic", detail: "Use the harness default" }];
  }
}

function defaultLeadModel(value: string, catalog: ModelCatalogEntry[] = []) {
  return leadModelChoices(value, catalog)[0]?.value ?? "";
}

function modelChoiceLabel(
  harness: string,
  value: string,
  catalog: ModelCatalogEntry[] = [],
) {
  const choice = leadModelChoices(harness, catalog).find((item) => item.value === value);
  if (choice) return choice.label;
  if (!value) return "Automatic";
  return value;
}

function modelChoiceDetail(
  harness: string,
  value: string,
  catalog: ModelCatalogEntry[] = [],
) {
  const choice = leadModelChoices(harness, catalog).find((item) => item.value === value);
  return choice?.detail ?? (value ? "Custom provider model" : "Use the harness default");
}

function ModelPicker({
  harness,
  value,
  onChange,
  catalog = [],
}: {
  harness: string;
  value: string;
  onChange: (value: string) => void;
  catalog?: ModelCatalogEntry[];
}) {
  const choices = leadModelChoices(harness, catalog);
  const [customMode, setCustomMode] = useState(
    () => !choices.some((choice) => choice.value === value),
  );
  const knownValue = choices.some((choice) => choice.value === value);
  const custom = customMode || !knownValue;
  const selectedValue = custom ? CUSTOM_MODEL_VALUE : value;
  const selectedChoice = custom
    ? undefined
    : choices.find((choice) => choice.value === value);
  return (
    <div className="model-picker">
      <div className="model-picker-heading">
        <span>Model</span>
        <small>
          {harnessLabel(leadProviderValue(harness))} · {modelChoiceLabel(harness, value, catalog)}
        </small>
      </div>
      <div className="model-picker-options" role="radiogroup" aria-label="AI model">
        {choices.map((choice) => {
          const selected = selectedValue === choice.value;
          return (
            <button
              type="button"
              className={`model-picker-option${selected ? " selected" : ""}`}
              key={`${harness}-${choice.value || "default"}`}
              role="radio"
              aria-checked={selected}
              disabled={
                choice.available === false ||
                choice.authState === "sign_in" ||
                choice.authState === "pending"
              }
              title={choice.disabledReason ?? choice.billing}
              onClick={() => {
                setCustomMode(false);
                onChange(choice.value);
              }}
            >
              <span
                className={`model-picker-radio${selected ? " selected" : ""}`}
                aria-hidden="true"
              >
                <span>{selected ? "✓" : ""}</span>
              </span>
              <span>
                <strong>{choice.label}</strong>
                <small>
                  {choice.detail}
                  {choice.authLabel ? ` · ${choice.authLabel}` : ""}
                  {choice.authState === "sign_in" ? " · Sign in required" : ""}
                  {choice.authState === "pending" ? " · Sign-in pending" : ""}
                  {choice.available === false
                    ? ` · Unavailable: ${choice.disabledReason ?? "provider is not ready"}`
                    : ""}
                </small>
              </span>
            </button>
          );
        })}
        <button
          type="button"
          className={`model-picker-option${custom ? " selected" : ""}`}
          role="radio"
          aria-checked={custom}
          onClick={() => {
            setCustomMode(true);
            if (!custom) onChange("");
          }}
        >
          <span
            className={`model-picker-radio${custom ? " selected" : ""}`}
            aria-hidden="true"
          >
            <span>{custom ? "✓" : ""}</span>
          </span>
          <span>
            <strong>Custom model ID</strong>
            <small>Use an exact provider/model identifier</small>
          </span>
        </button>
      </div>
      {custom && (
        <input
          value={knownValue && !customMode ? "" : value}
          onChange={(event) => {
            setCustomMode(true);
            onChange(event.target.value);
          }}
          placeholder={
            leadProviderValue(harness) === "opencode"
              ? "provider/model"
              : "model ID"
          }
          aria-label="Custom AI model ID"
        />
      )}
      <small className="model-picker-note">
        {custom
          ? "Enter the exact model ID supported by this provider."
          : selectedChoice?.detail ?? modelChoiceDetail(harness, value, catalog)}
      </small>
    </div>
  );
}

function searchConnectorStateFrom(value: unknown): SearchConnectorState | null {
  if (!value || typeof value !== "object") return null;
  const payload = value as Record<string, unknown>;
  if (
    typeof payload.web_search_plus_enabled !== "boolean" ||
    typeof payload.hound_enabled !== "boolean" ||
    typeof payload.donsetch_enabled !== "boolean"
  ) {
    return null;
  }
  const webSearchPlus =
    payload.web_search_plus && typeof payload.web_search_plus === "object"
      ? (payload.web_search_plus as Record<string, unknown>)
      : null;
  const hound =
    payload.hound && typeof payload.hound === "object"
      ? (payload.hound as Record<string, unknown>)
      : null;
  const donsetch =
    payload.donsetch && typeof payload.donsetch === "object"
      ? (payload.donsetch as Record<string, unknown>)
      : null;
  return {
    web_search_plus_enabled: payload.web_search_plus_enabled,
    hound_enabled: payload.hound_enabled,
    donsetch_enabled: payload.donsetch_enabled,
    web_search_plus_launcher_ready:
      typeof webSearchPlus?.ready === "boolean" ? webSearchPlus.ready : undefined,
    web_search_plus_detail:
      typeof webSearchPlus?.detail === "string" ? webSearchPlus.detail : undefined,
    hound_launcher_ready:
      typeof hound?.ready === "boolean" ? hound.ready : undefined,
    hound_detail: typeof hound?.detail === "string" ? hound.detail : undefined,
    donsetch_launcher_ready:
      typeof donsetch?.ready === "boolean" ? donsetch.ready : undefined,
    donsetch_detail:
      typeof donsetch?.detail === "string" ? donsetch.detail : undefined,
    web_search_plus_credential_status:
      typeof payload.web_search_plus_credential_status === "string"
        ? payload.web_search_plus_credential_status
        : undefined,
    web_search_plus_credential_masked:
      typeof payload.web_search_plus_credential_masked === "string"
        ? payload.web_search_plus_credential_masked
        : undefined,
  };
}

function maskedCredentialStatus(state: SearchConnectorState) {
  const masked = state.web_search_plus_credential_masked?.trim();
  if (masked) {
    const suffix = masked.match(/([a-zA-Z0-9]{1,4})$/)?.[1];
    return suffix ? `Credential ••••${suffix}` : "Credential configured";
  }
  const status = state.web_search_plus_credential_status?.trim().toLowerCase();
  if (!status) return null;
  if (status.includes("external") || status.includes("not inspected"))
    return "Credentials managed externally";
  return status.includes("missing") || status.includes("not configured")
    ? "Credential not configured"
    : "Credential ••••";
}

function SearchConnectorCards({
  availability,
  connectors,
  busy,
  error,
  onToggle,
  nativeSearchMode = "connected_harness",
}: {
  availability: SearchConnectorAvailability;
  connectors: SearchConnectorState | null;
  busy: SearchConnectorID | null;
  error: string | null;
  onToggle: (connector: SearchConnectorID, enabled: boolean) => void;
  nativeSearchMode?: NativeSearchMode;
}) {
  const nativeSearchCopy =
    nativeSearchMode === "opencode"
      ? {
          title: "OpenCode provider search",
          description:
            "Uses OpenCode's configured provider and tool defaults. Use an optional MCP connector for an explicit search route.",
          status: "OpenCode automatic",
          unavailable:
            "OpenCode provider search depends on its configured tools. Optional MCP connectors are separate.",
        }
      : {
          title: "Built-in lead search",
          description:
            "Grok Build and Codex App Server can use live web search through the selected harness.",
          status: "Configured for new agents",
          unavailable:
            "Grok Build and Codex App Server search is available after the selected harness is connected.",
        };
  if (availability === "loading" || availability === "idle") {
    return <div className="search-connectors-loading">Checking optional connectors…</div>;
  }
  if (availability === "absent") {
    return (
      <div className="search-connectors-unavailable">
        Optional search connectors are not available in this build.{" "}
        {nativeSearchCopy.unavailable}
      </div>
    );
  }
  if (availability === "error" || !connectors) {
    return (
      <div className="search-connectors-unavailable">
        {error ?? "Search connector status is temporarily unavailable."}{" "}
        {nativeSearchCopy.unavailable}
      </div>
    );
  }

  const credentialStatus = maskedCredentialStatus(connectors);
  return (
    <div className="search-connector-list">
      <article
        className={[
          "search-connector-card",
          nativeSearchMode === "connected_harness" ? "native" : "",
        ]
          .filter(Boolean)
          .join(" ")}
      >
        <span className="search-connector-mark">⌕</span>
        <div>
          <strong>{nativeSearchCopy.title}</strong>
          <small>{nativeSearchCopy.description}</small>
        </div>
        <span
          className={[
            "connector-status",
            nativeSearchMode === "connected_harness" ? "on" : "",
          ]
            .filter(Boolean)
            .join(" ")}
        >
          {nativeSearchCopy.status}
        </span>
      </article>
      <article className="search-connector-card">
        <span className="search-connector-mark">＋</span>
        <div>
          <strong>Web Search Plus</strong>
          <small>Independent optional search connector.</small>
          {credentialStatus && <em>{credentialStatus}</em>}
          {connectors.web_search_plus_enabled && (
            <em>
              {connectors.web_search_plus_launcher_ready
                ? "Launcher verified · MCP starts on use"
                : connectors.web_search_plus_detail ?? "Launcher unavailable"}
            </em>
          )}
        </div>
        <label className="connector-switch">
          <span>{connectors.web_search_plus_enabled ? "On" : "Off"}</span>
          <input
            type="checkbox"
            checked={connectors.web_search_plus_enabled}
            disabled={busy !== null}
            onChange={(event) =>
              onToggle("web_search_plus", event.target.checked)
            }
            aria-label="Enable Web Search Plus"
          />
        </label>
      </article>
      <article className="search-connector-card">
        <span className="search-connector-mark">H</span>
        <div>
          <strong>Hound</strong>
          <small>Independent keyless search connector.</small>
          <em>No credential required</em>
          {connectors.hound_enabled && (
            <em>
              {connectors.hound_launcher_ready
                ? "Launcher verified · MCP starts on use"
                : connectors.hound_detail ?? "Launcher unavailable"}
            </em>
          )}
        </div>
        <label className="connector-switch">
          <span>{connectors.hound_enabled ? "On" : "Off"}</span>
          <input
            type="checkbox"
            checked={connectors.hound_enabled}
            disabled={busy !== null}
            onChange={(event) => onToggle("hound", event.target.checked)}
            aria-label="Enable Hound"
          />
        </label>
      </article>
      <article className="search-connector-card">
        <span className="search-connector-mark">D</span>
        <div>
          <strong>Donsetch</strong>
          <small>Independent keyless fetch, search, and crawl connector.</small>
          <em>No credential required</em>
          {connectors.donsetch_enabled && (
            <em>
              {connectors.donsetch_launcher_ready
                ? "Launcher verified · MCP starts on use"
                : connectors.donsetch_detail ?? "Launcher unavailable"}
            </em>
          )}
        </div>
        <label className="connector-switch">
          <span>{connectors.donsetch_enabled ? "On" : "Off"}</span>
          <input
            type="checkbox"
            checked={connectors.donsetch_enabled}
            disabled={busy !== null}
            onChange={(event) => onToggle("donsetch", event.target.checked)}
            aria-label="Enable Donsetch"
          />
        </label>
      </article>
      {error && <p className="search-connectors-error">{error}</p>}
    </div>
  );
}

function formatFileSize(size: number) {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${Math.round(size / 1024)} KB`;
  return `${(size / (1024 * 1024)).toFixed(size >= 10 * 1024 * 1024 ? 0 : 1)} MB`;
}

function attachmentIcon(mediaType: string) {
  if (mediaType.startsWith("image/")) return "▧";
  if (mediaType.startsWith("audio/")) return "◖";
  if (mediaType.includes("pdf")) return "PDF";
  if (mediaType.startsWith("text/") || mediaType.includes("json")) return "TXT";
  return "FILE";
}

function App() {
  const [data, setData] = useState<Bootstrap | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [apiReady, setAPIReady] = useState(false);
  const [nativeRuntime, setNativeRuntime] = useState(false);
  const [nativeStartupError, setNativeStartupError] = useState<string | null>(
    null,
  );
  const [selectedConversationID, setSelectedConversationID] = useState("");
  const [agentBuilderOpen, setAgentBuilderOpen] = useState(false);
  const [botMenuID, setBotMenuID] = useState<string | null>(null);
  const [composerModelOpen, setComposerModelOpen] = useState(false);
  const [composerModelDraft, setComposerModelDraft] = useState("");
  const [composerModelBusy, setComposerModelBusy] = useState(false);
  const [workspaceModelDraft, setWorkspaceModelDraft] = useState<string | null>(null);
  const [selectedAgentTemplate, setSelectedAgentTemplate] =
    useState<AgentTemplate | null>(null);
	const [onboardingOpen, setOnboardingOpen] = useState(false);
	const [onboardingStep, setOnboardingStep] = useState(0);
	const [onboardingLead, setOnboardingLead] =
		useState<OnboardingLead>("grok_build");
	const [onboardingModel, setOnboardingModel] = useState(() =>
		defaultLeadModel("grok_build"),
	);
	const [onboardingReasoning, setOnboardingReasoning] = useState("high");
	const [onboardingBusy, setOnboardingBusy] = useState(false);
	const [onboardingSaveError, setOnboardingSaveError] = useState<string | null>(
		null,
	);
	const [runtimeInstallBusy, setRuntimeInstallBusy] = useState(false);
	const [runtimeInstallError, setRuntimeInstallError] = useState<string | null>(null);
	const [runtimeCommandCopied, setRuntimeCommandCopied] = useState(false);
  const [searchConnectorAvailability, setSearchConnectorAvailability] =
    useState<SearchConnectorAvailability>("idle");
  const [searchConnectors, setSearchConnectors] =
    useState<SearchConnectorState | null>(null);
  const [searchConnectorBusy, setSearchConnectorBusy] =
    useState<SearchConnectorID | null>(null);
  const [onboardingConnectorMCPs, setOnboardingConnectorMCPs] = useState<
    string[]
  >([]);
  const [searchConnectorsError, setSearchConnectorsError] = useState<
    string | null
  >(null);
  const [agentBuilderBusy, setAgentBuilderBusy] = useState(false);
  const [agentAdvancedOpen, setAgentAdvancedOpen] = useState(false);
  const [agentToolsOpen, setAgentToolsOpen] = useState(false);
  const [agentName, setAgentName] = useState("");
  const [agentTitle, setAgentTitle] = useState("");
  const [agentDescription, setAgentDescription] = useState("");
  const [agentOrchestrator, setAgentOrchestrator] = useState(false);
  const [agentEditingID, setAgentEditingID] = useState<string | null>(null);
  const [agentLeadHarness, setAgentLeadHarness] = useState("grok_build");
  const [agentModel, setAgentModel] = useState("");
  const [agentLeadReasoning, setAgentLeadReasoning] = useState("high");
  const [agentLeadTier, setAgentLeadTier] = useState("default");
  const [agentLeadPermission, setAgentLeadPermission] = useState("ask");
  const [agentLeadWebSearch, setAgentLeadWebSearch] = useState<
    "live" | "disabled"
  >("live");
  const [agentNotifyFinished, setAgentNotifyFinished] = useState(true);
  const [agentNotifyNeedsInput, setAgentNotifyNeedsInput] = useState(true);
  const [agentWorkers, setAgentWorkers] = useState<AgentExecutionDraft[]>([]);
  const [agentPlugins, setAgentPlugins] = useState("");
  const [agentMCPs, setAgentMCPs] = useState("");
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const [computerBusy, setComputerBusy] = useState(false);
  const [computerViewOpen, setComputerViewOpen] = useState(false);
  const [computerViewMode, setComputerViewMode] =
    useState<ComputerViewMode>("desktop");
  const [computerFrameURL, setComputerFrameURL] = useState<string | null>(null);
  const [desktopFrameURL, setDesktopFrameURL] = useState<string | null>(null);
  const [computerFrameState, setComputerFrameState] =
    useState<ComputerFrameState>({ surface: "desktop", status: "idle" });
  const [computerFrameRetryKey, setComputerFrameRetryKey] = useState(0);
  const [computerURL, setComputerURL] = useState("https://example.com");
  const [computerText, setComputerText] = useState("");
  const [computerSensitive, setComputerSensitive] = useState(false);
  const [computerActionBusy, setComputerActionBusy] = useState(false);
  const [secureHandoffBusy, setSecureHandoffBusy] =
    useState<SecretPurpose | null>(null);
  const [lastRun, setLastRun] = useState<Run | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [provider, setProvider] = useState("grok");
  const [reasoningEffort, setReasoningEffort] = useState("high");
  const [permissionMode, setPermissionMode] = useState("default");
  const [activity, setActivity] = useState<StreamEvent[]>([]);
  const [liveOutput, setLiveOutput] = useState("");
  const [expandedActivity, setExpandedActivity] = useState<string | null>(null);
  const [grokInfoKind, setGrokInfoKind] = useState<GrokInfoKind | null>(null);
  const [grokInfoOutput, setGrokInfoOutput] = useState("");
  const [grokInfoBusy, setGrokInfoBusy] = useState(false);
  const [nativeBusy, setNativeBusy] = useState(false);
  const [searchOpen, setSearchOpen] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [searchBusy, setSearchBusy] = useState(false);
  const [searchHits, setSearchHits] = useState<SearchHit[]>([]);
  const [oauthBusy, setOAuthBusy] = useState<string | null>(null);
  const [authRefreshBusy, setAuthRefreshBusy] = useState<string | null>(null);
  const [pendingAttachments, setPendingAttachments] = useState<Attachment[]>(
    [],
  );
  const [uploadingAttachments, setUploadingAttachments] = useState(0);
  const [composerDraggingFiles, setComposerDraggingFiles] = useState(false);
  const [removingAttachmentIDs, setRemovingAttachmentIDs] = useState<string[]>(
    [],
  );
  const [sttStatus, setSTTStatus] = useState<STTStatus | null>(null);
  const [recording, setRecording] = useState(false);
  const [transcribing, setTranscribing] = useState(false);
  const [nativeDictationAvailable, setNativeDictationAvailable] =
    useState(false);
  const [browserSpeechAvailable] = useState(
    () =>
      !NATIVE_RUNTIME_AVAILABLE &&
      browserSpeechRecognitionConstructor() !== null,
  );
  const [preferences, setPreferences] = useState<Preferences>({});
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [mobileDevices, setMobileDevices] = useState<MobileDevice[]>([]);
  const [mobileDevicesLoading, setMobileDevicesLoading] = useState(false);
  const [mobileDevicesError, setMobileDevicesError] = useState<string | null>(
    null,
  );
  const [mobileEndpoint, setMobileEndpoint] = useState("");
  const [mobileScopeProfile, setMobileScopeProfile] =
    useState<MobileScopeProfile>("controller");
  const [mobilePairingBusy, setMobilePairingBusy] = useState(false);
  const [mobilePairingBundle, setMobilePairingBundle] =
    useState<MobilePairingBundle | null>(null);
  const [mobilePairingExpired, setMobilePairingExpired] = useState(false);
  const [mobileBundleCopied, setMobileBundleCopied] = useState(false);
  const [mobileRevokeCandidateID, setMobileRevokeCandidateID] = useState<
    string | null
  >(null);
  const [mobileRevokingID, setMobileRevokingID] = useState<string | null>(null);
  const [memoryBotID, setMemoryBotID] = useState("");
  const [memories, setMemories] = useState<Memory[]>([]);
  const [memoriesLoading, setMemoriesLoading] = useState(false);
  const [memoriesError, setMemoriesError] = useState<string | null>(null);
  const [memoriesUnavailable, setMemoriesUnavailable] = useState(false);
  const [memoryDraft, setMemoryDraft] = useState<MemoryDraft | null>(null);
  const [editingMemoryID, setEditingMemoryID] = useState<string | null>(null);
  const [memoryBusyID, setMemoryBusyID] = useState<string | null>(null);
  const [memoryDeleteCandidateID, setMemoryDeleteCandidateID] = useState<
    string | null
  >(null);
  const [workspaceOpen, setWorkspaceOpen] = useState(false);
  const [teach, setTeach] = useState<TeachStatus>({ state: "idle" });
  const [teachGoalOpen, setTeachGoalOpen] = useState(false);
  const [teachGoal, setTeachGoal] = useState("");
  const [teachBusy, setTeachBusy] = useState(false);
  const [integrations, setIntegrations] = useState<Integration[] | null>(null);
  const [integrationsError, setIntegrationsError] = useState<string | null>(
    null,
  );
  const [desktopNotificationPermission, setDesktopNotificationPermission] =
    useState<NotificationPermission | "unavailable">(() =>
      typeof Notification === "undefined" ? "unavailable" : Notification.permission,
    );
  const [now, setNow] = useState(Date.now());
  const preferencesPatchChainRef = useRef(Promise.resolve());
  const loadRequestIDRef = useRef(0);
  const selectedConversationIDRef = useRef("");
  const onboardingCompletionRef = useRef(false);
  const onboardingLeadHydratedRef = useRef(false);
  const workspaceControlsRequestIDRef = useRef(0);
  const frameURLRef = useRef<string | null>(null);
  const desktopFrameURLRef = useRef<string | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const attachmentUploadBusyRef = useRef(false);
  const composerDragDepthRef = useRef(0);
  const mediaRecorderRef = useRef<MediaRecorder | null>(null);
  const mediaStreamRef = useRef<MediaStream | null>(null);
  const recorderStartingRef = useRef(false);
  const browserSpeechRef = useRef<BrowserSpeechRecognition | null>(null);
  const browserSpeechBaseDraftRef = useRef("");
  const browserSpeechConversationIDRef = useRef("");
  const nativeDictationSessionRef = useRef<string | null>(null);
  const nativeDictationStopRequestedRef = useRef(false);
  const nativeDictationBaseDraftRef = useRef("");
  const nativeDictationConversationIDRef = useRef("");
  const recordingConversationIDRef = useRef("");
  const messageScrollRef = useRef<HTMLDivElement>(null);
  const handledNotificationIDsRef = useRef(new Set<string>());
  const notificationPreferencesRef = useRef({
    finished: true,
    needsInput: true,
  });
  const computerCloseRef = useRef<HTMLButtonElement>(null);
  const computerFrameRef = useRef<HTMLImageElement>(null);
  const computerFrameFocusPendingRef = useRef(false);
  const settingsCloseRef = useRef<HTMLButtonElement>(null);
  const teachGoalRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    let active = true;
    async function loadNativeAPICredential() {
      const auth = await readNativeAPIToken();
      if (auth) {
        setNativeRuntime(true);
        runtimeAPIToken = typeof auth.token === "string" ? auth.token : "";
        setNativeStartupError(
          typeof auth.startup_error === "string" ? auth.startup_error : null,
        );
      } else if (NATIVE_RUNTIME_AVAILABLE) {
        setNativeRuntime(true);
        setNativeStartupError(
          "The local OpenAgentFleet credential is not available yet.",
        );
      } else {
        setNativeRuntime(false);
        runtimeAPIToken = "";
        setNativeStartupError(null);
      }
      if (active) setAPIReady(true);
    }
    void loadNativeAPICredential();
    return () => {
      active = false;
    };
  }, []);

  useEffect(() => {
    if (!NATIVE_RUNTIME_AVAILABLE) return;
    let disposed = false;
    let unlisten: (() => void) | undefined;

    void invoke<NativeDictationStatus>("native_dictation_status")
      .then((status) => {
        if (!disposed) setNativeDictationAvailable(status.available);
      })
      .catch(() => {
        if (!disposed) setNativeDictationAvailable(false);
      });

    void listen<NativeDictationEvent>("native-dictation", (event) => {
      const payload = event.payload;
      const activeSession = nativeDictationSessionRef.current;
      if (!activeSession || payload.session_id !== activeSession) return;
      const recordingConversationID =
        nativeDictationConversationIDRef.current;
      if (
        recordingConversationID &&
        selectedConversationIDRef.current &&
        recordingConversationID !== selectedConversationIDRef.current
      ) {
        void invoke("native_dictation_cancel", {
          session_id: payload.session_id,
        }).catch(() => {
          // The native bridge may already have torn the session down.
        });
        setRecording(false);
        nativeDictationSessionRef.current = null;
        nativeDictationBaseDraftRef.current = "";
        nativeDictationConversationIDRef.current = "";
        setNotice("Dictation stopped because the active chat changed.");
        return;
      }
      const text = payload.text?.trim();
      if (text && (payload.state === "partial" || payload.state === "final")) {
        const base = nativeDictationBaseDraftRef.current.trim();
        setDraft(base ? `${base} ${text}` : text);
      }
      if (payload.state === "started") {
        setRecording(true);
      } else if (payload.state === "final") {
        setRecording(false);
      } else if (payload.state === "stopped" || payload.state === "cancelled") {
        setRecording(false);
        nativeDictationSessionRef.current = null;
        nativeDictationBaseDraftRef.current = "";
        nativeDictationConversationIDRef.current = "";
        nativeDictationStopRequestedRef.current = false;
      } else if (payload.state === "failed") {
        setRecording(false);
        void invoke("native_dictation_cancel", {
          session_id: payload.session_id,
        }).catch(() => {
          // Keep the session reference so a later explicit stop can retry cleanup.
        });
        setNotice(payload.detail ?? "Native dictation failed.");
      }
      if (
        payload.state === "final" &&
        activeSession === payload.session_id &&
        !nativeDictationStopRequestedRef.current
      ) {
        // A final recognition callback does not stop AVAudioEngine by itself.
        // Explicitly finish the native task so the next recording can start.
        nativeDictationStopRequestedRef.current = true;
        void invoke("native_dictation_stop", {
          session_id: payload.session_id,
        }).catch(() => {
          // The native task may already have completed during teardown.
        });
      }
    })
      .then((cleanup) => {
        if (disposed) cleanup();
        else unlisten = cleanup;
      })
      .catch(() => {
        // Browser builds and older Tauri runtimes simply keep the fallback.
      });

    return () => {
      disposed = true;
      unlisten?.();
      const sessionID = nativeDictationSessionRef.current;
      if (sessionID) {
        void invoke("native_dictation_cancel", { session_id: sessionID });
      }
      nativeDictationStopRequestedRef.current = false;
      nativeDictationConversationIDRef.current = "";
    };
  }, []);

  useEffect(() => {
    return () => {
      browserSpeechRef.current?.abort();
      browserSpeechRef.current = null;
      browserSpeechBaseDraftRef.current = "";
      browserSpeechConversationIDRef.current = "";
    };
  }, []);

  async function load(conversationID = selectedConversationID) {
    const requestID = ++loadRequestIDRef.current;
    if (nativeRuntime && !runtimeAPIToken) {
      if (requestID === loadRequestIDRef.current) {
        setError(
          nativeStartupError ??
            "The local OpenAgentFleet service could not start.",
        );
        setLoading(false);
      }
      return;
    }
    try {
      const path = conversationID
        ? `/api/bootstrap?conversation_id=${encodeURIComponent(conversationID)}`
        : "/api/bootstrap";
      const response = await apiFetch(path);
      if (!response.ok) {
		throw new Error(`botd returned ${response.status}`);
      }
      const payload: unknown = await response.json();
      if (!isBootstrap(payload)) {
        throw new Error("OpenAgentFleet received incomplete workspace data");
      }
      if (requestID !== loadRequestIDRef.current) return;
      const next = payload;
      setData(next);
      setMemoryBotID((current) =>
        next.bots.some((item) => item.id === current)
          ? current
          : next.bots[0]?.id ?? "",
      );
      if (Array.isArray(next.memories)) setMemories(next.memories);
      setPendingAttachments(
        (next.attachments ?? []).filter(
          (attachment) =>
            attachment.conversation_id === next.conversation.id &&
            !attachment.message_id,
        ),
      );
      selectedConversationIDRef.current = next.conversation.id;
      setSelectedConversationID(next.conversation.id);
      const latestRun = next.runs?.[next.runs.length - 1];
      if (latestRun) setLastRun(latestRun);
      setError(null);
    } catch (loadError) {
      if (requestID !== loadRequestIDRef.current) return;
      setError(
        nativeStartupError ??
          (loadError instanceof Error ? loadError.message : "botd is unavailable"),
      );
    } finally {
      if (requestID === loadRequestIDRef.current) setLoading(false);
    }
  }

  async function refreshHarnessAuth(
    target: "grok" | "codex_app_server",
  ) {
    setAuthRefreshBusy(target);
    setNotice(null);
    try {
      const response = await apiFetch("/api/harnesses/auth");
      const payload = (await response.json()) as {
        auth?: HarnessAuth[];
        model_catalog?: ModelCatalogEntry[];
        error?: string;
      };
      if (!response.ok) {
        throw new Error(
          payload.error ?? "Auth status returned " + response.status,
        );
      }
      const next = payload.auth?.find((item) => item.provider === target);
      if (!next) {
        throw new Error(harnessLabel(target) + " auth status was not returned");
      }
      setData((current) =>
        current
          ? {
              ...current,
              auth: [
                ...(current.auth ?? []).filter(
                  (item) => item.provider !== target,
                ),
                next,
              ],
              model_catalog: payload.model_catalog ?? current.model_catalog,
            }
          : current,
      );
    } catch (authError) {
      setNotice(
        authError instanceof Error
          ? authError.message
          : "Harness login status could not be refreshed",
      );
    } finally {
      setAuthRefreshBusy(null);
    }
  }

  useEffect(() => {
    if (!apiReady) return;
    void load(selectedConversationID);
    const interval = window.setInterval(
      () => void load(selectedConversationID),
      4000,
    );
    return () => window.clearInterval(interval);
  }, [selectedConversationID, apiReady, nativeStartupError, nativeRuntime]);

  useEffect(() => {
    if (!apiReady) return;
    const conversationID = data?.conversation.id ?? "";
    if (!conversationID) return;
    const controller = new AbortController();
    let cancelled = false;
    let lastEventID = "";
    const pause = (milliseconds: number) =>
      new Promise<void>((resolve) => window.setTimeout(resolve, milliseconds));
    async function connect() {
      while (!cancelled) {
        try {
          const headers = lastEventID
            ? { "Last-Event-ID": lastEventID }
            : undefined;
          const response = await apiFetch(
            `/api/events?conversation_id=${encodeURIComponent(conversationID)}`,
            { signal: controller.signal, headers },
          );
          if (!response.ok || !response.body)
            throw new Error(`event channel returned ${response.status}`);
          setNotice((current) =>
            current?.startsWith("Live channel:") ? null : current,
          );
          const reader = response.body.getReader();
          const decoder = new TextDecoder();
          let buffer = "";
          while (!cancelled) {
            const chunk = await reader.read();
            if (chunk.done) break;
            buffer += decoder.decode(chunk.value, { stream: true });
            const blocks = buffer.split("\n\n");
            buffer = blocks.pop() ?? "";
            for (const block of blocks) {
              const idLine = block
                .split("\n")
                .find((line) => line.startsWith("id:"));
              if (idLine) lastEventID = idLine.slice(3).trim();
              const payload = block
                .split("\n")
                .filter((line) => line.startsWith("data:"))
                .map((line) => line.slice(5).trim())
                .join("\n");
              if (!payload) continue;
              try {
                const event = JSON.parse(payload) as StreamEvent;
                if (event.type !== "ready") handleStreamEvent(event);
              } catch {
                // Ignore malformed provider data; the durable bootstrap remains authoritative.
              }
            }
          }
        } catch (streamError) {
          if (!controller.signal.aborted)
            setNotice(
              streamError instanceof Error
                ? `Live channel: ${streamError.message}`
                : "Live channel unavailable",
            );
        }
        if (!cancelled) await pause(1000);
      }
    }
    void connect();
    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [data?.conversation.id, apiReady]);

  useEffect(() => {
    if (data?.computer.url && data.computer.url !== "about:blank")
      setComputerURL(data.computer.url);
  }, [data?.computer.url]);

  useEffect(() => {
    if (
      !data?.computer.running ||
      computerViewMode !== "browser"
    ) {
      if (frameURLRef.current) URL.revokeObjectURL(frameURLRef.current);
      frameURLRef.current = null;
      setComputerFrameURL(null);
      if (computerViewOpen && computerViewMode === "browser") {
        setComputerFrameState({ surface: "browser", status: "idle" });
      }
      return;
    }
    let cancelled = false;
    let timer = 0;
    const controller = new AbortController();
    if (computerViewOpen) {
      setComputerFrameState({ surface: "browser", status: "loading" });
    }
    async function pollFrame() {
      try {
        const response = await apiFetch(`/api/computer/frame?ts=${Date.now()}`, {
          signal: controller.signal,
        });
        if (!response.ok) throw new Error(`frame returned ${response.status}`);
        const blob = await response.blob();
        if (await isNearBlankFrame(blob))
          throw new Error("Chromium is repainting the live frame");
        const nextURL = URL.createObjectURL(blob);
        if (cancelled) {
          URL.revokeObjectURL(nextURL);
        } else {
          if (frameURLRef.current) URL.revokeObjectURL(frameURLRef.current);
          frameURLRef.current = nextURL;
          setComputerFrameURL(nextURL);
          setData((current) =>
            current
              ? { ...current, computer: { ...current.computer, browser_ready: true } }
              : current,
          );
          if (computerViewOpen) {
            setComputerFrameState({ surface: "browser", status: "ready" });
          }
        }
      } catch {
        if (!cancelled && !controller.signal.aborted && computerViewOpen) {
          setComputerFrameState({
            surface: "browser",
            status: "error",
            error: "Chromium is warming up. The live frame will appear automatically.",
          });
        }
      }
      if (!cancelled)
        timer = window.setTimeout(
          () => void pollFrame(),
          computerViewOpen ? 280 : 2400,
        );
    }
    void pollFrame();
    return () => {
      cancelled = true;
      controller.abort();
      window.clearTimeout(timer);
      if (frameURLRef.current) URL.revokeObjectURL(frameURLRef.current);
      frameURLRef.current = null;
      setComputerFrameURL(null);
    };
  }, [
    computerViewOpen,
    computerViewMode,
    computerFrameRetryKey,
    data?.computer.running,
  ]);

  useEffect(() => {
    if (
      !data?.computer.running ||
      computerViewMode !== "desktop"
    ) {
      if (desktopFrameURLRef.current)
        URL.revokeObjectURL(desktopFrameURLRef.current);
      desktopFrameURLRef.current = null;
      setDesktopFrameURL(null);
      if (computerViewOpen && computerViewMode === "desktop") {
        setComputerFrameState({ surface: "desktop", status: "idle" });
      }
      return;
    }
    let cancelled = false;
    let timer = 0;
    const controller = new AbortController();
    if (computerViewOpen) {
      setComputerFrameState({ surface: "desktop", status: "loading" });
    }
    async function pollFrame() {
      try {
        const response = await apiFetch(
          `/api/computer/desktop/frame?ts=${Date.now()}`,
          { signal: controller.signal },
        );
        if (!response.ok)
          throw new Error(`desktop frame returned ${response.status}`);
        const blob = await response.blob();
        if (await isNearBlankFrame(blob))
          throw new Error("The virtual desktop is repainting the live frame");
        const nextURL = URL.createObjectURL(blob);
        if (cancelled) {
          URL.revokeObjectURL(nextURL);
        } else {
          if (desktopFrameURLRef.current)
            URL.revokeObjectURL(desktopFrameURLRef.current);
          desktopFrameURLRef.current = nextURL;
          setDesktopFrameURL(nextURL);
          setData((current) =>
            current
              ? { ...current, computer: { ...current.computer, desktop_ready: true } }
              : current,
          );
          setComputerFrameState({ surface: "desktop", status: "ready" });
        }
      } catch {
        if (!cancelled && !controller.signal.aborted) {
          setComputerFrameState({
            surface: "desktop",
            status: "error",
            error: "The desktop is warming up. The live frame will appear automatically.",
          });
        }
      }
      if (!cancelled) {
        timer = window.setTimeout(
          () => void pollFrame(),
          computerViewOpen ? 280 : 1200,
        );
      }
    }
    void pollFrame();
    return () => {
      cancelled = true;
      controller.abort();
      window.clearTimeout(timer);
      if (desktopFrameURLRef.current)
        URL.revokeObjectURL(desktopFrameURLRef.current);
      desktopFrameURLRef.current = null;
      setDesktopFrameURL(null);
    };
  }, [
    computerViewOpen,
    computerViewMode,
    computerFrameRetryKey,
    data?.computer.running,
  ]);

  useEffect(() => {
    if (!computerViewOpen) return;
    const preferred = preferences.computer?.default_surface ?? "desktop";
    setComputerViewMode(preferred);
  }, [
    computerViewOpen,
    preferences.computer?.default_surface,
  ]);

  useEffect(() => {
    if (!apiReady) return;
    void refreshSTTStatus();
  }, [apiReady]);

  useEffect(() => {
    if (!apiReady) return;
    let cancelled = false;
    async function loadWorkspaceControls() {
      const requestID = ++workspaceControlsRequestIDRef.current;
      const [preferencesResult, teachResult, integrationsResult] =
        await Promise.allSettled([
          apiFetch("/api/preferences"),
          apiFetch("/api/teach"),
          apiFetch("/api/integrations"),
        ]);
      if (
        cancelled ||
        requestID !== workspaceControlsRequestIDRef.current
      )
        return;
      if (
        preferencesResult.status === "fulfilled" &&
        preferencesResult.value.ok
      ) {
        const next = (await preferencesResult.value.json()) as Preferences;
        if (
          cancelled ||
          requestID !== workspaceControlsRequestIDRef.current
        )
          return;
        setPreferences(next);
        if (next.onboarding?.completed) {
          onboardingCompletionRef.current = true;
          onboardingLeadHydratedRef.current = true;
          setOnboardingOpen(false);
        } else if (!onboardingCompletionRef.current) {
          setOnboardingOpen(true);
          if (!onboardingLeadHydratedRef.current) {
            const persistedLead =
              next.workspace?.engine === "codex_app_server"
                ? "codex_app_server"
                : next.workspace?.engine === "opencode"
                  ? "opencode"
                  : "grok_build";
            setOnboardingLead(persistedLead);
            setOnboardingModel(
              next.workspace?.model ??
                defaultLeadModel(persistedLead, data?.model_catalog ?? []),
            );
            const persistedReasoning = next.usage?.reasoning_effort ?? "high";
            setOnboardingReasoning(
              persistedLead === "codex_app_server" &&
                ["low", "medium", "high", "xhigh", "max"].includes(
                  persistedReasoning,
                )
                ? persistedReasoning
                : ["low", "medium", "high"].includes(persistedReasoning)
                  ? persistedReasoning
                  : "high",
            );
            onboardingLeadHydratedRef.current = true;
          }
        }
        if (next.workspace?.engine) setProvider(next.workspace.engine);
        else if (next.usage?.default_worker)
          setProvider(next.usage.default_worker);
        if (next.usage?.reasoning_effort)
          setReasoningEffort(next.usage.reasoning_effort);
        if (next.usage?.permission_mode)
          setPermissionMode(next.usage.permission_mode);
      }
      if (teachResult.status === "fulfilled" && teachResult.value.ok) {
        const payload = (await teachResult.value.json()) as TeachResponse;
        const next = teachStatusFromResponse(payload);
        if (
          !cancelled &&
          requestID === workspaceControlsRequestIDRef.current &&
          next
        )
          setTeach(next);
      }
      if (
        integrationsResult.status === "fulfilled" &&
        integrationsResult.value.ok
      ) {
        const next = (await integrationsResult.value.json()) as
          | Integration[]
          | { integrations?: Integration[] };
        if (
          !cancelled &&
          requestID === workspaceControlsRequestIDRef.current
        ) {
          setIntegrations(
            Array.isArray(next) ? next : (next.integrations ?? []),
          );
          setIntegrationsError(null);
        }
      } else if (
        !cancelled &&
        requestID === workspaceControlsRequestIDRef.current
      ) {
        const detail =
          integrationsResult.status === "fulfilled"
            ? `Inventory unavailable (${integrationsResult.value.status})`
            : "Inventory unavailable";
        setIntegrationsError(detail);
      }
    }
    void loadWorkspaceControls();
    const interval = window.setInterval(() => {
      void loadWorkspaceControls();
    }, 7000);
    return () => {
      cancelled = true;
      window.clearInterval(interval);
    };
  }, [apiReady]);

  useEffect(() => {
    const interval = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(interval);
  }, []);

  useEffect(() => {
    if (!botMenuID) return;
    const closeOnOutsidePress = () => setBotMenuID(null);
    document.addEventListener("pointerdown", closeOnOutsidePress);
    return () => document.removeEventListener("pointerdown", closeOnOutsidePress);
  }, [botMenuID]);

  useEffect(() => {
    if (!settingsOpen || !apiReady) return;
    void loadMobileDevices();
  }, [settingsOpen, apiReady]);

  useEffect(() => {
    if (!apiReady || (!onboardingOpen && !settingsOpen)) return;
    void loadSearchConnectors();
  }, [apiReady, onboardingOpen, settingsOpen]);

  useEffect(() => {
    if (!settingsOpen || !apiReady || !memoryBotID) return;
    void loadMemories(memoryBotID);
  }, [settingsOpen, apiReady, memoryBotID]);

  useEffect(() => {
    if (
      !mobilePairingBundle ||
      new Date(mobilePairingBundle.expiresAt).getTime() > now
    ) {
      return;
    }
    setMobilePairingBundle(null);
    setMobilePairingExpired(true);
    setMobileBundleCopied(false);
  }, [mobilePairingBundle, now]);

  useEffect(() => {
    document.documentElement.dataset.theme =
      preferences.appearance?.theme ?? "system";
    document.documentElement.dataset.density =
      preferences.appearance?.density ?? "comfortable";
    document.documentElement.style.setProperty(
      "--font-scale",
      String(preferences.appearance?.font_scale ?? 1),
    );
  }, [preferences]);

  useEffect(() => {
    if (!computerViewOpen && !settingsOpen && !teachGoalOpen && !agentBuilderOpen) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      if (botMenuID) setBotMenuID(null);
      else if (agentBuilderOpen) setAgentBuilderOpen(false);
      else if (teachGoalOpen) setTeachGoalOpen(false);
      else if (settingsOpen) closeSettings();
      else setComputerViewOpen(false);
    };
    window.addEventListener("keydown", onKeyDown);
    const focusTarget = agentBuilderOpen
      ? undefined
      : teachGoalOpen
      ? teachGoalRef.current
      : settingsOpen
        ? settingsCloseRef.current
        : computerCloseRef.current;
    window.setTimeout(() => focusTarget?.focus(), 0);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [computerViewOpen, settingsOpen, teachGoalOpen, agentBuilderOpen, botMenuID]);

  useEffect(
    () => () => {
      const recorder = mediaRecorderRef.current;
      if (recorder && recorder.state !== "inactive") {
        recorder.ondataavailable = null;
        recorder.onstop = null;
        recorder.stop();
      }
      mediaRecorderRef.current = null;
      mediaStreamRef.current?.getTracks().forEach((track) => track.stop());
      mediaStreamRef.current = null;
      recorderStartingRef.current = false;
    },
    [],
  );

  const messages = data?.messages ?? [];
  const bot =
    data?.bots.find((item) => item.id === data.conversation.bot_id) ??
    data?.bots[0];
	const activeAgent = data?.agents?.find((agent) => agent.bot.id === bot?.id);
	notificationPreferencesRef.current = {
		finished: activeAgent?.metadata?.notify_finished ?? true,
		needsInput: activeAgent?.metadata?.notify_needs_input ?? true,
	};
	const activeLead = activeAgent?.metadata?.lead;
	const activeEngine = activeLead?.harness ?? preferences.workspace?.engine ?? provider;
	const activeModel =
		activeLead?.model !== undefined
			? activeLead.model
			: preferences.workspace?.model ?? defaultLeadModel(activeEngine, data?.model_catalog ?? []);
  const availableHarnesses = useMemo(
    () =>
      data?.capabilities.filter(
        (capability) => capability.available && capability.name !== "docker",
      ) ?? [],
    [data],
  );
  const activeSecretRun = useMemo(
    () =>
      [...(data?.runs ?? [])]
        .reverse()
        .find((run) =>
          ["queued", "running", "waiting_for_approval"].includes(
            run.status,
          ),
        ) ?? null,
    [data?.runs],
  );
  const pendingApprovals = useMemo(() => {
    const approvals = data?.approvals ?? [];
    const runs = data?.runs ?? [];
    if (runs.length === 0) return approvals;
    const runIDs = new Set(
      runs
        .filter(
          (run) =>
            run.status === "waiting_for_approval" &&
            (!run.conversation_id ||
              run.conversation_id === data?.conversation.id),
        )
        .map((run) => run.id),
    );
    return approvals.filter((approval) => runIDs.has(approval.run_id));
  }, [data?.approvals, data?.conversation.id, data?.runs]);
  const attachmentsByMessage = useMemo(() => {
    const grouped: Record<string, Attachment[]> = {};
    for (const attachment of data?.attachments ?? []) {
      if (!attachment.message_id) continue;
      grouped[attachment.message_id] = [
        ...(grouped[attachment.message_id] ?? []),
        attachment,
      ];
    }
    return grouped;
  }, [data?.attachments]);
  const speechToText = sttStatus ?? data?.stt;
  const voiceInputUnavailable = NATIVE_RUNTIME_AVAILABLE
    ? nativeRuntime && !nativeDictationAvailable && speechToText?.available !== true
    : speechToText?.available !== true && !browserSpeechAvailable;

  useEffect(() => {
    const element = messageScrollRef.current;
    if (!element) return;
    const distanceFromBottom =
      element.scrollHeight - element.scrollTop - element.clientHeight;
    if (distanceFromBottom > 140) return;
    const frame = window.requestAnimationFrame(() => {
      element.scrollTop = element.scrollHeight;
    });
    return () => window.cancelAnimationFrame(frame);
  }, [messages.length, liveOutput, lastRun?.status]);
  const onboardingEngines = useMemo(() => {
    const capability = (name: string) =>
      data?.capabilities.find((item) => item.name === name);
    const auth = (providerName: string) =>
      data?.auth?.find((item) => item.provider === providerName);
    return [
      {
        value: "grok_build" as const,
        label: "Grok Build",
        description: "Grok's local app harness with browser and computer tools.",
        available: capability("grok")?.available ?? false,
        authProvider: "grok" as const,
        auth: auth("grok"),
      },
      {
        value: "codex_app_server" as const,
        label: "Codex App Server",
        description: "Rich threads, approvals and ChatGPT OAuth through Codex.",
        available: capability("codex_app_server")?.available ?? false,
        authProvider: "codex_app_server" as const,
        auth: auth("codex_app_server"),
      },
      {
        value: "opencode" as const,
        label: "OpenCode",
        description:
          "Open-source local-provider fallback with a starter route whose availability and billing may vary.",
        available: capability("opencode")?.available ?? false,
        authProvider: null,
        auth: undefined,
      },
    ];
  }, [data]);

  const leadChoices = useMemo(
    () =>
      onboardingEngines.map((engine) => ({
        value: leadProviderValue(engine.value),
        label: engine.label,
        available:
          engine.available &&
          (!engine.authProvider || engine.auth?.authenticated === true),
        status: !engine.available
          ? "Not installed"
          : engine.authProvider && !engine.auth?.authenticated
            ? engine.auth?.login_required
              ? "Sign in required"
              : "Check connection"
            : "Ready",
      })),
    [onboardingEngines],
  );

  const selectedLeadEngine = leadProviderValue(
    preferences.workspace?.engine ?? provider,
  );
  const selectedLeadChoice =
    leadChoices.find((choice) => choice.value === selectedLeadEngine) ??
    leadChoices.find((choice) => choice.available) ??
    leadChoices[0];
  const selectedReasoning =
    preferences.usage?.reasoning_effort ?? reasoningEffort;
  const selectedReasoningValue = (
    selectedLeadChoice?.value === "codex_app_server"
      ? ["low", "medium", "high", "xhigh", "max"]
      : ["low", "medium", "high"]
  ).includes(selectedReasoning)
    ? selectedReasoning
    : "high";

  useEffect(() => {
    if (!onboardingOpen) return;
    if (
      onboardingEngines.some(
        (engine) => engine.value === onboardingLead && engine.available,
      )
    )
      return;
    const firstAvailable = onboardingEngines.find((engine) => engine.available);
    if (firstAvailable) {
      setOnboardingLead(firstAvailable.value);
      setOnboardingModel(defaultLeadModel(firstAvailable.value, data?.model_catalog ?? []));
    }
  }, [onboardingEngines, onboardingLead, onboardingOpen]);

  const teachRemainingSeconds = teach.deadline_at
    ? Math.max(
        0,
        Math.ceil((new Date(teach.deadline_at).getTime() - now) / 1000),
      )
    : 10 * 60;
  const teachTimer = `${String(Math.floor(teachRemainingSeconds / 60)).padStart(2, "0")}:${String(teachRemainingSeconds % 60).padStart(2, "0")}`;
  const mobilePairingRemainingSeconds = mobilePairingBundle
    ? Math.max(
        0,
        Math.ceil(
          (new Date(mobilePairingBundle.expiresAt).getTime() - now) / 1000,
        ),
      )
    : 0;
  const mobilePairingTimer = `${String(
    Math.floor(mobilePairingRemainingSeconds / 60),
  ).padStart(2, "0")}:${String(mobilePairingRemainingSeconds % 60).padStart(2, "0")}`;
  const pendingMobileRevoke = mobileRevokeCandidateID
    ? mobileDevices.find((device) => device.id === mobileRevokeCandidateID)
    : null;
  const memoryBot = data?.bots.find((item) => item.id === memoryBotID) ?? bot;
  const memoryDeleteCandidate = memoryDeleteCandidateID
    ? memories.find((memory) => memory.id === memoryDeleteCandidateID)
    : null;
  const selectedMemories = useMemo(
    () =>
      memories
        .filter((memory) => memory.bot_id === memoryBotID)
        .sort((left, right) => right.priority - left.priority),
    [memories, memoryBotID],
  );

  function clearMobilePairing() {
    setMobilePairingBundle(null);
    setMobilePairingExpired(false);
    setMobileBundleCopied(false);
    setMobileRevokeCandidateID(null);
  }

  function closeSettings() {
    clearMobilePairing();
    setMemoryDraft(null);
    setEditingMemoryID(null);
    setMemoryDeleteCandidateID(null);
    setWorkspaceModelDraft(null);
    setSettingsOpen(false);
  }

  function emptyMemoryDraft(): MemoryDraft {
    return { category: "fact", content: "", priority: "2", expires_at: "" };
  }

  function memoryDraftFor(memory: Memory): MemoryDraft {
    return {
      category: memory.category,
      content: memory.content,
      priority: String(memory.priority),
      expires_at: memory.expires_at ? memory.expires_at.slice(0, 10) : "",
    };
  }

  function memoryExpiryForAPI(value: string): string | null {
    if (!value) return null;
    return new Date(`${value}T00:00:00.000Z`).toISOString();
  }

  async function loadMemories(botID = memoryBotID) {
    if (!apiReady || !botID) return;
    setMemoriesLoading(true);
    setMemoriesError(null);
    try {
      const response = await apiFetch(
        `/api/memories?bot_id=${encodeURIComponent(botID)}`,
      );
      if (response.status === 404) {
        setMemoriesUnavailable(true);
        return;
      }
      const payload = (await response.json().catch(() => ({}))) as
        | Memory[]
        | { memories?: Memory[]; error?: string };
      if (!response.ok) {
        const detail = Array.isArray(payload) ? undefined : payload.error;
        throw new Error(detail ?? `Memories returned ${response.status}`);
      }
      setMemories(Array.isArray(payload) ? payload : (payload.memories ?? []));
      setMemoriesUnavailable(false);
    } catch (memoriesLoadError) {
      setMemoriesError(
        memoriesLoadError instanceof Error
          ? memoriesLoadError.message
          : "Memories could not be loaded",
      );
    } finally {
      setMemoriesLoading(false);
    }
  }

  function changeMemoryBot(nextBotID: string) {
    setMemoryBotID(nextBotID);
    setMemoryDraft(null);
    setEditingMemoryID(null);
    setMemoryDeleteCandidateID(null);
  }

  function openMemoryEditor(memory?: Memory) {
    setMemoriesError(null);
    setMemoryDeleteCandidateID(null);
    setEditingMemoryID(memory?.id ?? null);
    setMemoryDraft(memory ? memoryDraftFor(memory) : emptyMemoryDraft());
  }

  async function saveMemory(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!apiReady || !memoryBotID || !memoryDraft) return;
    const content = memoryDraft.content.trim();
    if (!content) {
      setMemoriesError("Memory content is required.");
      return;
    }
    const priority = Number(memoryDraft.priority);
    if (!Number.isInteger(priority) || priority < 1 || priority > 5) {
      setMemoriesError("Priority must be a whole number from one to five.");
      return;
    }
    const currentID = editingMemoryID;
    setMemoryBusyID(currentID ?? "new");
    setMemoriesError(null);
    try {
      const body = currentID
        ? {
            category: memoryDraft.category,
            content,
            priority,
            expires_at: memoryExpiryForAPI(memoryDraft.expires_at),
          }
        : {
            bot_id: memoryBotID,
            category: memoryDraft.category,
            content,
            priority,
            expires_at: memoryExpiryForAPI(memoryDraft.expires_at) ?? "",
          };
      const response = await apiFetch(
        currentID
          ? `/api/memories/${encodeURIComponent(currentID)}?bot_id=${encodeURIComponent(memoryBotID)}`
          : "/api/memories",
        {
          method: currentID ? "PATCH" : "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        },
      );
      const payload = (await response.json().catch(() => ({}))) as {
        error?: string;
      };
      if (response.status === 404) {
        setMemoriesUnavailable(true);
        return;
      }
      if (!response.ok)
        throw new Error(payload.error ?? `Memory returned ${response.status}`);
      setMemoryDraft(null);
      setEditingMemoryID(null);
      await loadMemories();
      setNotice(currentID ? "Memory updated." : "Memory added.");
    } catch (memorySaveError) {
      setMemoriesError(
        memorySaveError instanceof Error
          ? memorySaveError.message
          : "Memory could not be saved",
      );
    } finally {
      setMemoryBusyID(null);
    }
  }

  async function patchMemory(memory: Memory, patch: Partial<Memory>) {
    if (!apiReady) return;
    setMemoryBusyID(memory.id);
    setMemoriesError(null);
    try {
      const response = await apiFetch(
        `/api/memories/${encodeURIComponent(memory.id)}?bot_id=${encodeURIComponent(memory.bot_id)}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(patch),
        },
      );
      const payload = (await response.json().catch(() => ({}))) as {
        error?: string;
      };
      if (response.status === 404) {
        setMemoriesUnavailable(true);
        return;
      }
      if (!response.ok)
        throw new Error(payload.error ?? `Memory returned ${response.status}`);
      await loadMemories();
      setNotice(
        patch.status === "archived" ? "Memory archived." : "Memory restored.",
      );
    } catch (memoryPatchError) {
      setMemoriesError(
        memoryPatchError instanceof Error
          ? memoryPatchError.message
          : "Memory could not be updated",
      );
    } finally {
      setMemoryBusyID(null);
    }
  }

  async function deleteMemory(memory: Memory) {
    if (!apiReady) return;
    setMemoryBusyID(memory.id);
    setMemoriesError(null);
    try {
      const response = await apiFetch(
        `/api/memories/${encodeURIComponent(memory.id)}?bot_id=${encodeURIComponent(memory.bot_id)}`,
        { method: "DELETE" },
      );
      if (response.status === 404) {
        setMemoriesUnavailable(true);
        return;
      }
      if (!response.ok) {
        const payload = (await response.json().catch(() => ({}))) as {
          error?: string;
        };
        throw new Error(payload.error ?? `Delete returned ${response.status}`);
      }
      setMemoryDeleteCandidateID(null);
      await loadMemories();
      setNotice("Memory deleted.");
    } catch (memoryDeleteError) {
      setMemoriesError(
        memoryDeleteError instanceof Error
          ? memoryDeleteError.message
          : "Memory could not be deleted",
      );
    } finally {
      setMemoryBusyID(null);
    }
  }

  async function loadMobileDevices() {
    setMobileDevicesLoading(true);
    setMobileDevicesError(null);
    try {
      const response = await apiFetch("/api/mobile/devices");
      const payload = (await response.json()) as MobileDevicesResponse;
      if (!response.ok) {
        throw new Error(
          payload.error ?? `Mobile devices returned ${response.status}`,
        );
      }
      setMobileDevices(payload.devices ?? []);
    } catch (mobileDevicesLoadError) {
      setMobileDevicesError(
        mobileDevicesLoadError instanceof Error
          ? mobileDevicesLoadError.message
          : "Mobile devices could not be loaded",
      );
    } finally {
      setMobileDevicesLoading(false);
    }
  }

  async function createMobilePairing() {
    let baseURL = "";
    try {
      baseURL = normalizeTailnetEndpoint(mobileEndpoint);
    } catch (endpointError) {
      setMobileDevicesError(
        endpointError instanceof Error
          ? endpointError.message
          : "Enter a valid Tailnet HTTPS address",
      );
      return;
    }

    setMobilePairingBusy(true);
    setMobileDevicesError(null);
    setMobilePairingExpired(false);
    setMobileBundleCopied(false);
    try {
      const response = await apiFetch("/api/mobile/pairings", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ scope: mobileScopeProfile }),
      });
      const payload = (await response.json()) as MobilePairingResponse;
      if (
        !response.ok ||
        !payload.host_id ||
        !payload.grant ||
        !payload.pairing_secret
      ) {
        throw new Error(
          payload.error ?? `Pairing returned ${response.status}`,
        );
      }
      setMobilePairingBundle({
        grantID: payload.grant.id,
        expiresAt: payload.grant.expires_at,
        text: JSON.stringify(
          {
            version: 1,
            base_url: baseURL,
            host_id: payload.host_id,
            grant_id: payload.grant.id,
            pairing_secret: payload.pairing_secret,
            expires_at: payload.grant.expires_at,
          },
          null,
          2,
        ),
      });
    } catch (pairingError) {
      setMobileDevicesError(
        pairingError instanceof Error
          ? pairingError.message
          : "Pairing could not be created",
      );
    } finally {
      setMobilePairingBusy(false);
    }
  }

  async function copyMobilePairingBundle() {
    if (!mobilePairingBundle) return;
    try {
      await navigator.clipboard.writeText(mobilePairingBundle.text);
      setMobileBundleCopied(true);
    } catch {
      setMobileDevicesError("Copy is unavailable. Select the bundle and copy it manually.");
    }
  }

  async function revokeMobileDevice(device: MobileDevice) {
    setMobileRevokingID(device.id);
    setMobileDevicesError(null);
    try {
      const response = await apiFetch(
        `/api/mobile/devices/${encodeURIComponent(device.id)}/revoke`,
        { method: "POST" },
      );
      if (!response.ok) {
        const payload = (await response.json().catch(() => ({}))) as {
          error?: string;
        };
        throw new Error(payload.error ?? `Revoke returned ${response.status}`);
      }
      setMobileRevokeCandidateID(null);
      await loadMobileDevices();
    } catch (revokeError) {
      setMobileDevicesError(
        revokeError instanceof Error
          ? revokeError.message
          : "Device access could not be revoked",
      );
    } finally {
      setMobileRevokingID(null);
    }
  }

  function patchPreferences(
    patch: Record<string, unknown>,
    onError?: (message: string) => void,
  ): Promise<boolean> {
    const queued = preferencesPatchChainRef.current.then(async () => {
      try {
        const response = await apiFetch("/api/preferences", {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(patch),
        });
        const next = (await response.json()) as Preferences & { error?: string };
        if (!response.ok)
          throw new Error(
            next.error ?? `Preferences returned ${response.status}`,
          );
        setPreferences(next);
        const usage = next.usage;
        if (next.workspace?.engine) setProvider(next.workspace.engine);
        else if (usage?.default_worker) setProvider(usage.default_worker);
        if (usage?.reasoning_effort) setReasoningEffort(usage.reasoning_effort);
        if (usage?.permission_mode) setPermissionMode(usage.permission_mode);
        return true;
      } catch (preferencesError) {
        const message =
          preferencesError instanceof Error
            ? preferencesError.message
            : "Preferences could not be saved";
        setNotice(message);
        onError?.(message);
        return false;
      }
    });
    preferencesPatchChainRef.current = queued.then(
      () => undefined,
      () => undefined,
    );
    return queued;
  }

  async function loadSearchConnectors() {
    if (searchConnectorAvailability !== "available") {
      setSearchConnectorAvailability("loading");
    }
    setSearchConnectorsError(null);
    try {
      const response = await apiFetch("/api/search-connectors");
      if (response.status === 404) {
        setSearchConnectors(null);
        setSearchConnectorAvailability("absent");
        return;
      }
      if (!response.ok) {
        throw new Error(`Search connectors returned ${response.status}`);
      }
      const next = searchConnectorStateFrom(await response.json());
      if (!next) throw new Error("Search connector status was incomplete");
      setSearchConnectors(next);
      setSearchConnectorAvailability("available");
    } catch (connectorError) {
      setSearchConnectorAvailability("error");
      setSearchConnectorsError(
        connectorError instanceof Error
          ? connectorError.message
          : "Search connector status is temporarily unavailable.",
      );
    }
  }

  async function patchSearchConnector(
    connector: SearchConnectorID,
    enabled: boolean,
    source: "onboarding" | "settings",
  ) {
    if (!searchConnectors || searchConnectorAvailability !== "available") return;
    const requested: SearchConnectorState = {
      ...searchConnectors,
      web_search_plus_enabled:
        connector === "web_search_plus"
          ? enabled
          : searchConnectors.web_search_plus_enabled,
      hound_enabled:
        connector === "hound" ? enabled : searchConnectors.hound_enabled,
      donsetch_enabled:
        connector === "donsetch" ? enabled : searchConnectors.donsetch_enabled,
    };
    setSearchConnectorBusy(connector);
    setSearchConnectorsError(null);
    try {
      const response = await apiFetch("/api/search-connectors", {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          web_search_plus_enabled: requested.web_search_plus_enabled,
          hound_enabled: requested.hound_enabled,
          donsetch_enabled: requested.donsetch_enabled,
        }),
      });
      const responseText = await response.text();
      let responsePayload: unknown = null;
      if (responseText) {
        try {
          responsePayload = JSON.parse(responseText);
        } catch {
          responsePayload = null;
        }
      }
      if (response.status === 404) {
        setSearchConnectors(null);
        setSearchConnectorAvailability("absent");
        return;
      }
      if (!response.ok) {
        throw new Error(
          `Search connector setting could not be saved (${response.status})`,
        );
      }
      const confirmed = searchConnectorStateFrom(responsePayload) ?? requested;
      setSearchConnectors(confirmed);
      setSearchConnectorAvailability("available");
      const confirmedEnabled =
        connector === "web_search_plus"
          ? confirmed.web_search_plus_enabled
          : connector === "hound"
            ? confirmed.hound_enabled
            : confirmed.donsetch_enabled;
      const mcpID =
        connector === "web_search_plus"
          ? "web-search-plus"
          : connector === "hound"
            ? "hound"
            : "donsetch";
      setOnboardingConnectorMCPs((current) => {
        if (!confirmedEnabled) return current.filter((id) => id !== mcpID);
        if (source !== "onboarding" || current.includes(mcpID)) return current;
        return [...current, mcpID];
      });
    } catch (connectorError) {
      setSearchConnectorsError(
        connectorError instanceof Error
          ? connectorError.message
          : "Search connector setting could not be saved.",
      );
    } finally {
      setSearchConnectorBusy(null);
    }
  }

  async function finishOnboarding() {
    setOnboardingSaveError(null);
    const selectedEngine = onboardingEngines.find(
      (engine) => engine.value === onboardingLead,
    );
    if (!selectedEngine?.available) {
      setOnboardingSaveError(
        "No usable lead harness is selected. Install or connect a lead before finishing setup.",
      );
      return;
    }
    if (
      selectedEngine.authProvider &&
      selectedEngine.auth?.authenticated !== true
    ) {
      setOnboardingSaveError(
        `${selectedEngine.label} is installed but not signed in. Use Sign in on this step, or choose OpenCode for a local provider setup.`,
      );
      setOnboardingStep(0);
      return;
    }
    setOnboardingBusy(true);
    const saved = await patchPreferences(
      {
        onboarding: { completed: true },
        workspace: {
          engine: onboardingLead === "grok_build" ? "grok" : onboardingLead,
          model: onboardingModel,
        },
        usage: {
          reasoning_effort: onboardingReasoning,
        },
      },
      setOnboardingSaveError,
    );
    setOnboardingBusy(false);
    if (!saved) return;
    onboardingCompletionRef.current = true;
    setOnboardingOpen(false);
    setProvider(onboardingLead === "grok_build" ? "grok" : onboardingLead);
    if (data?.conversation.id) {
      setSelectedConversationID(data.conversation.id);
      void load(data.conversation.id);
    }
  }

  async function skipOnboarding() {
    setOnboardingSaveError(null);
    setOnboardingBusy(true);
    const saved = await patchPreferences(
      { onboarding: { completed: true } },
      setOnboardingSaveError,
    );
    setOnboardingBusy(false);
    if (!saved) return;
    onboardingCompletionRef.current = true;
    setOnboardingOpen(false);
  }

  function colimaRuntime() {
    return data?.runtimes?.find((runtime) => runtime.id === "colima");
  }

  function dockerEngineRuntime() {
    return data?.runtimes?.find((runtime) => runtime.id === "docker");
  }

  async function copyRuntimeInstallCommand(command: string) {
    try {
      await navigator.clipboard.writeText(command);
      setRuntimeCommandCopied(true);
    } catch {
      setRuntimeInstallError("Copy is unavailable. Select the command and copy it manually.");
    }
  }

  async function copyColimaInstallCommand() {
    await copyRuntimeInstallCommand(
      colimaRuntime()?.install_command ?? "brew install colima docker",
    );
  }

  async function installColima() {
    setRuntimeInstallBusy(true);
    setRuntimeInstallError(null);
    setRuntimeCommandCopied(false);
    try {
      const response = await apiFetch("/api/runtimes/colima/install", {
        method: "POST",
      });
      const payload = (await response.json()) as {
        error?: string;
        runtimes?: RuntimeInfo[];
      };
      if (!response.ok) {
        throw new Error(payload.error ?? `Colima installer returned ${response.status}`);
      }
      if (payload.runtimes) {
        setData((current) => current ? { ...current, runtimes: payload.runtimes } : current);
      }
      setNotice("Colima and the Docker CLI are installed. The VM starts only when Agent Computer is requested.");
      void load(data?.conversation.id ?? "");
    } catch (installError) {
      setRuntimeInstallError(
        installError instanceof Error ? installError.message : "Colima could not be installed.",
      );
    } finally {
      setRuntimeInstallBusy(false);
    }
  }

  function patchOptionalFeature(
    feature: keyof OptionalFeatures,
    enabled: boolean,
  ) {
    const features: OptionalFeatures = { [feature]: enabled };
    if (!enabled && feature === "routines") features.heartbeat = false;
    if (!enabled && feature === "remote_nodes") features.remote_control = false;
    void patchPreferences({ features });
  }

  async function teachAction(
    action: "start" | "pause" | "resume" | "stop" | "discard",
    body?: Record<string, string>,
  ) {
    setTeachBusy(true);
    try {
      const response = await apiFetch(`/api/teach/${action}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body ?? {}),
      });
      const payload = (await response.json()) as TeachResponse;
      if (!response.ok)
        throw new Error(payload.error ?? `Teach task returned ${response.status}`);
      const next = teachStatusFromResponse(payload);
      if (!next) throw new Error("Teach task returned an incomplete status");
      setTeach(next);
      if (payload.detail) setNotice(payload.detail);
      if (action === "start") {
        setTeachGoalOpen(false);
        setTeachGoal("");
      }
    } catch (teachError) {
      setNotice(
        teachError instanceof Error
          ? teachError.message
          : "Teach a task is unavailable",
      );
    } finally {
      setTeachBusy(false);
    }
  }

  function showRunNotification(message: string) {
    setNotice(message);
    if (
      typeof document === "undefined" ||
      document.visibilityState === "visible" ||
      typeof Notification === "undefined" ||
      Notification.permission !== "granted"
    ) {
      return;
    }
    try {
      new Notification("OpenAgentFleet", { body: message });
    } catch {
      // The in-app notice remains the reliable fallback in WebViews.
    }
  }

  async function requestDesktopNotifications() {
    if (typeof Notification === "undefined") {
      setDesktopNotificationPermission("unavailable");
      setNotice("Desktop notifications are unavailable in this client.");
      return;
    }
    try {
      const permission = await Notification.requestPermission();
      setDesktopNotificationPermission(permission);
      setNotice(
        permission === "granted"
          ? "Desktop notifications enabled."
          : "Desktop notifications remain off; in-app notices still work.",
      );
    } catch {
      setNotice("Desktop notification permission could not be requested.");
    }
  }

  function handleStreamEvent(event: StreamEvent) {
    let visibleEvent = event;
    if (event.type === "provider.output") {
      try {
        const payload = JSON.parse(event.data) as {
          text?: string;
          type?: string;
        };
        if (payload.type)
          visibleEvent = {
            ...event,
            type: `provider.${payload.type}`,
            data: payload.text ?? event.data,
          };
        if (payload.text && (!payload.type || payload.type === "text"))
          setLiveOutput((current) =>
            payload.type === "text" ? current + payload.text : payload.text!,
          );
      } catch {
        // Provider output is best-effort UI detail.
      }
    }
    setActivity((current) =>
      [
        visibleEvent,
        ...current.filter((item) => item.id !== visibleEvent.id),
      ].slice(0, 24),
    );
    if (event.type === "provider.output") return;
    if (event.type === "approval.requested") {
      if (
        !handledNotificationIDsRef.current.has(event.id) &&
        notificationPreferencesRef.current.needsInput
      ) {
        handledNotificationIDsRef.current.add(event.id);
        showRunNotification("Agent needs your approval before it can continue.");
      }
      try {
        const approval = JSON.parse(event.data) as Approval;
        setData((current) =>
          current
            ? {
                ...current,
                approvals: [
                  ...(current.approvals ?? []).filter(
                    (item) => item.id !== approval.id,
                  ),
                  approval,
                ],
              }
            : current,
        );
      } catch {
        // Approval details remain reloadable from bootstrap.
      }
      return;
    }
    if (event.type === "approval.resolved") {
      try {
        const approval = JSON.parse(event.data) as Approval;
        setData((current) =>
          current
            ? {
                ...current,
                approvals: (current.approvals ?? []).filter(
                  (item) => item.id !== approval.id,
                ),
              }
            : current,
        );
      } catch {
        // Approval details remain reloadable from bootstrap.
      }
      return;
    }
    const statusByEvent: Record<string, string> = {
      "run.queued": "queued",
      "run.started": "running",
      "run.waiting_for_approval": "waiting_for_approval",
      "run.resumed": "running",
      "run.blocked": "blocked",
      "run.failed": "failed",
      "run.stopped": "stopped",
      "run.completed": "completed",
    };
    const status = statusByEvent[event.type];
    if (!status || !event.run_id) return;
    let error = "";
    try {
      const payload = JSON.parse(event.data) as { error?: string };
      error = payload.error ?? "";
    } catch {
      // State transitions do not require structured detail.
    }
    setLastRun((current) =>
      current && current.id === event.run_id
        ? { ...current, status, ...(error ? { error } : {}) }
        : current,
    );
    setData((current) =>
      current
        ? {
            ...current,
            runs: (current.runs ?? []).map((run) =>
              run.id === event.run_id
                ? { ...run, status, ...(error ? { error } : {}) }
                : run,
            ),
          }
        : current,
    );
    const notifyFinished = notificationPreferencesRef.current.finished;
    const shouldNotify =
      notifyFinished &&
      (event.type === "run.completed" ||
        event.type === "run.failed" ||
        event.type === "run.blocked") &&
      !handledNotificationIDsRef.current.has(event.id);
    if (shouldNotify) {
      handledNotificationIDsRef.current.add(event.id);
      showRunNotification(
        event.type === "run.completed"
          ? "Agent finished the task."
          : event.type === "run.blocked"
            ? "Agent is blocked and needs your attention."
            : error || "Agent run failed; review the activity.",
      );
    }
  }

  async function refreshSTTStatus(): Promise<STTStatus> {
    try {
      const response = await apiFetch("/api/stt");
      const payload = (await response.json()) as STTStatus & { error?: string };
      if (!response.ok)
        throw new Error(payload.error ?? `botd returned ${response.status}`);
      setSTTStatus(payload);
      return payload;
    } catch (statusError) {
      const unavailable = {
        available: false,
        detail:
          statusError instanceof Error
            ? statusError.message
            : "Speech-to-text status unavailable",
      };
      setSTTStatus(unavailable);
      return unavailable;
    }
  }

  async function uploadAttachments(files: File[]) {
    if (!data || files.length === 0) return;
    if (attachmentUploadBusyRef.current) {
      setNotice("Attachments are still uploading. Try again when they finish.");
      return;
    }
    const remaining = Math.max(0, 10 - pendingAttachments.length);
    const selected = files.slice(0, remaining);
    if (selected.length < files.length)
      setNotice("A message can include at most 10 attachments.");
    if (selected.length === 0) return;

    const conversationID = data.conversation.id;
    attachmentUploadBusyRef.current = true;
    setUploadingAttachments((current) => current + selected.length);
    setNotice(null);
    try {
      for (const file of selected) {
        const form = new FormData();
        form.append("conversation_id", conversationID);
        form.append("file", file, file.name);
        const response = await apiFetch("/api/attachments", {
          method: "POST",
          body: form,
        });
        const payload = (await response.json()) as Attachment & {
          error?: string;
        };
        if (!response.ok)
          throw new Error(
            payload.error ?? `attachment upload returned ${response.status}`,
          );
        setPendingAttachments((current) =>
          selectedConversationIDRef.current !== conversationID
            ? current
            : current.some((attachment) => attachment.id === payload.id)
            ? current
            : [...current, payload],
        );
        setData((current) =>
          current && current.conversation.id === conversationID
            ? {
                ...current,
                attachments: [
                  ...(current.attachments ?? []).filter(
                    (attachment) => attachment.id !== payload.id,
                  ),
                  payload,
                ],
              }
            : current,
        );
      }
    } catch (attachmentError) {
      setNotice(
        attachmentError instanceof Error
          ? attachmentError.message
          : "Attachment could not be uploaded",
      );
    } finally {
      attachmentUploadBusyRef.current = false;
      setUploadingAttachments((current) =>
        Math.max(0, current - selected.length),
      );
    }
  }

  function isFileDrag(event: ReactDragEvent<HTMLFormElement>) {
    return Array.from(event.dataTransfer.types).includes("Files");
  }

  function handleComposerDragEnter(event: ReactDragEvent<HTMLFormElement>) {
    if (!isFileDrag(event)) return;
    event.preventDefault();
    composerDragDepthRef.current += 1;
    setComposerDraggingFiles(true);
  }

  function handleComposerDragOver(event: ReactDragEvent<HTMLFormElement>) {
    if (!isFileDrag(event)) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = attachmentUploadBusyRef.current
      ? "none"
      : "copy";
  }

  function handleComposerDragLeave(event: ReactDragEvent<HTMLFormElement>) {
    if (!isFileDrag(event)) return;
    composerDragDepthRef.current = Math.max(0, composerDragDepthRef.current - 1);
    if (composerDragDepthRef.current === 0) setComposerDraggingFiles(false);
  }

  function handleComposerDrop(event: ReactDragEvent<HTMLFormElement>) {
    if (!isFileDrag(event)) return;
    event.preventDefault();
    composerDragDepthRef.current = 0;
    setComposerDraggingFiles(false);
    void uploadAttachments(Array.from(event.dataTransfer.files));
  }

  async function removePendingAttachment(attachment: Attachment) {
    if (removingAttachmentIDs.includes(attachment.id)) return;
    setRemovingAttachmentIDs((current) => [...current, attachment.id]);
    try {
      const response = await apiFetch(
        `/api/attachments/${encodeURIComponent(attachment.id)}`,
        { method: "DELETE" },
      );
      const payload = (await response.json()) as { error?: string };
      if (!response.ok)
        throw new Error(
          payload.error ?? `attachment removal returned ${response.status}`,
        );
      setPendingAttachments((current) =>
        current.filter((item) => item.id !== attachment.id),
      );
      setData((current) =>
        current
          ? {
              ...current,
              attachments: (current.attachments ?? []).filter(
                (item) => item.id !== attachment.id,
              ),
            }
          : current,
      );
    } catch (attachmentError) {
      setNotice(
        attachmentError instanceof Error
          ? attachmentError.message
          : "Attachment could not be removed",
      );
    } finally {
      setRemovingAttachmentIDs((current) =>
        current.filter((id) => id !== attachment.id),
      );
    }
  }

  async function openAttachment(attachment: Attachment) {
    try {
      const response = await apiFetch(
        `/api/attachments/${encodeURIComponent(attachment.id)}`,
      );
      if (!response.ok)
        throw new Error(`attachment returned ${response.status}`);
      const objectURL = URL.createObjectURL(await response.blob());
      if (canPreviewAttachment(attachment.media_type)) {
        const attachmentWindow = window.open(
          objectURL,
          "_blank",
          "noopener,noreferrer",
        );
        if (!attachmentWindow)
          throw new Error("The browser blocked the attachment window");
      } else {
        const download = document.createElement("a");
        download.href = objectURL;
        download.download = attachment.name;
        download.rel = "noopener noreferrer";
        download.click();
      }
      window.setTimeout(() => URL.revokeObjectURL(objectURL), 60_000);
    } catch (attachmentError) {
      setNotice(
        attachmentError instanceof Error
          ? attachmentError.message
          : "Attachment could not be opened",
      );
    }
  }

  async function transcribeAudio(audio: Blob, conversationID: string) {
    if (!audio.size) return;
    setTranscribing(true);
    setNotice(null);
    try {
      const form = new FormData();
      const extension = audio.type.includes("mp4")
        ? "m4a"
        : audio.type.includes("ogg")
          ? "ogg"
          : "webm";
      form.append("audio", audio, `openagentfleet-voice.${extension}`);
      const response = await apiFetch("/api/transcriptions", {
        method: "POST",
        body: form,
      });
      const payload = (await response.json()) as {
        text?: string;
        error?: string;
      };
      if (!response.ok)
        throw new Error(
          payload.error ?? `transcription returned ${response.status}`,
        );
      const transcription = payload.text?.trim();
      if (!transcription) throw new Error("No speech was recognized");
      if (
        conversationID &&
        selectedConversationIDRef.current &&
        selectedConversationIDRef.current !== conversationID
      ) {
        setNotice("Speech-to-text finished for the previous chat; text was not inserted.");
        return;
      }
      setDraft((current) =>
        current.trim()
          ? `${current.trimEnd()} ${transcription}`
          : transcription,
      );
    } catch (transcriptionError) {
      setNotice(
        transcriptionError instanceof Error
          ? transcriptionError.message
          : "Speech-to-text failed",
      );
    } finally {
      setTranscribing(false);
    }
  }

  async function startNativeRecording(): Promise<"started" | "unavailable" | "failed" | "cancelled"> {
    if (!NATIVE_RUNTIME_AVAILABLE) return "unavailable";
    try {
      const status = await invoke<NativeDictationStatus>(
        "native_dictation_status",
      );
      setNativeDictationAvailable(status.available);
      if (!status.available) return "unavailable";
      const sessionID = crypto.randomUUID();
      nativeDictationBaseDraftRef.current = draft;
      nativeDictationSessionRef.current = sessionID;
      nativeDictationConversationIDRef.current = selectedConversationIDRef.current;
      nativeDictationStopRequestedRef.current = false;
      const started = await invoke<NativeDictationStatus>(
        "native_dictation_start",
        { session_id: sessionID },
      );
      if (!started.available || !started.running) {
        nativeDictationSessionRef.current = null;
        nativeDictationBaseDraftRef.current = "";
        nativeDictationConversationIDRef.current = "";
        return "unavailable";
      }
      setRecording(true);
      return "started";
    } catch (nativeError) {
      nativeDictationSessionRef.current = null;
      nativeDictationBaseDraftRef.current = "";
      nativeDictationConversationIDRef.current = "";
      setNotice(
        nativeError instanceof Error
          ? nativeError.message
          : "Native on-device dictation could not be started",
      );
      return nativeError instanceof Error && /cancelled/i.test(nativeError.message)
        ? "cancelled"
        : "failed";
    }
  }

  function startBrowserSpeechRecording(): "started" | "unavailable" | "failed" {
    if (NATIVE_RUNTIME_AVAILABLE) return "unavailable";
    const Recognition = browserSpeechRecognitionConstructor();
    if (!Recognition) return "unavailable";
    try {
      const recognition = new Recognition();
      const conversationID = selectedConversationIDRef.current;
      browserSpeechBaseDraftRef.current = draft;
      browserSpeechConversationIDRef.current = conversationID;
      recognition.continuous = true;
      recognition.interimResults = true;
      recognition.lang = navigator.language || "en-US";
      recognition.onstart = () => setRecording(true);
      recognition.onresult = (event) => {
        if (
          conversationID &&
          selectedConversationIDRef.current &&
          conversationID !== selectedConversationIDRef.current
        ) {
          recognition.stop();
          setRecording(false);
          setNotice("Dictation stopped because the active chat changed.");
          return;
        }
        let transcript = "";
        for (let index = 0; index < event.results.length; index += 1) {
          transcript += event.results[index]?.[0]?.transcript ?? "";
        }
        const trimmed = transcript.trim();
        if (!trimmed) return;
        const base = browserSpeechBaseDraftRef.current.trim();
        setDraft(base ? `${base} ${trimmed}` : trimmed);
      };
      recognition.onerror = (event) => {
        if (event.error !== "aborted") {
          setNotice(
            event.error === "not-allowed"
              ? "Browser microphone or speech permission was denied."
              : `Browser speech recognition failed${event.error ? `: ${event.error}` : "."}`,
          );
        }
        setRecording(false);
      };
      recognition.onend = () => {
        if (browserSpeechRef.current !== recognition) return;
        browserSpeechRef.current = null;
        browserSpeechBaseDraftRef.current = "";
        browserSpeechConversationIDRef.current = "";
        setRecording(false);
      };
      browserSpeechRef.current = recognition;
      recognition.start();
      setRecording(true);
      return "started";
    } catch (speechError) {
      browserSpeechRef.current = null;
      browserSpeechBaseDraftRef.current = "";
      browserSpeechConversationIDRef.current = "";
      setNotice(
        speechError instanceof Error
          ? speechError.message
          : "Browser speech recognition could not be started.",
      );
      return "failed";
    }
  }

  async function startRecording() {
    if (
      recording ||
      transcribing ||
      recorderStartingRef.current ||
      browserSpeechRef.current !== null ||
      mediaRecorderRef.current?.state === "recording"
    )
      return;
    recorderStartingRef.current = true;
    try {
      const nativeResult = await startNativeRecording();
      if (nativeResult === "started") return;
      if (nativeResult === "cancelled") return;
      const browserSpeechResult = startBrowserSpeechRecording();
      if (browserSpeechResult === "started") return;
      let currentSTTStatus = speechToText;
      if (!currentSTTStatus?.available)
        currentSTTStatus = await refreshSTTStatus();
      if (!currentSTTStatus?.available) {
        setNotice(
          nativeResult === "failed"
            ? "Native dictation failed and no remote STT fallback is configured."
            : currentSTTStatus?.detail ??
                "Speech-to-text is not configured on this OpenAgentFleet host.",
        );
        return;
      }
      if (
        !navigator.mediaDevices?.getUserMedia ||
        typeof MediaRecorder === "undefined"
      ) {
        setNotice("This client does not support microphone capture.");
        return;
      }
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      recordingConversationIDRef.current = selectedConversationIDRef.current;
      const mimeType = [
        "audio/webm;codecs=opus",
        "audio/webm",
        "audio/mp4",
      ].find((candidate) => MediaRecorder.isTypeSupported(candidate));
      const recorder = mimeType
        ? new MediaRecorder(stream, { mimeType })
        : new MediaRecorder(stream);
      const chunks: Blob[] = [];
      mediaStreamRef.current = stream;
      mediaRecorderRef.current = recorder;
      recorder.ondataavailable = (event) => {
        if (event.data.size > 0) chunks.push(event.data);
      };
      recorder.onerror = () => {
        setNotice("Microphone recording failed.");
        if (recorder.state !== "inactive") recorder.stop();
      };
      recorder.onstop = () => {
        stream.getTracks().forEach((track) => track.stop());
        mediaStreamRef.current = null;
        mediaRecorderRef.current = null;
        setRecording(false);
        const audio = new Blob(chunks, {
          type: recorder.mimeType || mimeType || "audio/webm",
        });
        const conversationID = recordingConversationIDRef.current;
        recordingConversationIDRef.current = "";
        void transcribeAudio(audio, conversationID);
      };
      recorder.start();
      setRecording(true);
    } catch (recordingError) {
      mediaStreamRef.current?.getTracks().forEach((track) => track.stop());
      mediaStreamRef.current = null;
      mediaRecorderRef.current = null;
      recordingConversationIDRef.current = "";
      setNotice(
        recordingError instanceof Error
          ? recordingError.message
          : "Microphone access could not be started",
      );
      setRecording(false);
    } finally {
      recorderStartingRef.current = false;
    }
  }

  function stopRecording() {
    const nativeSessionID = nativeDictationSessionRef.current;
    if (nativeSessionID) {
        void invoke("native_dictation_stop", { session_id: nativeSessionID }).catch(
        (stopError: unknown) => {
          setNotice(
            stopError instanceof Error
              ? stopError.message
              : "Native dictation could not be stopped",
          );
        },
      );
      return;
    }
    const browserSpeech = browserSpeechRef.current;
    if (browserSpeech) {
      browserSpeech.stop();
      setRecording(false);
      return;
    }
    const recorder = mediaRecorderRef.current;
    if (!recorder || recorder.state === "inactive") return;
    recorder.stop();
    setRecording(false);
  }

  async function submit(event: FormEvent | KeyboardEvent) {
    event.preventDefault();
    const content = draft.trim();
    const attachmentIDs = pendingAttachments.map((attachment) => attachment.id);
    if ((!content && attachmentIDs.length === 0) || !data || sending) return;
    const conversationID = data.conversation.id;
    setSending(true);
    setNotice(null);
    setLiveOutput("");
    try {
      const response = await apiFetch("/api/messages", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          conversation_id: conversationID,
          content,
          attachment_ids: attachmentIDs,
        }),
      });
      const payload = (await response.json()) as {
        message?: Message;
        run?: Run;
        error?: string;
      };
      if (!response.ok)
        throw new Error(payload.error ?? `botd returned ${response.status}`);
      const stillVisible = selectedConversationIDRef.current === conversationID;
      if (payload.message && stillVisible) {
        setData((current) =>
          current
            ? {
                ...current,
                messages: [...(current.messages ?? []), payload.message!],
                attachments: (current.attachments ?? []).map((attachment) =>
                  attachmentIDs.includes(attachment.id)
                    ? { ...attachment, message_id: payload.message!.id }
                    : attachment,
                ),
              }
            : current,
        );
        setPendingAttachments([]);
      }
      if (payload.run && stillVisible) {
        setLastRun(payload.run);
        setData((current) =>
          current
            ? {
                ...current,
                runs: [
                  ...(current.runs ?? []).filter(
                    (run) => run.id !== payload.run!.id,
                  ),
                  payload.run!,
                ],
              }
            : current,
        );
        if (payload.run.status === "blocked")
          setNotice(
            "Run saved. Harness execution is not enabled yet.",
          );
      }
      if (stillVisible) setDraft("");
    } catch (sendError) {
      if (selectedConversationIDRef.current === conversationID) {
        setNotice(
          sendError instanceof Error
            ? sendError.message
            : "Message could not be sent",
        );
      }
    } finally {
      setSending(false);
    }
  }

  async function ensureComputer(openView = true) {
    setComputerBusy(true);
    setNotice(null);
    try {
      const response = await apiFetch("/api/computer/ensure", {
        method: "POST",
      });
      const payload = (await response.json()) as Computer & { error?: string };
      if (!response.ok)
        throw new Error(
          payload.error ?? "Agent Computer could not be started",
        );
      setData((current) =>
        current ? { ...current, computer: payload } : current,
      );
      setComputerFrameRetryKey((current) => current + 1);
      if (openView) openComputerView(false);
    } catch (computerError) {
      setNotice(
        computerError instanceof Error
          ? computerError.message
          : "Agent Computer could not be started",
      );
    } finally {
      setComputerBusy(false);
    }
  }

  async function stopComputer() {
    if (computerBusy) return;
    setComputerBusy(true);
    try {
      const response = await apiFetch("/api/computer/stop", {
        method: "POST",
      });
      const payload = (await response.json()) as Computer & { error?: string };
      if (!response.ok)
        throw new Error(
          payload.error ?? "Agent Computer could not be stopped",
        );
      setComputerViewOpen(false);
      setComputerFrameURL(null);
      setDesktopFrameURL(null);
      setData((current) =>
        current ? { ...current, computer: payload } : current,
      );
      setNotice("Agent Computer stopped. The container will start again only when requested.");
    } catch (computerError) {
      setNotice(
        computerError instanceof Error
          ? computerError.message
          : "Agent Computer could not be stopped",
      );
    } finally {
      setComputerBusy(false);
    }
  }

  function openComputerView(ensure = true) {
    setNotice(null);
    setComputerViewOpen(true);
    if (!ensure || !data || computerBusy) return;
    const ready =
      data.computer.running &&
      Boolean(data.computer.browser_ready || data.computer.desktop_ready);
    if (!ready) void ensureComputer(false);
  }

  function retryComputerFrame() {
    if (!data) return;
    const surface = computerViewMode;
    const ready =
      surface === "desktop"
        ? Boolean(data.computer.desktop_ready)
        : data.computer.browser_ready;
    setComputerFrameState({ surface, status: "loading" });
    setComputerFrameRetryKey((current) => current + 1);
    if (!data.computer.running || !ready) void ensureComputer(false);
  }

  async function setComputerTakeover(enabled: boolean) {
    try {
      const response = await apiFetch("/api/computer/takeover", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled }),
      });
      const payload = (await response.json()) as Computer & { error?: string };
      if (!response.ok)
        throw new Error(payload.error ?? `botd returned ${response.status}`);
      setData((current) =>
        current ? { ...current, computer: payload } : current,
      );
      setNotice(
        enabled
          ? "Takeover enabled. Computer control remains with you; sensitive Teach steps are redacted."
          : "Takeover released. OpenAgentFleet can observe again, but cannot control the computer from this toolbar.",
      );
    } catch (takeoverError) {
      setNotice(
        takeoverError instanceof Error
          ? takeoverError.message
          : "Takeover could not be changed",
      );
    }
  }

  async function setComputerAgentControl(enabled: boolean) {
    try {
      const response = await apiFetch("/api/computer/agent-control", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled }),
      });
      const payload = (await response.json()) as Computer & { error?: string };
      if (!response.ok)
        throw new Error(payload.error ?? `botd returned ${response.status}`);
      setData((current) =>
        current ? { ...current, computer: payload } : current,
      );
      setNotice(
        enabled
          ? "Agent Control enabled. Approved Agent Computer tools can now control the isolated workspace."
          : "Agent Control paused. The workspace remains visible.",
      );
    } catch (agentControlError) {
      setNotice(
        agentControlError instanceof Error
          ? agentControlError.message
          : "Agent Control could not be changed",
      );
    }
  }

  async function computerAction(
    action: {
      action: string;
      url?: string;
      x?: number;
      y?: number;
      delta_x?: number;
      delta_y?: number;
      text?: string;
      key?: string;
      sensitive?: boolean;
    },
    surface: ComputerViewMode = "browser",
  ) {
    setComputerActionBusy(true);
    try {
      const endpoint =
        surface === "desktop"
          ? "/api/computer/desktop/action"
          : "/api/computer/action";
      const response = await apiFetch(endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(action),
      });
      const payload = (await response.json()) as Computer & { error?: string };
      if (!response.ok)
        throw new Error(
          payload.error ?? `computer action returned ${response.status}`,
        );
      setData((current) =>
        current ? { ...current, computer: payload } : current,
      );
    } catch (actionError) {
      setNotice(
        actionError instanceof Error
          ? actionError.message
          : "Computer action failed",
      );
    } finally {
      setComputerActionBusy(false);
    }
  }

  async function promptSecureHandoff(purpose: SecretPurpose) {
    if (!data || !activeSecretRun) {
      setNotice(
        "A secure handoff needs an active run that is waiting for your input.",
      );
      return;
    }
    if (!data.computer.takeover || data.computer.agent_control) {
      setNotice("Take control before entering a password or one-time code.");
      return;
    }
    if (activeComputerView !== "browser") {
      setNotice(
        "Secure handoff is currently available only for a focused Browser field, not the desktop.",
      );
      return;
    }
    if (!nativeRuntime) {
      setNotice(
        isLinuxHost(data)
          ? "Secure password entry is a native-app path. This web preview cannot collect it."
          : "Secure password entry is available in the OpenAgentFleet Mac app, not this web preview.",
      );
      return;
    }
    if (isLinuxHost(data)) {
      setNotice(
        "Secure password entry is macOS-only. Type the password in the Agent Computer after you take control.",
      );
      return;
    }
    setSecureHandoffBusy(purpose);
    try {
      const response = await apiFetch("/api/secret-handoffs", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          run_id: activeSecretRun.id,
          conversation_id: data.conversation.id,
          surface: activeComputerView,
          purpose,
        }),
      });
      const payload = (await response.json()) as SecretHandoffResponse;
      if (!response.ok || !payload.request?.id) {
        throw new Error(
          payload.error ?? `secure handoff returned ${response.status}`,
        );
      }
      await invoke("prompt_secret", {
        handoffId: payload.request.id,
        purpose,
      });
      setNotice(
        purpose === "password"
          ? "Password entered through the native secure prompt."
          : "One-time code entered through the native secure prompt.",
      );
      void load(data.conversation.id);
    } catch (handoffError) {
      setNotice(
        handoffError instanceof Error
          ? handoffError.message
          : "Secure handoff could not be completed",
      );
    } finally {
      setSecureHandoffBusy(null);
    }
  }

  async function openOAuthURL(url: string) {
    if (!isAllowedOAuthURL(url)) {
      throw new Error("The harness returned an untrusted OAuth URL");
    }
    if (nativeRuntime) {
      await openUrl(url);
      return;
    }
    const oauthWindow = window.open(url, "_blank", "noopener,noreferrer");
    if (!oauthWindow) throw new Error("The browser blocked the OAuth window");
  }

  async function startOAuth(
    target: "grok" | "codex_app_server",
    flow: "browser" | "device",
  ) {
    const busyKey = `${target}:${flow}`;
    setOAuthBusy(busyKey);
    setNotice(null);
    try {
      const response = await apiFetch(
        `/api/harnesses/${target}/oauth/${flow}`,
        { method: "POST" },
      );
      const payload = (await response.json()) as OAuthStart;
      if (!response.ok)
        throw new Error(payload.error ?? `botd returned ${response.status}`);
      if (payload.state)
        setData((current) =>
          current
            ? {
                ...current,
                auth: [
                  ...(current.auth ?? []).filter(
                    (item) => item.provider !== payload.state.provider,
                  ),
                  payload.state,
                ],
              }
            : current,
        );
      const oauthURL = payload.authorization_url ?? payload.verification_url;
      if (oauthURL) await openOAuthURL(oauthURL);
      if (payload.user_code) {
        setNotice(
          `OAuth page opened. Enter code ${payload.user_code} to finish ${harnessLabel(target)} sign-in.`,
        );
      } else if (target === "grok") {
        setNotice(
          `Grok Build opened its OAuth flow in the default browser on this ${hostDeviceName(data)}.`,
        );
      } else {
        setNotice(
          "ChatGPT OAuth page opened. OpenAgentFleet is waiting for Codex App Server confirmation.",
        );
      }
    } catch (oauthError) {
      setNotice(
        oauthError instanceof Error
          ? oauthError.message
          : "OAuth could not be started",
      );
    } finally {
      setOAuthBusy(null);
    }
  }

  function framePoint(
    event: ReactPointerEvent<HTMLImageElement>,
    surface: ComputerViewMode,
  ) {
    if (!data) return null;
    const bounds = event.currentTarget.getBoundingClientRect();
    const sourceWidth =
      data.computer.viewport_width || event.currentTarget.naturalWidth;
    const sourceHeight =
      data.computer.viewport_height || event.currentTarget.naturalHeight;
    if (!sourceWidth || !sourceHeight || !bounds.width || !bounds.height)
      return null;

    // Both live images use object-fit: contain. Calculate the actual painted
    // image rectangle instead of mapping against the full CSS box; this keeps
    // clicks aligned when the frame is letterboxed or retina-scaled.
    const sourceRatio = sourceWidth / sourceHeight;
    const boxRatio = bounds.width / bounds.height;
    const paintedWidth = boxRatio > sourceRatio
      ? bounds.height * sourceRatio
      : bounds.width;
    const paintedHeight = boxRatio > sourceRatio
      ? bounds.height
      : bounds.width / sourceRatio;
    const offsetX = (bounds.width - paintedWidth) / 2;
    const offsetY = (bounds.height - paintedHeight) / 2;
    const x = ((event.clientX - bounds.left - offsetX) / paintedWidth) * sourceWidth;
    const y = ((event.clientY - bounds.top - offsetY) / paintedHeight) * sourceHeight;
    if (x < 0 || y < 0 || x > sourceWidth || y > sourceHeight) return null;
    return { x, y, surface };
  }

  function handleComputerPointerDown(event: ReactPointerEvent<HTMLImageElement>) {
    if (!data || !data.computer.takeover || !data.computer.browser_ready)
      return;
    event.preventDefault();
    event.currentTarget.focus({ preventScroll: true });
    const point = framePoint(event, "browser");
    if (point) void computerAction({ action: "click", x: point.x, y: point.y });
  }

  function handleDesktopPointerDown(event: ReactPointerEvent<HTMLImageElement>) {
    if (!data || !data.computer.takeover || !data.computer.desktop_ready)
      return;
    event.preventDefault();
    event.currentTarget.focus({ preventScroll: true });
    const point = framePoint(event, "desktop");
    if (point)
      void computerAction({ action: "click", x: point.x, y: point.y }, "desktop");
  }

  function computerKey(event: ReactKeyboardEvent<HTMLImageElement>, surface: ComputerViewMode) {
    const browserNames: Record<string, string> = {
      " ": "Space",
      ArrowUp: "ArrowUp",
      ArrowDown: "ArrowDown",
      ArrowLeft: "ArrowLeft",
      ArrowRight: "ArrowRight",
      Backspace: "Backspace",
      Delete: "Delete",
      Escape: "Escape",
      Enter: "Enter",
      Tab: "Tab",
      Home: "Home",
      End: "End",
      PageUp: "PageUp",
      PageDown: "PageDown",
      Insert: "Insert",
      CapsLock: "CapsLock",
      Meta: "Meta",
      Control: "Control",
      Alt: "Alt",
      Shift: "Shift",
    };
    const desktopNames: Record<string, string> = {
      ...browserNames,
      " ": "space",
      ArrowUp: "Up",
      ArrowDown: "Down",
      ArrowLeft: "Left",
      ArrowRight: "Right",
      Backspace: "BackSpace",
      PageUp: "Page_Up",
      PageDown: "Page_Down",
      CapsLock: "Caps_Lock",
      Meta: "super",
      Control: "ctrl",
      Alt: "alt",
      Shift: "shift",
    };
    const names = surface === "desktop" ? desktopNames : browserNames;
    const base = names[event.key] ?? event.key;
    const modifiers = [
      event.ctrlKey ? (surface === "desktop" ? "ctrl" : "Control") : "",
      event.altKey ? (surface === "desktop" ? "alt" : "Alt") : "",
      event.shiftKey && event.key.length > 1
        ? surface === "desktop" ? "shift" : "Shift"
        : "",
      event.metaKey ? (surface === "desktop" ? "super" : "Meta") : "",
    ].filter(Boolean);
    return modifiers.length > 0 ? `${modifiers.join("+")}+${base}` : base;
  }

  function handleComputerKeyDown(event: ReactKeyboardEvent<HTMLImageElement>) {
    if (!data || !data.computer.takeover || data.computer.agent_control)
      return;
    if (event.nativeEvent.isComposing || event.key === "Dead" || event.repeat) return;
    event.preventDefault();
    const surface = activeComputerView;
    const hasModifier = event.ctrlKey || event.altKey || event.metaKey;
    if (event.key.length === 1 && !hasModifier) {
      void computerAction({ action: "type", text: event.key }, surface);
      return;
    }
    void computerAction({ action: "press", key: computerKey(event, surface) }, surface);
  }

  function approvalOptions(approval: Approval): ApprovalOption[] {
    try {
      const payload = JSON.parse(approval.payload) as {
        options?: ApprovalOption[];
      };
      return payload.options ?? [];
    } catch {
      return [];
    }
  }

  function approvalSummary(approval: Approval) {
    try {
      const payload = JSON.parse(approval.payload) as {
        tool_call?: Record<string, unknown>;
      };
      const toolCall = payload.tool_call;
      if (toolCall) {
        for (const key of ["title", "name", "description"]) {
          const value = toolCall[key];
          if (typeof value === "string" && value.trim()) {
            return redactApprovalText(value);
          }
        }
        if (typeof toolCall.command === "string" && toolCall.command.trim()) {
          return "Command execution requested (details hidden until approval).";
        }
      }
    } catch {
      // The action name remains a safe fallback for provider-specific payloads.
    }
    return "The agent wants to perform an external action.";
  }

  function redactApprovalText(value: string) {
    return value
      .replace(
        /((?:authorization|bearer|token|password|secret|api[_-]?key)\s*[:=]\s*)(["']?)[^\s,"']+/gi,
        "$1$2[redacted]",
      )
      .replace(/\b(?:sk|xai|ghp|github_pat)_[A-Za-z0-9_-]+\b/g, "[redacted-token]")
      .replace(/\bBearer\s+[^\s]+/gi, "Bearer [redacted]")
      .trim()
      .slice(0, 240);
  }

  async function resolveApproval(
    approval: Approval,
    status: "approved" | "denied",
    optionID?: string,
  ) {
    try {
      const response = await apiFetch(`/api/approvals/${approval.id}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ status, option_id: optionID ?? "" }),
      });
      const payload = (await response.json()) as Approval & { error?: string };
      if (!response.ok)
        throw new Error(payload.error ?? `botd returned ${response.status}`);
      setData((current) =>
        current
          ? {
              ...current,
              approvals: (current.approvals ?? []).filter(
                (item) => item.id !== approval.id,
              ),
            }
          : current,
      );
      setNotice(
        status === "approved"
          ? "Approval sent. The run can continue."
          : "Approval denied. The run will stop at this action.",
      );
      // Reload the additive transcript read model so the resolved decision
      // remains visible after the pending card disappears.
      void load(data?.conversation.id ?? "");
    } catch (approvalError) {
      setNotice(
        approvalError instanceof Error
          ? approvalError.message
          : "Approval could not be resolved",
      );
    }
  }

  async function stopRun(run: Run) {
    try {
      const response = await apiFetch(`/api/runs/${run.id}/stop`, {
        method: "POST",
      });
      const payload = (await response.json()) as { error?: string };
      if (!response.ok)
        throw new Error(payload.error ?? `botd returned ${response.status}`);
      setNotice(
        "Stop requested. OpenAgentFleet will keep the durable stopped state.",
      );
    } catch (stopError) {
      setNotice(
        stopError instanceof Error
          ? stopError.message
          : "Run could not be stopped",
      );
    }
  }

  async function loadGrokInfo(kind: GrokInfoKind) {
    setGrokInfoKind(kind);
    setGrokInfoBusy(true);
    try {
      const response = await apiFetch(`/api/grok/${kind}`);
      const payload = (await response.json()) as {
        output?: string;
        error?: string;
      };
      if (!response.ok)
        throw new Error(payload.error ?? `botd returned ${response.status}`);
      setGrokInfoOutput(payload.output ?? "");
    } catch (infoError) {
      setGrokInfoOutput(
        infoError instanceof Error
          ? infoError.message
          : "Grok Build information unavailable",
      );
    } finally {
      setGrokInfoBusy(false);
    }
  }

  async function createConversation() {
    if (!data) return;
    try {
      const response = await apiFetch("/api/conversations", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          bot_id: data.conversation.bot_id,
          title: "New conversation",
        }),
      });
      const payload = (await response.json()) as Conversation & {
        error?: string;
      };
      if (!response.ok)
        throw new Error(payload.error ?? `botd returned ${response.status}`);
      setActivity([]);
      setLiveOutput("");
      setNotice(null);
      selectedConversationIDRef.current = payload.id;
      setSelectedConversationID(payload.id);
      await load(payload.id);
    } catch (conversationError) {
      setNotice(
        conversationError instanceof Error
          ? conversationError.message
          : "Conversation could not be created",
      );
    }
  }

  function identifierList(value: string) {
    return value
      .split(",")
      .map((item) => item.trim())
      .filter(Boolean);
  }

  function newWorkerDraft(): AgentExecutionDraft {
    return {
      id: crypto.randomUUID(),
      harness: "opencode",
      model: "",
      reasoning: "high",
      tier: "default",
      permission: "provider_default",
      maxTurns: "12",
      timeout: "900",
    };
  }

  function updateAgentWorker(
    workerID: string,
    field: Exclude<keyof AgentExecutionDraft, "id">,
    value: string,
  ) {
    setAgentWorkers((workers) =>
      workers.map((worker) => {
        if (worker.id !== workerID) return worker;
        const next = { ...worker, [field]: value };
        if (field === "harness" && value === "opencode") {
          next.permission = "provider_default";
          next.tier = "default";
        } else if (field === "harness" && value === "grok") {
          if (["xhigh", "max"].includes(next.reasoning)) next.reasoning = "high";
          next.tier = "default";
          if (next.permission === "provider_default") next.permission = "ask";
        }
        if (field === "reasoning" && next.harness === "grok" && ["xhigh", "max"].includes(value)) {
          next.reasoning = "high";
        }
        if (field === "tier" && next.harness === "grok" && value !== "default") {
          next.tier = "default";
        }
        if (field === "permission" && next.harness === "opencode") {
          next.permission = "provider_default";
        }
        return next;
      }),
    );
  }

  function agentPermissionFromUsageDefault() {
    return preferences.usage?.permission_mode === "plan"
      ? "read_only"
      : "ask";
  }

  function workerExecutionBounds(
    worker: AgentExecutionDraft,
    index: number,
  ) {
    const maxTurns = Number(worker.maxTurns);
    if (!Number.isInteger(maxTurns) || maxTurns < 1 || maxTurns > 100) {
      setNotice(
        `Worker ${index + 1}: Max turns must be a whole number between 1 and 100.`,
      );
      return null;
    }
    const timeoutSeconds = Number(worker.timeout);
    if (
      !Number.isInteger(timeoutSeconds) ||
      timeoutSeconds < 30 ||
      timeoutSeconds > 3600
    ) {
      setNotice(
        `Worker ${index + 1}: Timeout must be a whole number between 30 and 3600 seconds.`,
      );
      return null;
    }
    return { maxTurns, timeoutSeconds };
  }

  function openAgentBuilder(leadOverride?: OnboardingLead) {
    const initialLead =
      leadOverride ?? (provider === "codex_app_server"
        ? "codex_app_server"
        : provider === "opencode"
          ? "opencode"
          : "grok_build");
    setAgentEditingID(null);
    setAgentName("");
    setAgentTitle("");
    setAgentDescription("");
    setAgentOrchestrator(false);
    setAgentLeadHarness(initialLead);
    setAgentModel(
      preferences.workspace?.model ?? defaultLeadModel(initialLead, data?.model_catalog ?? []),
    );
    setAgentLeadReasoning(preferences.usage?.reasoning_effort ?? "high");
    setAgentLeadTier("default");
    setAgentLeadPermission(
      initialLead === "opencode" ? "provider_default" : agentPermissionFromUsageDefault(),
    );
    setAgentLeadWebSearch("live");
    setAgentNotifyFinished(true);
    setAgentNotifyNeedsInput(true);
    setAgentWorkers([]);
    setAgentPlugins("");
    setAgentMCPs(onboardingConnectorMCPs.join(", "));
    setAgentAdvancedOpen(false);
    setAgentToolsOpen(onboardingConnectorMCPs.length > 0);
    setSelectedAgentTemplate(null);
    setAgentBuilderOpen(true);
  }

  function applyAgentTemplate(template: AgentTemplate) {
    setAgentName(template.name);
    setAgentTitle(template.title);
    setAgentDescription(template.description);
    setSelectedAgentTemplate(null);
  }

  function openAgentEditor(agent: Agent, leadOverride?: OnboardingLead) {
    const metadata = agent.metadata;
    const lead = metadata?.lead;
    const workspaceEngine = preferences.workspace?.engine ?? provider;
    const workspaceLead =
      workspaceEngine === "codex_app_server"
        ? "codex_app_server"
        : workspaceEngine === "opencode"
          ? "opencode"
          : "grok_build";
    const initialLead =
      leadOverride ??
      (lead?.harness === "codex_app_server"
        ? "codex_app_server"
        : lead?.harness === "opencode"
          ? "opencode"
          : lead
            ? "grok_build"
            : workspaceLead);
    const configuringSeed = leadOverride !== undefined;
    setAgentEditingID(agent.bot.id);
    setAgentName(agent.bot.name);
    setAgentTitle(agent.bot.title);
    setAgentDescription(agent.bot.description);
    setAgentOrchestrator(metadata?.orchestrator === "lead");
    setAgentLeadHarness(initialLead);
    setAgentModel(
      configuringSeed
        ? defaultLeadModel(initialLead, data?.model_catalog ?? [])
        : (lead?.model ?? preferences.workspace?.model ?? defaultLeadModel(initialLead, data?.model_catalog ?? [])),
    );
    setAgentLeadReasoning(
      configuringSeed
        ? (preferences.usage?.reasoning_effort ?? "high")
        : (lead?.reasoning ?? "high"),
    );
    setAgentLeadTier(
      configuringSeed || initialLead === "opencode"
        ? "default"
        : (lead?.service_tier ?? "default"),
    );
    setAgentLeadPermission(
      configuringSeed
        ? initialLead === "opencode"
          ? "provider_default"
          : agentPermissionFromUsageDefault()
        : initialLead === "opencode"
          ? "provider_default"
          : (lead?.permission ?? agentPermissionFromUsageDefault()),
    );
    setAgentLeadWebSearch(configuringSeed ? "live" : (lead?.web_search ?? "live"));
    setAgentNotifyFinished(metadata?.notify_finished ?? true);
    setAgentNotifyNeedsInput(metadata?.notify_needs_input ?? true);
    setAgentWorkers(
      (metadata?.workers ?? []).map((worker) => ({
        id: worker.id ?? crypto.randomUUID(),
        harness: worker.harness,
        model: worker.model ?? "",
        reasoning: worker.reasoning,
        tier: worker.service_tier,
        permission:
          worker.harness === "opencode"
            ? "provider_default"
            : worker.permission,
        maxTurns: String(worker.max_turns ?? 12),
        timeout: String(worker.timeout_seconds ?? 900),
      })),
    );
    setAgentPlugins((metadata?.plugin_ids ?? []).join(", "));
    setAgentMCPs(
      configuringSeed
        ? onboardingConnectorMCPs.join(", ")
        : (metadata?.mcp_ids ?? []).join(", "),
    );
    setAgentAdvancedOpen(false);
    setAgentToolsOpen(
      configuringSeed
        ? onboardingConnectorMCPs.length > 0
        : (metadata?.plugin_ids?.length ?? 0) > 0 ||
          (metadata?.mcp_ids?.length ?? 0) > 0,
    );
    setSelectedAgentTemplate(null);
    setAgentBuilderOpen(true);
  }

  async function createAgent() {
    const name = agentName.trim();
    const title = agentTitle.trim();
    if (!name || !title) return;
    const workers: Array<{
      worker: AgentExecutionDraft;
      maxTurns: number;
      timeoutSeconds: number;
    }> = [];
    if (agentAdvancedOpen) {
      for (const [index, worker] of agentWorkers.entries()) {
        if (!workerHarnessOption(worker.harness)?.supported) {
          setNotice(
            `Worker ${index + 1}: ${harnessLabel(worker.harness)} is not available as a delegated worker yet. Choose Grok Build or OpenCode.`,
          );
          return;
        }
        const bounds = workerExecutionBounds(worker, index);
        if (!bounds) return;
        workers.push({ worker, ...bounds });
      }
    }
    const metadata = agentAdvancedOpen
      ? {
          lead: {
            harness: agentLeadHarness,
            model: agentModel.trim(),
            reasoning: agentLeadReasoning,
            service_tier:
              agentLeadHarness === "opencode" ? "default" : agentLeadTier,
            permission:
              agentLeadHarness === "opencode"
                ? "provider_default"
                : agentLeadPermission,
            web_search: agentLeadWebSearch,
          },
          orchestrator: agentOrchestrator ? "lead" : "",
          workers: workers.map(({ worker, maxTurns, timeoutSeconds }) => ({
            id: worker.id,
            harness: worker.harness,
            model: worker.model.trim(),
            reasoning: worker.reasoning,
            service_tier: worker.tier,
            permission: worker.permission,
            max_turns: maxTurns,
            timeout_seconds: timeoutSeconds,
          })),
          plugin_ids: identifierList(agentPlugins),
          mcp_ids: identifierList(agentMCPs),
          notify_finished: agentNotifyFinished,
          notify_needs_input: agentNotifyNeedsInput,
        }
      : undefined;
    setAgentBuilderBusy(true);
    try {
      const response = await apiFetch(
        agentEditingID
          ? `/api/agents/${encodeURIComponent(agentEditingID)}`
          : "/api/agents",
        {
          method: agentEditingID ? "PATCH" : "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            name,
            title,
            description: agentDescription.trim(),
            ...(metadata ? { metadata } : {}),
          }),
        },
      );
      const payload = (await response.json()) as {
        conversation?: Conversation;
        default_conversation?: Conversation;
        error?: string;
      };
      if (!response.ok)
        throw new Error(payload.error ?? `botd returned ${response.status}`);
      const conversation = payload.default_conversation ?? payload.conversation;
      if (!agentEditingID) setOnboardingConnectorMCPs([]);
      setAgentBuilderOpen(false);
      setActivity([]);
      setLiveOutput("");
      setNotice(null);
      await load(conversation?.id ?? data?.conversation.id ?? "");
    } catch (agentError) {
      setNotice(
        agentError instanceof Error
          ? agentError.message
          : agentEditingID
            ? "Agent could not be updated"
            : "Agent could not be created",
      );
    } finally {
      setAgentBuilderBusy(false);
    }
  }

  async function selectAgent(botID: string) {
    if (botID === data?.conversation.bot_id) return;
    try {
      const conversation = data?.agents?.find(
        (agent) => agent.bot.id === botID,
      )?.conversation;
      if (!conversation) throw new Error("Agent has no canonical chat yet");
      setMemoryBotID(botID);
      await selectConversation(conversation.id);
    } catch (agentError) {
      setNotice(
        agentError instanceof Error
          ? agentError.message
          : "Agent could not be opened",
      );
    }
  }

  function openComposerModelPicker() {
    setComposerModelDraft(activeModel);
    setComposerModelOpen((current) => !current);
  }

  async function saveComposerModel() {
    const model = composerModelDraft.trim();
    setComposerModelBusy(true);
    setNotice(null);
    try {
      if (activeAgent && activeLead) {
        const response = await apiFetch(
          `/api/agents/${encodeURIComponent(activeAgent.bot.id)}`,
          {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ metadata: { lead: { model } } }),
          },
        );
        const payload = (await response.json().catch(() => ({}))) as {
          error?: string;
        };
        if (!response.ok)
          throw new Error(payload.error ?? `Agent returned ${response.status}`);
        setNotice("Model saved for this agent.");
      } else {
        const saved = await patchPreferences({ workspace: { model } });
        if (!saved) return;
        setNotice("Default model saved.");
      }
      setComposerModelOpen(false);
      await load(data?.conversation.id ?? "");
    } catch (modelError) {
      setNotice(
        modelError instanceof Error
          ? modelError.message
          : "Model could not be saved",
      );
    } finally {
      setComposerModelBusy(false);
    }
  }

  function agentForBot(botID: string) {
    return data?.agents?.find((agent) => agent.bot.id === botID);
  }

  async function handleBotMenuAction(
    action: "edit" | "memory" | "computer" | "copy",
    botID: string,
  ) {
    setBotMenuID(null);
    const agent = agentForBot(botID);
    if (!agent) {
      setNotice("Agent details are still loading. Try again in a moment.");
      return;
    }
    if (action === "edit") {
      openAgentEditor(agent);
      return;
    }
    if (action === "memory") {
      setMemoryBotID(botID);
      setSettingsOpen(true);
      return;
    }
    if (action === "copy") {
      try {
        await navigator.clipboard.writeText(botID);
        setNotice("Agent ID copied.");
      } catch {
        setNotice("Agent ID could not be copied.");
      }
      return;
    }
    try {
      if (botID !== data?.conversation.bot_id) await selectAgent(botID);
      await ensureComputer(true);
    } catch {
      setNotice("Agent Computer could not be opened.");
    }
  }

  async function selectConversation(conversationID: string) {
    if (conversationID === data?.conversation.id) return;
    setComposerModelOpen(false);
    setComposerModelDraft("");
    setActivity([]);
    setLiveOutput("");
    setNotice(null);
    selectedConversationIDRef.current = conversationID;
    setSelectedConversationID(conversationID);
    await load(conversationID);
  }

  async function launchNative(options: {
    session_id?: string;
    fork?: boolean;
    dashboard?: boolean;
    fullscreen?: boolean;
  }) {
    setNativeBusy(true);
    try {
      const response = await apiFetch("/api/grok/native", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(options),
      });
      const payload = (await response.json()) as {
        command?: string;
        error?: string;
      };
      if (!response.ok)
        throw new Error(payload.error ?? `botd returned ${response.status}`);
      setNotice(`Native Grok TUI launched: ${payload.command ?? "ready"}`);
    } catch (nativeError) {
      setNotice(
        nativeError instanceof Error
          ? nativeError.message
          : "Native Grok TUI could not be launched",
      );
    } finally {
      setNativeBusy(false);
    }
  }

  async function runSearch(event: FormEvent) {
    event.preventDefault();
    const query = searchQuery.trim();
    if (!query) return;
    setSearchBusy(true);
    try {
      const response = await apiFetch(
        `/api/search?q=${encodeURIComponent(query)}`,
      );
      const payload = (await response.json()) as {
        hits?: SearchHit[];
        error?: string;
      };
      if (!response.ok)
        throw new Error(payload.error ?? `botd returned ${response.status}`);
      setSearchHits(payload.hits ?? []);
    } catch (searchError) {
      setNotice(
        searchError instanceof Error ? searchError.message : "Search failed",
      );
    } finally {
      setSearchBusy(false);
    }
  }

  // Keep these hooks above the loading/error returns. The app renders once
  // before botd finishes bootstrapping, so conditional hook placement would
  // make React crash with minified error #310 on the first successful load.
  const focusComputerView: ComputerViewMode = computerViewMode;
  const focusFrameURL =
    focusComputerView === "desktop" ? desktopFrameURL : computerFrameURL;
  useEffect(() => {
    if (!computerViewOpen) {
      computerFrameFocusPendingRef.current = false;
      return;
    }
    computerFrameFocusPendingRef.current = true;
  }, [computerViewOpen, focusComputerView]);

  useEffect(() => {
    if (
      !computerViewOpen ||
      !focusFrameURL ||
      !computerFrameFocusPendingRef.current
    )
      return;
    const frame = window.requestAnimationFrame(() => {
      computerFrameRef.current?.focus({ preventScroll: true });
      computerFrameFocusPendingRef.current = false;
    });
    return () => window.cancelAnimationFrame(frame);
  }, [computerViewOpen, focusFrameURL]);

  if (loading)
    return (
      <div className="loading-screen">
        <span className="orb" />
        Starting OpenAgentFleet…
      </div>
    );
  if (error && !data)
    return (
      <div className="loading-screen error-screen">
        <span className="orb warning" />
        <div>
          <strong>OpenAgentFleet is offline</strong>
          <p>{error}</p>
          <button
            onClick={() => {
              setLoading(true);
              void load();
            }}
          >
            Reconnect
          </button>
        </div>
      </div>
    );
  if (!data || !bot)
    return (
      <div className="loading-screen error-screen" role="alert">
        <span className="orb warning" />
        <div>
          <strong>OpenAgentFleet could not load this workspace</strong>
          <p>{error ?? "The local runtime returned incomplete workspace data."}</p>
          <button
            onClick={() => {
              setLoading(true);
              void load();
            }}
          >
            Reconnect
          </button>
        </div>
      </div>
    );
  const activeComputerView: ComputerViewMode = computerViewMode;
  const computerReady =
    data.computer.running &&
    Boolean(data.computer.browser_ready || data.computer.desktop_ready);
  const skillLearningEnabled = preferences.features?.skill_learning ?? false;
  const computerState =
    data.computer.state ??
    (computerReady ? "ready" : data.computer.running ? "starting" : "stopped");
  const computerResources = {
    cpus: preferences.computer?.cpus ?? 4,
    ram_gib: preferences.computer?.ram_gib ?? 4,
    disk_gib: preferences.computer?.disk_gib ?? 25,
    swap_gib: preferences.computer?.swap_gib ?? 1,
    os_image: preferences.computer?.os_image ?? "ubuntu-24.04",
  };
  const computerResourcePresets = [
    { value: "light", label: "Light · 2 CPU · 2 GiB · 25 GiB", cpus: 2, ram_gib: 2, disk_gib: 25, swap_gib: 0 },
    { value: "standard", label: "Standard · 4 CPU · 4 GiB · 25 GiB", cpus: 4, ram_gib: 4, disk_gib: 25, swap_gib: 1 },
    { value: "roomy", label: "Roomy · 6 CPU · 8 GiB · 50 GiB", cpus: 6, ram_gib: 8, disk_gib: 50, swap_gib: 2 },
  ];
  const selectedComputerResourcePreset = computerResourcePresets.find(
    (preset) =>
      preset.cpus === computerResources.cpus &&
      preset.ram_gib === computerResources.ram_gib &&
      preset.disk_gib === computerResources.disk_gib &&
      preset.swap_gib === computerResources.swap_gib,
  )?.value ?? "custom";
  const activeFrameState: ComputerFrameState =
    computerFrameState.surface === activeComputerView
      ? computerFrameState
      : { surface: activeComputerView, status: "idle" };
  const activeFrameURL =
    activeComputerView === "desktop" ? desktopFrameURL : computerFrameURL;
  const activeFrameError = activeFrameState.status === "error";
  const activeFrameReady =
    Boolean(activeFrameURL) && activeFrameState.status === "ready";

  const workspace = (
    <aside className="work-panel" aria-label="Workspace controls">
      <div className="work-panel-header">
        <div>
          <div className="eyebrow">Workspace</div>
          <h2>Agent Computer</h2>
        </div>
        <span
          className={`computer-indicator ${computerState}`}
          title={data.computer.detail ?? `Computer ${computerState}`}
        />
      </div>
      <button
        className="computer-preview"
        type="button"
        onClick={() => void openComputerView()}
        aria-label={
          computerReady
            ? "Open live Agent Computer"
            : "Start Agent Computer"
        }
      >
        <div className="preview-topbar">
          <span className="traffic red" />
          <span className="traffic yellow" />
          <span className="traffic green" />
          <span className="preview-url">
            {data.computer.url || "agent://workspace"}
          </span>
        </div>
        {activeFrameURL ? (
          <img
            className="preview-frame"
            src={activeFrameURL}
            alt={`Live Agent Computer ${activeComputerView} view`}
          />
        ) : (
          <span className="preview-content">
            <span className="preview-star">✦</span>
            <strong>
              {computerState === "ready" && activeFrameState.status === "loading"
                ? "Loading current screen"
                : computerState === "ready"
                ? "Computer ready"
                : computerState === "starting"
                  ? "Starting Chromium"
                  : computerState === "error"
                    ? "Computer needs attention"
                    : computerState === "unavailable"
                      ? "Computer runtime unavailable"
                      : "Computer stopped"}
            </strong>
            <span>
              {computerState === "ready" && activeFrameState.error
                ? activeFrameState.error
                : computerState === "ready"
                ? "Chromium, terminal and files live in an isolated workspace."
                : data.computer.detail ??
                  (computerState === "starting"
                    ? "The isolated desktop is warming up. Chromium readiness will appear here."
                    : "Start it for browser or desktop work.")}
            </span>
          </span>
        )}
      </button>
      <button
        className="computer-button"
        onClick={() =>
          computerReady ? openComputerView() : void ensureComputer()
        }
        disabled={computerBusy || (!data.computer.available && !data.computer.can_retry)}
      >
        {computerBusy
          ? "Starting…"
          : computerReady
            ? "Open live computer"
            : data.computer.running
              ? "Restart Agent Computer"
            : "Start Agent Computer"}
      </button>
      {data.computer.running && (
        <button
          type="button"
          className="computer-stop-button"
          onClick={() => void stopComputer()}
          disabled={computerBusy}
        >
          {computerBusy ? "Stopping…" : "Stop Agent Computer"}
        </button>
      )}
      <div className="workspace-action-row">
        <button
          type="button"
          className="quiet-button"
          onClick={() => {
            void openComputerView();
            setTeachGoalOpen(true);
          }}
          disabled={!computerReady || !skillLearningEnabled}
          title={
            skillLearningEnabled
              ? "Record a safe workflow for review"
              : "Enable Skill learning in Settings first"
          }
        >
          Teach a task
        </button>
        <button
          type="button"
          className="quiet-button"
          onClick={() => setSettingsOpen(true)}
        >
          Settings
        </button>
      </div>
      {teach.state === "recording" || teach.state === "paused" ? (
        <section className="teach-status" aria-live="polite">
          <div>
            <span className="record-dot" />
            <strong>
              {teach.state === "paused" ? "Secret pause" : "Teaching"}
            </strong>
            <time>{teachTimer}</time>
          </div>
          <p>
            {teach.goal || "Recording a safe workflow"} ·{" "}
            {teach.step_count ?? 0} steps
          </p>
          <div>
            {teach.state === "recording" ? (
              <button onClick={() => void teachAction("pause")}>
                Pause for secret
              </button>
            ) : (
              <button onClick={() => void teachAction("resume")}>Resume</button>
            )}
            <button
              onClick={() => void teachAction("stop")}
              disabled={teachBusy}
            >
              Stop
            </button>
            <button
              onClick={() => void teachAction("discard")}
              disabled={teachBusy}
            >
              Discard
            </button>
          </div>
        </section>
      ) : null}
      <details className="advanced-context workspace-tools-context">
        <summary>
          <span>Tools & skills</span>
          <span>
            {(integrations?.length ?? 0) + (data.skills?.length ?? 0)} available
          </span>
        </summary>
        <div className="advanced-context-body">
          <section className="work-section">
            <div className="section-label">
              Plugins & integrations <span>{integrations?.length ?? "—"}</span>
            </div>
            {integrations === null ? (
              <p className="activity-empty">
                {integrationsError ?? "Checking local read-only inventory…"}
              </p>
            ) : integrations.length === 0 ? (
              <p className="activity-empty">
                No local MCP servers or plugins found.
              </p>
            ) : (
              integrations.slice(0, 8).map((integration, index) => (
                <div
                  className="integration-row"
                  key={`${integration.source ?? integration.name}-${index}`}
                >
                  <span
                    className={`harness-dot ${integration.status === "available" ? "available" : ""}`}
                  />
                  <div>
                    <strong>{integration.name || "Unnamed integration"}</strong>
                    <span>
                      {integration.host || "local"} ·{" "}
                      {integration.kind || "integration"}
                    </span>
                  </div>
                  <em title={integration.detail}>
                    {integration.status === "available" ? "ready" : "unavailable"}
                  </em>
                </div>
              ))
            )}
          </section>
          <section className="work-section">
            <div className="section-label">
              Skills <span>{data.skills?.length ?? 0}</span>
            </div>
            {(data.skills ?? []).length === 0 ? (
              <p className="activity-empty">
                No reviewed skills are available yet.
              </p>
            ) : (
              data.skills!.slice(0, 5).map((skill) => (
                <div className="skill-row" key={skill.id}>
                  <span
                    className={`skill-dot ${skill.eligible ? "eligible" : ""}`}
                  />
                  <div>
                    <strong>{skill.name}</strong>
                    <span>{skill.description ?? skill.source}</span>
                  </div>
                  <em>{skill.eligible ? "ready" : "review"}</em>
                </div>
              ))
            )}
          </section>
        </div>
      </details>
      <details className="advanced-context">
        <summary>
          <span>Advanced execution</span>
          <span>{availableHarnesses.length} ready</span>
        </summary>
        <div className="advanced-context-body">
          <div className="work-section">
            {data.capabilities
              .filter((capability) => capability.name !== "docker")
              .map((capability) => (
                <div className="harness-row" key={capability.name}>
                  <span
                    className={`harness-dot ${capability.available ? "available" : ""}`}
                  />
                  <div>
                    <strong>{harnessLabel(capability.name)}</strong>
                    <span>{capability.version ?? "not installed"}</span>
                  </div>
                  <em>{capability.available ? "ready" : "missing"}</em>
                </div>
              ))}
          </div>
          {(data.auth ?? []).map((auth) => (
            <div className="oauth-card" key={auth.provider}>
              <div className="oauth-heading">
                <strong>{harnessLabel(auth.provider)}</strong>
                <span>
                  {auth.authenticated
                    ? "connected"
                    : auth.pending
                      ? "waiting for sign-in"
                      : "login required"}
                </span>
              </div>
              {(auth.provider === "grok" ||
                auth.provider === "codex_app_server") && (
                <div className="oauth-actions">
                  <button
                    onClick={() =>
                      void startOAuth(
                        auth.provider as "grok" | "codex_app_server",
                        "browser",
                      )
                    }
                    disabled={!auth.available || oauthBusy !== null}
                  >
                    {oauthBusy === `${auth.provider}:browser`
                      ? "Opening…"
                      : "OAuth"}
                  </button>
                  <button
                    onClick={() =>
                      void startOAuth(
                        auth.provider as "grok" | "codex_app_server",
                        "device",
                      )
                    }
                    disabled={!auth.available || oauthBusy !== null}
                  >
                    Device code
                  </button>
                </div>
              )}
            </div>
          ))}
          <div className="work-section">
            <div className="section-label">Grok Build tools</div>
            <div className="grok-controls">
              {(
                [
                  "inspect",
                  "models",
                  "plugins",
                  "mcp",
                  "sessions",
                ] as GrokInfoKind[]
              ).map((kind) => (
                <button
                  key={kind}
                  className={grokInfoKind === kind ? "selected" : ""}
                  onClick={() => void loadGrokInfo(kind)}
                >
                  {kind}
                </button>
              ))}
            </div>
            <div className="native-controls">
              <button
                onClick={() => void launchNative({ fullscreen: true })}
                disabled={nativeBusy}
              >
                {nativeBusy ? "Launching…" : "Open TUI"}
              </button>
            </div>
            {grokInfoKind && (
              <pre className="grok-info-output">
                {grokInfoBusy ? "Loading…" : grokInfoOutput}
              </pre>
            )}
          </div>
          <div className="work-section activity-section">
            <div className="section-label">
              Activity <span>{activity.length}</span>
            </div>
            {activity.length === 0 ? (
              <p className="activity-empty">Waiting for run events…</p>
            ) : (
              activity.slice(0, 4).map((event) => (
                <button
                  className="activity-row"
                  type="button"
                  key={event.id}
                  onClick={() =>
                    setExpandedActivity((current) =>
                      current === event.id ? null : event.id,
                    )
                  }
                  aria-expanded={expandedActivity === event.id}
                >
                  <span className="activity-dot" />
                  <div>
                    <strong>{event.type}</strong>
                    <span>{formatTime(event.created_at)}</span>
                  </div>
                </button>
              ))
            )}
          </div>
        </div>
      </details>
    </aside>
  );

  return (
    <main className="app-shell">
      {notice && (
        <div className="global-notice" role="status" aria-live="polite">
          {notice}
        </div>
      )}
      <aside className="sidebar" aria-label="Agents">
        <div className="brand-lockup">
          <div className="brand-mark">✦</div>
          <div>
            <strong>OpenAgentFleet</strong>
            <span>private agent workspace</span>
          </div>
        </div>
        <button
          className="new-button"
          onClick={() => {
            setDraft("");
            openAgentBuilder();
          }}
        >
          <span>＋</span> New agent <kbd>⌘ N</kbd>
        </button>
        <div className="sidebar-section">
          <div className="section-label">
            Agents <span>{data.bots.length}</span>
          </div>
          {data.bots.map((item) => (
            <div className="bot-row-wrap" key={item.id}>
              <button
                className={`bot-row ${item.id === data.conversation.bot_id ? "active" : ""}`}
                onClick={() => void selectAgent(item.id)}
                onContextMenu={(event) => {
                  event.preventDefault();
                  setBotMenuID(item.id);
                }}
              >
                <div className="avatar">{item.name.slice(0, 1).toUpperCase()}</div>
                <div className="bot-copy">
                  <strong>{item.name}</strong>
                  <span>{item.title || "Personal agent"}</span>
                </div>
                <span className="presence" />
              </button>
              <button
                type="button"
                className="bot-menu-trigger"
                aria-label={`Actions for ${item.name}`}
                aria-expanded={botMenuID === item.id}
                onClick={(event) => {
                  event.stopPropagation();
                  setBotMenuID((current) => (current === item.id ? null : item.id));
                }}
              >
                ···
              </button>
              {botMenuID === item.id && (
                <div
                  className="bot-context-menu"
                  role="menu"
                  onPointerDown={(event) => event.stopPropagation()}
                >
                  <div className="bot-context-heading">{item.name}</div>
                  <button
                    type="button"
                    role="menuitem"
                    onClick={() => void handleBotMenuAction("edit", item.id)}
                  >
                    Edit agent
                  </button>
                  <button
                    type="button"
                    role="menuitem"
                    onClick={() => void handleBotMenuAction("memory", item.id)}
                  >
                    Open memory
                  </button>
                  <button
                    type="button"
                    role="menuitem"
                    onClick={() => void handleBotMenuAction("computer", item.id)}
                  >
                    Open Computer
                  </button>
                  <button
                    type="button"
                    role="menuitem"
                    onClick={() => void handleBotMenuAction("copy", item.id)}
                  >
                    Copy agent ID
                  </button>
                </div>
              )}
            </div>
          ))}
        </div>
        {preferences.features?.multiple_conversations && (
          <div className="sidebar-section conversation-list">
            <div className="section-label">
              Chats <span>{data.conversations?.length ?? 0}</span>
            </div>
            <button
              className="conversation-row new-chat-row"
              onClick={() => void createConversation()}
            >
              <span>＋</span>
              <span>New chat</span>
            </button>
            {(data.conversations ?? []).slice(0, 10).map((conversation) => (
              <button
                className={`conversation-row ${conversation.id === data.conversation.id ? "active" : ""}`}
                key={conversation.id}
                onClick={() => void selectConversation(conversation.id)}
              >
                <span className="conversation-dot" />
                <span>{conversation.title}</span>
              </button>
            ))}
          </div>
        )}
        <div className="sidebar-section muted-section">
          <button
            className="nav-row"
            onClick={() => {
              setSearchOpen((current) => !current);
            }}
          >
            <span>⌕</span> Search <kbd>⌘ K</kbd>
          </button>
          <button className="nav-row" onClick={() => setSettingsOpen(true)}>
            <span>⚙</span> Settings
          </button>
        </div>
        {searchOpen && (
          <form className="search-box" onSubmit={runSearch}>
            <input
              value={searchQuery}
              onChange={(event) => setSearchQuery(event.target.value)}
              placeholder="Search OpenAgentFleet…"
              autoFocus
            />
            <button type="submit" aria-label="Run search">
              {searchBusy ? "…" : "↗"}
            </button>
            {searchHits.length > 0 && (
              <div className="search-results">
                {searchHits.slice(0, 6).map((hit) => (
                  <button
                    type="button"
                    key={`${hit.kind}-${hit.id}`}
                    onClick={() => {
                      if (hit.conversation_id)
                        void selectConversation(hit.conversation_id);
                      setSearchOpen(false);
                    }}
                  >
                    <strong>{hit.title || hit.kind}</strong>
                    <span>{hit.snippet}</span>
                  </button>
                ))}
              </div>
            )}
          </form>
        )}
        <div className="sidebar-footer">
          <div className="local-status">
            <span className="status-dot" />
            Local runtime <span className="runtime-chip">Online</span>
          </div>
          <div className="version-label">
            {availableHarnesses.length} runtimes detected
          </div>
        </div>
      </aside>
      <section className="conversation-panel">
        <header className="conversation-header">
          <div className="conversation-title">
            <div className="avatar large">O</div>
            <div>
              <div className="eyebrow">
                Private workspace <span className="live-chip">Live</span>
              </div>
              <h1>{bot.name}</h1>
              <span>{bot.description}</span>
            </div>
          </div>
          <div className="header-actions">
            <button
              className="icon-button mobile-only"
              aria-label="Open workspace"
              aria-expanded={workspaceOpen}
              onClick={() => setWorkspaceOpen(true)}
            >
              ▦
            </button>
            <button
              className="icon-button"
              aria-label="Open settings"
              onClick={() => setSettingsOpen(true)}
            >
              ⚙
            </button>
          </div>
        </header>
        <div className="message-scroll" ref={messageScrollRef}>
          <div className="date-divider">
            <span>Today</span>
          </div>
          {pendingApprovals.map((approval) => {
            const options = approvalOptions(approval);
            return (
              <section className="inline-approval-card" key={approval.id} aria-live="assertive">
                <div className="approval-title">
                  <span className="attention-icon" aria-hidden="true">!</span>
                  <strong>OpenAgentFleet needs your approval</strong>
                </div>
                <p>
                  <strong>{approval.action}</strong> · {approvalSummary(approval)}
                </p>
                <div className="inline-approval-options">
                  {options.length > 0 ? (
                    options.map((option, index) => (
                      <button
                        type="button"
                        className={index === 0 ? "approve-button" : ""}
                        key={option.optionId}
                        onClick={() =>
                          void resolveApproval(approval, "approved", option.optionId)
                        }
                      >
                        {option.name || "Allow once"}
                      </button>
                    ))
                  ) : (
                    <span className="approval-no-options">
                      No safe approval choice was supplied by this harness.
                      Deny the request or inspect the advanced run details.
                    </span>
                  )}
                </div>
                <div className="inline-approval-footer">
                  <span>{harnessLabel(approval.provider)} · run waiting</span>
                  <button type="button" onClick={() => void resolveApproval(approval, "denied")}>
                    Deny
                  </button>
                </div>
              </section>
            );
          })}
          {messages.length === 0 && (
            <div className="empty-state">
              <div className="empty-orbit">✦</div>
              <h2>Give OpenAgentFleet a job.</h2>
              <p>
                Research, build, organize, or automate something that would
                otherwise wait for you.
              </p>
              <div className="suggestion-grid">
                <button
                  onClick={() =>
                    setDraft(
                      "Inspect the local runtime and summarize what is ready.",
                    )
                  }
                >
                  Inspect local runtime <span>↗</span>
                </button>
                <button
                  onClick={() => setDraft("Create a safe browser workflow.")}
                >
                  Design a browser workflow <span>↗</span>
                </button>
              </div>
            </div>
          )}
          {messages.map((message) => (
            <article className={`message ${message.role}`} key={message.id}>
              <div className="message-meta">
                <span>{message.role === "user" ? "You" : bot.name}</span>
                <time>{formatTime(message.created_at)}</time>
              </div>
              {message.content && (
                <div className="message-bubble">{message.content}</div>
              )}
              {(attachmentsByMessage[message.id] ?? []).map((attachment) => (
                <button
                  className="message-attachment"
                  type="button"
                  key={attachment.id}
                  onClick={() => void openAttachment(attachment)}
                  aria-label={`Open ${attachment.name}`}
                >
                  <span className="attachment-icon">
                    {attachmentIcon(attachment.media_type)}
                  </span>
                  <span className="attachment-copy">
                    <strong>{attachment.name}</strong>
                    <span>
                      {formatFileSize(attachment.size)} ·{" "}
                      {attachment.media_type}
                    </span>
                  </span>
                  <span>↗</span>
                </button>
              ))}
            </article>
          ))}
          {(data.transcript_blocks ?? [])
            .filter((block) => block.status !== "pending")
            .map((block) => (
              <section className="transcript-block-card" key={block.id}>
                <div className="transcript-block-heading">
                  <span className={`transcript-block-status ${block.status}`}>
                    {block.status === "approved"
                      ? "Allowed"
                      : block.status === "denied"
                        ? "Denied"
                        : "Cancelled"}
                  </span>
                  <strong>{block.action}</strong>
                </div>
                <p>
                  {block.status === "approved"
                    ? block.selected_option_id
                      ? `Selected ${block.selected_option_id}.`
                      : "The requested action was allowed."
                    : "The requested action did not continue."}
                </p>
                {(block.options ?? []).length > 0 && (
                  <div className="transcript-block-options">
                    {(block.options ?? []).map((option) => (
                      <span
                        className={
                          option.optionId === block.selected_option_id
                            ? "selected"
                            : ""
                        }
                        key={option.optionId}
                      >
                        {option.name}
                      </span>
                    ))}
                  </div>
                )}
                <small>
                  {harnessLabel(block.provider)} · decision recorded
                </small>
              </section>
            ))}
          {lastRun && (
            <div className="run-card">
              <div className="run-icon">✦</div>
              <div className="run-content">
                <div className="run-heading">
                  <strong>{harnessLabel(lastRun.provider)} run</strong>
                  <span className="run-state">{lastRun.status}</span>
                </div>
                <p>
                  {lastRun.error ??
                    (lastRun.status === "completed"
                      ? "Result saved to this agent."
                      : liveOutput || "Working in the Agent Computer…")}
                </p>
                {(lastRun.status === "running" ||
                  lastRun.status === "waiting_for_approval") && (
                  <button
                    className="stop-run-button"
                    onClick={() => void stopRun(lastRun)}
                  >
                    Stop run
                  </button>
                )}
              </div>
            </div>
          )}
        </div>
        <div className="composer-wrap">
          <form
            className={`composer${composerDraggingFiles ? " is-dragging-files" : ""}`}
            onSubmit={submit}
            aria-label="Message composer"
            onDragEnter={handleComposerDragEnter}
            onDragOver={handleComposerDragOver}
            onDragLeave={handleComposerDragLeave}
            onDrop={handleComposerDrop}
          >
            <input
              ref={fileInputRef}
              className="attachment-input"
              type="file"
              multiple
              onChange={(event) => {
                const files = event.currentTarget.files
                  ? Array.from(event.currentTarget.files)
                  : [];
                event.currentTarget.value = "";
                void uploadAttachments(files);
              }}
            />
            {composerDraggingFiles && (
              <div
                className="composer-drop-status"
                role="status"
                aria-live="polite"
              >
                {uploadingAttachments > 0
                  ? "Attachments are uploading — drop when they finish"
                  : "Drop files to attach them"}
              </div>
            )}
            {uploadingAttachments > 0 && !composerDraggingFiles && (
              <div className="composer-drop-status" role="status" aria-live="polite">
                Uploading {uploadingAttachments} attachment
                {uploadingAttachments === 1 ? "" : "s"}…
              </div>
            )}
            {pendingAttachments.length > 0 && (
              <div
                className="composer-attachments"
                aria-label="Attachments ready to send"
              >
                {pendingAttachments.map((attachment) => (
                  <div className="composer-attachment" key={attachment.id}>
                    <span className="attachment-icon">
                      {attachmentIcon(attachment.media_type)}
                    </span>
                    <span className="attachment-copy">
                      <strong>{attachment.name}</strong>
                      <span>{formatFileSize(attachment.size)}</span>
                    </span>
                    <button
                      type="button"
                      aria-label={`Remove ${attachment.name}`}
                      disabled={removingAttachmentIDs.includes(attachment.id)}
                      onClick={() => void removePendingAttachment(attachment)}
                    >
                      ×
                    </button>
                  </div>
                ))}
              </div>
            )}
            <textarea
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              onKeyDown={(event) => {
                if (
                  !event.nativeEvent.isComposing &&
                  event.key === "Enter" &&
                  !event.shiftKey
                ) {
                  event.preventDefault();
                  void submit(event);
                }
              }}
              placeholder={`Message ${bot.name}…`}
              aria-label={`Message ${bot.name}`}
              rows={1}
            />
            <div className="composer-toolbar">
              <div className="composer-tools">
                <button
                  type="button"
                  aria-label="Attach files"
                  onClick={() => fileInputRef.current?.click()}
                  disabled={uploadingAttachments > 0}
                >
                  ＋
                </button>
                <button
                  className={`composer-mic ${recording ? "recording" : ""}`}
                  type="button"
                    title={
                      nativeDictationAvailable
                        ? "On-device Mac dictation"
                        : speechToText?.available
                          ? speechToText.detail ??
                            "Voice uses the configured remote STT fallback"
                          : browserSpeechAvailable
                            ? "Browser speech recognition; audio is handled by the browser provider"
                          : "Voice input is unavailable here. Configure speech-to-text in Settings or use the Mac app."
                    }
                  aria-label={
                    recording
                      ? "Stop recording"
                      : voiceInputUnavailable
                        ? "Voice input unavailable"
                        : "Record a voice message"
                  }
                  aria-pressed={recording}
                  onClick={() =>
                    recording ? stopRecording() : void startRecording()
                  }
                  disabled={transcribing || voiceInputUnavailable}
                >
                  {transcribing ? "…" : recording ? "■" : "●"}
                </button>
                <div className="composer-model-control">
                  <button
                    type="button"
                    className="provider-pill agent-lead-pill"
                    title="Choose the lead model"
                    aria-label="Choose the lead model"
                    aria-expanded={composerModelOpen}
                    onClick={openComposerModelPicker}
                  >
                    <span className="provider-label">Lead</span>
                    <strong>{harnessLabel(activeEngine)}</strong>
                    <span>{modelChoiceLabel(activeEngine, activeModel, data?.model_catalog ?? [])}</span>
                    <span className="provider-pill-chevron" aria-hidden="true">
                      {composerModelOpen ? "⌃" : "⌄"}
                    </span>
                  </button>
                  {composerModelOpen && (
                    <div
                      className="composer-model-popover"
                      onPointerDown={(event) => event.stopPropagation()}
                    >
                      <div className="composer-model-popover-heading">
                        <div>
                          <strong>Lead model</strong>
                          <small>{harnessLabel(activeEngine)} for {bot.name}</small>
                        </div>
                        <button
                          type="button"
                          className="text-button"
                          onClick={() => {
                            setComposerModelOpen(false);
                            if (activeAgent) openAgentEditor(activeAgent);
                          }}
                        >
                          Settings
                        </button>
                      </div>
                      <ModelPicker
                        key={`composer-${activeEngine}`}
                        harness={activeEngine}
                        value={composerModelDraft}
                        onChange={setComposerModelDraft}
                        catalog={data?.model_catalog ?? []}
                      />
                      <div className="composer-model-popover-actions">
                        <button
                          type="button"
                          className="secondary-button"
                          onClick={() => setComposerModelOpen(false)}
                          disabled={composerModelBusy}
                        >
                          Cancel
                        </button>
                        <button
                          type="button"
                          className="primary-button"
                          onClick={() => void saveComposerModel()}
                          disabled={composerModelBusy}
                        >
                          {composerModelBusy ? "Saving…" : "Use model"}
                        </button>
                      </div>
                    </div>
                  )}
                </div>
              </div>
              <div className="composer-actions">
                <span>
                  {recording
                    ? "Recording…"
                    : transcribing
                      ? "Transcribing…"
                      : "Shift ↵ for newline"}
                </span>
                <button
                  className="send-button"
                  type="submit"
                  aria-label="Send message"
                  disabled={
                    (!draft.trim() && pendingAttachments.length === 0) ||
                    sending ||
                    uploadingAttachments > 0
                  }
                >
                  {sending ? "…" : "↑"}
                </button>
              </div>
            </div>
          </form>
          <p className="composer-note">
            Review external actions before they happen.{" "}
            {NATIVE_RUNTIME_AVAILABLE
              ? "Voice uses on-device Mac dictation when available; remote transcription is only the fallback."
              : browserSpeechAvailable
                ? "Voice uses browser speech recognition when available; otherwise audio goes only to the configured transcription service and is not stored here."
                : "Voice audio is sent to the configured transcription service only and is not stored here."}
          </p>
        </div>
      </section>
      {workspace}
      {onboardingOpen && data && (
        <div
          className="dialog-backdrop onboarding-backdrop"
          role="dialog"
          aria-modal="true"
          aria-labelledby="onboarding-title"
          aria-describedby={onboardingSaveError ? "onboarding-save-error" : undefined}
        >
          <section className="onboarding-dialog">
            <header className="onboarding-header">
              <div>
                <div className="eyebrow">OpenAgentFleet setup</div>
                <span className="onboarding-progress">
                  {onboardingStep + 1} / {ONBOARDING_STEP_COUNT}
                </span>
              </div>
              <button
                type="button"
                className="onboarding-skip"
                disabled={onboardingBusy || searchConnectorBusy !== null}
                onClick={() => void skipOnboarding()}
              >
                Skip setup
              </button>
            </header>

            {onboardingStep === 0 && (
              <div className="onboarding-page">
                <div className="eyebrow">Your lead harnesses</div>
                <h2 id="onboarding-title">Choose who runs your agents.</h2>
                <p>
                  OpenAgentFleet uses the AI tools already available on this
                  {isLinuxHost(data) ? " computer" : " Mac"}.
                  Pick one workspace lead; you can change it later in Settings.
                </p>
                <div
                  className="onboarding-engines"
                  role="radiogroup"
                  aria-label="Workspace lead harness"
                >
                  {onboardingEngines.map((engine) => {
                    const selected = onboardingLead === engine.value;
                    const auth = engine.auth;
                    const status = !engine.available
                      ? "Not installed"
                      : engine.value === "opencode"
                        ? "Available locally"
                        : !auth
                          ? "Checking sign-in…"
                        : auth?.authenticated
                          ? "Connected"
                          : auth?.pending
                            ? "Connecting…"
                            : auth?.login_required
                              ? "Sign in required"
                              : "Available";
                    return (
                      <article
                        key={engine.value}
                        className={`onboarding-engine ${selected ? "selected" : ""} ${!engine.available ? "unavailable" : ""}`}
                      >
                        <button
                          type="button"
                          role="radio"
                          aria-checked={selected}
                          disabled={!engine.available}
                          onClick={() => {
                            setOnboardingLead(engine.value);
                            setOnboardingModel(
                              defaultLeadModel(engine.value, data?.model_catalog ?? []),
                            );
                            setOnboardingReasoning("high");
                            if (engine.value === "opencode") {
                              setAgentLeadTier("default");
                              setAgentLeadPermission("provider_default");
                            } else {
                              setAgentLeadPermission(agentPermissionFromUsageDefault());
                            }
                          }}
                        >
                          <span
                            className={`onboarding-radio${selected ? " selected" : ""}`}
                            aria-hidden="true"
                          >
                            <span>{selected ? "✓" : ""}</span>
                          </span>
                          <span>
                            <strong>{engine.label}</strong>
                            <small>{engine.description}</small>
                          </span>
                          <em>{status}</em>
                        </button>
                        {engine.authProvider &&
                          auth?.login_required &&
                          !auth.authenticated &&
                          auth.available && (
                            <button
                              type="button"
                              className="onboarding-oauth"
                              disabled={oauthBusy !== null || auth.pending}
                              onClick={() =>
                                void startOAuth(engine.authProvider!, "browser")
                              }
                            >
                              {oauthBusy === `${engine.authProvider}:browser`
                                ? "Opening…"
                                : "Sign in"}
                            </button>
                          )}
                        {engine.authProvider &&
                          auth?.available &&
                          !auth.authenticated &&
                          !auth.login_required && (
                            <button
                              type="button"
                              className="onboarding-oauth"
                              disabled={
                                authRefreshBusy !== null || oauthBusy !== null
                              }
                              onClick={() =>
                                void refreshHarnessAuth(engine.authProvider!)
                              }
                              aria-label={`Refresh ${engine.label} status`}
                            >
                              {authRefreshBusy === engine.authProvider
                                ? "Checking…"
                                : "Retry"}
                            </button>
                          )}
                        {engine.authProvider && !auth && engine.available && (
                          <button
                            type="button"
                            className="onboarding-oauth"
                            disabled={authRefreshBusy !== null || oauthBusy !== null}
                            onClick={() => void refreshHarnessAuth(engine.authProvider!)}
                          >
                            {authRefreshBusy === engine.authProvider
                              ? "Checking…"
                              : "Check sign-in"}
                          </button>
                        )}
                      </article>
                    );
                  })}
                </div>
                <div className="onboarding-facts onboarding-engine-facts">
                  <div><strong>Local control</strong><span>Agents, memory and approvals stay on this {isLinuxHost(data) ? "computer" : "Mac"}.</span></div>
                  <div><strong>One lead harness</strong><span>All agents use this harness and model unless you add an advanced override.</span></div>
                  <div><strong>Open choices</strong><span>Optional tools stay off until you turn them on.</span></div>
                </div>
                <section className="onboarding-model-config" aria-labelledby="onboarding-model-title">
                  <div className="onboarding-model-config-heading">
                    <div>
                      <div className="eyebrow">AI choice inside the lead</div>
                      <h3 id="onboarding-model-title">Choose the model that does the work.</h3>
                    </div>
                    <span className="execution-badge">Optional</span>
                  </div>
                  <p className="field-note">
                    The harness is the execution system; the model is the AI. Start with the recommended model and change it later per workspace or agent.
                  </p>
                  <ModelPicker
                    key={`onboarding-${onboardingLead}`}
                    harness={onboardingLead}
                    value={onboardingModel}
                    onChange={setOnboardingModel}
                    catalog={data?.model_catalog ?? []}
                  />
                  <label className="onboarding-reasoning-field">
                    Reasoning depth
                    <select
                      value={onboardingReasoning}
                      onChange={(event) => setOnboardingReasoning(event.target.value)}
                    >
                      <option value="low">Low · quicker responses</option>
                      <option value="medium">Medium · balanced</option>
                      <option value="high">High · deeper work</option>
                      {onboardingLead === "codex_app_server" && (
                        <>
                          <option value="xhigh">XHigh · very deep work</option>
                          <option value="max">Max · maximum available</option>
                        </>
                      )}
                    </select>
                  </label>
                </section>
              </div>
            )}

            {onboardingStep === 1 && (
              <div className="onboarding-page">
                <div className="eyebrow">Optional permissions</div>
                <h2 id="onboarding-title">Give agents access when you need it.</h2>
                <p>
                  OpenAgentFleet asks for local access only when you use the
                  feature. Nothing here is required to start chatting.
                </p>
                <div className="onboarding-permissions">
                  <article className="onboarding-permission-card">
                    <span className="onboarding-permission-icon" aria-hidden="true">◉</span>
                    <div>
                      <strong>Microphone & speech</strong>
                      <small>
                        On Mac, dictation stays on-device when Apple supports the
                        current locale. Browser clients use browser speech
                        recognition when available, or the configured fallback.
                      </small>
                    </div>
                    <em>
                      {nativeDictationAvailable || speechToText?.available
                        ? "Ready"
                        : "Optional"}
                    </em>
                  </article>
                  <article className="onboarding-permission-card">
                    <span className="onboarding-permission-icon" aria-hidden="true">▣</span>
                    <div>
                      <strong>Agent Computer</strong>
                      <small>Chromium, Terminal and Files start lazily. Enable Agent control when an agent should operate them.</small>
                    </div>
                    <em>Set up later</em>
                  </article>
                  <article className="onboarding-permission-card">
                    <span className="onboarding-permission-icon" aria-hidden="true">✓</span>
                    <div>
                      <strong>Local control plane</strong>
                      <small>Chats, memory, attachments and approvals are stored by your local OpenAgentFleet service.</small>
                    </div>
                    <em>Always on</em>
                  </article>
                </div>
                <p className="onboarding-note">
                  Agent Computer runtime is configurable later in Settings. It
                  is not installed or started just because you finish setup;
                  opening its view and enabling Agent control are separate choices.
                </p>
              </div>
            )}

            {onboardingStep === 2 && (
              <div className="onboarding-page">
                <div className="eyebrow">Optional web search</div>
                <h2 id="onboarding-title">Add search when it helps.</h2>
                <p>
                  {onboardingLead === "opencode"
                    ? "OpenCode uses its configured model tools. Web Search Plus, keyless Hound, and keyless Donsetch are independent optional connectors."
                    : "The selected lead harness can provide built-in live search. Web Search Plus, keyless Hound, and keyless Donsetch add independent optional routes."}{" "}
                  You can change these choices later in Settings.
                </p>
                <div className="onboarding-search-connectors">
                  <SearchConnectorCards
                    availability={searchConnectorAvailability}
                    connectors={searchConnectors}
                    busy={searchConnectorBusy}
                    error={searchConnectorsError}
                    nativeSearchMode={
                      onboardingLead === "opencode"
                        ? "opencode"
                        : "connected_harness"
                    }
                    onToggle={(connector, enabled) =>
                      void patchSearchConnector(connector, enabled, "onboarding")
                    }
                  />
                </div>
              </div>
            )}

            {onboardingStep === 3 && (
              <div className="onboarding-page onboarding-ready-page">
                <div className="eyebrow">Ready</div>
                <h2 id="onboarding-title">Start using OpenAgentFleet.</h2>
                <p>
                  Your default chat is ready. You can create more agents from
                  New agent whenever you want; each agent keeps its own role and
                  memory while using this workspace lead harness.
                </p>
                <div className="onboarding-summary">
                  <span>Lead harness</span>
                  <strong>{harnessLabel(onboardingLead)}</strong>
                  <span>AI model</span>
                  <strong>{modelChoiceLabel(onboardingLead, onboardingModel, data?.model_catalog ?? [])}</strong>
                  <span>Reasoning depth</span>
                  <strong>{onboardingReasoning}</strong>
                  <span>Agent Computer</span>
                  <strong>Lazy — configure when first used</strong>
                  <span>Optional search</span>
                  <strong>
                    {searchConnectorAvailability === "available" && searchConnectors
                      ? [
                          searchConnectors.web_search_plus_enabled
                            ? "Web Search Plus"
                            : "",
                          searchConnectors.hound_enabled ? "Hound" : "",
                          searchConnectors.donsetch_enabled ? "Donsetch" : "",
                        ].filter(Boolean).join(" + ") || "None enabled"
                      : "Status unavailable"}
                  </strong>
                  <small>All choices can be changed later in Settings.</small>
                </div>
              </div>
            )}

            {onboardingSaveError && (
              <div
                id="onboarding-save-error"
                className="onboarding-save-error"
                role="alert"
                aria-live="assertive"
              >
                <strong>Setup could not be saved.</strong>
                <span>
                  {onboardingSaveError} Your selections are still here; try
                  Configure your agent again.
                </span>
              </div>
            )}

            <footer className="onboarding-actions">
              <button
                type="button"
                disabled={
                  onboardingStep === 0 ||
                  onboardingBusy || searchConnectorBusy !== null
                }
                onClick={() => {
                  setOnboardingSaveError(null);
                  setOnboardingStep((step) => Math.max(0, step - 1));
                }}
              >
                Back
              </button>
              {onboardingStep < ONBOARDING_STEP_COUNT - 1 ? (
                <button
                  type="button"
                  className="primary-button"
                  disabled={
                    onboardingBusy || searchConnectorBusy !== null
                  }
                  onClick={() => {
                    setOnboardingSaveError(null);
                    setOnboardingStep((step) =>
                      Math.min(ONBOARDING_STEP_COUNT - 1, step + 1),
                    );
                  }}
                >
                  Continue
                </button>
              ) : (
                <button
                  type="button"
                  className="primary-button"
                  aria-busy={onboardingBusy}
                  disabled={onboardingBusy || searchConnectorBusy !== null}
                  onClick={() => void finishOnboarding()}
                >
                  {onboardingBusy ? "Saving setup…" : "Start using OpenAgentFleet"}
                </button>
              )}
            </footer>
          </section>
        </div>
      )}
      {agentBuilderOpen && (
        <div
          className="dialog-backdrop"
          role="dialog"
          aria-modal="true"
          aria-labelledby="agent-builder-title"
        >
          <form
            className="agent-builder-dialog"
            onSubmit={(event) => {
              event.preventDefault();
              void createAgent();
            }}
          >
            <button
              type="button"
              className="icon-button dialog-close"
              onClick={() => setAgentBuilderOpen(false)}
              aria-label="Close agent builder"
            >
              ×
            </button>
            <div className="eyebrow">One agent · one durable chat</div>
            <h2 id="agent-builder-title">
              {agentEditingID ? "Edit agent" : "Create an agent"}
            </h2>
            <p>
              Give this agent a clear role. It will use the workspace default
                harness and its own durable memory.
            </p>
            {!agentEditingID && (
              <section
                className="agent-template-section"
                aria-labelledby="agent-template-title"
              >
                <div className="agent-template-heading">
                  <div>
                    <span className="eyebrow">Starter templates</span>
                    <h3 id="agent-template-title">Start with a role</h3>
                  </div>
                  <span>Role prompt only</span>
                </div>
                <div className="agent-template-grid" role="list">
                  {agentTemplates.map((template) => (
                    <button
                      type="button"
                      key={template.slug}
                      className={`agent-template-card ${
                        selectedAgentTemplate?.slug === template.slug
                          ? "selected"
                          : ""
                      }`}
                      aria-pressed={selectedAgentTemplate?.slug === template.slug}
                      onClick={() => setSelectedAgentTemplate(template)}
                      role="listitem"
                    >
                      <img src={template.avatarUrl} alt="" />
                      <span>
                        <strong>{template.name}</strong>
                        <small>{template.summary}</small>
                      </span>
                    </button>
                  ))}
                </div>
                {selectedAgentTemplate && (
                  <div className="agent-template-preview" role="status">
                    <img src={selectedAgentTemplate.avatarUrl} alt="" />
                    <div>
                      <strong>{selectedAgentTemplate.name}</strong>
                      <p>{selectedAgentTemplate.description}</p>
                      <small>
                        Adding fills the role fields only. Permissions, plugins,
                        MCP servers, and execution settings stay unchanged.
                      </small>
                    </div>
                    <button
                      type="button"
                      className="primary-button"
                      onClick={() => applyAgentTemplate(selectedAgentTemplate)}
                    >
                      Add {selectedAgentTemplate.name}
                    </button>
                  </div>
                )}
              </section>
            )}
            <div className="agent-builder-grid">
              <label>
                Name
                <input
                  autoFocus
                  required
                  maxLength={80}
                  value={agentName}
                  onChange={(event) => setAgentName(event.target.value)}
                  placeholder="Fleet Guide"
                />
              </label>
              <label>
                Role <span className="required-mark">Required</span>
                <input
                  required
                  maxLength={120}
                  value={agentTitle}
                  onChange={(event) => setAgentTitle(event.target.value)}
                  placeholder="Configures this fleet and knows the codebase"
                />
              </label>
            </div>
            <label>
              Description
              <textarea
                maxLength={1000}
                value={agentDescription}
                onChange={(event) => setAgentDescription(event.target.value)}
                placeholder="What this agent owns, how it should work, and when it should ask."
              />
            </label>
            <details
              className="builder-advanced builder-execution-advanced"
              open={agentAdvancedOpen}
              onToggle={(event) =>
                setAgentAdvancedOpen(event.currentTarget.open)
              }
            >
              <summary>
                <span>Advanced agent options</span>
                <small>Optional: override the lead or add helpers, plugins and MCPs</small>
              </summary>
              <div className="builder-advanced-body">
            <section className="execution-card" aria-labelledby="engine-config-title">
              <div className="execution-card-heading">
                <div>
                  <span className="eyebrow">Per-agent override</span>
                  <h3 id="engine-config-title">Who runs this agent?</h3>
                </div>
                <span className="execution-badge">Optional</span>
              </div>
              <p className="field-note execution-intro">
                Leave this section closed to inherit the workspace default. If
                you open it, this agent gets its own lead harness and model.
              </p>
              <div className="agent-builder-grid">
                <label>
                  Lead harness
                  <select
                    value={agentLeadHarness}
                    onChange={(event) => {
                      const next = event.target.value;
                      setAgentLeadHarness(next);
                      setAgentModel(defaultLeadModel(next, data?.model_catalog ?? []));
                      if (next !== "codex_app_server") {
                        setAgentLeadTier("default");
                        if (["xhigh", "max"].includes(agentLeadReasoning))
                          setAgentLeadReasoning("high");
                      }
                      if (next === "opencode") {
                        setAgentLeadPermission("provider_default");
                      } else {
                        setAgentLeadPermission(agentPermissionFromUsageDefault());
                      }
                    }}
                  >
					<option value="grok_build">Grok Build</option>
                    <option value="codex_app_server">Codex App Server</option>
                    <option value="opencode">OpenCode 1.18.10 · starter route</option>
                  </select>
                </label>
                <div className="builder-model-field">
                  <ModelPicker
                    key={`builder-${agentLeadHarness}`}
                    harness={agentLeadHarness}
                    value={agentModel}
                    onChange={setAgentModel}
                    catalog={data?.model_catalog ?? []}
                  />
                </div>
                <label>
                  Reasoning depth
                  <select
                    value={agentLeadReasoning}
                    onChange={(event) => setAgentLeadReasoning(event.target.value)}
                  >
                    <option value="low">Low</option>
                    <option value="medium">Medium</option>
                    <option value="high">High</option>
                    {agentLeadHarness === "codex_app_server" && (
                      <>
                        <option value="xhigh">XHigh</option>
                        <option value="max">Max</option>
                      </>
                    )}
                  </select>
                </label>
                <label>
                  Service tier
                  <select
                    value={agentLeadTier}
                    disabled={agentLeadHarness === "opencode"}
                    onChange={(event) => setAgentLeadTier(event.target.value)}
                  >
                    <option value="default">Default</option>
                    {agentLeadHarness === "codex_app_server" && (
                      <>
                        <option value="priority">Priority</option>
                        <option value="flex">Flex</option>
                      </>
                    )}
                  </select>
                </label>
                <label>
                  Web search
                  <select
                    value={agentLeadWebSearch}
                    onChange={(event) =>
                      setAgentLeadWebSearch(
                        event.target.value as "live" | "disabled",
                      )
                    }
                  >
                    <option value="live">On — live sources</option>
                    <option value="disabled">Off</option>
                  </select>
                </label>
              </div>
              <label className="execution-permission">
                Permission mode
                <select
                  value={agentLeadPermission}
                  disabled={agentLeadHarness === "opencode"}
                  onChange={(event) => setAgentLeadPermission(event.target.value)}
                >
                  {agentLeadHarness === "opencode" && <option value="provider_default">OpenCode default</option>}
                  <option value="ask">Ask through lead</option>
                  <option value="read_only">Read only</option>
                  <option value="workspace">Workspace</option>
                </select>
              </label>
              <p className="field-note">
                {agentLeadHarness === "opencode"
                  ? "OpenCode uses the selected provider/model and its own tool permissions. Web Search Plus, Hound, and Donsetch are explicit optional MCP grants."
                  : "Reasoning depth applies to the selected model. Live search is native to the lead; Web Search Plus, Hound, and Donsetch remain optional connectors."}
              </p>
            </section>
            <label className="builder-toggle">
              <span>
                <strong>Allow background helpers</strong>
                <small>
                  Let this agent delegate bounded tasks when a helper pool is
                  configured.
                </small>
              </span>
              <input
                type="checkbox"
                checked={agentOrchestrator}
                onChange={(event) => setAgentOrchestrator(event.target.checked)}
              />
            </label>
            <section className="worker-section" aria-labelledby="worker-config-title">
              <div className="execution-card-heading">
                <div>
                  <span className="eyebrow">Delegation</span>
                  <h3 id="worker-config-title">Background helpers</h3>
                </div>
                <button
                  type="button"
                  className="add-worker-button"
                  onClick={() => setAgentWorkers((workers) => [...workers, newWorkerDraft()])}
                >
                  + Add worker
                </button>
              </div>
              <p className="field-note worker-intro">
                Optional bounded helpers. Only Grok Build and OpenCode are
                executable here today. Claude, Pi, Codex CLI and Cursor remain
                visible as future adapters and never inherit hidden access.
              </p>
              {agentWorkers.length === 0 ? (
                <div className="worker-empty">No delegated workers. This agent runs through its lead only.</div>
              ) : (
                <div className="worker-list">
                  {agentWorkers.map((worker, index) => (
                    <article className="worker-row" key={worker.id}>
                      <div className="worker-row-heading">
                        <strong>Worker {index + 1}</strong>
                        <button
                          type="button"
                          className="remove-worker-button"
                          onClick={() => setAgentWorkers((workers) => workers.filter((item) => item.id !== worker.id))}
                          aria-label={`Remove worker ${index + 1}`}
                        >
                          Remove
                        </button>
                      </div>
                      <div className="worker-grid worker-grid-primary">
                        <label>
                          Worker harness
                          <select
                            value={worker.harness}
                            aria-describedby={`worker-${worker.id}-support`}
                            onChange={(event) =>
                              updateAgentWorker(
                                worker.id,
                                "harness",
                                event.target.value,
                              )
                            }
                          >
                            {WORKER_HARNESS_OPTIONS.map((option) => (
                              <option
                                key={option.value}
                                value={option.value}
                                disabled={!option.supported}
                              >
                                {option.label}
                                {!option.supported ? " · future" : ""}
                              </option>
                            ))}
                          </select>
                          <small
                            id={`worker-${worker.id}-support`}
                            className="worker-harness-note"
                          >
                            {workerHarnessOption(worker.harness)?.supported
                              ? workerHarnessOption(worker.harness)?.detail
                              : "Future adapter: this worker cannot run in the lead-worker path yet."}
                          </small>
                        </label>
                        <label>
                          Worker model
                          <input value={worker.model} onChange={(event) => updateAgentWorker(worker.id, "model", event.target.value)} placeholder="Harness automatic" />
                        </label>
                        <label>
                          Reasoning depth
                          <select value={worker.reasoning} onChange={(event) => updateAgentWorker(worker.id, "reasoning", event.target.value)}>
                            <option value="low">Low</option><option value="medium">Medium</option><option value="high">High</option>
                            {worker.harness === "opencode" && <><option value="xhigh">XHigh</option><option value="max">Max</option></>}
                          </select>
                        </label>
                      </div>
                      <div className="worker-grid">
                        <label>
                          Service tier
                          <select value={worker.tier} disabled={worker.harness !== "codex"} onChange={(event) => updateAgentWorker(worker.id, "tier", event.target.value)}>
                            <option value="default">Default</option>
                            {worker.harness === "codex" && <><option value="priority">Priority</option><option value="flex">Flex</option></>}
                          </select>
                        </label>
                        <label>
                          Permission
                          <select value={worker.permission} disabled={worker.harness === "opencode"} onChange={(event) => updateAgentWorker(worker.id, "permission", event.target.value)}>
                            {worker.harness === "opencode" ? <option value="provider_default">OpenCode default</option> : <><option value="ask">Ask every time</option><option value="read_only">Read only</option><option value="workspace">Workspace</option></>}
                          </select>
                        </label>
                        <label>
                          Max turns
                          <input inputMode="numeric" value={worker.maxTurns} onChange={(event) => updateAgentWorker(worker.id, "maxTurns", event.target.value)} />
                        </label>
                        <label>
                          Timeout (sec)
                          <input inputMode="numeric" value={worker.timeout} onChange={(event) => updateAgentWorker(worker.id, "timeout", event.target.value)} />
                        </label>
                      </div>
                    </article>
                  ))}
                </div>
              )}
            </section>
            <details
              className="builder-tools-advanced"
              open={agentToolsOpen}
              onToggle={(event) =>
                setAgentToolsOpen(event.currentTarget.open)
              }
            >
              <summary>Plugins & MCPs</summary>
              <label>
                Plugins
                <input
                  value={agentPlugins}
                  onChange={(event) => setAgentPlugins(event.target.value)}
                  placeholder="github, linear"
                />
              </label>
              <label>
                MCP servers
                <input
                  value={agentMCPs}
                  onChange={(event) => setAgentMCPs(event.target.value)}
                  placeholder="filesystem, browser"
                />
              </label>
              <p className="field-note">
                Comma-separated identifiers. Capabilities still require
                explicit approval.
              </p>
              {!agentEditingID && onboardingConnectorMCPs.length > 0 && (
                <p className="connector-prefill-note">
                  Prefilled from onboarding for this first Agent. Review, edit,
                  or remove these MCP IDs before creating it.
                </p>
              )}
            </details>
              </div>
            </details>
            <div className="dialog-actions">
              <button type="button" onClick={() => setAgentBuilderOpen(false)}>
                Cancel
              </button>
              <button
                className="primary-button"
                type="submit"
                disabled={!agentName.trim() || !agentTitle.trim() || agentBuilderBusy}
              >
                {agentBuilderBusy
                  ? agentEditingID
                    ? "Saving…"
                    : "Creating…"
                  : agentEditingID
                    ? "Save agent"
                    : "Create agent"}
              </button>
            </div>
          </form>
        </div>
      )}
      {workspaceOpen && (
        <div
          className="workspace-drawer"
          role="dialog"
          aria-modal="true"
          aria-labelledby="workspace-title"
        >
          <div
            className="workspace-drawer-backdrop"
            onClick={() => setWorkspaceOpen(false)}
          />
          <div className="workspace-drawer-panel">
            <button
              className="icon-button drawer-close"
              aria-label="Close workspace"
              onClick={() => setWorkspaceOpen(false)}
            >
              ×
            </button>
            <h2 id="workspace-title">Workspace</h2>
            {workspace}
          </div>
        </div>
      )}
      {computerViewOpen && (
        <div
          className="computer-modal"
          role="dialog"
          aria-modal="true"
          aria-labelledby="computer-title"
        >
          <div className="computer-modal-card">
            <header className="computer-modal-header">
              <div>
                <div className="eyebrow">
                  Builder&apos;s computer{" "}
                  <span className="live-chip">
                    {data.computer.takeover ? "Takeover" : "Live"}
                  </span>
                </div>
                <strong id="computer-title">
                  {activeComputerView === "desktop"
                    ? "Full desktop · files, terminal, Chromium"
                    : data.computer.title || "Chromium workspace"}
                </strong>
                <span>
                  {activeComputerView === "desktop"
                    ? "Isolated local workspace"
                    : data.computer.url || "about:blank"}
                </span>
              </div>
              <div className="computer-modal-actions">
                <span
                  className={`computer-state ${activeFrameReady ? "ready" : computerState}`}
                  aria-live="polite"
                >
                  {activeFrameReady
                    ? `${activeComputerView} ready`
                    : activeFrameError
                      ? "frame unavailable"
                      : computerState === "starting"
                        ? "starting Chromium…"
                        : computerState === "error"
                          ? "computer needs attention"
                          : computerState === "unavailable"
                            ? "runtime unavailable"
                            : "computer stopped"}
                </span>
                {activeFrameError && (
                  <button
                    type="button"
                    className="computer-frame-retry"
                    onClick={retryComputerFrame}
                  >
                    Retry frame
                  </button>
                )}
                <button
                  ref={computerCloseRef}
                  className="icon-button"
                  onClick={() => setComputerViewOpen(false)}
                  aria-label="Close computer view"
                >
                  ×
                </button>
              </div>
            </header>
            <div
              className={`computer-viewport ${activeComputerView} ${data.computer.takeover ? "takeover" : ""}`}
            >
              {activeComputerView === "desktop" ? (
                desktopFrameURL ? (
                  <img
                    className="computer-desktop-frame"
                    src={desktopFrameURL}
                    alt="Builder's Computer desktop live screen"
                    ref={computerFrameRef}
                    tabIndex={0}
                    draggable={false}
                    onPointerDown={handleDesktopPointerDown}
                    onKeyDown={handleComputerKeyDown}
                  />
                ) : (
                  <div className="computer-viewport-empty">
                    <strong>
                      {!data.computer.running
                        ? "Agent Computer is stopped"
                        : activeFrameError
                          ? "Desktop frame unavailable"
                          : "Connecting to desktop…"}
                    </strong>
                    <span>
                      {!data.computer.running
                        ? "Start the isolated computer to open its desktop."
                        : activeFrameError
                          ? activeFrameState.error ??
                            "The local computer is running, but its live frame did not respond."
                          : "The local computer is running. The first frame can take a moment to arrive."}
                    </span>
                    {activeFrameError && (
                      <button
                        type="button"
                        className="computer-frame-retry"
                        onClick={retryComputerFrame}
                      >
                        Retry frame
                      </button>
                    )}
                  </div>
                )
              ) : computerFrameURL ? (
                <img
                  src={computerFrameURL}
                  alt="Builder's Computer browser live screen"
                  ref={computerFrameRef}
                  tabIndex={0}
                  draggable={false}
                  onPointerDown={handleComputerPointerDown}
                  onKeyDown={handleComputerKeyDown}
                />
              ) : (
                <div className="computer-viewport-empty">
                  <strong>
                    {!data.computer.running
                      ? "Agent Computer is stopped"
                      : activeFrameError
                        ? "Browser frame unavailable"
                        : "Connecting to Chromium…"}
                  </strong>
                  <span>
                    {!data.computer.running
                      ? "Start the isolated computer to open Chromium."
                      : activeFrameError
                        ? activeFrameState.error ??
                          "The local computer is running, but its live frame did not respond."
                        : "Chromium is starting. The first frame can take a moment to arrive."}
                  </span>
                  {activeFrameError && (
                    <button
                      type="button"
                      className="computer-frame-retry"
                      onClick={retryComputerFrame}
                    >
                      Retry frame
                    </button>
                  )}
                </div>
              )}
            </div>
            <div className="computer-toolbar">
              <div
                className="computer-view-tabs"
                role="tablist"
                aria-label="Computer view"
              >
                <button
                  type="button"
                  role="tab"
                  aria-selected={activeComputerView === "desktop"}
                  className={activeComputerView === "desktop" ? "selected" : ""}
                  onClick={() => setComputerViewMode("desktop")}
                  disabled={!data.computer.desktop_ready}
                >
                  Desktop
                </button>
                <button
                  type="button"
                  role="tab"
                  aria-selected={activeComputerView === "browser"}
                  className={activeComputerView === "browser" ? "selected" : ""}
                  onClick={() => setComputerViewMode("browser")}
                  disabled={!data.computer.browser_ready}
                >
                  Browser
                </button>
              </div>
              {activeComputerView === "browser" && (
                <form
                  className="computer-nav"
                  onSubmit={(event) => {
                    event.preventDefault();
                    void computerAction({
                      action: "navigate",
                      url: computerURL,
                    });
                  }}
                >
                  <button
                    type="button"
                    onClick={() => void computerAction({ action: "back" })}
                    disabled={!data.computer.takeover || computerActionBusy}
                    aria-label="Back"
                  >
                    ‹
                  </button>
                  <button
                    type="button"
                    onClick={() => void computerAction({ action: "forward" })}
                    disabled={!data.computer.takeover || computerActionBusy}
                    aria-label="Forward"
                  >
                    ›
                  </button>
                  <input
                    value={computerURL}
                    onChange={(event) => setComputerURL(event.target.value)}
                    aria-label="Browser URL"
                  />
                  <button
                    type="submit"
                    disabled={!data.computer.takeover || computerActionBusy}
                  >
                    Go
                  </button>
                </form>
              )}
              <div className="computer-actions">
                <button
                  type="button"
                  className="computer-stop-button"
                  onClick={() => void stopComputer()}
                  disabled={computerBusy || !data.computer.running}
                >
                  {computerBusy
                    ? computerState === "starting" || !data.computer.running
                      ? "Starting…"
                      : "Stopping…"
                    : "Stop computer"}
                </button>
                <button
                  className={
                    data.computer.agent_control
                      ? "agent-control-active"
                      : "agent-control-button"
                  }
                  aria-pressed={Boolean(data.computer.agent_control)}
                  title={
                    data.computer.agent_control
                      ? "Pause Agent Control; new runs will no longer receive Computer tools"
                      : "Allow new OpenAgentFleet runs to use the isolated Computer"
                  }
                  onClick={() =>
                    void setComputerAgentControl(!data.computer.agent_control)
                  }
                  disabled={!computerReady}
                >
                  {data.computer.agent_control
                    ? "Pause agent"
                    : "Agent control"}
                </button>
                <button
                  className={
                    data.computer.takeover
                      ? "takeover-active"
                      : "takeover-button"
                  }
                  aria-pressed={data.computer.takeover}
                  onClick={() =>
                    void setComputerTakeover(!data.computer.takeover)
                  }
                  disabled={computerActionBusy || !computerReady}
                >
                  {data.computer.takeover ? "Release control" : "Take control"}
                </button>
                <button
                  className="teach-button"
                  onClick={() => setTeachGoalOpen(true)}
                  disabled={!computerReady || !skillLearningEnabled}
                  title={
                    skillLearningEnabled
                      ? "Record a safe workflow for review"
                      : "Enable Skill learning in Settings first"
                  }
                >
                  Teach a task
                </button>
              </div>
              {data.computer.takeover && (
                <div className="computer-input-row">
                  <form
                    onSubmit={(event) => {
                      event.preventDefault();
                      if (computerText) {
                        void computerAction(
                          {
                            action: "type",
                            text: computerText,
                            sensitive: computerSensitive,
                          },
                          activeComputerView,
                        );
                        setComputerText("");
                      }
                    }}
                  >
                    <input
                      value={computerText}
                      onChange={(event) => setComputerText(event.target.value)}
                      placeholder={
                        activeComputerView === "desktop"
                          ? "Type into active desktop app…"
                          : "Type into active page…"
                      }
                      aria-label={
                        activeComputerView === "desktop"
                          ? "Type into desktop"
                          : "Type into browser"
                      }
                    />
                    <button
                      type="submit"
                      disabled={!computerText || computerActionBusy}
                    >
                      Type
                    </button>
                  </form>
                  {activeComputerView === "desktop" && (
                    <button
                      type="button"
                      onClick={() =>
                        void computerAction(
                          { action: "press", key: "Enter" },
                          "desktop",
                        )
                      }
                      disabled={computerActionBusy}
                    >
                      Press Enter
                    </button>
                  )}
                  <label>
                    <input
                      type="checkbox"
                      checked={computerSensitive}
                      onChange={(event) =>
                        setComputerSensitive(event.target.checked)
                      }
                    />{" "}
                    redact from Teach trace
                  </label>
                  <div className="secure-handoff-actions">
                    <button
                      type="button"
                      onClick={() => void promptSecureHandoff("password")}
                      disabled={
                        !nativeRuntime ||
                        activeComputerView !== "browser" ||
                        !activeSecretRun ||
                        computerActionBusy ||
                        secureHandoffBusy !== null
                      }
                      title={
                        nativeRuntime
                          ? "Open the native macOS secure password prompt"
                          : "Available in the OpenAgentFleet Mac app"
                      }
                    >
                      {secureHandoffBusy === "password"
                        ? "Opening secure prompt…"
                        : "Enter password securely…"}
                    </button>
                    <button
                      type="button"
                      onClick={() =>
                        void promptSecureHandoff("two_factor_code")
                      }
                      disabled={
                        !nativeRuntime ||
                        activeComputerView !== "browser" ||
                        !activeSecretRun ||
                        computerActionBusy ||
                        secureHandoffBusy !== null
                      }
                      title={
                        nativeRuntime
                          ? "Open the native macOS secure one-time-code prompt"
                          : "Available in the OpenAgentFleet Mac app"
                      }
                    >
                      {secureHandoffBusy === "two_factor_code"
                        ? "Opening secure prompt…"
                        : "Enter code securely…"}
                    </button>
                  </div>
                </div>
              )}
            </div>
            <p className="computer-safety-note">
              Take Control and Agent Control are separate, manual controls.
              {!data.computer.agent_control
                ? " Agent Control is off, so new agent runs will not receive Computer tools until you enable it."
                : " Agent Control is on, so the selected lead may use the isolated Computer; Take Control remains yours. "}
              Desktop actions use the same server-gated control path as browser
              actions, and sensitive Teach steps are redacted from its trace.
              For passwords and one-time codes, use the native secure prompt
              below while an active run is waiting and the focused target is a
              Browser field. Do not paste passwords, passkeys, CAPTCHA answers,
              or payment details into this field.
            </p>
          </div>
        </div>
      )}
      {teachGoalOpen && (
        <div
          className="dialog-backdrop"
          role="dialog"
          aria-modal="true"
          aria-labelledby="teach-title"
        >
          <form
            className="small-dialog"
            onSubmit={(event) => {
              event.preventDefault();
              if (teachGoal.trim())
                void teachAction("start", { goal: teachGoal.trim() });
            }}
          >
            <button
              type="button"
              className="icon-button dialog-close"
              onClick={() => setTeachGoalOpen(false)}
              aria-label="Close teach a task"
            >
              ×
            </button>
            <div className="eyebrow">Safe workflow capture</div>
            <h2 id="teach-title">Teach a task</h2>
            <p>
              State the outcome, then demonstrate it once. The recording lasts
              at most ten minutes. Pause before any password, passkey, 2FA,
              CAPTCHA, payment or other secret.
            </p>
            <label>
              Outcome
              <input
                ref={teachGoalRef}
                value={teachGoal}
                onChange={(event) => setTeachGoal(event.target.value)}
                placeholder="For example: download the latest CI artifact"
              />
            </label>
            <div className="dialog-actions">
              <button type="button" onClick={() => setTeachGoalOpen(false)}>
                Cancel
              </button>
              <button
                className="primary-button"
                type="submit"
                disabled={!teachGoal.trim() || teachBusy || !skillLearningEnabled}
              >
                {skillLearningEnabled
                  ? "Start teaching"
                  : "Enable Skill learning in Settings"}
              </button>
            </div>
          </form>
        </div>
      )}
      {settingsOpen && (
        <div
          className="dialog-backdrop"
          role="dialog"
          aria-modal="true"
          aria-labelledby="settings-title"
        >
          <section className="settings-dialog">
            <button
              ref={settingsCloseRef}
              className="icon-button dialog-close"
              onClick={closeSettings}
              aria-label="Close settings"
            >
              ×
            </button>
            <div className="eyebrow">OpenAgentFleet</div>
            <h2 id="settings-title">Settings</h2>
            <section>
              <h3>Appearance</h3>
              <div className="segmented">
                {(["light", "dark", "system"] as Theme[]).map((theme) => (
                  <button
                    key={theme}
                    aria-pressed={
                      (preferences.appearance?.theme ?? "system") === theme
                    }
                    className={
                      (preferences.appearance?.theme ?? "system") === theme
                        ? "selected"
                        : ""
                    }
                    onClick={() =>
                      void patchPreferences({ appearance: { theme } })
                    }
                  >
                    {theme}
                  </button>
                ))}
              </div>
              <label>
                Density
                <select
                  value={preferences.appearance?.density ?? "comfortable"}
                  onChange={(event) =>
                    void patchPreferences({
                      appearance: { density: event.target.value },
                    })
                  }
                >
                  <option value="comfortable">Comfortable</option>
                  <option value="compact">Compact</option>
                </select>
              </label>
              <label>
                Text size
                <input
                  type="range"
                  min="0.9"
                  max="1.2"
                  step="0.05"
                  value={preferences.appearance?.font_scale ?? 1}
                  onChange={(event) =>
                    void patchPreferences({
                      appearance: { font_scale: Number(event.target.value) },
                    })
                  }
                />
              </label>
            </section>
            <section aria-labelledby="settings-notifications-title">
              <h3 id="settings-notifications-title">Notifications</h3>
              <p className="field-note">
                In-app notices are always available. Desktop notifications are
                optional and only fire when this window is hidden.
              </p>
              <div className="notification-setting-row">
                <span>
                  <strong>
                    {desktopNotificationPermission === "granted"
                      ? "Desktop notifications enabled"
                      : desktopNotificationPermission === "denied"
                        ? "Desktop notifications blocked"
                        : desktopNotificationPermission === "unavailable"
                          ? "Desktop notifications unavailable"
                          : "Desktop notifications off"}
                  </strong>
                  <small>
                    {desktopNotificationPermission === "denied"
                      ? "Allow them in macOS notification settings if you want them later."
                      : "Get a discreet alert when an Agent finishes or needs approval."}
                  </small>
                </span>
                {desktopNotificationPermission !== "granted" &&
                  desktopNotificationPermission !== "unavailable" && (
                    <button
                      type="button"
                      onClick={() => void requestDesktopNotifications()}
                      disabled={desktopNotificationPermission === "denied"}
                    >
                      {desktopNotificationPermission === "denied"
                        ? "Blocked"
                        : "Enable"}
                    </button>
                  )}
              </div>
            </section>
            <section className="settings-default-lead">
              <h3>Workspace default</h3>
              <p className="field-note">
                One lead harness and one model run every agent unless you enable a
                per-agent override in Agent Builder.
              </p>
              <label>
                Lead harness
                <select
                  value={selectedLeadChoice?.value ?? "grok"}
                  onChange={(event) => {
                    const next = event.target.value;
                    const currentReasoning =
                      preferences.usage?.reasoning_effort ?? reasoningEffort;
                    const nextReasoning =
                      next === "codex_app_server" ||
                      ["low", "medium", "high"].includes(currentReasoning)
                        ? currentReasoning
                        : "high";
                    const currentPermission =
                      preferences.usage?.permission_mode ?? permissionMode;
                    void patchPreferences({
                      workspace: {
                        engine: next,
                        model: defaultLeadModel(next, data?.model_catalog ?? []),
                      },
                      usage: {
                        reasoning_effort: nextReasoning,
                        permission_mode:
                          next === "opencode" ? "default" : currentPermission,
                      },
                    });
                    setWorkspaceModelDraft(null);
                  }}
                >
                  {leadChoices.map((choice) => (
                    <option
                      key={choice.value}
                      value={choice.value}
                      disabled={!choice.available}
                    >
                      {choice.label} · {choice.status}
                    </option>
                  ))}
                </select>
              </label>
              <div className="settings-model-field">
                <ModelPicker
                  key={`workspace-${selectedLeadChoice?.value ?? selectedLeadEngine}`}
                  harness={selectedLeadChoice?.value ?? selectedLeadEngine}
                  value={
                    workspaceModelDraft ??
                    preferences.workspace?.model ??
                    defaultLeadModel(selectedLeadChoice?.value ?? selectedLeadEngine, data?.model_catalog ?? [])
                  }
                  onChange={setWorkspaceModelDraft}
                  catalog={data?.model_catalog ?? []}
                />
                {workspaceModelDraft !== null && (
                  <div className="settings-model-actions">
                    <button
                      type="button"
                      className="primary-button"
                      onClick={async () => {
                        const saved = await patchPreferences({
                          workspace: { model: workspaceModelDraft },
                        });
                        if (saved) {
                          setWorkspaceModelDraft(null);
                          setNotice("Default model saved.");
                        }
                      }}
                    >
                      Use model
                    </button>
                  </div>
                )}
              </div>
              <label>
                Reasoning depth
                <select
                  value={selectedReasoningValue}
                  onChange={(event) =>
                    void patchPreferences({
                      usage: { reasoning_effort: event.target.value },
                    })
                  }
                >
                  <option value="low">Low</option>
                  <option value="medium">Medium</option>
                  <option value="high">High</option>
                  {selectedLeadChoice?.value === "codex_app_server" && (
                    <>
                      <option value="xhigh">XHigh</option>
                      <option value="max">Max</option>
                    </>
                  )}
                </select>
              </label>
              <label>
                Permission
                <select
                  value={
                    selectedLeadChoice?.value === "opencode"
                      ? "default"
                      : preferences.usage?.permission_mode ?? permissionMode
                  }
                  disabled={selectedLeadChoice?.value === "opencode"}
                  onChange={(event) =>
                    void patchPreferences({
                      usage: { permission_mode: event.target.value },
                    })
                  }
                >
                  <option value="default">
                    {selectedLeadChoice?.value === "opencode"
                      ? "OpenCode default"
                      : "Ask"}
                  </option>
                  {selectedLeadChoice?.value !== "opencode" && (
                    <>
                      <option value="auto">Auto</option>
                      <option value="plan">Plan</option>
                    </>
                  )}
                </select>
              </label>
              <label>
                Computer opens to
                <select
                  value={preferences.computer?.default_surface ?? "desktop"}
                  onChange={(event) =>
                    void patchPreferences({
                      computer: { default_surface: event.target.value },
                    })
                  }
                >
                  <option value="desktop">Desktop</option>
                  <option value="browser">Browser</option>
                </select>
              </label>
              <label>
                Agent Computer runtime
                <select
                  value={
                    preferences.computer?.runtime ??
                    (isLinuxHost(data) ? "docker" : "colima")
                  }
                  onChange={(event) =>
                    void patchPreferences({
                      computer: { runtime: event.target.value },
                    })
                  }
                >
                  {isLinuxHost(data) ? (
                    <>
                      <option value="docker">Docker Engine (recommended)</option>
                      <option value="auto">Automatic compatibility fallback</option>
                      <option value="colima">Colima + Docker</option>
                      <option value="docker_desktop">Docker Desktop</option>
                      <option value="orbstack">OrbStack + Docker</option>
                    </>
                  ) : (
                    <>
                      <option value="colima">Colima + Docker (recommended)</option>
                      <option value="docker_desktop">Docker Desktop</option>
                      <option value="orbstack">OrbStack + Docker</option>
                      <option value="docker">Docker Engine</option>
                      <option value="auto">Automatic compatibility fallback</option>
                      <option value="apple_container" disabled>
                        Apple Container — experimental adapter pending
                      </option>
                    </>
                  )}
                </select>
              </label>
              <details className="settings-advanced-section computer-resources-settings">
                <summary>
                  Computer resources
                  <span className="settings-summary-value">
                    {computerResources.cpus} CPU · {computerResources.ram_gib} GiB RAM · {computerResources.disk_gib} GiB disk
                  </span>
                </summary>
                <p className="field-note">
                  {isLinuxHost(data)
                    ? "Optional. Docker Engine uses CPU, RAM and swap as container limits. Disk is managed by the Docker host."
                    : "Optional. Colima uses these values for the isolated Linux VM; Docker Desktop and OrbStack use CPU/RAM/swap as container limits while their VM disk stays managed by the runtime."}
                </p>
                <label>
                  Resource preset
                  <select
                    value={selectedComputerResourcePreset}
                    onChange={(event) => {
                      const preset = computerResourcePresets.find(
                        (item) => item.value === event.target.value,
                      );
                      if (!preset) return;
                      void patchPreferences({
                        computer: {
                          cpus: preset.cpus,
                          ram_gib: preset.ram_gib,
                          disk_gib: preset.disk_gib,
                          swap_gib: preset.swap_gib,
                        },
                      });
                    }}
                  >
                    {computerResourcePresets.map((preset) => (
                      <option key={preset.value} value={preset.value}>
                        {preset.label}
                      </option>
                    ))}
                    <option value="custom">Custom</option>
                  </select>
                </label>
                <div className="computer-resource-grid">
                  <label>
                    CPU
                    <input
                      type="number"
                      min={1}
                      max={16}
                      step={1}
                      value={computerResources.cpus}
                      onChange={(event) =>
                        void patchPreferences({
                          computer: { cpus: Number(event.target.value) },
                        })
                      }
                    />
                  </label>
                  <label>
                    RAM (GiB)
                    <input
                      type="number"
                      min={2}
                      max={64}
                      step={1}
                      value={computerResources.ram_gib}
                      onChange={(event) =>
                        void patchPreferences({
                          computer: { ram_gib: Number(event.target.value) },
                        })
                      }
                    />
                  </label>
                  <label>
                    Disk (GiB)
                    <input
                      type="number"
                      min={10}
                      max={500}
                      step={1}
                      value={computerResources.disk_gib}
                      onChange={(event) =>
                        void patchPreferences({
                          computer: { disk_gib: Number(event.target.value) },
                        })
                      }
                    />
                  </label>
                  <label>
                    Guest swap (GiB)
                    <input
                      type="number"
                      min={0}
                      max={16}
                      step={1}
                      value={computerResources.swap_gib}
                      onChange={(event) =>
                        void patchPreferences({
                          computer: { swap_gib: Number(event.target.value) },
                        })
                      }
                    />
                  </label>
                </div>
                <label>
                  Linux image
                  <select
                    value={computerResources.os_image}
                    onChange={(event) =>
                      void patchPreferences({
                        computer: { os_image: event.target.value },
                      })
                    }
                  >
                    <option value="ubuntu-24.04">Ubuntu 24.04 LTS (recommended)</option>
                    <option value="ubuntu-26.04">Ubuntu 26.04 LTS</option>
                    <option value="debian-13">Debian 13 (Trixie)</option>
                  </select>
                </label>
                <p className="field-note">
                  Changes apply the next time the Agent Computer starts. A
                  larger existing Colima disk is never deleted or shrunk; for
                  example, an existing 100 GiB profile remains 100 GiB when
                  the setting says 25 GiB. Swap is a small emergency buffer,
                  not a replacement for RAM.
                </p>
                {data.computer.resources && (
                  <p className="field-note">
                    Active computer: {data.computer.resources.cpus ?? computerResources.cpus} CPU · {data.computer.resources.memory_gib ?? computerResources.ram_gib} GiB RAM · {data.computer.resources.disk_gib ?? computerResources.disk_gib} GiB disk · {data.computer.resources.swap_gib ?? computerResources.swap_gib} GiB swap
                  </p>
                )}
              </details>
              <div className="runtime-status-card" aria-label="Runtime status">
                <div>
                  <span className="eyebrow">Active now</span>
                  <strong>{data.computer.runtime_name ?? "Docker Engine"}</strong>
                </div>
                <span
                  className={`runtime-health ${data.computer.available ? "ready" : "warming"}`}
                >
                  {data.computer.available ? "ready" : "unavailable"}
                </span>
                <small>
                  {data.computer.runtime_context
                    ? `Context: ${data.computer.runtime_context}`
                    : data.computer.runtime_detail ??
                      "Runtime selection applies the next time the local controller starts."}
                </small>
              </div>
              <div className="runtime-inventory">
                {(data.runtimes ?? []).map((runtime) => (
                  <div className="runtime-inventory-row" key={runtime.id}>
                    <span>
                      <strong>{runtime.name}</strong>
                      <small>{runtime.detail ?? "Not probed"}</small>
                    </span>
                    <em className={runtime.healthy ? "healthy" : ""}>
                      {runtime.healthy
                        ? "ready"
                        : runtime.available
                          ? "configured"
                          : "not installed"}
                    </em>
                  </div>
                ))}
              </div>
              {isLinuxHost(data) && !dockerEngineRuntime()?.healthy && (
                <div className="runtime-install-card compact">
                  <div>
                    <strong>Install Docker Engine</strong>
                    <small>
                      {dockerEngineRuntime()?.detail ??
                        "The Agent Computer needs a local Docker Engine. The package recommends Docker but does not start a container until you open Computer View."}
                    </small>
                  </div>
                  <code>
                    {dockerEngineRuntime()?.install_command ??
                      "sudo apt update && sudo apt install -y docker.io && sudo usermod -aG docker $USER && sudo systemctl enable --now docker"}
                  </code>
                  <div className="runtime-install-actions">
                    <button
                      type="button"
                      onClick={() =>
                        void copyRuntimeInstallCommand(
                          dockerEngineRuntime()?.install_command ??
                            "sudo apt update && sudo apt install -y docker.io && sudo usermod -aG docker $USER && sudo systemctl enable --now docker",
                        )
                      }
                    >
                      {runtimeCommandCopied ? "Copied" : "Copy command"}
                    </button>
                  </div>
                  {runtimeInstallError && <p>{runtimeInstallError}</p>}
                </div>
              )}
              {!isLinuxHost(data) && !colimaRuntime()?.available && (
                <div className="runtime-install-card compact">
                  <div>
                    <strong>Install the recommended runtime</strong>
                    <small>{colimaRuntime()?.detail ?? "Colima and the Docker CLI are not installed."}</small>
                  </div>
                  <code>{colimaRuntime()?.install_command ?? "brew install colima docker"}</code>
                  <div className="runtime-install-actions">
                    {colimaRuntime()?.installable && (
                      <button
                        type="button"
                        className="primary-button"
                        disabled={runtimeInstallBusy}
                        onClick={() => void installColima()}
                      >
                        {runtimeInstallBusy ? "Installing…" : "Install Colima"}
                      </button>
                    )}
                    <button type="button" onClick={() => void copyColimaInstallCommand()}>
                      {runtimeCommandCopied ? "Copied" : "Copy command"}
                    </button>
                  </div>
                  {runtimeInstallError && <p>{runtimeInstallError}</p>}
                </div>
              )}
              <p className="field-note">
                {isLinuxHost(data)
                  ? "On Linux, Docker Engine is the recommended Agent Computer runtime. It starts lazily when Computer View or an approved desktop task needs it."
                  : "Colima starts lazily when Agent Computer is requested. Runtime changes apply after restarting the local controller. Apple Container is discovery-only until its adapter passes the full Chromium/Xfce/Takeover test."}
              </p>
              <details className="settings-advanced-section">
                <summary>Advanced computer routing</summary>
                <label>
                  Remote worker URL
                  <input
                    type="url"
                    defaultValue={preferences.computer?.remote_url ?? ""}
                    onBlur={(event) =>
                      void patchPreferences({
                        computer: { remote_url: event.target.value },
                      })
                    }
                    placeholder="https://computer.your-tailnet.ts.net"
                  />
                </label>
                <p className="field-note">
                  Leave blank for the local Colima computer. A remote worker
                  runs on another Mac or Linux server and stays behind private
                  Tailscale access. Its bearer token is read from
                  <code>OPENAGENTFLEET_COMPUTER_REMOTE_TOKEN</code> on the controller;
                  OpenAgentFleet never stores that token here. Restart the local
                  controller after changing the URL.
                </p>
              </details>
            </section>
            {searchConnectorAvailability !== "absent" && (
              <section
                className="settings-search-connectors"
                aria-labelledby="settings-search-connectors-title"
              >
                <div className="settings-connector-heading">
                  <div>
                    <div className="eyebrow">Independent routes</div>
                    <h3 id="settings-search-connectors-title">Web Search</h3>
                  </div>
                  <span className="connector-status on">Native on</span>
                </div>
                <p>
                  Optional connectors remain separate from lead-native search.
                  Enabling one here makes it available globally; it never adds
                  an MCP ID to existing Agents. This app never collects or
                  stores raw credentials.
                </p>
                <SearchConnectorCards
                  availability={searchConnectorAvailability}
                  connectors={searchConnectors}
                  busy={searchConnectorBusy}
                  error={searchConnectorsError}
                  onToggle={(connector, enabled) =>
                    void patchSearchConnector(connector, enabled, "settings")
                  }
                />
              </section>
            )}
            <section>
              <h3>Retention</h3>
              <label className="toggle-row">
                <span>Retain transcripts</span>
                <input
                  type="checkbox"
                  checked={preferences.safety?.retain_transcripts ?? false}
                  onChange={(event) =>
                    void patchPreferences({
                      safety: { retain_transcripts: event.target.checked },
                    })
                  }
                />
              </label>
              <label className="toggle-row">
                <span>Retain computer activity</span>
                <input
                  type="checkbox"
                  checked={preferences.safety?.retain_activity ?? false}
                  onChange={(event) =>
                    void patchPreferences({
                      safety: { retain_activity: event.target.checked },
                    })
                  }
                />
              </label>
            </section>
            <section className="optional-systems" aria-labelledby="optional-systems-title">
              <div className="optional-systems-heading">
                <div>
                  <div className="eyebrow">Explicit opt-in</div>
                  <h3 id="optional-systems-title">Optional systems</h3>
                </div>
                <span className="alpha-badge">Experimental</span>
              </div>
              <p>
                New authority stays off after install and upgrades. Enable only
                the systems this {hostDeviceName(data)} should expose.
              </p>
              <div className="optional-system-list">
                {([
                  ["lead_worker_runtime", "Lead → worker runtime", "A lead harness may delegate bounded work."],
                  ["worker_isolation", "Per-session isolation", "Run workers in isolated environments."],
                  ["routines", "Routines", "Experimental: the durable schedule schema is ready; scheduler controls are not wired yet."],
                  ["heartbeat", "Heartbeat", "Experimental: wake enabled routines on a schedule once Routines is implemented."],
                  ["remote_nodes", "Remote nodes", "Experimental: pair private Mac, iPhone, and Android nodes; runtime node selection is not wired yet."],
                  ["remote_control", "Remote control", "Experimental: grant short, revocable control leases after node routing is connected."],
                  ["extensions", "Plugins & connectors", "Experimental: lifecycle settings only; plugin installation/runtime is not wired yet."],
                  ["research_runs", "Research runs", "Run bounded, source-backed investigations."],
                  ["memory_proposals", "Memory proposals", "Experimental: reviewable proposal storage is planned; automatic agent proposals are not wired yet."],
                  ["skill_learning", "Skill learning", "Enable Teach a task and keep generated workflow drafts behind review."],
                  ["native_mac_worker", "Native Mac worker", "Let selected work run outside a container."],
                  ["existing_browser_profile", "Existing browser profile", "Allow an explicitly selected signed-in profile."],
                  ["multiple_conversations", "Multiple chats per agent", "Show advanced chat creation and switching. Memory stays shared by the agent."],
                ] as const).map(([key, label, detail]) => {
                  const dependencyMissing =
                    (key === "heartbeat" && !preferences.features?.routines) ||
                    (key === "remote_control" && !preferences.features?.remote_nodes);
                  return (
                    <label className="optional-system-row" key={key}>
                      <span>
                        <strong>{label}</strong>
                        <small>{detail}</small>
                      </span>
                      <input
                        type="checkbox"
                        checked={preferences.features?.[key] ?? false}
                        disabled={dependencyMissing}
                        onChange={(event) =>
                          patchOptionalFeature(key, event.target.checked)
                        }
                      />
                    </label>
                  );
                })}
              </div>
            </section>
            <section
              className="mobile-access-settings"
              aria-labelledby="mobile-access-title"
            >
              <div className="mobile-access-heading">
                <div>
                  <div className="eyebrow">Private Tailnet connection</div>
                  <h3 id="mobile-access-title">Mobile remote access</h3>
                </div>
                <span className="alpha-badge">Alpha</span>
              </div>
              <p>
                Pair an iPhone or Android phone over your private Tailnet.
                OpenAgentFleet never uses a public tunnel for this connection.
              </p>
              <label>
                Tailnet HTTPS endpoint
                <input
                  value={mobileEndpoint}
                  onChange={(event) => setMobileEndpoint(event.target.value)}
                  placeholder="https://mac.tailnet.ts.net"
                  inputMode="url"
                  autoCapitalize="none"
                  autoCorrect="off"
                  spellCheck={false}
                />
              </label>
              <p className="field-note">
                Enter this {hostDeviceName(data)}&apos;s Tailscale Serve address. It is used only
                to compose the pairing bundle and is not saved as a preference.
              </p>
              <div className="mobile-pairing-form">
                <label>
                  Phone access
                  <select
                    value={mobileScopeProfile}
                    onChange={(event) =>
                      setMobileScopeProfile(
                        event.target.value as MobileScopeProfile,
                      )
                    }
                  >
                    <option value="observer">Observer — view status only</option>
                    <option value="controller">
                      Controller — chat
                    </option>
                  </select>
                </label>
                <button
                  className="primary-button mobile-pair-button"
                  type="button"
                  onClick={() => void createMobilePairing()}
                  disabled={mobilePairingBusy || !mobileEndpoint.trim()}
                >
                  {mobilePairingBusy ? "Creating…" : "Create pairing"}
                </button>
              </div>
              {mobileDevicesError && (
                <div className="mobile-access-error" role="alert">
                  <span>{mobileDevicesError}</span>
                  <button
                    type="button"
                    onClick={() => void loadMobileDevices()}
                    disabled={mobileDevicesLoading}
                  >
                    Retry
                  </button>
                </div>
              )}
              {mobilePairingBundle && (
                <div className="pairing-bundle" aria-live="polite">
                  <div className="pairing-bundle-heading">
                    <div>
                      <strong>Pairing bundle ready</strong>
                      <span>Expires in {mobilePairingTimer}</span>
                    </div>
                    <span className="bundle-live-dot" aria-hidden="true" />
                  </div>
                  <textarea
                    aria-label={`Pairing bundle for grant ${mobilePairingBundle.grantID}`}
                    readOnly
                    value={mobilePairingBundle.text}
                    onFocus={(event) => event.currentTarget.select()}
                  />
                  <div className="pairing-bundle-actions">
                    <button
                      type="button"
                      className="quiet-button"
                      onClick={() => void copyMobilePairingBundle()}
                    >
                      {mobileBundleCopied ? "Copied" : "Copy bundle"}
                    </button>
                    <span>
                      Transfer it only to the phone you intend to pair. It is
                      cleared when Settings closes.
                    </span>
                  </div>
                </div>
              )}
              {mobilePairingExpired && !mobilePairingBundle && (
                <div className="pairing-expired" role="status">
                  The pairing bundle expired and was cleared. Create a new one
                  when the phone is ready.
                </div>
              )}
              <div className="mobile-devices-heading">
                <div>
                  <h4>Paired phones</h4>
                  <span>Each phone can be revoked independently.</span>
                </div>
                <button
                  type="button"
                  className="text-button"
                  onClick={() => void loadMobileDevices()}
                  disabled={mobileDevicesLoading}
                >
                  {mobileDevicesLoading ? "Refreshing…" : "Refresh"}
                </button>
              </div>
              {mobileDevicesLoading && mobileDevices.length === 0 ? (
                <p className="mobile-devices-empty" role="status">
                  Loading paired phones…
                </p>
              ) : mobileDevices.length === 0 ? (
                <p className="mobile-devices-empty">
                  No phone has been paired yet.
                </p>
              ) : (
                <div className="mobile-device-list">
                  {mobileDevices.map((device) => {
                    const isRevoked =
                      device.status.toLowerCase() === "revoked" ||
                      Boolean(device.revoked_at);
                    return (
                      <article className="mobile-device" key={device.id}>
                        <div className="mobile-device-icon" aria-hidden="true">
                          {device.platform.toLowerCase() === "android"
                            ? "◫"
                            : "▯"}
                        </div>
                        <div className="mobile-device-copy">
                          <div>
                            <strong>{device.display_name}</strong>
                            <span
                              className={`mobile-device-status ${isRevoked ? "revoked" : "active"}`}
                            >
                              {isRevoked ? "Revoked" : device.status}
                            </span>
                          </div>
                          <span>
                            {device.platform} · {device.scope_profile} · Last
                            used {formatRelativeDate(device.last_used_at, now)}
                          </span>
                        </div>
                        {isRevoked ? (
                          <span className="device-revoked-label">Revoked</span>
                        ) : (
                          <button
                            type="button"
                            className="device-revoke-button"
                            onClick={() => setMobileRevokeCandidateID(device.id)}
                          >
                            Revoke
                          </button>
                        )}
                      </article>
                    );
                  })}
                </div>
              )}
              {pendingMobileRevoke && (
                <div className="mobile-revoke-confirm" role="alert">
                  <div>
                    <strong>Revoke {pendingMobileRevoke.display_name}?</strong>
                    <span>
                      This phone will lose access immediately and must be paired
                      again.
                    </span>
                  </div>
                  <div>
                    <button
                      type="button"
                      onClick={() => setMobileRevokeCandidateID(null)}
                    >
                      Keep access
                    </button>
                    <button
                      type="button"
                      className="confirm-revoke-button"
                      onClick={() => void revokeMobileDevice(pendingMobileRevoke)}
                      disabled={mobileRevokingID === pendingMobileRevoke.id}
                    >
                      {mobileRevokingID === pendingMobileRevoke.id
                        ? "Revoking…"
                        : "Revoke access"}
                    </button>
                  </div>
                </div>
              )}
            </section>
            <section className="memory-settings" aria-labelledby="memory-title">
              <div className="memory-heading">
                <div>
                  <div className="eyebrow">Reviewable context</div>
                  <h3 id="memory-title">Bot memory</h3>
                </div>
                <div className="memory-heading-actions">
                  <button
                    type="button"
                    className="text-button"
                    onClick={() => void loadMemories()}
                    disabled={memoriesLoading || !apiReady || !memoryBotID}
                  >
                    {memoriesLoading ? "Refreshing…" : "Refresh"}
                  </button>
                  <button
                    type="button"
                    className="quiet-button"
                    onClick={() => openMemoryEditor()}
                    disabled={memoriesUnavailable || !apiReady || !memoryBotID}
                  >
                    Add memory
                  </button>
                </div>
              </div>
              <p>
                Memories are visible, reviewable and removable. They are not
                silently written from this panel.
              </p>
              {data && data.bots.length > 1 && (
                <label>
                  Bot
                  <select
                    value={memoryBotID}
                    onChange={(event) => changeMemoryBot(event.target.value)}
                  >
                    {data.bots.map((item) => (
                      <option key={item.id} value={item.id}>
                        {item.name}
                      </option>
                    ))}
                  </select>
                </label>
              )}
              {memoryBot && (
                <p className="field-note">Reviewing memory for {memoryBot.name}.</p>
              )}
              {memoriesUnavailable ? (
                <div className="memory-unavailable" role="status">
                  <span>
                    Bot memory is not available from this local service yet.
                  </span>
                  <button
                    type="button"
                    className="text-button"
                    onClick={() => void loadMemories()}
                    disabled={memoriesLoading || !apiReady}
                  >
                    Retry
                  </button>
                </div>
              ) : (
                <>
                  {memoriesError && (
                    <div className="memory-error" role="alert">
                      <span>{memoriesError}</span>
                      <button
                        type="button"
                        className="text-button"
                        onClick={() => void loadMemories()}
                        disabled={memoriesLoading || !apiReady}
                      >
                        Retry
                      </button>
                    </div>
                  )}
                  {memoryDraft && (
                    <form className="memory-editor" onSubmit={saveMemory}>
                      <div className="memory-editor-heading">
                        <strong>
                          {editingMemoryID ? "Edit memory" : "Add memory"}
                        </strong>
                        <button
                          type="button"
                          className="text-button"
                          onClick={() => {
                            setMemoryDraft(null);
                            setEditingMemoryID(null);
                          }}
                          disabled={memoryBusyID !== null}
                        >
                          Cancel
                        </button>
                      </div>
                      <div className="memory-editor-fields">
                        <label>
                          Category
                          <select
                            value={memoryDraft.category}
                            onChange={(event) =>
                              setMemoryDraft({
                                ...memoryDraft,
                                category: event.target.value as MemoryCategory,
                              })
                            }
                          >
                            <option value="fact">Fact</option>
                            <option value="preference">Preference</option>
                            <option value="instruction">Instruction</option>
                            <option value="project">Project</option>
                          </select>
                        </label>
                        <label>
                          Priority
                          <input
                            type="number"
                            min="1"
                            max="5"
                            step="1"
                            value={memoryDraft.priority}
                            onChange={(event) =>
                              setMemoryDraft({
                                ...memoryDraft,
                                priority: event.target.value,
                              })
                            }
                          />
                        </label>
                      </div>
                      <label>
                        Content
                        <textarea
                          value={memoryDraft.content}
                          onChange={(event) =>
                            setMemoryDraft({
                              ...memoryDraft,
                              content: event.target.value,
                            })
                          }
                          rows={4}
                        />
                      </label>
                      <label>
                        Expires on
                        <input
                          type="date"
                          value={memoryDraft.expires_at}
                          onChange={(event) =>
                            setMemoryDraft({
                              ...memoryDraft,
                              expires_at: event.target.value,
                            })
                          }
                        />
                      </label>
                      <div className="memory-editor-actions">
                        <button
                          className="primary-button"
                          type="submit"
                          disabled={
                            !memoryDraft.content.trim() || memoryBusyID !== null
                          }
                        >
                          {memoryBusyID === (editingMemoryID ?? "new")
                            ? "Saving…"
                            : editingMemoryID
                              ? "Save changes"
                              : "Add memory"}
                        </button>
                      </div>
                    </form>
                  )}
                  {memoriesLoading && selectedMemories.length === 0 ? (
                    <p className="memory-empty" role="status">
                      Loading bot memory…
                    </p>
                  ) : selectedMemories.length === 0 ? (
                    <p className="memory-empty">No memory has been added.</p>
                  ) : (
                    <div className="memory-list" aria-live="polite">
                      {selectedMemories.map((memory) => {
                        const busy = memoryBusyID === memory.id;
                        const archived = memory.status === "archived";
                        return (
                          <article
                            className={`memory-item ${archived ? "archived" : ""}`}
                            key={memory.id}
                          >
                            <div className="memory-item-heading">
                              <span className="memory-category">{memory.category}</span>
                              <span className="memory-priority">
                                Priority {memory.priority}
                              </span>
                            </div>
                            <p>{memory.content}</p>
                            <div className="memory-meta">
                              <span>{memory.source === "agent_proposal" ? "Agent proposal" : "User"}</span>
                              <span>{archived ? "Archived" : "Approved"}</span>
                              {memory.expires_at && (
                                <span>
                                  Expires {formatRelativeDate(memory.expires_at, now)}
                                </span>
                              )}
                            </div>
                            <div className="memory-actions">
                              <button
                                type="button"
                                className="text-button"
                                onClick={() => openMemoryEditor(memory)}
                                disabled={busy}
                              >
                                Edit
                              </button>
                              <button
                                type="button"
                                className="text-button"
                                onClick={() =>
                                  void patchMemory(memory, {
                                    status: archived ? "approved" : "archived",
                                  })
                                }
                                disabled={busy}
                              >
                                {busy
                                  ? "Updating…"
                                  : archived
                                    ? "Restore"
                                    : "Archive"}
                              </button>
                              <button
                                type="button"
                                className="memory-delete-button"
                                onClick={() => setMemoryDeleteCandidateID(memory.id)}
                                disabled={busy}
                              >
                                Delete
                              </button>
                            </div>
                          </article>
                        );
                      })}
                    </div>
                  )}
                  {memoryDeleteCandidate && (
                    <div className="memory-delete-confirm" role="alert">
                      <div>
                        <strong>Delete this memory?</strong>
                        <span>This cannot be undone.</span>
                      </div>
                      <div>
                        <button
                          type="button"
                          onClick={() => setMemoryDeleteCandidateID(null)}
                          disabled={memoryBusyID !== null}
                        >
                          Cancel
                        </button>
                        <button
                          type="button"
                          className="memory-delete-button"
                          onClick={() => void deleteMemory(memoryDeleteCandidate)}
                          disabled={memoryBusyID === memoryDeleteCandidate.id}
                        >
                          {memoryBusyID === memoryDeleteCandidate.id
                            ? "Deleting…"
                            : "Delete memory"}
                        </button>
                      </div>
                    </div>
                  )}
                </>
              )}
            </section>
            <p className="settings-note">
              Computer takeover and agent control can only be enabled manually
              in the live computer.
            </p>
          </section>
        </div>
      )}
    </main>
  );
}

export default App;
