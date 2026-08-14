package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
)

const maxCapturedOutput = 512 * 1024

var (
	apiKeyPattern   = regexp.MustCompile(`(?i)\b(?:xai|sk|ghp|github_pat|akia)[_-][a-z0-9_\-]{12,}\b`)
	bearerPattern   = regexp.MustCompile(`(?i)(bearer\s+)[a-z0-9_\-.=+/]{12,}`)
	secretJSONValue = regexp.MustCompile(`(?i)("(?:api[_-]?key|token|secret|password)"\s*:\s*")[^"]+(")`)
)

type OutputLine struct {
	Stream string
	Text   string
	Type   string
}

type Runner struct {
	AllowExecution       bool
	AllowWorkspaceWrites bool
	CodexAppServer       *CodexAppServer
}

func NewRunner(allowExecution bool) *Runner {
	return &Runner{AllowExecution: allowExecution, AllowWorkspaceWrites: os.Getenv("OPENAGENTFLEET_ALLOW_HARNESS_WORKSPACE_WRITES") == "1"}
}

// Run executes a structured provider command. No shell is involved: provider
// arguments are built by BuildCommand and the explicit gate is checked here
// as a second line of defense behind the HTTP/API configuration.
func (r *Runner) Run(ctx context.Context, provider, prompt, workdir string, onLine func(OutputLine)) (string, error) {
	return r.RunWithOptions(ctx, provider, prompt, workdir, RunOptions{OnLine: onLine})
}

func (r *Runner) runCommand(ctx context.Context, provider, prompt, workdir string, options RunOptions) (string, error) {
	command, err := BuildCommandWithOptions(provider, prompt, workdir, CommandOptions{
		SessionID:       options.SessionID,
		Model:           options.Model,
		ReasoningEffort: options.ReasoningEffort,
	})
	if err != nil {
		return "", err
	}
	if options.OnLine == nil {
		options.OnLine = func(OutputLine) {}
	}

	process := newIsolatedCommandContext(ctx, provider, command.Program, command.Args...)
	process.Dir = command.Dir
	if provider == OpenCodeProvider && (len(options.MCPServers) > 0 || options.WebSearch != "") {
		config, err := marshalOpenCodeRunConfig(options.MCPServers, options.WebSearch)
		if err != nil {
			return "", err
		}
		process.Env = append(process.Env, "OPENCODE_CONFIG_CONTENT="+config)
	}
	stdout, err := process.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("capture %s stdout: %w", provider, err)
	}
	stderr, err := process.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("capture %s stderr: %w", provider, err)
	}
	if err := process.Start(); err != nil {
		if provider == OpenCodeProvider {
			return "", openCodeStartError(err)
		}
		return "", fmt.Errorf("start %s: %w", provider, err)
	}

	lines := make(chan OutputLine, 128)
	var readers sync.WaitGroup
	read := func(stream string, scanner *bufio.Scanner) {
		defer readers.Done()
		scanner.Buffer(make([]byte, 32*1024), 2*1024*1024)
		for scanner.Scan() {
			value := Redact(scanner.Text())
			lines <- OutputLine{Stream: stream, Text: value, Type: outputType(value)}
		}
	}
	readers.Add(2)
	go read("stdout", bufio.NewScanner(stdout))
	go read("stderr", bufio.NewScanner(stderr))
	go func() {
		readers.Wait()
		close(lines)
	}()

	var captured bytes.Buffer
	nativeSessionID := ""
	for line := range lines {
		if nativeSessionID == "" && options.OnSession != nil {
			if candidate := sessionIDFromOutput(provider, line.Text); candidate != "" {
				nativeSessionID = candidate
				options.OnSession(candidate)
			}
		}
		options.OnLine(line)
		if captured.Len() < maxCapturedOutput {
			remaining := maxCapturedOutput - captured.Len()
			value := line.Text + "\n"
			if len(value) > remaining {
				value = value[:remaining]
			}
			captured.WriteString(value)
		}
	}
	waitErr := process.Wait()
	if waitErr != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return captured.String(), ctx.Err()
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return captured.String(), ctx.Err()
		}
		if provider == OpenCodeProvider {
			return captured.String(), openCodeExitError(waitErr, captured.String())
		}
		return captured.String(), fmt.Errorf("%s exited: %w", provider, waitErr)
	}
	return captured.String(), nil
}

func Redact(value string) string {
	value = apiKeyPattern.ReplaceAllString(value, "[REDACTED_TOKEN]")
	value = bearerPattern.ReplaceAllString(value, "${1}[REDACTED_TOKEN]")
	return secretJSONValue.ReplaceAllString(value, "$1[REDACTED_SECRET]$2")
}

// AssistantText extracts the user-facing answer from the structured output
// formats used by the requested CLIs. Unknown or plain output is preserved so
// an adapter never silently loses a provider result.
func AssistantText(provider, output string) string {
	if strings.TrimSpace(output) == "" {
		return ""
	}
	var text strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var value map[string]any
		if json.Unmarshal([]byte(line), &value) != nil {
			if provider != "grok" && provider != "claude" && provider != "codex" && provider != "codex_app_server" && provider != "pi" && provider != OpenCodeProvider && provider != "cursor" {
				text.WriteString(line)
				text.WriteByte('\n')
			}
			continue
		}
		appendString := func(candidate any) {
			if item, ok := candidate.(string); ok && strings.TrimSpace(item) != "" {
				text.WriteString(item)
				text.WriteByte('\n')
			}
		}
		switch provider {
		case "grok":
			if value["type"] == "text" {
				if item, ok := value["data"].(string); ok {
					text.WriteString(item)
				}
			} else if value["type"] == "result" {
				appendString(value["text"])
				appendString(value["result"])
			}
		case "claude":
			if value["type"] == "result" {
				appendString(value["result"])
			}
			if message, ok := value["message"].(map[string]any); ok {
				appendContentBlocks(&text, message["content"])
			}
		case "codex":
			if item, ok := value["item"].(map[string]any); ok && item["type"] == "agent_message" {
				appendString(item["text"])
			}
			appendString(value["text"])
		case "codex_app_server":
			if value["type"] == "text" {
				// Codex App Server emits agent_message/delta chunks. They are
				// already ordered text fragments, so inserting a newline between
				// every fragment corrupts words and markdown as the stream grows.
				if item, ok := value["data"].(string); ok {
					text.WriteString(item)
				}
			}
		case "pi":
			appendString(value["text"])
			if message, ok := value["message"].(map[string]any); ok {
				appendContentBlocks(&text, message["content"])
			}
		case OpenCodeProvider:
			if part, ok := value["part"].(map[string]any); ok && part["type"] == "text" {
				appendString(part["text"])
			}
		case "cursor":
			appendCursorText(&text, value)
		}
	}
	if text.Len() == 0 {
		return strings.TrimSpace(output)
	}
	return strings.TrimSpace(text.String())
}

func outputType(line string) string {
	var value map[string]any
	if json.Unmarshal([]byte(line), &value) != nil {
		return "text"
	}
	if part, ok := value["part"].(map[string]any); ok {
		if item, ok := part["type"].(string); ok {
			return item
		}
	}
	if item, ok := value["type"].(string); ok {
		return item
	}
	return "text"
}

func sessionIDFromOutput(provider, line string) string {
	if provider != "cursor" && provider != OpenCodeProvider {
		return ""
	}
	var value map[string]any
	if json.Unmarshal([]byte(line), &value) != nil {
		return ""
	}
	keys := []string{"session_id", "sessionId", "chat_id", "chatId"}
	if provider == OpenCodeProvider {
		keys = []string{"sessionID"}
	}
	for _, key := range keys {
		if candidate, ok := value[key].(string); ok && candidate != "" {
			return candidate
		}
	}
	return ""
}

// Cursor emits newline-delimited event objects in stream-json mode. Keep only
// assistant-facing events; tool calls and internal reasoning stay in the live
// activity stream but do not become the conversation answer.
func appendCursorText(text *strings.Builder, value map[string]any) {
	eventType, _ := value["type"].(string)
	normalized := strings.ToLower(eventType)
	if strings.Contains(normalized, "thinking") || strings.Contains(normalized, "tool") {
		return
	}
	if !strings.Contains(normalized, "assistant") && normalized != "text" && normalized != "result" {
		return
	}
	appendValue := func(candidate any) {
		if item, ok := candidate.(string); ok && strings.TrimSpace(item) != "" {
			text.WriteString(item)
			text.WriteByte('\n')
		}
	}
	appendValue(value["text"])
	appendValue(value["result"])
	appendValue(value["delta"])
	if message, ok := value["message"].(map[string]any); ok {
		appendValue(message["text"])
		appendContentBlocks(text, message["content"])
	}
	if delta, ok := value["delta"].(map[string]any); ok {
		appendValue(delta["text"])
	}
}

func appendContentBlocks(text *strings.Builder, content any) {
	blocks, ok := content.([]any)
	if !ok {
		return
	}
	for _, block := range blocks {
		item, ok := block.(map[string]any)
		if ok && item["type"] == "text" {
			if value, ok := item["text"].(string); ok {
				text.WriteString(value)
				text.WriteByte('\n')
			}
		}
	}
}
