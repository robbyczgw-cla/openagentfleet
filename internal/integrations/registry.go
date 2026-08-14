// Package integrations contains the read-only integration inventory used by
// OpenAgentFleet. It deliberately knows about a small, fixed set of CLI probes.
package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Host identifies the CLI or product whose local registry is being inspected.
type Host string

const (
	HostGrok     Host = "grok"
	HostCodex    Host = "codex"
	HostOpenCode Host = "opencode"
	HostCursor   Host = "cursor"
)

// Kind is the type of integration represented by a Record.
type Kind string

const (
	KindMCP    Kind = "mcp"
	KindPlugin Kind = "plugin"
)

// Status is intentionally small. A successful probe may report an installed
// integration as unavailable when the host says it is disabled or offline.
type Status string

const (
	StatusAvailable   Status = "available"
	StatusUnavailable Status = "unavailable"
)

// Record is the safe, UI-facing representation of one discovered integration.
// Detail is always compacted and credential-redacted before it leaves this
// package. Raw command output is never stored in a Record.
type Record struct {
	Host   Host   `json:"host"`
	Kind   Kind   `json:"kind"`
	Name   string `json:"name"`
	Status Status `json:"status"`
	Source string `json:"source"`
	Detail string `json:"detail,omitempty"`
}

// CommandSpec is a command selected from the package's read-only allowlist.
// Callers should pass specs to Runner only as received by Inspect or
// AllowedCommandSpecs. ExecRunner rejects every other value.
type CommandSpec struct {
	Host    Host
	Kind    Kind
	Program string
	Args    []string
	Source  string
}

// CommandOutput is the bounded output returned by a Runner.
type CommandOutput struct {
	Stdout string
	Stderr string
}

// Runner is injected so registry inspection is deterministic and testable.
// Implementations must execute only the supplied CommandSpec. Inspect itself
// supplies no user-controlled command, path, argument, environment, or shell.
type Runner interface {
	Run(context.Context, CommandSpec) (CommandOutput, error)
}

// ExecRunner executes the fixed probes without invoking a shell. It is safe to
// use from the daemon because Run validates the complete command specification
// before looking up or starting a process.
type ExecRunner struct{}

// Run executes an allowlisted command and bounds both output streams.
func (ExecRunner) Run(ctx context.Context, spec CommandSpec) (CommandOutput, error) {
	if !isAllowedSpec(spec) {
		return CommandOutput{}, errors.New("integration command is not allowlisted")
	}

	program, err := exec.LookPath(spec.Program)
	if err != nil {
		return CommandOutput{}, fmt.Errorf("program unavailable: %w", err)
	}
	command := exec.CommandContext(ctx, program, spec.Args...)
	stdout := &boundedBuffer{limit: maxOutputBytes}
	stderr := &boundedBuffer{limit: maxErrorBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return CommandOutput{Stdout: stdout.String(), Stderr: stderr.String()}, err
	}
	return CommandOutput{Stdout: stdout.String(), Stderr: stderr.String()}, nil
}

// AllowedCommandSpecs returns a defensive copy of the exact probes used by
// Inspect. The order is stable and is also the order of the returned records.
func AllowedCommandSpecs() []CommandSpec {
	result := make([]CommandSpec, 0, len(allowedSpecs))
	for _, spec := range allowedSpecs {
		result = append(result, cloneSpec(spec))
	}
	return result
}

// Inspect runs every allowlisted probe in stable order. A failed probe is
// represented by one unavailable registry record, allowing callers to show a
// useful partial inventory when a CLI is missing or not authenticated. No
// error is returned because probe errors are data in this read-only view.
func Inspect(ctx context.Context, runner Runner) []Record {
	if runner == nil {
		return unavailableRecordsForNilRunner()
	}

	result := make([]Record, 0)
	for _, spec := range allowedSpecs {
		output, err := runner.Run(ctx, cloneSpec(spec))
		if err != nil {
			result = append(result, unavailableRecord(spec, err, output))
			continue
		}
		result = append(result, parseRecords(spec, output.Stdout)...)
	}
	return result
}

var allowedSpecs = []CommandSpec{
	{Host: HostGrok, Kind: KindMCP, Program: "grok", Args: []string{"mcp", "list"}, Source: "grok:mcp:list"},
	{Host: HostGrok, Kind: KindPlugin, Program: "grok", Args: []string{"plugin", "list"}, Source: "grok:plugin:list"},
	{Host: HostCodex, Kind: KindMCP, Program: "codex", Args: []string{"mcp", "list"}, Source: "codex:mcp:list"},
	{Host: HostOpenCode, Kind: KindMCP, Program: "opencode", Args: []string{"mcp", "list"}, Source: "opencode:mcp:list"},
	{Host: HostCursor, Kind: KindMCP, Program: "cursor-agent", Args: []string{"mcp", "list"}, Source: "cursor:mcp:list"},
}

const (
	maxOutputBytes = 512 << 10
	maxErrorBytes  = 64 << 10
	maxRecordName  = 160
	maxDetail      = 240
)

var (
	credentialAssignment = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|auth(?:orization)?|bearer|client[_-]?secret|password|passwd|private[_-]?key|secret|token)\s*[:=]\s*[^\s,;]+`)
	bearerValue          = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+`)
	commonToken          = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{8,}|xai-[A-Za-z0-9_-]{8,}|gh[pousr]_[A-Za-z0-9_]{8,}|xox[baprs]-[A-Za-z0-9-]{8,}|AIza[A-Za-z0-9_-]{20,})\b`)
	jwtValue             = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)
)

func parseRecords(spec CommandSpec, text string) []Record {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	if records, ok := parseJSON(spec, text); ok {
		return records
	}
	return parseText(spec, text)
}

func parseJSON(spec CommandSpec, text string) ([]Record, bool) {
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return nil, false
	}

	items := jsonItems(value)
	result := make([]Record, 0, len(items))
	for _, item := range items {
		name, status, detail, ok := jsonRecord(item)
		if !ok {
			continue
		}
		result = append(result, makeRecord(spec, name, status, detail))
	}
	return result, true
}

func jsonItems(value any) []map[string]any {
	switch typed := value.(type) {
	case []any:
		return mapsFromSlice(typed)
	case map[string]any:
		for _, key := range []string{"servers", "mcp", "plugins", "items", "entries", "data"} {
			if nested, exists := typed[key]; exists {
				if items := jsonItems(nested); len(items) > 0 || nested == nil {
					return items
				}
			}
		}
		if _, hasName := lookupString(typed, "name", "id", "server", "plugin"); hasName {
			return []map[string]any{typed}
		}
		// Some CLIs represent a registry as {"name": {"status": ...}}.
		names := make([]string, 0, len(typed))
		for name := range typed {
			names = append(names, name)
		}
		sort.Strings(names)
		result := make([]map[string]any, 0, len(typed))
		for _, name := range names {
			raw := typed[name]
			if child, ok := raw.(map[string]any); ok {
				copy := cloneMap(child)
				if _, exists := lookupString(copy, "name", "id", "server", "plugin"); !exists {
					copy["name"] = name
				}
				result = append(result, copy)
			}
		}
		return result
	default:
		return nil
	}
}

func mapsFromSlice(values []any) []map[string]any {
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if item, ok := value.(map[string]any); ok {
			result = append(result, item)
		}
	}
	return result
}

func jsonRecord(item map[string]any) (string, Status, string, bool) {
	rawName, ok := lookupString(item, "name", "id", "server", "plugin", "key")
	if !ok {
		return "", "", "", false
	}
	name, ok := safeName(rawName)
	if !ok {
		return "", "", "", false
	}
	status := statusFromValue(item, "status", "state", "enabled", "connected", "available", "installed")
	detail := detailFromJSON(item)
	return name, status, detail, true
}

func statusFromValue(item map[string]any, keys ...string) Status {
	for _, key := range keys {
		value, exists := item[key]
		if !exists {
			continue
		}
		switch typed := value.(type) {
		case bool:
			if typed {
				return StatusAvailable
			}
			return StatusUnavailable
		case string:
			return normalizeStatus(typed)
		}
	}
	return StatusAvailable
}

func detailFromJSON(item map[string]any) string {
	for _, key := range []string{"detail", "message", "description", "error", "status", "state"} {
		if value, ok := item[key].(string); ok {
			if detail := safeDetail(value); detail != "" {
				return detail
			}
		}
	}
	return "reported by read-only registry probe"
}

func parseText(spec CommandSpec, text string) []Record {
	result := make([]Record, 0)
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || isTextHeader(line) {
			continue
		}
		if strings.HasPrefix(line, "{") || strings.HasPrefix(line, "[") {
			if records, ok := parseJSON(spec, line); ok {
				result = append(result, records...)
				continue
			}
		}

		name, status, detail, ok := parseTextLine(line)
		if !ok {
			continue
		}
		result = append(result, makeRecord(spec, name, status, detail))
	}
	return deduplicateRecords(result)
}

func parseTextLine(line string) (string, Status, string, bool) {
	line = strings.TrimSpace(strings.TrimLeft(line, "-•* \\t"))
	if line == "" {
		return "", "", "", false
	}

	// Prefer a simple `name: status` or `name — status` shape.
	for _, separator := range []string{":", "—", " - ", "\t\t"} {
		if left, right, found := strings.Cut(line, separator); found {
			if name, ok := safeName(left); ok {
				status := normalizeStatus(right)
				if isStatusOnly(right) {
					return name, status, safeDetail(right), true
				}
			}
		}
	}

	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", "", "", false
	}
	if len(fields) >= 2 && isStatusOnly(fields[len(fields)-1]) {
		name, ok := safeName(strings.Join(fields[:len(fields)-1], " "))
		if ok {
			return name, normalizeStatus(fields[len(fields)-1]), safeDetail(fields[len(fields)-1]), true
		}
	}
	if name, ok := safeName(fields[0]); ok && len(fields) == 1 {
		return name, StatusAvailable, "reported by read-only registry probe", true
	}
	return "", "", "", false
}

func makeRecord(spec CommandSpec, name string, status Status, detail string) Record {
	if status != StatusUnavailable {
		status = StatusAvailable
	}
	return Record{
		Host:   spec.Host,
		Kind:   spec.Kind,
		Name:   name,
		Status: status,
		Source: spec.Source,
		Detail: safeDetail(detail),
	}
}

func unavailableRecord(spec CommandSpec, err error, output CommandOutput) Record {
	detail := "read-only registry probe unavailable"
	if err != nil {
		if candidate := safeDetail(err.Error()); candidate != "" {
			detail += ": " + candidate
		}
	}
	if candidate := safeDetail(output.Stderr); candidate != "" && candidate != safeDetail(errString(err)) {
		detail += ": " + candidate
	}
	return Record{
		Host:   spec.Host,
		Kind:   spec.Kind,
		Name:   string(spec.Host),
		Status: StatusUnavailable,
		Source: spec.Source,
		Detail: safeDetail(detail),
	}
}

func unavailableRecordsForNilRunner() []Record {
	result := make([]Record, 0, len(allowedSpecs))
	for _, spec := range allowedSpecs {
		result = append(result, unavailableRecord(spec, errors.New("runner unavailable"), CommandOutput{}))
	}
	return result
}

func normalizeStatus(value string) Status {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.Trim(value, "()[]{}.,;\"")
	switch value {
	case "disabled", "inactive", "offline", "disconnected", "failed", "failure", "error", "unavailable", "stopped", "blocked", "false":
		return StatusUnavailable
	default:
		return StatusAvailable
	}
}

func isStatusOnly(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.Trim(value, "()[]{}.,;\"")
	switch value {
	case "enabled", "installed", "available", "active", "connected", "ready", "running", "online", "true", "disabled", "inactive", "offline", "disconnected", "failed", "failure", "error", "unavailable", "stopped", "blocked", "false":
		return true
	default:
		return false
	}
}

func safeName(value string) (string, bool) {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "\"'`[](){}:,. ")
	value = strings.Join(strings.Fields(value), " ")
	if value == "" || len(value) > maxRecordName || isTextHeader(value) || isStatusOnly(value) {
		return "", false
	}
	if strings.ContainsAny(value, "\r\n\x00") || credentialAssignment.MatchString(value) || bearerValue.MatchString(value) || commonToken.MatchString(value) || jwtValue.MatchString(value) {
		return "", false
	}
	for _, runeValue := range value {
		if unicode.IsControl(runeValue) {
			return "", false
		}
	}
	return value, true
}

func safeDetail(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = bearerValue.ReplaceAllString(value, "Bearer [redacted]")
	value = credentialAssignment.ReplaceAllString(value, "$1=[redacted]")
	value = commonToken.ReplaceAllString(value, "[redacted]")
	value = jwtValue.ReplaceAllString(value, "[redacted]")
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > maxDetail {
		value = value[:maxDetail]
	}
	return value
}

func isTextHeader(line string) bool {
	value := strings.ToLower(strings.TrimSpace(strings.Trim(line, "-_= :\t")))
	if value == "" {
		return true
	}
	for _, header := range []string{
		"name", "server", "servers", "mcp", "mcp servers", "plugins", "plugin", "configured", "integrations", "status", "command", "no servers", "no plugins", "none",
	} {
		if value == header {
			return true
		}
	}
	return false
}

func lookupString(item map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			return value, true
		}
	}
	return "", false
}

func cloneMap(value map[string]any) map[string]any {
	result := make(map[string]any, len(value)+1)
	for key, item := range value {
		result[key] = item
	}
	return result
}

func deduplicateRecords(records []Record) []Record {
	seen := make(map[string]struct{}, len(records))
	result := make([]Record, 0, len(records))
	for _, record := range records {
		key := string(record.Host) + "\x00" + string(record.Kind) + "\x00" + record.Name
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, record)
	}
	return result
}

func isAllowedSpec(spec CommandSpec) bool {
	for _, allowed := range allowedSpecs {
		if spec.Host != allowed.Host || spec.Kind != allowed.Kind || spec.Program != allowed.Program || spec.Source != allowed.Source || len(spec.Args) != len(allowed.Args) {
			continue
		}
		match := true
		for index := range allowed.Args {
			if spec.Args[index] != allowed.Args[index] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func cloneSpec(spec CommandSpec) CommandSpec {
	copy := spec
	copy.Args = append([]string(nil), spec.Args...)
	return copy
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type boundedBuffer struct {
	value strings.Builder
	limit int
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	remaining := buffer.limit - buffer.value.Len()
	if remaining > 0 {
		if len(value) > remaining {
			_, _ = buffer.value.Write(value[:remaining])
		} else {
			_, _ = buffer.value.Write(value)
		}
	}
	return len(value), nil
}

func (buffer *boundedBuffer) String() string {
	return buffer.value.String()
}
