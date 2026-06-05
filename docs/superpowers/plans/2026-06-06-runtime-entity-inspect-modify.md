# Runtime Entity Inspect & Modify — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `entity.inspect` and `entity.modify` cmdsys verbs (console + admin UI) that generically list and mutate any live entity's component fields by network ID.

**Architecture:** Extend the existing kind-registration walk to expose a per-component runtime accessor (`{Name, reflect.Type, Get(entity)→*T}`). A pure reflection helper (`ListFields`/`SetFieldByPath`) reads/writes scalar leaves by dotted path. Two new `RouteEntityOwner` verbs in `pkg/universe/builtins_entity.go` wire these together; the admin SPA adds an Entities page + inspect/edit drawer over the same verbs.

**Tech Stack:** Go (Ark v0.7.1 ECS, cmdsys), Svelte 5 + Vite + Bun (web-admin).

**Spec:** [docs/superpowers/specs/2026-06-06-runtime-entity-inspect-modify-design.md](../specs/2026-06-06-runtime-entity-inspect-modify-design.md)

**Conventions to honor (from CLAUDE.md + memory):**
- Verify Go compiles with `go vet ./...` — NEVER `go build ./...` (drops binaries in package dirs).
- Required command args are positional; only optional ones become flags.
- Console list/table results use `cmd:"table"` + flat rows.
- Admin SPA: no `window.confirm/alert/prompt` — use `ConfirmDialog.svelte`. Use `bun`, not npm.
- Game code imports `mmokit` only; but `pkg/universe` is engine code and imports `ark/ecs` freely.

---

## File Structure

**Backend (Go):**
- Modify `pkg/universe/entity_kind.go` — add `ComponentAccessor` type, extend `kindComponent` + `KindComponentByID`, add `EntityKindDef.ComponentAccessors()`.
- Create `pkg/universe/fieldpath.go` — pure reflection: `FieldInfo`, `ListFields`, `SetFieldByPath`.
- Create `pkg/universe/fieldpath_test.go` — table-driven tests for the reflection helper.
- Modify `pkg/universe/builtins_entity.go` — add `entity.inspect` + `entity.modify` args/results/handlers/registration.
- Modify `pkg/universe/builtins_entity_test.go` — update registration test, add accessor + inspect + modify tests.

**Frontend (web-admin):**
- Modify `web-admin/src/lib/types.ts` — `EntityListRow`, `EntityInspectRow`, `EntityInspectResult`.
- Create `web-admin/src/routes/entities.svelte` — list page.
- Create `web-admin/src/components/EntityDrawer.svelte` — inspect/edit/despawn drawer.
- Modify `web-admin/src/app.svelte` — route wiring.
- Modify `web-admin/src/components/Sidebar.svelte` — sidebar entry.
- Modify `web-admin/src/components/CommandPalette.svelte` — entity search entries (best-effort; see Task 9).

---

## Task 1: Component accessors on EntityKindDef

**Files:**
- Modify: `pkg/universe/entity_kind.go`
- Test: `pkg/universe/builtins_entity_test.go` (new test func appended)

- [ ] **Step 1: Write the failing test** — append to `pkg/universe/builtins_entity_test.go`:

```go
// ── ComponentAccessors ────────────────────────────────────────────────────────

func TestComponentAccessors_ReturnsLivePointer(t *testing.T) {
	coords.SetCellSize(1024)
	log := logger.New()
	eng := engine.New(engine.DefaultConfig(), netpkg.NewConnManager(), log)
	parsed, _ := ParseCellID("0_0")
	stage := NewStage(eng, parsed, 300, nil)
	w := stage.ECSWorld()

	// Build a kind def with Position as a Required component via the same
	// low-level primitive mmokit.RegisterKind uses.
	def := EntityKindDef{Kind: 7, Name: "Probe"}
	posType := reflect.TypeFor[component.Position]()
	KindComponentByID(&def, w, ecs.TypeID(w, posType), posType, KindComponentRequired)
	stage.RegisterEntityKind(def)

	e := stage.Spawn(component.Position{X: 12, Y: 34}, component.EntityKind{Type: 7})

	accs := stage.EntityKindDefs()[7].ComponentAccessors()
	if len(accs) != 1 {
		t.Fatalf("ComponentAccessors len = %d, want 1", len(accs))
	}
	if accs[0].Name != "Position" {
		t.Errorf("accessor Name = %q, want Position", accs[0].Name)
	}
	got, ok := accs[0].Get(e.Handle())
	if !ok {
		t.Fatal("Get returned ok=false for present component")
	}
	pos, ok := got.(*component.Position)
	if !ok {
		t.Fatalf("Get returned %T, want *component.Position", got)
	}
	if pos.X != 12 || pos.Y != 34 {
		t.Errorf("pos = %+v, want {12 34}", *pos)
	}
}
```

Note: `reflect` and `ecs` are already imported in the test file (reflect) — add `"github.com/mlange-42/ark/ecs"` to the import block if missing.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/universe/ -run TestComponentAccessors_ReturnsLivePointer -count=1`
Expected: FAIL — `accs[0].ComponentAccessors undefined` / `ComponentAccessor` not defined.

- [ ] **Step 3: Implement** — in `pkg/universe/entity_kind.go`:

Add the `ComponentAccessor` type after the imports / near `EntityKindDef`:

```go
// ComponentAccessor exposes one component on an entity for runtime inspection
// and mutation. Built during kind registration with the concrete component
// type in scope, so it can resolve the component without a typed Map1[T].
type ComponentAccessor struct {
	Name string       // component struct type name, e.g. "Health"
	Type reflect.Type // the component struct type
	// Get returns a *T (as any) pointing at the entity's live component
	// storage, or (nil, false) if the entity lacks the component.
	Get func(entity ecs.Entity) (any, bool)
}
```

Add a field to `kindComponent`:

```go
// buildAccessor constructs a ComponentAccessor for this component bound to
// the registration-time world. nil only if registration predates accessors.
buildAccessor func() ComponentAccessor
```

In `KindComponentByID`, after `u := w.Unsafe()` and before `def.components = append(...)`, set the builder on `kc`:

```go
	kc.buildAccessor = func() ComponentAccessor {
		return ComponentAccessor{
			Name: t.Name(),
			Type: t,
			Get: func(entity ecs.Entity) (any, bool) {
				if !u.Has(entity, id) {
					return nil, false
				}
				return reflect.NewAt(t, u.Get(entity, id)).Interface(), true
			},
		}
	}
```

Add the accessor enumerator method (next to `func (def *EntityKindDef) Components()`):

```go
// ComponentAccessors returns one accessor per registered component (including
// local-only components), in registration order. Used by entity.inspect /
// entity.modify to enumerate and mutate component fields generically.
func (def *EntityKindDef) ComponentAccessors() []ComponentAccessor {
	out := make([]ComponentAccessor, 0, len(def.components))
	for _, kc := range def.components {
		if kc.buildAccessor != nil {
			out = append(out, kc.buildAccessor())
		}
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/universe/ -run TestComponentAccessors_ReturnsLivePointer -count=1`
Expected: PASS

- [ ] **Step 5: Verify nothing else broke + commit**

Run: `go vet ./pkg/universe/ && go test ./pkg/universe/ -count=1`
Expected: vet clean, tests PASS.

```bash
git add pkg/universe/entity_kind.go pkg/universe/builtins_entity_test.go
git commit -m "feat(universe): component accessors on EntityKindDef"
```

---

## Task 2: Reflection field-walker (ListFields / SetFieldByPath)

**Files:**
- Create: `pkg/universe/fieldpath.go`
- Test: `pkg/universe/fieldpath_test.go`

- [ ] **Step 1: Write the failing test** — create `pkg/universe/fieldpath_test.go`:

```go
package universe

import "testing"

type fpInner struct {
	Max int32
}

type fpEnum uint8

type fpProbe struct {
	Current  int32
	Speed    float32
	Alive    bool
	Label    string
	Mode     fpEnum
	Nested   fpInner
	Tags     []string       // read-only
	Counts   map[string]int // read-only
	hidden   int            // unexported — skipped
}

func TestListFields_EnumeratesScalarLeaves(t *testing.T) {
	p := &fpProbe{Current: 5, Speed: 1.5, Alive: true, Label: "hi", Mode: 2,
		Nested: fpInner{Max: 9}, Tags: []string{"a"}, Counts: map[string]int{"x": 1}}
	fields := ListFields(p)

	byPath := map[string]FieldInfo{}
	for _, f := range fields {
		byPath[f.Path] = f
	}

	for _, want := range []string{"Current", "Speed", "Alive", "Label", "Mode", "Nested.Max"} {
		f, ok := byPath[want]
		if !ok {
			t.Fatalf("missing scalar leaf %q; got %v", want, byPath)
		}
		if !f.Editable {
			t.Errorf("%q Editable = false, want true", want)
		}
	}
	if byPath["Current"].Value != "5" {
		t.Errorf("Current value = %q, want 5", byPath["Current"].Value)
	}
	// Slices/maps are surfaced read-only.
	if f, ok := byPath["Tags"]; !ok || f.Editable {
		t.Errorf("Tags should be present and read-only, got %+v ok=%v", f, ok)
	}
	if f, ok := byPath["Counts"]; !ok || f.Editable {
		t.Errorf("Counts should be present and read-only, got %+v ok=%v", f, ok)
	}
	// Unexported fields are skipped.
	if _, ok := byPath["hidden"]; ok {
		t.Error("unexported field 'hidden' must not appear")
	}
}

func TestSetFieldByPath_Scalars(t *testing.T) {
	cases := []struct {
		path, val, wantNew string
	}{
		{"Current", "42", "42"},
		{"Speed", "2.5", "2.5"},
		{"Alive", "false", "false"},
		{"Label", "world", "world"},
		{"Mode", "3", "3"},
		{"Nested.Max", "100", "100"},
	}
	for _, c := range cases {
		p := &fpProbe{Current: 5, Speed: 1.5, Alive: true, Label: "hi", Mode: 1, Nested: fpInner{Max: 9}}
		_, gotNew, err := SetFieldByPath(p, c.path, c.val)
		if err != nil {
			t.Fatalf("SetFieldByPath(%q,%q): %v", c.path, c.val, err)
		}
		if gotNew != c.wantNew {
			t.Errorf("%q new = %q, want %q", c.path, gotNew, c.wantNew)
		}
	}
	// Verify the int actually landed.
	p := &fpProbe{}
	if _, _, err := SetFieldByPath(p, "Current", "7"); err != nil || p.Current != 7 {
		t.Fatalf("Current not set: p.Current=%d err=%v", p.Current, err)
	}
	if _, _, err := SetFieldByPath(p, "Nested.Max", "11"); err != nil || p.Nested.Max != 11 {
		t.Fatalf("Nested.Max not set: %d err=%v", p.Nested.Max, err)
	}
}

func TestSetFieldByPath_Errors(t *testing.T) {
	p := &fpProbe{}
	if _, _, err := SetFieldByPath(p, "Nope", "1"); err == nil {
		t.Error("expected error for unknown field")
	}
	if _, _, err := SetFieldByPath(p, "Tags", "x"); err == nil {
		t.Error("expected error for read-only slice field")
	}
	if _, _, err := SetFieldByPath(p, "Current", "abc"); err == nil {
		t.Error("expected error for uncoercible int")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/universe/ -run 'TestListFields|TestSetFieldByPath' -count=1`
Expected: FAIL — `ListFields`/`SetFieldByPath`/`FieldInfo` undefined.

- [ ] **Step 3: Implement** — create `pkg/universe/fieldpath.go`:

```go
package universe

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// FieldInfo describes one inspectable field on a component.
type FieldInfo struct {
	Path     string // dotted path within the component, e.g. "Current" or "Nested.Max"
	Type     string // human type, e.g. "int32", "float32", "bool", "string", "[]string"
	Value    string // rendered value (scalars: literal; non-scalar: JSON)
	Editable bool   // true for coercible scalar leaves
}

// ListFields reflects over component (a pointer to a struct) and returns one
// FieldInfo per exported scalar leaf — recursing through nested structs and
// non-nil struct pointers — plus one read-only entry per slice/map/array field
// rendered as JSON. Unexported fields are skipped.
func ListFields(component any) []FieldInfo {
	v := reflect.ValueOf(component)
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	var out []FieldInfo
	walkFields(v, "", &out)
	return out
}

func walkFields(v reflect.Value, prefix string, out *[]FieldInfo) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if sf.PkgPath != "" { // unexported
			continue
		}
		fv := v.Field(i)
		path := sf.Name
		if prefix != "" {
			path = prefix + "." + sf.Name
		}
		// Deref non-nil struct pointers.
		if fv.Kind() == reflect.Pointer {
			if fv.IsNil() {
				*out = append(*out, FieldInfo{Path: path, Type: fv.Type().String(), Value: "null", Editable: false})
				continue
			}
			fv = fv.Elem()
		}
		switch fv.Kind() {
		case reflect.Bool, reflect.String,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64:
			*out = append(*out, FieldInfo{
				Path:     path,
				Type:     fv.Type().String(),
				Value:    renderScalar(fv),
				Editable: true,
			})
		case reflect.Struct:
			walkFields(fv, path, out)
		default: // slice, map, array, chan, func, interface, complex
			*out = append(*out, FieldInfo{
				Path:     path,
				Type:     fv.Type().String(),
				Value:    renderJSON(fv),
				Editable: false,
			})
		}
	}
}

func renderScalar(v reflect.Value) string {
	switch v.Kind() {
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10)
	case reflect.Float32:
		return strconv.FormatFloat(v.Float(), 'g', -1, 32)
	case reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64)
	case reflect.String:
		return v.String()
	default:
		return fmt.Sprintf("%v", v.Interface())
	}
}

func renderJSON(v reflect.Value) string {
	b, err := json.Marshal(v.Interface())
	if err != nil {
		return fmt.Sprintf("%v", v.Interface())
	}
	return string(b)
}

// SetFieldByPath walks the dotted path to a settable scalar leaf on component
// (a pointer to a struct), coerces strVal to the leaf's kind, sets it, and
// returns the rendered old and new values. Errors on unknown path, non-scalar /
// read-only leaf, or uncoercible value.
func SetFieldByPath(component any, path, strVal string) (oldStr, newStr string, err error) {
	v := reflect.ValueOf(component)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return "", "", fmt.Errorf("component must be a non-nil pointer")
	}
	v = v.Elem()
	segs := strings.Split(path, ".")
	for i, seg := range segs {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return "", "", fmt.Errorf("nil pointer at %q", strings.Join(segs[:i], "."))
			}
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct {
			return "", "", fmt.Errorf("%q is not a struct", strings.Join(segs[:i], "."))
		}
		f := v.FieldByName(seg)
		if !f.IsValid() {
			return "", "", fmt.Errorf("unknown field %q", seg)
		}
		if !f.CanSet() {
			return "", "", fmt.Errorf("field %q is not settable (unexported?)", seg)
		}
		v = f
	}
	// v is the leaf.
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return "", "", fmt.Errorf("cannot set nil pointer leaf %q", path)
		}
		v = v.Elem()
	}
	oldStr = renderScalar(v)
	if err := coerceInto(v, strVal); err != nil {
		return "", "", err
	}
	return oldStr, renderScalar(v), nil
}

func coerceInto(v reflect.Value, s string) error {
	switch v.Kind() {
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return fmt.Errorf("cannot parse %q as bool", s)
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, v.Type().Bits())
		if err != nil {
			return fmt.Errorf("cannot parse %q as %s", s, v.Type())
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(s, 10, v.Type().Bits())
		if err != nil {
			return fmt.Errorf("cannot parse %q as %s", s, v.Type())
		}
		v.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(s, v.Type().Bits())
		if err != nil {
			return fmt.Errorf("cannot parse %q as %s", s, v.Type())
		}
		v.SetFloat(f)
	case reflect.String:
		v.SetString(s)
	default:
		return fmt.Errorf("field of kind %s is not editable", v.Kind())
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/universe/ -run 'TestListFields|TestSetFieldByPath' -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/fieldpath.go pkg/universe/fieldpath_test.go
git commit -m "feat(universe): reflection field-walker for component inspect/modify"
```

---

## Task 3: entity.inspect verb

**Files:**
- Modify: `pkg/universe/builtins_entity.go`
- Test: `pkg/universe/builtins_entity_test.go`

- [ ] **Step 1: Write the failing test** — append to `pkg/universe/builtins_entity_test.go`:

```go
// ── entity.inspect handler ────────────────────────────────────────────────────

// registerProbeKind registers a kind (id 7, "Probe") whose only component is
// Position, using the low-level accessor primitive. Returns the stage.
func registerProbeKind(t *testing.T, coord *Process) *Stage {
	t.Helper()
	stage := coord.Cells["0_0"].Stage
	w := stage.ECSWorld()
	def := EntityKindDef{Kind: 7, Name: "Probe"}
	posType := reflect.TypeFor[component.Position]()
	KindComponentByID(&def, w, ecs.TypeID(w, posType), posType, KindComponentRequired)
	stage.RegisterEntityKind(def)
	return stage
}

func TestEntityInspectHandler_ReturnsComponentFields(t *testing.T) {
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	withFreshRegistry(coord)
	if err := registerEntityCommands(coord); err != nil {
		t.Fatalf("registerEntityCommands: %v", err)
	}
	stage := registerProbeKind(t, coord)
	stage.Spawn(component.Position{X: 11, Y: 22}, component.EntityKind{Type: 7})
	netID := findFirstLiveNetID(t, stage)

	cmd, _ := coord.registry.Lookup("entity.inspect")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := cmd.Handler(ctx, &cmdsys.Env{}, entityInspectArgs{NetID: netID})
	if err != nil {
		t.Fatalf("inspect handler: %v", err)
	}
	out := res.(entityInspectResult)
	if out.Kind != "Probe" {
		t.Errorf("Kind = %q, want Probe", out.Kind)
	}
	var sawX bool
	for _, r := range out.Components {
		if r.Component == "Position" && r.Field == "X" {
			sawX = true
			if r.Value != "11" {
				t.Errorf("Position.X value = %q, want 11", r.Value)
			}
			if !r.Editable {
				t.Error("Position.X should be editable")
			}
		}
	}
	if !sawX {
		t.Errorf("expected a Position.X row; got %+v", out.Components)
	}
}

func TestEntityInspectHandler_UnknownNetID(t *testing.T) {
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	withFreshRegistry(coord)
	if err := registerEntityCommands(coord); err != nil {
		t.Fatalf("registerEntityCommands: %v", err)
	}
	cmd, _ := coord.registry.Lookup("entity.inspect")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := cmd.Handler(ctx, &cmdsys.Env{}, entityInspectArgs{NetID: 99999}); err == nil {
		t.Fatal("expected error for unknown netID")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/universe/ -run TestEntityInspectHandler -count=1`
Expected: FAIL — `entityInspectArgs`/`entityInspectResult` undefined, `entity.inspect` not registered.

- [ ] **Step 3: Implement** — in `pkg/universe/builtins_entity.go`:

Add args/result types (near the other type blocks, e.g. after the `entity.despawn` block):

```go
// ── entity.inspect ───────────────────────────────────────────────────────────

type entityInspectArgs struct {
	NetID uint32 `cmd:"help=entity network ID"`
}

type entityInspectRow struct {
	Component string
	Field     string // dotted path WITHIN the component (maps to entity.modify)
	Type      string
	Value     string
	Editable  bool
}

type entityInspectResult struct {
	NetID      uint32
	Kind       string
	Components []entityInspectRow `cmd:"table"`
}
```

Register the command inside `registerEntityCommands`, after the `entity.summary` block and before `return nil`:

```go
	if err := reg.Register(cmdsys.Command{
		Verb:        "entity.inspect",
		Capability:  "entity.inspect",
		Description: "list an entity's components and field values by network ID",
		Route:       cmdsys.RouteEntityOwner,
		Args:        entityInspectArgs{},
		Result:      entityInspectResult{},
		Handler:     entityInspectHandler(coord),
	}); err != nil {
		return fmt.Errorf("entity.inspect: %w", err)
	}
```

Add the handler (near the other handlers, e.g. after `entityDespawnHandler`):

```go
func entityInspectHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(entityInspectArgs)

		ownerCell, ownerCellID, ok := findCellOwningNetID(coord, args.NetID)
		if !ok {
			return nil, fmt.Errorf("entity.inspect: netID %d not found on any local cell", args.NetID)
		}

		return runOnCell(ctx, ownerCell, func() (entityInspectResult, error) {
			entity, pres, exists := ownerCell.Stage.LookupNetID(args.NetID)
			if !exists || pres != PresenceLive {
				return entityInspectResult{}, fmt.Errorf("entity.inspect: netID %d not live in cell %s", args.NetID, ownerCellID)
			}

			kindMap := ecs.NewMap1[component.EntityKind](ownerCell.Stage.ECSWorld())
			if !kindMap.HasAll(entity) {
				return entityInspectResult{}, fmt.Errorf("entity.inspect: netID %d has no EntityKind", args.NetID)
			}
			kindType := kindMap.Get(entity).Type
			def, ok := ownerCell.Stage.EntityKindDefs()[kindType]
			if !ok {
				return entityInspectResult{}, fmt.Errorf("entity.inspect: kind %d not registered", kindType)
			}

			var rows []entityInspectRow
			for _, acc := range def.ComponentAccessors() {
				comp, present := acc.Get(entity)
				if !present {
					continue
				}
				for _, fi := range ListFields(comp) {
					rows = append(rows, entityInspectRow{
						Component: acc.Name,
						Field:     fi.Path,
						Type:      fi.Type,
						Value:     fi.Value,
						Editable:  fi.Editable,
					})
				}
			}
			return entityInspectResult{
				NetID:      args.NetID,
				Kind:       resolveKindName(ownerCell.Stage, kindType),
				Components: rows,
			}, nil
		})
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/universe/ -run TestEntityInspectHandler -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/builtins_entity.go pkg/universe/builtins_entity_test.go
git commit -m "feat(universe): entity.inspect verb"
```

---

## Task 4: entity.modify verb

**Files:**
- Modify: `pkg/universe/builtins_entity.go`
- Test: `pkg/universe/builtins_entity_test.go`

- [ ] **Step 1: Write the failing test** — append to `pkg/universe/builtins_entity_test.go`:

```go
// ── entity.modify handler ─────────────────────────────────────────────────────

func TestEntityModifyHandler_SetsField(t *testing.T) {
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	withFreshRegistry(coord)
	if err := registerEntityCommands(coord); err != nil {
		t.Fatalf("registerEntityCommands: %v", err)
	}
	stage := registerProbeKind(t, coord)
	e := stage.Spawn(component.Position{X: 11, Y: 22}, component.EntityKind{Type: 7})
	netID := findFirstLiveNetID(t, stage)

	cmd, _ := coord.registry.Lookup("entity.modify")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := cmd.Handler(ctx, &cmdsys.Env{}, entityModifyArgs{
		NetID: netID, Component: "Position", Field: "X", Value: "99",
	})
	if err != nil {
		t.Fatalf("modify handler: %v", err)
	}
	out := res.(entityModifyResult)
	if out.Old != "11" || out.New != "99" {
		t.Errorf("old/new = %q/%q, want 11/99", out.Old, out.New)
	}
	// Verify the live component changed.
	pos := ecs.NewMap1[component.Position](stage.ECSWorld()).Get(e.Handle())
	if pos.X != 99 {
		t.Errorf("live Position.X = %v, want 99", pos.X)
	}
}

func TestEntityModifyHandler_Errors(t *testing.T) {
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	withFreshRegistry(coord)
	if err := registerEntityCommands(coord); err != nil {
		t.Fatalf("registerEntityCommands: %v", err)
	}
	stage := registerProbeKind(t, coord)
	stage.Spawn(component.Position{X: 1, Y: 2}, component.EntityKind{Type: 7})
	netID := findFirstLiveNetID(t, stage)
	cmd, _ := coord.registry.Lookup("entity.modify")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Unknown component.
	if _, err := cmd.Handler(ctx, &cmdsys.Env{}, entityModifyArgs{NetID: netID, Component: "Nope", Field: "X", Value: "1"}); err == nil {
		t.Error("expected error for unknown component")
	}
	// Unknown field.
	if _, err := cmd.Handler(ctx, &cmdsys.Env{}, entityModifyArgs{NetID: netID, Component: "Position", Field: "Z", Value: "1"}); err == nil {
		t.Error("expected error for unknown field")
	}
	// Bad value.
	if _, err := cmd.Handler(ctx, &cmdsys.Env{}, entityModifyArgs{NetID: netID, Component: "Position", Field: "X", Value: "abc"}); err == nil {
		t.Error("expected error for uncoercible value")
	}
	// Unknown netID.
	if _, err := cmd.Handler(ctx, &cmdsys.Env{}, entityModifyArgs{NetID: 99999, Component: "Position", Field: "X", Value: "1"}); err == nil {
		t.Error("expected error for unknown netID")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/universe/ -run TestEntityModifyHandler -count=1`
Expected: FAIL — `entityModifyArgs`/`entityModifyResult` undefined, `entity.modify` not registered.

- [ ] **Step 3: Implement** — in `pkg/universe/builtins_entity.go`:

Add types:

```go
// ── entity.modify ────────────────────────────────────────────────────────────

type entityModifyArgs struct {
	NetID     uint32 `cmd:"help=entity network ID"`
	Component string `cmd:"help=component name, e.g. Health"`
	Field     string `cmd:"help=dotted field path within the component, e.g. Current"`
	Value     string `cmd:"help=new value (coerced to the field's type)"`
}

type entityModifyResult struct {
	NetID     uint32
	Component string
	Field     string
	Old       string
	New       string
}
```

Register inside `registerEntityCommands` (after the `entity.inspect` block):

```go
	if err := reg.Register(cmdsys.Command{
		Verb:        "entity.modify",
		Capability:  "entity.modify",
		Description: "set a scalar field on a component of a live entity by network ID",
		Route:       cmdsys.RouteEntityOwner,
		Args:        entityModifyArgs{},
		Result:      entityModifyResult{},
		Handler:     entityModifyHandler(coord),
	}); err != nil {
		return fmt.Errorf("entity.modify: %w", err)
	}
```

Add the handler:

```go
func entityModifyHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(entityModifyArgs)

		ownerCell, ownerCellID, ok := findCellOwningNetID(coord, args.NetID)
		if !ok {
			return nil, fmt.Errorf("entity.modify: netID %d not found on any local cell", args.NetID)
		}

		return runOnCell(ctx, ownerCell, func() (entityModifyResult, error) {
			entity, pres, exists := ownerCell.Stage.LookupNetID(args.NetID)
			if !exists || pres != PresenceLive {
				return entityModifyResult{}, fmt.Errorf("entity.modify: netID %d not live in cell %s", args.NetID, ownerCellID)
			}

			kindMap := ecs.NewMap1[component.EntityKind](ownerCell.Stage.ECSWorld())
			if !kindMap.HasAll(entity) {
				return entityModifyResult{}, fmt.Errorf("entity.modify: netID %d has no EntityKind", args.NetID)
			}
			kindType := kindMap.Get(entity).Type
			def, ok := ownerCell.Stage.EntityKindDefs()[kindType]
			if !ok {
				return entityModifyResult{}, fmt.Errorf("entity.modify: kind %d not registered", kindType)
			}

			var comp any
			var found bool
			var available []string
			for _, acc := range def.ComponentAccessors() {
				available = append(available, acc.Name)
				if acc.Name == args.Component {
					c, present := acc.Get(entity)
					if !present {
						return entityModifyResult{}, fmt.Errorf("entity.modify: entity lacks component %q", args.Component)
					}
					comp, found = c, true
					break
				}
			}
			if !found {
				return entityModifyResult{}, fmt.Errorf("entity.modify: unknown component %q (available: %v)", args.Component, available)
			}

			oldStr, newStr, err := SetFieldByPath(comp, args.Field, args.Value)
			if err != nil {
				return entityModifyResult{}, fmt.Errorf("entity.modify: %w", err)
			}
			return entityModifyResult{
				NetID:     args.NetID,
				Component: args.Component,
				Field:     args.Field,
				Old:       oldStr,
				New:       newStr,
			}, nil
		})
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/universe/ -run TestEntityModifyHandler -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/builtins_entity.go pkg/universe/builtins_entity_test.go
git commit -m "feat(universe): entity.modify verb"
```

---

## Task 5: Update the registration test for the two new verbs

**Files:**
- Modify: `pkg/universe/builtins_entity_test.go`

- [ ] **Step 1: Extend the registration + arg/result tests** — in `TestEntityCommandsRegistration`, add rows to the `tests` slice:

```go
		{"entity.inspect", cmdsys.RouteEntityOwner, "entity.inspect"},
		{"entity.modify", cmdsys.RouteEntityOwner, "entity.modify"},
```

In `TestEntityCommandsArgResultTypes`, add rows to the loop slice:

```go
		{"entity.inspect", entityInspectArgs{}, entityInspectResult{}},
		{"entity.modify", entityModifyArgs{}, entityModifyResult{}},
```

- [ ] **Step 2: Run the full universe suite + vet**

Run: `go vet ./pkg/universe/ && go test ./pkg/universe/ -count=1`
Expected: PASS, vet clean.

- [ ] **Step 3: Build the whole module to ensure nothing else broke**

Run: `go vet ./...`
Expected: clean (no compile errors anywhere).

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/builtins_entity_test.go
git commit -m "test(universe): cover entity.inspect/modify registration"
```

---

## Task 6: Admin SPA — types + Entities list page + routing

**Files:**
- Modify: `web-admin/src/lib/types.ts`
- Create: `web-admin/src/routes/entities.svelte`
- Modify: `web-admin/src/app.svelte`
- Modify: `web-admin/src/components/Sidebar.svelte`

> **Before editing:** read the four files to match the exact existing patterns (import style, `apiPost` envelope shape `{ok, result, error}`, how `players.svelte` filters/searches, how `Sidebar.svelte` `builtinItems` entries look, and the route-switch shape in `app.svelte`). The snippets below are the intended shape — adapt names to the real file conventions you observe.

- [ ] **Step 1: Add types** — append to `web-admin/src/lib/types.ts`:

```ts
export type EntityListRow = {
  NetID: number;
  Kind: string;
  WorldX: number;
  WorldY: number;
  CellID: string;
  HostID: string;
};

export type EntityInspectRow = {
  Component: string;
  Field: string;
  Type: string;
  Value: string;
  Editable: boolean;
};

export type EntityInspectResult = {
  NetID: number;
  Kind: string;
  Components: EntityInspectRow[];
};
```

- [ ] **Step 2: Create the list page** — `web-admin/src/routes/entities.svelte`:

```svelte
<script lang="ts">
  import { apiPost } from "../lib/api";
  import type { EntityListRow } from "../lib/types";
  import EntityDrawer from "../components/EntityDrawer.svelte";

  let rows = $state<EntityListRow[]>([]);
  let loading = $state(false);
  let error = $state("");
  let kindFilter = $state("");
  let cellFilter = $state("");
  let search = $state("");
  let selected = $state<EntityListRow | null>(null);

  async function refresh() {
    loading = true;
    error = "";
    try {
      const res = await apiPost<{ ok: boolean; result?: { Entities?: EntityListRow[] }; error?: string }>(
        "/admin/api/commands/entity.list",
        kindFilter ? { Kind: kindFilter } : {},
      );
      if (res.ok === false) {
        error = res.error || "command failed";
        rows = [];
      } else {
        rows = res.result?.Entities ?? [];
      }
    } catch (e) {
      error = (e as Error).message;
      rows = [];
    } finally {
      loading = false;
    }
  }

  let filtered = $derived(
    rows.filter((r) => {
      if (cellFilter && r.CellID !== cellFilter) return false;
      if (search) {
        const q = search.toLowerCase();
        if (!String(r.NetID).includes(q) && !r.Kind.toLowerCase().includes(q)) return false;
      }
      return true;
    }),
  );

  $effect(() => {
    refresh();
  });
</script>

<div class="flex h-full">
  <div class="flex-1 overflow-auto p-4">
    <div class="mb-3 flex items-center gap-2">
      <h1 class="text-lg font-semibold">Entities</h1>
      <button class="rounded border px-2 py-1 text-sm" onclick={refresh} disabled={loading}>
        {loading ? "Loading…" : "Refresh"}
      </button>
      <input class="rounded border px-2 py-1 text-sm" placeholder="kind filter" bind:value={kindFilter} />
      <input class="rounded border px-2 py-1 text-sm" placeholder="cell filter" bind:value={cellFilter} />
      <input class="rounded border px-2 py-1 text-sm" placeholder="search netid/kind" bind:value={search} />
    </div>
    {#if error}
      <div class="mb-2 text-sm text-red-400">{error}</div>
    {/if}
    <table class="w-full text-sm">
      <thead>
        <tr class="text-left opacity-70">
          <th class="py-1">NetID</th><th>Kind</th><th>Cell</th><th>X</th><th>Y</th>
        </tr>
      </thead>
      <tbody>
        {#each filtered as r (r.NetID)}
          <tr class="cursor-pointer hover:bg-white/5" onclick={() => (selected = r)}>
            <td class="py-1">{r.NetID}</td>
            <td>{r.Kind}</td>
            <td>{r.CellID}</td>
            <td>{r.WorldX.toFixed(1)}</td>
            <td>{r.WorldY.toFixed(1)}</td>
          </tr>
        {/each}
        {#if filtered.length === 0}
          <tr><td colspan="5" class="py-3 opacity-50">no entities</td></tr>
        {/if}
      </tbody>
    </table>
  </div>

  {#if selected}
    <EntityDrawer netID={selected.NetID} onClose={() => (selected = null)} onDespawned={() => { selected = null; refresh(); }} />
  {/if}
</div>
```

- [ ] **Step 3: Wire the route** — in `web-admin/src/app.svelte`: import the page (`import Entities from "./routes/entities.svelte";`) and add a branch to the route switch matching the existing style, e.g. `{:else if path === "/entities"}<Entities />`.

- [ ] **Step 4: Add the sidebar entry** — in `web-admin/src/components/Sidebar.svelte`, add to `builtinItems` in the ENGINE group (import an icon such as `Boxes` from `@lucide/svelte` alongside the existing icon imports):

```ts
{ id: "entities", label: "Entities", icon: Boxes, group: "ENGINE", path: "/entities", glyph: "E" },
```

(If glyph "E" collides with an existing shortcut, pick an unused letter.)

- [ ] **Step 5: Build the SPA to typecheck**

Run: `cd web-admin && bun install && bun run build`
Expected: build succeeds, bundle written to `pkg/admin/static/dist/`. Fix any TS errors (e.g. apiPost generic shape) before moving on.

- [ ] **Step 6: Commit**

```bash
git add web-admin/src/lib/types.ts web-admin/src/routes/entities.svelte web-admin/src/app.svelte web-admin/src/components/Sidebar.svelte pkg/admin/static/dist
git commit -m "feat(admin-spa): Entities list page + route + sidebar entry"
```

---

## Task 7: Admin SPA — EntityDrawer (inspect + edit + despawn)

**Files:**
- Create: `web-admin/src/components/EntityDrawer.svelte`

> **Before editing:** read `WorldInspector.svelte` (dirty-buffer get/setField pattern), `PlayerDrawer.svelte` (drawer shell + loadInfo POST), and `ConfirmDialog.svelte` (props). Match their styling/structure. The snippet below is the intended behavior; adapt class names and the confirm-dialog usage to the real components.

- [ ] **Step 1: Create the drawer** — `web-admin/src/components/EntityDrawer.svelte`:

```svelte
<script lang="ts">
  import { apiPost } from "../lib/api";
  import type { EntityInspectResult, EntityInspectRow } from "../lib/types";
  import ConfirmDialog from "./ConfirmDialog.svelte";

  let { netID, onClose, onDespawned }: {
    netID: number;
    onClose: () => void;
    onDespawned: () => void;
  } = $props();

  let data = $state<EntityInspectResult | null>(null);
  let loading = $state(false);
  let error = $state("");
  let dirty = $state<Record<string, string>>({}); // key: "Component/Field"
  let applying = $state(false);
  let confirmOpen = $state(false);

  function key(r: EntityInspectRow): string {
    return `${r.Component}/${r.Field}`;
  }

  async function inspect() {
    loading = true;
    error = "";
    try {
      const res = await apiPost<{ ok: boolean; result?: EntityInspectResult; error?: string }>(
        "/admin/api/commands/entity.inspect",
        { NetID: netID },
      );
      if (res.ok === false) error = res.error || "command failed";
      else { data = res.result ?? null; dirty = {}; }
    } catch (e) {
      error = (e as Error).message;
    } finally {
      loading = false;
    }
  }

  function setField(r: EntityInspectRow, value: string) {
    const k = key(r);
    if (value === r.Value) {
      const next = { ...dirty };
      delete next[k];
      dirty = next;
    } else {
      dirty = { ...dirty, [k]: value };
    }
  }

  let dirtyCount = $derived(Object.keys(dirty).length);

  async function apply() {
    if (!data || dirtyCount === 0) return;
    applying = true;
    error = "";
    try {
      for (const r of data.Components) {
        const k = key(r);
        if (!(k in dirty)) continue;
        const res = await apiPost<{ ok: boolean; error?: string }>(
          "/admin/api/commands/entity.modify",
          { NetID: netID, Component: r.Component, Field: r.Field, Value: dirty[k] },
        );
        if (res.ok === false) { error = `${k}: ${res.error}`; break; }
      }
      await inspect(); // refresh + clear dirty
    } catch (e) {
      error = (e as Error).message;
    } finally {
      applying = false;
    }
  }

  async function despawn() {
    confirmOpen = false;
    try {
      await apiPost("/admin/api/commands/entity.despawn", { NetID: netID });
      onDespawned();
    } catch (e) {
      error = (e as Error).message;
    }
  }

  // Group rows by component for display.
  let groups = $derived.by(() => {
    const m = new Map<string, EntityInspectRow[]>();
    for (const r of data?.Components ?? []) {
      if (!m.has(r.Component)) m.set(r.Component, []);
      m.get(r.Component)!.push(r);
    }
    return [...m.entries()];
  });

  $effect(() => {
    netID; // re-inspect when netID changes
    inspect();
  });
</script>

<aside class="flex w-96 flex-col border-l border-white/10 bg-black/30">
  <header class="flex items-center justify-between border-b border-white/10 p-3">
    <div>
      <div class="font-semibold">Entity {netID}</div>
      <div class="text-xs opacity-60">{data?.Kind ?? ""}</div>
    </div>
    <button class="opacity-70 hover:opacity-100" onclick={onClose} aria-label="Close">✕</button>
  </header>

  <div class="flex-1 overflow-auto p-3">
    {#if loading}<div class="opacity-60">Loading…</div>{/if}
    {#if error}<div class="mb-2 text-sm text-red-400">{error}</div>{/if}
    {#each groups as [comp, fields] (comp)}
      <div class="mb-4">
        <div class="mb-1 text-xs font-semibold uppercase opacity-60">{comp}</div>
        {#each fields as r (r.Field)}
          <div class="flex items-center gap-2 py-0.5 text-sm">
            <label class="w-28 shrink-0 truncate opacity-80" title={r.Field}>{r.Field}</label>
            {#if r.Editable}
              {#if r.Type === "bool"}
                <input type="checkbox"
                  checked={(dirty[key(r)] ?? r.Value) === "true"}
                  onchange={(e) => setField(r, (e.currentTarget as HTMLInputElement).checked ? "true" : "false")} />
              {:else}
                <input class="w-full rounded border border-white/10 bg-transparent px-1 py-0.5"
                  type={r.Type.startsWith("int") || r.Type.startsWith("uint") || r.Type.startsWith("float") ? "number" : "text"}
                  value={dirty[key(r)] ?? r.Value}
                  oninput={(e) => setField(r, (e.currentTarget as HTMLInputElement).value)} />
              {/if}
            {:else}
              <span class="w-full truncate opacity-50" title={r.Value}>{r.Value}</span>
            {/if}
          </div>
        {/each}
      </div>
    {/each}
  </div>

  <footer class="flex items-center gap-2 border-t border-white/10 p-3">
    <button class="rounded border border-white/10 px-2 py-1 text-sm disabled:opacity-40"
      onclick={apply} disabled={dirtyCount === 0 || applying}>
      {applying ? "Applying…" : `Apply (${dirtyCount})`}
    </button>
    <button class="rounded border border-white/10 px-2 py-1 text-sm disabled:opacity-40"
      onclick={() => (dirty = {})} disabled={dirtyCount === 0}>Discard</button>
    <span class="flex-1"></span>
    <button class="rounded border border-red-500/40 px-2 py-1 text-sm text-red-300"
      onclick={() => (confirmOpen = true)}>Despawn</button>
  </footer>
</aside>

<ConfirmDialog
  open={confirmOpen}
  title="Despawn entity {netID}?"
  confirmLabel="Despawn"
  danger={true}
  onConfirm={despawn}
  onCancel={() => (confirmOpen = false)}>
  This permanently removes the entity from the world.
</ConfirmDialog>
```

- [ ] **Step 2: Build the SPA**

Run: `cd web-admin && bun run build`
Expected: success. Resolve TS / Svelte errors (verify `ConfirmDialog` prop names against the real component — adjust `confirmLabel`/`danger`/`children` usage to match).

- [ ] **Step 3: Commit**

```bash
git add web-admin/src/components/EntityDrawer.svelte pkg/admin/static/dist
git commit -m "feat(admin-spa): EntityDrawer inspect/edit/despawn"
```

---

## Task 8: Command palette entity entries (best-effort)

**Files:**
- Modify: `web-admin/src/components/CommandPalette.svelte`

> This task is optional polish. The palette reads live stores; there is no live entities store (Entities is fetch-on-demand). If wiring entities into the palette requires a store that doesn't exist, SKIP this task and note it in the final summary rather than inventing a store/SSE topic (out of scope per spec).

- [ ] **Step 1: Inspect the palette.** Read `CommandPalette.svelte`. If (and only if) there is a trivial way to add a navigation entry "Go to Entities" (a static command that navigates to `/entities`), add it following the existing entry pattern. Do NOT add per-entity search (no store backs it in v1).

- [ ] **Step 2: Build + commit (only if changed)**

Run: `cd web-admin && bun run build`
```bash
git add web-admin/src/components/CommandPalette.svelte pkg/admin/static/dist
git commit -m "feat(admin-spa): command palette → Entities navigation"
```

---

## Task 9: Full verification

**Files:** none (verification only)

- [ ] **Step 1: Go vet + full test suite**

Run: `go vet ./... && go test ./pkg/universe/ -count=1`
Expected: vet clean; universe tests PASS.

- [ ] **Step 2: Build server + SDK via just (catches integration breakage)**

Run: `just build`
Expected: succeeds (compiles server to `bin/`, regenerates SDK, builds admin bundle). If `just build` requires services not available headless, fall back to `go vet ./...` + `cd web-admin && bun run build` and note it.

- [ ] **Step 3: Lint guard for game-layer ark imports (should be unaffected, but confirm)**

Run: `just lint-no-ark`
Expected: passes (we only touched `pkg/universe`, which may import ark).

- [ ] **Step 4: Write the smoke-test instructions inline** (do NOT create a `*_SMOKE.md` file — deliver in the final chat summary). The manual smoke an operator runs:

```
# Console (single-process dev server: `just run` or examples/4node-basic `just dev`)
entity list
entity inspect <netid>           # table of Component/Field/Type/Value/Editable
entity modify <netid> Health Current 50
entity inspect <netid>           # confirm Current == 50

# Admin UI (http://localhost:9101/admin → ENGINE → Entities)
#  - Refresh lists entities; click a row → drawer
#  - edit an editable field → Apply → drawer re-inspects with new value
#  - Despawn → ConfirmDialog → entity removed from list
```

- [ ] **Step 5: Final commit if anything regenerated**

```bash
git add -A
git commit -m "chore: rebuild artifacts for entity inspect/modify" --allow-empty
```

---

## Self-Review Notes

- **Spec coverage:** Task 1 (accessors) + Task 2 (field-walker) = engine layers 1-2; Tasks 3-4 = the two verbs (layer 3); Task 5 = registration coverage; Tasks 6-8 = admin UI (list, drawer, palette); Task 9 = verification + smoke. Read-only complex fields handled in Task 2 (`default:` branch → `Editable:false`). Replica rejection in Tasks 3-4 (`pres != PresenceLive`). `RouteEntityOwner` reused (no new routing). RBAC: new capabilities are auto-covered by the seeded `*.*` admin grant + console caller bypass — no grant wiring needed.
- **Type consistency:** `entityInspectRow{Component,Field,Type,Value,Editable}` is identical across handler (Task 3), test (Task 3), and TS `EntityInspectRow` (Task 6). `FieldInfo{Path,Type,Value,Editable}` (Task 2) maps to `entityInspectRow.Field = fi.Path` in Task 3. `ComponentAccessor{Name,Type,Get}` (Task 1) consumed identically in Tasks 3-4.
- **Known limitation carried from spec:** only kind-bundle components appear; slice/map/custom-marshaled fields are read-only.
