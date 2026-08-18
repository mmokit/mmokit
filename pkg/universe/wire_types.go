package universe

import (
	"fmt"
	"hash/fnv"
	"reflect"
	"strconv"
)

// Wire primitives shared by pkg/universe's dispatch paths and the mmokit
// facade's registration verbs. They live here rather than in the facade
// because universe is the layer that decodes and routes the bytes; the
// facade re-exports each one so game code keeps importing mmokit alone.

// TypeIDOf returns the stable wire identifier for a registered Go message
// type. Computed as fnv32a(reflect.Type.String()) — e.g. "game.Damage" → some
// uint32.
//
// reflect.Type.String() qualifies by package NAME, not import path: a type in
// .../internal/game and one in .../examples/space/internal/game both stringify
// to "game.Damage" and hash identically. So the ID survives moving a package
// between directories, and breaks on renaming the package or the type. Two
// registered types whose package names AND type names collide would share an
// ID; the registration verbs panic on a duplicate.
//
// Renaming is a deliberate wire-break in lockstep with SDK regeneration.
// Contrast pkg/service.EventTypeName, which keys the server-internal service
// event bus by PkgPath()+"."+Name() and is therefore path-sensitive; no client
// wire type reaches it.
func TypeIDOf(t reflect.Type) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(t.String()))
	return h.Sum32()
}

// RouteKind classifies where a typed-op handler runs in the cluster.
//
// RouteGatewayLocal — handler runs on the gateway, no cell forwarding.
// Examples: login, ping, anything that doesn't need ECS access.
//
// RoutePlayerCell — handler runs on the player's authoritative cell
// (the cell currently owning the player's entity). Examples: marketplace,
// bank, anything that mutates ECS state.
type RouteKind uint8

const (
	RouteGatewayLocal RouteKind = iota
	RoutePlayerCell
)

// String returns a stable, human-readable name. Used by diagnostics, logs,
// and SDK schema export.
func (k RouteKind) String() string {
	switch k {
	case RouteGatewayLocal:
		return "gateway-local"
	case RoutePlayerCell:
		return "player-cell"
	default:
		return "unknown"
	}
}

// TypedOpEntry is the registry record for one typed-op binding. Created by
// mmokit.RegisterOp[Req, Res] and looked up by request typeID by the
// dispatcher.
type TypedOpEntry struct {
	Kind           RouteKind
	RequestType    reflect.Type
	ResponseType   reflect.Type
	ResponseTypeID uint32
	// Handler is the originally-registered func(*OpContext, *Req) (*Res, error)
	// stored as `any`. The dispatcher uses reflect.Call to invoke it.
	Handler any
}

// entityReflectType is the reflect.Type for Entity, used by walkAnchors to
// identify Entity-typed fields without an interface check.
var entityReflectType = reflect.TypeFor[Entity]()

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
	if v.Kind() == reflect.Pointer {
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
		if f.Type() == entityReflectType {
			add(f.Interface().(Entity).NetID())
			continue
		}
		if f.Kind() == reflect.Struct {
			walkAnchors(f, add)
		}
	}
}

// FormatSchemaFingerprint renders a structural schema fingerprint as 8
// lowercase hex digits — the form carried in the schema JSON, in the
// connection-setup query parameter, and in logs. Fixed-width and greppable in
// an access log, unlike decimal.
func FormatSchemaFingerprint(fp uint32) string {
	return fmt.Sprintf("%08x", fp)
}

// ParseSchemaFingerprint reads the form FormatSchemaFingerprint writes.
// Deliberately strict: exactly 8 hex digits, nothing else, because this parses
// an untrusted query parameter and a lenient parse is a way to be admitted by
// accident.
func ParseSchemaFingerprint(s string) (uint32, bool) {
	if len(s) != 8 {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, false
	}
	return uint32(v), true
}
