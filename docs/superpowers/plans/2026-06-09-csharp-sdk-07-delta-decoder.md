# C# SDK — Plan 7: DeltaDecoder.cs (per-entity world-delta decode)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Generate `DeltaDecoder.cs` — the per-entity binary decoder that turns an `SE_DELTA_WORLD_UPDATE` frame into a `DeltaWorldUpdate` of typed entity objects (the `Entities.cs` classes from Plan 5), using the `DeltaDecoderCore` primitives from Plan 3.

**Architecture:** A new emitter `csharpBackend.genDeltaDecoder` (in `cmd/sdkgen/backend_csharp_delta.go`) ports `cmd/sdkgen/generate.go::genDeltaDecoder`. Per entity kind it emits a `Decode<Name>Snapshot(snap, initial, existing)` static method that reads the fixed snapshot fields (`f32`/`qvel`/`qangle`/`qnorm`/`u8`/`u16`/`u32`/`i16`/`bool`), the optional var-tail (`List<Item>`), and the initial-only fields (from the entry's `initialData`, falling back to the previous entity). A `<Game>DeltaDecoder` class drives the frame: `DecodeFrameHeader` → full entries (decode + cache baseline) → delta entries (`ApplyDelta` over the cached baseline) → removed/exited → returns `DeltaWorldUpdate`. `BaselineStore<BaselineMeta>` (Plan 3 core) caches the last snapshot + decoded entity per netID. Correctness is gated by the existing `dotnet build` compile gate, whose sample schema already exercises `qvel`, an initial `string`, and a var-tail.

**Tech Stack:** Go (`cmd/sdkgen`), C# (`netstandard2.1`), `dotnet build` compile gate.

**Spec:** [docs/superpowers/specs/2026-06-06-csharp-sdk-unity-design.md](../specs/2026-06-06-csharp-sdk-unity-design.md) §D (DeltaDecoder.cs). Builds on Plan 3 (`DeltaDecoderCore`) + Plan 5 (`Entities.cs`/`EntityBase`).

**Prerequisites:** Plans 1–6 merged.

---

## Background facts (verified against `cmd/sdkgen/generate.go`)

- Per-entity `<NAME>_FIELD_SIZES` = the entity's `Layout` with a trailing `-1` (var-tail marker) stripped; `<NAME>_HAS_VAR_TAIL` = whether that `-1` was present. `ApplyDelta(fieldSizes, hasVarTail, baseline, delta)` (Plan 3 core) takes the stripped sizes + the bool.
- Fixed snapshot field decode (`writeFieldDecoder`): `f32`→`ReadFloat32`(+4); `qvel`→`UnVel(ReadInt16, scale)`(+2); `qangle`→`UnAngle(ReadUint16)`(+2); `qnorm`→`UnNorm(snap[o])`(+1); `u8`→`snap[o]`(+1); `u16`→`ReadUint16`(+2); `u32`→`ReadUint32`(+4); `i16`→`ReadInt16`(+2); `bool`→`snap[o]!=0`(+1). Strings never appear in the fixed layout (they're initial-only).
- Var-tail (`writeVarTailDecoder`): `u16` byte-length prefix, then a `while (o < end)` loop reading fixed-stride scalar item fields (`f32`/`qvel`/`qangle`/`qnorm`/`u8`/`u16`/`u32`/`i16`/`bool` — NO strings) into `List<Item>`.
- Initial fields (`writeInitialFieldDecoder`): read from the entry's `initialData` with a running `initialOff`; `string` = 1-byte length + UTF-8; `u8`/`i8`/`u16`/`i16`/`u32`/`f32`/`bool` supported; fall back to `existing?.<field>` (or zero/"") when the initial blob is absent or exhausted.
- Frame decode (`<Game>DeltaDecoder.decode`): `DecodeFrameHeader`; if `FrameFlagFreshSnapshot` set → `baselines.Clear()`; full loop (`DecodeFullEntry` → `DecodeEntity` → cache `{type,lastEntity}` baseline → `entered`); delta loop (`DecodeDeltaEntry` → look up baseline, skip if none → `ApplyDelta` → `DecodeEntity` → re-cache → `updated`); `DecodeRemovedIDs` ×2 (removed, exited) → delete baselines → return `DeltaWorldUpdate`.
- `DeltaDecoderCore` (Plan 3) provides: `ReadFloat32/ReadInt16/ReadUint16/ReadUint32`, `UnAngle(int)/UnNorm(int)/UnVel(int,double)` (all returning `double`), `DecodeFrameHeader/DecodeFullEntry/DecodeDeltaEntry/DecodeRemovedIDs`, `ApplyDelta`, `BaselineStore<T>` (`Set(netID,snapshot,meta)`, `TryGet(netID, out snapshot, out meta)`, `Delete`, `Clear`), `FrameFlagFreshSnapshot`, `FullEntryHeader{NetID,EntityType,ProducedAtMs,Snapshot,InitialData}`, `DeltaEntryHeader{…,DeltaData}`.
- `Entities.cs` (Plan 5): `EntityBase{uint NetID; byte EntityKind; ulong ProducedAtMs}`; per-kind `<Name>Entity : EntityBase` with verbatim field names; var-tail `List<<Name><Tail>Item>`; `DeltaWorldUpdate{uint Tick; uint Seq; bool FreshSnapshot; List<EntityBase> Entered/Updated; List<uint> Removed/Exited}`. Quantized + f32 fields are typed `float`; `u8`→`byte`, `u16`→`ushort`, `u32`→`uint`, `i16`→`short`, `bool`→`bool`, `string`→`string`.
- Game name → class prefix: `titleCase(schema.Game)` + `"DeltaDecoder"` (matches the TS `<Game>DeltaDecoder`).

---

## File Structure

- **Create:** `cmd/sdkgen/backend_csharp_delta.go` — `genDeltaDecoder` + C# field/var-tail/initial decoders.
- **Modify:** `cmd/sdkgen/backend_csharp.go` — wire `DeltaDecoder.cs` into `OutputFiles` (gated on `len(Entities) > 0`).
- **Modify:** `cmd/sdkgen/backend_csharp_test.go` — emitter assertions.

---

### Task 1: genDeltaDecoder emitter + wiring + tests

**Files:**
- Create: `cmd/sdkgen/backend_csharp_delta.go`
- Modify: `cmd/sdkgen/backend_csharp.go`, `cmd/sdkgen/backend_csharp_test.go`

- [ ] **Step 1: Create `cmd/sdkgen/backend_csharp_delta.go`:**

```go
package main

import (
	"fmt"
	"strings"
)

// genDeltaDecoder emits DeltaDecoder.cs: per-entity snapshot decoders + a
// <Game>DeltaDecoder class driving full/delta frame application over the
// DeltaDecoderCore primitives. Port of generate.go::genDeltaDecoder.
func (b csharpBackend) genDeltaDecoder(schema ProtocolSchema) string {
	var sb strings.Builder
	sb.WriteString("// GENERATED by sdkgen — do not edit.\n\n")
	sb.WriteString("using System;\nusing System.Collections.Generic;\nusing System.Text;\nusing Mmokit.Sdk.Core;\n\n")
	fmt.Fprintf(&sb, "namespace %s\n{\n", b.namespace)

	gameName := titleCase(schema.Game)

	// Per-entity snapshot decoders.
	for _, ent := range schema.Entities {
		writeCsSnapshotDecoder(&sb, ent, csharpEntityName(ent), csharpTailItemName(ent))
	}

	// Baseline meta: the cached snapshot's kind + last decoded entity (so
	// delta frames can restore initial-only fields like names).
	sb.WriteString("    sealed class BaselineMeta { public byte Type; public EntityBase? LastEntity; }\n\n")

	fmt.Fprintf(&sb, "    public sealed class %sDeltaDecoder\n    {\n", gameName)
	sb.WriteString("        readonly BaselineStore<BaselineMeta> _baselines = new();\n\n")
	sb.WriteString("        public void Clear() => _baselines.Clear();\n\n")

	sb.WriteString("        public DeltaWorldUpdate Decode(byte[] data)\n        {\n")
	sb.WriteString("            var (header, pos0) = DeltaDecoderCore.DecodeFrameHeader(data, 0);\n")
	sb.WriteString("            int pos = pos0;\n")
	sb.WriteString("            bool freshSnapshot = (header.Flags & DeltaDecoderCore.FrameFlagFreshSnapshot) != 0;\n")
	sb.WriteString("            if (freshSnapshot) _baselines.Clear();\n\n")
	sb.WriteString("            var entered = new List<EntityBase>();\n")
	sb.WriteString("            var updated = new List<EntityBase>();\n\n")

	// Full entries.
	sb.WriteString("            for (int i = 0; i < header.FullCount; i++)\n            {\n")
	sb.WriteString("                var (entry, next) = DeltaDecoderCore.DecodeFullEntry(data, pos);\n")
	sb.WriteString("                pos = next;\n")
	sb.WriteString("                EntityBase? prev = _baselines.TryGet(entry.NetID, out _, out var pm) ? pm?.LastEntity : null;\n")
	sb.WriteString("                var entity = DecodeEntity(entry.EntityType, entry.Snapshot, entry.InitialData, entry.NetID, entry.ProducedAtMs, prev);\n")
	sb.WriteString("                _baselines.Set(entry.NetID, entry.Snapshot, new BaselineMeta { Type = entry.EntityType, LastEntity = entity });\n")
	sb.WriteString("                if (entity != null) entered.Add(entity);\n")
	sb.WriteString("            }\n\n")

	// Delta entries.
	sb.WriteString("            for (int i = 0; i < header.DeltaCount; i++)\n            {\n")
	sb.WriteString("                var (entry, next) = DeltaDecoderCore.DecodeDeltaEntry(data, pos);\n")
	sb.WriteString("                pos = next;\n")
	sb.WriteString("                if (!_baselines.TryGet(entry.NetID, out var snap, out var meta)) continue;\n")
	sb.WriteString("                int[] fieldSizes = FieldSizesFor(entry.EntityType);\n")
	sb.WriteString("                bool hasVarTail = HasVarTailFor(entry.EntityType);\n")
	sb.WriteString("                byte[] newSnap = DeltaDecoderCore.ApplyDelta(fieldSizes, hasVarTail, snap, entry.DeltaData);\n")
	sb.WriteString("                var entity = DecodeEntity(entry.EntityType, newSnap, null, entry.NetID, entry.ProducedAtMs, meta?.LastEntity);\n")
	sb.WriteString("                _baselines.Set(entry.NetID, newSnap, new BaselineMeta { Type = meta?.Type ?? entry.EntityType, LastEntity = entity });\n")
	sb.WriteString("                if (entity != null) updated.Add(entity);\n")
	sb.WriteString("            }\n\n")

	// Removed + exited.
	sb.WriteString("            var (removed, pos2) = DeltaDecoderCore.DecodeRemovedIDs(data, pos, header.RemovedCount);\n")
	sb.WriteString("            var (exited, _) = DeltaDecoderCore.DecodeRemovedIDs(data, pos2, header.ExitedCount);\n")
	sb.WriteString("            foreach (var id in removed) _baselines.Delete(id);\n")
	sb.WriteString("            foreach (var id in exited) _baselines.Delete(id);\n\n")

	sb.WriteString("            return new DeltaWorldUpdate {\n")
	sb.WriteString("                Tick = header.Tick, Seq = header.Seq, FreshSnapshot = freshSnapshot,\n")
	sb.WriteString("                Entered = entered, Updated = updated,\n")
	sb.WriteString("                Removed = new List<uint>(removed), Exited = new List<uint>(exited),\n")
	sb.WriteString("            };\n")
	sb.WriteString("        }\n\n")

	// DecodeEntity dispatcher.
	sb.WriteString("        static EntityBase? DecodeEntity(byte type_, byte[] snap, byte[]? initial, uint netID, ulong producedAtMs, EntityBase? existing)\n        {\n")
	sb.WriteString("            switch (type_)\n            {\n")
	for _, ent := range schema.Entities {
		name := csharpEntityName(ent)
		fmt.Fprintf(&sb, "                case %d: { var e = Decode%sSnapshot(snap, initial, existing as %s); e.NetID = netID; e.ProducedAtMs = producedAtMs; return e; }\n", ent.Kind, name, name)
	}
	sb.WriteString("                default: return null;\n")
	sb.WriteString("            }\n        }\n\n")

	// FieldSizesFor / HasVarTailFor.
	sb.WriteString("        static int[] FieldSizesFor(byte type_)\n        {\n            switch (type_)\n            {\n")
	for _, ent := range schema.Entities {
		fixed, _ := splitVarTailLayout(ent.Layout)
		fmt.Fprintf(&sb, "                case %d: return new int[] { %s };\n", ent.Kind, joinInts(fixed))
	}
	sb.WriteString("                default: return System.Array.Empty<int>();\n            }\n        }\n\n")

	sb.WriteString("        static bool HasVarTailFor(byte type_)\n        {\n            switch (type_)\n            {\n")
	for _, ent := range schema.Entities {
		_, hasVarTail := splitVarTailLayout(ent.Layout)
		fmt.Fprintf(&sb, "                case %d: return %v;\n", ent.Kind, hasVarTail)
	}
	sb.WriteString("                default: return false;\n            }\n        }\n")

	sb.WriteString("    }\n}\n")
	return sb.String()
}

// splitVarTailLayout strips a trailing -1 var-tail marker, returning the fixed
// field sizes + whether a var tail was present.
func splitVarTailLayout(layout []int) (fixed []int, hasVarTail bool) {
	fixed = layout
	if len(fixed) > 0 && fixed[len(fixed)-1] == -1 {
		hasVarTail = true
		fixed = fixed[:len(fixed)-1]
	}
	return fixed, hasVarTail
}

// writeCsSnapshotDecoder emits Decode<Name>Snapshot.
func writeCsSnapshotDecoder(sb *strings.Builder, ent EntitySchema, name, itemName string) {
	fmt.Fprintf(sb, "    static %s Decode%sSnapshot(byte[] snap, byte[]? initial, %s? existing)\n    {\n", name, name, name)
	sb.WriteString("        int o = 0;\n")
	fmt.Fprintf(sb, "        var e = new %s { EntityKind = %d };\n", name, ent.Kind)

	// Fixed (non-initial) fields.
	for _, binding := range ent.Bindings {
		for _, f := range binding.Fields {
			if f.Initial {
				continue
			}
			writeCsFixedFieldDecode(sb, "e."+f.Name, f, "        ")
		}
	}

	// Var tail.
	if ent.VarTail != nil {
		vt := ent.VarTail
		fmt.Fprintf(sb, "        { ushort _tlen = DeltaDecoderCore.ReadUint16(snap, o); o += 2; int _tend = o + _tlen;\n")
		fmt.Fprintf(sb, "          while (o < _tend) {\n")
		fmt.Fprintf(sb, "            var _it = new %s();\n", itemName)
		for _, f := range vt.ItemFields {
			writeCsFixedFieldDecode(sb, "_it."+f.Name, f, "            ")
		}
		fmt.Fprintf(sb, "            e.%s.Add(_it);\n", vt.Name)
		sb.WriteString("          } }\n")
	}

	// Initial fields.
	hasInitial := false
	for _, binding := range ent.Bindings {
		for _, f := range binding.Fields {
			if f.Initial {
				hasInitial = true
			}
		}
	}
	if hasInitial {
		sb.WriteString("        int initialOff = 0;\n")
		for _, binding := range ent.Bindings {
			for _, f := range binding.Fields {
				if !f.Initial {
					continue
				}
				writeCsInitialFieldDecode(sb, "e."+f.Name, "existing?."+f.Name, f)
			}
		}
		sb.WriteString("        _ = initialOff;\n")
	}

	sb.WriteString("        return e;\n    }\n\n")
}

// writeCsFixedFieldDecode emits one fixed-layout field decode into `target`.
func writeCsFixedFieldDecode(sb *strings.Builder, target string, f BindingSchemaField, indent string) {
	switch f.Encoding {
	case "f32":
		fmt.Fprintf(sb, "%s%s = DeltaDecoderCore.ReadFloat32(snap, o); o += 4;\n", indent, target)
	case "qvel":
		fmt.Fprintf(sb, "%s%s = (float)DeltaDecoderCore.UnVel(DeltaDecoderCore.ReadInt16(snap, o), %g); o += 2;\n", indent, target, f.Scale)
	case "qangle":
		fmt.Fprintf(sb, "%s%s = (float)DeltaDecoderCore.UnAngle(DeltaDecoderCore.ReadUint16(snap, o)); o += 2;\n", indent, target)
	case "qnorm":
		fmt.Fprintf(sb, "%s%s = (float)DeltaDecoderCore.UnNorm(snap[o]); o += 1;\n", indent, target)
	case "u8":
		fmt.Fprintf(sb, "%s%s = snap[o]; o += 1;\n", indent, target)
	case "u16":
		fmt.Fprintf(sb, "%s%s = DeltaDecoderCore.ReadUint16(snap, o); o += 2;\n", indent, target)
	case "u32":
		fmt.Fprintf(sb, "%s%s = DeltaDecoderCore.ReadUint32(snap, o); o += 4;\n", indent, target)
	case "i16":
		fmt.Fprintf(sb, "%s%s = DeltaDecoderCore.ReadInt16(snap, o); o += 2;\n", indent, target)
	case "bool":
		fmt.Fprintf(sb, "%s%s = snap[o] != 0; o += 1;\n", indent, target)
	default:
		panic(fmt.Sprintf("sdkgen csharp: unsupported fixed field encoding %q for %q", f.Encoding, f.Name))
	}
}

// writeCsInitialFieldDecode emits an if/else decode of one initial-only field
// from `initial`+`initialOff`, falling back to `fallback` (existing?.field).
func writeCsInitialFieldDecode(sb *strings.Builder, target, fallback string, f BindingSchemaField) {
	switch f.Encoding {
	case "string":
		fmt.Fprintf(sb, "        if (initial != null && initialOff < initial.Length) { int _l = initial[initialOff]; %s = Encoding.UTF8.GetString(initial, initialOff + 1, _l); initialOff += 1 + _l; } else { %s = %s ?? \"\"; }\n", target, target, fallback)
	case "u8":
		fmt.Fprintf(sb, "        if (initial != null && initialOff < initial.Length) { %s = initial[initialOff]; initialOff += 1; } else { %s = %s ?? (byte)0; }\n", target, target, fallback)
	case "i8":
		fmt.Fprintf(sb, "        if (initial != null && initialOff < initial.Length) { %s = (sbyte)initial[initialOff]; initialOff += 1; } else { %s = %s ?? (sbyte)0; }\n", target, target, fallback)
	case "u16":
		fmt.Fprintf(sb, "        if (initial != null && initialOff + 2 <= initial.Length) { %s = DeltaDecoderCore.ReadUint16(initial, initialOff); initialOff += 2; } else { %s = %s ?? (ushort)0; }\n", target, target, fallback)
	case "i16":
		fmt.Fprintf(sb, "        if (initial != null && initialOff + 2 <= initial.Length) { %s = DeltaDecoderCore.ReadInt16(initial, initialOff); initialOff += 2; } else { %s = %s ?? (short)0; }\n", target, target, fallback)
	case "u32":
		fmt.Fprintf(sb, "        if (initial != null && initialOff + 4 <= initial.Length) { %s = DeltaDecoderCore.ReadUint32(initial, initialOff); initialOff += 4; } else { %s = %s ?? 0u; }\n", target, target, fallback)
	case "f32":
		fmt.Fprintf(sb, "        if (initial != null && initialOff + 4 <= initial.Length) { %s = DeltaDecoderCore.ReadFloat32(initial, initialOff); initialOff += 4; } else { %s = %s ?? 0f; }\n", target, target, fallback)
	case "bool":
		fmt.Fprintf(sb, "        if (initial != null && initialOff < initial.Length) { %s = initial[initialOff] != 0; initialOff += 1; } else { %s = %s ?? false; }\n", target, target, fallback)
	default:
		fmt.Fprintf(sb, "        %s = %s ?? default;\n", target, fallback)
	}
}
```

- [ ] **Step 2: Wire `DeltaDecoder.cs` into `OutputFiles`** in `cmd/sdkgen/backend_csharp.go` — inside the `if len(schema.Entities) > 0 {` block (next to EntityType/Entities):

```go
		files["DeltaDecoder.cs"] = func() string { return b.genDeltaDecoder(schema) }
```

- [ ] **Step 3: Add an emitter test** to `cmd/sdkgen/backend_csharp_test.go`:

```go
func TestCsharpBackend_DeltaDecoder(t *testing.T) {
	out := csharpBackend{namespace: "Mmokit.Sdk"}.genDeltaDecoder(sampleEntitySchema())
	for _, want := range []string{
		"public sealed class DemoDeltaDecoder",                 // titleCase("demo")
		"public DeltaWorldUpdate Decode(byte[] data)",
		"static ShipEntity DecodeShipSnapshot(byte[] snap, byte[]? initial, ShipEntity? existing)",
		"e.x = DeltaDecoderCore.ReadFloat32(snap, o); o += 4;", // f32
		"e.vx = (float)DeltaDecoderCore.UnVel(DeltaDecoderCore.ReadInt16(snap, o), 0.01); o += 2;", // qvel + scale
		"e.statusEffects.Add(_it);",                            // var-tail
		"int initialOff = 0;",                                  // initial fields present (shipName)
		"Encoding.UTF8.GetString(initial, initialOff + 1, _l)", // initial string
		"case 0: { var e = DecodeShipSnapshot(snap, initial, existing as ShipEntity);",
		"static bool HasVarTailFor(byte type_)",
		"case 0: return true;",                                 // Ship has a var tail
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("genDeltaDecoder missing %q in:\n%s", want, out)
		}
	}
}

func TestCsharpBackend_OutputFiles_IncludesDeltaDecoder(t *testing.T) {
	files := csharpBackend{namespace: "Mmokit.Sdk"}.OutputFiles(sampleEntitySchema())
	if _, ok := files["DeltaDecoder.cs"]; !ok {
		t.Fatalf("OutputFiles missing DeltaDecoder.cs")
	}
}
```

- [ ] **Step 4: Verify (Go) + commit**

Run: `go vet ./cmd/sdkgen/... && go test ./cmd/sdkgen/ 2>&1 | tail -5`
Expected: vet clean; all tests pass.

```bash
git add cmd/sdkgen/backend_csharp_delta.go cmd/sdkgen/backend_csharp.go cmd/sdkgen/backend_csharp_test.go
git commit -m "feat(sdkgen): csharp DeltaDecoder.cs (per-entity world-delta decode)

Port of generate.go::genDeltaDecoder: per-kind snapshot decoders (fixed
f32/qvel/qangle/qnorm/int/bool fields, var-tail List items, initial fields
w/ existing-fallback) + a <Game>DeltaDecoder driving full/delta frames over
DeltaDecoderCore + BaselineStore. Wired into OutputFiles.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Compile gate the generated decoder

The Plan 5 compile gate emits ALL of `OutputFiles` and copies `CoreFiles`. With `DeltaDecoder.cs` now in `OutputFiles` and the sample schema exercising `qvel` + an initial `string` + a var-tail, the gate now compiles the decoder against `Entities.cs` + `DeltaDecoderCore`.

**Files:** none (verification) — unless the gate surfaces an emitter bug to fix in `backend_csharp_delta.go`.

- [ ] **Step 1: Run the compile gate**

Run: `go test -tags=csharptest ./cmd/sdkgen/ -run TestCsharpSdk_Compiles -v 2>&1 | tail -30`
Expected: PASS — `DeltaDecoder.cs` compiles against `Entities.cs`, `EntityType.cs`, `Events/Inputs/Operations.cs`, and the `_core` files.

**If `dotnet build` fails:** the errors point at a `backend_csharp_delta.go` emitter bug. Likely suspects and fixes (fix the generator, never weaken the gate):
- A `DeltaDecoderCore` method name/signature mismatch (e.g. `ReadUint16` vs `ReadUInt16`, `UnVel(int,double)` arg types). Cross-check against `csharp/Mmokit.Sdk.Core/DeltaDecoderCore.cs`.
- An entity field-type mismatch (e.g. assigning a `double` where `Entities.cs` declared `float` — the `(float)` casts on `qvel`/`qangle`/`qnorm` handle this; verify they're present).
- `BaselineStore.TryGet` signature (`out byte[] snapshot, out T? meta`) — confirm the call shape.
- Nullable: `byte[]? initial`, `EntityBase? existing`, `EntityBase? LastEntity` need the library's `<Nullable>enable</Nullable>` (it's on). `existing as <Name>Entity` yields a nullable; `existing?.field` in the fallback is correct.
- `DeltaWorldUpdate.Removed`/`Exited` are `List<uint>`; `DecodeRemovedIDs` returns `uint[]` → wrapped via `new List<uint>(removed)`.

Re-run until it compiles; commit any generator fix with a `fix(sdkgen):` message describing it.

- [ ] **Step 2: Confirm full suites**

Run: `cd csharp && dotnet test 2>&1 | tail -4` → all C# tests pass.
Run: `cd . && go test ./cmd/sdkgen/ 2>&1 | tail -3` → PASS (tagged gate excluded).

- [ ] **Step 3: Commit** (only if Step 1 required an emitter fix; otherwise nothing to commit — note it).

---

## Self-Review

- **Spec coverage (§D DeltaDecoder.cs):** per-entity snapshot decoders (fixed/var-tail/initial) + `<Game>DeltaDecoder` frame driver over `DeltaDecoderCore` + `BaselineStore`, wired into `OutputFiles`, compile-gated against the Plan 5 `Entities.cs`. Decode-correctness against real server frames is the Plan 9 smoke (the compile gate proves structural validity here). ✅
- **Placeholder scan:** Complete emitter code; the compile gate reuses Plan 5's harness. ✅
- **Type/name consistency:** `DeltaDecoderCore` methods (`ReadFloat32`/`ReadInt16`/`ReadUint16`/`ReadUint32`, `UnVel`/`UnAngle`/`UnNorm`, `DecodeFrameHeader`/`DecodeFullEntry`/`DecodeDeltaEntry`/`DecodeRemovedIDs`, `ApplyDelta`, `BaselineStore<T>`, `FrameFlagFreshSnapshot`) match Plan 3's `DeltaDecoderCore.cs`. Entity class + field names (`csharpEntityName`/`csharpTailItemName`) match Plan 5's `Entities.cs`. `splitVarTailLayout` mirrors the TS `-1`-strip. `joinInts` reused from `generate.go`. The `(float)` casts reconcile `UnVel/UnAngle/UnNorm`'s `double` return with the `float` entity fields. ✅

## Open items (resolve during planning, not blocking)

- Strings are initial-only (never in the fixed layout or var-tail items) — matches the server codec; the fixed/var-tail decoders panic on `string` to enforce it.
- Decode correctness vs. live server frames is validated by the Plan 9 end-to-end smoke; this plan's compile gate proves the decoder builds against the generated entity model.
