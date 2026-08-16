package harness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func installedPiBinary(t *testing.T) string {
	t.Helper()
	if bundled := strings.TrimSpace(os.Getenv("OPENAGENTFLEET_PI_BINARY")); bundled != "" {
		if _, err := os.Stat(bundled); err == nil {
			return bundled
		}
		if os.Getenv("OPENAGENTFLEET_RUN_PI_RPC_SMOKE") == "1" {
			t.Fatalf("OPENAGENTFLEET_PI_BINARY=%q is not a file", bundled)
		}
	}
	path, err := exec.LookPath("pi")
	if err == nil {
		return path
	}
	if os.Getenv("OPENAGENTFLEET_RUN_PI_RPC_SMOKE") == "1" {
		t.Fatal("set OPENAGENTFLEET_PI_BINARY or put pi on PATH to run live Pi RPC smoke tests")
	}
	t.Skip("live Pi RPC tests need an installed pi; set OPENAGENTFLEET_PI_BINARY or OPENAGENTFLEET_RUN_PI_RPC_SMOKE=1")
	return ""
}

func isolatePiHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
}

func TestInstalledPiVersion(t *testing.T) {
	binary := installedPiBinary(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("pi --version: %v\n%s", err, output)
	}
	if strings.TrimSpace(string(output)) == "" {
		t.Fatalf("empty version output: %q", output)
	}
}

func TestLivePiRPCWorkerRejectsUnauthenticatedPromptWithoutSession(t *testing.T) {
	binary := installedPiBinary(t)
	isolatePiHome(t)
	t.Setenv("OPENAGENTFLEET_PI_BINARY", binary)

	workdir := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	_, err := (&Runner{AllowExecution: true}).RunWithOptions(ctx, "pi", "Reply with exactly PI_SMOKE_OK. Do not use tools.", workdir, RunOptions{
		PermissionMode: "read_only",
	})
	if err == nil {
		t.Fatal("unauthenticated live Pi run succeeded; isolate HOME so this cannot spend a provider token")
	}
	if !strings.Contains(err.Error(), "Pi RPC rejected the prompt") {
		t.Fatalf("error = %v, want rejected prompt from live RPC", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "exited before the run settled") {
		t.Fatalf("live Pi exited without an RPC prompt response: %v", err)
	}
	assertNoPiSessionFiles(t, workdir)
}

func TestLivePiRPCLeadAskLoadsBundledExtensionThenRejectsAuth(t *testing.T) {
	binary := installedPiBinary(t)
	isolatePiHome(t)
	t.Setenv("OPENAGENTFLEET_PI_BINARY", binary)

	command, err := BuildCommandWithOptions("pi", "must not appear", t.TempDir(), CommandOptions{
		Role:           "lead",
		PermissionMode: "ask",
	})
	if err != nil {
		t.Fatal(err)
	}
	if command.Program != binary {
		t.Fatalf("program = %q, want %q", command.Program, binary)
	}
	if !containsFlag(command.Args, "-e") || !strings.Contains(strings.Join(command.Args, " "), "bash") {
		t.Fatalf("lead ask args = %#v", command.Args)
	}

	workdir := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	_, runErr := (&Runner{AllowExecution: true}).RunWithOptions(ctx, "pi", "Do not run bash. Reply PI_SMOKE_OK.", workdir, RunOptions{
		Role:           "lead",
		PermissionMode: "ask",
		OnPermission: func(context.Context, PermissionRequest) (PermissionDecision, error) {
			t.Fatal("live unauthenticated run must not reach a bash approval")
			return PermissionDecision{}, nil
		},
	})
	if runErr == nil || !strings.Contains(runErr.Error(), "Pi RPC rejected the prompt") {
		t.Fatalf("error = %v", runErr)
	}
	assertNoPiSessionFiles(t, workdir)
}

func TestLivePiRPCLeadWorkspaceDoesNotGrantBash(t *testing.T) {
	_ = installedPiBinary(t)
	command, err := BuildCommandWithOptions("pi", "prompt", t.TempDir(), CommandOptions{
		Role:           "lead",
		PermissionMode: "workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(command.Args, " ")
	if strings.Contains(joined, "bash") || containsFlag(command.Args, "-e") {
		t.Fatalf("lead workspace live command = %#v", command.Args)
	}
}

func assertNoPiSessionFiles(t *testing.T, workdir string) {
	t.Helper()
	err := filepath.Walk(workdir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := strings.ToLower(info.Name())
		if strings.Contains(name, "session") || strings.HasSuffix(name, ".jsonl") {
			t.Errorf("Pi wrote a session-like file despite --no-session: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
