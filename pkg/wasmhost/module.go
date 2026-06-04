package wasmhost

import (
	"context"
	"encoding/binary"
	"fmt"

	"github.com/tetratelabs/wazero/api"
	"github.com/zenion/mmoserver/pkg/wasmabi"
)

// Module is one instantiated system. Not safe for concurrent use — each cell
// owns its own Module and only ever calls it on that cell's loop goroutine.
type Module struct {
	mod        api.Module
	arena      api.Function
	update     api.Function
	query      api.Function
	abiVersion api.Function
	snapshot   api.Function
	restore    api.Function
}

// Load instantiates a compiled wasm image and binds its exports.
func Load(ctx context.Context, rt *Runtime, wasm []byte) (*Module, error) {
	mod, err := rt.Instantiate(ctx, wasm)
	if err != nil {
		return nil, err
	}
	m := &Module{
		mod:        mod,
		arena:      mod.ExportedFunction(wasmabi.ExportArena),
		update:     mod.ExportedFunction(wasmabi.ExportUpdate),
		query:      mod.ExportedFunction(wasmabi.ExportQuery),
		abiVersion: mod.ExportedFunction(wasmabi.ExportABIVersion),
		snapshot:   mod.ExportedFunction(wasmabi.ExportSnapshot),
		restore:    mod.ExportedFunction(wasmabi.ExportRestore),
	}
	for name, fn := range map[string]api.Function{
		wasmabi.ExportArena: m.arena, wasmabi.ExportUpdate: m.update,
		wasmabi.ExportQuery: m.query, wasmabi.ExportABIVersion: m.abiVersion,
	} {
		if fn == nil {
			mod.Close(ctx)
			return nil, fmt.Errorf("wasmhost: module missing export %q", name)
		}
	}
	return m, nil
}

func (m *Module) ABIVersion(ctx context.Context) uint64 {
	r, _ := m.abiVersion.Call(ctx)
	return r[0]
}

func (m *Module) Query(ctx context.Context) (typeID uint32, readWrite bool) {
	r, _ := m.query.Call(ctx)
	return wasmabi.DecodeQuery(r[0])
}

// arenaWrite grows the guest arena to fit a header + payload, writes the
// header (count) and payload, and returns the arena base pointer.
func (m *Module) arenaWrite(ctx context.Context, count uint32, payload []byte) (uint32, error) {
	need := uint32(wasmabi.HeaderSize + len(payload))
	r, err := m.arena.Call(ctx, uint64(need))
	if err != nil {
		return 0, err
	}
	ptr := uint32(r[0])
	var hdr [wasmabi.HeaderSize]byte
	binary.LittleEndian.PutUint32(hdr[0:4], count)
	if !m.mod.Memory().Write(ptr, hdr[:]) {
		return 0, fmt.Errorf("wasmhost: header write out of range at %d", ptr)
	}
	if len(payload) > 0 && !m.mod.Memory().Write(ptr+wasmabi.HeaderSize, payload) {
		return 0, fmt.Errorf("wasmhost: payload write out of range")
	}
	return ptr, nil
}

// Update bridges one tick: write count+column into the arena, call update,
// and read the (possibly mutated) column back. in/out lengths match.
func (m *Module) Update(ctx context.Context, count uint32, dt float32, in []byte) ([]byte, error) {
	ptr, err := m.arenaWrite(ctx, count, in)
	if err != nil {
		return nil, err
	}
	if _, err := m.update.Call(ctx, api.EncodeF32(dt)); err != nil {
		return nil, err
	}
	out, ok := m.mod.Memory().Read(ptr+wasmabi.HeaderSize, uint32(len(in)))
	if !ok {
		return nil, fmt.Errorf("wasmhost: column read-back out of range")
	}
	cp := make([]byte, len(out))
	copy(cp, out)
	return cp, nil
}

func (m *Module) Snapshot(ctx context.Context) ([]byte, error) {
	if m.snapshot == nil {
		return nil, nil
	}
	r, err := m.snapshot.Call(ctx)
	if err != nil {
		return nil, err
	}
	packed := r[0]
	ptr, length := uint32(packed>>32), uint32(packed)
	if length == 0 {
		return nil, nil
	}
	b, ok := m.mod.Memory().Read(ptr, length)
	if !ok {
		return nil, fmt.Errorf("wasmhost: snapshot read out of range")
	}
	cp := make([]byte, length)
	copy(cp, b)
	return cp, nil
}

func (m *Module) Restore(ctx context.Context, state []byte) error {
	if m.restore == nil || len(state) == 0 {
		return nil
	}
	ptr, err := m.arenaWrite(ctx, 0, state) // reuse arena as inbound buffer
	if err != nil {
		return err
	}
	_, err = m.restore.Call(ctx, uint64(ptr+wasmabi.HeaderSize), uint64(len(state)))
	return err
}

func (m *Module) Close(ctx context.Context) error { return m.mod.Close(ctx) }
