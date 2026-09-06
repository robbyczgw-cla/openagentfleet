// Package computer is the Agent Computer backend contract. The Agent domain
// must not call the local OS, Docker, or a remote worker directly.
package computer

import "context"

// Kind identifies a backend implementation. remote is reserved; this package
// does not implement a remote stack.
type Kind string

const (
	KindNative Kind = "native"
	KindDocker Kind = "docker"
	KindRemote Kind = "remote"
)

// Backend is the runtime-neutral Agent Computer surface.
type Backend interface {
	ID() string
	Kind() Kind
	Start(context.Context) error
	Stop(context.Context) error
	Health(context.Context) (Health, error)
	Capabilities() Capabilities
	Exec(context.Context, ExecRequest) (ExecResult, error)
}

// ExecRequest is argv, never a shell string.
type ExecRequest struct {
	Argv    []string
	Workdir string
}

// ExecResult is captured command output. Execution errors are returned
// separately so callers can inspect output after a non-zero exit.
type ExecResult struct {
	Output string
}

// Health is a product-facing lifecycle snapshot.
type Health struct {
	State  string
	Ready  bool
	Detail string
}

// Capabilities are declared by a backend, never inferred by the Agent domain.
type Capabilities struct {
	Exec    bool
	Files   bool
	Browser bool
	Screen  bool
	Remote  bool
}

var (
	_ Backend = (*NativeBackend)(nil)
	_ Backend = (*DockerBackend)(nil)
)
