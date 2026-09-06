package computer

import (
	"fmt"
	"sync"
)

// Registry holds computer backends keyed by id. The Agent domain looks up a
// backend here instead of calling the local OS.
type Registry struct {
	mu       sync.RWMutex
	backends map[string]Backend
}

func NewRegistry() *Registry {
	return &Registry{backends: make(map[string]Backend)}
}

func (r *Registry) Register(backend Backend) error {
	if backend == nil {
		return fmt.Errorf("computer backend is required")
	}
	id := backend.ID()
	if id == "" {
		return fmt.Errorf("computer backend id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backends[id] = backend
	return nil
}

func (r *Registry) Get(id string) (Backend, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	backend, ok := r.backends[id]
	return backend, ok
}

func (r *Registry) List() []Backend {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Backend, 0, len(r.backends))
	for _, backend := range r.backends {
		result = append(result, backend)
	}
	return result
}
