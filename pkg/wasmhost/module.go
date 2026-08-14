package wasmhost

import (
	"context"
	"encoding/binary"
	"fmt"

	"github.com/tetratelabs/wazero/api"
	"github.com/zenion/mmokit/pkg/wasmabi"
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

	paramsSchema api.Function
	paramsSet    api.Function
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
	m.paramsSchema = mod.ExportedFunction(wasmabi.ExportParamsSchema)
	m.paramsSet = mod.ExportedFunction(wasmabi.ExportParamsSet)
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

func (m *Module) Query(ctx context.Context) (elemSize uint32, readWrite bool) {
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
// and read the (possibly mutated) column back. in/out lengths match. nowMs is
// the host's cluster-coherent wall clock (ms) for this tick, passed through to
// the guest so time-based systems share one timeline across cells/hosts.
func (m *Module) Update(ctx context.Context, count uint32, dt float32, nowMs uint64, in []byte) ([]byte, error) {
	ptr, err := m.arenaWrite(ctx, count, in)
	if err != nil {
		return nil, err
	}
	if _, err := m.update.Call(ctx, api.EncodeF32(dt), nowMs); err != nil {
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

// ParamsSchema returns the module's tunable schema, or nil if the module
// declares no params (export absent or empty).
func (m *Module) ParamsSchema(ctx context.Context) ([]wasmabi.ParamField, error) {
	if m.paramsSchema == nil {
		return nil, nil
	}
	r, err := m.paramsSchema.Call(ctx)
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
		return nil, fmt.Errorf("wasmhost: params schema read out of range")
	}
	cp := make([]byte, length)
	copy(cp, b)
	return wasmabi.DecodeSchema(cp)
}

// ParamsSet pushes a value block (one float64 per schema field, in order) into
// the guest. No-op if the module declares no params.
func (m *Module) ParamsSet(ctx context.Context, block []float64) error {
	if m.paramsSet == nil || len(block) == 0 {
		return nil
	}
	payload := wasmabi.EncodeBlock(block)
	ptr, err := m.arenaWrite(ctx, 0, payload)
	if err != nil {
		return err
	}
	_, err = m.paramsSet.Call(ctx, uint64(ptr+wasmabi.HeaderSize), uint64(len(payload)))
	return err
}

func (m *Module) Close(ctx context.Context) error { return m.mod.Close(ctx) }
