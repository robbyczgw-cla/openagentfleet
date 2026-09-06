package domain

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type MemoryProposalStatus string

const (
	MemoryProposalStatusPending  MemoryProposalStatus = "pending"
	MemoryProposalStatusAccepted MemoryProposalStatus = "accepted"
	MemoryProposalStatusRejected MemoryProposalStatus = "rejected"
)

const (
	MemoryProposalPendingPerBot = 25
	MemoryProposalPerRun        = 5
)

// MemoryProposal keeps agent-authored suggestions separate from approved
// memory until a local reviewer accepts one.
type MemoryProposal struct {
	ID              string               `json:"id"`
	BotID           string               `json:"bot_id"`
	SourceRunID     string               `json:"source_run_id"`
	SourceMessageID string               `json:"source_message_id"`
	Category        MemoryCategory       `json:"category"`
	Status          MemoryProposalStatus `json:"status"`
	Content         string               `json:"content"`
	Priority        int                  `json:"priority"`
	ExpiresAt       string               `json:"expires_at,omitempty"`
	MemoryID        string               `json:"memory_id,omitempty"`
	CreatedAt       string               `json:"created_at"`
	UpdatedAt       string               `json:"updated_at"`
}

type MemoryProposalDraft struct {
	BotID           string         `json:"bot_id"`
	SourceRunID     string         `json:"source_run_id"`
	SourceMessageID string         `json:"source_message_id"`
	Category        MemoryCategory `json:"category"`
	Content         string         `json:"content"`
	Priority        int            `json:"priority"`
	ExpiresAt       string         `json:"expires_at,omitempty"`
}

type MemoryProposalUpdate struct {
	Category  MemoryCategory `json:"category"`
	Content   string         `json:"content"`
	Priority  int            `json:"priority"`
	ExpiresAt string         `json:"expires_at,omitempty"`
}

func NormalizeMemoryProposalDraft(value MemoryProposalDraft) (MemoryProposalDraft, error) {
	if err := ValidateMemoryIdentifier("bot id", value.BotID); err != nil {
		return MemoryProposalDraft{}, err
	}
	if err := ValidateMemoryIdentifier("source run id", value.SourceRunID); err != nil {
		return MemoryProposalDraft{}, err
	}
	if err := ValidateMemoryIdentifier("source message id", value.SourceMessageID); err != nil {
		return MemoryProposalDraft{}, err
	}
	fields, err := NormalizeMemoryProposalUpdate(MemoryProposalUpdate{
		Category: value.Category, Content: value.Content, Priority: value.Priority, ExpiresAt: value.ExpiresAt,
	})
	if err != nil {
		return MemoryProposalDraft{}, err
	}
	value.Category = fields.Category
	value.Content = fields.Content
	value.Priority = fields.Priority
	value.ExpiresAt = fields.ExpiresAt
	return value, nil
}

func NormalizeMemoryProposalUpdate(value MemoryProposalUpdate) (MemoryProposalUpdate, error) {
	normalized, err := NormalizeBotMemoryDraft(BotMemoryDraft{
		Category: value.Category, Status: MemoryStatusApproved, Source: MemorySourceAgentProposal,
		Content: value.Content, Priority: value.Priority, ExpiresAt: value.ExpiresAt,
	})
	if err != nil {
		return MemoryProposalUpdate{}, err
	}
	return MemoryProposalUpdate{
		Category: normalized.Category, Content: normalized.Content, Priority: normalized.Priority, ExpiresAt: normalized.ExpiresAt,
	}, nil
}

func ValidateMemoryProposalStatus(value MemoryProposalStatus) error {
	switch value {
	case MemoryProposalStatusPending, MemoryProposalStatusAccepted, MemoryProposalStatusRejected:
		return nil
	default:
		return errors.New("invalid memory proposal status")
	}
}

// NormalizeMemoryProposalContentKey creates the stable deduplication key used
// for proposals from the same bot.
func NormalizeMemoryProposalContentKey(content string) (string, error) {
	if !utf8.ValidString(content) {
		return "", errors.New("memory proposal content must be valid UTF-8")
	}
	key := strings.ToLower(strings.Join(strings.FieldsFunc(strings.TrimSpace(content), unicode.IsSpace), " "))
	if key == "" {
		return "", errors.New("memory proposal content is required")
	}
	if len(key) > MemoryContentMaxBytes {
		return "", fmt.Errorf("memory proposal content key must be at most %d bytes", MemoryContentMaxBytes)
	}
	return key, nil
}
