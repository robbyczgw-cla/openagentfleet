package domain

// ModelCatalogEntry is the controller-owned description of a selectable lead
// model. It deliberately contains metadata only: provider credentials and
// model responses never belong in the catalog or bootstrap payload.
//
// The shape keeps provider, billing, authentication and availability metadata
// together while remaining provider-neutral for OpenAgentFleet's Grok, Codex
// App Server and OpenCode adapters.
type ModelCatalogEntry struct {
	Harness          string   `json:"harness"`
	Provider         string   `json:"provider"`
	Model            string   `json:"model"`
	Label            string   `json:"label"`
	Detail           string   `json:"detail,omitempty"`
	Billing          string   `json:"billing,omitempty"`
	AuthMode         string   `json:"auth_mode,omitempty"`
	AuthLabel        string   `json:"auth_label,omitempty"`
	AuthState        string   `json:"auth_state"`
	Subscription     string   `json:"subscription,omitempty"`
	Available        bool     `json:"available"`
	DisabledReason   string   `json:"disabled_reason,omitempty"`
	ReasoningEfforts []string `json:"reasoning_efforts,omitempty"`
	ServiceTiers     []string `json:"service_tiers,omitempty"`
}
