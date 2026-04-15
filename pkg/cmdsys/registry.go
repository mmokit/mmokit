package cmdsys

import (
	"fmt"
	"sync"
)

// Registry holds registered command definitions keyed by verb.
type Registry struct {
	mu       sync.RWMutex
	commands map[string]Command
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{commands: make(map[string]Command)}
}

// Register adds a command to the registry. It computes ArgsSchemaHash and
// ResultSchemaHash from the command's Args and Result zero values at
// registration time. Returns an error on duplicate verbs or schema errors.
func (r *Registry) Register(cmd Command) error {
	if cmd.Args != nil {
		h, err := schemaHashOf(cmd.Args)
		if err != nil {
			return fmt.Errorf("registry.Register %q: args schema: %w", cmd.Verb, err)
		}
		cmd.ArgsSchemaHash = h
	}
	if cmd.Result != nil {
		h, err := schemaHashOf(cmd.Result)
		if err != nil {
			return fmt.Errorf("registry.Register %q: result schema: %w", cmd.Verb, err)
		}
		cmd.ResultSchemaHash = h
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.commands[cmd.Verb]; exists {
		return fmt.Errorf("registry.Register: duplicate verb %q", cmd.Verb)
	}
	r.commands[cmd.Verb] = cmd
	return nil
}

// Lookup returns the command for the given verb and a boolean indicating
// whether it was found. Returns standard Go map-access idiom (value, ok).
func (r *Registry) Lookup(verb string) (Command, bool) {
	r.mu.RLock()
	c, ok := r.commands[verb]
	r.mu.RUnlock()
	return c, ok
}

// List returns all registered verbs in an unspecified order.
func (r *Registry) List() []string {
	r.mu.RLock()
	out := make([]string, 0, len(r.commands))
	for v := range r.commands {
		out = append(out, v)
	}
	r.mu.RUnlock()
	return out
}
