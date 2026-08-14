package harness

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"sort"
	"strings"
)

// inheritedEnvironmentNames is the complete ambient environment allowlist for
// harness subprocesses. Keep this deliberately small: anything omitted here is
// unavailable to child processes unless it is explicitly assigned to the
// matching provider below.
var inheritedEnvironmentNames = []string{
	"PATH",
	"HOME",
	"USER",
	"LOGNAME",
	"SHELL",
	"LANG",
	"LANGUAGE",
	"LC_ALL",
	"LC_COLLATE",
	"LC_CTYPE",
	"LC_MESSAGES",
	"LC_MONETARY",
	"LC_NUMERIC",
	"LC_TIME",
	"TMPDIR",
	"TMP",
	"TEMP",
	"SSH_AUTH_SOCK",
	"SSH_AGENT_PID",
	"TERM",
	"COLORTERM",
	"NO_COLOR",
	"__CF_USER_TEXT_ENCODING",
	"SSL_CERT_FILE",
	"SSL_CERT_DIR",
	"NODE_EXTRA_CA_CERTS",
}

// providerEnvironmentNames contains credentials and runtime/config locations
// that are meaningful only to one provider. Multi-provider workers (Pi and
// OpenCode) intentionally receive no ambient model-provider keys: their saved
// authentication remains available through HOME, while future per-model key
// injection must name the selected underlying provider explicitly.
var providerEnvironmentNames = map[string][]string{
	"claude": {
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"CLAUDE_CODE_OAUTH_TOKEN",
		"CLAUDE_CONFIG_DIR",
	},
	"codex": {
		"OPENAI_API_KEY",
		"CODEX_HOME",
		"CODEX_CA_CERTIFICATE",
	},
	CodexAppServerProvider: {
		"OPENAI_API_KEY",
		"CODEX_HOME",
		"CODEX_CA_CERTIFICATE",
	},
	"grok": {
		"XAI_API_KEY",
		"GROK_HOME",
	},
	"cursor": {
		"CURSOR_API_KEY",
	},
}

// requestedToolEnvironmentNames is the small subset of ACP terminal/create
// environment entries that may override the clean baseline. Credentials,
// HOME, SSH_AUTH_SOCK, OPENAGENTFLEET_* and arbitrary runtime injection variables are
// intentionally impossible to forward through this path.
var requestedToolEnvironmentNames = map[string]struct{}{
	"PATH":        {},
	"TMPDIR":      {},
	"TMP":         {},
	"TEMP":        {},
	"TERM":        {},
	"COLORTERM":   {},
	"NO_COLOR":    {},
	"FORCE_COLOR": {},
	"CI":          {},
}

func newIsolatedCommand(provider, program string, args ...string) *exec.Cmd {
	command := exec.Command(program, args...)
	configureCommandEnvironment(command, provider, nil)
	return command
}

func newIsolatedCommandContext(ctx context.Context, provider, program string, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, program, args...)
	configureCommandEnvironment(command, provider, nil)
	return command
}

func newToolCommandContext(ctx context.Context, program string, args []string, requested map[string]string) *exec.Cmd {
	command := exec.CommandContext(ctx, program, args...)
	configureCommandEnvironment(command, "", requested)
	return command
}

func configureCommandEnvironment(command *exec.Cmd, provider string, requested map[string]string) {
	values := make(map[string]string)
	for _, name := range inheritedEnvironmentNames {
		inheritEnvironment(values, name)
	}
	ensureMacOSCLIEnvironment(values)
	for _, name := range providerEnvironmentNames[provider] {
		inheritEnvironment(values, name)
	}
	for name, value := range requested {
		if _, allowed := requestedToolEnvironmentNames[name]; allowed {
			values[name] = value
		}
	}

	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	command.Env = make([]string, 0, len(names))
	for _, name := range names {
		command.Env = append(command.Env, name+"="+values[name])
	}
}

func inheritEnvironment(values map[string]string, name string) {
	if value, ok := os.LookupEnv(name); ok && value != "" {
		values[name] = value
	}
}

func ensureMacOSCLIEnvironment(values map[string]string) {
	if values["PATH"] == "" {
		if runtime.GOOS == "darwin" {
			values["PATH"] = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
		} else {
			values["PATH"] = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
		}
	}
	if values["HOME"] == "" {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			values["HOME"] = home
		}
	}
	if values["USER"] == "" || values["LOGNAME"] == "" {
		if current, err := user.Current(); err == nil && current.Username != "" {
			if values["USER"] == "" {
				values["USER"] = current.Username
			}
			if values["LOGNAME"] == "" {
				values["LOGNAME"] = current.Username
			}
		}
	}
	if values["TMPDIR"] == "" {
		values["TMPDIR"] = os.TempDir()
	}
}
