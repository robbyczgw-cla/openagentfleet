package httpapi

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/collaborationmcp"
	"github.com/robbyczgw-cla/openagentfleet/internal/testexe"
)

func TestCollaborationMCPUsesBundledCommandOutsidePATH(t *testing.T) {
	command := testexe.WriteEcho(t, t.TempDir(), "collaboration-mcp", "ok")
	t.Setenv(collaborationmcp.MCPServerCommandEnv, command)
	t.Setenv("PATH", t.TempDir())
	server := &Server{RemoteToken: "controller"}
	spec, capability, err := server.collaborationMCPServerSpec()
	if err != nil {
		t.Fatal(err)
	}
	if spec.Command != command || capability == "" {
		t.Fatalf("bundled command resolution = %q, capability present = %t", spec.Command, capability != "")
	}
	if spec.Env[collaborationmcp.RunTokenEnv] != capability {
		t.Fatal("bridge capability missing from environment")
	}
	if spec.Env[collaborationmcp.APITokenEnv] != capability || spec.Env[collaborationmcp.APITokenEnv] == server.RemoteToken {
		t.Fatal("bridge received the controller bearer instead of its scoped capability")
	}
}

func TestCollaborationMCPRejectsMissingBundledCommand(t *testing.T) {
	command := filepath.Join(t.TempDir(), "missing-collaboration-mcp")
	t.Setenv(collaborationmcp.MCPServerCommandEnv, command)
	server := &Server{RemoteToken: "controller"}
	if _, _, err := server.collaborationMCPServerSpec(); err == nil || !strings.Contains(err.Error(), command) {
		t.Fatalf("missing bundled command error = %v", err)
	}
}

func TestCollaborationMCPRejectsTokenlessController(t *testing.T) {
	command := testexe.WriteEcho(t, t.TempDir(), "collaboration-mcp", "ok")
	server := &Server{CollaborationMCPCommand: command}
	if _, capability, err := server.collaborationMCPServerSpec(); err == nil || capability != "" || !strings.Contains(err.Error(), "controller bearer authentication") {
		t.Fatalf("tokenless bridge spec = capability %q, error %v", capability, err)
	}
}

func TestCollaborationMCPExplicitCommandOverridesBundledCommand(t *testing.T) {
	command := testexe.WriteEcho(t, t.TempDir(), "explicit-collaboration-mcp", "ok")
	t.Setenv(collaborationmcp.MCPServerCommandEnv, filepath.Join(t.TempDir(), "missing"))
	server := &Server{RemoteToken: "controller", CollaborationMCPCommand: command}
	spec, _, err := server.collaborationMCPServerSpec()
	if err != nil {
		t.Fatal(err)
	}
	if spec.Command != command {
		t.Fatalf("explicit command = %q, want %q", spec.Command, command)
	}
}
