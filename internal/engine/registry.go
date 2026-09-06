package engine

import (
	"fmt"
	"sort"
	"sync"
)

// Registry holds interchangeable adapters. It is the lookup used by the
// coordinator; Agent metadata still stores only the engine id string.
type Registry struct {
	mu       sync.RWMutex
	adapters map[ID]Adapter
}

func NewRegistry() *Registry {
	return &Registry{adapters: make(map[ID]Adapter)}
}

func (r *Registry) Register(adapter Adapter) error {
	if r == nil {
		return fmt.Errorf("engine registry is required")
	}
	if adapter == nil {
		return fmt.Errorf("engine adapter is required")
	}
	id := adapter.ID()
	if id == "" {
		return fmt.Errorf("engine adapter id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[id] = adapter
	return nil
}

func (r *Registry) Get(id ID) (Adapter, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	adapter, ok := r.adapters[id]
	return adapter, ok
}

func (r *Registry) List() []Adapter {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Adapter, 0, len(r.adapters))
	for _, adapter := range r.adapters {
		result = append(result, adapter)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID() < result[j].ID()
	})
	return result
}
