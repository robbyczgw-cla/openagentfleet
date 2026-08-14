package policy

import (
	"context"
	"time"
)

const CurrentVersion = 1

type Effect string

const (
	EffectAllow Effect = "allow"
	EffectDeny  Effect = "deny"
	EffectAsk   Effect = "ask"
)

type ResourceKind string

const (
	ResourceFolder         ResourceKind = "folder"
	ResourceBrowserProfile ResourceKind = "browser_profile"
	ResourceNativeApp      ResourceKind = "native_app"
	ResourceNetwork        ResourceKind = "network"
	ResourceMCP            ResourceKind = "mcp"
	ResourceComputer       ResourceKind = "computer"
	ResourceConnector      ResourceKind = "connector"
)

type PrincipalKind string

const (
	PrincipalController PrincipalKind = "controller"
	PrincipalLead       PrincipalKind = "lead"
	PrincipalWorker     PrincipalKind = "worker"
	PrincipalRoutine    PrincipalKind = "routine"
	PrincipalMobile     PrincipalKind = "mobile"
	PrincipalPlugin     PrincipalKind = "plugin"
)

// Principal is an exact controller-owned identity. V1 intentionally has no
// all-principals wildcard.
type Principal struct {
	Kind PrincipalKind `json:"kind"`
	ID   string        `json:"id"`
}

// Resource is one exact capability target. Qualifier is used only by MCP and
// names one exact tool. An empty qualifier means the server itself and never
// all tools on that server.
type Resource struct {
	Kind      ResourceKind `json:"kind"`
	Target    string       `json:"target"`
	Qualifier string       `json:"qualifier,omitempty"`
}

type ScopeMatch string

const (
	MatchExact ScopeMatch = "exact"
	MatchTree  ScopeMatch = "tree"
)

type Scope struct {
	Resource Resource   `json:"resource"`
	Match    ScopeMatch `json:"match"`
}

// Rule always names one principal, one scope, and explicit operations. An
// expired rule is ignored.
type Rule struct {
	ID         string     `json:"id"`
	Principal  Principal  `json:"principal"`
	Effect     Effect     `json:"effect"`
	Scope      Scope      `json:"scope"`
	Operations []string   `json:"operations"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// Config is immutable after New. The zero value means v1 disabled.
type Config struct {
	Version int    `json:"version"`
	Enabled bool   `json:"enabled"`
	Rules   []Rule `json:"rules,omitempty"`
}

// Parameter values are covered by the action hash but omitted from audit
// records. They must never contain credentials or secret bytes; sensitive
// actions use an opaque handoff reference instead. Names are unique, so
// ordering cannot change the hash.
type Parameter struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Action is one proposed side effect. RunID is required so an approval cannot
// be replayed across runs.
type Action struct {
	Principal  Principal   `json:"principal"`
	RunID      string      `json:"run_id"`
	Resource   Resource    `json:"resource"`
	Operation  string      `json:"operation"`
	Parameters []Parameter `json:"parameters,omitempty"`
}

type ApprovalStatus string

const (
	ApprovalApproved ApprovalStatus = "approved"
	ApprovalDenied   ApprovalStatus = "denied"
	ApprovalRevoked  ApprovalStatus = "revoked"
)

// Approval can resolve exactly one ask rule for one action, run, and
// principal. Permanent authority belongs in reviewed rules, not approvals.
type Approval struct {
	ID         string         `json:"id"`
	ActionHash string         `json:"action_hash"`
	RuleID     string         `json:"rule_id"`
	Principal  Principal      `json:"principal"`
	RunID      string         `json:"run_id"`
	Status     ApprovalStatus `json:"status"`
	CreatedAt  time.Time      `json:"created_at"`
	ExpiresAt  time.Time      `json:"expires_at"`
}

type Reason string

const (
	ReasonDisabled         Reason = "broker_disabled"
	ReasonDefaultDeny      Reason = "default_deny"
	ReasonRuleAllow        Reason = "rule_allow"
	ReasonRuleDeny         Reason = "rule_deny"
	ReasonApprovalRequired Reason = "approval_required"
	ReasonApprovalGranted  Reason = "approval_granted"
	ReasonApprovalDenied   Reason = "approval_denied"
	ReasonInvalidAction    Reason = "invalid_action"
	ReasonInvalidApproval  Reason = "invalid_approval"
	ReasonAuditUnavailable Reason = "audit_unavailable"
)

// Decision is audit-ready and contains no action parameter values.
type Decision struct {
	Effect         Effect    `json:"effect"`
	Reason         Reason    `json:"reason"`
	ActionHash     string    `json:"action_hash,omitempty"`
	WinningRuleID  string    `json:"winning_rule_id,omitempty"`
	MatchedRuleIDs []string  `json:"matched_rule_ids,omitempty"`
	ApprovalID     string    `json:"approval_id,omitempty"`
	EvaluatedAt    time.Time `json:"evaluated_at"`
	AuditID        string    `json:"audit_id"`
}

type AuditRecord struct {
	ID             string    `json:"id"`
	EvaluatedAt    time.Time `json:"evaluated_at"`
	Principal      Principal `json:"principal"`
	RunID          string    `json:"run_id,omitempty"`
	Resource       Resource  `json:"resource"`
	Operation      string    `json:"operation,omitempty"`
	ActionHash     string    `json:"action_hash,omitempty"`
	Effect         Effect    `json:"effect"`
	Reason         Reason    `json:"reason"`
	WinningRuleID  string    `json:"winning_rule_id,omitempty"`
	MatchedRuleIDs []string  `json:"matched_rule_ids,omitempty"`
	ApprovalID     string    `json:"approval_id,omitempty"`
}

// Auditor receives append-only decision records and must be safe for
// concurrent calls. Broker also keeps a local trail. External audit failure
// converts an allow result into a denial.
type Auditor interface {
	RecordPolicyDecision(context.Context, AuditRecord) error
}

type AuditorFunc func(context.Context, AuditRecord) error

func (f AuditorFunc) RecordPolicyDecision(ctx context.Context, record AuditRecord) error {
	return f(ctx, record)
}
