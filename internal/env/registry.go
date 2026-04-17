package env

import (
	"fmt"
	"sync"
)

// Registry holds the set of Environment adapters registered with the server.
// Registration is one-shot at startup; lookup is lock-free-ish after that.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Environment
}

func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]Environment)}
}

// Register adds an adapter. Panics on duplicate — adapters are registered at
// startup, and a duplicate is a programmer error worth failing fast.
func (r *Registry) Register(a Environment) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := a.RuntimeID()
	if _, exists := r.adapters[id]; exists {
		panic(fmt.Sprintf("env: duplicate adapter registration %q", id))
	}
	r.adapters[id] = a
}

// Get returns the adapter for id, or an error if none is registered.
func (r *Registry) Get(id string) (Environment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[id]
	if !ok {
		return nil, fmt.Errorf("env: no adapter registered for %q", id)
	}
	return a, nil
}

// List returns the registered adapter IDs in arbitrary order.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.adapters))
	for id := range r.adapters {
		ids = append(ids, id)
	}
	return ids
}
