package service

import (
	"fmt"
	"sort"
	"sync"
)

// Registry is a process-local catalog of registered Kinds. Built up by
// Coordinator.RegisterService(...) before Build(). Validation runs at
// Build to ensure no two kinds claim the same op code.
type Registry struct {
	mu    sync.RWMutex
	kinds map[string]Kind
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{kinds: map[string]Kind{}}
}

// Register adds a Kind. Returns an error on duplicate Name or empty
// required fields. Op-code overlap is validated by Validate(), not here,
// so order of Register calls doesn't matter.
func (r *Registry) Register(k Kind) error {
	if k.Name == "" {
		return fmt.Errorf("service.Register: Name is required")
	}
	if k.Factory == nil {
		return fmt.Errorf("service.Register: Factory is required for kind %q", k.Name)
	}
	if len(k.OpCodes) == 0 {
		return fmt.Errorf("service.Register: at least one OpCode is required for kind %q", k.Name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.kinds[k.Name]; exists {
		return fmt.Errorf("service.Register: duplicate kind name %q", k.Name)
	}
	r.kinds[k.Name] = k
	return nil
}

// Get returns a registered kind by name.
func (r *Registry) Get(name string) (Kind, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.kinds[name]
	return k, ok
}

// Names returns all registered kind names in sorted order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.kinds))
	for n := range r.kinds {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// All returns a defensive snapshot of all registered Kinds, ordered by Name.
func (r *Registry) All() []Kind {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.kinds))
	for n := range r.kinds {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Kind, 0, len(names))
	for _, n := range names {
		out = append(out, r.kinds[n])
	}
	return out
}

// Validate ensures no two kinds share an op code and any RequiresDB
// kind has DB available. Called by Coordinator at Build time. Returns
// the first violation as an error.
func (r *Registry) Validate(dbConfigured bool) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	codeOwner := map[uint32]string{}
	// Iterate kinds in name order so error messages are deterministic.
	names := make([]string, 0, len(r.kinds))
	for n := range r.kinds {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		k := r.kinds[name]
		if k.RequiresDB && !dbConfigured {
			return fmt.Errorf("service.Validate: kind %q requires DB but Config.PostgresURL is empty", k.Name)
		}
		for _, code := range k.OpCodes {
			if owner, exists := codeOwner[code]; exists {
				return fmt.Errorf("service.Validate: op code %d claimed by both %q and %q", code, owner, k.Name)
			}
			codeOwner[code] = k.Name
		}
	}
	return nil
}

// SelectKinds returns the kinds whose names appear in the requested list.
// Returns an error if any requested name is not registered.
func (r *Registry) SelectKinds(names []string) ([]Kind, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Kind, 0, len(names))
	for _, n := range names {
		k, ok := r.kinds[n]
		if !ok {
			return nil, fmt.Errorf("service.SelectKinds: kind %q is not registered", n)
		}
		out = append(out, k)
	}
	return out, nil
}
