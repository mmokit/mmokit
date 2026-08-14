package service

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// CoordRegistry is the coordinator-side roster of running service
// instances across the cluster. Peer to HostRegistry / GatewayRegistry.
//
// Concurrency: protected by mu. All mutations also bump epoch so
// callers can detect changes for PeerList re-broadcast.
type CoordRegistry struct {
	mu        sync.RWMutex
	instances map[string]CoordInstance // by InstanceID
	byKind    map[string][]string      // kind → []instanceID (sorted lex)
	opToKind  map[uint32]string        // op code → owning kind
	epoch     uint64
}

// CoordInstance is a single live service instance in the cluster.
type CoordInstance struct {
	Kind       string
	InstanceID string
	HostID     string
	OpCodes    []uint32
	JoinedAt   time.Time
}

// NewCoordRegistry returns an empty registry.
func NewCoordRegistry() *CoordRegistry {
	return &CoordRegistry{
		instances: map[string]CoordInstance{},
		byKind:    map[string][]string{},
		opToKind:  map[uint32]string{},
	}
}

// Register adds an instance. Validates:
//   - InstanceID not already in use
//   - If kind already has live instances, op codes match exactly
//   - Otherwise no op code overlaps with a different kind
func (r *CoordRegistry) Register(inst CoordInstance) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.instances[inst.InstanceID]; exists {
		return fmt.Errorf("service.CoordRegistry.Register: duplicate instanceID %q", inst.InstanceID)
	}

	// All instances of the same kind must declare the same op-code set.
	if existingIDs := r.byKind[inst.Kind]; len(existingIDs) > 0 {
		ref := r.instances[existingIDs[0]].OpCodes
		if !sameCodeSet(ref, inst.OpCodes) {
			return fmt.Errorf("service.CoordRegistry.Register: kind %q already running with op codes %v; new instance %q declares %v",
				inst.Kind, ref, inst.InstanceID, inst.OpCodes)
		}
	} else {
		// New kind: ensure op codes don't overlap with another kind.
		for _, code := range inst.OpCodes {
			if owner, exists := r.opToKind[code]; exists && owner != inst.Kind {
				return fmt.Errorf("service.CoordRegistry.Register: op code %d claimed by kind %q; new kind %q rejected",
					code, owner, inst.Kind)
			}
		}
	}

	r.instances[inst.InstanceID] = inst
	r.byKind[inst.Kind] = append(r.byKind[inst.Kind], inst.InstanceID)
	sort.Strings(r.byKind[inst.Kind])
	for _, code := range inst.OpCodes {
		r.opToKind[code] = inst.Kind
	}
	r.epoch++
	return nil
}

// Unregister removes an instance. If it was the last instance of its
// kind, the op-code claims for that kind are also released.
func (r *CoordRegistry) Unregister(instanceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	inst, ok := r.instances[instanceID]
	if !ok {
		return
	}
	delete(r.instances, instanceID)

	ids := r.byKind[inst.Kind]
	out := ids[:0]
	for _, id := range ids {
		if id != instanceID {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		delete(r.byKind, inst.Kind)
		for _, code := range inst.OpCodes {
			delete(r.opToKind, code)
		}
	} else {
		r.byKind[inst.Kind] = out
	}
	r.epoch++
}

// UnregisterByHost removes every instance owned by hostID. Used when a
// host crashes or gracefully leaves.
func (r *CoordRegistry) UnregisterByHost(hostID string) {
	r.mu.Lock()
	ids := []string{}
	for id, inst := range r.instances {
		if inst.HostID == hostID {
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()
	for _, id := range ids {
		r.Unregister(id)
	}
}

// Snapshot returns the full instance list for PeerList broadcast,
// sorted by (Kind, InstanceID) for stable wire output.
func (r *CoordRegistry) Snapshot() []CoordInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]CoordInstance, 0, len(r.instances))
	for _, inst := range r.instances {
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].InstanceID < out[j].InstanceID
	})
	return out
}

// Epoch returns the current change counter. Callers can compare epochs
// to decide whether a re-broadcast is needed.
func (r *CoordRegistry) Epoch() uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.epoch
}

// LookupByOpCode returns the kind that owns opCode, or "" if unclaimed.
func (r *CoordRegistry) LookupByOpCode(opCode uint32) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.opToKind[opCode]
}

// InstancesOfKind returns a defensive copy of all live instances for kind.
func (r *CoordRegistry) InstancesOfKind(kind string) []CoordInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := r.byKind[kind]
	out := make([]CoordInstance, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.instances[id])
	}
	return out
}

// Kinds returns the sorted list of kinds currently live.
func (r *CoordRegistry) Kinds() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.byKind))
	for k := range r.byKind {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sameCodeSet(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[uint32]bool{}
	for _, c := range a {
		seen[c] = true
	}
	for _, c := range b {
		if !seen[c] {
			return false
		}
	}
	return true
}
