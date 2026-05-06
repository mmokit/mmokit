package mmokit

import (
	"fmt"
	"hash/fnv"
	"reflect"
	"sort"
	"sync"
	"unicode"
)

// BroadcastTypeSchema is a serializable description of a broadcast-eligible
// Go type. Used by sdkgen (via Protocol.AssembleFromProcess) to emit a TS
// class with a matching binary deserializer. Wire layout mirrors the
// reflect_marshal codec (universe.ReflectMarshal) — fields encoded in source
// declaration order, no padding.
type BroadcastTypeSchema struct {
	Name   string                 `json:"name"`
	TypeID uint32                 `json:"type_id"`
	Fields []BroadcastFieldSchema `json:"fields"`
}

// ClientInputTypeSchema describes a registered client-input message type.
// Same shape as BroadcastTypeSchema; aliased to keep JSON output identical
// and to share the field-encoding logic. Used by sdkgen to emit a TS class
// with an `encode()` instance method (mirror of Broadcast's static decode).
type ClientInputTypeSchema = BroadcastTypeSchema

// BroadcastFieldSchema describes one field on a broadcast-eligible type.
// Encoding strings: f32, f64, u8/u16/u32/u64, i8/i16/i32/i64, bool, entity,
// string, struct, slice. Size is the on-wire byte count for fixed-width
// fields; zero for length-prefixed (string, slice, struct).
//
//   - struct: nested-struct field. Fields lists the inner fields in source
//     order; the wire format inlines their bytes contiguously.
//   - slice:  variable-length sequence. Item describes the element schema
//     (recursive, so slices of structs work). Wire format prefixes a
//     uint16 LE length, then encodes each element via Item.
type BroadcastFieldSchema struct {
	Name     string                 `json:"name"`
	Encoding string                 `json:"encoding"`
	Size     int                    `json:"size"`
	Fields   []BroadcastFieldSchema `json:"fields,omitempty"` // for encoding == "struct"
	Item     *BroadcastFieldSchema  `json:"item,omitempty"`   // for encoding == "slice"
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
// HandleAll[T] (the broadcast-on-by-default registration verb).
// HandleAllInternal[T] explicitly does not call this — that's how
// server-internal types stay out of the broadcast registry. Idempotent.
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

// BroadcastTypeOf returns a serializable schema describing a broadcast-eligible
// type t. Field shape mirrors the reflection codec's wire layout — sdkgen
// uses this to emit a TS class with a matching binary deserializer.
//
// Fields are emitted in source declaration order, matching the marshal/
// unmarshal pass in pkg/universe/reflect_marshal.go. Unexported fields are
// skipped. mmokit.Entity fields are encoded as 4-byte NetID via the registered
// reflect codec (see entity.go init()).
//
// Panics on unsupported field types — the call site is schema export at
// program startup, where a panic produces a useful "fix your message type"
// error instead of a silently-incorrect SDK.
func BroadcastTypeOf(t reflect.Type) BroadcastTypeSchema {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	s := BroadcastTypeSchema{
		Name:   t.String(),
		TypeID: TypeIDOf(t),
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		s.Fields = append(s.Fields, fieldSchemaOf(f))
	}
	return s
}

// fieldSchemaOf resolves one struct field into its on-wire encoding tag.
// Mirrors reflect_marshal.marshalValue exactly — any encoding added there
// must be added here too, otherwise schema dump and runtime bytes drift.
func fieldSchemaOf(f reflect.StructField) BroadcastFieldSchema {
	out := schemaForType(f.Type)
	out.Name = lowerFirst(f.Name)
	return out
}

// schemaForType is the recursive helper used by fieldSchemaOf to walk into
// nested structs and slice element types. Returns a schema with Encoding +
// Size + (optionally) Fields/Item populated; Name is filled in by the
// caller.
func schemaForType(t reflect.Type) BroadcastFieldSchema {
	var out BroadcastFieldSchema
	if t == entityType {
		out.Encoding = "entity"
		out.Size = 4
		return out
	}
	switch t.Kind() {
	case reflect.Float32:
		out.Encoding = "f32"
		out.Size = 4
	case reflect.Float64:
		out.Encoding = "f64"
		out.Size = 8
	case reflect.Uint8:
		out.Encoding = "u8"
		out.Size = 1
	case reflect.Uint16:
		out.Encoding = "u16"
		out.Size = 2
	case reflect.Uint32:
		out.Encoding = "u32"
		out.Size = 4
	case reflect.Uint64:
		out.Encoding = "u64"
		out.Size = 8
	case reflect.Int8:
		out.Encoding = "i8"
		out.Size = 1
	case reflect.Int16:
		out.Encoding = "i16"
		out.Size = 2
	case reflect.Int32:
		out.Encoding = "i32"
		out.Size = 4
	case reflect.Int64:
		out.Encoding = "i64"
		out.Size = 8
	case reflect.Bool:
		out.Encoding = "bool"
		out.Size = 1
	case reflect.String:
		out.Encoding = "string"
		out.Size = 0 // length-prefixed (uint16 length + bytes)
	case reflect.Struct:
		out.Encoding = "struct"
		out.Size = 0 // variable size (sum of inner fields)
		for i := range t.NumField() {
			inner := t.Field(i)
			if !inner.IsExported() {
				continue
			}
			out.Fields = append(out.Fields, fieldSchemaOf(inner))
		}
	case reflect.Slice:
		out.Encoding = "slice"
		out.Size = 0 // u16 length + N elements
		item := schemaForType(t.Elem())
		out.Item = &item
	default:
		panic(fmt.Sprintf("BroadcastTypeOf: unsupported type %s (kind=%s)", t, t.Kind()))
	}
	return out
}

// lowerFirst converts "AbilityType" → "abilityType" for TS field naming.
// Matches the camelCase convention the rest of the schema uses.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = unicode.ToLower(runes[0])
	return string(runes)
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
var entityType = reflect.TypeFor[Entity]()

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
		if f.Type() == entityType {
			add(f.Interface().(Entity).NetID())
			continue
		}
		if f.Kind() == reflect.Struct {
			walkAnchors(f, add)
		}
	}
}
