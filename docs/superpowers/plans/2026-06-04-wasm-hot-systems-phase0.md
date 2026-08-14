# WASM Hot-Swappable Systems — Phase 0 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove a single game-logic system (`ShieldRegen`) can be authored in Go, compiled to WASM, and loaded / unloaded / hot-swapped into a running single-process server with state continuity and no per-entity boundary cost.

**Architecture:** A swappable system is a `.wasm` module (Go → `wasip1/wasm`) authored against the `pkg/wasmsys` SDK. The native host (`pkg/wasmhost`, ECS-agnostic) embeds the **wazero** runtime, bulk-copies the system's declared POD component column into the module's linear memory once per tick, calls the module's `update`, and copies the column back — boundary crossed twice per tick, never per entity. A generic adapter (`mmokit.NewWasmSystem[T]`) presents the module to the existing game loop as an ordinary `engine.System`, and `engine.GameLoop` gains live add/remove so modules can be swapped between ticks.

**Tech Stack:** Go 1.26 (`//go:wasmexport`, `GOOS=wasip1 GOARCH=wasm`), [wazero](https://github.com/tetratelabs/wazero) pure-Go WASM runtime, ark ECS (host-side only), the existing mmokit/universe/engine layers.

**Spec:** [docs/superpowers/specs/2026-06-04-hot-swappable-wasm-systems-design.md](../specs/2026-06-04-hot-swappable-wasm-systems-design.md)

---

## File Structure

**New packages (host + shared):**
- `pkg/wasmabi/abi.go` — shared ABI contract: version constant, export/import names, query encode/decode, arena header layout. POD, zero deps, compiles for both native and `wasip1`.
- `pkg/wasmhost/runtime.go` — wazero runtime wrapper; compile-once.
- `pkg/wasmhost/module.go` — a loaded module instance: arena alloc, memory bridge, `Update`/`Snapshot`/`Restore`/`Query`/`ABIVersion`/`Close`. **Knows nothing about ECS** — operates on raw `[]byte` columns.
- `pkg/wasmhost/runtime_test.go`, `pkg/wasmhost/module_test.go` — integration tests against a built fixture module.

**New packages (guest SDK):**
- `pkg/wasmsys/sdk.go` — authoring surface: `Ctx`, `Query`, `ReadWrite[T]`, `Read[T]`, `Column[T]`, `View[T]`, `Register`, the `System` interface, optional `Stateful`.
- `pkg/wasmsys/exports.go` — `//go:wasmexport` ABI functions wiring the registered system to the host.

**Adapter + loop integration:**
- `pkg/mmokit/wasm_system.go` — `NewWasmSystem[T any](modulePath string) SystemDef`; the generic POD gather/scatter adapter; per-cell instantiation.
- `pkg/mmokit/wasm_system_test.go` — native-vs-wasm equivalence + swap-with-state integration tests.
- `pkg/engine/loop.go` (modify) — `AddSystemLive` / `RemoveSystemLive`.
- `pkg/engine/loop_live_test.go` — live add/remove unit tests.

**Demo module + tooling:**
- `examples/4node-basic/wasmmods/shieldregen/main.go` — the demo system source (with an internal counter to exercise state continuity).
- `pkg/wasmhost/internal/testmod/main.go` — minimal fixture module used by host tests.
- `justfile` (modify) — `wasm-build` recipe.

**Benchmark + console:**
- `pkg/mmokit/wasm_system_bench_test.go` — native vs wasm at N=100/1k/10k.
- `examples/4node-basic/main.go` (modify) — `wasm load/unload/swap` console command for the live demo.

---

## Task 1: Add wazero and prove Go→wasip1 exports run under it

This de-risks the entire approach up front: a Go module compiled to `wasip1` with `//go:wasmexport`, instantiated by wazero as a **reactor** (run `_initialize`, not `_start`), with an exported function called from the host.

**Files:**
- Create: `pkg/wasmhost/internal/testmod/main.go`
- Create: `pkg/wasmhost/runtime.go`
- Create: `pkg/wasmhost/runtime_test.go`
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add the wazero dependency**

Run:
```bash
go get github.com/tetratelabs/wazero@v1.8.2
```
Expected: `go.mod` gains `github.com/tetratelabs/wazero v1.8.2`.

- [ ] **Step 2: Write the fixture module**

Create `pkg/wasmhost/internal/testmod/main.go`:
```go
//go:build wasip1

package main

// add is the smoke-test export: host calls add(2,3) and expects 5.
//
//go:wasmexport add
func add(a int32, b int32) int32 { return a + b }

// main must exist for the toolchain but is never run — the host
// instantiates this as a reactor and calls _initialize, not _start.
func main() {}
```

- [ ] **Step 3: Write the failing host test**

Create `pkg/wasmhost/runtime_test.go`:
```go
package wasmhost

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// buildModule compiles a Go package at srcDir to a wasip1 .wasm and returns its bytes.
func buildModule(t *testing.T, srcDir string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "mod.wasm")
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = srcDir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", srcDir, err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRuntime_CallsExportedAdd(t *testing.T) {
	wasm := buildModule(t, "internal/testmod")
	rt := New(context.Background())
	defer rt.Close(context.Background())

	mod, err := rt.Instantiate(context.Background(), wasm)
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	defer mod.Close(context.Background())

	res, err := mod.ExportedFunction("add").Call(context.Background(), 2, 3)
	if err != nil {
		t.Fatalf("call add: %v", err)
	}
	if res[0] != 5 {
		t.Fatalf("add(2,3) = %d, want 5", res[0])
	}
}
```

- [ ] **Step 4: Run it to verify it fails**

Run: `go test ./pkg/wasmhost/ -run TestRuntime_CallsExportedAdd -v`
Expected: FAIL — `New`/`Instantiate` undefined.

- [ ] **Step 5: Implement the runtime wrapper**

Create `pkg/wasmhost/runtime.go`:
```go
// Package wasmhost embeds a WASM runtime and bridges raw component-column
// bytes in and out of guest linear memory. It is ECS-agnostic: callers
// (the mmokit adapter) own all gather/scatter against the world.
package wasmhost

import (
	"context"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/api"
)

// Runtime wraps a wazero runtime. Compile modules once, instantiate per cell.
type Runtime struct {
	rt wazero.Runtime
}

// New creates a Runtime with the WASI preview1 host functions Go's wasip1
// port requires.
func New(ctx context.Context) *Runtime {
	rt := wazero.NewRuntime(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)
	return &Runtime{rt: rt}
}

// Instantiate loads a wasm image as a reactor: _start is suppressed so the
// module does not run-and-exit; _initialize is invoked so package init and
// the wasmsys.Register call run, leaving exports callable.
func (r *Runtime) Instantiate(ctx context.Context, wasm []byte) (api.Module, error) {
	cfg := wazero.NewModuleConfig().WithStartFunctions("_initialize")
	return r.rt.InstantiateWithConfig(ctx, wasm, cfg)
}

func (r *Runtime) Close(ctx context.Context) error { return r.rt.Close(ctx) }
```

- [ ] **Step 6: Run it to verify it passes**

Run: `go test ./pkg/wasmhost/ -run TestRuntime_CallsExportedAdd -v`
Expected: PASS.

> If instantiation errors with a missing `_initialize`, the toolchain emitted a command not a reactor. Confirm Go ≥ 1.24 and that `main()` is present but empty; `//go:wasmexport` makes the binary a reactor automatically on `wasip1`.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum pkg/wasmhost/
git commit -m "feat(wasmhost): wazero runtime + Go wasip1 reactor smoke test"
```

---

## Task 2: The shared ABI contract (`pkg/wasmabi`)

The contract both sides agree on: version, export names, the query encoding, and the arena header layout.

**Files:**
- Create: `pkg/wasmabi/abi.go`
- Create: `pkg/wasmabi/abi_test.go`

- [ ] **Step 1: Write the failing test**

Create `pkg/wasmabi/abi_test.go`:
```go
package wasmabi

import "testing"

func TestEncodeQuery_RoundTrips(t *testing.T) {
	q := EncodeQuery(7, true)
	id, rw := DecodeQuery(q)
	if id != 7 || !rw {
		t.Fatalf("got id=%d rw=%v, want 7,true", id, rw)
	}
	id, rw = DecodeQuery(EncodeQuery(3, false))
	if id != 3 || rw {
		t.Fatalf("got id=%d rw=%v, want 3,false", id, rw)
	}
}

func TestHeaderSize_Aligned(t *testing.T) {
	if HeaderSize%8 != 0 {
		t.Fatalf("HeaderSize=%d must be 8-aligned", HeaderSize)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./pkg/wasmabi/ -v`
Expected: FAIL — undefined `EncodeQuery`.

- [ ] **Step 3: Implement the contract**

Create `pkg/wasmabi/abi.go`:
```go
// Package wasmabi is the shared host<->guest contract for WASM game systems.
// It has zero dependencies and compiles for both native and wasip1 targets.
package wasmabi

// ABIVersion is bumped whenever the export/import contract or the agreed
// component layouts change. The host rejects any module whose embedded
// version differs. Phase 1 will replace the manual bump with a layout hash.
const ABIVersion uint64 = 1

// Exported function names the guest provides (see pkg/wasmsys/exports.go).
const (
	ExportArena       = "wasmsys_arena"        // (min u32) -> ptr u32
	ExportInit        = "wasmsys_init"         // ()
	ExportUpdate      = "wasmsys_update"       // (dt f32)
	ExportSnapshot    = "wasmsys_snapshot"     // () -> (ptr<<32 | len) u64
	ExportRestore     = "wasmsys_restore"      // (ptr u32, len u32)
	ExportQuery       = "wasmsys_query"        // () -> encoded query u64
	ExportABIVersion  = "wasmsys_abi_version"  // () -> u64
)

// HeaderSize is the byte length of the per-tick batch header written at the
// start of the arena. Layout: [count u32][pad u32], 8-aligned so the column
// array that follows is 8-byte aligned for any POD component.
const HeaderSize = 8

// EncodeQuery packs a component type id and read/write mode into one u64.
// Bit 0 is the write-back flag; bits 1.. are the type id.
func EncodeQuery(typeID uint32, readWrite bool) uint64 {
	v := uint64(typeID) << 1
	if readWrite {
		v |= 1
	}
	return v
}

// DecodeQuery is the inverse of EncodeQuery.
func DecodeQuery(q uint64) (typeID uint32, readWrite bool) {
	return uint32(q >> 1), q&1 == 1
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./pkg/wasmabi/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/wasmabi/
git commit -m "feat(wasmabi): shared host/guest ABI contract (version, query, header)"
```

---

## Task 3: The guest authoring SDK (`pkg/wasmsys`)

The surface a system author writes against, plus the `//go:wasmexport` glue that connects a registered system to the host. Single read-write POD column for Phase 0.

**Files:**
- Create: `pkg/wasmsys/sdk.go`
- Create: `pkg/wasmsys/exports.go`
- Create: `pkg/wasmsys/doc_build_test.go`

- [ ] **Step 1: Implement the SDK surface**

Create `pkg/wasmsys/sdk.go`:
```go
// Package wasmsys is the authoring SDK compiled INTO a hot-swappable system
// module (GOOS=wasip1 GOARCH=wasm). A module defines one System, declares its
// column via Query(), and loops over the column inside Update().
package wasmsys

import (
	"unsafe"

	"github.com/zenion/mmokit/pkg/wasmabi"
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

// Ctx is handed to Update. It exposes the mapped column via Column/View.
type Ctx struct {
	count uint32
}

// Column returns a writable view over the host-mapped column. Mutations are
// read back by the host when the system declared ReadWrite.
func Column[T any](ctx *Ctx) []T {
	if ctx.count == 0 {
		return nil
	}
	base := unsafe.Pointer(uintptr(arenaPtr()) + uintptr(wasmabi.HeaderSize))
	return unsafe.Slice((*T)(base), int(ctx.count))
}

// View is Column for read-only systems (identical mechanics; intent marker
// that becomes the RW/RO discriminator for the Phase 1 codegen).
func View[T any](ctx *Ctx) []T { return Column[T](ctx) }
```

- [ ] **Step 2: Implement the exports + arena**

Create `pkg/wasmsys/exports.go`:
```go
//go:build wasip1

package wasmsys

import (
	"encoding/binary"
	"unsafe"

	"github.com/zenion/mmokit/pkg/wasmabi"
)

var (
	registered System
	arenaBuf   []byte // host-writable scratch: [header][column]
	snapBuf    []byte // holds snapshot bytes so their pointer stays alive
)

// Register records the module's System. Call from the module's main().
func Register(s System) { registered = s }

func arenaPtr() uint32 { return uint32(uintptr(unsafe.Pointer(&arenaBuf[0]))) }

//go:wasmexport wasmsys_arena
func wasmArena(min uint32) uint32 {
	if uint32(cap(arenaBuf)) < min {
		arenaBuf = make([]byte, min)
	}
	arenaBuf = arenaBuf[:min]
	return arenaPtr()
}

//go:wasmexport wasmsys_init
func wasmInit() {}

//go:wasmexport wasmsys_update
func wasmUpdate(dt float32) {
	count := binary.LittleEndian.Uint32(arenaBuf[0:4])
	registered.Update(&Ctx{count: count}, dt)
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
	return ptr<<32 | uint64(len(snapBuf))
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
```

- [ ] **Step 3: Add a build-only guard test**

Create `pkg/wasmsys/doc_build_test.go`:
```go
package wasmsys

import "testing"

// The native build only compiles sdk.go (exports.go is wasip1-tagged). This
// test exists so `go test ./pkg/wasmsys/` exercises the package on the host
// toolchain; the real wasip1 compile is validated by Task 5's fixture build.
func TestPackageCompiles(t *testing.T) {
	_ = ReadWrite[int32](1)
}
```

- [ ] **Step 4: Verify native build + wasip1 build both succeed**

Run:
```bash
go test ./pkg/wasmsys/ -run TestPackageCompiles -v
GOOS=wasip1 GOARCH=wasm go build ./pkg/wasmsys/
```
Expected: native test PASS; wasip1 build produces no output and no error.

- [ ] **Step 5: Commit**

```bash
git add pkg/wasmsys/
git commit -m "feat(wasmsys): guest authoring SDK + wasip1 ABI exports"
```

---

## Task 4: The host-side module (`pkg/wasmhost.Module`) — arena bridge + lifecycle

Wraps an instantiated module with the per-tick column bridge and the lifecycle calls, all over raw bytes.

**Files:**
- Create: `pkg/wasmhost/module.go`
- Create: `pkg/wasmhost/internal/echomod/main.go` (fixture: a 1-column system that increments each element)
- Create: `pkg/wasmhost/module_test.go`

- [ ] **Step 1: Write the fixture system module**

Create `pkg/wasmhost/internal/echomod/main.go`:
```go
//go:build wasip1

package main

import "github.com/zenion/mmokit/pkg/wasmsys"

// inc adds 1.0 to every float32 in its column each tick, and counts ticks in
// internal state to exercise snapshot/restore.
type inc struct{ ticks uint32 }

func (s *inc) Query() wasmsys.Query { return wasmsys.ReadWrite[float32](42) }

func (s *inc) Update(ctx *wasmsys.Ctx, dt float32) {
	col := wasmsys.Column[float32](ctx)
	for i := range col {
		col[i] += 1
	}
	s.ticks++
}

func (s *inc) Snapshot() []byte {
	return []byte{byte(s.ticks), byte(s.ticks >> 8), byte(s.ticks >> 16), byte(s.ticks >> 24)}
}
func (s *inc) Restore(b []byte) {
	if len(b) == 4 {
		s.ticks = uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
	}
}

func main() { wasmsys.Register(&inc{}) }
```

- [ ] **Step 2: Write the failing host test**

Create `pkg/wasmhost/module_test.go`:
```go
package wasmhost

import (
	"context"
	"encoding/binary"
	"math"
	"testing"
)

func f32sToBytes(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}
func bytesToF32s(b []byte) []float32 {
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return out
}

func TestModule_UpdateBridgesColumn(t *testing.T) {
	ctx := context.Background()
	rt := New(ctx)
	defer rt.Close(ctx)
	m, err := Load(ctx, rt, buildModule(t, "internal/echomod"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(ctx)

	if got := m.ABIVersion(ctx); got != 1 {
		t.Fatalf("ABIVersion=%d want 1", got)
	}
	id, rw := m.Query(ctx)
	if id != 42 || !rw {
		t.Fatalf("Query=(%d,%v) want (42,true)", id, rw)
	}

	in := f32sToBytes([]float32{1, 2, 3})
	out, err := m.Update(ctx, 3, 0.05, in)
	if err != nil {
		t.Fatal(err)
	}
	got := bytesToF32s(out)
	want := []float32{2, 3, 4}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("col[%d]=%v want %v", i, got[i], want[i])
		}
	}
}

func TestModule_SnapshotRestore(t *testing.T) {
	ctx := context.Background()
	rt := New(ctx)
	defer rt.Close(ctx)
	wasm := buildModule(t, "internal/echomod")

	m1, _ := Load(ctx, rt, wasm)
	in := f32sToBytes([]float32{0})
	m1.Update(ctx, 1, 0.05, in)
	m1.Update(ctx, 1, 0.05, in) // ticks == 2
	state, err := m1.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m1.Close(ctx)

	m2, _ := Load(ctx, rt, wasm)
	defer m2.Close(ctx)
	if err := m2.Restore(ctx, state); err != nil {
		t.Fatal(err)
	}
	s2, _ := m2.Snapshot(ctx) // ticks should still read 2 (4 LE bytes)
	if len(s2) != 4 || s2[0] != 2 {
		t.Fatalf("restored ticks = %v, want first byte 2", s2)
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./pkg/wasmhost/ -run TestModule -v`
Expected: FAIL — undefined `Load`.

- [ ] **Step 4: Implement the module bridge**

Create `pkg/wasmhost/module.go`:
```go
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
```

- [ ] **Step 5: Run it to verify it passes**

Run: `go test ./pkg/wasmhost/ -run TestModule -v`
Expected: PASS (both `TestModule_UpdateBridgesColumn` and `TestModule_SnapshotRestore`).

- [ ] **Step 6: Commit**

```bash
git add pkg/wasmhost/
git commit -m "feat(wasmhost): per-tick column bridge + snapshot/restore lifecycle"
```

---

## Task 5: Generic POD adapter (`mmokit.NewWasmSystem[T]`)

Presents a module as an ordinary `engine.System`. Gathers the declared POD column from the cell's ECS into bytes, runs the module, scatters the result back — using the exact mmokit query primitives the native system would.

**Files:**
- Create: `pkg/mmokit/wasm_system.go`
- Create: `pkg/mmokit/wasm_system_test.go`

- [ ] **Step 1: Write the failing equivalence test**

Create `pkg/mmokit/wasm_system_test.go`. This test builds the demo module (Task 6 supplies it; for now point at the host echomod-equivalent shield module path created here), spawns N entities with a POD component, and asserts the wasm system mutates them identically to a hand-rolled native loop.

```go
package mmokit_test

import (
	"testing"

	"github.com/zenion/mmokit/pkg/mmokit"
)

// PodVal is a minimal POD component for the adapter test.
type PodVal struct{ X float32 }

func TestWasmSystem_MatchesNativeLoop(t *testing.T) {
	wasmPath := buildShieldishModule(t) // helper defined below; builds testdata module
	stage := mmokit.NewTestStage(t)     // see note in Step 3 on the test stage helper

	// Spawn 5 entities with PodVal{X: i}.
	want := make([]float32, 5)
	for i := 0; i < 5; i++ {
		mmokit.SpawnForTest(stage, PodVal{X: float32(i)})
		want[i] = float32(i) + 1 // module adds 1 per tick
	}

	sys := mmokit.NewWasmSystem[PodVal](wasmPath)
	mmokit.RunSystemOneTick(t, stage, sys, 0.05)

	got := mmokit.CollectForTest[PodVal](stage)
	for i := range want {
		if got[i].X != want[i] {
			t.Fatalf("entity %d X=%v want %v", i, got[i].X, want[i])
		}
	}
}
```

> **Note on test helpers:** `NewTestStage`, `SpawnForTest`, `RunSystemOneTick`, `CollectForTest`, and `buildShieldishModule` are thin test-only helpers. Check whether equivalents already exist in `pkg/mmokit` test files (`rg -n "func NewTestStage|SpawnForTest|RunSystemOneTick" pkg/`); reuse them if present. If absent, add them in `pkg/mmokit/wasm_testutil_test.go` wrapping the existing universe test-stage construction used elsewhere in `pkg/mmokit/*_test.go`. The module built by `buildShieldishModule` is a `wasip1` package identical in shape to `pkg/wasmhost/internal/echomod` but operating on a `PodVal`-sized (one float32) column with typeID matching `NewWasmSystem`'s expectation.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./pkg/mmokit/ -run TestWasmSystem_MatchesNativeLoop -v`
Expected: FAIL — undefined `NewWasmSystem`.

- [ ] **Step 3: Implement the adapter**

Create `pkg/mmokit/wasm_system.go`:
```go
package mmokit

import (
	"context"
	"fmt"
	"os"
	"unsafe"

	"github.com/zenion/mmokit/pkg/engine"
	"github.com/zenion/mmokit/pkg/wasmabi"
	"github.com/zenion/mmokit/pkg/wasmhost"
)

// wasmRuntime is a process-wide runtime shared by all wasm systems; module
// instances are per-cell and independent.
var wasmRuntime = wasmhost.New(context.Background())

// NewWasmSystem builds a SystemDef whose per-cell instances run a hot-loadable
// WASM module over the POD component T. T must be a value-type (no pointers/
// slices/maps) — its in-memory layout is memcpy'd across the boundary.
//
// The module bytes are read and ABI-checked once at registration; each cell
// instantiates its own module with independent linear memory/state.
func NewWasmSystem[T any](modulePath string) SystemDef {
	wasm, err := os.ReadFile(modulePath)
	if err != nil {
		panic(fmt.Sprintf("mmokit.NewWasmSystem: %v", err))
	}
	// One-time ABI handshake against a throwaway instance.
	probe, err := wasmhost.Load(context.Background(), wasmRuntime, wasm)
	if err != nil {
		panic(fmt.Sprintf("mmokit.NewWasmSystem: load %s: %v", modulePath, err))
	}
	if v := probe.ABIVersion(context.Background()); v != wasmabi.ABIVersion {
		probe.Close(context.Background())
		panic(fmt.Sprintf("mmokit.NewWasmSystem: %s ABI v%d, host v%d", modulePath, v, wasmabi.ABIVersion))
	}
	_, readWrite := probe.Query(context.Background())
	probe.Close(context.Background())

	return SystemDef{
		Name: "Wasm:" + baseName(modulePath),
		Factory: func() System {
			mod, err := wasmhost.Load(context.Background(), wasmRuntime, wasm)
			if err != nil {
				panic(fmt.Sprintf("mmokit.NewWasmSystem: per-cell load: %v", err))
			}
			return &wasmSystem[T]{mod: mod, readWrite: readWrite}
		},
	}
}

type wasmSystem[T any] struct {
	SystemBase
	mod       *wasmhost.Module
	readWrite bool
	col       []T // reused gather buffer
}

func (s *wasmSystem[T]) Update(dt float32) {
	stage := s.Stage()
	s.col = s.col[:0]
	ForEach1[T](stage, func(_ Entity, c *T) { s.col = append(s.col, *c) })
	n := len(s.col)
	if n == 0 {
		return
	}
	size := int(unsafe.Sizeof(s.col[0]))
	in := unsafe.Slice((*byte)(unsafe.Pointer(&s.col[0])), n*size)

	out, err := s.mod.Update(context.Background(), uint32(n), dt, in)
	if err != nil {
		stage.Engine().Log.Log("wasm:system", "update failed: %v", err)
		return
	}
	if !s.readWrite {
		return
	}
	res := unsafe.Slice((*T)(unsafe.Pointer(&out[0])), n)
	i := 0
	ForEach1[T](stage, func(_ Entity, c *T) { *c = res[i]; i++ })
}

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}

// Compile-time assertion that wasmSystem satisfies the engine System contract.
var _ engine.System = (*wasmSystem[struct{}])(nil)
```

> **Gather/scatter ordering invariant:** both `ForEach1` passes run inside the same `Update`, with no structural ECS mutation between them (Commands flush only at the `AfterSystem` boundary). ark archetype iteration order is therefore stable across the two passes, so `res[i]` scatters back to the same entity it was gathered from. This is the same stability the native systems already rely on.

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./pkg/mmokit/ -run TestWasmSystem_MatchesNativeLoop -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/wasm_system.go pkg/mmokit/wasm_system_test.go pkg/mmokit/wasm_testutil_test.go
git commit -m "feat(mmokit): generic POD WasmSystem adapter over wasmhost.Module"
```

---

## Task 6: The real ShieldRegen demo module

Port the actual `ShieldRegenSystem` logic to a `.wasm` module, with an internal counter to prove state continuity across swaps. Build it via a `just` recipe.

**Files:**
- Create: `examples/4node-basic/wasmmods/shieldregen/main.go`
- Create: `internal/component/shield_typeid.go`
- Modify: `justfile`

- [ ] **Step 1: Pin the Shield type id next to the component**

Create `internal/component/shield_typeid.go`:
```go
package component

// ShieldTypeID is the stable WASM-ABI id for the Shield column. Host and
// guest both reference this constant so they agree on which column maps.
// Phase 1 codegen will derive ids automatically.
const ShieldTypeID uint32 = 1
```

- [ ] **Step 2: Write the module**

Create `examples/4node-basic/wasmmods/shieldregen/main.go`:
```go
//go:build wasip1

package main

import (
	gamecomp "github.com/zenion/mmokit/internal/component"
	"github.com/zenion/mmokit/pkg/wasmsys"
)

// shieldRegen mirrors internal/game/system_shieldregen.go, but as a
// hot-swappable module. ticks is internal state that must survive a swap.
type shieldRegen struct{ ticks uint64 }

func (s *shieldRegen) Query() wasmsys.Query {
	return wasmsys.ReadWrite[gamecomp.Shield](gamecomp.ShieldTypeID)
}

func (s *shieldRegen) Update(ctx *wasmsys.Ctx, dt float32) {
	shields := wasmsys.Column[gamecomp.Shield](ctx)
	for i := range shields {
		sh := &shields[i]
		if sh.DamageCooldown > 0 {
			sh.DamageCooldown -= dt
			continue
		}
		if sh.Current < sh.Max {
			sh.Current = min(sh.Current+sh.RegenRate*dt, sh.Max)
		}
	}
	s.ticks++
}

func (s *shieldRegen) Snapshot() []byte {
	b := make([]byte, 8)
	for i := 0; i < 8; i++ {
		b[i] = byte(s.ticks >> (8 * i))
	}
	return b
}
func (s *shieldRegen) Restore(b []byte) {
	if len(b) == 8 {
		var v uint64
		for i := 0; i < 8; i++ {
			v |= uint64(b[i]) << (8 * i)
		}
		s.ticks = v
	}
}

func main() { wasmsys.Register(&shieldRegen{}) }
```

> **Constraint check:** `internal/component` (and anything it imports) is compiled into this `wasip1` module, so it must not transitively import ark or host-only packages. Verify with: `GOOS=wasip1 GOARCH=wasm go build ./examples/4node-basic/wasmmods/shieldregen/`. If it fails on an ark import, the offending field/type must move out of the shared component package (this is the POD-component boundary the spec calls out).

- [ ] **Step 3: Add the build recipe**

In `justfile`, add (place near the other build recipes; do not output to repo root):
```just
# build all hot-swappable wasm system modules into dist/wasmmods/
wasm-build:
    mkdir -p dist/wasmmods
    GOOS=wasip1 GOARCH=wasm go build -o dist/wasmmods/shieldregen.wasm ./examples/4node-basic/wasmmods/shieldregen/
```

- [ ] **Step 4: Build it**

Run: `just wasm-build`
Expected: `dist/wasmmods/shieldregen.wasm` exists, no error.

- [ ] **Step 5: Commit**

```bash
git add internal/component/shield_typeid.go examples/4node-basic/wasmmods/ justfile
git commit -m "feat(examples): ShieldRegen hot-swappable wasm module + build recipe"
```

---

## Task 7: Live add/remove on the game loop

Let the loop add or remove a system between ticks, on the loop goroutine, so a module can be loaded/unloaded into a running cell.

**Files:**
- Modify: `pkg/engine/loop.go`
- Create: `pkg/engine/loop_live_test.go`

- [ ] **Step 1: Write the failing test**

Create `pkg/engine/loop_live_test.go`:
```go
package engine

import "testing"

type countSys struct {
	SystemBase
	n *int
}

func (s *countSys) Update(dt float32) { *s.n++ }

func TestGameLoop_AddRemoveSystemLive(t *testing.T) {
	gl := &GameLoop{systems: nil, sysTimings: nil}
	n := 0
	gl.AddSystemLive("counter", &countSys{n: &n})
	if len(gl.systems) != 1 || len(gl.sysTimings) != 1 {
		t.Fatalf("after add: systems=%d timings=%d", len(gl.systems), len(gl.sysTimings))
	}
	if !gl.RemoveSystemLive("counter") {
		t.Fatal("RemoveSystemLive returned false")
	}
	if len(gl.systems) != 0 || len(gl.sysTimings) != 0 {
		t.Fatalf("after remove: systems=%d timings=%d", len(gl.systems), len(gl.sysTimings))
	}
	if gl.RemoveSystemLive("counter") {
		t.Fatal("RemoveSystemLive of absent system returned true")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./pkg/engine/ -run TestGameLoop_AddRemoveSystemLive -v`
Expected: FAIL — undefined `AddSystemLive`.

- [ ] **Step 3: Implement live add/remove**

In `pkg/engine/loop.go`, first add a parallel `systemNames []string` field to `GameLoop` (used to rebuild the profiler). Add it to the struct and populate it in `NewGameLoop` from the `names` argument (`gl.systemNames = names`). Then add:
```go
// AddSystemLive appends a system to the running loop. MUST be called on the
// loop goroutine (e.g. via engine.RunOnLoop). It resizes the timing buffer and
// rebuilds the profiler so the new system appears in perf output.
func (gl *GameLoop) AddSystemLive(name string, s System) {
	gl.systems = append(gl.systems, s)
	gl.systemNames = append(gl.systemNames, name)
	gl.sysTimings = make([]time.Duration, len(gl.systems))
	gl.engine.Perf = NewTickProfile(gl.systemNames)
}

// RemoveSystemLive removes the first system registered under name. Returns
// false if no such system exists. MUST be called on the loop goroutine.
func (gl *GameLoop) RemoveSystemLive(name string) bool {
	for i, n := range gl.systemNames {
		if n == name {
			gl.systems = append(gl.systems[:i], gl.systems[i+1:]...)
			gl.systemNames = append(gl.systemNames[:i], gl.systemNames[i+1:]...)
			gl.sysTimings = make([]time.Duration, len(gl.systems))
			gl.engine.Perf = NewTickProfile(gl.systemNames)
			return true
		}
	}
	return false
}
```

> Resizing `sysTimings` and rebuilding `Perf` keeps the `tick()` loop's `gl.sysTimings[i] = ...` indexing valid and the `eng.Perf.Record(gl.sysTimings, ...)` call consistent. Both mutate only loop-owned state, which is why these must run on the loop goroutine.

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./pkg/engine/ -run TestGameLoop_AddRemoveSystemLive -v`
Expected: PASS.

- [ ] **Step 5: Verify nothing else broke**

Run: `go test ./pkg/engine/ ./pkg/universe/`
Expected: PASS (the new `systemNames` field is populated wherever `NewGameLoop` is called).

- [ ] **Step 6: Commit**

```bash
git add pkg/engine/loop.go pkg/engine/loop_live_test.go
git commit -m "feat(engine): live add/remove of systems on the running loop"
```

---

## Task 8: End-to-end swap-with-state continuity test

The Phase 0 done-bar: load ShieldRegen into a running cell, unload it, swap it for a freshly-loaded instance, and prove (a) shields regen correctly while loaded and (b) the module's internal `ticks` counter survives the swap via snapshot/restore.

**Files:**
- Create: `pkg/mmokit/wasm_swap_test.go`

- [ ] **Step 1: Write the integration test**

Create `pkg/mmokit/wasm_swap_test.go`:
```go
package mmokit_test

import (
	"testing"

	gamecomp "github.com/zenion/mmokit/internal/component"
	"github.com/zenion/mmokit/pkg/mmokit"
)

func TestWasmSwap_PreservesStateAndBehavior(t *testing.T) {
	wasmPath := buildShieldModule(t) // builds examples/4node-basic/wasmmods/shieldregen → temp .wasm

	stage := mmokit.NewTestStage(t)
	mmokit.SpawnForTest(stage, gamecomp.Shield{Current: 50, Max: 100, RegenRate: 10})

	// Load v1, tick twice (shield regenerates by RegenRate*dt each tick).
	sys := mmokit.NewWasmSystemInstance[gamecomp.Shield](t, wasmPath) // test helper: build one instance + bind stage
	sys.Tick(0.5) // +5
	sys.Tick(0.5) // +5  -> Current == 60, internal ticks == 2

	got := mmokit.CollectForTest[gamecomp.Shield](stage)
	if got[0].Current != 60 {
		t.Fatalf("after 2 ticks Current=%v want 60", got[0].Current)
	}

	// Swap: snapshot v1, load v2, restore. Internal ticks must carry over.
	state := sys.Snapshot()
	sys2 := mmokit.NewWasmSystemInstance[gamecomp.Shield](t, wasmPath)
	sys2.Restore(state)
	sys2.BindStage(stage)

	sys2.Tick(0.5) // +5 -> Current == 65
	got = mmokit.CollectForTest[gamecomp.Shield](stage)
	if got[0].Current != 65 {
		t.Fatalf("after swap+tick Current=%v want 65", got[0].Current)
	}
	if ticks := mmokit.ReadTicks(sys2.Snapshot()); ticks != 3 {
		t.Fatalf("internal ticks after swap=%d want 3 (2 pre-swap + 1 post)", ticks)
	}
}
```

> **Test-helper note:** `NewWasmSystemInstance`, `.Tick`, `.Snapshot`, `.Restore`, `.BindStage`, and `ReadTicks` are thin test wrappers exposing the per-cell `wasmSystem[T]`'s module so a test can drive ticks and snapshot/restore directly without standing up a full coordinator. Add them to `pkg/mmokit/wasm_testutil_test.go`. They wrap the same `wasmhost.Module` calls the adapter uses; `ReadTicks` decodes the 8 LE bytes the demo module's `Snapshot` emits.

- [ ] **Step 2: Run it to verify it fails, then passes**

Run: `go test ./pkg/mmokit/ -run TestWasmSwap_PreservesStateAndBehavior -v`
Expected: first FAIL on the missing helpers, then PASS once they wrap the adapter. No production-code change should be needed — if the test reveals a real gap in `wasm_system.go`, fix it there and note it in the commit.

- [ ] **Step 3: Commit**

```bash
git add pkg/mmokit/wasm_swap_test.go pkg/mmokit/wasm_testutil_test.go
git commit -m "test(mmokit): e2e wasm load/unload/swap preserves state + behavior"
```

---

## Task 9: Native-vs-WASM benchmark

Quantify the boundary cost and confirm it is per-tick, not per-entity.

**Files:**
- Create: `pkg/mmokit/wasm_system_bench_test.go`

- [ ] **Step 1: Write the benchmark**

Create `pkg/mmokit/wasm_system_bench_test.go`:
```go
package mmokit_test

import (
	"fmt"
	"testing"

	gamecomp "github.com/zenion/mmokit/internal/component"
	"github.com/zenion/mmokit/pkg/mmokit"
)

func benchShield(b *testing.B, n int, wasm bool) {
	wasmPath := mmokit.BuildShieldModuleB(b)
	stage := mmokit.NewBenchStage(b)
	for i := 0; i < n; i++ {
		mmokit.SpawnForBench(stage, gamecomp.Shield{Current: 0, Max: 100, RegenRate: 10})
	}
	tick := mmokit.NativeShieldTick(stage) // closure running the native loop once
	if wasm {
		sys := mmokit.NewWasmSystemInstanceB[gamecomp.Shield](b, wasmPath)
		sys.BindStage(stage)
		tick = func() { sys.Tick(0.05) }
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tick()
	}
}

func BenchmarkShield(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("native/%d", n), func(b *testing.B) { benchShield(b, n, false) })
		b.Run(fmt.Sprintf("wasm/%d", n), func(b *testing.B) { benchShield(b, n, true) })
	}
}
```

> Reuse/extend the Task 5/8 test helpers for the `B`-suffixed variants (they take `*testing.B`). `NativeShieldTick` runs the exact loop from `internal/game/system_shieldregen.go` against the stage so the comparison is apples-to-apples.

- [ ] **Step 2: Run the benchmark and record results**

Run: `go test ./pkg/mmokit/ -run x -bench BenchmarkShield -benchmem`
Expected: completes; per-op time for `wasm/N` grows roughly linearly in N (the column copy) but the per-tick **fixed** overhead (visible as `wasm/100` minus `native/100`) stays small and constant.

- [ ] **Step 3: Append the measured numbers to the spec**

Add a short "Phase 0 benchmark results" subsection to the spec file with the actual ns/op table, so the perf claim is evidence-backed rather than asserted.

```bash
git add pkg/mmokit/wasm_system_bench_test.go docs/superpowers/specs/2026-06-04-hot-swappable-wasm-systems-design.md
git commit -m "test(mmokit): native-vs-wasm ShieldRegen benchmark + recorded results"
```

---

## Task 10: Live console demo command

Make the swap visible on a running server: a `wasm` console command group on the 4node-basic example that loads/unloads/swaps the ShieldRegen module on a chosen cell.

**Files:**
- Modify: `examples/4node-basic/main.go`
- Create: `examples/4node-basic/wasm_console.go`

- [ ] **Step 1: Wire the command group**

Create `examples/4node-basic/wasm_console.go` with a command group registered via the example's existing `OnConsoleReady` hook (mirror the existing `bot spawn/clear/list` group in this example). The handlers call, on the target cell's loop via `engine.RunOnLoop`:
- `wasm load <cellID>` → `cell.Loop.AddSystemLive("Wasm:shieldregen", instance)` where `instance` is a freshly `wasmhost.Load`-ed module wrapped by the adapter for that stage.
- `wasm unload <cellID>` → snapshot (discard for the demo) + `cell.Loop.RemoveSystemLive("Wasm:shieldregen")`.
- `wasm swap <cellID>` → snapshot current instance, `RemoveSystemLive`, load fresh, `Restore(state)`, `AddSystemLive`.

> Look at how the existing `bot` group in `examples/4node-basic/main.go` resolves a cellID argument and reaches `cell.Loop` / the stage; reuse that exact pattern (`rg -n "bot spawn\|OnConsoleReady\|cell.Loop" examples/4node-basic/`). Keep the three verbs minimal — this is a demo surface, not the Phase 3 cluster protocol.

- [ ] **Step 2: Build the server and the module**

Run:
```bash
just wasm-build
cd examples/4node-basic && go vet ./... && cd -
```
Expected: no errors.

- [ ] **Step 3: Manual smoke (record steps, do not leave a server running)**

Document in the commit body (not a separate file) the manual check: start the example, `wasm load 0_0`, observe shields regen, `wasm swap 0_0`, confirm no disconnect and shields keep regenerating. (Per project convention, smoke instructions go inline, never in a `*_SMOKE.md`.)

- [ ] **Step 4: Commit**

```bash
git add examples/4node-basic/
git commit -m "feat(examples): wasm load/unload/swap console command for live demo"
```

---

## Self-Review

**Spec coverage:**
- WASM-via-wazero, Go `wasip1` → Tasks 1, 3, 4. ✓
- Frozen ABI + version handshake → Task 2 (`wasmabi.ABIVersion`), Task 5 (handshake in `NewWasmSystem`). ✓
- Column bridge (twice-per-tick, not per-entity) → Task 4 (`Module.Update` arena bridge), Task 5 (gather/scatter), Task 9 (benchmark proves it). ✓
- Authoring model (`Query()`/`Column[T]`/`Register`) → Task 3. ✓
- POD-only constraint → Task 6 build-check note + `NewWasmSystem[T]` doc. ✓
- State snapshot/restore + true unload → Task 4 (`Snapshot`/`Restore`/`Close`), Task 8 (state survives swap). ✓
- Live add/remove/swap, single-process, on the loop goroutine → Task 7, Task 8, Task 10. ✓
- Phase 0 done-bar (load/unload/swap ShieldRegen single-process + perf benchmark) → Tasks 6–9. ✓
- Explicitly **out of Phase 0**: Commands host-imports, cross-entity lookup, multi-component queries, cluster atomicity. Not present in any task. ✓

**Placeholder scan:** No "TBD"/"handle errors appropriately". The two areas that defer to existing patterns (test-stage helpers in Tasks 5/8/9, console-arg parsing in Task 10) point at concrete existing code to mirror and name the exact functions to add, rather than hand-waving.

**Type consistency:** `wasmabi` export-name constants are referenced identically in `pkg/wasmsys/exports.go` and `pkg/wasmhost/module.go`. `Module.Update(ctx, count, dt, in)` signature matches its callers in Task 5 and the tests in Task 4. `ShieldTypeID` is defined once (Task 6) and referenced by the demo module's `Query()`. `wasmSystem[T]` satisfies `engine.System` (compile-time assertion in Task 5).

**Known soft spot:** Tasks 5/8/9 lean on test-only stage/spawn helpers whose exact names may already exist in `pkg/mmokit` test files under different names. The first step of Task 5 instructs the implementer to grep for existing equivalents before adding new ones — this avoids duplicating harness code and is the right place to reconcile names.
