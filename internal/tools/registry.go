package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Registry is the in-process catalog of canonical tools. Lookup accepts both
// the namespaced name and any registered alias; both resolve to the same Tool.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	names map[string]string
	order []string
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
		names: make(map[string]string),
	}
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return New() }

// NewFromCatalog registers DefaultCatalog, including original MCP aliases.
func NewFromCatalog() (*Registry, error) {
	registry := New()
	for _, tool := range DefaultCatalog() {
		if err := registry.Register(tool); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// NewDefaultRegistry registers DefaultCatalog. Catalog collisions panic because
// they are a static programmer error.
func NewDefaultRegistry() *Registry {
	registry, err := NewFromCatalog()
	if err != nil {
		panic(err)
	}
	return registry
}

// Register stores a tool under its canonical name and aliases. Aliases point
// at the stored tool; they are not separate tools. A later Register of the
// same name or alias is rejected.
func (r *Registry) Register(tool Tool) error {
	if r == nil {
		return fmt.Errorf("%w: registry is nil", ErrInvalidTool)
	}
	normalized, err := validateTool(tool)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.names[normalized.Name]; exists {
		return fmt.Errorf("%w: %s", ErrAlreadyRegistered, normalized.Name)
	}
	for _, alias := range normalized.Aliases {
		if _, exists := r.names[alias]; exists {
			return fmt.Errorf("%w: %s", ErrAlreadyRegistered, alias)
		}
	}

	stored := cloneTool(normalized)
	r.tools[stored.Name] = stored
	r.names[stored.Name] = stored.Name
	for _, alias := range stored.Aliases {
		r.names[alias] = stored.Name
	}
	r.order = append(r.order, stored.Name)
	return nil
}

// Lookup resolves a canonical name or alias to the stored tool. The returned
// Tool.Name is always the canonical name.
func (r *Registry) Lookup(name string) (Tool, bool) {
	if r == nil {
		return Tool{}, false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Tool{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	canonical, ok := r.names[name]
	if !ok {
		return Tool{}, false
	}
	tool, ok := r.tools[canonical]
	if !ok {
		return Tool{}, false
	}
	return cloneTool(tool), true
}

// List returns canonical tools in registration order.
func (r *Registry) List() []Tool {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		tool, ok := r.tools[name]
		if !ok {
			continue
		}
		out = append(out, cloneTool(tool))
	}
	return out
}

// Execute looks up name (canonical or alias), rejects missing capabilities,
// rejects missing JSON Schema required keys, then invokes Tool.Execute.
// A definition-only tool (nil Execute) returns ErrExecuteUnbound after those
// checks.
func (r *Registry) Execute(ctx ExecutionContext, name string, input json.RawMessage) (Result, error) {
	if r == nil {
		return Result{}, unknownToolError(name)
	}
	name = strings.TrimSpace(name)
	r.mu.RLock()
	canonical, ok := r.names[name]
	var tool Tool
	if ok {
		tool, ok = r.tools[canonical]
		if ok {
			tool = cloneTool(tool)
		}
	}
	r.mu.RUnlock()
	if !ok {
		return Result{}, unknownToolError(name)
	}

	if missing := missingCapabilities(tool.RequiredCapabilities, ctx.GrantedCapabilities); len(missing) > 0 {
		return Result{}, fmt.Errorf("%w: %s requires %s", ErrCapabilityDenied, tool.Name, joinQuoted(missing))
	}
	if missing := missingRequiredKeys(tool.InputSchema, input); len(missing) > 0 {
		return Result{}, fmt.Errorf("%w: %s missing required %s", ErrInvalidInput, tool.Name, joinQuoted(missing))
	}
	if tool.Execute == nil {
		return Result{}, fmt.Errorf("%w: %s", ErrExecuteUnbound, tool.Name)
	}
	return tool.Execute(ctx, cloneRaw(input))
}

func unknownToolError(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrUnknownTool
	}
	return fmt.Errorf("%w: %s", ErrUnknownTool, name)
}
