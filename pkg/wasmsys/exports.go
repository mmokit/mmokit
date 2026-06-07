//go:build wasip1

package wasmsys

import (
	"encoding/binary"
	"unsafe"

	"github.com/zenion/mmoserver/pkg/wasmabi"
)

var (
	registered System
	arenaBuf   []byte    // host-writable scratch: [header][column]
	snapBuf    []byte    // holds snapshot bytes so their pointer stays alive
	paramset   *paramSet // built from the registered system in Register
	schemaBuf  []byte    // holds the encoded schema so its pointer stays alive
	stateset   *stateSet // auto-snapshot fields, used when the system isn't Stateful
)

// Register records the module's System. Call from the module's init() (NOT
// main): the host instantiates the module as a reactor, which runs
// _initialize (package init) but never _start (main), so a Register call in
// main would never execute and the ABI exports would trap on a nil system.
func Register(s System) {
	registered = s
	paramset = buildParamSet(s)
	stateset = buildStateSet(s)
}

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
func wasmUpdate(dt float32, nowMs uint64) {
	// Precondition: the host has called wasmsys_arena(>=HeaderSize) and written the header this tick.
	count := binary.LittleEndian.Uint32(arenaBuf[0:4])
	var base unsafe.Pointer
	if count > 0 {
		// Safe to hold across Update: Go's wasip1 GC is currently non-moving, so this pointer stays valid for the duration of the call.
		base = unsafe.Pointer(&arenaBuf[wasmabi.HeaderSize])
	}
	// nowMs is the host's cluster-coherent wall clock (ms), identical across
	// every cell and host this tick — drive time-based animation from it so
	// the result is seamless when an entity crosses a cell boundary.
	registered.Update(&Ctx{base: base, count: count, nowMs: nowMs}, dt)
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
		snapBuf = stateset.marshal()
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
	src := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), int(length))
	cp := make([]byte, length)
	copy(cp, src)
	if s, ok := registered.(Stateful); ok {
		s.Restore(cp)
	} else {
		stateset.unmarshal(cp)
	}
}

//go:wasmexport wasmsys_params_schema
func wasmParamsSchema() uint64 {
	if paramset == nil || len(paramset.schema) == 0 {
		return 0
	}
	schemaBuf = wasmabi.EncodeSchema(paramset.schema)
	if len(schemaBuf) == 0 {
		return 0
	}
	ptr := uint64(uint32(uintptr(unsafe.Pointer(&schemaBuf[0]))))
	return ptr<<32 | uint64(uint32(len(schemaBuf)))
}

//go:wasmexport wasmsys_params_set
func wasmParamsSet(ptr uint32, length uint32) {
	if paramset == nil || length == 0 {
		return
	}
	src := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), int(length))
	cp := make([]byte, length)
	copy(cp, src)
	block, err := wasmabi.DecodeBlock(cp)
	if err != nil {
		return
	}
	paramset.scatter(block)
}
