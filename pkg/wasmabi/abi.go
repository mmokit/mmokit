// Package wasmabi is the shared host<->guest contract for WASM game systems.
// It has zero dependencies and compiles for both native and wasip1 targets.
package wasmabi

import "unsafe"

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
