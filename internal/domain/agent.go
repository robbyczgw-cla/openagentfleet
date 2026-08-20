package domain

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	MaxAgentNameBytes               = 128
	MaxAgentTitleBytes              = 256
	MaxAgentDescriptionBytes        = 4 << 10
	MaxAgentConversationTitleBytes  = 256
	MaxAgentMetadataIdentifierBytes = 128
	MaxAgentMetadataIdentifiers     = 64
	MaxAgentExecutionProfiles       = 32
	MaxAgentExecutionTurns          = 100
	DefaultAgentWorkerMaxTurns      = 12
	MinAgentExecutionTimeoutSeconds = 30
	MaxAgentExecutionTimeoutSeconds = 3600
	DefaultAgentWorkerTimeout       = 900
	MaxAgentAvatarEmojiBytes        = 32
	MaxAgentAvatarURLBytes          = 2048

	DefaultAgentCollaborationMaxDepth           uint8  = 2
	MaxAgentCollaborationMaxDepth               uint8  = 4
	DefaultAgentCollaborationMaxActivePeerTasks uint8  = 2
	MaxAgentCollaborationMaxActivePeerTasks     uint8  = 4
	DefaultAgentCollaborationTimeoutSeconds     uint32 = 300
	MaxAgentCollaborationTimeoutSeconds         uint32 = 3600

	AgentMetadataPersisted = "persisted"
	// AgentMetadataNotPersisted is retained for older clients that observed the
	// initial request-only metadata prototype.
	AgentMetadataNotPersisted = "not_persisted_without_schema"

	AgentWebSearchLive     = "live"
	AgentWebSearchDisabled = "disabled"
)

type AgentConversationMode string

const (
	AgentConversationModeSingle        AgentConversationMode = "single"
	AgentConversationModeAdvancedMulti AgentConversationMode = "advanced_multi"
)

// Agent is the Agent-facing view of a bot. Conversation is the canonical
// default: the earliest durable conversation for the bot. Conversations is
// always lossless, including legacy bots that already have several threads.
type Agent struct {
	Bot                 Bot                   `json:"bot"`
	Conversation        *Conversation         `json:"conversation"`
	Conversations       []Conversation        `json:"conversations"`
	ConversationMode    AgentConversationMode `json:"conversation_mode"`
	Metadata            *AgentMetadata        `json:"metadata,omitempty"`
	MetadataPersistence string                `json:"metadata_persistence"`
}

// AgentDraft is the durable portion of an Agent creation request. Its first
// conversation is created in the same transaction as the bot.
type AgentDraft struct {
	Name              string
	Title             string
	Description       string
	ConversationTitle string
}

type AgentProfile struct {
	Name        string
	Title       string
	Description string
}

// AgentProfileUpdate is an optional-field profile patch. Configuration is
// intentionally separate because the current schema only persists profiles.
type AgentProfileUpdate struct {
	Name        *string
	Title       *string
	Description *string
}

// AgentMetadata is durable Agent configuration. Identifier lists are explicit
// so the runtime never infers worker/plugin/MCP authority from installed tools.
type AgentMetadata struct {
	Lead    *AgentExecutionProfile  `json:"lead,omitempty"`
	Workers []AgentExecutionProfile `json:"workers,omitempty"`
	// LeadHarness, Model and WorkerIDs are retained as a read/write migration
	// bridge for clients created before structured execution profiles existed.
	// NormalizeAgentMetadata copies them into Lead and Workers; new clients
	// should use only the structured fields.
	LeadHarness      string               `json:"lead_harness,omitempty"`
	Model            string               `json:"model,omitempty"`
	Orchestrator     string               `json:"orchestrator,omitempty"`
	WorkerIDs        []string             `json:"worker_ids,omitempty"`
	PluginIDs        []string             `json:"plugin_ids,omitempty"`
	MCPIDs           []string             `json:"mcp_ids,omitempty"`
	NotifyFinished   bool                 `json:"notify_finished"`
	NotifyNeedsInput bool                 `json:"notify_needs_input"`
	Avatar           *AgentAvatarMetadata `json:"avatar,omitempty"`
	Collaboration    *AgentCollaboration  `json:"collaboration,omitempty"`
}

type AgentCollaboration struct {
	Enabled            bool     `json:"enabled"`
	AllowAgentIDs      []string `json:"allow_agent_ids,omitempty"`
	MaxDepth           uint8    `json:"max_depth,omitempty"`
	MaxActivePeerTasks uint8    `json:"max_active_peer_tasks,omitempty"`
	TimeoutSeconds     uint32   `json:"timeout_seconds,omitempty"`
}

// AgentExecutionProfile pins every execution choice that otherwise tends to
// fall back to a provider default. The lead accepts Grok Build, Codex App
// Server, or OpenCode; worker profiles accept only the six bounded adapters.
type AgentExecutionProfile struct {
	ID             string `json:"id,omitempty"`
	Harness        string `json:"harness"`
	Model          string `json:"model,omitempty"`
	Reasoning      string `json:"reasoning"`
	ServiceTier    string `json:"service_tier"`
	Permission     string `json:"permission"`
	WebSearch      string `json:"web_search,omitempty"`
	MaxTurns       uint16 `json:"max_turns,omitempty"`
	TimeoutSeconds uint32 `json:"timeout_seconds,omitempty"`
}

// AgentAvatarMetadata permits presentation-only emoji or HTTPS artwork. It
// does not accept local paths, file URLs, or binary uploads.
type AgentAvatarMetadata struct {
	Emoji string `json:"emoji,omitempty"`
	URL   string `json:"url,omitempty"`
}

func DefaultAgentMetadata() AgentMetadata {
	return AgentMetadata{NotifyFinished: true, NotifyNeedsInput: true}
}

func NormalizeAgentDraft(value AgentDraft) (AgentDraft, error) {
	profile, err := NormalizeAgentProfile(value.Name, value.Title, value.Description)
	if err != nil {
		return AgentDraft{}, err
	}
	value.Name = profile.Name
	value.Title = profile.Title
	value.Description = profile.Description
	value.ConversationTitle = strings.TrimSpace(value.ConversationTitle)
	if value.ConversationTitle == "" {
		value.ConversationTitle = value.Title
	}
	if err := validateAgentText("conversation title", value.ConversationTitle, MaxAgentConversationTitleBytes, true); err != nil {
		return AgentDraft{}, err
	}
	return value, nil
}

func NormalizeAgentProfile(name, title, description string) (AgentProfile, error) {
	value := AgentProfile{Name: strings.TrimSpace(name), Title: strings.TrimSpace(title), Description: strings.TrimSpace(description)}
	if err := validateAgentText("agent name", value.Name, MaxAgentNameBytes, true); err != nil {
		return AgentProfile{}, err
	}
	if err := validateAgentText("agent title", value.Title, MaxAgentTitleBytes, true); err != nil {
		return AgentProfile{}, err
	}
	if err := validateAgentText("agent description", value.Description, MaxAgentDescriptionBytes, false); err != nil {
		return AgentProfile{}, err
	}
	return value, nil
}

func NormalizeAgentMetadata(value AgentMetadata) (AgentMetadata, error) {
	var err error
	if value.LeadHarness, err = normalizeAgentMetadataIdentifier("lead harness", value.LeadHarness, false); err != nil {
		return AgentMetadata{}, err
	}
	if value.Model, err = normalizeAgentMetadataIdentifier("model", value.Model, false); err != nil {
		return AgentMetadata{}, err
	}
	if value.Orchestrator, err = normalizeAgentMetadataIdentifier("orchestrator", value.Orchestrator, false); err != nil {
		return AgentMetadata{}, err
	}
	if value.WorkerIDs, err = normalizeAgentMetadataIdentifiers("worker ids", value.WorkerIDs); err != nil {
		return AgentMetadata{}, err
	}
	if value.Lead == nil {
		switch value.LeadHarness {
		case "grok":
			// The first Agent Builder called Grok Build simply "grok". Keep
			// existing profiles loadable while writing the canonical ID.
			value.Lead = &AgentExecutionProfile{Harness: "grok_build", Model: value.Model}
		case "grok_build", "codex_app_server", "opencode", "pi":
			value.Lead = &AgentExecutionProfile{Harness: value.LeadHarness, Model: value.Model}
		}
	}
	if value.Lead != nil {
		lead, normalizeErr := normalizeAgentExecutionProfile("lead", *value.Lead, true)
		if normalizeErr != nil {
			return AgentMetadata{}, normalizeErr
		}
		value.Lead = &lead
		value.LeadHarness = lead.Harness
		value.Model = lead.Model
	}
	// WorkerIDs used to be free-form installed-worker identifiers, not harness
	// names. Preserve them for compatibility but never reinterpret them as
	// execution authority. Only structured Workers are executable profiles.
	if len(value.Workers) > MaxAgentExecutionProfiles {
		return AgentMetadata{}, fmt.Errorf("workers must contain at most %d profiles", MaxAgentExecutionProfiles)
	}
	workerIDs := make(map[string]struct{}, len(value.Workers))
	for index := range value.Workers {
		worker, normalizeErr := normalizeAgentExecutionProfile("worker", value.Workers[index], false)
		if normalizeErr != nil {
			return AgentMetadata{}, fmt.Errorf("workers[%d]: %w", index, normalizeErr)
		}
		if worker.ID == "" {
			worker.ID = fmt.Sprintf("worker-%d", index+1)
		}
		if _, duplicate := workerIDs[worker.ID]; duplicate {
			return AgentMetadata{}, fmt.Errorf("workers must not contain duplicate id %q", worker.ID)
		}
		workerIDs[worker.ID] = struct{}{}
		value.Workers[index] = worker
	}
	if value.PluginIDs, err = normalizeAgentMetadataIdentifiers("plugin ids", value.PluginIDs); err != nil {
		return AgentMetadata{}, err
	}
	if value.MCPIDs, err = normalizeAgentMetadataIdentifiers("MCP ids", value.MCPIDs); err != nil {
		return AgentMetadata{}, err
	}
	if value.Avatar != nil {
		avatar, err := NormalizeAgentAvatarMetadata(*value.Avatar)
		if err != nil {
			return AgentMetadata{}, err
		}
		value.Avatar = &avatar
	}
	if value.Collaboration != nil {
		collaboration, err := NormalizeAgentCollaboration(*value.Collaboration)
		if err != nil {
			return AgentMetadata{}, err
		}
		value.Collaboration = &collaboration
	}
	return value, nil
}

func NormalizeAgentCollaboration(value AgentCollaboration) (AgentCollaboration, error) {
	var err error
	if value.AllowAgentIDs, err = normalizeAgentMetadataIdentifiers("collaboration allow agent ids", value.AllowAgentIDs); err != nil {
		return AgentCollaboration{}, err
	}
	value.MaxDepth = clampCollaborationBound(value.MaxDepth, DefaultAgentCollaborationMaxDepth, MaxAgentCollaborationMaxDepth)
	value.MaxActivePeerTasks = clampCollaborationBound(value.MaxActivePeerTasks, DefaultAgentCollaborationMaxActivePeerTasks, MaxAgentCollaborationMaxActivePeerTasks)
	if value.TimeoutSeconds == 0 {
		value.TimeoutSeconds = DefaultAgentCollaborationTimeoutSeconds
	} else if value.TimeoutSeconds > MaxAgentCollaborationTimeoutSeconds {
		value.TimeoutSeconds = MaxAgentCollaborationTimeoutSeconds
	}
	return value, nil
}

func clampCollaborationBound(value, fallback, maximum uint8) uint8 {
	if value == 0 {
		return fallback
	}
	if value > maximum {
		return maximum
	}
	return value
}

func (value AgentCollaboration) EffectiveMaxDepth() uint8 {
	return clampCollaborationBound(value.MaxDepth, DefaultAgentCollaborationMaxDepth, MaxAgentCollaborationMaxDepth)
}

func (value AgentCollaboration) EffectiveMaxActivePeerTasks() uint8 {
	return clampCollaborationBound(value.MaxActivePeerTasks, DefaultAgentCollaborationMaxActivePeerTasks, MaxAgentCollaborationMaxActivePeerTasks)
}

func (value AgentCollaboration) EffectiveTimeoutSeconds() uint32 {
	if value.TimeoutSeconds == 0 {
		return DefaultAgentCollaborationTimeoutSeconds
	}
	if value.TimeoutSeconds > MaxAgentCollaborationTimeoutSeconds {
		return MaxAgentCollaborationTimeoutSeconds
	}
	return value.TimeoutSeconds
}

func normalizeAgentExecutionProfile(label string, value AgentExecutionProfile, lead bool) (AgentExecutionProfile, error) {
	var err error
	if value.ID, err = normalizeAgentMetadataIdentifier(label+" id", value.ID, false); err != nil {
		return AgentExecutionProfile{}, err
	}
	if value.Harness, err = normalizeAgentMetadataIdentifier(label+" harness", value.Harness, true); err != nil {
		return AgentExecutionProfile{}, err
	}
	if value.Model, err = normalizeAgentMetadataIdentifier(label+" model", value.Model, false); err != nil {
		return AgentExecutionProfile{}, err
	}
	if lead {
		if value.Harness != "grok_build" && value.Harness != "codex_app_server" && value.Harness != "opencode" && value.Harness != "pi" {
			return AgentExecutionProfile{}, fmt.Errorf("lead harness must be grok_build, codex_app_server, opencode, or pi")
		}
	} else {
		switch value.Harness {
		case "pi", "claude", "codex", "grok", "opencode", "cursor":
		default:
			return AgentExecutionProfile{}, fmt.Errorf("worker harness %q is not supported", value.Harness)
		}
	}
	value.Reasoning = strings.TrimSpace(value.Reasoning)
	if value.Reasoning == "" {
		value.Reasoning = "default"
	}
	switch value.Reasoning {
	case "default", "low", "medium", "high", "xhigh", "max":
	default:
		return AgentExecutionProfile{}, fmt.Errorf("%s reasoning %q is not supported", label, value.Reasoning)
	}
	value.ServiceTier = strings.TrimSpace(value.ServiceTier)
	if value.ServiceTier == "" {
		value.ServiceTier = "default"
	}
	switch value.ServiceTier {
	case "default", "priority", "flex":
	default:
		return AgentExecutionProfile{}, fmt.Errorf("%s service tier %q is not supported", label, value.ServiceTier)
	}
	value.Permission = strings.TrimSpace(value.Permission)
	if value.Permission == "" {
		if lead && value.Harness == "pi" {
			value.Permission = "workspace"
		} else {
			value.Permission = "ask"
		}
	}
	switch value.Permission {
	case "ask", "read_only", "workspace", "provider_default":
	default:
		return AgentExecutionProfile{}, fmt.Errorf("%s permission %q is not supported", label, value.Permission)
	}
	value.WebSearch = strings.TrimSpace(value.WebSearch)
	if lead {
		if value.WebSearch == "" {
			value.WebSearch = AgentWebSearchLive
		}
		switch value.WebSearch {
		case AgentWebSearchLive, AgentWebSearchDisabled:
		default:
			return AgentExecutionProfile{}, fmt.Errorf("lead web_search %q is not supported; use live or disabled", value.WebSearch)
		}
	} else if value.WebSearch != "" {
		return AgentExecutionProfile{}, fmt.Errorf("worker web_search is not supported; configure it on the lead")
	}
	if value.MaxTurns > MaxAgentExecutionTurns {
		return AgentExecutionProfile{}, fmt.Errorf("%s max turns must be at most %d", label, MaxAgentExecutionTurns)
	}
	if lead && value.MaxTurns != 0 {
		return AgentExecutionProfile{}, fmt.Errorf("lead max turns are not supported; configure turn bounds on delegated workers")
	}
	if !lead && value.MaxTurns == 0 {
		value.MaxTurns = DefaultAgentWorkerMaxTurns
	}
	if value.TimeoutSeconds != 0 && (value.TimeoutSeconds < MinAgentExecutionTimeoutSeconds || value.TimeoutSeconds > MaxAgentExecutionTimeoutSeconds) {
		return AgentExecutionProfile{}, fmt.Errorf("%s timeout must be between %d and %d seconds", label, MinAgentExecutionTimeoutSeconds, MaxAgentExecutionTimeoutSeconds)
	}
	if !lead && value.TimeoutSeconds == 0 {
		value.TimeoutSeconds = DefaultAgentWorkerTimeout
	}
	if lead && value.Harness == "grok_build" && value.ServiceTier != "default" {
		return AgentExecutionProfile{}, fmt.Errorf("Grok Build lead service tier must be default because ACP exposes no service-tier control")
	}
	if lead && value.Harness == "opencode" {
		if value.ServiceTier != "default" {
			return AgentExecutionProfile{}, fmt.Errorf("OpenCode lead service tier must be default because opencode run exposes no service-tier control")
		}
		// Migrate the early preview value without pretending OpenFleet owns an
		// approval channel that OpenCode's headless runner does not expose.
		if value.Permission == "ask" {
			value.Permission = "provider_default"
		}
		if value.Permission != "provider_default" {
			return AgentExecutionProfile{}, fmt.Errorf("OpenCode lead permission must be provider_default because opencode run has no OpenFleet approval callback")
		}
		if value.Model != "" && !validOpenCodeModel(value.Model) {
			return AgentExecutionProfile{}, fmt.Errorf("OpenCode lead model must use provider/model format")
		}
	}
	if lead && value.Harness == "pi" {
		if value.ServiceTier != "default" {
			return AgentExecutionProfile{}, fmt.Errorf("Pi lead service tier must be default because pi --mode rpc exposes no service-tier control")
		}
		switch value.Permission {
		case "read_only", "workspace", "ask":
		default:
			return AgentExecutionProfile{}, fmt.Errorf("Pi lead permission must be read_only, workspace, or ask so --tools can enforce the sandbox")
		}
		// Pi has no native search and no MCP injection. Do not keep a live
		// search flag that the adapter cannot honor.
		if value.WebSearch == AgentWebSearchLive {
			value.WebSearch = AgentWebSearchDisabled
		}
	}
	return value, nil
}

func validOpenCodeModel(value string) bool {
	provider, model, found := strings.Cut(value, "/")
	return found && provider != "" && model != "" &&
		!strings.ContainsAny(provider, " \t\r\n") && !strings.ContainsAny(model, " \t\r\n")
}

func NormalizeAgentAvatarMetadata(value AgentAvatarMetadata) (AgentAvatarMetadata, error) {
	value.Emoji = strings.TrimSpace(value.Emoji)
	value.URL = strings.TrimSpace(value.URL)
	if value.Emoji == "" && value.URL == "" {
		return AgentAvatarMetadata{}, fmt.Errorf("avatar must include emoji or HTTPS URL")
	}
	if value.Emoji != "" && value.URL != "" {
		return AgentAvatarMetadata{}, fmt.Errorf("avatar must include either emoji or HTTPS URL, not both")
	}
	if err := validateAgentText("avatar emoji", value.Emoji, MaxAgentAvatarEmojiBytes, false); err != nil {
		return AgentAvatarMetadata{}, err
	}
	if value.URL != "" {
		if err := validateAgentText("avatar URL", value.URL, MaxAgentAvatarURLBytes, true); err != nil {
			return AgentAvatarMetadata{}, err
		}
		parsed, err := url.Parse(value.URL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return AgentAvatarMetadata{}, fmt.Errorf("avatar URL must be an HTTPS URL without credentials")
		}
	}
	return value, nil
}

func validateAgentText(name, value string, maximum int, required bool) error {
	if required && value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", name)
	}
	if len(value) > maximum {
		return fmt.Errorf("%s must be at most %d bytes", name, maximum)
	}
	if strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%s must not contain NUL", name)
	}
	return nil
}

func normalizeAgentMetadataIdentifier(name, value string, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if err := validateAgentText(name, value, MaxAgentMetadataIdentifierBytes, required); err != nil {
		return "", err
	}
	return value, nil
}

func normalizeAgentMetadataIdentifiers(name string, values []string) ([]string, error) {
	if len(values) > MaxAgentMetadataIdentifiers {
		return nil, fmt.Errorf("%s must contain at most %d identifiers", name, MaxAgentMetadataIdentifiers)
	}
	if values == nil {
		return nil, nil
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized, err := normalizeAgentMetadataIdentifier(name, value, true)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[normalized]; exists {
			return nil, fmt.Errorf("%s must not contain duplicate identifiers", name)
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result, nil
}
