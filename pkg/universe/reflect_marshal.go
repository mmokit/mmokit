package universe

import (
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"reflect"
	"sync/atomic"
	"time"

	"github.com/mlange-42/ark/ecs"

	pkgnet "github.com/zenion/mmoserver/pkg/net"
)

var ecsEntityType = reflect.TypeFor[ecs.Entity]()

// Wire-format length ceilings for the reflection codec.
//
// The prefix widths are a frozen cross-language contract — cmd/sdkgen emits
// dv.getUint16 for the string arm and dv.getUint32 for the []byte arm, and
// csharp/Mmokit.Sdk.Core/ReflectCodec.cs hard-codes WriteSliceLen/ReadSliceLen
// to u16 — so widening one is not an option. A value that does not fit is
// rejected at encode time instead: writing uint16(len(s)) unchecked emits a
// frame whose prefix disagrees with the bytes that follow it, which every
// decoder in the cluster then reads as a differently-shaped message.
const (
	maxWireStringLen  = math.MaxUint16 // string arm:         [u16 len][len bytes]
	maxWireSliceElems = math.MaxUint16 // generic slice arm:  [u16 len][elem0]...
	maxWireBytesLen   = math.MaxUint32 // []byte fast path:   [u32 len][len bytes]
)

// maxValidateDepth bounds the type-graph walk in validateStruct/validateType.
//
// It exists purely so the walk TERMINATES. Neither validator had cycle
// detection, and the recursion is on types rather than values, so a registered
// type of the form `type Node struct { Kids []Node }` recursed forever and took
// the process out with an unrecoverable stack overflow at package init. No
// registered type is self-referential today; a registration-time guard that
// panics is only safe if it terminates on every input a future game author can
// write.
//
// Deliberately larger than the default decode-time WireLimits.MaxDepth (16).
// The two bound different things: this one guarantees registration terminates,
// that one is wire policy on a body an attacker supplies. A type legitimately
// nested deeper than the decode limit is a limits-configuration question, not a
// registration error.
const maxValidateDepth = 64

// ValidateComponentType panics if t contains any fields with unsupported types.
// Call this at ECS-component registration time to catch problems early.
// Slices are rejected — components must have a fixed memory shape for the
// ECS column store. For typed-event message types (which support slices),
// use ValidateMessageType.
func ValidateComponentType(t reflect.Type) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("reflect_marshal: expected struct, got %s", t.Kind()))
	}
	validateStruct(t, "", false, 0)
}

// ValidateMessageType is the same as ValidateComponentType but accepts
// slice fields — the typed-event wire codec serializes them as
// [u16 len][elem0]...[elemN]. Use for types registered via
// mmokit.RegisterEvent[T] / RegisterClientInputType[T].
func ValidateMessageType(t reflect.Type) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("reflect_marshal: expected struct, got %s", t.Kind()))
	}
	validateStruct(t, "", true, 0)
}

func validateStruct(t reflect.Type, path string, allowSlice bool, depth int) {
	if depth > maxValidateDepth {
		panic(fmt.Sprintf("reflect_marshal: type nesting deeper than %d at %s (self-referential type?)",
			maxValidateDepth, path))
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		fpath := path + "." + f.Name
		if f.Type == ecsEntityType {
			continue // skipped during marshal
		}
		validateType(f.Type, fpath, allowSlice, depth+1)
	}
}

func validateType(t reflect.Type, path string, allowSlice bool, depth int) {
	// Custom-codec-registered types are accepted regardless of their default
	// reflective shape (e.g. mmokit.Entity contains a *Stage pointer the
	// default validator would reject).
	if LookupReflectCodec(t) != nil {
		return
	}
	if depth > maxValidateDepth {
		panic(fmt.Sprintf("reflect_marshal: type nesting deeper than %d at %s (self-referential type?)",
			maxValidateDepth, path))
	}
	switch t.Kind() {
	case reflect.Float32, reflect.Float64,
		reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Bool, reflect.String:
		// supported
	case reflect.Array:
		validateType(t.Elem(), path+"[]", allowSlice, depth+1)
	case reflect.Slice:
		if !allowSlice {
			panic(fmt.Sprintf("reflect_marshal: unsupported type %s at %s (kind=%s)", t, path, t.Kind()))
		}
		// []byte fast path: encoded as [u32 len][bytes] without per-element
		// iteration. Same validity rule as any byte field — always allowed.
		if t.Elem().Kind() == reflect.Uint8 {
			return
		}
		// The only recursion that can revisit a type: Go forbids a struct or
		// array that contains itself by value, but `[]Node` inside `Node` is
		// legal and is what maxValidateDepth exists to stop.
		validateType(t.Elem(), path+"[]", allowSlice, depth+1)
	case reflect.Struct:
		validateStruct(t, path, allowSlice, depth+1)
	default:
		panic(fmt.Sprintf("reflect_marshal: unsupported type %s at %s (kind=%s)", t, path, t.Kind()))
	}
}

// ReflectMarshal serializes a struct pointer to binary.
// Unexported fields and ecs.Entity fields are skipped.
//
// Returns an error, never a panic, when a value does not fit its wire length
// prefix (see maxWireStringLen and friends). ReflectMarshal runs on the cell
// tick goroutine — GameLoop.tick has no recover — so a panic here would abort
// the process on data a client can supply.
func ReflectMarshal(ptr any) ([]byte, error) {
	v := reflect.ValueOf(ptr)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	// Pre-calculate size to avoid repeated allocation. The length guards run in
	// both walks and must stay in lockstep: if valueSize accepted a length that
	// marshalValue rejects the frame is short, and if it rejected one that
	// marshalValue accepts the write overruns the buffer.
	size, err := structSize(v)
	if err != nil {
		return nil, fmt.Errorf("reflect_marshal: encode %s: %w", v.Type(), err)
	}
	buf := make([]byte, size)
	off, err := marshalStruct(buf, 0, v)
	if err != nil {
		return nil, fmt.Errorf("reflect_marshal: encode %s: %w", v.Type(), err)
	}
	return buf[:off], nil
}

// ReflectUnmarshalOnStage decodes data into the struct ptr, threading stage
// to any registered field codecs that need it. Use when the struct contains
// types whose decode is stage-dependent (e.g. mmokit.Entity, which resolves
// its local ECS handle via the stage's NetID index).
//
// A non-nil error means the checked decoder refused the body: a read past the
// end, a length prefix over a configured ceiling, or an allocation over the
// budget. The destination is then PARTIALLY written — fields decoded before the
// failing one hold their decoded values and the rest are zero — so every caller
// must treat an error as "drop this message", never as "use what arrived".
// Ignoring the error dispatches a zero-valued request to a handler, which is a
// worse failure than the panic this decoder replaced.
//
// TOLERATES trailing bytes: a body longer than the struct decodes the struct
// and discards the surplus. That asymmetry with ReflectUnmarshalStrict is
// deliberate — mesh transfer blobs and border component blobs are appended to
// today, and UnmarshalTransferFrame does not reject trailing data either.
func ReflectUnmarshalOnStage(stage *Stage, data []byte, ptr any) error {
	_, err := decodeStruct(stage, data, ptr, pkgnet.DefaultWireLimits())
	return err
}

// ReflectUnmarshal deserializes binary data into a struct pointer.
// Unexported fields and ecs.Entity fields are left at zero value.
//
// Preserves the no-stage call signature for callers that don't need stage
// context (the existing transfer codec). Equivalent to
// ReflectUnmarshalOnStage(nil, data, ptr), including its error contract.
func ReflectUnmarshal(data []byte, ptr any) error {
	return ReflectUnmarshalOnStage(nil, data, ptr)
}

// ReflectUnmarshalStrict decodes data into the struct ptr under lim and, unlike
// the tolerant wrappers above, rejects a body that is not exactly the size of
// the struct it claims to be. It also rejects an over-large body before
// decoding a single field.
//
// Use it wherever the body's length is itself part of the contract — a typed
// client op or event, where a body longer than the registered type means the
// sender and the receiver disagree about the type, not that a producer appended
// something. lim may be the zero value; Normalized fills in the defaults,
// because universe.New's `if !flag.Parsed()` guard is always false under
// `go test` and flag defaults therefore never reach a test.
//
// No production call site uses this yet. Switching them over depends on both
// the error propagation (unit 9) and the ValidateMessageType wiring (unit 10).
func ReflectUnmarshalStrict(stage *Stage, data []byte, ptr any, lim pkgnet.WireLimits) error {
	lim = lim.Normalized()
	if len(data) > lim.MaxFrameBytes {
		return fmt.Errorf("reflect_marshal: body of %d bytes exceeds the %d-byte frame limit",
			len(data), lim.MaxFrameBytes)
	}
	consumed, err := decodeStruct(stage, data, ptr, lim)
	if err != nil {
		return err
	}
	if consumed != len(data) {
		return fmt.Errorf("reflect_marshal: decode %T consumed %d of %d bytes",
			ptr, consumed, len(data))
	}
	return nil
}

// decodeStruct is the single entry point into the checked decoder. It returns
// the number of bytes consumed so ReflectUnmarshalStrict can reject a surplus
// the tolerant wrappers accept.
//
// lim must already be Normalized. It is not re-normalized here because this runs
// once per replicated component per entity per tick and a zero-valued limit set
// can only arrive from inside this package.
func decodeStruct(stage *Stage, data []byte, ptr any, lim pkgnet.WireLimits) (int, error) {
	v := reflect.ValueOf(ptr)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return 0, fmt.Errorf("reflect_marshal: decode expected struct, got %s", v.Kind())
	}
	d := decodeState{stage: stage, data: data, lim: lim, alloc: uint64(lim.MaxTotalAllocBytes)}
	off, err := d.structFields(0, v)
	if err != nil {
		return 0, fmt.Errorf("reflect_marshal: decode %s: %w", v.Type(), err)
	}
	return off, nil
}

// codecDropInterval bounds how often a dropNoter writes a line. The value that
// overflows a length prefix — or the body that declares one — is content- or
// client-driven, so an unthrottled line per entity per tick turns one bad
// component into a log flood, and on the decode side into an amplifier.
const codecDropInterval = time.Second

// dropNoter counts values a codec guard rejected and reports at most one line
// per codecDropInterval.
//
// It writes through the standard logger rather than a *logger.Logger category
// because Logger.Log is a no-op whenever its category is off, and a dropped
// component is silent state loss on the destination cell — the one class of
// message that must not depend on a debug category being enabled. cat keeps the
// line's shape identical to a wired category line so log filters still match.
type dropNoter struct {
	count atomic.Uint64
	last  atomic.Int64
}

func (n *dropNoter) note(cat string, format string, args ...any) {
	n.count.Add(1)
	now := time.Now().UnixNano()
	previous := n.last.Load()
	if now-previous < int64(codecDropInterval) || !n.last.CompareAndSwap(previous, now) {
		return
	}
	log.Printf("[%s] %s", cat, fmt.Sprintf(format, args...))
}

// Separate noters so the two counters — and the two throttles — cannot starve
// each other: an encode-side flood must not hide the first decode rejection.
var (
	marshalDrops dropNoter
	decodeDrops  dropNoter
)

// NoteMarshalDrop counts and reports a value the encoder guards rejected, from
// a call site that holds no *Process or *Stage and therefore no logger: the
// ComponentReplicator Scan closures in component_registry.go, the free function
// BuildTypedEventFrameRaw, and mmokit's chat fanout sender.
//
// Call sites that do hold a logger should use it directly; this is the fallback.
func NoteMarshalDrop(cat string, format string, args ...any) {
	marshalDrops.note(cat, format, args...)
}

// MarshalDrops reports how many values the encoder length guards have rejected
// process-wide since start. Non-zero means a component, event or broadcast was
// dropped rather than written as a frame whose length prefix lies.
func MarshalDrops() uint64 { return marshalDrops.total() }

// NoteDecodeDrop is the decode-side counterpart: a body the checked decoder
// refused, from a call site that cannot report the error onward. Since unit 9
// widened ReflectUnmarshalOnStage those sites are the ones that deliberately
// swallow the error as policy rather than for lack of a channel — chiefly
// noteComponentDecodeDrop, where skipping one component is the correct outcome
// and failing the surrounding transfer would be entity loss. Keeping the
// counter separate from MarshalDrops is what lets an operator tell "we are
// emitting garbage" from "we are being fed it".
func NoteDecodeDrop(cat string, format string, args ...any) {
	decodeDrops.note(cat, format, args...)
}

// DecodeDrops reports how many bodies the decoder bounds, limit or allocation
// guards have rejected process-wide since start.
func DecodeDrops() uint64 { return decodeDrops.total() }

func (n *dropNoter) total() uint64 { return n.count.Load() }

func structSize(v reflect.Value) (int, error) {
	total := 0
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() || f.Type == ecsEntityType {
			continue
		}
		if codec := LookupReflectCodec(f.Type); codec != nil {
			total += codec.Size()
			continue
		}
		n, err := valueSize(v.Field(i))
		if err != nil {
			// Name the field only on the failure path — composing a path
			// eagerly would allocate for every field of every replicated
			// component on every tick.
			return 0, fmt.Errorf("%s.%s: %w", t.Name(), f.Name, err)
		}
		total += n
	}
	return total, nil
}

func valueSize(v reflect.Value) (int, error) {
	switch v.Kind() {
	case reflect.Float32:
		return 4, nil
	case reflect.Float64:
		return 8, nil
	case reflect.Uint8, reflect.Int8, reflect.Bool:
		return 1, nil
	case reflect.Uint16, reflect.Int16:
		return 2, nil
	case reflect.Uint32, reflect.Int32:
		return 4, nil
	case reflect.Uint64, reflect.Int64:
		return 8, nil
	case reflect.String:
		n := len(v.String())
		if n > maxWireStringLen {
			return 0, fmt.Errorf("string of %d bytes exceeds the %d-byte u16 wire prefix", n, maxWireStringLen)
		}
		return 2 + n, nil
	case reflect.Array:
		n := v.Len()
		if n == 0 {
			return 0, nil
		}
		// All elements have the same type, but strings vary in length.
		total := 0
		for i := range n {
			sz, err := valueSize(v.Index(i))
			if err != nil {
				return 0, err
			}
			total += sz
		}
		return total, nil
	case reflect.Slice:
		// []byte fast path: [u32 len][N bytes]. Uses u32 (vs u16 for
		// generic slices) because typed binary payloads (e.g. WorldDelta
		// body) routinely exceed 65535 bytes for stress-test densities.
		if v.Type().Elem().Kind() == reflect.Uint8 {
			n := v.Len()
			if uint64(n) > maxWireBytesLen {
				return 0, fmt.Errorf("byte slice of %d bytes exceeds the %d-byte u32 wire prefix", n, uint64(maxWireBytesLen))
			}
			return 4 + n, nil
		}
		// Wire layout: [u16 len][elem0]...[elemN-1]. Slices are length-prefixed
		// rather than fixed-stride because they can hold strings (themselves
		// length-prefixed) or nested structs whose own size varies.
		n := v.Len()
		if n > maxWireSliceElems {
			return 0, fmt.Errorf("slice of %d elements exceeds the %d-element u16 wire prefix", n, maxWireSliceElems)
		}
		total := 2
		for i := range n {
			sz, err := valueSize(v.Index(i))
			if err != nil {
				return 0, err
			}
			total += sz
		}
		return total, nil
	case reflect.Struct:
		return structSize(v)
	default:
		return 0, nil
	}
}

func marshalStruct(buf []byte, off int, v reflect.Value) (int, error) {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() || f.Type == ecsEntityType {
			continue
		}
		if codec := LookupReflectCodec(f.Type); codec != nil {
			codec.Encode(buf[off:], v.Field(i))
			off += codec.Size()
			continue
		}
		next, err := marshalValue(buf, off, v.Field(i))
		if err != nil {
			return 0, fmt.Errorf("%s.%s: %w", t.Name(), f.Name, err)
		}
		off = next
	}
	return off, nil
}

func marshalValue(buf []byte, off int, v reflect.Value) (int, error) {
	switch v.Kind() {
	case reflect.Float32:
		binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(float32(v.Float())))
		return off + 4, nil
	case reflect.Float64:
		binary.LittleEndian.PutUint64(buf[off:], math.Float64bits(v.Float()))
		return off + 8, nil
	case reflect.Uint8:
		buf[off] = uint8(v.Uint())
		return off + 1, nil
	case reflect.Uint16:
		binary.LittleEndian.PutUint16(buf[off:], uint16(v.Uint()))
		return off + 2, nil
	case reflect.Uint32:
		binary.LittleEndian.PutUint32(buf[off:], uint32(v.Uint()))
		return off + 4, nil
	case reflect.Uint64:
		binary.LittleEndian.PutUint64(buf[off:], v.Uint())
		return off + 8, nil
	case reflect.Int8:
		buf[off] = byte(int8(v.Int()))
		return off + 1, nil
	case reflect.Int16:
		binary.LittleEndian.PutUint16(buf[off:], uint16(int16(v.Int())))
		return off + 2, nil
	case reflect.Int32:
		binary.LittleEndian.PutUint32(buf[off:], uint32(int32(v.Int())))
		return off + 4, nil
	case reflect.Int64:
		binary.LittleEndian.PutUint64(buf[off:], uint64(v.Int()))
		return off + 8, nil
	case reflect.Bool:
		if v.Bool() {
			buf[off] = 1
		} else {
			buf[off] = 0
		}
		return off + 1, nil
	case reflect.String:
		s := v.String()
		// Mirrors valueSize. Reachable only if the two walks disagree, but the
		// check has to be here too: an unguarded uint16(len(s)) truncates the
		// prefix and silently emits a self-inconsistent frame.
		if len(s) > maxWireStringLen {
			return 0, fmt.Errorf("string of %d bytes exceeds the %d-byte u16 wire prefix", len(s), maxWireStringLen)
		}
		binary.LittleEndian.PutUint16(buf[off:], uint16(len(s)))
		off += 2
		copy(buf[off:], s)
		return off + len(s), nil
	case reflect.Array:
		for i := range v.Len() {
			next, err := marshalValue(buf, off, v.Index(i))
			if err != nil {
				return 0, err
			}
			off = next
		}
		return off, nil
	case reflect.Slice:
		// []byte fast path: [u32 len][N bytes]. See valueSize for rationale.
		if v.Type().Elem().Kind() == reflect.Uint8 {
			b := v.Bytes()
			if uint64(len(b)) > maxWireBytesLen {
				return 0, fmt.Errorf("byte slice of %d bytes exceeds the %d-byte u32 wire prefix", len(b), uint64(maxWireBytesLen))
			}
			binary.LittleEndian.PutUint32(buf[off:], uint32(len(b)))
			off += 4
			copy(buf[off:], b)
			return off + len(b), nil
		}
		n := v.Len()
		// Mirrors valueSize; see the String arm for why the guard is duplicated.
		if n > maxWireSliceElems {
			return 0, fmt.Errorf("slice of %d elements exceeds the %d-element u16 wire prefix", n, maxWireSliceElems)
		}
		binary.LittleEndian.PutUint16(buf[off:], uint16(n))
		off += 2
		for i := range n {
			next, err := marshalValue(buf, off, v.Index(i))
			if err != nil {
				return 0, err
			}
			off = next
		}
		return off, nil
	case reflect.Struct:
		return marshalStruct(buf, off, v)
	default:
		return off, nil
	}
}

// decodeState carries the per-decode context the checked decoder needs: the
// body being read, the limits in force, the current nesting depth, and how much
// of the aggregate allocation budget is left.
//
// It exists because this decoder is reachable pre-authentication on goroutines
// with no recover above them (docs/roadmap.md §6.3). Two rules follow, and both
// are structural rather than stylistic:
//
//   - Every read is preceded by need(). The eleven scalar arms used to index
//     data[off] raw, so four bytes of payload could walk off the end.
//   - Every allocation is charged BEFORE it is made. Reaching the allocation
//     at all puts the outcome in the host's hands rather than the program's:
//     where overcommit allows the reservation the process merely wastes it,
//     and where it does not, make/MakeSlice raises Go's out-of-memory FATAL
//     error, which no panic barrier anywhere in the process can contain. Only
//     refusing to reach the allocation is a control.
//
// The (int, error) shape mirrors structSize/valueSize on the encode side so the
// two halves of this file read the same way.
type decodeState struct {
	stage *Stage
	data  []byte
	lim   pkgnet.WireLimits
	depth int
	// alloc is what remains of the aggregate allocation budget, in bytes.
	// Debited by every allocating arm so many individually-legal fields
	// cannot add up to an illegal total.
	alloc uint64
}

// need reports whether n bytes are readable at off. This is CE-002 criterion 2:
// every arm calls it before touching d.data. Written without off+n so it cannot
// itself overflow on a wire-supplied u32 length.
func (d *decodeState) need(off, n int) error {
	if off < 0 || n < 0 || off > len(d.data) || n > len(d.data)-off {
		return fmt.Errorf("read of %d bytes at offset %d exceeds the %d-byte body", n, off, len(d.data))
	}
	return nil
}

// charge debits n bytes from the decode's allocation budget, and is called
// before the corresponding make/MakeSlice rather than after it.
func (d *decodeState) charge(n uint64) error {
	if n > d.alloc {
		return fmt.Errorf("allocation of %d bytes exceeds the %d bytes left of the %d-byte decode budget",
			n, d.alloc, d.lim.MaxTotalAllocBytes)
	}
	d.alloc -= n
	return nil
}

// enter charges one level of nesting; leave releases it.
//
// Defence in depth, and recorded as such: wire-driven unbounded recursion does
// NOT exist in this codec. How deep a body can nest is fixed by the static Go
// type graph, and a self-referential registered type is rejected inside
// validateType at REGISTRATION (see maxValidateDepth), not at decode. The
// control that actually bounds amplification is the allocation budget; do not
// let this counter be mistaken for it.
func (d *decodeState) enter() error {
	if d.depth >= d.lim.MaxDepth {
		return fmt.Errorf("nesting deeper than the %d-level limit", d.lim.MaxDepth)
	}
	d.depth++
	return nil
}

func (d *decodeState) leave() { d.depth-- }

func (d *decodeState) structFields(off int, v reflect.Value) (int, error) {
	if err := d.enter(); err != nil {
		return 0, err
	}
	defer d.leave()

	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() || f.Type == ecsEntityType {
			continue
		}
		if codec := LookupReflectCodec(f.Type); codec != nil {
			size := codec.Size()
			if err := d.need(off, size); err != nil {
				return 0, fmt.Errorf("%s.%s: %w", t.Name(), f.Name, err)
			}
			// Hand the codec exactly its own field rather than the rest of
			// the body: Size() is documented fixed-width, so a codec reading
			// past it would be reading the next field's bytes.
			if err := codec.Decode(d.stage, d.data[off:off+size], v.Field(i)); err != nil {
				return 0, fmt.Errorf("%s.%s: %w", t.Name(), f.Name, err)
			}
			off += size
			continue
		}
		next, err := d.value(off, v.Field(i))
		if err != nil {
			// Name the field only on the failure path, as structSize does —
			// composing a path eagerly would allocate for every field of
			// every replicated component on every tick.
			return 0, fmt.Errorf("%s.%s: %w", t.Name(), f.Name, err)
		}
		off = next
	}
	return off, nil
}

func (d *decodeState) value(off int, v reflect.Value) (int, error) {
	switch v.Kind() {
	case reflect.Float32:
		if err := d.need(off, 4); err != nil {
			return 0, err
		}
		v.SetFloat(float64(math.Float32frombits(binary.LittleEndian.Uint32(d.data[off:]))))
		return off + 4, nil
	case reflect.Float64:
		if err := d.need(off, 8); err != nil {
			return 0, err
		}
		v.SetFloat(math.Float64frombits(binary.LittleEndian.Uint64(d.data[off:])))
		return off + 8, nil
	case reflect.Uint8:
		if err := d.need(off, 1); err != nil {
			return 0, err
		}
		v.SetUint(uint64(d.data[off]))
		return off + 1, nil
	case reflect.Uint16:
		if err := d.need(off, 2); err != nil {
			return 0, err
		}
		v.SetUint(uint64(binary.LittleEndian.Uint16(d.data[off:])))
		return off + 2, nil
	case reflect.Uint32:
		if err := d.need(off, 4); err != nil {
			return 0, err
		}
		v.SetUint(uint64(binary.LittleEndian.Uint32(d.data[off:])))
		return off + 4, nil
	case reflect.Uint64:
		if err := d.need(off, 8); err != nil {
			return 0, err
		}
		v.SetUint(binary.LittleEndian.Uint64(d.data[off:]))
		return off + 8, nil
	case reflect.Int8:
		if err := d.need(off, 1); err != nil {
			return 0, err
		}
		v.SetInt(int64(int8(d.data[off])))
		return off + 1, nil
	case reflect.Int16:
		if err := d.need(off, 2); err != nil {
			return 0, err
		}
		v.SetInt(int64(int16(binary.LittleEndian.Uint16(d.data[off:]))))
		return off + 2, nil
	case reflect.Int32:
		if err := d.need(off, 4); err != nil {
			return 0, err
		}
		v.SetInt(int64(int32(binary.LittleEndian.Uint32(d.data[off:]))))
		return off + 4, nil
	case reflect.Int64:
		if err := d.need(off, 8); err != nil {
			return 0, err
		}
		v.SetInt(int64(binary.LittleEndian.Uint64(d.data[off:])))
		return off + 8, nil
	case reflect.Bool:
		if err := d.need(off, 1); err != nil {
			return 0, err
		}
		v.SetBool(d.data[off] != 0)
		return off + 1, nil
	case reflect.String:
		if err := d.need(off, 2); err != nil {
			return 0, err
		}
		slen := int(binary.LittleEndian.Uint16(d.data[off:]))
		off += 2
		if slen > d.lim.MaxStringBytes {
			return 0, fmt.Errorf("string of %d bytes exceeds the %d-byte limit", slen, d.lim.MaxStringBytes)
		}
		// The client-reachable vector: slen is fully wire-controlled and was
		// used directly as a slice bound, so four bytes of payload produced a
		// 65535-byte read past the end. need() is the payload-DERIVED half of
		// the check and is strictly stronger than the configurable ceiling —
		// it cannot be misconfigured.
		if err := d.need(off, slen); err != nil {
			return 0, err
		}
		if err := d.charge(uint64(slen)); err != nil {
			return 0, err
		}
		v.SetString(string(d.data[off : off+slen]))
		return off + slen, nil
	case reflect.Array:
		if err := d.enter(); err != nil {
			return 0, err
		}
		defer d.leave()
		for i := range v.Len() {
			next, err := d.value(off, v.Index(i))
			if err != nil {
				return 0, err
			}
			off = next
		}
		return off, nil
	case reflect.Slice:
		return d.slice(off, v)
	case reflect.Struct:
		return d.structFields(off, v)
	default:
		return off, nil
	}
}

// slice decodes both slice forms: the []byte fast path and the generic
// length-prefixed arm. Split out of value because between them they carry every
// allocation this codec makes, and the ordering of check-then-allocate is the
// whole point of CE-002.
func (d *decodeState) slice(off int, v reflect.Value) (int, error) {
	elem := v.Type().Elem()

	// []byte fast path: [u32 len][N bytes]. See valueSize for rationale.
	if elem.Kind() == reflect.Uint8 {
		if err := d.need(off, 4); err != nil {
			return 0, err
		}
		// Read as uint64 rather than int, so the comparisons below cannot
		// themselves overflow on a 32-bit build. A wire-supplied 0xFFFFFFFF
		// is 4 GiB, and every check has to happen before that number is used
		// as a length — see decodeState's doc comment for why reaching the
		// make() at all is the failure, whether or not it survives.
		n := uint64(binary.LittleEndian.Uint32(d.data[off:]))
		off += 4
		if n > uint64(d.lim.MaxBytesFieldLen) {
			return 0, fmt.Errorf("byte field of %d bytes exceeds the %d-byte limit",
				n, d.lim.MaxBytesFieldLen)
		}
		if n > uint64(len(d.data)-off) {
			return 0, fmt.Errorf("byte field of %d bytes exceeds the %d bytes remaining",
				n, len(d.data)-off)
		}
		if err := d.charge(n); err != nil {
			return 0, err
		}
		// Copy out so the decoded slice doesn't alias the caller's data
		// buffer (which the connection layer may reuse).
		out := make([]byte, int(n))
		copy(out, d.data[off:off+int(n)])
		v.SetBytes(out)
		return off + int(n), nil
	}

	if err := d.enter(); err != nil {
		return 0, err
	}
	defer d.leave()

	if err := d.need(off, 2); err != nil {
		return 0, err
	}
	n := int(binary.LittleEndian.Uint16(d.data[off:]))
	off += 2
	if n > d.lim.MaxSliceElems {
		return 0, fmt.Errorf("slice of %d elements exceeds the %d-element limit", n, d.lim.MaxSliceElems)
	}
	// Payload-derived ceiling, the same shape as UnmarshalTransferFrame's
	// "reject impossible counts before allocating attacker-controlled
	// capacity". Strictly stronger than MaxSliceElems whenever the body is
	// small, which is exactly the amplification case: a ~20-byte
	// chat.ChatBulkSetMembersRequest declaring UserIDs length 0xFFFF used to
	// force a 65535-element reservation before a single element was read.
	if elemMin := minWireSize(elem); elemMin > 0 && n > (len(d.data)-off)/elemMin {
		return 0, fmt.Errorf("slice of %d elements needs at least %d bytes, %d remain",
			n, n*elemMin, len(d.data)-off)
	}
	if err := d.charge(uint64(n) * uint64(elem.Size())); err != nil {
		return 0, err
	}
	// Allocate a fresh slice of the right length so the caller's
	// (possibly-shared, possibly-nil) underlying array is not aliased.
	dst := reflect.MakeSlice(v.Type(), n, n)
	for i := range n {
		next, err := d.value(off, dst.Index(i))
		if err != nil {
			return 0, err
		}
		off = next
	}
	v.Set(dst)
	return off, nil
}

// minWireSize reports the fewest bytes one value of type t can occupy on the
// wire, or 0 when no positive minimum exists (an empty struct, or a kind the
// codec skips). Callers treat 0 as "no payload-derived bound available" and
// fall back to the configured ceiling plus the allocation budget.
//
// It terminates on every legal Go type: it recurses only through array elements
// and struct fields, and Go rejects a type that contains itself by value. The
// slice arm deliberately does not recurse — the length prefix is all a slice is
// guaranteed to contribute.
func minWireSize(t reflect.Type) int {
	if codec := LookupReflectCodec(t); codec != nil {
		return codec.Size()
	}
	switch t.Kind() {
	case reflect.Uint8, reflect.Int8, reflect.Bool:
		return 1
	case reflect.Uint16, reflect.Int16:
		return 2
	case reflect.Float32, reflect.Uint32, reflect.Int32:
		return 4
	case reflect.Float64, reflect.Uint64, reflect.Int64:
		return 8
	case reflect.String:
		return 2 // [u16 len] with an empty body
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return 4 // [u32 len] with an empty body
		}
		return 2 // [u16 len] with no elements
	case reflect.Array:
		return t.Len() * minWireSize(t.Elem())
	case reflect.Struct:
		total := 0
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() || f.Type == ecsEntityType {
				continue
			}
			total += minWireSize(f.Type)
		}
		return total
	default:
		return 0
	}
}
