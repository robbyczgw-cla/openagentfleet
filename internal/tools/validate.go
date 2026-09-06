package tools

import (
	"bytes"
	"encoding/json"
	"strings"
)

type schemaRequired struct {
	Required []string `json:"required"`
}

func missingRequiredKeys(schema, input json.RawMessage) []string {
	schema = bytes.TrimSpace(schema)
	if len(schema) == 0 || bytes.Equal(schema, []byte("null")) {
		return nil
	}
	var spec schemaRequired
	if err := json.Unmarshal(schema, &spec); err != nil {
		return nil
	}
	required := make([]string, 0, len(spec.Required))
	seen := make(map[string]struct{}, len(spec.Required))
	for _, key := range spec.Required {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		required = append(required, key)
	}
	if len(required) == 0 {
		return nil
	}

	raw := bytes.TrimSpace(input)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = []byte("{}")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return required
	}

	missing := make([]string, 0)
	for _, key := range required {
		if _, ok := object[key]; !ok {
			missing = append(missing, key)
		}
	}
	return missing
}

func missingCapabilities(required, granted []string) []string {
	if len(required) == 0 {
		return nil
	}
	have := make(map[string]struct{}, len(granted))
	for _, capability := range granted {
		capability = strings.TrimSpace(capability)
		if capability == "" {
			continue
		}
		have[capability] = struct{}{}
	}
	missing := make([]string, 0)
	seen := make(map[string]struct{}, len(required))
	for _, capability := range required {
		capability = strings.TrimSpace(capability)
		if capability == "" {
			continue
		}
		if _, exists := seen[capability]; exists {
			continue
		}
		seen[capability] = struct{}{}
		if _, ok := have[capability]; !ok {
			missing = append(missing, capability)
		}
	}
	return missing
}

func joinQuoted(values []string) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = `"` + value + `"`
	}
	return strings.Join(parts, ", ")
}
