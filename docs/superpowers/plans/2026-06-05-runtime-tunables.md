# Runtime Tunables Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let operators tweak per-system numeric/bool values live (console + admin sliders) for both native Go systems and hot-swappable wasm modules, via one `tune:` tag convention; and delete the old global-`GameConfig` runtime-config command surface.

**Architecture:** A tunable is a `tune:`-tagged exported scalar field — the single source of truth, read live every tick. A new zero-dep `pkg/tunable` package parses the tag and reflects native structs into a `Source` (Tunables/Set). The wasm guest reflects the same tag to publish a schema + accept a `[]float64` value block across two new optional ABI exports; the host `wasmSystem` adapter implements `Source` by bridging that block. A per-`*Process` registry holds intended values, pushes them to every live cell on `set`, and re-applies on cell (re)creation. Operators drive it through `tune.*` cmdsys verbs (console) and a first-class `/tunables` admin route (sliders/toggles).

**Tech Stack:** Go 1.24+ (`unsafe`, reflection), wazero (`GOOS=wasip1`), cmdsys (`RouteAllHosts`), Svelte 5 + Vite + Bun (admin SPA), Postgres unaffected (tunables are ephemeral).

**Spec:** `docs/superpowers/specs/2026-06-05-runtime-tunables-design.md`

**Conventions to honor (project memory):** mmokit facade only in game code; omit inferable generic type args; required args positional / optional args as `--flags`; console list commands render as flat tables (`cmd:"table"`); never edit generated files; add logging to new server-side logic; build only into `bin/`/`dist/` (use `go vet ./...` / `-o /dev/null` for compile checks, never bare `go build ./...`); no re-export/alias shims when removing — update callers directly.

---

## File Structure

**New:**
- `pkg/tunable/tunable.go` — `Kind`, `Def`, `Source`, the `tune:` tag parser, `ToF64`/`FromF64`, range validation.
- `pkg/tunable/reflect.go` — `Reflect(ptr) Source`, `HasTunables(ptr) bool`, `ApplyDefaults`.
- `pkg/tunable/tunable_test.go`, `pkg/tunable/reflect_test.go` — unit tests.
- `pkg/mmokit/tunable_registry.go` — per-`*Process` registry, `tunableSourceFor`, `SyncCellTunables`.
- `pkg/mmokit/tunable_verbs.go` — `tune.list/get/set/reset` cmdsys verbs + `tunables` SSE publish.
- `pkg/mmokit/internal/testmods/wavetune/main.go` — wasm fixture with two tunable fields (test).
- `pkg/admin/api_tunables.go` — `GET /admin/api/tunables` read route + `tunables` SSE topic publisher.
- `web-admin/src/routes/tunables.svelte` — the `/tunables` page (sliders/toggles).
- `web-admin/src/lib/tunables.ts` — typed fetch + SSE glue.

**Modified:**
- `pkg/wasmabi/abi.go` — two export-name consts + schema/block codec.
- `pkg/wasmsys/sdk.go` / `pkg/wasmsys/exports.go` — guest schema build, default-apply, `params_set` scatter, two exports.
- `pkg/wasmhost/module.go` — `ParamsSchema` / `ParamsSet`.
- `pkg/mmokit/wasm_system.go` — `wasmSystem[T]` implements `tunable.Source`; harvest schema at `Init`.
- `pkg/engine/loop.go` — `EachSystem(fn)` named iterator.
- `pkg/universe/coordinator.go` + `pkg/universe/cell_transfer_executor.go` — call `SyncCellTunables` after cell systems init (boot + split).
- `pkg/mmokit/mmokit.go` — register `tune.*` verbs in `New`; remove config facade aliases.
- `examples/simple/wasmmods/wave/main.go` — consts → tunable fields.
- `examples/simple/system_field.go` — add `Baseline` tunable.

**Removed (old runtime-config surface):**
- `pkg/engine/configurable.go`, `pkg/engine/configurable_test.go`
- `pkg/engine/builtins_config.go`
- config fields/handling in `pkg/engine/builtins.go`
- `ConsoleOpts` config fields + wiring in `pkg/universe/coordinator.go`
- config aliases in `pkg/mmokit/mmokit.go`
- `RegisterBuiltins(BuiltinOpts{Config:…})` block in `cmd/server/main.go`
- config cases in `pkg/engine/console_cmdsys_test.go` / completion tests

---

## Phase A — `pkg/tunable` foundation

### Task A1: Kind, Def, tag parser, float conversions

**Files:**
- Create: `pkg/tunable/tunable.go`
- Test: `pkg/tunable/tunable_test.go`

- [ ] **Step 1: Write the failing test**

```go
package tunable

import "testing"

func TestParseTag(t *testing.T) {
	tag, ok := ParseTag("default=220,min=60,max=420,step=10")
	if !ok {
		t.Fatal("expected tag present")
	}
	if tag.Default != "220" || tag.Min != "60" || tag.Max != "420" || tag.Step != "10" {
		t.Fatalf("bad parse: %+v", tag)
	}
	if _, ok := ParseTag(""); ok {
		t.Fatal("empty tag should report absent")
	}
}

func TestKindOfAndF64RoundTrip(t *testing.T) {
	cases := []struct {
		kind Kind
		in   string
		f    float64
	}{
		{KindFloat, "1.5", 1.5},
		{KindInt, "7", 7},
		{KindUint, "9", 9},
		{KindBool, "true", 1},
	}
	for _, c := range cases {
		got, err := ToF64(c.kind, c.in)
		if err != nil || got != c.f {
			t.Fatalf("ToF64(%v,%q)=%v,%v want %v", c.kind, c.in, got, err, c.f)
		}
		back := FromF64(c.kind, c.f)
		if _, err := ToF64(c.kind, back); err != nil {
			t.Fatalf("FromF64 produced unparseable %q", back)
		}
	}
}

func TestValidateRange(t *testing.T) {
	d := Def{Name: "amp", Kind: KindFloat, Min: "0", Max: "10"}
	if err := d.Validate("5"); err != nil {
		t.Fatalf("5 in [0,10] should pass: %v", err)
	}
	if err := d.Validate("11"); err == nil {
		t.Fatal("11 should fail max")
	}
	if err := d.Validate("-1"); err == nil {
		t.Fatal("-1 should fail min")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tunable/`
Expected: FAIL — undefined `ParseTag`/`Kind`/`Def`/`ToF64`/`FromF64`.

- [ ] **Step 3: Write the implementation**

```go
// Package tunable is the zero-dependency contract for runtime-tunable system
// values. It compiles for both native and GOOS=wasip1 so the host and the wasm
// guest parse the `tune:` struct tag identically. A tunable is an exported
// scalar struct field tagged `tune:"default=...,min=...,max=...,step=..."`.
package tunable

import (
	"fmt"
	"strconv"
	"strings"
)

// Kind enumerates the supported tunable scalar kinds. Every kind round-trips
// losslessly through a float64 (the wasm value-transport unit).
type Kind uint8

const (
	KindInt Kind = iota
	KindUint
	KindFloat
	KindBool
)

func (k Kind) String() string {
	switch k {
	case KindInt:
		return "int"
	case KindUint:
		return "uint"
	case KindFloat:
		return "float"
	case KindBool:
		return "bool"
	default:
		return "?"
	}
}

// Def describes one tunable: its identity, kind, optional bounds (empty string
// = unset), and current stringified value.
type Def struct {
	Name    string
	Kind    Kind
	Default string
	Min     string
	Max     string
	Step    string
	Value   string
}

// Tag is the parsed contents of a `tune:"..."` struct tag.
type Tag struct {
	Default string
	Min     string
	Max     string
	Step    string
}

// ParseTag parses a tune-tag body ("default=1,min=0,max=2"). The bool is false
// for an empty body (the field is not a tunable).
func ParseTag(body string) (Tag, bool) {
	body = strings.TrimSpace(body)
	if body == "" {
		return Tag{}, false
	}
	var t Tag
	for _, part := range strings.Split(body, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		key, val := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		switch key {
		case "default":
			t.Default = val
		case "min":
			t.Min = val
		case "max":
			t.Max = val
		case "step":
			t.Step = val
		}
	}
	return t, true
}

// ToF64 parses a stringified value of the given kind into a float64 for the
// wasm value block. bool → 0/1.
func ToF64(kind Kind, value string) (float64, error) {
	switch kind {
	case KindInt:
		n, err := strconv.ParseInt(value, 10, 64)
		return float64(n), err
	case KindUint:
		n, err := strconv.ParseUint(value, 10, 64)
		return float64(n), err
	case KindFloat:
		return strconv.ParseFloat(value, 64)
	case KindBool:
		b, err := strconv.ParseBool(value)
		if b {
			return 1, err
		}
		return 0, err
	default:
		return 0, fmt.Errorf("tunable: unknown kind %v", kind)
	}
}

// FromF64 renders a float64 (as carried in the wasm block) back to the kind's
// canonical string form.
func FromF64(kind Kind, v float64) string {
	switch kind {
	case KindInt:
		return strconv.FormatInt(int64(v), 10)
	case KindUint:
		return strconv.FormatUint(uint64(v), 10)
	case KindBool:
		if v != 0 {
			return "true"
		}
		return "false"
	default: // KindFloat
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
}

// Validate checks that value parses for the Def's kind and falls within
// [Min,Max] when those bounds are set.
func (d Def) Validate(value string) error {
	v, err := ToF64(d.Kind, value)
	if err != nil {
		return fmt.Errorf("invalid %s value %q: %w", d.Kind, value, err)
	}
	if d.Min != "" {
		lo, err := ToF64(d.Kind, d.Min)
		if err == nil && v < lo {
			return fmt.Errorf("%s=%s below min %s", d.Name, value, d.Min)
		}
	}
	if d.Max != "" {
		hi, err := ToF64(d.Kind, d.Max)
		if err == nil && v > hi {
			return fmt.Errorf("%s=%s above max %s", d.Name, value, d.Max)
		}
	}
	return nil
}

// Source is the host-side contract for anything exposing tunables — a native
// struct (via Reflect) or the wasm adapter.
type Source interface {
	Tunables() []Def
	Set(name, value string) error
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tunable/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/tunable/tunable.go pkg/tunable/tunable_test.go
git commit -m "feat(tunable): Kind/Def/tag-parser/float-conv foundation"
```

---

### Task A2: Reflection-backed Source for native structs

**Files:**
- Create: `pkg/tunable/reflect.go`
- Test: `pkg/tunable/reflect_test.go`

- [ ] **Step 1: Write the failing test**

```go
package tunable

import "testing"

type knobs struct {
	Amplitude float32 `tune:"default=220,min=60,max=420,step=10"`
	Count     int     `tune:"default=3,min=1,max=9"`
	Enabled   bool    `tune:"default=true"`
	private   float32 // untagged + unexported → ignored
	Untagged  float32 // exported but untagged → ignored
}

func TestReflectSourceListAndSet(t *testing.T) {
	k := &knobs{}
	if !HasTunables(k) {
		t.Fatal("knobs should report tunables")
	}
	src := Reflect(k)
	src.(interface{ ApplyDefaults() }).ApplyDefaults()

	if k.Amplitude != 220 || k.Count != 3 || !k.Enabled {
		t.Fatalf("defaults not applied: %+v", k)
	}
	defs := src.Tunables()
	if len(defs) != 3 {
		t.Fatalf("want 3 tunables (private/untagged excluded), got %d: %+v", len(defs), defs)
	}
	if defs[0].Name != "Amplitude" || defs[0].Kind != KindFloat || defs[0].Value != "220" {
		t.Fatalf("bad first def: %+v", defs[0])
	}

	if err := src.Set("Amplitude", "300"); err != nil {
		t.Fatal(err)
	}
	if k.Amplitude != 300 {
		t.Fatalf("set did not write field: %v", k.Amplitude)
	}
	if err := src.Set("Amplitude", "999"); err == nil {
		t.Fatal("out-of-range set should fail")
	}
	if err := src.Set("Nope", "1"); err == nil {
		t.Fatal("unknown field should fail")
	}
}

func TestHasTunablesFalse(t *testing.T) {
	type plain struct{ X float32 }
	if HasTunables(&plain{}) {
		t.Fatal("untagged struct should report no tunables")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tunable/ -run Reflect`
Expected: FAIL — undefined `Reflect`/`HasTunables`.

- [ ] **Step 3: Write the implementation**

```go
package tunable

import (
	"fmt"
	"reflect"
)

// field binds a Def to the reflect index path of the live struct field.
type field struct {
	def   Def
	index int
}

type reflectSource struct {
	v      reflect.Value // the addressable struct value
	fields []field
}

// kindOf maps a reflect.Kind to a tunable.Kind, reporting false for
// unsupported kinds (so they're skipped rather than panicking).
func kindOf(k reflect.Kind) (Kind, bool) {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return KindInt, true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return KindUint, true
	case reflect.Float32, reflect.Float64:
		return KindFloat, true
	case reflect.Bool:
		return KindBool, true
	default:
		return 0, false
	}
}

// scan walks ptr's struct fields collecting tagged, exported, scalar tunables.
func scan(ptr any) (reflect.Value, []field) {
	rv := reflect.ValueOf(ptr)
	if rv.Kind() != reflect.Ptr || rv.Elem().Kind() != reflect.Struct {
		panic(fmt.Sprintf("tunable: Reflect requires a struct pointer, got %T", ptr))
	}
	elem := rv.Elem()
	t := elem.Type()
	var fields []field
	for i := range t.NumField() {
		sf := t.Field(i)
		if sf.PkgPath != "" { // unexported
			continue
		}
		body, ok := sf.Tag.Lookup("tune")
		if !ok {
			continue
		}
		tag, present := ParseTag(body)
		if !present {
			continue
		}
		kind, ok := kindOf(sf.Type.Kind())
		if !ok {
			continue
		}
		fields = append(fields, field{
			def: Def{
				Name: sf.Name, Kind: kind,
				Default: tag.Default, Min: tag.Min, Max: tag.Max, Step: tag.Step,
			},
			index: i,
		})
	}
	return elem, fields
}

// HasTunables reports whether ptr (a struct pointer) has any tune-tagged fields.
func HasTunables(ptr any) bool {
	rv := reflect.ValueOf(ptr)
	if rv.Kind() != reflect.Ptr || rv.Elem().Kind() != reflect.Struct {
		return false
	}
	_, fields := scan(ptr)
	return len(fields) > 0
}

// Reflect wraps a struct pointer as a Source over its tune-tagged fields.
func Reflect(ptr any) Source {
	v, fields := scan(ptr)
	return &reflectSource{v: v, fields: fields}
}

func (r *reflectSource) Tunables() []Def {
	out := make([]Def, len(r.fields))
	for i, f := range r.fields {
		d := f.def
		d.Value = r.read(f)
		out[i] = d
	}
	return out
}

func (r *reflectSource) Set(name, value string) error {
	for _, f := range r.fields {
		if f.def.Name != name {
			continue
		}
		if err := f.def.Validate(value); err != nil {
			return err
		}
		return r.write(f, value)
	}
	return fmt.Errorf("tunable: unknown field %q", name)
}

// ApplyDefaults writes each field's tag default (skipping fields without one).
func (r *reflectSource) ApplyDefaults() {
	for _, f := range r.fields {
		if f.def.Default == "" {
			continue
		}
		_ = r.write(f, f.def.Default)
	}
}

func (r *reflectSource) read(f field) string {
	fv := r.v.Field(f.index)
	switch f.def.Kind {
	case KindInt:
		return FromF64(KindInt, float64(fv.Int()))
	case KindUint:
		return FromF64(KindUint, float64(fv.Uint()))
	case KindBool:
		return FromF64(KindBool, boolF(fv.Bool()))
	default:
		return FromF64(KindFloat, fv.Float())
	}
}

func (r *reflectSource) write(f field, value string) error {
	v, err := ToF64(f.def.Kind, value)
	if err != nil {
		return err
	}
	fv := r.v.Field(f.index)
	switch f.def.Kind {
	case KindInt:
		fv.SetInt(int64(v))
	case KindUint:
		fv.SetUint(uint64(v))
	case KindBool:
		fv.SetBool(v != 0)
	default:
		fv.SetFloat(v)
	}
	return nil
}

func boolF(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tunable/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/tunable/reflect.go pkg/tunable/reflect_test.go
git commit -m "feat(tunable): reflection-backed Source for native structs"
```

---

## Phase B — wasm ABI: schema + value block

### Task B1: wasmabi export names + schema/block codec

**Files:**
- Modify: `pkg/wasmabi/abi.go`
- Test: `pkg/wasmabi/abi_test.go` (create)

- [ ] **Step 1: Write the failing test**

```go
package wasmabi

import "testing"

func TestSchemaRoundTrip(t *testing.T) {
	in := []ParamField{
		{Name: "Amplitude", Kind: 2, Default: 220, Min: 60, Max: 420, Step: 10, BoundsMask: BoundDefault | BoundMin | BoundMax | BoundStep},
		{Name: "Enabled", Kind: 3, Default: 1, BoundsMask: BoundDefault},
	}
	out, err := DecodeSchema(EncodeSchema(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Name != "Amplitude" || out[0].Max != 420 || out[1].Kind != 3 {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestBlockRoundTrip(t *testing.T) {
	in := []float64{1, 2.5, 0}
	out, err := DecodeBlock(EncodeBlock(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 || out[1] != 2.5 {
		t.Fatalf("block round-trip mismatch: %+v", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/wasmabi/`
Expected: FAIL — undefined `ParamField`/`EncodeSchema`/`DecodeSchema`/`EncodeBlock`/`DecodeBlock`.

- [ ] **Step 3: Add to `pkg/wasmabi/abi.go`**

Append these declarations (keep existing content; add to the export-name const block and add new code at the end):

```go
// Params exports (optional, nil-safe — like snapshot/restore, so adding them
// needs NO ABIVersion bump and modules without tunables keep working).
const (
	ExportParamsSchema = "wasmsys_params_schema" // () -> (ptr<<32 | len) u64
	ExportParamsSet    = "wasmsys_params_set"    // (ptr u32, len u32)
)

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
	Name                     string
	Kind                     uint8
	Default, Min, Max, Step  float64
	BoundsMask               uint8
}

// EncodeSchema serializes a module's param schema. Layout:
//   u16 count, then per field:
//   u8 nameLen, name bytes, u8 kind, 4×f64 (default,min,max,step LE), u8 mask.
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
	for i := 0; i < int(n); i++ {
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
```

Also add the small LE helpers + reader at the end of the file (no new imports — hand-rolled to keep wasmabi dependency-free):

```go
import "errors" // ADD to the existing import block

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
	for i := 0; i < 8; i++ {
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
```

Add `math`-free float bit conversion helpers (wasmabi already imports `unsafe`):

```go
func f64bits(f float64) uint64 { return *(*uint64)(unsafe.Pointer(&f)) }
func f64from(u uint64) float64 { return *(*float64)(unsafe.Pointer(&u)) }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/wasmabi/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/wasmabi/
git commit -m "feat(wasmabi): param schema + float64 value-block codec"
```

---

### Task B2: Guest SDK — schema build, default-apply, params_set scatter

**Files:**
- Modify: `pkg/wasmsys/sdk.go` (params reflection, shared native+wasip1)
- Modify: `pkg/wasmsys/exports.go` (the two new wasmexports, wasip1-only)

- [ ] **Step 1: Add guest param plumbing to `pkg/wasmsys/sdk.go`**

Add imports `reflect` and keep `github.com/zenion/mmokit/pkg/tunable` + `github.com/zenion/mmokit/pkg/wasmabi`. Append:

```go
// paramField binds a guest struct field to its tunable kind for the params ABI.
type paramField struct {
	index int
	kind  tunable.Kind
}

// paramSet is the guest-side reflection over the registered system's
// tune-tagged fields. Built once in Register; reused by the params exports.
type paramSet struct {
	v      reflect.Value // addressable system struct
	fields []paramField
	schema []wasmabi.ParamField
}

// buildParamSet reflects sys (a struct pointer) into a paramSet, applies tag
// defaults to the live fields, and precomputes the wire schema.
func buildParamSet(sys any) *paramSet {
	rv := reflect.ValueOf(sys)
	if rv.Kind() != reflect.Ptr || rv.Elem().Kind() != reflect.Struct {
		return &paramSet{}
	}
	src := tunable.Reflect(sys)
	if ad, ok := src.(interface{ ApplyDefaults() }); ok {
		ad.ApplyDefaults() // fields hold correct values before any host push
	}
	elem := rv.Elem()
	t := elem.Type()
	ps := &paramSet{v: elem}
	for i := range t.NumField() {
		sf := t.Field(i)
		if sf.PkgPath != "" {
			continue
		}
		body, ok := sf.Tag.Lookup("tune")
		if !ok {
			continue
		}
		tag, present := tunable.ParseTag(body)
		if !present {
			continue
		}
		kind, ok := guestKindOf(sf.Type.Kind())
		if !ok {
			continue
		}
		ps.fields = append(ps.fields, paramField{index: i, kind: kind})
		ps.schema = append(ps.schema, schemaField(sf.Name, kind, tag))
	}
	return ps
}

func schemaField(name string, kind tunable.Kind, tag tunable.Tag) wasmabi.ParamField {
	pf := wasmabi.ParamField{Name: name, Kind: uint8(kind)}
	if tag.Default != "" {
		if v, err := tunable.ToF64(kind, tag.Default); err == nil {
			pf.Default, pf.BoundsMask = v, pf.BoundsMask|wasmabi.BoundDefault
		}
	}
	if tag.Min != "" {
		if v, err := tunable.ToF64(kind, tag.Min); err == nil {
			pf.Min, pf.BoundsMask = v, pf.BoundsMask|wasmabi.BoundMin
		}
	}
	if tag.Max != "" {
		if v, err := tunable.ToF64(kind, tag.Max); err == nil {
			pf.Max, pf.BoundsMask = v, pf.BoundsMask|wasmabi.BoundMax
		}
	}
	if tag.Step != "" {
		if v, err := tunable.ToF64(kind, tag.Step); err == nil {
			pf.Step, pf.BoundsMask = v, pf.BoundsMask|wasmabi.BoundStep
		}
	}
	return pf
}

func guestKindOf(k reflect.Kind) (tunable.Kind, bool) {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return tunable.KindInt, true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return tunable.KindUint, true
	case reflect.Float32, reflect.Float64:
		return tunable.KindFloat, true
	case reflect.Bool:
		return tunable.KindBool, true
	default:
		return 0, false
	}
}

// scatter writes a host value block into the live struct fields, in schema order.
func (ps *paramSet) scatter(block []float64) {
	for i, f := range ps.fields {
		if i >= len(block) {
			return
		}
		fv := ps.v.Field(f.index)
		switch f.kind {
		case tunable.KindInt:
			fv.SetInt(int64(block[i]))
		case tunable.KindUint:
			fv.SetUint(uint64(block[i]))
		case tunable.KindBool:
			fv.SetBool(block[i] != 0)
		default:
			fv.SetFloat(block[i])
		}
	}
}
```

- [ ] **Step 2: Wire the paramSet into Register + add exports in `pkg/wasmsys/exports.go`**

In `exports.go`, add a package var and build it in `Register`, then add the two exports:

```go
var paramset *paramSet // built from the registered system in Register

// (modify Register)
func Register(s System) {
	registered = s
	paramset = buildParamSet(s)
}

//go:wasmexport wasmsys_params_schema
func wasmParamsSchema() uint64 {
	if paramset == nil || len(paramset.schema) == 0 {
		return 0
	}
	snapBuf = wasmabi.EncodeSchema(paramset.schema) // reuse snapBuf to keep ptr alive
	if len(snapBuf) == 0 {
		return 0
	}
	ptr := uint64(uint32(uintptr(unsafe.Pointer(&snapBuf[0]))))
	return ptr<<32 | uint64(uint32(len(snapBuf)))
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
```

Note: `snapBuf` is reused for the schema return — schema and snapshot are never both in flight (host reads schema once at load, snapshots only on swap). Keep `buildParamSet` callable on native (it returns an empty set for non-struct), so `sdk.go` stays build-tag-free.

- [ ] **Step 3: Compile-check both targets**

Run:
```bash
go vet ./pkg/wasmsys/
GOOS=wasip1 GOARCH=wasm go build -o /dev/null ./pkg/wasmsys/
```
Expected: both succeed (no binary left in the tree — `/dev/null` sink).

- [ ] **Step 4: Commit**

```bash
git add pkg/wasmsys/
git commit -m "feat(wasmsys): guest param schema build + default-apply + params_set scatter"
```

---

### Task B3: Host Module — ParamsSchema / ParamsSet

**Files:**
- Modify: `pkg/wasmhost/module.go`
- Test: `pkg/wasmhost/module_params_test.go` (create)
- Fixture: `pkg/wasmhost/internal/parammod/main.go` (create)

- [ ] **Step 1: Create the fixture module `pkg/wasmhost/internal/parammod/main.go`**

```go
//go:build wasip1

// parammod is a wasmhost test fixture: a system with two tunable fields and a
// trivial column, used to exercise the params ABI from the host.
package main

import "github.com/zenion/mmokit/pkg/wasmsys"

type sys struct {
	Gain   float32 `tune:"default=2,min=0,max=10,step=0.5"`
	Enable bool    `tune:"default=true"`
}

func (s *sys) Query() wasmsys.Query { return wasmsys.ReadWrite[float32]() }
func (s *sys) Update(ctx *wasmsys.Ctx, dt float32) {
	col := wasmsys.Column[float32](ctx)
	g := s.Gain
	if !s.Enable {
		g = 0
	}
	for i := range col {
		col[i] *= g
	}
}

func init() { wasmsys.Register(&sys{}) }
func main()  {}
```

- [ ] **Step 2: Write the failing test `pkg/wasmhost/module_params_test.go`**

```go
package wasmhost

import (
	"context"
	"testing"
)

func TestParamsSchemaAndSet(t *testing.T) {
	ctx := context.Background()
	rt := New(ctx)
	defer rt.Close(ctx)
	m, err := Load(ctx, rt, buildModule(t, "internal/parammod"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(ctx)

	fields, err := m.ParamsSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 || fields[0].Name != "Gain" || fields[0].Max != 10 {
		t.Fatalf("bad schema: %+v", fields)
	}
	// Gain default 2 → column doubles.
	col := []float32{1, 2, 3}
	in := f32bytes(col)
	out, err := m.Update(ctx, 3, 0.1, in)
	if err != nil {
		t.Fatal(err)
	}
	if f32at(out, 0) != 2 || f32at(out, 2) != 6 {
		t.Fatalf("default gain not applied: %v", out)
	}
	// Push Gain=3, Enable=true → triples.
	if err := m.ParamsSet(ctx, []float64{3, 1}); err != nil {
		t.Fatal(err)
	}
	out, _ = m.Update(ctx, 3, 0.1, f32bytes(col))
	if f32at(out, 1) != 6 {
		t.Fatalf("pushed gain not applied: %v", out)
	}
}
```

Add tiny float32 byte helpers in the same test file:

```go
import "unsafe"

func f32bytes(c []float32) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(&c[0])), len(c)*4)
}
func f32at(b []byte, i int) float32 {
	return *(*float32)(unsafe.Pointer(&b[i*4]))
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./pkg/wasmhost/ -run Params`
Expected: FAIL — undefined `ParamsSchema`/`ParamsSet`.

- [ ] **Step 4: Add methods to `pkg/wasmhost/module.go`**

Add the two function fields to the `Module` struct + bind them in `Load` (non-fatal if absent), then the methods:

```go
// in struct Module add:
//   paramsSchema api.Function
//   paramsSet    api.Function

// in Load(), after binding snapshot/restore:
m.paramsSchema = mod.ExportedFunction(wasmabi.ExportParamsSchema)
m.paramsSet = mod.ExportedFunction(wasmabi.ExportParamsSet)

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
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./pkg/wasmhost/ -run Params`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/wasmhost/module.go pkg/wasmhost/module_params_test.go pkg/wasmhost/internal/parammod/
git commit -m "feat(wasmhost): Module.ParamsSchema/ParamsSet + parammod fixture"
```

---

## Phase C — mmokit integration

### Task C1: `wasmSystem[T]` implements `tunable.Source`

**Files:**
- Modify: `pkg/mmokit/wasm_system.go`

- [ ] **Step 1: Add params state + harvest to the adapter**

Add fields to `wasmSystem[T]`:

```go
schema []wasmabi.ParamField // harvested once at Init
defs   []tunable.Def        // cached descriptor list (kind/bounds)
values map[string]float64   // current values by field name (source of truth for this instance)
```

In `Init()`, after registering the log category, harvest the schema:

```go
func (s *wasmSystem[T]) Init() {
	s.Stage().Engine().Log.RegisterCategories(catWasmSystem)
	s.harvestParams()
}

func (s *wasmSystem[T]) harvestParams() {
	fields, err := s.mod.ParamsSchema(context.Background())
	if err != nil || len(fields) == 0 {
		return
	}
	s.schema = fields
	s.values = make(map[string]float64, len(fields))
	s.defs = make([]tunable.Def, len(fields))
	for i, f := range fields {
		kind := tunable.Kind(f.Kind)
		d := tunable.Def{Name: f.Name, Kind: kind}
		if f.BoundsMask&wasmabi.BoundDefault != 0 {
			d.Default = tunable.FromF64(kind, f.Default)
		}
		if f.BoundsMask&wasmabi.BoundMin != 0 {
			d.Min = tunable.FromF64(kind, f.Min)
		}
		if f.BoundsMask&wasmabi.BoundMax != 0 {
			d.Max = tunable.FromF64(kind, f.Max)
		}
		if f.BoundsMask&wasmabi.BoundStep != 0 {
			d.Step = tunable.FromF64(kind, f.Step)
		}
		s.defs[i] = d
		s.values[f.Name] = f.Default // guest already applied defaults at init
	}
}
```

- [ ] **Step 2: Implement `tunable.Source` on the adapter**

```go
// Tunables reports the module's current tunable values (this instance's view).
func (s *wasmSystem[T]) Tunables() []tunable.Def {
	out := make([]tunable.Def, len(s.defs))
	for i, d := range s.defs {
		d.Value = tunable.FromF64(d.Kind, s.values[d.Name])
		out[i] = d
	}
	return out
}

// Set validates, updates this instance's value, and pushes the full block to
// the guest (params_set replaces the whole block, so we always send all).
func (s *wasmSystem[T]) Set(name, value string) error {
	for i := range s.defs {
		if s.defs[i].Name != name {
			continue
		}
		if err := s.defs[i].Validate(value); err != nil {
			return err
		}
		v, _ := tunable.ToF64(s.defs[i].Kind, value)
		s.values[name] = v
		return s.pushBlock()
	}
	return fmt.Errorf("wasm module has no tunable %q", name)
}

func (s *wasmSystem[T]) pushBlock() error {
	block := make([]float64, len(s.schema))
	for i, f := range s.schema {
		block[i] = s.values[f.Name]
	}
	return s.mod.ParamsSet(context.Background(), block)
}
```

Add imports `github.com/zenion/mmokit/pkg/tunable` and ensure `wasmabi` is imported. Add the compile-time assertion:

```go
var _ tunable.Source = (*wasmSystem[struct{}])(nil)
```

- [ ] **Step 3: Compile-check**

Run: `go vet ./pkg/mmokit/`
Expected: success.

- [ ] **Step 4: Commit**

```bash
git add pkg/mmokit/wasm_system.go
git commit -m "feat(mmokit): wasmSystem implements tunable.Source via params ABI bridge"
```

---

### Task C2: loop named-system iterator

**Files:**
- Modify: `pkg/engine/loop.go`
- Test: `pkg/engine/loop_eachsystem_test.go` (create)

- [ ] **Step 1: Write the failing test**

```go
package engine

import "testing"

func TestEachSystem(t *testing.T) {
	gl := NewGameLoop(nil, []System{&SystemBase{}, &SystemBase{}}, []string{"A", "B"}, Hooks{})
	var got []string
	gl.EachSystem(func(name string, _ System) { got = append(got, name) })
	if len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Fatalf("EachSystem order/names wrong: %v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/engine/ -run EachSystem`
Expected: FAIL — undefined `EachSystem`.

- [ ] **Step 3: Add to `pkg/engine/loop.go`**

```go
// EachSystem calls fn for every system in tick order with its registered name
// (parallel to systemNames). Call on the loop goroutine.
func (gl *GameLoop) EachSystem(fn func(name string, s System)) {
	for i, s := range gl.systems {
		name := ""
		if i < len(gl.systemNames) {
			name = gl.systemNames[i]
		}
		fn(name, s)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/engine/ -run EachSystem`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/engine/loop.go pkg/engine/loop_eachsystem_test.go
git commit -m "feat(engine): GameLoop.EachSystem named-system iterator"
```

---

### Task C3: per-process tunable registry + resolution + cell sync

**Files:**
- Create: `pkg/mmokit/tunable_registry.go`
- Test: `pkg/mmokit/tunable_registry_test.go`

- [ ] **Step 1: Write the failing test**

```go
package mmokit

import "testing"

type tuneNativeSys struct {
	SystemBase
	Gain float32 `tune:"default=2,min=0,max=10"`
}

func (s *tuneNativeSys) Update(dt float32) {}

func TestTunableSourceForNativeAndRegistry(t *testing.T) {
	sys := &tuneNativeSys{}
	src, ok := tunableSourceFor(sys)
	if !ok {
		t.Fatal("native tagged system should resolve to a Source")
	}
	if err := src.Set("Gain", "5"); err != nil {
		t.Fatal(err)
	}
	if sys.Gain != 5 {
		t.Fatalf("set did not write field: %v", sys.Gain)
	}

	reg := newTuneRegistry()
	reg.harvest("Field", src.Tunables())
	reg.setValue("Field", "Gain", "7")
	if v, _ := reg.value("Field", "Gain"); v != "7" {
		t.Fatalf("registry value = %q want 7", v)
	}
	defs := reg.defs("Field")
	if len(defs) != 1 || defs[0].Value != "7" {
		t.Fatalf("registry defs wrong: %+v", defs)
	}
}

func TestTunableSourceForNone(t *testing.T) {
	if _, ok := tunableSourceFor(NewPhysicsSystem().Factory()); ok {
		t.Fatal("physics system has no tunables")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/mmokit/ -run Tunable`
Expected: FAIL — undefined `tunableSourceFor`/`newTuneRegistry`.

- [ ] **Step 3: Implement `pkg/mmokit/tunable_registry.go`**

```go
package mmokit

import (
	"sort"
	"sync"

	"github.com/zenion/mmokit/pkg/engine"
	"github.com/zenion/mmokit/pkg/tunable"
	"github.com/zenion/mmokit/pkg/universe"
)

// tunableSourceFor resolves a system to a tunable.Source: the wasm adapter
// implements Source directly; a native struct with tune-tagged fields is
// wrapped via reflection; anything else reports false.
func tunableSourceFor(sys engine.System) (tunable.Source, bool) {
	if s, ok := sys.(tunable.Source); ok {
		return s, true
	}
	if tunable.HasTunables(sys) {
		return tunable.Reflect(sys), true
	}
	return nil, false
}

// tuneSystem holds the descriptors + intended values for one named system.
type tuneSystem struct {
	defs   []tunable.Def     // descriptor order (kind/bounds/default)
	values map[string]string // field → current intended value
}

// tuneRegistry is the per-Process source of truth for intended tunable values.
type tuneRegistry struct {
	mu      sync.Mutex
	systems map[string]*tuneSystem
}

func newTuneRegistry() *tuneRegistry {
	return &tuneRegistry{systems: map[string]*tuneSystem{}}
}

// harvest records descriptors for a system the first time it is seen, seeding
// intended values from each descriptor's current Value.
func (r *tuneRegistry) harvest(name string, defs []tunable.Def) {
	if len(defs) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.systems[name]; ok {
		return
	}
	ts := &tuneSystem{values: map[string]string{}}
	for _, d := range defs {
		ts.defs = append(ts.defs, d)
		ts.values[d.Name] = d.Value
	}
	r.systems[name] = ts
}

func (r *tuneRegistry) setValue(name, field, value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ts, ok := r.systems[name]; ok {
		ts.values[field] = value
	}
}

func (r *tuneRegistry) value(name, field string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ts, ok := r.systems[name]
	if !ok {
		return "", false
	}
	v, ok := ts.values[field]
	return v, ok
}

// defs returns descriptor copies with Value set to the registry's current
// intended value.
func (r *tuneRegistry) defs(name string) []tunable.Def {
	r.mu.Lock()
	defer r.mu.Unlock()
	ts, ok := r.systems[name]
	if !ok {
		return nil
	}
	out := make([]tunable.Def, len(ts.defs))
	for i, d := range ts.defs {
		d.Value = ts.values[d.Name]
		out[i] = d
	}
	return out
}

// systemNames returns the sorted set of registered tunable system names.
func (r *tuneRegistry) systemNames() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.systems))
	for k := range r.systems {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// snapshotValues returns a copy of a system's intended values.
func (r *tuneRegistry) snapshotValues(name string) map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ts, ok := r.systems[name]
	if !ok {
		return nil
	}
	out := make(map[string]string, len(ts.values))
	for k, v := range ts.values {
		out[k] = v
	}
	return out
}

// ── per-Process registry map ────────────────────────────────────────────────

var (
	tuneRegMu  sync.Mutex
	tuneRegMap = map[*universe.Process]*tuneRegistry{}
)

func tuneRegistryFor(p *universe.Process) *tuneRegistry {
	tuneRegMu.Lock()
	defer tuneRegMu.Unlock()
	r, ok := tuneRegMap[p]
	if !ok {
		r = newTuneRegistry()
		tuneRegMap[p] = r
	}
	return r
}

// SyncCellTunables harvests every tunable system in the cell into the process
// registry (once per system name) and applies the registry's intended values
// to each live instance. Runs on the cell's loop goroutine — call inside
// RunOnLoop or from a loop-safe context (cell bootstrap / post-set).
func SyncCellTunables(p *universe.Process, cell *universe.Cell) {
	reg := tuneRegistryFor(p)
	cell.Loop.EachSystem(func(name string, sys engine.System) {
		src, ok := tunableSourceFor(sys)
		if !ok {
			return
		}
		reg.harvest(name, src.Tunables())
		for field, val := range reg.snapshotValues(name) {
			_ = src.Set(name2field(field), val)
		}
	})
}

// name2field is an identity helper kept for readability at the call site
// (registry keys are field names already).
func name2field(field string) string { return field }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/mmokit/ -run Tunable`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/tunable_registry.go pkg/mmokit/tunable_registry_test.go
git commit -m "feat(mmokit): per-process tunable registry + source resolution + cell sync"
```

---

### Task C4: `tune.*` cmdsys verbs + SSE publish

**Files:**
- Create: `pkg/mmokit/tunable_verbs.go`
- Modify: `pkg/mmokit/mmokit.go` (call `registerTuneVerbs(proc)` in `New`)
- Test: `pkg/mmokit/tunable_verbs_test.go`

- [ ] **Step 1: Write the failing test**

```go
package mmokit

import (
	"context"
	"testing"

	"github.com/zenion/mmokit/pkg/cmdsys"
)

func TestTuneVerbsRegistered(t *testing.T) {
	proc := New(Config{Name: "tunetest", Headless: true})
	for _, verb := range []string{"tune.list", "tune.get", "tune.set", "tune.reset"} {
		if _, ok := proc.CmdRegistry().Lookup(verb); !ok {
			t.Fatalf("verb %q not registered", verb)
		}
	}
	_ = context.Background
	_ = cmdsys.RouteAllHosts
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/mmokit/ -run TuneVerbs`
Expected: FAIL — verbs not registered (`registerTuneVerbs` not wired).

- [ ] **Step 3: Implement `pkg/mmokit/tunable_verbs.go`**

```go
package mmokit

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/zenion/mmokit/pkg/cmdsys"
	"github.com/zenion/mmokit/pkg/tunable"
	"github.com/zenion/mmokit/pkg/universe"
)

const catTune = "tune"

// tuneSetArgs: required system+field+value positional, optional cell/node filters.
type tuneSetArgs struct {
	System string `cmd:"help=system name"`
	Field  string `cmd:"help=tunable field name"`
	Value  string `cmd:"help=new value"`
	Node   string `cmd:"optional,name=node,help=limit to one host id,complete=hosts"`
	Cell   string `cmd:"optional,name=cell,help=limit to one cell id,complete=cells"`
}

type tuneGetArgs struct {
	System string `cmd:"help=system name"`
	Field  string `cmd:"help=tunable field name"`
}

type tuneResetArgs struct {
	System string `cmd:"help=system name"`
	Field  string `cmd:"optional,help=field to reset (default: all fields)"`
	Node   string `cmd:"optional,name=node,help=limit to one host id,complete=hosts"`
	Cell   string `cmd:"optional,name=cell,help=limit to one cell id,complete=cells"`
}

type tuneListArgs struct {
	System string `cmd:"optional,name=system,help=limit to one system"`
	Node   string `cmd:"optional,name=node,help=limit to one host id,complete=hosts"`
	Cell   string `cmd:"optional,name=cell,help=limit to one cell id,complete=cells"`
}

type tuneRow struct {
	System  string
	Field   string
	Value   string
	Default string
	Min     string
	Max     string
	Kind    string
}

type tuneResult struct {
	Rows []tuneRow `cmd:"table"`
}

type tuneGetResult struct {
	System string
	Field  string
	Value  string
}

// registerTuneVerbs registers tune.list/get/set/reset on the process registry.
// All RouteAllHosts: fan out to every host, each iterating its local cells.
func registerTuneVerbs(proc *universe.Process) error {
	reg := proc.CmdRegistry()
	proc.Log.RegisterCategories(catTune)

	targetCells := func(node, cell string) []*universe.Cell {
		var wantCell string
		if cell != "" {
			if canon, err := ParseCellID(cell); err == nil {
				wantCell = string(canon.MeshID())
			} else {
				wantCell = cell
			}
		}
		var out []*universe.Cell
		for id, c := range proc.Cells {
			if node != "" && proc.HostIDForCell(c) != node {
				continue
			}
			if cell != "" && string(id) != wantCell {
				continue
			}
			out = append(out, c)
		}
		return out
	}

	// applySet pushes (system,field,value) to every matching cell's live Source.
	applySet := func(ctx context.Context, system, field, value string, node, cell string) error {
		for _, c := range targetCells(node, cell) {
			cc := c
			if _, err := CmdOnLoop(ctx, cc.Engine, func() (struct{}, error) {
				sys, ok := cc.Loop.SystemByName(system)
				if !ok {
					return struct{}{}, nil
				}
				src, ok := tunableSourceFor(sys)
				if !ok {
					return struct{}{}, nil
				}
				if err := src.Set(field, value); err != nil {
					return struct{}{}, err
				}
				return struct{}{}, nil
			}); err != nil {
				return err
			}
		}
		return nil
	}

	if err := reg.Register(cmdsys.Command{
		Verb: "tune.set", Capability: "tune.set",
		Description: "set a system tunable at runtime (default: all cells/all nodes)",
		Examples:    []string{"tune set wave amplitude 420", "tune set wave amplitude 300 --cell 0_0"},
		Route:       cmdsys.RouteAllHosts, Args: tuneSetArgs{}, Result: tuneResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			a := raw.(tuneSetArgs)
			reg := tuneRegistryFor(proc)
			defs := reg.defs(a.System)
			d, ok := findDef(defs, a.Field)
			if !ok {
				return nil, fmt.Errorf("system %q has no tunable %q (known: %s)", a.System, a.Field, fieldNames(defs))
			}
			if err := d.Validate(a.Value); err != nil {
				return nil, err
			}
			// Unfiltered set rewrites the registry (new cells inherit it);
			// a --cell/--node set is an ephemeral live-only override.
			if a.Cell == "" && a.Node == "" {
				reg.setValue(a.System, a.Field, a.Value)
			}
			if err := applySet(ctx, a.System, a.Field, a.Value, a.Node, a.Cell); err != nil {
				return nil, err
			}
			publishTunables(proc, a.System)
			proc.Log.Log(catTune, "tune set %s.%s=%s (cell=%q node=%q)", a.System, a.Field, a.Value, a.Cell, a.Node)
			return tuneResult{Rows: rowsFor(reg, a.System)}, nil
		},
	}); err != nil {
		return fmt.Errorf("tune.set: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb: "tune.get", Capability: "tune.get",
		Description: "show one system tunable's current value",
		Examples:    []string{"tune get wave amplitude"},
		Route:       cmdsys.RouteLocal, Args: tuneGetArgs{}, Result: tuneGetResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			a := raw.(tuneGetArgs)
			v, ok := tuneRegistryFor(proc).value(a.System, a.Field)
			if !ok {
				return nil, fmt.Errorf("no tunable %s.%s", a.System, a.Field)
			}
			return tuneGetResult{System: a.System, Field: a.Field, Value: v}, nil
		},
	}); err != nil {
		return fmt.Errorf("tune.get: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb: "tune.reset", Capability: "tune.reset",
		Description: "reset a system tunable (or all its tunables) to tag defaults",
		Examples:    []string{"tune reset wave", "tune reset wave amplitude"},
		Route:       cmdsys.RouteAllHosts, Args: tuneResetArgs{}, Result: tuneResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			a := raw.(tuneResetArgs)
			reg := tuneRegistryFor(proc)
			for _, d := range reg.defs(a.System) {
				if a.Field != "" && d.Name != a.Field {
					continue
				}
				if d.Default == "" {
					continue
				}
				if a.Cell == "" && a.Node == "" {
					reg.setValue(a.System, d.Name, d.Default)
				}
				if err := applySet(ctx, a.System, d.Name, d.Default, a.Node, a.Cell); err != nil {
					return nil, err
				}
			}
			publishTunables(proc, a.System)
			return tuneResult{Rows: rowsFor(reg, a.System)}, nil
		},
	}); err != nil {
		return fmt.Errorf("tune.reset: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb: "tune.list", Capability: "tune.list",
		Description: "list system tunables and current values",
		Examples:    []string{"tune list", "tune list --system wave"},
		Route:       cmdsys.RouteLocal, Args: tuneListArgs{}, Result: tuneResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			a := raw.(tuneListArgs)
			reg := tuneRegistryFor(proc)
			var rows []tuneRow
			names := reg.systemNames()
			if a.System != "" {
				names = []string{a.System}
			}
			for _, name := range names {
				rows = append(rows, rowsFor(reg, name)...)
			}
			return tuneResult{Rows: rows}, nil
		},
	}); err != nil {
		return fmt.Errorf("tune.list: %w", err)
	}

	return nil
}

func rowsFor(reg *tuneRegistry, system string) []tuneRow {
	defs := reg.defs(system)
	rows := make([]tuneRow, 0, len(defs))
	for _, d := range defs {
		rows = append(rows, tuneRow{
			System: system, Field: d.Name, Value: d.Value,
			Default: d.Default, Min: d.Min, Max: d.Max, Kind: d.Kind.String(),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Field < rows[j].Field })
	return rows
}

func findDef(defs []tunable.Def, field string) (tunable.Def, bool) {
	for _, d := range defs {
		if d.Name == field {
			return d, true
		}
	}
	return tunable.Def{}, false
}

func fieldNames(defs []tunable.Def) string {
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	return strings.Join(names, ", ")
}
```

Note: `proc.Log()` — confirm the accessor name in `universe.Process`; if it differs (e.g. `proc.Logger()`), use that. `publishTunables` is defined in Task E1; add a temporary stub now so this package compiles:

```go
// publishTunables is defined in api_tunables wiring (Task E1). Stub until then.
func publishTunables(p *universe.Process, system string) {}
```

(Replace the stub in Task E1.)

- [ ] **Step 4: Wire into `mmokit.New`**

In `pkg/mmokit/mmokit.go`, find where `registerWasmVerbs(proc)` is called in `New` and add immediately after it:

```go
if err := registerTuneVerbs(proc); err != nil {
	panic(fmt.Sprintf("mmokit.New: registerTuneVerbs: %v", err))
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./pkg/mmokit/ -run TuneVerbs`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/mmokit/tunable_verbs.go pkg/mmokit/mmokit.go pkg/mmokit/tunable_verbs_test.go
git commit -m "feat(mmokit): tune.list/get/set/reset cmdsys verbs"
```

---

### Task C5: call SyncCellTunables at cell bootstrap (boot + split)

**Files:**
- Modify: `pkg/universe/coordinator.go` (boot cell-init site) and `pkg/universe/cell_transfer_executor.go` (split populate site)

Note: `pkg/universe` cannot import `pkg/mmokit` (mmokit imports universe). So expose the sync as a hook the universe calls. Add a package-level hook var in universe and have mmokit set it.

- [ ] **Step 1: Add a hook in `pkg/universe/coordinator.go`**

```go
// OnCellSystemsReady, if set, is invoked (on the cell's loop goroutine) after a
// cell's systems are constructed and Init'd — boot and split alike. mmokit uses
// it to apply registry tunable values to fresh system instances.
var OnCellSystemsReady func(p *Process, cell *Cell)

func fireCellSystemsReady(p *Process, cell *Cell) {
	if OnCellSystemsReady != nil {
		OnCellSystemsReady(p, cell)
	}
}
```

- [ ] **Step 2: Call it where cells finish system init**

In `pkg/universe/coordinator.go`, after each cell's `Stage.Init()` + system construction completes in the boot path (near `s.cell.Stage.Init()` around the existing init loop), add — on the loop, after the loop is built:

```go
fireCellSystemsReady(c, s.cell)
```

In `pkg/universe/cell_transfer_executor.go`, after `node.Stage.Init()` (line ~426) where a split/transfer-created cell is populated, add:

```go
fireCellSystemsReady(node.Stage.Process(), node)
```

(Use the `*Process` + `*Cell` in scope at each site; the grep `grep -n "Stage.Init()" pkg/universe/*.go` locates them. If a site lacks a ready `*Cell`, wrap with the cell variable already present in that scope.)

- [ ] **Step 3: Set the hook from mmokit**

In `pkg/mmokit/tunable_registry.go`, add an `init()`:

```go
func init() {
	universe.OnCellSystemsReady = func(p *universe.Process, cell *universe.Cell) {
		SyncCellTunables(p, cell)
	}
}
```

- [ ] **Step 4: Compile-check + full package tests**

Run:
```bash
go vet ./pkg/universe/ ./pkg/mmokit/
go test ./pkg/mmokit/ -run Tunable
```
Expected: success.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/coordinator.go pkg/universe/cell_transfer_executor.go pkg/mmokit/tunable_registry.go
git commit -m "feat(universe): OnCellSystemsReady hook → apply registry tunables on cell (re)create"
```

---

### Task C6: end-to-end native + wasm tunable integration test

**Files:**
- Create: `pkg/mmokit/internal/testmods/wavetune/main.go`
- Test: `pkg/mmokit/tunable_e2e_test.go`

- [ ] **Step 1: Create the wasm fixture `pkg/mmokit/internal/testmods/wavetune/main.go`**

```go
//go:build wasip1

// wavetune is an mmokit test fixture: a Position-column system with one tunable
// (Offset) added to every entity's Y. Used to verify the full host↔guest
// tunable bridge through the tune.* verbs.
package main

import (
	comp "github.com/zenion/mmokit/pkg/component"
	"github.com/zenion/mmokit/pkg/wasmsys"
)

type wavetune struct {
	Offset float32 `tune:"default=10,min=0,max=100,step=5"`
}

func (w *wavetune) Query() wasmsys.Query { return wasmsys.ReadWrite[comp.Position]() }
func (w *wavetune) Update(ctx *wasmsys.Ctx, dt float32) {
	pos := wasmsys.Column[comp.Position](ctx)
	for i := range pos {
		pos[i].Y = w.Offset
	}
}

func init() { wasmsys.Register(&wavetune{}) }
func main()  {}
```

- [ ] **Step 2: Write the e2e test (build the fixture, drive via verbs)**

Model the harness on `pkg/mmokit/wasm_swap_test.go` (build fixture with `buildWasmModule`, stand up a single-cell process, spawn an entity, tick, assert). Concretely:

```go
package mmokit

import (
	"context"
	"testing"
)

func TestTunableEndToEndWasm(t *testing.T) {
	wasmPath := buildWasmModule(t, "internal/testmods/wavetune")

	proc := New(Config{Name: "tunee2e", Headless: true})
	AddWasmSystem[Position](proc, wasmPath) // registers as system "wavetune"
	// ... Build() a single cell, spawn a Position entity, run one tick ...
	// (follow wasm_swap_test.go's cell/stage bootstrap exactly)

	// Default Offset=10 → entity Y becomes 10 after a tick.
	// Then dispatch tune.set wavetune Offset 50 via the registry/verbs and
	// assert the next tick sets Y=50.
	_ = context.Background()
	_ = proc
}
```

Fill in the cell bootstrap + entity spawn + tick + the `Dispatcher.Invoke(ctx, caller, "tune.set", tuneSetArgs{System:"wavetune", Field:"Offset", Value:"50"})` call by copying the established pattern from `wasm_swap_test.go`. Assert Y == 10 before, Y == 50 after.

- [ ] **Step 3: Run the test**

Run: `go test ./pkg/mmokit/ -run TunableEndToEndWasm -v`
Expected: PASS (Offset default applied, then live-set takes effect next tick).

- [ ] **Step 4: Commit**

```bash
git add pkg/mmokit/internal/testmods/wavetune/ pkg/mmokit/tunable_e2e_test.go
git commit -m "test(mmokit): e2e wasm tunable via tune.set verb"
```

---

## Phase D — remove the old runtime-config surface

### Task D1: delete config commands + Configurable + wiring

**Files:**
- Delete: `pkg/engine/configurable.go`, `pkg/engine/configurable_test.go`, `pkg/engine/builtins_config.go`
- Modify: `pkg/engine/builtins.go`, `pkg/universe/coordinator.go`, `pkg/mmokit/mmokit.go`, `cmd/server/main.go`
- Modify tests: `pkg/engine/console_cmdsys_test.go`, `pkg/engine/console_completion_test.go` (drop config cases)

- [ ] **Step 1: Delete the files**

```bash
git rm pkg/engine/configurable.go pkg/engine/configurable_test.go pkg/engine/builtins_config.go
```

- [ ] **Step 2: Strip config from `pkg/engine/builtins.go`**

Remove the config fields from `BuiltinOpts` and the `registerConfigCommands` call. Result:

```go
package engine

// BuiltinOpts configures which built-in command groups to register.
type BuiltinOpts struct {
	// Engine is used by built-in handlers to schedule work on the game loop.
	Engine *Engine
}

// RegisterBuiltins registers opt-in built-in command groups.
func (c *Console) RegisterBuiltins(opts BuiltinOpts) {
	c.snapshotBuiltinCategories()
}
```

Delete the config typed args/results structs (`configGetArgs`…`configResetResult`) from this file too.

- [ ] **Step 3: Strip config from `pkg/universe/coordinator.go`**

Remove the `Config`/`ConfigSave`/`ConfigReset`/`ConfigOnChanged` fields from `ConsoleOpts` (lines ~382-385). Remove the wiring block at ~2681-2688 and the fallback at ~2701-2708 that called `RegisterBuiltins` based on `builtinOpts.Config`. Keep `builtinOpts := engine.BuiltinOpts{Engine: defaultEng}` only if still used; if `RegisterBuiltins` is no longer needed at the coordinator at all, remove that block entirely.

- [ ] **Step 4: Strip config aliases from `pkg/mmokit/mmokit.go`**

Delete the `Configurable`, `ReflectConfig`, `NewReflectConfig`, and `BuiltinOpts` facade aliases (lines ~157-170 and ~832-833). Per project convention, no re-export shims — just delete.

- [ ] **Step 5: Strip the config block from `cmd/server/main.go`**

Remove the `console.RegisterBuiltins(mmokit.BuiltinOpts{Config: …, ConfigSave: …, ConfigReset: …, ConfigOnChanged: …})` block (~lines 287-315). Keep `game.LoadConfig` (boot seed) and `anyWorld.Config` reads. If `RegisterBuiltins` has no remaining call, that's fine.

- [ ] **Step 6: Drop config cases from console tests**

In `pkg/engine/console_cmdsys_test.go` and `pkg/engine/console_completion_test.go`, remove any test cases exercising `config.list/get/set/save/reset` or `config_fields` completions. Grep: `grep -rn "config\." pkg/engine/*_test.go`.

- [ ] **Step 7: Build + vet the whole tree**

Run:
```bash
go vet ./...
go test ./pkg/engine/ ./pkg/universe/ ./pkg/mmokit/
```
Expected: compiles clean, no references to removed symbols, tests pass.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "refactor: remove legacy runtime-config command surface (Configurable/config.*)

GameConfig struct + boot-time LoadConfig/SaveConfig retained; only the
reactive runtime get/set/save/reset surface is removed. Superseded by tune.*."
```

---

## Phase E — admin `/tunables` page

### Task E1: backend read route + SSE topic publisher

**Files:**
- Create: `pkg/admin/api_tunables.go`
- Modify: `pkg/mmokit/tunable_verbs.go` (replace the `publishTunables` stub with a real SSE publish)
- Modify: wherever admin API routes are mounted (mirror `pkg/admin/api_read.go` registration)

- [ ] **Step 1: Add the read route `pkg/admin/api_tunables.go`**

Model on `pkg/admin/api_read.go`. Expose `GET /admin/api/tunables` returning the `tune.list` result as JSON by dispatching the verb through the existing cmdsys path (so RBAC + audit apply). Concretely, reuse the command-dispatch helper that `api_commands.go` already uses to invoke a verb and marshal its typed result. Return shape: `{ "systems": [ { "name": "wave", "fields": [ {name,value,default,min,max,kind} ] } ] }` (group the flat `tuneRow`s by `System`).

- [ ] **Step 2: Replace the `publishTunables` stub (Task C4) with a real publisher**

In `pkg/mmokit/tunable_verbs.go`, replace the stub with a call through the existing admin topic bus used by `PublishAdminTopic`:

```go
func publishTunables(p *universe.Process, system string) {
	reg := tuneRegistryFor(p)
	PublishAdminTopic(p, "tunables", map[string]any{
		"system": system,
		"rows":   rowsFor(reg, system),
	})
}
```

- [ ] **Step 3: Register the `tunables` SSE topic**

Confirm the SSE multiplexer (`pkg/admin/api_stream.go` / `topicbus.go`) forwards arbitrary topic names; if topics are an allowlist, add `"tunables"` to it (mirror how `"hosts"`/`"players"` are listed).

- [ ] **Step 4: Compile-check**

Run: `go vet ./pkg/admin/ ./pkg/mmokit/`
Expected: success.

- [ ] **Step 5: Commit**

```bash
git add pkg/admin/api_tunables.go pkg/mmokit/tunable_verbs.go
git commit -m "feat(admin): /admin/api/tunables read route + tunables SSE topic"
```

---

### Task E2: SPA `/tunables` route with sliders/toggles

**Files:**
- Create: `web-admin/src/routes/tunables.svelte`, `web-admin/src/lib/tunables.ts`
- Modify: `web-admin/src/app.svelte` (route + import), sidebar nav (mirror an existing entry)

- [ ] **Step 1: Typed glue `web-admin/src/lib/tunables.ts`**

```ts
export type TuneField = {
  name: string; value: string; default: string;
  min: string; max: string; kind: string;
};
export type TuneSystem = { name: string; fields: TuneField[] };

export async function fetchTunables(): Promise<TuneSystem[]> {
  const r = await fetch("/admin/api/tunables", { credentials: "include" });
  if (!r.ok) throw new Error(`tunables: ${r.status}`);
  const j = await r.json();
  return j.systems ?? [];
}

export async function setTunable(system: string, field: string, value: string) {
  const r = await fetch("/admin/api/commands/tune.set", {
    method: "POST",
    credentials: "include",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ System: system, Field: field, Value: value }),
  });
  if (!r.ok) throw new Error(`tune.set: ${r.status}`);
}
```

- [ ] **Step 2: The route `web-admin/src/routes/tunables.svelte`**

Model structure on `web-admin/src/routes/players.svelte`. Render each system as a card; each field as a control chosen by kind/bounds:
- `kind==="bool"` → toggle (`<input type="checkbox">`) that posts `"true"`/`"false"`.
- numeric with both `min` and `max` → `<input type="range" min max step>` + a value label; on `input` (debounced ~80ms) post the value.
- numeric without bounds → `<input type="number">` posting on `change`.
Subscribe to the `tunables` SSE topic (reuse the SPA's existing SSE store/util the other routes use — e.g. the multiplexed `/admin/api/stream` subscriber) to live-update `value` when another operator changes it. Use `$state` runes for the systems array.

Provide a complete component (sketch — fill imports/SSE per the existing route pattern):

```svelte
<script lang="ts">
  import { onMount } from "svelte";
  import { fetchTunables, setTunable, type TuneSystem } from "$lib/tunables";

  let systems = $state<TuneSystem[]>([]);
  let error = $state<string>("");

  async function load() {
    try { systems = await fetchTunables(); }
    catch (e) { error = String(e); }
  }
  onMount(load);

  let timers: Record<string, number> = {};
  function onSlide(sys: string, field: string, value: string) {
    const key = `${sys}.${field}`;
    clearTimeout(timers[key]);
    timers[key] = setTimeout(() => setTunable(sys, field, value).catch((e) => (error = String(e))), 80) as unknown as number;
  }
  function onToggle(sys: string, field: string, checked: boolean) {
    setTunable(sys, field, checked ? "true" : "false").catch((e) => (error = String(e)));
  }
</script>

<h1>Tunables</h1>
{#if error}<p class="err">{error}</p>{/if}
{#each systems as sys (sys.name)}
  <section class="card">
    <h2>{sys.name}</h2>
    {#each sys.fields as f (f.name)}
      <div class="row">
        <label>{f.name}</label>
        {#if f.kind === "bool"}
          <input type="checkbox" checked={f.value === "true"}
                 onchange={(e) => onToggle(sys.name, f.name, e.currentTarget.checked)} />
        {:else if f.min !== "" && f.max !== ""}
          <input type="range" min={f.min} max={f.max} step={f.step || "any"} value={f.value}
                 oninput={(e) => { f.value = e.currentTarget.value; onSlide(sys.name, f.name, f.value); }} />
          <span class="val">{f.value}</span>
        {:else}
          <input type="number" value={f.value}
                 onchange={(e) => setTunable(sys.name, f.name, e.currentTarget.value)} />
        {/if}
      </div>
    {/each}
  </section>
{/each}
```

- [ ] **Step 3: Register the route + sidebar entry**

In `web-admin/src/app.svelte` add the import + route mapping for `/tunables` (mirror the `Players` entry), and add a sidebar nav item (find where `/players` is listed and copy the pattern). Use a `@lucide/svelte` icon (e.g. `SlidersHorizontal`).

- [ ] **Step 4: Build the SPA bundle**

Run:
```bash
cd web-admin && bun run build
```
Expected: writes `pkg/admin/static/dist/` with no type errors.

- [ ] **Step 5: Commit**

```bash
git add web-admin/src/routes/tunables.svelte web-admin/src/lib/tunables.ts web-admin/src/app.svelte pkg/admin/static/dist/
git commit -m "feat(admin-spa): /tunables page with live sliders/toggles"
```

---

## Phase F — demo wiring + smoke

### Task F1: convert `wave` consts to tunables + add native `Baseline`

**Files:**
- Modify: `examples/simple/wasmmods/wave/main.go`
- Modify: `examples/simple/system_field.go`

- [ ] **Step 1: `wave` consts → tunable fields**

Rewrite `examples/simple/wasmmods/wave/main.go` so `amplitude`/`freqHz`/`spread` are `tune:`-tagged fields on `wave` (keep `phase` unexported; keep Snapshot/Restore for `phase`):

```go
type wave struct {
	Amplitude float32 `tune:"default=220,min=60,max=420,step=10"`
	FreqHz    float32 `tune:"default=0.6,min=0.1,max=3,step=0.1"`
	Spread    float32 `tune:"default=0.012,min=0,max=0.05,step=0.001"`
	phase     float32
}

func (w *wave) Query() wasmsys.Query { return wasmsys.ReadWrite[comp.Position]() }

func (w *wave) Update(ctx *wasmsys.Ctx, dt float32) {
	w.phase += dt
	t := 2 * math.Pi * float64(w.FreqHz) * float64(w.phase)
	pos := wasmsys.Column[comp.Position](ctx)
	for i := range pos {
		pos[i].Y = w.Amplitude * float32(math.Sin(t+float64(pos[i].X)*float64(w.Spread)))
	}
}

func (w *wave) Snapshot() []byte { return wasmsys.MarshalState(w.phase) }
func (w *wave) Restore(b []byte) { wasmsys.UnmarshalState(b, &w.phase) }

func init() { wasmsys.Register(&wave{}) }
func main()  {}
```

(Drop the old `const` block and the standalone `waveY` indirection, or keep `waveY` taking amplitude/spread as args if you prefer the formula-swap comment — author's choice; the tunables are the point.)

- [ ] **Step 2: Native `FieldSystem.Baseline` tunable**

In `examples/simple/system_field.go`, add a tunable field and apply it in the broadcast:

```go
type FieldSystem struct {
	mmokit.SystemBase
	Baseline float32 `tune:"default=0,min=-200,max=200,step=10"`
	entities mmokit.Query[struct {
		Pos *mmokit.Position
	}]
}

// in Update, offset the broadcast Y by Baseline:
msg.Positions = append(msg.Positions, WavePos{X: e.Pos.X, Y: e.Pos.Y + s.Baseline})
```

- [ ] **Step 3: Build the wasm module + vet the example**

Run:
```bash
cd examples/simple && just wasm-build
cd . && go vet ./examples/simple/
```
Expected: `dist/wasmmods/wave.wasm` rebuilt; example vets clean.

- [ ] **Step 4: Verify wave.wasm exports the params funcs**

Run:
```bash
go run ./cmd/... 2>/dev/null; \
wasm-objdump -x examples/simple/dist/wasmmods/wave.wasm 2>/dev/null | grep -E "wasmsys_params_(schema|set)" || \
echo "(no wasm-objdump; rely on the e2e test instead)"
```
Expected: both `wasmsys_params_schema` and `wasmsys_params_set` listed (or skip if the tool is absent).

- [ ] **Step 5: Commit**

```bash
git add examples/simple/wasmmods/wave/main.go examples/simple/system_field.go examples/simple/dist/wasmmods/wave.wasm
git commit -m "feat(examples/simple): wave + FieldSystem tunables (amplitude/freqHz/spread/baseline)"
```

---

### Task F2: full build + manual smoke instructions

- [ ] **Step 1: Whole-tree gate**

Run:
```bash
go vet ./...
go test ./pkg/tunable/ ./pkg/wasmabi/ ./pkg/wasmhost/ ./pkg/mmokit/ ./pkg/engine/
just build
```
Expected: all green; binary in `bin/` only.

- [ ] **Step 2: Manual smoke (deliver inline to the user — do NOT write a SMOKE.md)**

From `examples/simple/`:
1. `just run`, open `http://localhost:5174` — a sine field of dots.
2. Server console: `tune list` → shows `wave` (Amplitude/FreqHz/Spread) and `FieldSystem` (Baseline).
3. `tune set wave amplitude 420` → field grows taller next tick. `tune set wave freqHz 2.5` → faster. `tune set FieldSystem baseline 120` → whole field shifts down.
4. Admin `http://localhost:9101/admin/` → `/tunables` page: drag the Amplitude slider → field reshapes live; toggle/numeric controls match each field's kind.
5. Edit `wave`'s `Update` (e.g. swap to `math.Abs(math.Sin(...))`), `just wasm-build`, console `wasm swap wave` → motion changes shape AND current tunable values persist (re-pushed from registry); phase preserved.
6. Restart `just run` → tunables back at tag defaults (ephemeral).

- [ ] **Step 3: Final commit (if any doc/touchups)**

```bash
git add -A && git commit -m "chore(tunables): finalize demo + smoke" --allow-empty
```

---

## Self-Review

**Spec coverage:**
- One-place principle / tagged field → A1/A2 (native), B2 (guest), C1 (wasm adapter). ✓
- `tune:` grammar in zero-dep pkg compiling both targets → A1 (`pkg/tunable`), used by guest in B2. ✓
- Host `Source` + two providers + resolver → A2 + C1 + C3 (`tunableSourceFor`). ✓
- Per-process registry, set-pushes-to-cells, new-cell inheritance → C3 + C4 + C5. ✓
- Uniform float64 block + 2 optional exports, no ABI bump → B1/B2/B3. ✓
- Tunables separate from snapshot/restore → C1 (separate `values`/`pushBlock`; snapshot untouched), F1 (phase still snapshotted). ✓
- Console `tune list/get/set/reset`, RouteAllHosts, positional required args, table → C4. ✓
- Admin `/tunables` sliders/toggles + SSE → E1/E2. ✓
- Ephemeral, numeric+bool only → A1 (kinds), no persistence wiring anywhere. ✓
- Removal of old config surface → D1. ✓
- Demo (wave tunables + FieldSystem.Baseline) → F1; smoke → F2. ✓

**Placeholder scan:** Backend tasks carry complete code. The admin SPA (E1 read-route handler, E2 SSE subscription) and the C6/F1 test-harness bootstrap are specified as "mirror existing file X" with the concrete shape given, because they depend on established local patterns (`api_read.go`, `api_commands.go`, `players.svelte`, `wasm_swap_test.go`) that the implementer must match exactly rather than reinvent — this is direction, not a TODO. Every novel mechanism has literal code.

**Type consistency:** `tunable.Kind`/`Def`/`Source`/`ParseTag`/`ToF64`/`FromF64` consistent A1↔A2↔B2↔C1. `wasmabi.ParamField`/`EncodeSchema`/`DecodeSchema`/`EncodeBlock`/`DecodeBlock`/`Bound*` consistent B1↔B2↔B3↔C1. `tuneRegistry` methods (`harvest`/`setValue`/`value`/`defs`/`systemNames`/`snapshotValues`) consistent C3↔C4↔E1. `SyncCellTunables`/`OnCellSystemsReady` consistent C3↔C5. Verb arg structs (`tuneSetArgs`…) match the dispatch in C4 and the SPA POST body in E2 (`System`/`Field`/`Value`).
