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
	validateStruct(t, "", false)
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
	validateStruct(t, "", true)
}

func validateStruct(t reflect.Type, path string, allowSlice bool) {
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		fpath := path + "." + f.Name
		if f.Type == ecsEntityType {
			continue // skipped during marshal
		}
		validateType(f.Type, fpath, allowSlice)
	}
}

func validateType(t reflect.Type, path string, allowSlice bool) {
	// Custom-codec-registered types are accepted regardless of their default
	// reflective shape (e.g. mmokit.Entity contains a *Stage pointer the
	// default validator would reject).
	if LookupReflectCodec(t) != nil {
		return
	}
	switch t.Kind() {
	case reflect.Float32, reflect.Float64,
		reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Bool, reflect.String:
		// supported
	case reflect.Array:
		validateType(t.Elem(), path+"[]", allowSlice)
	case reflect.Slice:
		if !allowSlice {
			panic(fmt.Sprintf("reflect_marshal: unsupported type %s at %s (kind=%s)", t, path, t.Kind()))
		}
		// []byte fast path: encoded as [u32 len][bytes] without per-element
		// iteration. Same validity rule as any byte field — always allowed.
		if t.Elem().Kind() == reflect.Uint8 {
			return
		}
		validateType(t.Elem(), path+"[]", allowSlice)
	case reflect.Struct:
		validateStruct(t, path, allowSlice)
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
func ReflectUnmarshalOnStage(stage *Stage, data []byte, ptr any) {
	v := reflect.ValueOf(ptr)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	unmarshalStructOnStage(stage, data, 0, v)
}

// ReflectUnmarshal deserializes binary data into a struct pointer.
// Unexported fields and ecs.Entity fields are left at zero value.
//
// Preserves the no-stage call signature for callers that don't need stage
// context (the existing transfer codec). Equivalent to
// ReflectUnmarshalOnStage(nil, data, ptr).
func ReflectUnmarshal(data []byte, ptr any) {
	ReflectUnmarshalOnStage(nil, data, ptr)
}

// marshalDropInterval bounds how often NoteMarshalDrop writes a line. The value
// that overflows a length prefix is usually content- or client-driven, so an
// unthrottled line per entity per tick turns one bad component into a log flood.
const marshalDropInterval = time.Second

var (
	marshalDropCount atomic.Uint64
	marshalDropLast  atomic.Int64
)

// NoteMarshalDrop counts and reports a value the encoder guards rejected, from
// a call site that holds no *Process or *Stage and therefore no logger: the
// ComponentReplicator Scan closures in component_registry.go, the free function
// BuildTypedEventFrameRaw, and mmokit's chat fanout sender.
//
// It writes through the standard logger rather than a *logger.Logger category
// because Logger.Log is a no-op whenever its category is off, and a dropped
// component is silent state loss on the destination cell — the one class of
// message that must not depend on a debug category being enabled. cat keeps the
// line's shape identical to a wired category line so log filters still match.
//
// Call sites that do hold a logger should use it directly; this is the fallback.
func NoteMarshalDrop(cat string, format string, args ...any) {
	marshalDropCount.Add(1)
	now := time.Now().UnixNano()
	previous := marshalDropLast.Load()
	if now-previous < int64(marshalDropInterval) || !marshalDropLast.CompareAndSwap(previous, now) {
		return
	}
	log.Printf("[%s] %s", cat, fmt.Sprintf(format, args...))
}

// MarshalDrops reports how many values the encoder length guards have rejected
// process-wide since start. Non-zero means a component, event or broadcast was
// dropped rather than written as a frame whose length prefix lies.
func MarshalDrops() uint64 { return marshalDropCount.Load() }

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

func unmarshalStructOnStage(stage *Stage, data []byte, off int, v reflect.Value) int {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() || f.Type == ecsEntityType {
			continue
		}
		if codec := LookupReflectCodec(f.Type); codec != nil {
			codec.Decode(stage, data[off:], v.Field(i))
			off += codec.Size()
			continue
		}
		off = unmarshalValueOnStage(stage, data, off, v.Field(i))
	}
	return off
}

func unmarshalValueOnStage(stage *Stage, data []byte, off int, v reflect.Value) int {
	switch v.Kind() {
	case reflect.Float32:
		v.SetFloat(float64(math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))))
		return off + 4
	case reflect.Float64:
		v.SetFloat(math.Float64frombits(binary.LittleEndian.Uint64(data[off:])))
		return off + 8
	case reflect.Uint8:
		v.SetUint(uint64(data[off]))
		return off + 1
	case reflect.Uint16:
		v.SetUint(uint64(binary.LittleEndian.Uint16(data[off:])))
		return off + 2
	case reflect.Uint32:
		v.SetUint(uint64(binary.LittleEndian.Uint32(data[off:])))
		return off + 4
	case reflect.Uint64:
		v.SetUint(binary.LittleEndian.Uint64(data[off:]))
		return off + 8
	case reflect.Int8:
		v.SetInt(int64(int8(data[off])))
		return off + 1
	case reflect.Int16:
		v.SetInt(int64(int16(binary.LittleEndian.Uint16(data[off:]))))
		return off + 2
	case reflect.Int32:
		v.SetInt(int64(int32(binary.LittleEndian.Uint32(data[off:]))))
		return off + 4
	case reflect.Int64:
		v.SetInt(int64(binary.LittleEndian.Uint64(data[off:])))
		return off + 8
	case reflect.Bool:
		v.SetBool(data[off] != 0)
		return off + 1
	case reflect.String:
		slen := int(binary.LittleEndian.Uint16(data[off:]))
		off += 2
		v.SetString(string(data[off : off+slen]))
		return off + slen
	case reflect.Array:
		for i := range v.Len() {
			off = unmarshalValueOnStage(stage, data, off, v.Index(i))
		}
		return off
	case reflect.Slice:
		// []byte fast path: [u32 len][N bytes]. See valueSize for rationale.
		if v.Type().Elem().Kind() == reflect.Uint8 {
			n := int(binary.LittleEndian.Uint32(data[off:]))
			off += 4
			// Copy out so the decoded slice doesn't alias the caller's
			// data buffer (which the connection layer may reuse).
			out := make([]byte, n)
			copy(out, data[off:off+n])
			v.SetBytes(out)
			return off + n
		}
		n := int(binary.LittleEndian.Uint16(data[off:]))
		off += 2
		// Allocate a fresh slice of the right length so the caller's
		// (possibly-shared, possibly-nil) underlying array is not aliased.
		slice := reflect.MakeSlice(v.Type(), n, n)
		for i := range n {
			off = unmarshalValueOnStage(stage, data, off, slice.Index(i))
		}
		v.Set(slice)
		return off
	case reflect.Struct:
		return unmarshalStructOnStage(stage, data, off, v)
	default:
		return off
	}
}
