package mmokit

import (
	"maps"
	"sort"
	"sync"

	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/tunable"
	"github.com/zenion/mmoserver/pkg/universe"
)

func init() {
	universe.OnCellSystemsReady = func(p *universe.Process, cell *universe.Cell) {
		SyncCellTunables(p, cell)
	}
}

// tunableSourceFor resolves a system to a tunable.Source: the wasm adapter
// implements Source directly; a native struct with tune-tagged fields is
// wrapped via reflection; anything else reports false.
func tunableSourceFor(sys engine.System) (tunable.Source, bool) {
	if s, ok := sys.(tunable.Source); ok {
		return s, true
	}
	if tunable.HasTunables(sys) {
		return tunable.Reflect(sys), true
	}
	return nil, false
}

// tuneSystem holds the descriptors + intended values for one named system.
type tuneSystem struct {
	defs   []tunable.Def     // descriptor order (kind/bounds/default)
	values map[string]string // field → current intended value
}

// tuneRegistry is the per-Process source of truth for intended tunable values.
type tuneRegistry struct {
	mu      sync.Mutex
	systems map[string]*tuneSystem
}

func newTuneRegistry() *tuneRegistry {
	return &tuneRegistry{systems: map[string]*tuneSystem{}}
}

// harvest records descriptors for a system the first time it is seen, seeding
// intended values from each descriptor's current Value.
func (r *tuneRegistry) harvest(name string, defs []tunable.Def) {
	if len(defs) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.systems[name]; ok {
		return
	}
	ts := &tuneSystem{values: map[string]string{}}
	for _, d := range defs {
		ts.defs = append(ts.defs, d)
		ts.values[d.Name] = d.Value
	}
	r.systems[name] = ts
}

func (r *tuneRegistry) setValue(name, field, value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ts, ok := r.systems[name]; ok {
		ts.values[field] = value
	}
}

func (r *tuneRegistry) value(name, field string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ts, ok := r.systems[name]
	if !ok {
		return "", false
	}
	v, ok := ts.values[field]
	return v, ok
}

// defs returns descriptor copies with Value set to the registry's current
// intended value.
func (r *tuneRegistry) defs(name string) []tunable.Def {
	r.mu.Lock()
	defer r.mu.Unlock()
	ts, ok := r.systems[name]
	if !ok {
		return nil
	}
	out := make([]tunable.Def, len(ts.defs))
	for i, d := range ts.defs {
		d.Value = ts.values[d.Name]
		out[i] = d
	}
	return out
}

// systemNames returns the sorted set of registered tunable system names.
func (r *tuneRegistry) systemNames() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.systems))
	for k := range r.systems {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// snapshotValues returns a copy of a system's intended values.
func (r *tuneRegistry) snapshotValues(name string) map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ts, ok := r.systems[name]
	if !ok {
		return nil
	}
	out := make(map[string]string, len(ts.values))
	maps.Copy(out, ts.values)
	return out
}

// ── per-Process registry map ────────────────────────────────────────────────

var (
	tuneRegMu  sync.Mutex
	tuneRegMap = map[*universe.Process]*tuneRegistry{}
)

func tuneRegistryFor(p *universe.Process) *tuneRegistry {
	tuneRegMu.Lock()
	defer tuneRegMu.Unlock()
	r, ok := tuneRegMap[p]
	if !ok {
		r = newTuneRegistry()
		tuneRegMap[p] = r
	}
	return r
}

// syncSource brings one resolved tunable source into agreement with the
// registry: it applies tag defaults to the live instance (native sources only —
// the wasm adapter seeds defaults from the guest), harvests its descriptors
// into the registry on first sight, then writes the registry's intended values
// back onto the instance. Applying defaults BEFORE harvest ensures a native
// system's tag defaults (not its Go zero values) become the registry baseline.
func syncSource(reg *tuneRegistry, name string, src tunable.Source) {
	if ad, ok := src.(interface{ ApplyDefaults() }); ok {
		ad.ApplyDefaults()
	}
	reg.harvest(name, src.Tunables())
	for field, val := range reg.snapshotValues(name) {
		_ = src.Set(field, val)
	}
}

// SyncCellTunables harvests every tunable system in the cell into the process
// registry (once per system name) and applies the registry's intended values
// to each live instance. Runs on the cell's loop goroutine — call inside
// RunOnLoop or from a loop-safe context (cell bootstrap / post-set).
func SyncCellTunables(p *universe.Process, cell *universe.Cell) {
	reg := tuneRegistryFor(p)
	cell.Loop.EachSystem(func(name string, sys engine.System) {
		if src, ok := tunableSourceFor(sys); ok {
			syncSource(reg, name, src)
		}
	})
}
