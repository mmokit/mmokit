package admin

import (
	"fmt"
	"sort"
	"sync"
)

// PanelDef declares a dashboard panel. The SPA fetches the registered set
// at boot and renders any panel reflectively from this metadata. Games add
// custom panels via mmokit.RegisterAdminPanel; pkg/admin registers builtins.
type PanelDef struct {
	ID            string   `json:"id"`                      // unique key; stable across releases
	Label         string   `json:"label"`                   // sidebar label
	Icon          string   `json:"icon"`                    // lucide icon name
	Group         string   `json:"group"`                   // sidebar group, e.g. "Cluster", "Diagnose", "Game"
	Topics        []string `json:"topics"`                  // SSE topic names this panel subscribes to
	Commands      []string `json:"commands"`                // cmdsys verbs this panel exposes as toolbar buttons
	InitialFetch  string   `json:"initialFetch,omitempty"`  // optional one-shot URL fetched at mount
	Component     string   `json:"component,omitempty"`     // optional named Svelte component override (v2)
	Visualization string   `json:"visualization,omitempty"` // optional "table" (default) | "chart"
}

// PanelRegistry holds the dashboard panel set. Builtins are registered by
// pkg/admin at server startup (RegisterBuiltinPanels — Task 18); games add
// custom panels via mmokit.RegisterAdminPanel.
type PanelRegistry struct {
	mu    sync.RWMutex
	defs  map[string]PanelDef
	order []string
}

func NewPanelRegistry() *PanelRegistry {
	return &PanelRegistry{defs: make(map[string]PanelDef)}
}

// Register adds a panel definition. Returns an error if def.ID is empty or
// already registered.
func (r *PanelRegistry) Register(def PanelDef) error {
	if def.ID == "" {
		return fmt.Errorf("admin: PanelDef.ID is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.defs[def.ID]; ok {
		return fmt.Errorf("admin: panel %q already registered", def.ID)
	}
	r.defs[def.ID] = def
	r.order = append(r.order, def.ID)
	return nil
}

// List returns all registered panels sorted by ID for stable ordering.
func (r *PanelRegistry) List() []PanelDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.order))
	ids = append(ids, r.order...)
	sort.Strings(ids)
	out := make([]PanelDef, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.defs[id])
	}
	return out
}
