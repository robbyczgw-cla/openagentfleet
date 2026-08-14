package harness

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var sentinelSecretNames = []string{
	"OPENAGENTFLEET_REMOTE_TOKEN",
	"OPENAGENTFLEET_FUTURE_SECRET",
	"OPENAGENTFLEET_ALLOW_HARNESS_EXECUTION",
	"OPENAGENTFLEET_ALLOW_HARNESS_WORKSPACE_WRITES",
	"OPENAGENTFLEET_STT_API_KEY",
	"OPENAI_API_KEY",
	"CODEX_ACCESS_TOKEN",
	"XAI_API_KEY",
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"CLAUDE_CODE_OAUTH_TOKEN",
	"CURSOR_API_KEY",
	"DEEPSEEK_API_KEY",
	"GOOGLE_API_KEY",
	"OPENROUTER_API_KEY",
	"OPENCODE_API_KEY",
	"AWS_SECRET_ACCESS_KEY",
	"GITHUB_TOKEN",
	"DATABASE_URL",
}

func TestProviderCommandEnvironmentIsExactAndCrossProviderIsolated(t *testing.T) {
	prepareSentinelEnvironment(t)

	baseNames := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
		"SHELL": true, "LANG": true, "TMPDIR": true, "SSH_AUTH_SOCK": true,
	}
	tests := []struct {
		provider string
		extra    []string
	}{
		{provider: "pi"},
		{provider: "claude", extra: []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CONFIG_DIR"}},
		{provider: "codex", extra: []string{"OPENAI_API_KEY", "CODEX_HOME", "CODEX_CA_CERTIFICATE"}},
		{provider: CodexAppServerProvider, extra: []string{"OPENAI_API_KEY", "CODEX_HOME", "CODEX_CA_CERTIFICATE"}},
		{provider: "grok", extra: []string{"XAI_API_KEY", "GROK_HOME"}},
		{provider: "opencode"},
		{provider: "cursor", extra: []string{"CURSOR_API_KEY"}},
		{provider: "unknown"},
	}

	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			command := newIsolatedCommand(test.provider, "/usr/bin/true")
			if command.Env == nil {
				t.Fatal("command environment must be explicit, never nil")
			}
			actual := environmentNameSet(command.Env)
			expected := cloneNameSet(baseNames)
			for _, name := range test.extra {
				expected[name] = true
			}
			assertExactEnvironmentNames(t, actual, expected)
			for _, name := range sentinelSecretNames {
				if expected[name] {
					continue
				}
				if actual[name] {
					t.Errorf("irrelevant subprocess received protected environment key %s", name)
				}
			}
		})
	}
}

func TestCredentialFreeCommandEnvironmentProtectsAllHarnessSecrets(t *testing.T) {
	prepareSentinelEnvironment(t)
	command := newIsolatedCommand("", "/usr/bin/true")
	actual := environmentNameSet(command.Env)
	for _, name := range sentinelSecretNames {
		if actual[name] {
			t.Errorf("credential-free subprocess received protected environment key %s", name)
		}
	}
}

func TestACPToolCommandFiltersRequestedEnvironment(t *testing.T) {
	prepareSentinelEnvironment(t)
	requested := map[string]string{
		"PATH":                         "/requested/bin",
		"CI":                           "1",
		"FORCE_COLOR":                  "1",
		"HOME":                         "/untrusted/home",
		"SSH_AUTH_SOCK":                "/untrusted/agent",
		"OPENAGENTFLEET_REMOTE_TOKEN":  "sentinel-requested-remote-token",
		"OPENAGENTFLEET_FUTURE_SECRET": "sentinel-requested-future-secret",
		"OPENAGENTFLEET_STT_API_KEY":   "sentinel-requested-stt-key",
		"OPENAI_API_KEY":               "sentinel-requested-openai-key",
		"CODEX_ACCESS_TOKEN":           "sentinel-requested-codex-token",
		"XAI_API_KEY":                  "sentinel-requested-xai-key",
		"ANTHROPIC_API_KEY":            "sentinel-requested-anthropic-key",
		"ANTHROPIC_AUTH_TOKEN":         "sentinel-requested-anthropic-token",
		"CLAUDE_CODE_OAUTH_TOKEN":      "sentinel-requested-claude-token",
		"CURSOR_API_KEY":               "sentinel-requested-cursor-key",
		"DEEPSEEK_API_KEY":             "sentinel-requested-deepseek-key",
		"OPENROUTER_API_KEY":           "sentinel-requested-openrouter-key",
		"AWS_SECRET_ACCESS_KEY":        "sentinel-requested-aws-key",
		"GITHUB_TOKEN":                 "sentinel-requested-github-token",
		"NODE_OPTIONS":                 "--require=/untrusted/module.js",
		"DYLD_INSERT_LIBRARIES":        "/untrusted/library.dylib",
		"HTTP_PROXY":                   "http://user:password@proxy.invalid",
	}
	command := newToolCommandContext(t.Context(), "/usr/bin/true", nil, requested)
	actual := environmentMap(command.Env)
	if actual["PATH"] != "/requested/bin" {
		t.Error("safe requested PATH was not applied")
	}
	if actual["CI"] != "1" || actual["FORCE_COLOR"] != "1" {
		t.Error("safe requested tool environment was not applied")
	}
	if actual["HOME"] != "/sentinel/home" {
		t.Error("ACP request overrode protected HOME")
	}
	if actual["SSH_AUTH_SOCK"] != "/sentinel/ssh-agent" {
		t.Error("ACP request overrode inherited SSH agent location")
	}
	for _, name := range []string{
		"OPENAGENTFLEET_REMOTE_TOKEN", "OPENAGENTFLEET_FUTURE_SECRET", "OPENAGENTFLEET_STT_API_KEY",
		"OPENAI_API_KEY", "CODEX_ACCESS_TOKEN", "XAI_API_KEY",
		"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CURSOR_API_KEY",
		"DEEPSEEK_API_KEY", "OPENROUTER_API_KEY",
		"AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN", "NODE_OPTIONS",
		"DYLD_INSERT_LIBRARIES", "HTTP_PROXY",
	} {
		if _, ok := actual[name]; ok {
			t.Errorf("ACP tool command accepted protected environment key %s", name)
		}
	}
}

func TestHarnessProcessCreationIsCentralized(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fileset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "environment.go" {
			continue
		}
		file, err := parser.ParseFile(fileset, filepath.Clean(name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		execAliases := importAliases(file, "os/exec", "exec")
		osAliases := importAliases(file, "os", "os")
		syscallAliases := importAliases(file, "syscall", "syscall")
		unixAliases := importAliases(file, "golang.org/x/sys/unix", "unix")
		ast.Inspect(file, func(node ast.Node) bool {
			if literal, ok := node.(*ast.CompositeLit); ok {
				if selector, ok := literal.Type.(*ast.SelectorExpr); ok && selector.Sel.Name == "Cmd" && selectorUsesAlias(selector, execAliases) {
					position := fileset.Position(literal.Pos())
					t.Errorf("direct exec.Cmd construction bypasses environment isolation at %s:%d", name, position.Line)
				}
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if identifier, ok := call.Fun.(*ast.Ident); ok && execAliases["."] && (identifier.Name == "Command" || identifier.Name == "CommandContext") {
				position := fileset.Position(call.Pos())
				t.Errorf("dot-imported exec.%s bypasses environment isolation at %s:%d", identifier.Name, name, position.Line)
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if selectorUsesAlias(selector, execAliases) && (selector.Sel.Name == "Command" || selector.Sel.Name == "CommandContext") {
				position := fileset.Position(call.Pos())
				t.Errorf("direct exec.%s bypasses environment isolation at %s:%d", selector.Sel.Name, name, position.Line)
			}
			if selectorUsesAlias(selector, osAliases) && selector.Sel.Name == "StartProcess" {
				position := fileset.Position(call.Pos())
				t.Errorf("direct os.StartProcess bypasses environment isolation at %s:%d", name, position.Line)
			}
			if (selectorUsesAlias(selector, syscallAliases) || selectorUsesAlias(selector, unixAliases)) &&
				(selector.Sel.Name == "Exec" || selector.Sel.Name == "ForkExec" || selector.Sel.Name == "StartProcess") {
				position := fileset.Position(call.Pos())
				t.Errorf("direct low-level process spawn bypasses environment isolation at %s:%d", name, position.Line)
			}
			return true
		})
	}
}

func importAliases(file *ast.File, importPath, defaultName string) map[string]bool {
	aliases := make(map[string]bool)
	for _, specification := range file.Imports {
		path, err := strconv.Unquote(specification.Path.Value)
		if err != nil || path != importPath {
			continue
		}
		name := defaultName
		if specification.Name != nil {
			name = specification.Name.Name
		}
		aliases[name] = true
	}
	return aliases
}

func selectorUsesAlias(selector *ast.SelectorExpr, aliases map[string]bool) bool {
	identifier, ok := selector.X.(*ast.Ident)
	return ok && aliases[identifier.Name]
}

func prepareSentinelEnvironment(t *testing.T) {
	for _, name := range inheritedEnvironmentNames {
		t.Setenv(name, "")
	}
	for _, names := range providerEnvironmentNames {
		for _, name := range names {
			t.Setenv(name, "")
		}
	}
	for _, name := range sentinelSecretNames {
		t.Setenv(name, "sentinel-protected-value")
	}

	t.Setenv("PATH", "/sentinel/bin")
	t.Setenv("HOME", "/sentinel/home")
	t.Setenv("USER", "sentinel-user")
	t.Setenv("LOGNAME", "sentinel-user")
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("TMPDIR", "/sentinel/tmp")
	t.Setenv("SSH_AUTH_SOCK", "/sentinel/ssh-agent")
	t.Setenv("CLAUDE_CONFIG_DIR", "/sentinel/claude")
	t.Setenv("CODEX_HOME", "/sentinel/codex")
	t.Setenv("CODEX_CA_CERTIFICATE", "/sentinel/codex-ca.pem")
	t.Setenv("GROK_HOME", "/sentinel/grok")
}

func environmentNameSet(environment []string) map[string]bool {
	result := make(map[string]bool, len(environment))
	for _, item := range environment {
		name, _, found := strings.Cut(item, "=")
		if found {
			result[name] = true
		}
	}
	return result
}

func environmentMap(environment []string) map[string]string {
	result := make(map[string]string, len(environment))
	for _, item := range environment {
		name, value, found := strings.Cut(item, "=")
		if found {
			result[name] = value
		}
	}
	return result
}

func cloneNameSet(source map[string]bool) map[string]bool {
	result := make(map[string]bool, len(source))
	for name, value := range source {
		result[name] = value
	}
	return result
}

func assertExactEnvironmentNames(t *testing.T, actual, expected map[string]bool) {
	t.Helper()
	for name := range expected {
		if !actual[name] {
			t.Errorf("expected environment key %s is missing", name)
		}
	}
	for name := range actual {
		if !expected[name] {
			t.Errorf("unexpected environment key %s is present", name)
		}
	}
}
