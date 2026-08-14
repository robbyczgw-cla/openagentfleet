package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type canonicalAction struct {
	Version    int         `json:"version"`
	Principal  Principal   `json:"principal"`
	RunID      string      `json:"run_id"`
	Resource   Resource    `json:"resource"`
	Operation  string      `json:"operation"`
	Parameters []Parameter `json:"parameters,omitempty"`
}

// ActionHash returns a stable SHA-256 hash over the validated action.
// Parameter order does not affect the result; all other action fields do.
func ActionHash(action Action) (string, error) {
	if err := validateAction(action); err != nil {
		return "", err
	}
	parameters := append([]Parameter(nil), action.Parameters...)
	sort.Slice(parameters, func(i, j int) bool { return parameters[i].Name < parameters[j].Name })
	payload, err := json.Marshal(canonicalAction{
		Version: CurrentVersion, Principal: action.Principal, RunID: action.RunID,
		Resource: action.Resource, Operation: action.Operation, Parameters: parameters,
	})
	if err != nil {
		return "", fmt.Errorf("marshal canonical action: %w", err)
	}
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func validateActionHash(value string) error {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) {
		return errors.New("action hash must use sha256 prefix")
	}
	encoded := strings.TrimPrefix(value, prefix)
	if len(encoded) != sha256.Size*2 || encoded != strings.ToLower(encoded) {
		return errors.New("action hash must contain 64 lowercase hexadecimal characters")
	}
	decoded, err := hex.DecodeString(encoded)
	if err != nil || len(decoded) != sha256.Size {
		return errors.New("action hash must contain 64 lowercase hexadecimal characters")
	}
	return nil
}
