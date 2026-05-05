package mmokit

import (
	"hash/fnv"
	"reflect"
	"sort"
	"sync"
)

// ServerOnly is the marker interface that opts a typed message OUT of
// AoI auto-broadcast. Implement via:
//
//	func (T) ServerOnly() {}
//
// Used by KillCredit (currency rewards are server-internal accounting,
// no client visibility needed). The method name is exported so types in
// any package — game code in internal/game, third-party plugins, etc. —
// can satisfy the marker without an unexported-method package-locality
// trick.
type ServerOnly interface{ ServerOnly() }

var serverOnlyType = reflect.TypeOf((*ServerOnly)(nil)).Elem()

// IsServerOnly reflects T to determine if it implements the ServerOnly
// marker. Checked at registration time in Handle/HandleAll.
func IsServerOnly(t reflect.Type) bool {
	return t.Implements(serverOnlyType) ||
		reflect.PointerTo(t).Implements(serverOnlyType)
}

// TypeIDOf returns the stable wire identifier for a broadcast-eligible Go type.
// Computed as fnv32(reflect.Type.String()) — e.g. "game.Damage" → some uint32.
//
// Stable as long as the package path and type name don't change. Renaming
// is a deliberate wire-break in lockstep with SDK regeneration.
func TypeIDOf(t reflect.Type) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(t.String()))
	return h.Sum32()
}

// broadcastRegistry holds the global registry of broadcast-eligible types.
// Sdkgen reads this via BroadcastTypes() to emit TS class declarations.
var (
	brMu  sync.RWMutex
	brSet = map[reflect.Type]struct{}{} // T (NOT *T)
)

// RegisterBroadcastType marks T as broadcast-eligible. Called from
// Handle/HandleAll when T does not implement ServerOnly. Idempotent.
func RegisterBroadcastType(t reflect.Type) {
	brMu.Lock()
	brSet[t] = struct{}{}
	brMu.Unlock()
}

// BroadcastTypes returns the registered broadcast-eligible types in
// deterministic order (sorted by reflect.Type.String()). Used by sdkgen
// to emit TS class declarations.
func BroadcastTypes() []reflect.Type {
	brMu.RLock()
	defer brMu.RUnlock()
	out := make([]reflect.Type, 0, len(brSet))
	for t := range brSet {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// brIsRegistered reports whether t is currently in the broadcast registry.
// Internal helper for the universe-side eligibility hook (see init.go).
func brIsRegistered(t reflect.Type) bool {
	brMu.RLock()
	_, ok := brSet[t]
	brMu.RUnlock()
	return ok
}

// entityType is the reflect.Type for mmokit.Entity, used by walkAnchors
// to identify Entity-typed fields without an interface check.
var entityType = reflect.TypeOf(Entity{})

// ExtractAnchors reflects on msgPtr (pointer to a broadcast-eligible struct)
// and returns deduped NetIDs of all Entity-typed fields plus the receiver.
//
// Recurses into sub-struct fields. Skips zero-value Entities (NetID == 0).
// Returns deduped slice in stable order (first encountered wins; the
// target/receiver is added first).
func ExtractAnchors(msgPtr any, target Entity) []uint32 {
	seen := map[uint32]struct{}{}
	var out []uint32

	add := func(nid uint32) {
		if nid == 0 {
			return
		}
		if _, dup := seen[nid]; dup {
			return
		}
		seen[nid] = struct{}{}
		out = append(out, nid)
	}

	add(target.NetID())

	v := reflect.ValueOf(msgPtr)
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return out
		}
		v = v.Elem()
	}
	walkAnchors(v, add)
	return out
}

// walkAnchors recursively visits struct fields, calling add(nid) for each
// Entity-typed field's NetID. Non-struct, non-Entity fields are ignored.
func walkAnchors(v reflect.Value, add func(uint32)) {
	if v.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if !v.Type().Field(i).IsExported() {
			continue
		}
		if f.Type() == entityType {
			add(f.Interface().(Entity).NetID())
			continue
		}
		if f.Kind() == reflect.Struct {
			walkAnchors(f, add)
		}
	}
}
