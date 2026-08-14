package policy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxLocalAuditRecords = 2048

type Option func(*Broker) error

func WithClock(clock func() time.Time) Option {
	return func(broker *Broker) error {
		if clock == nil {
			return errors.New("policy clock cannot be nil")
		}
		broker.clock = clock
		return nil
	}
}

func WithAuditor(auditor Auditor) Option {
	return func(broker *Broker) error {
		if auditor == nil {
			return errors.New("policy auditor cannot be nil")
		}
		broker.auditor = auditor
		return nil
	}
}

// Broker is immutable except for a bounded local audit buffer and is safe for
// concurrent evaluation.
type Broker struct {
	config  Config
	clock   func() time.Time
	auditor Auditor

	mu          sync.Mutex
	auditPrefix string
	nextAudit   uint64
	audit       []AuditRecord
}

// New validates and defensively clones config. New(Config{}) is disabled and
// fail-closed.
func New(config Config, options ...Option) (*Broker, error) {
	validated, err := validateConfig(config)
	if err != nil {
		return nil, err
	}
	auditPrefix, err := newAuditPrefix()
	if err != nil {
		return nil, fmt.Errorf("create policy audit namespace: %w", err)
	}
	broker := &Broker{config: validated, clock: time.Now, auditPrefix: auditPrefix, audit: make([]AuditRecord, 0)}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("nil policy option")
		}
		if err := option(broker); err != nil {
			return nil, err
		}
	}
	return broker, nil
}

func (b *Broker) Enabled() bool { return b != nil && b.config.Enabled }

func (b *Broker) Config() Config {
	if b == nil {
		return Config{Version: CurrentVersion}
	}
	config := b.config
	config.Rules = cloneRules(config.Rules)
	return config
}

func (b *Broker) Evaluate(ctx context.Context, action Action) (Decision, error) {
	return b.EvaluateWithApprovals(ctx, action, nil)
}

// EvaluateWithApprovals applies deny-by-default policy. Deny outranks ask,
// ask outranks allow, and approvals may resolve only the winning ask rule.
func (b *Broker) EvaluateWithApprovals(ctx context.Context, action Action, approvals []Approval) (Decision, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().UTC()
	if b != nil && b.clock != nil {
		now = b.clock().UTC()
	}
	hash, err := ActionHash(action)
	if err != nil {
		decision := Decision{Effect: EffectDeny, Reason: ReasonInvalidAction, EvaluatedAt: now}
		// Never place malformed resource data (for example URL userinfo) in the
		// audit trail.
		return b.finish(ctx, Action{}, decision, fmt.Errorf("validate policy action: %w", err))
	}
	decision := Decision{Effect: EffectDeny, Reason: ReasonDisabled, ActionHash: hash, EvaluatedAt: now}
	if b == nil || !b.config.Enabled {
		return b.finish(ctx, action, decision, nil)
	}

	matches := b.matchingRules(action, now)
	if len(matches) == 0 {
		decision.Reason = ReasonDefaultDeny
		return b.finish(ctx, action, decision, nil)
	}
	sortMatches(matches)
	decision.MatchedRuleIDs = make([]string, 0, len(matches))
	for _, match := range matches {
		decision.MatchedRuleIDs = append(decision.MatchedRuleIDs, match.rule.ID)
	}
	winner := matches[0].rule
	decision.WinningRuleID = winner.ID

	switch winner.Effect {
	case EffectDeny:
		decision.Effect, decision.Reason = EffectDeny, ReasonRuleDeny
	case EffectAsk:
		decision.Effect, decision.Reason = EffectAsk, ReasonApprovalRequired
		resolved, approvalErr := resolveApproval(action, hash, winner.ID, approvals, now)
		if approvalErr != nil {
			decision.Effect, decision.Reason = EffectDeny, ReasonInvalidApproval
			return b.finish(ctx, action, decision, approvalErr)
		}
		if resolved != nil {
			decision.ApprovalID = resolved.ID
			if resolved.Status == ApprovalApproved {
				decision.Effect, decision.Reason = EffectAllow, ReasonApprovalGranted
			} else {
				decision.Effect, decision.Reason = EffectDeny, ReasonApprovalDenied
			}
		}
	case EffectAllow:
		decision.Effect, decision.Reason = EffectAllow, ReasonRuleAllow
	}
	return b.finish(ctx, action, decision, nil)
}

func (b *Broker) AuditTrail() []AuditRecord {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	result := make([]AuditRecord, len(b.audit))
	copy(result, b.audit)
	for index := range result {
		result[index].MatchedRuleIDs = append([]string(nil), result[index].MatchedRuleIDs...)
	}
	return result
}

type ruleMatch struct {
	rule        Rule
	specificity int
}

func (b *Broker) matchingRules(action Action, now time.Time) []ruleMatch {
	result := make([]ruleMatch, 0)
	for _, rule := range b.config.Rules {
		if rule.ExpiresAt != nil && !rule.ExpiresAt.After(now) {
			continue
		}
		if rule.Principal != action.Principal || !containsOperation(rule.Operations, action.Operation) {
			continue
		}
		if matched, specificity := scopeMatches(rule.Scope, action.Resource); matched {
			result = append(result, ruleMatch{rule: rule, specificity: specificity})
		}
	}
	return result
}

// Deny and ask are safety constraints, not specificity preferences: they
// always outrank a matching allow rule. Specificity only attributes the
// winning rule deterministically within one effect.
func sortMatches(matches []ruleMatch) {
	sort.Slice(matches, func(i, j int) bool {
		left, right := effectRank(matches[i].rule.Effect), effectRank(matches[j].rule.Effect)
		if left != right {
			return left > right
		}
		if matches[i].specificity != matches[j].specificity {
			return matches[i].specificity > matches[j].specificity
		}
		return matches[i].rule.ID < matches[j].rule.ID
	})
}

func effectRank(effect Effect) int {
	switch effect {
	case EffectDeny:
		return 3
	case EffectAsk:
		return 2
	case EffectAllow:
		return 1
	default:
		return 0
	}
}

func scopeMatches(scope Scope, resource Resource) (bool, int) {
	if scope.Resource.Kind != resource.Kind || scope.Resource.Qualifier != resource.Qualifier {
		return false, 0
	}
	if scope.Match == MatchExact {
		if scope.Resource.Target != resource.Target {
			return false, 0
		}
		return true, 10_000 + pathDepth(resource.Target)
	}
	root, target := scope.Resource.Target, resource.Target
	if target != root {
		relative, err := filepath.Rel(root, target)
		if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return false, 0
		}
	}
	return true, 1_000 + pathDepth(root)
}

func pathDepth(path string) int {
	depth := 0
	for current := filepath.Clean(path); filepath.Dir(current) != current; current = filepath.Dir(current) {
		depth++
	}
	return depth
}

func containsOperation(operations []string, operation string) bool {
	for _, candidate := range operations {
		if candidate == operation {
			return true
		}
	}
	return false
}

func resolveApproval(action Action, hash, ruleID string, approvals []Approval, now time.Time) (*Approval, error) {
	matching := make([]Approval, 0)
	for index := range approvals {
		approval := approvals[index]
		// Unrelated records are not authority for this action and cannot make
		// its approval path unavailable merely by being stale or corrupt.
		if approval.ActionHash != hash || approval.RuleID != ruleID || approval.Principal != action.Principal || approval.RunID != action.RunID {
			continue
		}
		if err := validateApproval(approval); err != nil {
			return nil, fmt.Errorf("approval %d: %w", index, err)
		}
		if approval.Status == ApprovalRevoked || !approval.ExpiresAt.After(now) {
			continue
		}
		matching = append(matching, approval)
	}
	if len(matching) == 0 {
		return nil, nil
	}
	sort.Slice(matching, func(i, j int) bool {
		if matching[i].Status != matching[j].Status {
			return matching[i].Status == ApprovalDenied
		}
		if !matching[i].CreatedAt.Equal(matching[j].CreatedAt) {
			return matching[i].CreatedAt.After(matching[j].CreatedAt)
		}
		return matching[i].ID < matching[j].ID
	})
	return &matching[0], nil
}

func (b *Broker) finish(ctx context.Context, action Action, decision Decision, evaluationErr error) (Decision, error) {
	if b == nil {
		return decision, evaluationErr
	}
	record := b.newAuditRecord(action, decision)
	decision.AuditID = record.ID
	if b.auditor != nil {
		if err := b.auditor.RecordPolicyDecision(ctx, record); err != nil {
			if decision.Effect == EffectAllow {
				decision.Effect, decision.Reason = EffectDeny, ReasonAuditUnavailable
				record.Effect, record.Reason = decision.Effect, decision.Reason
			}
			b.storeAudit(record)
			return decision, errors.Join(evaluationErr, fmt.Errorf("record policy audit: %w", err))
		}
	}
	b.storeAudit(record)
	return decision, evaluationErr
}

func (b *Broker) newAuditRecord(action Action, decision Decision) AuditRecord {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextAudit++
	prefix := b.auditPrefix
	if prefix == "" {
		prefix = "zero"
	}
	return AuditRecord{
		ID: fmt.Sprintf("audit-%s-%012d", prefix, b.nextAudit), EvaluatedAt: decision.EvaluatedAt,
		Principal: action.Principal, RunID: action.RunID, Resource: action.Resource,
		Operation: action.Operation, ActionHash: decision.ActionHash, Effect: decision.Effect,
		Reason: decision.Reason, WinningRuleID: decision.WinningRuleID,
		MatchedRuleIDs: append([]string(nil), decision.MatchedRuleIDs...), ApprovalID: decision.ApprovalID,
	}
}

func (b *Broker) storeAudit(record AuditRecord) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.audit) == maxLocalAuditRecords {
		copy(b.audit, b.audit[1:])
		b.audit[len(b.audit)-1] = record
	} else {
		b.audit = append(b.audit, record)
	}
}

func newAuditPrefix() (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func cloneRules(rules []Rule) []Rule {
	result := make([]Rule, len(rules))
	copy(result, rules)
	for index := range result {
		result[index].Operations = append([]string(nil), result[index].Operations...)
		if result[index].ExpiresAt != nil {
			expires := *result[index].ExpiresAt
			result[index].ExpiresAt = &expires
		}
	}
	return result
}
