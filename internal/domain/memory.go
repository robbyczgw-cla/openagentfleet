package domain

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type MemoryCategory string

const (
	MemoryCategoryFact        MemoryCategory = "fact"
	MemoryCategoryPreference  MemoryCategory = "preference"
	MemoryCategoryInstruction MemoryCategory = "instruction"
	MemoryCategoryProject     MemoryCategory = "project"
)

type MemoryStatus string

const (
	MemoryStatusApproved MemoryStatus = "approved"
	MemoryStatusArchived MemoryStatus = "archived"
)

type MemorySource string

const (
	MemorySourceUser          MemorySource = "user"
	MemorySourceAgentProposal MemorySource = "agent_proposal"
)

const (
	MemoryContentMaxBytes    = 4096
	MemoryRetrievalMaxCount  = 100
	MemoryRetrievalMaxBytes  = MemoryContentMaxBytes * MemoryRetrievalMaxCount
	MemoryIdentifierMaxBytes = 256
)

// BotMemory is durable, user-reviewable bot context. Source and CreatedAt are
// immutable after creation so an agent proposal cannot later be presented as a
// user-authored memory.
type BotMemory struct {
	ID        string         `json:"id"`
	BotID     string         `json:"bot_id"`
	Category  MemoryCategory `json:"category"`
	Status    MemoryStatus   `json:"status"`
	Source    MemorySource   `json:"source"`
	Content   string         `json:"content"`
	Priority  int            `json:"priority"`
	ExpiresAt string         `json:"expires_at,omitempty"`
	CreatedAt string         `json:"created_at"`
	UpdatedAt string         `json:"updated_at"`
}

type BotMemoryDraft struct {
	Category  MemoryCategory `json:"category"`
	Status    MemoryStatus   `json:"status"`
	Source    MemorySource   `json:"source"`
	Content   string         `json:"content"`
	Priority  int            `json:"priority"`
	ExpiresAt string         `json:"expires_at,omitempty"`
}

// BotMemoryUpdate deliberately omits Source. Provenance is immutable.
type BotMemoryUpdate struct {
	Category  MemoryCategory `json:"category"`
	Status    MemoryStatus   `json:"status"`
	Content   string         `json:"content"`
	Priority  int            `json:"priority"`
	ExpiresAt string         `json:"expires_at,omitempty"`
}

var (
	privateKeyPattern = regexp.MustCompile(`(?i)-----BEGIN[[:space:]]+(?:RSA[[:space:]]+|EC[[:space:]]+|DSA[[:space:]]+|OPENSSH[[:space:]]+)?PRIVATE KEY-----`)
	jwtPattern        = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	knownTokenPattern = regexp.MustCompile(`(?i)\b(?:sk-[A-Za-z0-9_-]{16,}|xai-[A-Za-z0-9_-]{16,}|ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|AKIA[A-Z0-9]{16})\b`)
	bearerPattern     = regexp.MustCompile(`(?i)\bauthorization\s*:\s*bearer\s+([^\s,;]+)`)
	envSecretPattern  = regexp.MustCompile(`(?m)\b[A-Z][A-Z0-9_]*(?:API_KEY|ACCESS_TOKEN|REFRESH_TOKEN|CLIENT_SECRET|PASSWORD)\s*=\s*([^\s,;]+)`)
	credentialPattern = regexp.MustCompile(`(?i)\b(password|passcode|api[ _-]?key|access[ _-]?token|refresh[ _-]?token|client[ _-]?secret)\b\s*(?:is|=|:)\s*([^\s,;]+)`)
	pinPattern        = regexp.MustCompile(`(?i)\bpin\b\s*(?:is|=|:)\s*([0-9]{4,12})\b`)
)

func NormalizeBotMemoryDraft(value BotMemoryDraft) (BotMemoryDraft, error) {
	value.Content = strings.TrimSpace(value.Content)
	expiresAt, err := normalizeMemoryExpiry(value.ExpiresAt)
	if err != nil {
		return BotMemoryDraft{}, err
	}
	value.ExpiresAt = expiresAt
	if err := validateMemoryFields(value.Category, value.Status, value.Source, value.Content, value.Priority); err != nil {
		return BotMemoryDraft{}, err
	}
	return value, nil
}

func NormalizeBotMemoryUpdate(value BotMemoryUpdate) (BotMemoryUpdate, error) {
	value.Content = strings.TrimSpace(value.Content)
	expiresAt, err := normalizeMemoryExpiry(value.ExpiresAt)
	if err != nil {
		return BotMemoryUpdate{}, err
	}
	value.ExpiresAt = expiresAt
	if err := validateMemoryFields(value.Category, value.Status, MemorySourceUser, value.Content, value.Priority); err != nil {
		return BotMemoryUpdate{}, err
	}
	return value, nil
}

func ValidateMemoryIdentifier(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must not have surrounding whitespace", name)
	}
	if len(value) > MemoryIdentifierMaxBytes || !utf8.ValidString(value) {
		return fmt.Errorf("%s is invalid", name)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s is invalid", name)
		}
	}
	return nil
}

func ValidateMemoryRetrievalLimits(maxCount, maxBytes int) error {
	if maxCount < 1 || maxCount > MemoryRetrievalMaxCount {
		return fmt.Errorf("max count must be between 1 and %d", MemoryRetrievalMaxCount)
	}
	if maxBytes < 1 || maxBytes > MemoryRetrievalMaxBytes {
		return fmt.Errorf("max bytes must be between 1 and %d", MemoryRetrievalMaxBytes)
	}
	return nil
}

func validateMemoryFields(category MemoryCategory, status MemoryStatus, source MemorySource, content string, priority int) error {
	switch category {
	case MemoryCategoryFact, MemoryCategoryPreference, MemoryCategoryInstruction, MemoryCategoryProject:
	default:
		return errors.New("invalid memory category")
	}
	switch status {
	case MemoryStatusApproved, MemoryStatusArchived:
	default:
		return errors.New("invalid memory status")
	}
	switch source {
	case MemorySourceUser, MemorySourceAgentProposal:
	default:
		return errors.New("invalid memory source")
	}
	if priority < 1 || priority > 5 {
		return errors.New("memory priority must be between 1 and 5")
	}
	if content == "" {
		return errors.New("memory content is required")
	}
	if !utf8.ValidString(content) || len(content) > MemoryContentMaxBytes {
		return fmt.Errorf("memory content must be valid UTF-8 and at most %d bytes", MemoryContentMaxBytes)
	}
	for _, character := range content {
		if unicode.IsControl(character) && character != '\n' && character != '\t' {
			return errors.New("memory content contains unsupported control characters")
		}
	}
	if containsUnsafeMemorySecret(content) {
		return errors.New("memory content appears to contain a secret")
	}
	return nil
}

func normalizeMemoryExpiry(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", errors.New("memory expiry must be an RFC3339 timestamp")
	}
	return parsed.UTC().Format(time.RFC3339Nano), nil
}

func containsUnsafeMemorySecret(content string) bool {
	if privateKeyPattern.MatchString(content) || jwtPattern.MatchString(content) || knownTokenPattern.MatchString(content) {
		return true
	}
	for _, pattern := range []*regexp.Regexp{bearerPattern, envSecretPattern} {
		matches := pattern.FindAllStringSubmatch(content, -1)
		for _, match := range matches {
			if len(match) > 1 && !isSecretPlaceholder(match[1]) {
				return true
			}
		}
	}
	if pinPattern.MatchString(content) {
		return true
	}
	for _, match := range credentialPattern.FindAllStringSubmatch(content, -1) {
		if len(match) > 2 && looksLikeAssignedSecret(match[2]) {
			return true
		}
	}
	for _, field := range strings.Fields(content) {
		candidate := strings.Trim(field, `()[]{}<>,.;"'`)
		parsed, err := url.Parse(candidate)
		if err == nil && parsed.IsAbs() && parsed.User != nil {
			if _, hasPassword := parsed.User.Password(); hasPassword {
				return true
			}
		}
	}
	return false
}

func isSecretPlaceholder(value string) bool {
	value = strings.Trim(strings.TrimSpace(value), `"'`)
	lower := strings.ToLower(value)
	if lower == "" || lower == "..." || lower == "redacted" || lower == "[redacted]" || lower == "<redacted>" {
		return true
	}
	return strings.HasPrefix(value, "${") || strings.HasPrefix(value, "<") || strings.Contains(lower, "your-") || strings.Contains(lower, "example")
}

func looksLikeAssignedSecret(value string) bool {
	value = strings.Trim(strings.TrimSpace(value), `"'`)
	if isSecretPlaceholder(value) {
		return false
	}
	if len(value) >= 12 {
		return true
	}
	var letters, digits, symbols int
	for _, character := range value {
		switch {
		case unicode.IsLetter(character):
			letters++
		case unicode.IsDigit(character):
			digits++
		default:
			symbols++
		}
	}
	return len(value) >= 6 && (digits > 0 || symbols > 0) && letters > 0
}
