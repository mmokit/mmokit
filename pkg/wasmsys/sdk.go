// Package wasmsys is the authoring SDK compiled INTO a hot-swappable system
// module (GOOS=wasip1 GOARCH=wasm). A module defines one System, declares its
// column via Query(), and loops over the column inside Update().
package wasmsys

import (
	"unsafe"

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
	typeID    uint32
	readWrite bool
}

// ReadWrite declares a column the host copies in and reads back after Update.
func ReadWrite[T any](typeID uint32) Query { return Query{typeID, true} }

// Read declares a column the host copies in but does not read back.
func Read[T any](typeID uint32) Query { return Query{typeID, false} }

func (q Query) encode() uint64 { return wasmabi.EncodeQuery(q.typeID, q.readWrite) }

// Ctx is handed to Update. It exposes the mapped column via Column/View. The
// base pointer is the address of the column data (arena + HeaderSize), set by
// the guest's update export; on a native build Ctx is just an inert struct.
type Ctx struct {
	base  unsafe.Pointer
	count uint32
}

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
