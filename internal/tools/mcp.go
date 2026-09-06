package tools

import (
	"bytes"
	"encoding/json"
)

// MCPDescriptors maps canonical tools onto the MCP tools/list shape: name,
// description, and inputSchema. Aliases are emitted as extra descriptors of
// the same logical tool so existing underscore MCP names keep working.
func MCPDescriptors(tools []Tool) []map[string]any {
	out := make([]map[string]any, 0, len(tools)*2)
	for _, tool := range tools {
		schema := schemaValue(tool.InputSchema)
		seen := make(map[string]struct{}, 1+len(tool.Aliases))
		for _, name := range append([]string{tool.Name}, tool.Aliases...) {
			name = trimDescriptorName(name)
			if name == "" {
				continue
			}
			if _, exists := seen[name]; exists {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, map[string]any{
				"name":        name,
				"description": tool.Description,
				"inputSchema": schema,
			})
		}
	}
	return out
}

func trimDescriptorName(name string) string {
	if name == "" {
		return ""
	}
	normalized, err := normalizeName(name)
	if err != nil {
		return ""
	}
	return normalized
}

func schemaValue(raw json.RawMessage) any {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return map[string]any{}
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return map[string]any{}
	}
	return value
}
