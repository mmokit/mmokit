// Package wasmsys is the authoring SDK compiled INTO a hot-swappable system
// module (GOOS=wasip1 GOARCH=wasm). A module defines one System, declares its
// column via Query(), and loops over the column inside Update().
//
// A system's internal state is auto-snapshotted across a hot-swap from its
// exported, non-tunable, fixed-size fields — no hand-written Snapshot/Restore
// is needed for the common case. Implement the Stateful interface only for
// state that needs custom encoding (e.g. unexported or variable-size fields);
// it takes precedence over the auto-snapshot path.
package wasmsys

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"unsafe"

	"github.com/zenion/mmoserver/pkg/tunable"
	"github.com/zenion/mmoserver/pkg/wasmabi"
)

// System is the contract a hot-swappable system implements.
type System interface {
	// Query declares the single POD component column this system reads/writes.
	Query() Query
	// Update runs once per tick over the host-mapped column.
	Update(ctx *Ctx, dt float32)
}

// Stateful is optionally implemented by systems holding internal state that
// must survive an unload/swap. Pure-function systems omit it.
type Stateful interface {
	Snapshot() []byte
	Restore(state []byte)
}

// Query is a column declaration produced by ReadWrite[T] / Read[T].
type Query struct {
	elemSize  uint32
	readWrite bool
}

// ReadWrite declares a column the host copies in and reads back after Update.
// The column element size is derived from T and checked by the host at load.
func ReadWrite[T any]() Query { return Query{wasmabi.ElemSize[T](), true} }

// Read declares a read-only column (copied in, not read back).
func Read[T any]() Query { return Query{wasmabi.ElemSize[T](), false} }

func (q Query) encode() uint64 { return wasmabi.EncodeQuery(q.elemSize, q.readWrite) }

// Ctx is handed to Update. It exposes the mapped column via Column/View. The
// base pointer is the address of the column data (arena + HeaderSize), set by
// the guest's update export; on a native build Ctx is just an inert struct.
type Ctx struct {
	base  unsafe.Pointer
	count uint32
	nowMs uint64
}

// NowMs is the host's cluster-coherent wall clock in milliseconds for this
// tick. It is identical across every cell and host (sourced from the engine's
// ClusterClock, tick-aligned), so an animation phase derived from it stays
// continuous when an entity crosses a cell boundary — unlike a cell-local tick
// counter, which jumps to the destination cell's phase on handoff.
func (c *Ctx) NowMs() uint64 { return c.nowMs }

// TimeSec is NowMs expressed in seconds — the convenient unit for feeding a
// phase into trig (e.g. sin(TimeSec * radPerSec)).
func (c *Ctx) TimeSec() float64 { return float64(c.nowMs) / 1000 }

// Column returns a writable view over the host-mapped column. Mutations are
// read back by the host when the system declared ReadWrite.
func Column[T any](ctx *Ctx) []T {
	if ctx.count == 0 {
		return nil
	}
	return unsafe.Slice((*T)(ctx.base), int(ctx.count))
}

// View is Column for read-only systems (identical mechanics; intent marker
// that becomes the RW/RO discriminator for the Phase 1 codegen).
func View[T any](ctx *Ctx) []T { return Column[T](ctx) }

// MarshalState serializes a fixed-size value (or struct of fixed-size fields)
// to little-endian bytes — a convenience for a System's Snapshot(). Example:
//
//	func (s *mySys) Snapshot() []byte { return wasmsys.MarshalState(s.ticks) }
func MarshalState(v any) []byte {
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, v)
	return buf.Bytes()
}

// UnmarshalState is the inverse, for Restore(). ptr must be a pointer:
//
//	func (s *mySys) Restore(b []byte) { wasmsys.UnmarshalState(b, &s.ticks) }
func UnmarshalState(data []byte, ptr any) {
	_ = binary.Read(bytes.NewReader(data), binary.LittleEndian, ptr)
}

// stateSet is the guest-side reflection over a system's AUTO-SNAPSHOT fields:
// exported, non-tune-tagged, fixed-size (binary-marshalable) fields. The
// framework derives Snapshot/Restore from these when the system does not
// implement Stateful itself. Tunables (tune-tagged) are config, not state, so
// they are excluded; unexported fields can't be reflect-marshaled so they are
// skipped (use a Stateful impl for those, or export them). Fixed-size means
// binary.Size >= 0 (use intN/uintN/float32/64/bool/arrays/structs thereof —
// a bare `int` is NOT fixed-size and is skipped).
type stateSet struct {
	v       reflect.Value // addressable system struct
	indices []int         // field indices that are auto-snapshot state
}

func buildStateSet(sys any) *stateSet {
	rv := reflect.ValueOf(sys)
	if rv.Kind() != reflect.Pointer || rv.Elem().Kind() != reflect.Struct {
		return &stateSet{}
	}
	elem := rv.Elem()
	t := elem.Type()
	ss := &stateSet{v: elem}
	for i := range t.NumField() {
		sf := t.Field(i)
		if sf.PkgPath != "" {
			continue // unexported
		}
		if _, ok := sf.Tag.Lookup("tune"); ok {
			continue // tunable config, not snapshot state
		}
		if binary.Size(elem.Field(i).Interface()) < 0 {
			continue // not fixed-size — author should use Stateful
		}
		ss.indices = append(ss.indices, i)
	}
	return ss
}

// marshal serializes the auto-snapshot fields in declared order (little-endian).
func (ss *stateSet) marshal() []byte {
	if ss == nil || len(ss.indices) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, i := range ss.indices {
		_ = binary.Write(&buf, binary.LittleEndian, ss.v.Field(i).Interface())
	}
	return buf.Bytes()
}

// unmarshal restores the auto-snapshot fields from a marshal() blob.
func (ss *stateSet) unmarshal(data []byte) {
	if ss == nil || len(ss.indices) == 0 || len(data) == 0 {
		return
	}
	r := bytes.NewReader(data)
	for _, i := range ss.indices {
		_ = binary.Read(r, binary.LittleEndian, ss.v.Field(i).Addr().Interface())
	}
}

// paramField binds a guest struct field to its tunable kind for the params ABI.
type paramField struct {
	index int
	kind  tunable.Kind
}

// paramSet is the guest-side reflection over the registered system's
// tune-tagged fields. Built once in Register; reused by the params exports.
type paramSet struct {
	v      reflect.Value // addressable system struct
	fields []paramField
	schema []wasmabi.ParamField
}

// buildParamSet reflects sys (a struct pointer) into a paramSet, applies tag
// defaults to the live fields, and precomputes the wire schema. A non-struct
// system yields an empty set (no tunables).
func buildParamSet(sys any) *paramSet {
	rv := reflect.ValueOf(sys)
	if rv.Kind() != reflect.Pointer || rv.Elem().Kind() != reflect.Struct {
		return &paramSet{}
	}
	src := tunable.Reflect(sys)
	if ad, ok := src.(interface{ ApplyDefaults() }); ok {
		ad.ApplyDefaults() // fields hold correct values before any host push
	}
	elem := rv.Elem()
	t := elem.Type()
	ps := &paramSet{v: elem}
	for i := range t.NumField() {
		sf := t.Field(i)
		if sf.PkgPath != "" {
			continue
		}
		body, ok := sf.Tag.Lookup("tune")
		if !ok {
			continue
		}
		tag, present := tunable.ParseTag(body)
		if !present {
			continue
		}
		kind, ok := guestKindOf(sf.Type.Kind())
		if !ok {
			continue
		}
		ps.fields = append(ps.fields, paramField{index: i, kind: kind})
		ps.schema = append(ps.schema, schemaField(sf.Name, kind, tag))
	}
	return ps
}

func schemaField(name string, kind tunable.Kind, tag tunable.Tag) wasmabi.ParamField {
	pf := wasmabi.ParamField{Name: name, Kind: uint8(kind)}
	if tag.Default != "" {
		if v, err := tunable.ToF64(kind, tag.Default); err == nil {
			pf.Default, pf.BoundsMask = v, pf.BoundsMask|wasmabi.BoundDefault
		}
	}
	if tag.Min != "" {
		if v, err := tunable.ToF64(kind, tag.Min); err == nil {
			pf.Min, pf.BoundsMask = v, pf.BoundsMask|wasmabi.BoundMin
		}
	}
	if tag.Max != "" {
		if v, err := tunable.ToF64(kind, tag.Max); err == nil {
			pf.Max, pf.BoundsMask = v, pf.BoundsMask|wasmabi.BoundMax
		}
	}
	if tag.Step != "" {
		if v, err := tunable.ToF64(kind, tag.Step); err == nil {
			pf.Step, pf.BoundsMask = v, pf.BoundsMask|wasmabi.BoundStep
		}
	}
	return pf
}

func guestKindOf(k reflect.Kind) (tunable.Kind, bool) {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return tunable.KindInt, true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return tunable.KindUint, true
	case reflect.Float32, reflect.Float64:
		return tunable.KindFloat, true
	case reflect.Bool:
		return tunable.KindBool, true
	default:
		return 0, false
	}
}

// scatter writes a host value block into the live struct fields, in schema order.
func (ps *paramSet) scatter(block []float64) {
	for i, f := range ps.fields {
		if i >= len(block) {
			return
		}
		fv := ps.v.Field(f.index)
		switch f.kind {
		case tunable.KindInt:
			fv.SetInt(int64(block[i]))
		case tunable.KindUint:
			fv.SetUint(uint64(block[i]))
		case tunable.KindBool:
			fv.SetBool(block[i] != 0)
		default:
			fv.SetFloat(block[i])
		}
	}
}
