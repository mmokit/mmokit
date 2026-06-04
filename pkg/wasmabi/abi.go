// Package wasmabi is the shared host<->guest contract for WASM game systems.
// It has zero dependencies and compiles for both native and wasip1 targets.
package wasmabi

import (
	"errors"
	"unsafe"
)

// ElemSize returns the in-memory byte size of POD component T. Host and guest
// compute it identically, so a module's declared column element size can be
// checked against the type the host registered — catching size/layout
// mismatches that would otherwise corrupt memory in the scatter.
func ElemSize[T any]() uint32 {
	var z T
	return uint32(unsafe.Sizeof(z))
}

// ABIVersion is bumped whenever the export/import contract or the agreed
// component layouts change. The host rejects any module whose embedded
// version differs. Phase 1 will replace the manual bump with a layout hash.
const ABIVersion uint64 = 1

// Exported function names the guest provides (see pkg/wasmsys/exports.go).
const (
	ExportArena      = "wasmsys_arena"       // (min u32) -> ptr u32
	ExportInit       = "wasmsys_init"        // ()
	ExportUpdate     = "wasmsys_update"      // (dt f32)
	ExportSnapshot   = "wasmsys_snapshot"    // () -> (ptr<<32 | len) u64
	ExportRestore    = "wasmsys_restore"     // (ptr u32, len u32)
	ExportQuery      = "wasmsys_query"       // () -> encoded query u64
	ExportABIVersion = "wasmsys_abi_version" // () -> u64

	ExportParamsSchema = "wasmsys_params_schema" // () -> (ptr<<32 | len) u64
	ExportParamsSet    = "wasmsys_params_set"    // (ptr u32, len u32)
)

// HeaderSize is the byte length of the per-tick batch header written at the
// start of the arena. Layout: [count u32][pad u32], 8-aligned so the column
// array that follows is 8-byte aligned for any POD component.
const HeaderSize = 8

// EncodeQuery packs a column element size (in bytes) and read/write mode into
// one u64. Bit 0 is the write-back flag; bits 1.. are the element size.
func EncodeQuery(elemSize uint32, readWrite bool) uint64 {
	v := uint64(elemSize) << 1
	if readWrite {
		v |= 1
	}
	return v
}

// DecodeQuery is the inverse of EncodeQuery. The returned uint32 is the column
// element size in bytes.
func DecodeQuery(q uint64) (elemSize uint32, readWrite bool) {
	return uint32(q >> 1), q&1 == 1
}

// Bounds-presence bits in ParamField.BoundsMask.
const (
	BoundDefault uint8 = 1 << iota
	BoundMin
	BoundMax
	BoundStep
)

// ParamField is one tunable in a module's schema. Kind is uint8(tunable.Kind)
// — wasmabi stays dependency-free; the host/guest map it to tunable.Kind.
type ParamField struct {
	Name                    string
	Kind                    uint8
	Default, Min, Max, Step float64
	BoundsMask              uint8
}

// EncodeSchema serializes a module's param schema. Layout:
//
//	u16 count, then per field:
//	u8 nameLen, name bytes, u8 kind, 4×f64 (default,min,max,step LE), u8 mask.
func EncodeSchema(fields []ParamField) []byte {
	buf := make([]byte, 0, 2+len(fields)*40)
	buf = appendU16(buf, uint16(len(fields)))
	for _, f := range fields {
		buf = append(buf, byte(len(f.Name)))
		buf = append(buf, f.Name...)
		buf = append(buf, f.Kind)
		buf = appendF64(buf, f.Default)
		buf = appendF64(buf, f.Min)
		buf = appendF64(buf, f.Max)
		buf = appendF64(buf, f.Step)
		buf = append(buf, f.BoundsMask)
	}
	return buf
}

// DecodeSchema is the inverse of EncodeSchema.
func DecodeSchema(b []byte) ([]ParamField, error) {
	r := reader{b: b}
	n, ok := r.u16()
	if !ok {
		return nil, errShort
	}
	out := make([]ParamField, 0, n)
	for range int(n) {
		nl, ok := r.u8()
		if !ok {
			return nil, errShort
		}
		name, ok := r.bytes(int(nl))
		if !ok {
			return nil, errShort
		}
		kind, ok := r.u8()
		if !ok {
			return nil, errShort
		}
		def, ok1 := r.f64()
		mn, ok2 := r.f64()
		mx, ok3 := r.f64()
		st, ok4 := r.f64()
		mask, ok5 := r.u8()
		if !(ok1 && ok2 && ok3 && ok4 && ok5) {
			return nil, errShort
		}
		out = append(out, ParamField{
			Name: string(name), Kind: kind,
			Default: def, Min: mn, Max: mx, Step: st, BoundsMask: mask,
		})
	}
	return out, nil
}

// EncodeBlock serializes a value block ([]float64) little-endian.
func EncodeBlock(vals []float64) []byte {
	buf := make([]byte, 0, len(vals)*8)
	for _, v := range vals {
		buf = appendF64(buf, v)
	}
	return buf
}

// DecodeBlock reads a little-endian []float64 value block.
func DecodeBlock(b []byte) ([]float64, error) {
	if len(b)%8 != 0 {
		return nil, errShort
	}
	out := make([]float64, len(b)/8)
	r := reader{b: b}
	for i := range out {
		v, ok := r.f64()
		if !ok {
			return nil, errShort
		}
		out[i] = v
	}
	return out, nil
}

var errShort = errors.New("wasmabi: short buffer")

func appendU16(b []byte, v uint16) []byte { return append(b, byte(v), byte(v>>8)) }

func appendF64(b []byte, f float64) []byte {
	u := f64bits(f)
	return append(b, byte(u), byte(u>>8), byte(u>>16), byte(u>>24),
		byte(u>>32), byte(u>>40), byte(u>>48), byte(u>>56))
}

type reader struct {
	b   []byte
	off int
}

func (r *reader) u8() (uint8, bool) {
	if r.off+1 > len(r.b) {
		return 0, false
	}
	v := r.b[r.off]
	r.off++
	return v, true
}

func (r *reader) u16() (uint16, bool) {
	if r.off+2 > len(r.b) {
		return 0, false
	}
	v := uint16(r.b[r.off]) | uint16(r.b[r.off+1])<<8
	r.off += 2
	return v, true
}

func (r *reader) f64() (float64, bool) {
	if r.off+8 > len(r.b) {
		return 0, false
	}
	var u uint64
	for i := range 8 {
		u |= uint64(r.b[r.off+i]) << (8 * i)
	}
	r.off += 8
	return f64from(u), true
}

func (r *reader) bytes(n int) ([]byte, bool) {
	if r.off+n > len(r.b) {
		return nil, false
	}
	v := r.b[r.off : r.off+n]
	r.off += n
	return v, true
}

func f64bits(f float64) uint64 { return *(*uint64)(unsafe.Pointer(&f)) }
func f64from(u uint64) float64 { return *(*float64)(unsafe.Pointer(&u)) }
