package universe

import (
	"sort"
	"sync"
)

// serviceEventRouter is the coord-side routing table for the typed
// service event bus. Maps fully-qualified Go type names to the list of
// process IDs that subscribed to them.
//
// Whole-set replace: every ServiceEventSubscribe message replaces the
// entire entry for the originating process. UpdateProcess is the only
// mutator.
//
// Snapshot returns a defensive copy used to populate
// PeerList.event_routing on each broadcast.
type serviceEventRouter struct {
	mu sync.RWMutex
	// procToTypes[procID] = set of subscribed type names (deduped via map)
	procToTypes map[string]map[string]struct{}
}

func newServiceEventRouter() *serviceEventRouter {
	return &serviceEventRouter{
		procToTypes: map[string]map[string]struct{}{},
	}
}

// UpdateProcess replaces the recorded type set for procID with typeNames.
// Empty typeNames removes the process entirely (equivalent to RemoveProcess).
func (r *serviceEventRouter) UpdateProcess(procID string, typeNames []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(typeNames) == 0 {
		delete(r.procToTypes, procID)
		return
	}
	set := make(map[string]struct{}, len(typeNames))
	for _, n := range typeNames {
		set[n] = struct{}{}
	}
	r.procToTypes[procID] = set
}

// RemoveProcess drops procID from the routing table. Idempotent.
// Called on graceful leave or heartbeat-detected process death.
func (r *serviceEventRouter) RemoveProcess(procID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.procToTypes, procID)
}

// Snapshot returns a fresh typeName → []processID map suitable for
// stamping into a PeerList broadcast. Sorted for determinism (test
// stability + log readability).
func (r *serviceEventRouter) Snapshot() map[string][]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[string][]string{}
	for proc, types := range r.procToTypes {
		for t := range types {
			out[t] = append(out[t], proc)
		}
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}
