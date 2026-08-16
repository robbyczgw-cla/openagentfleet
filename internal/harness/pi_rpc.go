package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

func (r *Runner) runPiRPC(ctx context.Context, prompt, workdir string, options RunOptions) (string, error) {
	if options.Role == piLeadRole {
		if err := ValidatePiLeadOptions(options.Model, options.ReasoningEffort, options.ServiceTier, options.PermissionMode); err != nil {
			return "", err
		}
	} else if err := ValidatePiOptions(options.Model, options.ReasoningEffort, options.ServiceTier, options.PermissionMode); err != nil {
		return "", err
	}
	command, err := BuildCommandWithOptions(piProvider, prompt, workdir, CommandOptions{
		Model:           options.Model,
		ReasoningEffort: options.ReasoningEffort,
		PermissionMode:  options.PermissionMode,
		Role:            options.Role,
	})
	if err != nil {
		return "", err
	}

	process := newIsolatedCommandContext(ctx, piProvider, command.Program, command.Args...)
	process.Dir = command.Dir
	stdin, err := process.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("open Pi RPC stdin: %w", err)
	}
	stdout, err := process.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("capture Pi RPC stdout: %w", err)
	}
	stderr, err := process.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("capture Pi RPC stderr: %w", err)
	}
	if err := process.Start(); err != nil {
		return "", fmt.Errorf("start Pi RPC; verify `pi --version` succeeds and pi is in PATH: %w", err)
	}

	var output bytes.Buffer
	var assistant strings.Builder
	events := make(chan OutputLine, 128)
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		scanPiRPC(stdout, "stdout", events)
	}()
	go func() {
		defer readers.Done()
		scanPiRPC(stderr, "stderr", events)
	}()
	go func() {
		readers.Wait()
		close(events)
	}()

	var stdinMu sync.Mutex
	send := func(payload map[string]any) error {
		stdinMu.Lock()
		defer stdinMu.Unlock()
		return writePiRPC(stdin, payload)
	}

	if err := send(map[string]any{"id": "prompt-1", "type": "prompt", "message": prompt}); err != nil {
		_ = process.Process.Kill()
		_ = process.Wait()
		return "", fmt.Errorf("send Pi RPC prompt: %w", err)
	}

	settled := false
	promptAccepted := false
	promptFailed := ""
	for {
		select {
		case <-ctx.Done():
			_ = send(map[string]any{"type": "abort"})
			_ = stdin.Close()
			_ = process.Wait()
			return output.String(), ctx.Err()
		case line, ok := <-events:
			if !ok {
				waitErr := process.Wait()
				_ = stdin.Close()
				if settled {
					return output.String(), nil
				}
				if waitErr != nil {
					return output.String(), fmt.Errorf("Pi RPC exited before the run settled: %w", waitErr)
				}
				return output.String(), errors.New("Pi RPC exited before the run settled")
			}
			line.Text = Redact(line.Text)
			options.OnLine(line)
			if output.Len() < maxCapturedOutput {
				remaining := maxCapturedOutput - output.Len()
				value := line.Text + "\n"
				if len(value) > remaining {
					value = value[:remaining]
				}
				output.WriteString(value)
			}
			kind, payload := decodePiRPCLine(line.Text)
			switch kind {
			case "extension_ui_request":
				handlePiExtensionUI(ctx, send, payload, options)
			case "response":
				if payload["command"] == "prompt" {
					if success, _ := payload["success"].(bool); success {
						promptAccepted = true
						break
					}
					promptFailed = piRPCError(payload)
				}
			case "agent_settled":
				settled = true
				if assistant.Len() > 0 {
					encoded, _ := json.Marshal(map[string]any{"type": "text", "text": assistant.String()})
					output.Write(encoded)
					output.WriteByte('\n')
				}
				_ = stdin.Close()
			case "message_update":
				if delta := piTextDelta(payload); delta != "" {
					assistant.WriteString(delta)
				}
			case "message_end":
				if text := piMessageText(payload["message"]); text != "" {
					assistant.Reset()
					assistant.WriteString(text)
				}
			}
			if promptFailed != "" {
				_ = stdin.Close()
				_ = process.Wait()
				return output.String(), fmt.Errorf("Pi RPC rejected the prompt: %s", promptFailed)
			}
			if settled && promptAccepted {
				_ = process.Wait()
				return output.String(), nil
			}
		}
	}
}

func writePiRPC(stdin io.Writer, payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = stdin.Write(append(encoded, '\n'))
	return err
}

func scanPiRPC(reader io.Reader, stream string, events chan<- OutputLine) {
	scanner := bufio.NewScanner(reader)
	scanner.Split(scanPiRPCLine)
	scanner.Buffer(make([]byte, 32*1024), 2*1024*1024)
	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		events <- OutputLine{Stream: stream, Text: text, Type: piOutputType(text)}
	}
}

func scanPiRPCLine(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if index := bytes.IndexByte(data, '\n'); index >= 0 {
		line := data[:index]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		return index + 1, line, nil
	}
	if atEOF && len(data) > 0 {
		line := data
		if line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		return len(data), line, nil
	}
	return 0, nil, nil
}

func decodePiRPCLine(line string) (string, map[string]any) {
	var payload map[string]any
	if json.Unmarshal([]byte(line), &payload) != nil {
		return "", nil
	}
	kind, _ := payload["type"].(string)
	return kind, payload
}

func piOutputType(line string) string {
	kind, payload := decodePiRPCLine(line)
	switch kind {
	case "tool_execution_start", "tool_execution_update", "tool_execution_end":
		if name, _ := payload["toolName"].(string); name != "" {
			return name
		}
		return "tool"
	case "message_update":
		if event, _ := payload["assistantMessageEvent"].(map[string]any); event != nil {
			if eventType, _ := event["type"].(string); strings.HasPrefix(eventType, "thinking") {
				return "thought"
			}
		}
		return "text"
	case "agent_start", "agent_end", "agent_settled", "turn_start", "turn_end":
		return kind
	default:
		if kind != "" {
			return kind
		}
		return "text"
	}
}

func piTextDelta(payload map[string]any) string {
	event, _ := payload["assistantMessageEvent"].(map[string]any)
	if event == nil {
		return ""
	}
	if event["type"] != "text_delta" {
		return ""
	}
	delta, _ := event["delta"].(string)
	return delta
}

func piMessageText(value any) string {
	message, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	var text strings.Builder
	appendContentBlocks(&text, message["content"])
	return strings.TrimSpace(text.String())
}

func piRPCError(payload map[string]any) string {
	if errText, ok := payload["error"].(string); ok && strings.TrimSpace(errText) != "" {
		return compact(Redact(errText))
	}
	if message, ok := payload["message"].(string); ok && strings.TrimSpace(message) != "" {
		return compact(Redact(message))
	}
	return "prompt was not accepted"
}

func handlePiExtensionUI(ctx context.Context, send func(map[string]any) error, payload map[string]any, options RunOptions) {
	method, _ := payload["method"].(string)
	id, _ := payload["id"].(string)
	switch method {
	case "notify", "setStatus", "setWidget", "setTitle", "set_editor_text", "setEditorText":
		return
	case "confirm":
		_ = send(piExtensionUIConfirmReply(ctx, id, payload, options))
	case "select", "input", "editor":
		if id != "" {
			_ = send(map[string]any{"type": "extension_ui_response", "id": id, "cancelled": true})
		}
	default:
		if id != "" {
			_ = send(map[string]any{"type": "extension_ui_response", "id": id, "cancelled": true})
		}
	}
}

func piExtensionUIConfirmReply(ctx context.Context, id string, payload map[string]any, options RunOptions) map[string]any {
	denied := map[string]any{"type": "extension_ui_response", "id": id, "cancelled": true}
	if options.OnPermission == nil {
		return denied
	}
	title, _ := payload["title"].(string)
	message, _ := payload["message"].(string)
	tool, _ := payload["tool"].(string)
	if tool == "" {
		tool = "bash"
	}
	toolCall, _ := json.Marshal(map[string]any{"tool": tool, "title": firstNonEmpty(title, message), "message": message})
	decision, err := options.OnPermission(ctx, PermissionRequest{ToolCall: toolCall})
	if err != nil || decision.Outcome != "selected" {
		return denied
	}
	return map[string]any{"type": "extension_ui_response", "id": id, "confirmed": true}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
