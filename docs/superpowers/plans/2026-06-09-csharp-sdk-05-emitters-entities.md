# C# SDK — Plan 5: csharpBackend wiring + EntityType/Entities emitters + compile gate

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fill the Plan 2 `csharpBackend` stub so `--lang=csharp` emits a compiling C# SDK: copy the Plan 3/4 runtime `_core` files, generate `EntityType.cs` + `Entities.cs` from the schema, and prove the whole thing compiles via a hermetic `dotnet build` gate.

**Architecture:** A new `csharpBackend` implements the `Backend` interface from Plan 2. `backendFor` is refactored to take a `backendOpts` struct (so language-specific knobs — TS core paths, C# core dir, namespace — don't bloat its positional args). `CoreFiles()` copies the five hand-written runtime `.cs` files (DeltaDecoderCore, InterpolationCore, UdpProto, UdpTransport, UdpTransport.Socket) into the SDK's `_core/`. `OutputFiles()` emits `EntityType.cs` (an enum) and `Entities.cs` (an `EntityBase` + one `sealed class` per kind + var-tail item classes + a `DeltaWorldUpdate`). Correctness is gated by a build-tagged Go test that runs the backend over an in-test schema, copies the core files, writes a temp `.csproj`, and runs `dotnet build` — hermetic (no Postgres; only needs the .NET SDK).

**Tech Stack:** Go (`cmd/sdkgen`), C# (`netstandard2.1`), `dotnet build`, `just`.

**Spec:** [docs/superpowers/specs/2026-06-06-csharp-sdk-unity-design.md](../specs/2026-06-06-csharp-sdk-unity-design.md) §A, §D. Scope note: this plan does NOT emit the reflect-codec classes (Events/Inputs/Operations — Plan 6), the per-entity DeltaDecoder (Plan 7), or Client.cs (Plan 7). It establishes the backend + the two simplest emitters + the compile harness.

**Prerequisites:** Plans 1–4 merged. The C# `_core` source lives in `csharp/Mmokit.Sdk.Core/` (DeltaDecoderCore.cs, InterpolationCore.cs, UdpProto.cs, UdpTransport.cs, UdpTransport.Socket.cs). Plan 2's `backendFor(lang, coreTS, interpTS string)` returns a not-implemented error for `"csharp"`.

---

## Background facts (verified)

- `cmd/sdkgen/backend.go`: `Backend` interface = `Lang() string`, `CoreFiles() []CoreFile`, `OutputFiles(ProtocolSchema) map[string]func() string`; `CoreFile{Src,Dst string}`; `backendFor(lang, coreTS, interpTS string) (Backend, error)` with a `"csharp"` → not-implemented error case.
- `cmd/sdkgen/main.go`'s `main()` calls `backendFor(*lang, *coreTS, *interpTS)` and has flags `--lang`(ts), `--out`, `--core`, `--interp`.
- `cmd/sdkgen/backend_test.go` calls `backendFor("ts","core.ts","interp.ts")`, `backendFor("csharp","","")`, `backendFor("rust","","")`.
- `EntitySchema{Kind uint8, Name string, Bindings []BindingSchema, Layout []int, VarTail *VarTailSchema, InitialData string}`; `BindingSchema{Type string, Fields []BindingSchemaField}`; `BindingSchemaField{Name, Encoding string, Size int, Scale float64, Initial bool}`; `VarTailSchema{Name string, ItemSize int, ItemFields []BindingSchemaField}`.
- The TS entity emitter (`generate.go::genEntities`) emits, per kind: `netID`, `entityType`, `producedAtMs`, then non-initial fixed fields, then initial fixed fields, then a var-tail `Item[]`; plus an `AnyEntity` union + `DeltaWorldUpdate`. Field name = `field.Name` verbatim. Entity type name = `Name+"Entity"` (or `Entity{Kind}` if unnamed). Var-tail item type = `strip "Entity" + TitleCase(VarTail.Name) + "Item"`.

---

## File Structure

- **Create:** `cmd/sdkgen/backend_csharp.go` — `csharpBackend` + `genEntityType` + `genEntities` + C# type helpers.
- **Modify:** `cmd/sdkgen/backend.go` — `backendOpts` struct; `backendFor(lang string, o backendOpts)`; wire `"csharp"` → `csharpBackend`.
- **Modify:** `cmd/sdkgen/main.go` — add `--namespace` + `--csharp-core` flags; build `backendOpts`; pass to `backendFor`.
- **Modify:** `cmd/sdkgen/backend_test.go` — update the 3 `backendFor` calls to the struct form; add a csharp-selection assertion.
- **Create:** `cmd/sdkgen/backend_csharp_test.go` — hermetic emitter string-assertion tests.
- **Create:** `cmd/sdkgen/csharp_compile_test.go` — `//go:build csharptest` dotnet-build compile gate.
- **Modify:** `justfile` — add `csharp-compile-test` recipe.

---

### Task 1: csharpBackend + backendOpts refactor + EntityType/Entities emitters

**Files:**
- Modify: `cmd/sdkgen/backend.go`
- Create: `cmd/sdkgen/backend_csharp.go`
- Modify: `cmd/sdkgen/main.go`
- Modify: `cmd/sdkgen/backend_test.go`
- Create: `cmd/sdkgen/backend_csharp_test.go`

- [ ] **Step 1: Refactor `backendFor` to a struct in `cmd/sdkgen/backend.go`.** Replace the existing `backendFor` function with:

```go
// backendOpts carries the language-specific knobs main.go derives from flags.
type backendOpts struct {
	CoreTS        string // TS: delta-decoder-core.ts source path
	InterpTS      string // TS: interpolation-core.ts source path
	CSharpCoreDir string // C#: dir holding the hand-written _core .cs files
	Namespace     string // C#: root namespace for generated files
}

// backendFor selects a backend by --lang token.
func backendFor(lang string, o backendOpts) (Backend, error) {
	switch lang {
	case "ts":
		return tsBackend{coreTS: o.CoreTS, interpTS: o.InterpTS}, nil
	case "csharp":
		ns := o.Namespace
		if ns == "" {
			ns = "Mmokit.Sdk"
		}
		return csharpBackend{namespace: ns, coreDir: o.CSharpCoreDir}, nil
	default:
		return nil, fmt.Errorf("unknown --lang %q (want: ts, csharp)", lang)
	}
}
```

(The `fmt` import is already present in backend.go.)

- [ ] **Step 2: Create `cmd/sdkgen/backend_csharp.go`:**

```go
package main

import (
	"fmt"
	"sort"
	"strings"
)

// csharpBackend emits a C# client SDK. The hand-written runtime lives in
// csharp/Mmokit.Sdk.Core/*.cs and is copied verbatim into the SDK's _core/;
// the schema-driven types are generated here.
type csharpBackend struct {
	namespace string // root namespace for generated files (default "Mmokit.Sdk")
	coreDir   string // path to the hand-written _core .cs sources
}

func (b csharpBackend) Lang() string { return "csharp" }

// CoreFiles copies the five hand-written runtime files into <out>/_core/.
func (b csharpBackend) CoreFiles() []CoreFile {
	dir := b.coreDir
	if dir == "" {
		dir = "csharp/Mmokit.Sdk.Core"
	}
	names := []string{
		"DeltaDecoderCore.cs",
		"InterpolationCore.cs",
		"UdpProto.cs",
		"UdpTransport.cs",
		"UdpTransport.Socket.cs",
	}
	out := make([]CoreFile, len(names))
	for i, n := range names {
		out[i] = CoreFile{Src: dir + "/" + n, Dst: n}
	}
	return out
}

func (b csharpBackend) OutputFiles(schema ProtocolSchema) map[string]func() string {
	files := map[string]func() string{}
	// EntityType + Entities are emitted only when the schema registers kinds.
	if len(schema.Entities) > 0 {
		files["EntityType.cs"] = func() string { return b.genEntityType(schema) }
		files["Entities.cs"] = func() string { return b.genEntities(schema) }
	}
	return files
}

// genEntityType emits a byte-valued enum of entity kinds, sorted by Kind.
func (b csharpBackend) genEntityType(schema ProtocolSchema) string {
	entries := make([]EntitySchema, len(schema.Entities))
	copy(entries, schema.Entities)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Kind < entries[j].Kind })

	var sb strings.Builder
	sb.WriteString("// GENERATED by sdkgen — do not edit.\n\n")
	fmt.Fprintf(&sb, "namespace %s\n{\n", b.namespace)
	sb.WriteString("    /// Entity-kind wire IDs. Values match the kindID arg to\n")
	sb.WriteString("    /// mmokit.RegisterKind[T] on the server; names match its display-name arg.\n")
	sb.WriteString("    public enum EntityType : byte\n    {\n")
	for _, e := range entries {
		fmt.Fprintf(&sb, "        %s = %d,\n", csharpIdent(e.Name, e.Kind), e.Kind)
	}
	sb.WriteString("    }\n}\n")
	return sb.String()
}

// genEntities emits EntityBase + one sealed class per kind (+ var-tail item
// classes) + a DeltaWorldUpdate carrier.
func (b csharpBackend) genEntities(schema ProtocolSchema) string {
	var sb strings.Builder
	sb.WriteString("// GENERATED by sdkgen — do not edit.\n\n")
	sb.WriteString("using System.Collections.Generic;\n\n")
	fmt.Fprintf(&sb, "namespace %s\n{\n", b.namespace)

	// Common base: every decoded entity carries these three.
	sb.WriteString("    /// Fields every decoded entity carries. ProducedAtMs is the producer\n")
	sb.WriteString("    /// cluster-clock stamp (Unix ms) used as the interpolation time-base.\n")
	sb.WriteString("    public abstract class EntityBase\n    {\n")
	sb.WriteString("        public uint NetID;\n")
	sb.WriteString("        public byte EntityKind;\n")
	sb.WriteString("        public ulong ProducedAtMs;\n")
	sb.WriteString("    }\n\n")

	for _, ent := range schema.Entities {
		name := csharpEntityName(ent)
		fmt.Fprintf(&sb, "    /// Entity kind %d.\n", ent.Kind)
		fmt.Fprintf(&sb, "    public sealed class %s : EntityBase\n    {\n", name)
		// Non-initial then initial fixed fields (matches the TS order).
		for _, binding := range ent.Bindings {
			for _, f := range binding.Fields {
				if f.Initial {
					continue
				}
				fmt.Fprintf(&sb, "        public %s %s;\n", encodingToCSharpType(f.Encoding), f.Name)
			}
		}
		for _, binding := range ent.Bindings {
			for _, f := range binding.Fields {
				if !f.Initial {
					continue
				}
				fmt.Fprintf(&sb, "        public %s %s;\n", encodingToCSharpType(f.Encoding), f.Name)
			}
		}
		if ent.VarTail != nil {
			item := csharpTailItemName(ent)
			fmt.Fprintf(&sb, "        public List<%s> %s = new();\n", item, ent.VarTail.Name)
		}
		sb.WriteString("    }\n\n")

		if ent.VarTail != nil {
			item := csharpTailItemName(ent)
			fmt.Fprintf(&sb, "    /// Item record for %s.%s.\n", name, ent.VarTail.Name)
			fmt.Fprintf(&sb, "    public sealed class %s\n    {\n", item)
			for _, f := range ent.VarTail.ItemFields {
				fmt.Fprintf(&sb, "        public %s %s;\n", encodingToCSharpType(f.Encoding), f.Name)
			}
			sb.WriteString("    }\n\n")
		}
	}

	// World-update carrier (entered/updated are typed to the common base).
	sb.WriteString("    /// One decoded SE_DELTA_WORLD_UPDATE frame.\n")
	sb.WriteString("    public sealed class DeltaWorldUpdate\n    {\n")
	sb.WriteString("        public uint Tick;\n")
	sb.WriteString("        public uint Seq;\n")
	sb.WriteString("        public bool FreshSnapshot;\n")
	sb.WriteString("        public List<EntityBase> Entered = new();\n")
	sb.WriteString("        public List<EntityBase> Updated = new();\n")
	sb.WriteString("        public List<uint> Removed = new();\n")
	sb.WriteString("        public List<uint> Exited = new();\n")
	sb.WriteString("    }\n}\n")
	return sb.String()
}

// csharpEntityName mirrors generate.go::entityName: "Ship" -> "ShipEntity";
// unnamed -> "Entity{Kind}".
func csharpEntityName(ent EntitySchema) string {
	if ent.Name != "" {
		return ent.Name + "Entity"
	}
	return fmt.Sprintf("Entity%d", ent.Kind)
}

// csharpTailItemName mirrors generate.go::tailItemTypeName: strip "Entity",
// append TitleCase(varTail name) + "Item".
func csharpTailItemName(ent EntitySchema) string {
	if ent.VarTail == nil {
		return ""
	}
	base := strings.TrimSuffix(csharpEntityName(ent), "Entity")
	return base + titleCase(ent.VarTail.Name) + "Item"
}

// csharpIdent returns a valid C# enum-member identifier for an entity name,
// falling back to Entity{Kind} if the name is empty.
func csharpIdent(name string, kind uint8) string {
	if name == "" {
		return fmt.Sprintf("Entity%d", kind)
	}
	return name
}

// encodingToCSharpType maps a wire/field encoding to the DECODED C# type the
// entity data class holds. Quantized numerics decode to float; integer
// encodings keep their width; bool/string map directly. Mirrors the type the
// Plan 7 DeltaDecoder will produce into these fields.
func encodingToCSharpType(enc string) string {
	switch enc {
	case "string":
		return "string"
	case "bool":
		return "bool"
	case "u8":
		return "byte"
	case "u16":
		return "ushort"
	case "u32":
		return "uint"
	case "u64":
		return "ulong"
	case "i8":
		return "sbyte"
	case "i16":
		return "short"
	case "i32":
		return "int"
	case "i64":
		return "long"
	case "f32", "qvel", "qangle", "qnorm":
		return "float"
	default:
		// Unknown encodings decode to a float scalar, mirroring the TS
		// `number` fallback. New encodings should be added explicitly.
		return "float"
	}
}
```

- [ ] **Step 3: Wire the flags in `cmd/sdkgen/main.go`.** Add two flags after the `interpTS` flag declaration:

```go
	namespace := flag.String("namespace", "Mmokit.Sdk", "C#: root namespace for generated files")
	csharpCore := flag.String("csharp-core", "csharp/Mmokit.Sdk.Core", "C#: dir holding the hand-written _core .cs sources")
```

Replace the `backendFor` call:

```go
	backend, err := backendFor(*lang, backendOpts{
		CoreTS:        *coreTS,
		InterpTS:      *interpTS,
		CSharpCoreDir: *csharpCore,
		Namespace:     *namespace,
	})
	if err != nil {
		log.Fatalf("sdkgen: %v", err)
	}
```

- [ ] **Step 4: Update `cmd/sdkgen/backend_test.go`** — change the three `backendFor` calls to the struct form, and assert the csharp backend is now constructed:

Replace the body of `TestBackendFor` with:

```go
func TestBackendFor(t *testing.T) {
	// ts → a working tsBackend, no error.
	b, err := backendFor("ts", backendOpts{CoreTS: "core.ts", InterpTS: "interp.ts"})
	if err != nil {
		t.Fatalf("backendFor(ts) error = %v, want nil", err)
	}
	if b.Lang() != "ts" {
		t.Fatalf("Lang() = %q, want ts", b.Lang())
	}

	// csharp → a working csharpBackend (the Plan 2 not-implemented error is gone).
	cs, err := backendFor("csharp", backendOpts{})
	if err != nil {
		t.Fatalf("backendFor(csharp) error = %v, want nil", err)
	}
	if cs.Lang() != "csharp" {
		t.Fatalf("Lang() = %q, want csharp", cs.Lang())
	}

	// unknown → generic unknown-lang error.
	if _, err := backendFor("rust", backendOpts{}); err == nil ||
		!strings.Contains(err.Error(), "unknown --lang") {
		t.Fatalf("backendFor(rust) error = %v, want 'unknown --lang'", err)
	}
}
```

- [ ] **Step 5: Write the hermetic emitter tests** `cmd/sdkgen/backend_csharp_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

func sampleEntitySchema() ProtocolSchema {
	return ProtocolSchema{
		Game: "demo",
		Entities: []EntitySchema{
			{
				Kind: 0,
				Name: "Ship",
				Bindings: []BindingSchema{
					{Type: "Position", Fields: []BindingSchemaField{
						{Name: "x", Encoding: "f32", Size: 4},
						{Name: "y", Encoding: "f32", Size: 4},
					}},
					{Type: "Velocity", Fields: []BindingSchemaField{
						{Name: "vx", Encoding: "qvel", Size: 2, Scale: 0.01},
					}},
					{Type: "Name", Fields: []BindingSchemaField{
						{Name: "shipName", Encoding: "string", Initial: true},
					}},
				},
				VarTail: &VarTailSchema{
					Name:     "statusEffects",
					ItemSize: 5,
					ItemFields: []BindingSchemaField{
						{Name: "kind", Encoding: "u8", Size: 1},
						{Name: "stacks", Encoding: "f32", Size: 4},
					},
				},
			},
			{Kind: 1, Name: "Asteroid", Bindings: []BindingSchema{
				{Type: "Position", Fields: []BindingSchemaField{
					{Name: "x", Encoding: "f32", Size: 4},
				}},
			}},
		},
	}
}

func TestCsharpBackend_EntityType(t *testing.T) {
	out := csharpBackend{namespace: "Mmokit.Sdk"}.genEntityType(sampleEntitySchema())
	for _, want := range []string{
		"namespace Mmokit.Sdk",
		"public enum EntityType : byte",
		"Ship = 0,",
		"Asteroid = 1,",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("genEntityType missing %q in:\n%s", want, out)
		}
	}
}

func TestCsharpBackend_Entities(t *testing.T) {
	out := csharpBackend{namespace: "Mmokit.Sdk"}.genEntities(sampleEntitySchema())
	for _, want := range []string{
		"public abstract class EntityBase",
		"public uint NetID;",
		"public sealed class ShipEntity : EntityBase",
		"public float x;",         // f32 → float
		"public float vx;",        // qvel → float
		"public string shipName;", // string + initial field present
		"public List<ShipStatusEffectsItem> statusEffects = new();",
		"public sealed class ShipStatusEffectsItem",
		"public byte kind;",       // u8 → byte
		"public sealed class AsteroidEntity : EntityBase",
		"public sealed class DeltaWorldUpdate",
		"public List<EntityBase> Entered = new();",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("genEntities missing %q in:\n%s", want, out)
		}
	}
}

func TestCsharpBackend_OutputFiles_SkipsWhenNoEntities(t *testing.T) {
	files := csharpBackend{namespace: "Mmokit.Sdk"}.OutputFiles(ProtocolSchema{})
	if len(files) != 0 {
		t.Fatalf("expected no files for empty schema, got %d", len(files))
	}
	files = csharpBackend{namespace: "Mmokit.Sdk"}.OutputFiles(sampleEntitySchema())
	for _, name := range []string{"EntityType.cs", "Entities.cs"} {
		if _, ok := files[name]; !ok {
			t.Fatalf("expected %s in OutputFiles", name)
		}
	}
}

func TestCsharpBackend_CoreFiles(t *testing.T) {
	core := csharpBackend{coreDir: "x/core"}.CoreFiles()
	if len(core) != 5 {
		t.Fatalf("CoreFiles len = %d, want 5", len(core))
	}
	// Dst basenames are what the SDK compiles; Src is threaded from coreDir.
	if core[0].Dst != "DeltaDecoderCore.cs" || core[0].Src != "x/core/DeltaDecoderCore.cs" {
		t.Fatalf("CoreFiles[0] = %+v", core[0])
	}
}
```

- [ ] **Step 6: Verify + commit**

Run: `go vet ./cmd/sdkgen/... && go test ./cmd/sdkgen/ 2>&1 | tail -5`
Expected: vet clean; all tests pass (the Plan 2 selection tests + the new csharp emitter tests).

```bash
git add cmd/sdkgen/backend.go cmd/sdkgen/backend_csharp.go cmd/sdkgen/main.go cmd/sdkgen/backend_test.go cmd/sdkgen/backend_csharp_test.go
git commit -m "feat(sdkgen): csharpBackend — EntityType.cs + Entities.cs emitters

Fills the Plan 2 --lang=csharp stub. backendFor takes a backendOpts struct
(TS core paths, C# core dir, namespace). csharpBackend.CoreFiles copies the
five hand-written _core .cs; OutputFiles emits EntityType (enum) + Entities
(EntityBase + per-kind classes + var-tail items + DeltaWorldUpdate).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Hermetic dotnet-build compile gate

Proves the generated C# (the two emitted files + the five copied `_core` files) actually compiles as one assembly — the real validator that the emitters + the core-as-copied-source are sound.

**Files:**
- Create: `cmd/sdkgen/csharp_compile_test.go`
- Modify: `justfile`

- [ ] **Step 1: Create the build-tagged compile gate** `cmd/sdkgen/csharp_compile_test.go`:

```go
//go:build csharptest

// Compile gate: emit the C# SDK for a sample schema, copy the _core sources,
// and run `dotnet build` on the result. Tagged so it only runs where the .NET
// SDK is available: `go test -tags=csharptest ./cmd/sdkgen/`.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCsharpSdk_Compiles(t *testing.T) {
	if _, err := exec.LookPath("dotnet"); err != nil {
		t.Skip("dotnet not installed; skipping C# compile gate")
	}

	out := t.TempDir()
	if err := os.MkdirAll(filepath.Join(out, "_core"), 0o755); err != nil {
		t.Fatal(err)
	}

	b := csharpBackend{namespace: "Mmokit.Sdk", coreDir: "../../csharp/Mmokit.Sdk.Core"}

	// Copy the five _core files.
	for _, cf := range b.CoreFiles() {
		data, err := os.ReadFile(cf.Src)
		if err != nil {
			t.Fatalf("read core %s: %v", cf.Src, err)
		}
		if err := os.WriteFile(filepath.Join(out, "_core", cf.Dst), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Emit the generated files for the sample schema.
	for name, gen := range b.OutputFiles(sampleEntitySchema()) {
		if err := os.WriteFile(filepath.Join(out, name), []byte(gen()), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Minimal netstandard2.1 project that compiles every .cs in the dir.
	csproj := `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <TargetFramework>netstandard2.1</TargetFramework>
    <LangVersion>9.0</LangVersion>
    <Nullable>enable</Nullable>
  </PropertyGroup>
</Project>
`
	if err := os.WriteFile(filepath.Join(out, "Sdk.csproj"), []byte(csproj), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("dotnet", "build", "-nologo", "-v", "quiet")
	cmd.Dir = out
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dotnet build failed: %v\n%s", err, output)
	}
}
```

- [ ] **Step 2: Add the justfile recipe** — append to `justfile`:

```just
# compile-gate the generated C# SDK (emits a sample SDK + dotnet build)
csharp-compile-test:
    go test -tags=csharptest ./cmd/sdkgen/ -run TestCsharpSdk_Compiles -v
```

- [ ] **Step 3: Run the compile gate**

Run: `go test -tags=csharptest ./cmd/sdkgen/ -run TestCsharpSdk_Compiles -v 2>&1 | tail -15`
Expected: PASS — the generated `EntityType.cs` + `Entities.cs` + the five copied `_core` files compile as one `netstandard2.1` assembly. (First run restores/builds; allow it.)

- [ ] **Step 4: Confirm the default-tag build is unaffected**

Run: `go test ./cmd/sdkgen/ 2>&1 | tail -3`
Expected: PASS, and the `csharptest`-tagged file is excluded (no dotnet invoked).

- [ ] **Step 5: Commit**

```bash
git add cmd/sdkgen/csharp_compile_test.go justfile
git commit -m "test(sdkgen): hermetic dotnet-build compile gate for the C# SDK

go test -tags=csharptest emits the sample SDK (EntityType + Entities + the
five _core files) into a temp dir and runs dotnet build — proves the
generated C# + the core-as-copied-source compile together. just
csharp-compile-test wraps it.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

- **Spec coverage (§A, §D partial):** `csharpBackend` fills the Plan 2 stub and is wired via `backendFor`; `CoreFiles` copies the five runtime sources; `OutputFiles` emits `EntityType.cs` + `Entities.cs`, schema-gated. The compile gate proves it builds. Events/Inputs/Operations/DeltaDecoder/Client are explicitly deferred (scope note). ✅
- **Placeholder scan:** Complete code in every step. The sample schema is concrete; the compile gate is hermetic (no Postgres). ✅
- **Type/name consistency:** `backendOpts` fields used in `main.go` match the struct; `backendFor(string, backendOpts)` matches all call sites (main.go + the 3 updated tests + the compile gate constructs `csharpBackend` directly). `csharpEntityName`/`csharpTailItemName` mirror the TS `entityName`/`tailItemTypeName`. `encodingToCSharpType` covers the entity field encodings; the emitter tests assert the f32/qvel→float, u8→byte, string mappings. `genEntityType`/`genEntities`/`OutputFiles`/`CoreFiles` names match between the backend, the tests, and the compile gate. ✅
- **DRY:** `csharpBackend` reuses `titleCase` (main.go) for the var-tail item name, mirroring the TS path. ✅

## Open items (resolve during planning, not blocking)

- Generated entity field names use the schema's verbatim (camelCase) names (e.g. `vx`), while `EntityBase` uses PascalCase (`NetID`). This matches the TS SDK's verbatim-field-name choice and keeps the Plan 7 decoder's name-based assignment simple; a PascalCase pass can be added later if desired (it must move in lockstep with the decoder).
- `encodingToCSharpType`'s `float` fallback for unknown encodings mirrors the TS `number` fallback; if Plan 6/7 surface an encoding that should be a different C# type, add it explicitly there.
