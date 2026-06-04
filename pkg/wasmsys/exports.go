//go:build wasip1

package wasmsys

import (
	"encoding/binary"
	"unsafe"

	"github.com/zenion/mmoserver/pkg/wasmabi"
)

var (
	registered System
	arenaBuf   []byte // host-writable scratch: [header][column]
	snapBuf    []byte // holds snapshot bytes so their pointer stays alive
)

// Register records the module's System. Call from the module's main().
func Register(s System) { registered = s }

// Precondition: min >= wasmabi.HeaderSize (the host never requests a smaller arena).
//
//go:wasmexport wasmsys_arena
func wasmArena(min uint32) uint32 {
	if uint32(cap(arenaBuf)) < min {
		arenaBuf = make([]byte, min)
	}
	arenaBuf = arenaBuf[:min]
	return uint32(uintptr(unsafe.Pointer(&arenaBuf[0])))
}

//go:wasmexport wasmsys_init
func wasmInit() {}

//go:wasmexport wasmsys_update
func wasmUpdate(dt float32) {
	// Precondition: the host has called wasmsys_arena(>=HeaderSize) and written the header this tick.
	count := binary.LittleEndian.Uint32(arenaBuf[0:4])
	var base unsafe.Pointer
	if count > 0 {
		// Safe to hold across Update: Go's wasip1 GC is currently non-moving, so this pointer stays valid for the duration of the call.
		base = unsafe.Pointer(&arenaBuf[wasmabi.HeaderSize])
	}
	registered.Update(&Ctx{base: base, count: count}, dt)
}

//go:wasmexport wasmsys_query
func wasmQuery() uint64 { return registered.Query().encode() }

//go:wasmexport wasmsys_abi_version
func wasmABIVersion() uint64 { return wasmabi.ABIVersion }

//go:wasmexport wasmsys_snapshot
func wasmSnapshot() uint64 {
	if s, ok := registered.(Stateful); ok {
		snapBuf = s.Snapshot()
	} else {
		snapBuf = nil
	}
	if len(snapBuf) == 0 {
		return 0
	}
	ptr := uint64(uint32(uintptr(unsafe.Pointer(&snapBuf[0]))))
	return ptr<<32 | uint64(uint32(len(snapBuf)))
}

//go:wasmexport wasmsys_restore
func wasmRestore(ptr uint32, length uint32) {
	if length == 0 {
		return
	}
	if s, ok := registered.(Stateful); ok {
		src := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), int(length))
		cp := make([]byte, length)
		copy(cp, src)
		s.Restore(cp)
	}
}
