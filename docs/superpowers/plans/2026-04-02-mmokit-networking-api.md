# MMOKit Networking & Movement API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Provide reflection-based auto-replication, generic movement systems, and refactor the 4node-basic example to use them — making mmokit accessible to external game developers.

**Architecture:** Struct tags (`net:"..."`) on ECS components drive auto-generated `EntityReplicator` implementations via init-time reflection and runtime closures. Two minimal movement systems (click-to-move, direction-vector) write `Velocity` and delegate integration to `PhysicsSystem`. The 4node-basic example is refactored from ~350 lines of custom networking/movement to ~50 lines using the new APIs.

**Tech Stack:** Go 1.23, Ark ECS v0.7.1, protobuf (`buf generate`), reflect package

---

## File Structure

### New Files
| File | Responsibility |
|------|---------------|
| `pkg/system/auto_replicator.go` | `AutoReplicator()`, `Component()`, `OptionalComponent()`, built-in bindings (`ViewerRelativePos`, `EntryPosition`, `QVelocity`, `QAngle`, `QSize`), `autoReplicator` struct implementing `EntityReplicator` |
| `pkg/system/field_meta.go` | Tag parser (`parseNetTag`), `fieldMeta` struct, encoding-to-wire-size table |
| `pkg/system/field_writers.go` | Per-encoding hash writer and snapshot writer closures |
| `pkg/system/auto_replicator_test.go` | Unit tests for auto-replicator |
| `pkg/system/click_to_move.go` | `ClickToMoveSystem`, `SetMoveTarget()`, `CancelMoveTarget()` |
| `pkg/system/direction_move.go` | `DirectionMoveSystem` |
| `pkg/system/click_to_move_test.go` | Unit tests for click-to-move |
| `pkg/system/direction_move_test.go` | Unit tests for direction-move |
| `examples/4node-basic/replication.go` | Auto-replicator registration for the basic example |

### Modified Files
| File | Changes |
|------|---------|
| `pkg/component/core.go` | Add `MoveParams`, `DirectionInput` components |
| `pkg/mmokit/mmokit.go` | Re-export all new public API |
| `examples/4node-basic/components.go` | Add `net:"initial,string"` tag to `PlayerName.Name` |
| `examples/4node-basic/main.go` | Replace system registration to use new systems |
| `examples/4node-basic/world.go` | Wire up ReplicationSystem |
| `examples/4node-basic/system_input.go` | Use `SetMoveTarget` helper |
| `examples/4node-basic/web/index.html` | Update client to parse standard `FrameEncoder` wire format |

### Deleted Files
| File | Reason |
|------|--------|
| `examples/4node-basic/system_network.go` | Replaced by ReplicationSystem + AutoReplicator |
| `examples/4node-basic/system_movement.go` | Replaced by ClickToMoveSystem |

---

## Task 1: Add New Components to `pkg/component/core.go`

**Files:**
- Modify: `pkg/component/core.go`

- [ ] **Step 1: Add `MoveParams` and `DirectionInput` components**

Add after the `MoveTarget` struct (around line 101):

```go
// MoveParams holds per-entity movement configuration.
// Optional — movement systems use their defaults if this component is absent.
type MoveParams struct {
	MaxSpeed float32 // units/sec; 0 means use system default
}

// DirectionInput holds WASD/joystick direction state.
// Set by the game's input handler each tick.
type DirectionInput struct {
	X, Y   float32 // direction vector (normalized by client)
	Active bool    // currently holding a direction key
}
```

- [ ] **Step 2: Verify compilation**

Run: `go vet ./pkg/component/...`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add pkg/component/core.go
git commit -m "feat(component): add MoveParams and DirectionInput components"
```

---

## Task 2: Field Metadata Parser (`pkg/system/field_meta.go`)

**Files:**
- Create: `pkg/system/field_meta.go`

- [ ] **Step 1: Write the failing test**

Create `pkg/system/auto_replicator_test.go`:

```go
package system

import (
	"testing"
)

// Test component with various net tags.
type testVitals struct {
	Health    float32 `net:"qnorm"`
	MaxHealth float32 `net:"u16"`
	Unused    int     // no tag — should be skipped
	Boosting  bool    `net:"bool"`
}

type testProfile struct {
	Name   string `net:"initial,string"`
	SkinID uint8  `net:"initial,u8"`
}

func TestParseNetTag(t *testing.T) {
	tests := []struct {
		tag      string
		enc      string
		opts     map[string]string
		initial  bool
		wantSize int
	}{
		{"qnorm", "qnorm", nil, false, 1},
		{"u16", "u16", nil, false, 2},
		{"bool", "bool", nil, false, 1},
		{"qvel,scale=2000", "qvel", map[string]string{"scale": "2000"}, false, 2},
		{"initial,string", "string", nil, true, -1}, // variable
		{"initial,u8", "u8", nil, true, 1},
		{"f32", "f32", nil, false, 4},
		{"u32", "u32", nil, false, 4},
		{"u8", "u8", nil, false, 1},
		{"i16", "i16", nil, false, 2},
		{"pos", "pos", nil, false, 8},
		{"qangle", "qangle", nil, false, 2},
		{"qsize,scale=500", "qsize", map[string]string{"scale": "500"}, false, 2},
	}

	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			fm, err := parseNetTag(tt.tag)
			if err != nil {
				t.Fatalf("parseNetTag(%q) error: %v", tt.tag, err)
			}
			if fm.encoding != tt.enc {
				t.Errorf("encoding = %q, want %q", fm.encoding, tt.enc)
			}
			if fm.initial != tt.initial {
				t.Errorf("initial = %v, want %v", fm.initial, tt.initial)
			}
			if fm.wireSize != tt.wantSize {
				t.Errorf("wireSize = %d, want %d", fm.wireSize, tt.wantSize)
			}
			for k, v := range tt.opts {
				if fm.options[k] != v {
					t.Errorf("option[%s] = %q, want %q", k, fm.options[k], v)
				}
			}
		})
	}
}

func TestParseNetTagInvalid(t *testing.T) {
	_, err := parseNetTag("unknownencoding")
	if err == nil {
		t.Fatal("expected error for unknown encoding")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/system/ -run TestParseNetTag -v`
Expected: FAIL — `parseNetTag` undefined

- [ ] **Step 3: Implement `field_meta.go`**

Create `pkg/system/field_meta.go`:

```go
package system

import (
	"fmt"
	"strings"
)

// fieldMeta holds parsed metadata from a single `net:"..."` struct tag.
type fieldMeta struct {
	encoding string            // e.g. "qnorm", "u16", "qvel", "string"
	options  map[string]string // e.g. {"scale": "2000"}
	initial  bool              // true if tagged `initial,...`
	wireSize int               // bytes on wire (-1 for variable like string)
}

// encodingWireSize maps encoding names to their fixed wire size.
// -1 means variable length.
var encodingWireSize = map[string]int{
	"pos":    8, // 2× float32
	"f32":    4,
	"qvel":   2, // int16
	"qangle": 2, // uint16
	"qnorm":  1, // uint8
	"qsize":  2, // uint16
	"u8":     1,
	"u16":    2,
	"u32":    4,
	"i16":    2,
	"bool":   1,
	"string": -1,
}

// parseNetTag parses a `net:"..."` tag value into fieldMeta.
// Format: "encoding[,option=value]..." or "initial,encoding[,option=value]..."
func parseNetTag(tag string) (fieldMeta, error) {
	parts := strings.Split(tag, ",")
	fm := fieldMeta{options: make(map[string]string)}

	idx := 0
	if len(parts) > 0 && parts[0] == "initial" {
		fm.initial = true
		idx = 1
	}

	if idx >= len(parts) {
		return fm, fmt.Errorf("net tag missing encoding: %q", tag)
	}

	enc := parts[idx]
	size, ok := encodingWireSize[enc]
	if !ok {
		return fm, fmt.Errorf("unknown net encoding %q in tag %q", enc, tag)
	}
	fm.encoding = enc
	fm.wireSize = size

	// Parse remaining key=value options.
	for i := idx + 1; i < len(parts); i++ {
		kv := strings.SplitN(parts[i], "=", 2)
		if len(kv) == 2 {
			fm.options[kv[0]] = kv[1]
		}
	}

	return fm, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/system/ -run TestParseNetTag -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/system/field_meta.go pkg/system/auto_replicator_test.go
git commit -m "feat(system): add net tag parser for auto-replicator"
```

---

## Task 3: Field Writers (`pkg/system/field_writers.go`)

**Files:**
- Create: `pkg/system/field_writers.go`
- Modify: `pkg/system/auto_replicator_test.go`

- [ ] **Step 1: Write the failing test**

Add to `pkg/system/auto_replicator_test.go`:

```go
func TestFieldHashWriter(t *testing.T) {
	hw, err := hashWriterFor(fieldMeta{encoding: "f32", wireSize: 4})
	if err != nil {
		t.Fatal(err)
	}
	h := &Hasher{}
	h.Reset()
	// Hash a float32 value of 1.5
	hw(1.5, h)
	got := h.Sum()

	h2 := &Hasher{}
	h2.Reset()
	h2.Float32(1.5)
	want := h2.Sum()

	if got != want {
		t.Errorf("hash mismatch: got %d, want %d", got, want)
	}
}

func TestFieldSnapshotWriter(t *testing.T) {
	sw, err := snapshotWriterFor(fieldMeta{encoding: "u16", wireSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	w := quantize.NewSnapshotWriter(buf)
	sw(uint16(42), w)

	// Verify: manually write the same value.
	buf2 := make([]byte, 64)
	w2 := quantize.NewSnapshotWriter(buf2)
	w2.Uint16(42)

	if w.Len() != w2.Len() {
		t.Errorf("length mismatch: got %d, want %d", w.Len(), w2.Len())
	}
	for i := 0; i < w.Len(); i++ {
		if w.Bytes()[i] != w2.Bytes()[i] {
			t.Errorf("byte %d mismatch: got %d, want %d", i, w.Bytes()[i], w2.Bytes()[i])
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/system/ -run TestField -v`
Expected: FAIL — `hashWriterFor`, `snapshotWriterFor` undefined

- [ ] **Step 3: Implement `field_writers.go`**

Create `pkg/system/field_writers.go`:

```go
package system

import (
	"fmt"
	"strconv"

	"github.com/zenion/mmoserver/pkg/quantize"
)

// hashWriterFunc writes a Go value into a Hasher for change detection.
// The value is passed as any — the writer handles type assertion.
type hashWriterFunc func(val any, h *Hasher)

// snapshotWriterFunc writes a Go value into a SnapshotWriter for serialization.
type snapshotWriterFunc func(val any, w *quantize.SnapshotWriter)

// initialWriterFunc writes a Go value into a byte slice for InitialData.
type initialWriterFunc func(val any, buf []byte) []byte

// hashWriterFor returns a hash writer closure for the given field metadata.
func hashWriterFor(fm fieldMeta) (hashWriterFunc, error) {
	switch fm.encoding {
	case "f32", "pos":
		return func(val any, h *Hasher) { h.Float32(val.(float32)) }, nil
	case "qvel", "qsize", "qangle", "u16":
		return func(val any, h *Hasher) { h.Float32(toFloat32(val)) }, nil
	case "qnorm", "u8":
		return func(val any, h *Hasher) { h.Uint8(toUint8(val)) }, nil
	case "u32":
		return func(val any, h *Hasher) { h.Uint32(toUint32(val)) }, nil
	case "i16":
		return func(val any, h *Hasher) { h.Int32(int32(toInt16(val))) }, nil
	case "bool":
		return func(val any, h *Hasher) { h.Bool(val.(bool)) }, nil
	case "string":
		return func(val any, h *Hasher) {
			s := val.(string)
			h.Uint32(uint32(len(s)))
			for i := 0; i < len(s); i++ {
				h.Uint8(s[i])
			}
		}, nil
	}
	return nil, fmt.Errorf("no hash writer for encoding %q", fm.encoding)
}

// snapshotWriterFor returns a snapshot writer closure for the given field metadata.
func snapshotWriterFor(fm fieldMeta) (snapshotWriterFunc, error) {
	switch fm.encoding {
	case "f32":
		return func(val any, w *quantize.SnapshotWriter) { w.Float32(toFloat32(val)) }, nil
	case "pos":
		// pos expects the struct with X, Y float32 — handled at a higher level
		return func(val any, w *quantize.SnapshotWriter) { w.Float32(toFloat32(val)) }, nil
	case "qvel":
		scale := float32(1000)
		if s, ok := fm.options["scale"]; ok {
			if v, err := strconv.ParseFloat(s, 32); err == nil {
				scale = float32(v)
			}
		}
		return func(val any, w *quantize.SnapshotWriter) { w.QVel(toFloat32(val), scale) }, nil
	case "qangle":
		return func(val any, w *quantize.SnapshotWriter) { w.QAngle(toFloat32(val)) }, nil
	case "qnorm":
		return func(val any, w *quantize.SnapshotWriter) { w.QNorm(toFloat32(val)) }, nil
	case "qsize":
		scale := float32(500)
		if s, ok := fm.options["scale"]; ok {
			if v, err := strconv.ParseFloat(s, 32); err == nil {
				scale = float32(v)
			}
		}
		return func(val any, w *quantize.SnapshotWriter) { w.QVel(toFloat32(val), scale) }, nil
	case "u8":
		return func(val any, w *quantize.SnapshotWriter) { w.Uint8(toUint8(val)) }, nil
	case "u16":
		return func(val any, w *quantize.SnapshotWriter) { w.Uint16(toUint16(val)) }, nil
	case "u32":
		return func(val any, w *quantize.SnapshotWriter) { w.Uint32(toUint32(val)) }, nil
	case "i16":
		return func(val any, w *quantize.SnapshotWriter) { w.Int16(toInt16(val)) }, nil
	case "bool":
		return func(val any, w *quantize.SnapshotWriter) { w.Bool(val.(bool)) }, nil
	}
	return nil, fmt.Errorf("no snapshot writer for encoding %q", fm.encoding)
}

// initialWriterFor returns a writer that appends InitialData bytes.
func initialWriterFor(fm fieldMeta) (initialWriterFunc, error) {
	switch fm.encoding {
	case "string":
		return func(val any, buf []byte) []byte {
			s := val.(string)
			b := []byte(s)
			if len(b) > 255 {
				b = b[:255]
			}
			buf = append(buf, uint8(len(b)))
			buf = append(buf, b...)
			return buf
		}, nil
	case "u8":
		return func(val any, buf []byte) []byte {
			return append(buf, toUint8(val))
		}, nil
	case "u16":
		return func(val any, buf []byte) []byte {
			v := toUint16(val)
			return append(buf, byte(v>>8), byte(v))
		}, nil
	case "u32":
		return func(val any, buf []byte) []byte {
			v := toUint32(val)
			return append(buf, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
		}, nil
	case "bool":
		return func(val any, buf []byte) []byte {
			if val.(bool) {
				return append(buf, 1)
			}
			return append(buf, 0)
		}, nil
	}
	return nil, fmt.Errorf("no initial writer for encoding %q", fm.encoding)
}

// Type conversion helpers — handle common Go numeric types.
func toFloat32(v any) float32 {
	switch x := v.(type) {
	case float32:
		return x
	case float64:
		return float32(x)
	case int:
		return float32(x)
	case uint16:
		return float32(x)
	case int16:
		return float32(x)
	default:
		return 0
	}
}

func toUint8(v any) uint8 {
	switch x := v.(type) {
	case uint8:
		return x
	case int:
		return uint8(x)
	case uint16:
		return uint8(x)
	case uint32:
		return uint8(x)
	case float32:
		return uint8(x)
	default:
		return 0
	}
}

func toUint16(v any) uint16 {
	switch x := v.(type) {
	case uint16:
		return x
	case int:
		return uint16(x)
	case uint32:
		return uint16(x)
	case float32:
		return uint16(x)
	default:
		return 0
	}
}

func toUint32(v any) uint32 {
	switch x := v.(type) {
	case uint32:
		return x
	case int:
		return uint32(x)
	case uint16:
		return uint32(x)
	case float32:
		return uint32(x)
	default:
		return 0
	}
}

func toInt16(v any) int16 {
	switch x := v.(type) {
	case int16:
		return x
	case int:
		return int16(x)
	case float32:
		return int16(x)
	default:
		return 0
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/system/ -run TestField -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/system/field_writers.go pkg/system/auto_replicator_test.go
git commit -m "feat(system): add field writer closures for auto-replicator"
```

---

## Task 4: Auto-Replicator Core (`pkg/system/auto_replicator.go`)

**Files:**
- Create: `pkg/system/auto_replicator.go`
- Modify: `pkg/system/auto_replicator_test.go`

- [ ] **Step 1: Write the failing test**

Add to `pkg/system/auto_replicator_test.go`:

```go
import (
	"reflect"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/spatial"
)

type testHealth struct {
	Current float32 `net:"qnorm"`
	Max     float32 `net:"u16"`
}

type testName struct {
	Name string `net:"initial,string"`
}

func TestAutoReplicatorLayout(t *testing.T) {
	w := ecs.NewWorld()
	posMap := ecs.NewMap1[component.Position](&w)
	velMap := ecs.NewMap1[component.Velocity](&w)
	healthMap := ecs.NewMap1[testHealth](&w)
	nameMap := ecs.NewMap1[testName](&w)

	rep := AutoReplicator(1,
		EntryPosition(),
		QVelocity(velMap, 2000),
		Component(healthMap),
		Component(nameMap), // initial-only — not in layout
	)

	// Layout should be: pos(4+4) + vel(2+2) + health.Current(1) + health.Max(2) = [4,4,2,2,1,2]
	layout := rep.SnapshotLayout()
	expected := []int{4, 4, 2, 2, 1, 2}
	if !reflect.DeepEqual(layout, expected) {
		t.Errorf("layout = %v, want %v", layout, expected)
	}

	_ = posMap // used by EntryPosition implicitly via spatial entry
}

func TestAutoReplicatorEntityType(t *testing.T) {
	rep := AutoReplicator(42)
	if rep.EntityType() != 42 {
		t.Errorf("EntityType() = %d, want 42", rep.EntityType())
	}
}

func TestAutoReplicatorSnapshot(t *testing.T) {
	w := ecs.NewWorld()
	healthMap := ecs.NewMap1[testHealth](&w)

	entity := w.NewEntity()
	healthMap.Add(entity, &testHealth{Current: 0.5, Max: 100})

	rep := AutoReplicator(1,
		Component(healthMap),
	)

	buf := make([]byte, 64)
	sw := quantize.NewSnapshotWriter(buf)
	viewer := &ViewerInfo{}
	entry := spatial.Entry{Entity: entity}

	rep.Snapshot(sw, viewer, entry)

	// qnorm(0.5) = uint8(128), u16(100) = uint16(100)
	buf2 := make([]byte, 64)
	sw2 := quantize.NewSnapshotWriter(buf2)
	sw2.QNorm(0.5)
	sw2.Uint16(100)

	if sw.Len() != sw2.Len() {
		t.Errorf("snapshot length = %d, want %d", sw.Len(), sw2.Len())
	}
	for i := 0; i < sw.Len(); i++ {
		if sw.Bytes()[i] != sw2.Bytes()[i] {
			t.Errorf("byte %d: got %d, want %d", i, sw.Bytes()[i], sw2.Bytes()[i])
		}
	}
}

func TestAutoReplicatorInitialData(t *testing.T) {
	w := ecs.NewWorld()
	nameMap := ecs.NewMap1[testName](&w)

	entity := w.NewEntity()
	nameMap.Add(entity, &testName{Name: "alice"})

	rep := AutoReplicator(1,
		Component(nameMap),
	)

	viewer := &ViewerInfo{}
	entry := spatial.Entry{Entity: entity}
	data := rep.InitialData(viewer, entry)

	// Expected: len(5) + "alice" = [5, 97, 108, 105, 99, 101]
	expected := []byte{5, 'a', 'l', 'i', 'c', 'e'}
	if !reflect.DeepEqual(data, expected) {
		t.Errorf("InitialData = %v, want %v", data, expected)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/system/ -run TestAutoReplicator -v`
Expected: FAIL — `AutoReplicator`, `EntryPosition`, `QVelocity`, `Component` undefined

- [ ] **Step 3: Implement `auto_replicator.go`**

Create `pkg/system/auto_replicator.go`:

```go
package system

import (
	"fmt"
	"reflect"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// ComponentBinding is an opaque unit that knows how to hash, snapshot, and
// provide initial data for one ECS component (or a built-in like position).
type ComponentBinding interface {
	// snapshotFields returns wire sizes of per-tick fields (not initial-only).
	snapshotFields() []int
	// hash writes diff-relevant fields into the hasher.
	hash(entity ecs.Entity, h *Hasher, viewer *ViewerInfo, entry spatial.Entry)
	// snapshot writes per-tick fields into the writer.
	snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, viewer *ViewerInfo, entry spatial.Entry)
	// hasInitial returns true if this binding contributes to InitialData.
	hasInitial() bool
	// initialData appends initial-only fields to buf and returns it.
	initialData(entity ecs.Entity, viewer *ViewerInfo, entry spatial.Entry, buf []byte) []byte
}

// AutoReplicator creates an EntityReplicator from composable bindings.
// Each binding contributes fields to the snapshot layout, hash, and/or initial data.
func AutoReplicator(entityType uint8, bindings ...ComponentBinding) EntityReplicator {
	var layout []int
	anyInitial := false
	for _, b := range bindings {
		layout = append(layout, b.snapshotFields()...)
		if b.hasInitial() {
			anyInitial = true
		}
	}
	return &autoReplicator{
		kind:       entityType,
		bindings:   bindings,
		layout:     layout,
		hasInit:    anyInitial,
	}
}

type autoReplicator struct {
	kind     uint8
	bindings []ComponentBinding
	layout   []int
	hasInit  bool
}

func (r *autoReplicator) EntityType() uint8 { return r.kind }

func (r *autoReplicator) Hash(h *Hasher, viewer *ViewerInfo, entry spatial.Entry) {
	for _, b := range r.bindings {
		b.hash(entry.Entity, h, viewer, entry)
	}
}

func (r *autoReplicator) Snapshot(w *quantize.SnapshotWriter, viewer *ViewerInfo, entry spatial.Entry) {
	for _, b := range r.bindings {
		b.snapshot(entry.Entity, w, viewer, entry)
	}
}

func (r *autoReplicator) SnapshotLayout() []int {
	return r.layout
}

func (r *autoReplicator) InitialData(viewer *ViewerInfo, entry spatial.Entry) []byte {
	if !r.hasInit {
		return nil
	}
	var buf []byte
	for _, b := range r.bindings {
		if b.hasInitial() {
			buf = b.initialData(entry.Entity, viewer, entry, buf)
		}
	}
	return buf
}

// ---------------------------------------------------------------------------
// Built-in bindings
// ---------------------------------------------------------------------------

// EntryPosition uses the spatial entry's X/Y as two float32 fields.
// Use this for single-node games or when cell-relative position is fine.
func EntryPosition() ComponentBinding {
	return &entryPosBinding{}
}

type entryPosBinding struct{}

func (b *entryPosBinding) snapshotFields() []int { return []int{4, 4} }
func (b *entryPosBinding) hasInitial() bool      { return false }
func (b *entryPosBinding) initialData(_ ecs.Entity, _ *ViewerInfo, _ spatial.Entry, buf []byte) []byte {
	return buf
}
func (b *entryPosBinding) hash(_ ecs.Entity, h *Hasher, _ *ViewerInfo, entry spatial.Entry) {
	h.Float32(entry.X)
	h.Float32(entry.Y)
}
func (b *entryPosBinding) snapshot(_ ecs.Entity, w *quantize.SnapshotWriter, _ *ViewerInfo, entry spatial.Entry) {
	w.Float32(entry.X)
	w.Float32(entry.Y)
}

// ViewerRelativePos computes world-absolute position relative to the viewer's cell.
// Emits two float32 fields: worldX, worldY.
func ViewerRelativePos(posMap *ecs.Map1[component.Position], cellMap *ecs.Map1[component.CellCoord]) ComponentBinding {
	return &viewerRelPosBinding{posMap: posMap, cellMap: cellMap}
}

type viewerRelPosBinding struct {
	posMap  *ecs.Map1[component.Position]
	cellMap *ecs.Map1[component.CellCoord]
}

func (b *viewerRelPosBinding) snapshotFields() []int { return []int{4, 4} }
func (b *viewerRelPosBinding) hasInitial() bool      { return false }
func (b *viewerRelPosBinding) initialData(_ ecs.Entity, _ *ViewerInfo, _ spatial.Entry, buf []byte) []byte {
	return buf
}
func (b *viewerRelPosBinding) hash(entity ecs.Entity, h *Hasher, _ *ViewerInfo, _ spatial.Entry) {
	if b.posMap.HasAll(entity) {
		pos := b.posMap.Get(entity)
		h.Float32(pos.X)
		h.Float32(pos.Y)
	}
	if b.cellMap.HasAll(entity) {
		cc := b.cellMap.Get(entity)
		h.Int32(cc.CellX)
		h.Int32(cc.CellY)
	}
}
func (b *viewerRelPosBinding) snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, viewer *ViewerInfo, _ spatial.Entry) {
	var x, y float32
	if b.posMap.HasAll(entity) {
		pos := b.posMap.Get(entity)
		x, y = pos.X, pos.Y
	}
	// viewer.X/Y are already world-absolute from PlayerViewerSource
	// Entity position is cell-local, so add cell offset
	if b.cellMap.HasAll(entity) {
		cc := b.cellMap.Get(entity)
		cs := coords.CellSize
		x += float32(cc.CellX) * cs
		y += float32(cc.CellY) * cs
	}
	w.Float32(x)
	w.Float32(y)
}

// Note: bindings that need cell size use coords.CellSize directly.
// It is a package-level var set during game initialization via coords.SetCellSize().

// QVelocity quantizes a Velocity component's X/Y as two int16 fields.
func QVelocity(velMap *ecs.Map1[component.Velocity], scale float32) ComponentBinding {
	return &qvelBinding{velMap: velMap, scale: scale}
}

type qvelBinding struct {
	velMap *ecs.Map1[component.Velocity]
	scale  float32
}

func (b *qvelBinding) snapshotFields() []int { return []int{2, 2} }
func (b *qvelBinding) hasInitial() bool      { return false }
func (b *qvelBinding) initialData(_ ecs.Entity, _ *ViewerInfo, _ spatial.Entry, buf []byte) []byte {
	return buf
}
func (b *qvelBinding) hash(entity ecs.Entity, h *Hasher, _ *ViewerInfo, _ spatial.Entry) {
	if b.velMap.HasAll(entity) {
		v := b.velMap.Get(entity)
		h.Float32(v.X)
		h.Float32(v.Y)
	}
}
func (b *qvelBinding) snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, _ *ViewerInfo, _ spatial.Entry) {
	if b.velMap.HasAll(entity) {
		v := b.velMap.Get(entity)
		w.QVel(v.X, b.scale)
		w.QVel(v.Y, b.scale)
	} else {
		w.Int16(0)
		w.Int16(0)
	}
}

// QAngle quantizes a Rotation component's angle as a uint16 field.
func QAngle(rotMap *ecs.Map1[component.Rotation]) ComponentBinding {
	return &qangleBinding{rotMap: rotMap}
}

type qangleBinding struct {
	rotMap *ecs.Map1[component.Rotation]
}

func (b *qangleBinding) snapshotFields() []int { return []int{2} }
func (b *qangleBinding) hasInitial() bool      { return false }
func (b *qangleBinding) initialData(_ ecs.Entity, _ *ViewerInfo, _ spatial.Entry, buf []byte) []byte {
	return buf
}
func (b *qangleBinding) hash(entity ecs.Entity, h *Hasher, _ *ViewerInfo, _ spatial.Entry) {
	if b.rotMap.HasAll(entity) {
		h.Float32(b.rotMap.Get(entity).Angle)
	}
}
func (b *qangleBinding) snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, _ *ViewerInfo, _ spatial.Entry) {
	if b.rotMap.HasAll(entity) {
		w.QAngle(b.rotMap.Get(entity).Angle)
	} else {
		w.Uint16(0)
	}
}

// QSize quantizes a Collider's Radius as a uint16 field.
func QSize(colliderMap *ecs.Map1[component.Collider], scale float32) ComponentBinding {
	return &qsizeBinding{colliderMap: colliderMap, scale: scale}
}

type qsizeBinding struct {
	colliderMap *ecs.Map1[component.Collider]
	scale       float32
}

func (b *qsizeBinding) snapshotFields() []int { return []int{2} }
func (b *qsizeBinding) hasInitial() bool      { return false }
func (b *qsizeBinding) initialData(_ ecs.Entity, _ *ViewerInfo, _ spatial.Entry, buf []byte) []byte {
	return buf
}
func (b *qsizeBinding) hash(entity ecs.Entity, h *Hasher, _ *ViewerInfo, _ spatial.Entry) {
	if b.colliderMap.HasAll(entity) {
		h.Float32(b.colliderMap.Get(entity).Radius)
	}
}
func (b *qsizeBinding) snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, _ *ViewerInfo, _ spatial.Entry) {
	if b.colliderMap.HasAll(entity) {
		w.QVel(b.colliderMap.Get(entity).Radius, b.scale)
	} else {
		w.Int16(0)
	}
}

// ---------------------------------------------------------------------------
// Reflection-based Component binding
// ---------------------------------------------------------------------------

// componentField holds precomputed accessors for one struct field.
type componentField struct {
	index    int // reflect field index
	meta     fieldMeta
	hashW    hashWriterFunc
	snapW    snapshotWriterFunc
	initialW initialWriterFunc // nil unless initial-only
}

// reflectBinding is a ComponentBinding built from struct tag reflection.
type reflectBinding[T any] struct {
	ecsMap        *ecs.Map1[T]
	snapshotCols  []componentField // per-tick fields
	initialCols   []componentField // initial-only fields
	wireLayout    []int
}

// Component creates a ComponentBinding by reflecting on T's `net:"..."` tags.
// Only fields with a `net` tag are included. Untagged fields are skipped.
func Component[T any](ecsMap *ecs.Map1[T]) ComponentBinding {
	return buildReflectBinding(ecsMap, false)
}

// OptionalComponent is like Component but the entity may not have this component.
// If absent, writes zero bytes for snapshot fields.
func OptionalComponent[T any](ecsMap *ecs.Map1[T]) ComponentBinding {
	return buildReflectBinding(ecsMap, true)
}

func buildReflectBinding[T any](ecsMap *ecs.Map1[T], optional bool) ComponentBinding {
	var zero T
	rt := reflect.TypeOf(zero)
	if rt.Kind() == reflect.Ptr {
		rt = rt.Elem()
	}

	var snapCols, initCols []componentField
	var wireLayout []int

	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		tag := field.Tag.Get("net")
		if tag == "" {
			continue
		}
		fm, err := parseNetTag(tag)
		if err != nil {
			panic(fmt.Sprintf("auto_replicator: field %s.%s: %v", rt.Name(), field.Name, err))
		}

		cf := componentField{index: i, meta: fm}

		if fm.initial {
			iw, err := initialWriterFor(fm)
			if err != nil {
				panic(fmt.Sprintf("auto_replicator: field %s.%s: %v", rt.Name(), field.Name, err))
			}
			cf.initialW = iw
			// Also need hash writer for initial fields (to detect changes for InitialData)
			hw, err := hashWriterFor(fm)
			if err != nil {
				panic(fmt.Sprintf("auto_replicator: field %s.%s: %v", rt.Name(), field.Name, err))
			}
			cf.hashW = hw
			initCols = append(initCols, cf)
		} else {
			hw, err := hashWriterFor(fm)
			if err != nil {
				panic(fmt.Sprintf("auto_replicator: field %s.%s: %v", rt.Name(), field.Name, err))
			}
			cf.hashW = hw
			sw, err := snapshotWriterFor(fm)
			if err != nil {
				panic(fmt.Sprintf("auto_replicator: field %s.%s: %v", rt.Name(), field.Name, err))
			}
			cf.snapW = sw
			snapCols = append(snapCols, cf)
			wireLayout = append(wireLayout, fm.wireSize)
		}
	}

	return &reflectBinding[T]{
		ecsMap:       ecsMap,
		snapshotCols: snapCols,
		initialCols:  initCols,
		wireLayout:   wireLayout,
	}
}

func (b *reflectBinding[T]) snapshotFields() []int {
	return b.wireLayout
}

func (b *reflectBinding[T]) hasInitial() bool {
	return len(b.initialCols) > 0
}

func (b *reflectBinding[T]) hash(entity ecs.Entity, h *Hasher, _ *ViewerInfo, _ spatial.Entry) {
	if !b.ecsMap.HasAll(entity) {
		return
	}
	comp := b.ecsMap.Get(entity)
	rv := reflect.ValueOf(comp).Elem()
	for _, cf := range b.snapshotCols {
		cf.hashW(rv.Field(cf.index).Interface(), h)
	}
	for _, cf := range b.initialCols {
		cf.hashW(rv.Field(cf.index).Interface(), h)
	}
}

func (b *reflectBinding[T]) snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, _ *ViewerInfo, _ spatial.Entry) {
	if !b.ecsMap.HasAll(entity) {
		// Write zeros to maintain fixed layout.
		for _, size := range b.wireLayout {
			for j := 0; j < size; j++ {
				w.Uint8(0)
			}
		}
		return
	}
	comp := b.ecsMap.Get(entity)
	rv := reflect.ValueOf(comp).Elem()
	for _, cf := range b.snapshotCols {
		cf.snapW(rv.Field(cf.index).Interface(), w)
	}
}

func (b *reflectBinding[T]) initialData(entity ecs.Entity, _ *ViewerInfo, _ spatial.Entry, buf []byte) []byte {
	if !b.ecsMap.HasAll(entity) {
		return buf
	}
	comp := b.ecsMap.Get(entity)
	rv := reflect.ValueOf(comp).Elem()
	for _, cf := range b.initialCols {
		buf = cf.initialW(rv.Field(cf.index).Interface(), buf)
	}
	return buf
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/system/ -run TestAutoReplicator -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/system/auto_replicator.go pkg/system/auto_replicator_test.go
git commit -m "feat(system): add reflection-based auto-replicator"
```

---

## Task 5: Re-export Auto-Replicator API via mmokit

**Files:**
- Modify: `pkg/mmokit/mmokit.go`

- [ ] **Step 1: Add imports and re-exports**

Add to the imports section of `pkg/mmokit/mmokit.go`:
```go
// (already imported: "github.com/zenion/mmoserver/pkg/system")
```

Add to the `var` block of re-exports (after `NewPlayerViewerSource`):

```go
	// AutoReplicator creates an EntityReplicator from composable component bindings.
	// Use struct tags (net:"...") on component fields to define replication encoding.
	AutoReplicator = system.AutoReplicator

	// EntryPosition uses the spatial entry's X/Y as two float32 fields.
	EntryPosition = system.EntryPosition

	// ViewerRelativePos computes world-absolute position relative to the viewer's cell.
	ViewerRelativePos = system.ViewerRelativePos

	// QVelocity quantizes a Velocity component's X/Y as two int16 fields.
	QVelocity = system.QVelocity

	// QAngle quantizes a Rotation component's angle as a uint16 field.
	QAngle = system.QAngle

	// QSize quantizes a Collider's Radius as a uint16 field.
	QSize = system.QSize
```

Add type alias:
```go
	// ComponentBinding is an opaque unit for auto-replicator composition.
	type ComponentBinding = system.ComponentBinding
```

Add generic function wrappers (these need to be standalone functions, not `var` assignments, because they're generic):

```go
// Component creates a ComponentBinding by reflecting on T's net:"..." struct tags.
func Component[T any](ecsMap *ecs.Map1[T]) ComponentBinding {
	return system.Component(ecsMap)
}

// OptionalComponent is like Component but writes zero bytes if the component is absent.
func OptionalComponent[T any](ecsMap *ecs.Map1[T]) ComponentBinding {
	return system.OptionalComponent(ecsMap)
}
```

- [ ] **Step 2: Verify compilation**

Run: `go vet ./pkg/mmokit/...`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add pkg/mmokit/mmokit.go
git commit -m "feat(mmokit): re-export auto-replicator API"
```

---

## Task 6: ClickToMoveSystem (`pkg/system/click_to_move.go`)

**Files:**
- Create: `pkg/system/click_to_move.go`
- Create: `pkg/system/click_to_move_test.go`

- [ ] **Step 1: Write the failing test**

Create `pkg/system/click_to_move_test.go`:

```go
package system

import (
	"math"
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
)

func TestClickToMoveBasic(t *testing.T) {
	w := ecs.NewWorld()
	sys := &ClickToMoveSystem{}
	sys.SetDeps(&w, nil, nil)
	sys.Init()

	// Create entity at (0,0) with move target at (100,0).
	mapper := ecs.NewMap5[component.Position, component.Velocity, component.MoveTarget, component.CellCoord, component.MoveParams](&w)
	entity := mapper.NewEntity(
		&component.Position{X: 0, Y: 0},
		&component.Velocity{},
		&component.MoveTarget{X: 100, Y: 0, Active: true},
		&component.CellCoord{},
		&component.MoveParams{MaxSpeed: 300},
	)

	sys.Update(0.05) // 50ms tick

	velMap := ecs.NewMap1[component.Velocity](&w)
	vel := velMap.Get(entity)

	// Velocity should point toward (100, 0) at speed 300.
	if vel.X <= 0 {
		t.Errorf("expected positive X velocity, got %f", vel.X)
	}
	if math.Abs(float64(vel.Y)) > 0.001 {
		t.Errorf("expected zero Y velocity, got %f", vel.Y)
	}
	if math.Abs(float64(vel.X)-300) > 1 {
		t.Errorf("expected X velocity ~300, got %f", vel.X)
	}
}

func TestClickToMoveArrival(t *testing.T) {
	w := ecs.NewWorld()
	sys := &ClickToMoveSystem{}
	sys.SetDeps(&w, nil, nil)
	sys.Init()

	// Entity already at the target.
	mapper := ecs.NewMap4[component.Position, component.Velocity, component.MoveTarget, component.CellCoord](&w)
	entity := mapper.NewEntity(
		&component.Position{X: 100, Y: 100},
		&component.Velocity{X: 50, Y: 50},
		&component.MoveTarget{X: 100.5, Y: 100, Active: true},
		&component.CellCoord{},
	)

	sys.Update(0.05)

	velMap := ecs.NewMap1[component.Velocity](&w)
	vel := velMap.Get(entity)
	mtMap := ecs.NewMap1[component.MoveTarget](&w)
	mt := mtMap.Get(entity)

	if mt.Active {
		t.Error("expected MoveTarget.Active = false after arrival")
	}
	if vel.X != 0 || vel.Y != 0 {
		t.Errorf("expected zero velocity after arrival, got (%f, %f)", vel.X, vel.Y)
	}
}

func TestClickToMoveInactive(t *testing.T) {
	w := ecs.NewWorld()
	sys := &ClickToMoveSystem{}
	sys.SetDeps(&w, nil, nil)
	sys.Init()

	// Entity with inactive move target — velocity should not change.
	mapper := ecs.NewMap4[component.Position, component.Velocity, component.MoveTarget, component.CellCoord](&w)
	entity := mapper.NewEntity(
		&component.Position{X: 0, Y: 0},
		&component.Velocity{X: 99, Y: 99},
		&component.MoveTarget{Active: false},
		&component.CellCoord{},
	)

	sys.Update(0.05)

	velMap := ecs.NewMap1[component.Velocity](&w)
	vel := velMap.Get(entity)

	// Velocity should be unchanged — system only acts when Active.
	if vel.X != 99 || vel.Y != 99 {
		t.Errorf("expected velocity unchanged, got (%f, %f)", vel.X, vel.Y)
	}
}

func TestSetMoveTarget(t *testing.T) {
	// Test coordinate conversion with known cell size.
	origSize := component.CellSizeValue
	component.CellSizeValue = 2000
	defer func() { component.CellSizeValue = origSize }()

	mt := &component.MoveTarget{}
	SetMoveTarget(mt, 3500, -500)

	if mt.CellX != 1 {
		t.Errorf("CellX = %d, want 1", mt.CellX)
	}
	if mt.CellY != -1 {
		t.Errorf("CellY = %d, want -1", mt.CellY)
	}
	if !mt.Active {
		t.Error("expected Active = true")
	}
	// Local X should be 3500 - 1*2000 = 1500.
	if math.Abs(float64(mt.X)-1500) > 0.01 {
		t.Errorf("local X = %f, want 1500", mt.X)
	}
	// Local Y should be -500 - (-1)*2000 = 1500.
	if math.Abs(float64(mt.Y)-1500) > 0.01 {
		t.Errorf("local Y = %f, want 1500", mt.Y)
	}
}
```

**Note:** The `SetMoveTarget` test depends on accessing cell size. We need to check how `mmokit.CellSize()` works — it's likely `coords.CellSize()` from `pkg/coords/`. The test may need adjustment based on the actual API.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/system/ -run TestClickToMove -v`
Expected: FAIL — `ClickToMoveSystem`, `SetMoveTarget` undefined

- [ ] **Step 3: Implement `click_to_move.go`**

Create `pkg/system/click_to_move.go`:

```go
package system

import (
	"math"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
)

const defaultMaxSpeed float32 = 300

// ClickToMoveSystem moves entities toward their MoveTarget at MoveParams.MaxSpeed.
// Stops when within ~1 unit of the target. Does nothing when MoveTarget.Active is false.
// Skips Ghost and Replica entities.
type ClickToMoveSystem struct {
	engine.SystemBase
	filter    *ecs.Filter4[component.Position, component.Velocity, component.MoveTarget, component.CellCoord]
	paramsMap *ecs.Map1[component.MoveParams]
}

func (s *ClickToMoveSystem) Init() {
	w := s.ECSWorld()
	s.filter = ecs.NewFilter4[component.Position, component.Velocity, component.MoveTarget, component.CellCoord](w).
		Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
	s.paramsMap = ecs.NewMap1[component.MoveParams](w)
}

func (s *ClickToMoveSystem) Update(dt float32) {
	cellSize := coords.CellSize()
	query := s.filter.Query()
	for query.Next() {
		pos, vel, mt, cc := query.Get()

		if !mt.Active {
			continue
		}

		// Compute displacement to target, accounting for cross-cell offset.
		dx := float32(mt.CellX-cc.CellX)*cellSize + mt.X - pos.X
		dy := float32(mt.CellY-cc.CellY)*cellSize + mt.Y - pos.Y
		dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))

		if dist < 1.0 {
			mt.Active = false
			vel.X = 0
			vel.Y = 0
			continue
		}

		speed := defaultMaxSpeed
		entity := query.Entity()
		if s.paramsMap.HasAll(entity) {
			if p := s.paramsMap.Get(entity); p.MaxSpeed > 0 {
				speed = p.MaxSpeed
			}
		}

		vel.X = (dx / dist) * speed
		vel.Y = (dy / dist) * speed
	}
}

// SetMoveTarget converts world-absolute coordinates to cell-local and activates the target.
// Uses math.Floor for correct negative coordinate handling.
func SetMoveTarget(mt *component.MoveTarget, worldX, worldY float32) {
	cellSize := coords.CellSize()
	mt.CellX = int32(math.Floor(float64(worldX / cellSize)))
	mt.CellY = int32(math.Floor(float64(worldY / cellSize)))
	mt.X = worldX - float32(mt.CellX)*cellSize
	mt.Y = worldY - float32(mt.CellY)*cellSize
	mt.Active = true
}

// CancelMoveTarget deactivates movement.
func CancelMoveTarget(mt *component.MoveTarget) {
	mt.Active = false
}
```

- [ ] **Step 4: Fix the test if needed**

The `SetMoveTarget` test needs to use `coords.SetCellSize()` instead of a direct variable:

```go
func TestSetMoveTarget(t *testing.T) {
	coords.SetCellSize(2000)
	defer coords.SetCellSize(0) // reset

	mt := &component.MoveTarget{}
	SetMoveTarget(mt, 3500, -500)

	if mt.CellX != 1 {
		t.Errorf("CellX = %d, want 1", mt.CellX)
	}
	if mt.CellY != -1 {
		t.Errorf("CellY = %d, want -1", mt.CellY)
	}
	if !mt.Active {
		t.Error("expected Active = true")
	}
	if math.Abs(float64(mt.X)-1500) > 0.01 {
		t.Errorf("local X = %f, want 1500", mt.X)
	}
	if math.Abs(float64(mt.Y)-1500) > 0.01 {
		t.Errorf("local Y = %f, want 1500", mt.Y)
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./pkg/system/ -run "TestClickToMove|TestSetMoveTarget" -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/system/click_to_move.go pkg/system/click_to_move_test.go
git commit -m "feat(system): add generic ClickToMoveSystem"
```

---

## Task 7: DirectionMoveSystem (`pkg/system/direction_move.go`)

**Files:**
- Create: `pkg/system/direction_move.go`
- Create: `pkg/system/direction_move_test.go`

- [ ] **Step 1: Write the failing test**

Create `pkg/system/direction_move_test.go`:

```go
package system

import (
	"math"
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
)

func TestDirectionMoveActive(t *testing.T) {
	w := ecs.NewWorld()
	sys := &DirectionMoveSystem{}
	sys.SetDeps(&w, nil, nil)
	sys.Init()

	mapper := ecs.NewMap4[component.Position, component.Velocity, component.DirectionInput, component.MoveParams](&w)
	entity := mapper.NewEntity(
		&component.Position{},
		&component.Velocity{},
		&component.DirectionInput{X: 1, Y: 0, Active: true},
		&component.MoveParams{MaxSpeed: 200},
	)

	sys.Update(0.05)

	velMap := ecs.NewMap1[component.Velocity](&w)
	vel := velMap.Get(entity)

	if math.Abs(float64(vel.X)-200) > 1 {
		t.Errorf("expected X velocity ~200, got %f", vel.X)
	}
	if math.Abs(float64(vel.Y)) > 0.001 {
		t.Errorf("expected zero Y velocity, got %f", vel.Y)
	}
}

func TestDirectionMoveInactive(t *testing.T) {
	w := ecs.NewWorld()
	sys := &DirectionMoveSystem{}
	sys.SetDeps(&w, nil, nil)
	sys.Init()

	mapper := ecs.NewMap3[component.Position, component.Velocity, component.DirectionInput](&w)
	entity := mapper.NewEntity(
		&component.Position{},
		&component.Velocity{X: 100, Y: 100},
		&component.DirectionInput{Active: false},
	)

	sys.Update(0.05)

	velMap := ecs.NewMap1[component.Velocity](&w)
	vel := velMap.Get(entity)

	if vel.X != 0 || vel.Y != 0 {
		t.Errorf("expected zero velocity when inactive, got (%f, %f)", vel.X, vel.Y)
	}
}

func TestDirectionMoveNormalization(t *testing.T) {
	w := ecs.NewWorld()
	sys := &DirectionMoveSystem{}
	sys.SetDeps(&w, nil, nil)
	sys.Init()

	// Diagonal direction (1,1) should be normalized.
	mapper := ecs.NewMap4[component.Position, component.Velocity, component.DirectionInput, component.MoveParams](&w)
	entity := mapper.NewEntity(
		&component.Position{},
		&component.Velocity{},
		&component.DirectionInput{X: 1, Y: 1, Active: true},
		&component.MoveParams{MaxSpeed: 100},
	)

	sys.Update(0.05)

	velMap := ecs.NewMap1[component.Velocity](&w)
	vel := velMap.Get(entity)

	speed := float64(vel.X*vel.X + vel.Y*vel.Y)
	speed = math.Sqrt(speed)
	if math.Abs(speed-100) > 1 {
		t.Errorf("expected speed ~100, got %f", speed)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/system/ -run TestDirectionMove -v`
Expected: FAIL — `DirectionMoveSystem` undefined

- [ ] **Step 3: Implement `direction_move.go`**

Create `pkg/system/direction_move.go`:

```go
package system

import (
	"math"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/engine"
)

// DirectionMoveSystem moves entities in the direction of their DirectionInput
// at MoveParams.MaxSpeed. Sets velocity to zero when input is inactive.
// Skips Ghost and Replica entities.
type DirectionMoveSystem struct {
	engine.SystemBase
	filter    *ecs.Filter3[component.Position, component.Velocity, component.DirectionInput]
	paramsMap *ecs.Map1[component.MoveParams]
}

func (s *DirectionMoveSystem) Init() {
	w := s.ECSWorld()
	s.filter = ecs.NewFilter3[component.Position, component.Velocity, component.DirectionInput](w).
		Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
	s.paramsMap = ecs.NewMap1[component.MoveParams](w)
}

func (s *DirectionMoveSystem) Update(dt float32) {
	query := s.filter.Query()
	for query.Next() {
		_, vel, di := query.Get()

		if !di.Active {
			vel.X = 0
			vel.Y = 0
			continue
		}

		speed := defaultMaxSpeed
		entity := query.Entity()
		if s.paramsMap.HasAll(entity) {
			if p := s.paramsMap.Get(entity); p.MaxSpeed > 0 {
				speed = p.MaxSpeed
			}
		}

		// Normalize direction.
		mag := float32(math.Sqrt(float64(di.X*di.X + di.Y*di.Y)))
		if mag < 0.001 {
			vel.X = 0
			vel.Y = 0
			continue
		}

		vel.X = (di.X / mag) * speed
		vel.Y = (di.Y / mag) * speed
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/system/ -run TestDirectionMove -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/system/direction_move.go pkg/system/direction_move_test.go
git commit -m "feat(system): add generic DirectionMoveSystem"
```

---

## Task 8: Re-export Movement API via mmokit

**Files:**
- Modify: `pkg/mmokit/mmokit.go`
- Modify: `pkg/component/core.go` (if `MoveParams`/`DirectionInput` need type alias)

- [ ] **Step 1: Add re-exports to mmokit**

Add type aliases in the type section:
```go
	type MoveParams = component.MoveParams
	type DirectionInput = component.DirectionInput
```

Add to the var block:
```go
	// SetMoveTarget converts world-absolute coordinates to cell-local and activates.
	SetMoveTarget = system.SetMoveTarget

	// CancelMoveTarget deactivates movement.
	CancelMoveTarget = system.CancelMoveTarget
```

Add system constructor helpers (standalone functions):
```go
// NewClickToMoveSystem creates a factory function for ClickToMoveSystem.
func NewClickToMoveSystem() func() System {
	return func() System { return &system.ClickToMoveSystem{} }
}

// NewDirectionMoveSystem creates a factory function for DirectionMoveSystem.
func NewDirectionMoveSystem() func() System {
	return func() System { return &system.DirectionMoveSystem{} }
}
```

- [ ] **Step 2: Verify compilation**

Run: `go vet ./pkg/mmokit/...`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add pkg/mmokit/mmokit.go
git commit -m "feat(mmokit): re-export movement systems and helpers"
```

---

## Task 9: Refactor 4node-basic — Tag Component, Create Replicator

**Files:**
- Modify: `examples/4node-basic/components.go`
- Create: `examples/4node-basic/replication.go`

- [ ] **Step 1: Add `net` tag to `PlayerName`**

Edit `examples/4node-basic/components.go`:

```go
package main

// PlayerName stores a player's display name (replicated to other nodes).
type PlayerName struct {
	Name string `net:"initial,string"`
}
```

- [ ] **Step 2: Create `replication.go` with AutoReplicator setup**

Create `examples/4node-basic/replication.go`:

```go
package main

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

func setupReplication(gw *BasicWorld) *mmokit.ReplicatorRegistry {
	w := gw.ECSWorld()
	posMap := ecs.NewMap1[mmokit.Position](w)
	cellMap := ecs.NewMap1[mmokit.CellCoord](w)
	velMap := ecs.NewMap1[mmokit.Velocity](w)
	colliderMap := ecs.NewMap1[mmokit.Collider](w)

	replicators := mmokit.NewReplicatorRegistry()
	replicators.Register(mmokit.AutoReplicator(KindPlayer,
		mmokit.ViewerRelativePos(posMap, cellMap),
		mmokit.QVelocity(velMap, 2000),
		mmokit.QSize(colliderMap, 500),
		mmokit.Component(gw.NameMap),
	))
	return replicators
}
```

- [ ] **Step 3: Verify compilation**

Run: `go vet ./examples/4node-basic/...`
Expected: no errors (some unused warnings may appear until we wire it up)

- [ ] **Step 4: Commit**

```bash
git add examples/4node-basic/components.go examples/4node-basic/replication.go
git commit -m "feat(4node-basic): add AutoReplicator setup for player entities"
```

---

## Task 10: Refactor 4node-basic — Replace NetworkSystem and MovementSystem

**Files:**
- Delete: `examples/4node-basic/system_network.go`
- Delete: `examples/4node-basic/system_movement.go`
- Modify: `examples/4node-basic/main.go`
- Modify: `examples/4node-basic/world.go`
- Modify: `examples/4node-basic/system_input.go`

- [ ] **Step 1: Create new NetworkSystem using ReplicationSystem**

Create `examples/4node-basic/system_network.go` (replacing the old one):

```go
package main

import (
	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/system"
)

type NetworkSystem struct {
	mmokit.SystemBase
	gw      *BasicWorld
	replSys *mmokit.ReplicationSystem
}

func (s *NetworkSystem) Init() {
	s.gw = s.GameWorld().(*BasicWorld)
	gw := s.gw

	replicators := setupReplication(gw)

	s.replSys = mmokit.NewReplicationSystem(system.ReplicationConfig{
		World:       gw.ECSWorld(),
		Grid:        gw.Spatial,
		Viewers:     system.NewPlayerViewerSource(gw.ECSWorld(), gw.Engine().Players, mmokit.StateActive),
		Frame:       system.NewBinaryFrameWriter(gw.Engine().ConnMgr, uint32(enginepb.ServerEventCode_SE_DELTA_WORLD_UPDATE), mmokit.MakeEventRaw),
		Replicators: replicators,
		AoIRadius:   AoIRadius,
		GetTick:     func() uint32 { return gw.Engine().Tick },
	})
}

func (s *NetworkSystem) Update(dt float32) {
	s.replSys.Update(dt)
}
```

- [ ] **Step 2: Update `main.go` to use ClickToMoveSystem instead of MovementSystem**

Replace the system registration in `main.go`:

```go
	// Register systems in order of execution.
	coord.AddSystem("Input", mmokit.NewInputSystem(setupInputHandlers))
	coord.AddSystem("ClickToMove", func() mmokit.System { return &mmokit.ClickToMoveSystem{} })
	coord.AddSystem("Physics", func() mmokit.System { return &mmokit.PhysicsSystem{} })
	coord.AddSystem("DeadReckoning", func() mmokit.System { return &mmokit.ReplicaDeadReckoningSystem{} })
	coord.AddSystem("Spatial", func() mmokit.System { return &SpatialSystem{} })
	coord.AddSystem("Network", func() mmokit.System { return &NetworkSystem{} })
```

Note: The `ClickToMoveSystem` reference needs to work. Since `mmokit` re-exports it, this should be `system.ClickToMoveSystem` or the mmokit-provided factory. Check the exact pattern — may need to use the package-level type directly.

- [ ] **Step 3: Update `system_input.go` to use `SetMoveTarget`**

Replace `examples/4node-basic/system_input.go`:

```go
package main

import (
	basicpb "github.com/zenion/mmoserver/gen/go/basicpb"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

func setupInputHandlers(router *mmokit.InputRouter, gw *BasicWorld) {
	mmokit.Handle(router,
		basicpb.BasicClientEventCode_BCE_MOVE_TARGET,
		mmokit.States(mmokit.StateActive),
		func(ctx *mmokit.InputContext, msg *basicpb.BasicMoveTargetMsg) {
			if !gw.MoveTargetMap.HasAll(ctx.Entity) {
				return
			}
			mt := gw.MoveTargetMap.Get(ctx.Entity)
			mmokit.SetMoveTarget(mt, msg.TargetX, msg.TargetY)
		})
}
```

- [ ] **Step 4: Delete old `system_movement.go`**

```bash
rm examples/4node-basic/system_movement.go
```

- [ ] **Step 5: Remove unused imports/config from `config.go`**

Update `examples/4node-basic/config.go` — remove constants no longer needed:

```go
package main

const (
	TickRate   int     = 20
	MeshCellsX uint32  = 2
	MeshCellsY uint32  = 2
	CellSize   float32 = 2000.0
	AoIRadius  float32 = 800.0

	// Entity types
	KindPlayer uint8 = 1
)
```

Remove: `PlayerMoveSpeed`, `PlayerRadius`, `Friction`, `ArrivalDist`, `DecelDist`, `MinMoveSpeed` — these are now handled by `MoveParams` component or system defaults. Keep `PlayerRadius` if it's used in spawn (check `world.go`).

Actually, `PlayerRadius` is used in `world.go:123` for `WithCollider(PlayerRadius)` and spatial registration. Keep it.

```go
package main

const (
	TickRate        int     = 20
	MeshCellsX      uint32  = 2
	MeshCellsY      uint32  = 2
	CellSize        float32 = 2000.0
	AoIRadius       float32 = 800.0
	PlayerRadius    float32 = 20.0

	// Entity types
	KindPlayer uint8 = 1
)
```

- [ ] **Step 6: Verify compilation**

Run: `go vet ./examples/4node-basic/...`
Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add -A examples/4node-basic/
git commit -m "refactor(4node-basic): use ReplicationSystem and ClickToMoveSystem"
```

---

## Task 11: Update 4node-basic Web Client

**Files:**
- Modify: `examples/4node-basic/web/index.html`

- [ ] **Step 1: Read the current client code and the slither client for the standard frame format**

Read `examples/4node-basic/web/index.html` to understand the current parsing.
Read `examples/slither/web/` (if it exists) for the standard FrameEncoder format parsing.

The standard `FrameEncoder` wire format (from `pkg/quantize/wireformat.go`) is:

```
Header (24 bytes, big-endian):
  [4] tick        uint32
  [4] seq         uint32
  [4] viewerX     float32
  [4] viewerY     float32
  [2] fullCount   uint16
  [2] deltaCount  uint16
  [2] removedCount uint16
  [2] exitedCount  uint16

Full Entities (fullCount):
  [4] netID       uint32
  [1] entityType  uint8
  [2] snapshotLen uint16
  [N] snapshot    bytes (layout: viewerRelX(4) + viewerRelY(4) + qvelX(2) + qvelY(2) + qRadius(2) = 14 bytes)
  [2] initialLen  uint16
  [M] initialData bytes (nameLen(1) + name)

Delta Entities (deltaCount):
  [4] netID       uint32
  [1] entityType  uint8
  [2] deltaLen    uint16
  [D] deltaData   bytes (bitmask + changed fields)

Removed IDs: [4]*removedCount uint32
Exited IDs:  [4]*exitedCount uint32
```

- [ ] **Step 2: Update the JavaScript client frame parser**

Replace the custom binary parser in the web client with one that parses the standard FrameEncoder format. The client needs to:

1. Parse 24-byte header (tick, seq, viewerX, viewerY, counts)
2. For full entities: read netID, type, snapshotLen, snapshot, initialLen, initialData
3. Parse snapshot fields: worldX(f32), worldY(f32), velX(i16/2000), velY(i16/2000), radius(i16/500)
4. Parse initial data: nameLen(u8), name(utf8)
5. For delta entities: read netID, type, deltaLen, apply delta against cached baseline
6. For removed/exited: read netID lists

The delta decoding in JS needs to match the layout `[4, 4, 2, 2, 2]` (5 fields) and apply the bitmask to determine which fields changed.

**This step requires reading and modifying the full web client.** The exact code will depend on the current client structure. The key changes:
- Replace the 20-byte header parser with 24-byte header
- Replace the per-entity 28+nameLen parser with the full/delta/removed/exited format
- Add delta decoding (maintain per-entity baseline snapshots, apply bitmask diffs)
- Handle initial data separately from per-tick snapshots

- [ ] **Step 3: Test manually**

Run: `cd examples/4node-basic && go run . -port 8081`
Open: `http://localhost:8081`
Expected: Players appear, click-to-move works, names display

- [ ] **Step 4: Commit**

```bash
git add examples/4node-basic/web/
git commit -m "refactor(4node-basic): update web client for standard wire format"
```

---

## Task 12: Run Full Test Suite and Verify

**Files:** None (verification only)

- [ ] **Step 1: Run all package tests**

Run: `go test ./pkg/... -v`
Expected: All PASS

- [ ] **Step 2: Run all example tests (if any)**

Run: `go vet ./examples/...`
Expected: No errors

- [ ] **Step 3: Build everything**

Run: `make build`
Expected: Success

- [ ] **Step 4: Manual integration test**

Run: `cd examples/4node-basic && go run . -port 8081`
Open: `http://localhost:8081` in two browser tabs
Verify:
- Both players see each other
- Click-to-move works
- Player names display
- Players crossing cell boundaries transfer correctly
- Players leaving AoI disappear

- [ ] **Step 5: Final commit if any fixups needed**

```bash
git add -A
git commit -m "fix: integration fixups for mmokit networking API"
```
