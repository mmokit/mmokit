# C# SDK — Plan 6: Reflect-codec helper + Events/Inputs/Operations emitters

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Emit the typed-message surface of the C# SDK — `Events.cs` (server-event/broadcast decode + dispatcher), `Inputs.cs` (client-input encode), `Operations.cs` (op Request encode + Response decode) — over a hand-written `ReflectWriter`/`ReflectReader` runtime helper, cross-checked against Go's actual `ReflectMarshal`.

**Architecture:** A hand-written `ReflectCodec.cs` in `Mmokit.Sdk.Core` provides `ReflectWriter` (growable little-endian buffer) and `ReflectReader` (LE cursor) with one method per wire primitive (`WriteF32`/`ReadF32`, `WriteString`/`ReadString`, `WriteSliceLen`/`ReadSliceLen`, …). This is a cleaner port than the TS DataView approach: the generated `Encode`/`Decode` just call helper methods (the writer grows itself — no fragile size pass). `csharpBackend` gains `genEvents`/`genInputs`/`genOperations` that walk `BroadcastFieldSchema` and emit those calls. The wire layout mirrors `pkg/universe/reflect_marshal.go` (LE, source-field order, no padding; string = u16-len + UTF-8; bytes = u32-len + raw; slice = u16-len + elements; bool = 1 byte; entity = 4-byte NetID). A golden case produced by Go's real `ReflectMarshal` cross-checks the C# helper byte-for-byte.

**Scope cut:** Plan 6 handles the **flat** reflect codec — all scalars, `string`, `bytes`, `bool`, `entity`, and **slices of scalars/strings**. Nested **struct** fields and **slices of structs** are explicitly NOT handled: the emitter raises a clear `panic` ("struct fields not yet supported by csharpBackend") so any schema needing them fails loudly rather than mis-generating. This fully covers the C# target: the auth ops (`AuthLoginRequest{Username,Password,MFACode}`, `AuthLoginResponse{… string/int64/uint32 …}`, etc.) are entirely flat, and 4node registers no struct-bearing reflect types. Struct support is a deliberate follow-up (Plan 6b) if a future target needs it.

**Tech Stack:** C# (`netstandard2.1`), Go (`cmd/sdkgen` emitters + `cmd/csharp-golden` extension using `pkg/universe.ReflectMarshal`), `dotnet`.

**Spec:** [docs/superpowers/specs/2026-06-06-csharp-sdk-unity-design.md](../specs/2026-06-06-csharp-sdk-unity-design.md) §D (Events/Inputs/Operations.cs). Builds on Plan 5's `csharpBackend`.

**Prerequisites:** Plan 5 merged (`csharpBackend` with `OutputFiles`/`CoreFiles`, the compile gate, `sampleEntitySchema()`).

---

## Background facts (verified)

- Reflect-codec wire layout (`pkg/universe/reflect_marshal.go`, confirmed via the TS emitters `broadcasts.go`/`inputs.go`): little-endian, fields in source order, no padding. `string` = `u16` byte-length + UTF-8. `bytes` = `u32` byte-length + raw. `slice` = `u16` element-count + elements. `bool` = 1 byte (0/1). `entity` = `u32` NetID. `f32`=4, `f64`=8, `u8/i8`=1, `u16/i16`=2, `u32/i32`=4, `u64/i64`=8.
- `pkg/universe.ReflectMarshal(ptr any) []byte` is standalone (pure reflection: `structSize` + `marshalStruct`; no Stage, no registration) — safe to call from the golden generator on a plain flat struct.
- Schema types: `BroadcastTypeSchema{Name string, TypeID uint32, Fields []BroadcastFieldSchema}`; `ClientInputTypeSchema = ServerEventTypeSchema = BroadcastTypeSchema`; `OperationSchema{RequestTypeID/Name, RequestFields, ResponseTypeID/Name, ResponseFields}`; `BroadcastFieldSchema{Name, Encoding string, Size int, Fields []BroadcastFieldSchema, Item *BroadcastFieldSchema}`.
- TS class-name helper `broadcastClassName` strips the package prefix: `"game.Damage"` → `"Damage"`. The C# emitter needs the same (`csReflectClassName`).
- `main.go` emits files from `backend.OutputFiles(schema)`; Plan 5's csharp `OutputFiles` currently returns only EntityType/Entities (when entities exist). Plan 6 adds Events/Inputs/Operations gated on their registries (mirroring the TS `tsBackend.OutputFiles` conditions).

---

## File Structure

- **Create:** `csharp/Mmokit.Sdk.Core/ReflectCodec.cs` — `ReflectWriter` + `ReflectReader`.
- **Create:** `csharp/Mmokit.Sdk.Core.Tests/ReflectCodecGoldenTests.cs` — golden cross-check vs Go.
- **Modify:** `cmd/csharp-golden/main.go` — emit a `reflect` golden case via `universe.ReflectMarshal`.
- **Modify:** `csharp/Mmokit.Sdk.Core.Tests/GoldenModel.cs` — add `reflect` DTO.
- **Create:** `cmd/sdkgen/backend_csharp_reflect.go` — shared C# reflect-codec field emitters + `genEvents`/`genInputs`/`genOperations`.
- **Modify:** `cmd/sdkgen/backend_csharp.go` — add the 5 core files (`ReflectCodec.cs`) to `CoreFiles`; wire the 3 new emitters into `OutputFiles`.
- **Modify:** `cmd/sdkgen/backend_csharp_test.go` — extend the sample schema + add emitter assertions.
- **Modify:** `cmd/sdkgen/csharp_compile_test.go` — already emits all `OutputFiles`; just ensure the sample has broadcast/input/op types (via the shared sample).

---

### Task 1: ReflectWriter/Reader runtime helper + Go-authoritative golden

**Files:**
- Create: `csharp/Mmokit.Sdk.Core/ReflectCodec.cs`
- Modify: `cmd/csharp-golden/main.go`, `csharp/Mmokit.Sdk.Core.Tests/GoldenModel.cs`
- Create: `csharp/Mmokit.Sdk.Core.Tests/ReflectCodecGoldenTests.cs`

- [ ] **Step 1: Create `csharp/Mmokit.Sdk.Core/ReflectCodec.cs`:**

```csharp
using System;
using System.Collections.Generic;
using System.Text;

namespace Mmokit.Sdk.Core
{
    /// Little-endian growable writer for the reflect-codec wire format
    /// (pkg/universe/reflect_marshal.go). Field order is the caller's
    /// responsibility; this only writes primitives. Mirrors the layout the
    /// TS SDK encodes — string=u16+utf8, bytes=u32+raw, slice-len=u16, bool=1B.
    public sealed class ReflectWriter
    {
        readonly List<byte> _b = new();

        public byte[] ToArray() => _b.ToArray();

        public void WriteU8(byte v) => _b.Add(v);
        public void WriteI8(sbyte v) => _b.Add((byte)v);
        public void WriteU16(ushort v) { _b.Add((byte)v); _b.Add((byte)(v >> 8)); }
        public void WriteI16(short v) => WriteU16((ushort)v);
        public void WriteU32(uint v) { for (int i = 0; i < 4; i++) _b.Add((byte)(v >> (8 * i))); }
        public void WriteI32(int v) => WriteU32((uint)v);
        public void WriteU64(ulong v) { for (int i = 0; i < 8; i++) _b.Add((byte)(v >> (8 * i))); }
        public void WriteI64(long v) => WriteU64((ulong)v);
        public void WriteF32(float v) => WriteU32((uint)BitConverter.SingleToInt32Bits(v));
        public void WriteF64(double v) => WriteU64((ulong)BitConverter.DoubleToInt64Bits(v));
        public void WriteBool(bool v) => _b.Add((byte)(v ? 1 : 0));
        public void WriteEntity(uint netID) => WriteU32(netID);

        public void WriteString(string s)
        {
            byte[] bytes = Encoding.UTF8.GetBytes(s ?? "");
            WriteU16((ushort)bytes.Length);
            _b.AddRange(bytes);
        }

        public void WriteBytes(byte[] data)
        {
            int n = data?.Length ?? 0;
            WriteU32((uint)n);
            if (n > 0) _b.AddRange(data!);
        }

        /// slice element-count prefix (u16). Caller writes the elements after.
        public void WriteSliceLen(int n) => WriteU16((ushort)n);
    }

    /// Little-endian cursor reader, inverse of ReflectWriter.
    public sealed class ReflectReader
    {
        readonly byte[] _b;
        int _o;

        public ReflectReader(byte[] data) { _b = data; _o = 0; }

        public byte ReadU8() => _b[_o++];
        public sbyte ReadI8() => (sbyte)_b[_o++];
        public ushort ReadU16() { ushort v = (ushort)(_b[_o] | (_b[_o + 1] << 8)); _o += 2; return v; }
        public short ReadI16() => (short)ReadU16();
        public uint ReadU32() { uint v = (uint)_b[_o] | ((uint)_b[_o + 1] << 8) | ((uint)_b[_o + 2] << 16) | ((uint)_b[_o + 3] << 24); _o += 4; return v; }
        public int ReadI32() => (int)ReadU32();
        public ulong ReadU64() { ulong v = 0; for (int i = 0; i < 8; i++) v |= (ulong)_b[_o + i] << (8 * i); _o += 8; return v; }
        public long ReadI64() => (long)ReadU64();
        public float ReadF32() => BitConverter.Int32BitsToSingle((int)ReadU32());
        public double ReadF64() => BitConverter.Int64BitsToDouble((long)ReadU64());
        public bool ReadBool() => _b[_o++] != 0;
        public uint ReadEntity() => ReadU32();

        public string ReadString()
        {
            int n = ReadU16();
            string s = Encoding.UTF8.GetString(_b, _o, n);
            _o += n;
            return s;
        }

        public byte[] ReadBytes()
        {
            int n = (int)ReadU32();
            var r = new byte[n];
            Array.Copy(_b, _o, r, 0, n);
            _o += n;
            return r;
        }

        public int ReadSliceLen() => ReadU16();
    }
}
```

- [ ] **Step 2: Extend the golden generator** — in `cmd/csharp-golden/main.go`: add `"github.com/mmokit/mmokit/pkg/universe"` to imports; add `Reflect ReflectCase \`json:"reflect"\`` to the `Manifest` struct; add the DTO:

```go
type ReflectCase struct {
	HexBytes string   `json:"hexBytes"`
	A        float32  `json:"a"`
	B        uint32   `json:"b"`
	C        string   `json:"c"`
	D        bool     `json:"d"`
	E        int64    `json:"e"`
	F        []uint32 `json:"f"`
}

// goldenReflect is marshalled by the real reflect codec to anchor the C#
// ReflectReader/Writer to Go's actual wire bytes. All fields flat (the
// encodings the C# emitter supports): f32, u32, string, bool, i64, []u32.
type goldenReflect struct {
	A float32
	B uint32
	C string
	D bool
	E int64
	F []uint32
}
```

Then, before the `out := filepath.Join(...)` line in `main()`, populate it:

```go
	gr := goldenReflect{A: 1.5, B: 0xDEADBEEF, C: "héllo", D: true, E: -42, F: []uint32{7, 8, 9}}
	m.Reflect = ReflectCase{
		HexBytes: hex.EncodeToString(universe.ReflectMarshal(&gr)),
		A:        gr.A, B: gr.B, C: gr.C, D: gr.D, E: gr.E, F: gr.F,
	}
```

- [ ] **Step 3: Regenerate the manifest**

Run: `go vet ./cmd/csharp-golden/... && go run ./cmd/csharp-golden`
Expected: vet clean; manifest written. Confirm the `reflect` key exists: `grep -c '"reflect"' csharp/Mmokit.Sdk.Core.Tests/testdata/delta_golden.json` → ≥1.

- [ ] **Step 4: Add the C# DTO** — append to `GoldenModel.cs`'s `Manifest` class `public ReflectCase Reflect { get; set; } = new();` and add:

```csharp
    public class ReflectCase
    {
        public string HexBytes { get; set; } = "";
        public float A { get; set; }
        public uint B { get; set; }
        public string C { get; set; } = "";
        public bool D { get; set; }
        public long E { get; set; }
        public uint[] F { get; set; } = Array.Empty<uint>();
    }
```

- [ ] **Step 5: Write the golden cross-check** `csharp/Mmokit.Sdk.Core.Tests/ReflectCodecGoldenTests.cs`:

```csharp
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    public class ReflectCodecGoldenTests
    {
        readonly Manifest g = Golden.Load();

        // ReflectReader must decode Go's actual ReflectMarshal bytes to the
        // expected field values (in source order: f32, u32, string, bool, i64, []u32).
        [Fact]
        public void Reader_DecodesGoBytes()
        {
            var c = g.Reflect;
            var r = new ReflectReader(Golden.Hex(c.HexBytes));
            Assert.Equal(c.A, r.ReadF32(), 4);
            Assert.Equal(c.B, r.ReadU32());
            Assert.Equal(c.C, r.ReadString());
            Assert.Equal(c.D, r.ReadBool());
            Assert.Equal(c.E, r.ReadI64());
            int n = r.ReadSliceLen();
            Assert.Equal(c.F.Length, n);
            for (int i = 0; i < n; i++) Assert.Equal(c.F[i], r.ReadU32());
        }

        // ReflectWriter must reproduce Go's exact bytes for the same values.
        [Fact]
        public void Writer_ReproducesGoBytes()
        {
            var c = g.Reflect;
            var w = new ReflectWriter();
            w.WriteF32(c.A);
            w.WriteU32(c.B);
            w.WriteString(c.C);
            w.WriteBool(c.D);
            w.WriteI64(c.E);
            w.WriteSliceLen(c.F.Length);
            foreach (uint v in c.F) w.WriteU32(v);
            Assert.Equal(Golden.Hex(c.HexBytes), w.ToArray());
        }
    }
}
```

- [ ] **Step 6: Run + commit**

Run: `cd csharp && dotnet test --filter ReflectCodecGoldenTests 2>&1 | tail -8`
Expected: `Passed!  - Failed: 0, Passed: 2`. (Confirms the C# LE helpers match Go's real `ReflectMarshal`, including the UTF-8 multibyte "héllo".)

```bash
git add csharp/Mmokit.Sdk.Core/ReflectCodec.cs cmd/csharp-golden/main.go csharp/Mmokit.Sdk.Core.Tests/GoldenModel.cs csharp/Mmokit.Sdk.Core.Tests/testdata/delta_golden.json csharp/Mmokit.Sdk.Core.Tests/ReflectCodecGoldenTests.cs
git commit -m "feat(csharp): ReflectWriter/ReflectReader + Go-authoritative golden

Little-endian reflect-codec runtime helper (mirrors reflect_marshal.go) used
by the generated Events/Inputs/Operations classes. Cross-checked byte-for-byte
against Go's real universe.ReflectMarshal (incl. UTF-8 multibyte string).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Events/Inputs/Operations emitters

**Files:**
- Create: `cmd/sdkgen/backend_csharp_reflect.go`
- Modify: `cmd/sdkgen/backend_csharp.go`
- Modify: `cmd/sdkgen/backend_csharp_test.go`

- [ ] **Step 1: Create the shared reflect-codec emitters** `cmd/sdkgen/backend_csharp_reflect.go`:

```go
package main

import (
	"fmt"
	"strings"
)

// csReflectClassName strips the Go package prefix: "game.Damage" -> "Damage".
func csReflectClassName(goName string) string {
	if i := strings.LastIndex(goName, "."); i >= 0 {
		return goName[i+1:]
	}
	return goName
}

// csReflectFieldType maps a reflect-codec field encoding to its C# field type.
// Struct fields and struct-slice items are NOT supported in this plan — the
// emitter panics so a struct-bearing schema fails loudly. Slices of scalars/
// strings map to List<T>.
func csReflectFieldType(f BroadcastFieldSchema) string {
	switch f.Encoding {
	case "f32":
		return "float"
	case "f64":
		return "double"
	case "u8":
		return "byte"
	case "u16":
		return "ushort"
	case "u32", "entity":
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
	case "bool":
		return "bool"
	case "string":
		return "string"
	case "bytes":
		return "byte[]"
	case "slice":
		if f.Item == nil {
			panic(fmt.Sprintf("sdkgen csharp: slice field %q missing item", f.Name))
		}
		if f.Item.Encoding == "struct" || f.Item.Encoding == "slice" {
			panic(fmt.Sprintf("sdkgen csharp: slice-of-%s field %q not yet supported", f.Item.Encoding, f.Name))
		}
		return "List<" + csReflectScalarType(*f.Item) + ">"
	case "struct":
		panic(fmt.Sprintf("sdkgen csharp: struct field %q not yet supported (Plan 6b)", f.Name))
	default:
		panic(fmt.Sprintf("sdkgen csharp: unsupported reflect field encoding %q", f.Encoding))
	}
}

// csReflectScalarType is csReflectFieldType restricted to non-composite items.
func csReflectScalarType(f BroadcastFieldSchema) string {
	switch f.Encoding {
	case "struct", "slice":
		panic(fmt.Sprintf("sdkgen csharp: composite slice item %q not supported", f.Encoding))
	}
	return csReflectFieldType(f)
}

// csReflectFieldInit returns the C# field initializer (so reference-typed
// fields are never null): strings -> "", byte[] -> Array.Empty<byte>(),
// List<T> -> new(). Scalars are left default (no initializer).
func csReflectFieldInit(f BroadcastFieldSchema) string {
	switch f.Encoding {
	case "string":
		return ` = ""`
	case "bytes":
		return " = System.Array.Empty<byte>()"
	case "slice":
		return " = new()"
	default:
		return ""
	}
}

// csReflectReadCall returns the ReflectReader call for a scalar encoding.
func csReflectReadCall(enc string) string {
	switch enc {
	case "f32":
		return "ReadF32()"
	case "f64":
		return "ReadF64()"
	case "u8":
		return "ReadU8()"
	case "u16":
		return "ReadU16()"
	case "u32":
		return "ReadU32()"
	case "u64":
		return "ReadU64()"
	case "i8":
		return "ReadI8()"
	case "i16":
		return "ReadI16()"
	case "i32":
		return "ReadI32()"
	case "i64":
		return "ReadI64()"
	case "entity":
		return "ReadEntity()"
	case "bool":
		return "ReadBool()"
	case "string":
		return "ReadString()"
	case "bytes":
		return "ReadBytes()"
	default:
		panic(fmt.Sprintf("sdkgen csharp: no read call for encoding %q", enc))
	}
}

// csReflectWriteCall returns the ReflectWriter call for a scalar encoding,
// given the value expression `expr`.
func csReflectWriteCall(enc, expr string) string {
	switch enc {
	case "f32":
		return fmt.Sprintf("WriteF32(%s)", expr)
	case "f64":
		return fmt.Sprintf("WriteF64(%s)", expr)
	case "u8":
		return fmt.Sprintf("WriteU8(%s)", expr)
	case "u16":
		return fmt.Sprintf("WriteU16(%s)", expr)
	case "u32":
		return fmt.Sprintf("WriteU32(%s)", expr)
	case "u64":
		return fmt.Sprintf("WriteU64(%s)", expr)
	case "i8":
		return fmt.Sprintf("WriteI8(%s)", expr)
	case "i16":
		return fmt.Sprintf("WriteI16(%s)", expr)
	case "i32":
		return fmt.Sprintf("WriteI32(%s)", expr)
	case "i64":
		return fmt.Sprintf("WriteI64(%s)", expr)
	case "entity":
		return fmt.Sprintf("WriteEntity(%s)", expr)
	case "bool":
		return fmt.Sprintf("WriteBool(%s)", expr)
	case "string":
		return fmt.Sprintf("WriteString(%s)", expr)
	case "bytes":
		return fmt.Sprintf("WriteBytes(%s)", expr)
	default:
		panic(fmt.Sprintf("sdkgen csharp: no write call for encoding %q", enc))
	}
}

// writeCsFieldDecode emits decode for one field into `target` (e.g. "m.foo").
func writeCsFieldDecode(sb *strings.Builder, target string, f BroadcastFieldSchema) {
	if f.Encoding == "slice" {
		item := *f.Item
		fmt.Fprintf(sb, "            { int _n = r.ReadSliceLen(); for (int _i = 0; _i < _n; _i++) %s.Add(r.%s); }\n",
			target, csReflectReadCall(item.Encoding))
		return
	}
	fmt.Fprintf(sb, "            %s = r.%s;\n", target, csReflectReadCall(f.Encoding))
}

// writeCsFieldEncode emits encode for one field from `src` (e.g. "this.foo").
func writeCsFieldEncode(sb *strings.Builder, src string, f BroadcastFieldSchema) {
	if f.Encoding == "slice" {
		item := *f.Item
		fmt.Fprintf(sb, "            w.WriteSliceLen(%s.Count); foreach (var _v in %s) w.%s;\n",
			src, src, csReflectWriteCall(item.Encoding, "_v"))
		return
	}
	fmt.Fprintf(sb, "            w.%s;\n", csReflectWriteCall(f.Encoding, src))
}

// writeCsReflectClass emits one C# class for a reflect-codec type. withEncode
// adds Encode(); withDecode adds static Decode(byte[]).
func writeCsReflectClass(sb *strings.Builder, name string, typeID uint32, fields []BroadcastFieldSchema, withEncode, withDecode bool) {
	fmt.Fprintf(sb, "    /// Reflect-codec message %s (typeID 0x%08x).\n", name, typeID)
	fmt.Fprintf(sb, "    public sealed class %s\n    {\n", name)
	fmt.Fprintf(sb, "        public const uint TypeID = 0x%xu;\n", typeID)
	for _, f := range fields {
		fmt.Fprintf(sb, "        public %s %s%s;\n", csReflectFieldType(f), f.Name, csReflectFieldInit(f))
	}
	sb.WriteString("\n")
	if withDecode {
		fmt.Fprintf(sb, "        public static %s Decode(byte[] buf)\n        {\n", name)
		sb.WriteString("            var r = new ReflectReader(buf);\n")
		fmt.Fprintf(sb, "            var m = new %s();\n", name)
		for _, f := range fields {
			writeCsFieldDecode(sb, "m."+f.Name, f)
		}
		sb.WriteString("            return m;\n        }\n")
	}
	if withEncode {
		if withDecode {
			sb.WriteString("\n")
		}
		sb.WriteString("        public byte[] Encode()\n        {\n")
		sb.WriteString("            var w = new ReflectWriter();\n")
		for _, f := range fields {
			writeCsFieldEncode(sb, "this."+f.Name, f)
		}
		sb.WriteString("            return w.ToArray();\n        }\n")
	}
	sb.WriteString("    }\n\n")
}

// genEvents emits Events.cs: a decode class per broadcast + server-event type,
// plus a TypedDispatcher keyed on TypeID.
func (b csharpBackend) genEvents(schema ProtocolSchema) string {
	var sb strings.Builder
	sb.WriteString("// GENERATED by sdkgen — do not edit.\n\n")
	sb.WriteString("using System;\nusing System.Collections.Generic;\nusing Mmokit.Sdk.Core;\n\n")
	fmt.Fprintf(&sb, "namespace %s\n{\n", b.namespace)
	for _, bt := range schema.BroadcastTypes {
		writeCsReflectClass(&sb, csReflectClassName(bt.Name), bt.TypeID, bt.Fields, false, true)
	}
	for _, st := range schema.ServerEventTypes {
		writeCsReflectClass(&sb, csReflectClassName(st.Name), st.TypeID, st.Fields, false, true)
	}
	// Dispatcher: register a typed handler keyed on TypeID; dispatch decodes
	// the body and invokes it.
	sb.WriteString("    /// Routes incoming typed-event frames (typeID + body) to typed handlers.\n")
	sb.WriteString("    public sealed class TypedDispatcher\n    {\n")
	sb.WriteString("        readonly Dictionary<uint, Action<byte[]>> _handlers = new();\n\n")
	sb.WriteString("        /// Register a decode+handle for events of TypeID. Returns an unsubscribe.\n")
	sb.WriteString("        public Action On(uint typeID, Action<byte[]> handler)\n        {\n")
	sb.WriteString("            _handlers[typeID] = handler;\n")
	sb.WriteString("            return () => { if (_handlers.TryGetValue(typeID, out var h) && h == handler) _handlers.Remove(typeID); };\n")
	sb.WriteString("        }\n\n")
	sb.WriteString("        /// Dispatch one wire event. No-op if no handler is registered.\n")
	sb.WriteString("        public void Dispatch(uint typeID, byte[] body)\n        {\n")
	sb.WriteString("            if (_handlers.TryGetValue(typeID, out var h)) h(body);\n")
	sb.WriteString("        }\n")
	sb.WriteString("    }\n}\n")
	return sb.String()
}

// genInputs emits Inputs.cs: an encode class per client-input type.
func (b csharpBackend) genInputs(schema ProtocolSchema) string {
	var sb strings.Builder
	sb.WriteString("// GENERATED by sdkgen — do not edit.\n\n")
	sb.WriteString("using System;\nusing System.Collections.Generic;\nusing Mmokit.Sdk.Core;\n\n")
	fmt.Fprintf(&sb, "namespace %s\n{\n", b.namespace)
	for _, ct := range schema.ClientInputTypes {
		writeCsReflectClass(&sb, csReflectClassName(ct.Name), ct.TypeID, ct.Fields, true, false)
	}
	sb.WriteString("}\n")
	return sb.String()
}

// genOperations emits Operations.cs: a Request (encode+decode) + Response
// (decode) class per op, deduped by class name.
func (b csharpBackend) genOperations(schema ProtocolSchema) string {
	var sb strings.Builder
	sb.WriteString("// GENERATED by sdkgen — do not edit.\n\n")
	sb.WriteString("using System;\nusing System.Collections.Generic;\nusing Mmokit.Sdk.Core;\n\n")
	fmt.Fprintf(&sb, "namespace %s\n{\n", b.namespace)
	emitted := map[string]struct{}{}
	emit := func(name string, typeID uint32, fields []BroadcastFieldSchema, withEncode bool) {
		if _, dup := emitted[name]; dup {
			return
		}
		emitted[name] = struct{}{}
		writeCsReflectClass(&sb, name, typeID, fields, withEncode, true)
	}
	for _, op := range schema.Operations {
		emit(csReflectClassName(op.RequestTypeName), op.RequestTypeID, op.RequestFields, true)
		emit(csReflectClassName(op.ResponseTypeName), op.ResponseTypeID, op.ResponseFields, false)
	}
	sb.WriteString("}\n")
	return sb.String()
}
```

- [ ] **Step 2: Wire the emitters + core file in `cmd/sdkgen/backend_csharp.go`.** In `CoreFiles`, add `"ReflectCodec.cs"` to the `names` slice (after `InterpolationCore.cs`). In `OutputFiles`, after the entity block, add:

```go
	if len(schema.BroadcastTypes) > 0 || len(schema.ServerEventTypes) > 0 {
		files["Events.cs"] = func() string { return b.genEvents(schema) }
	}
	if len(schema.ClientInputTypes) > 0 {
		files["Inputs.cs"] = func() string { return b.genInputs(schema) }
	}
	if len(schema.Operations) > 0 {
		files["Operations.cs"] = func() string { return b.genOperations(schema) }
	}
```

- [ ] **Step 3: Extend the sample schema + add emitter tests** in `cmd/sdkgen/backend_csharp_test.go`. Add broadcast/input/op types to `sampleEntitySchema` by replacing its `return ProtocolSchema{...}` with a version that also sets:

```go
		BroadcastTypes: []BroadcastTypeSchema{
			{Name: "game.Damage", TypeID: 0xA1B2C3D4, Fields: []BroadcastFieldSchema{
				{Name: "amount", Encoding: "f32"},
				{Name: "victim", Encoding: "entity"},
			}},
		},
		ServerEventTypes: []ServerEventTypeSchema{
			{Name: "game.Pong", TypeID: 0x11223344, Fields: []BroadcastFieldSchema{
				{Name: "nonce", Encoding: "u32"},
			}},
		},
		ClientInputTypes: []ClientInputTypeSchema{
			{Name: "game.SetMoveTarget", TypeID: 0x55667788, Fields: []BroadcastFieldSchema{
				{Name: "x", Encoding: "f32"},
				{Name: "y", Encoding: "f32"},
				{Name: "tags", Encoding: "slice", Item: &BroadcastFieldSchema{Name: "tag", Encoding: "u32"}},
			}},
		},
		Operations: []OperationSchema{
			{
				RequestTypeName: "auth.AuthLoginRequest", RequestTypeID: 0xAA01,
				RequestFields: []BroadcastFieldSchema{{Name: "Username", Encoding: "string"}, {Name: "Password", Encoding: "string"}},
				ResponseTypeName: "auth.AuthLoginResponse", ResponseTypeID: 0xAA02,
				ResponseFields: []BroadcastFieldSchema{{Name: "SessionToken", Encoding: "string"}, {Name: "ExpiresAtMs", Encoding: "i64"}},
			},
		},
```

(Keep the existing `Game` + `Entities` fields.) Then add these tests:

```go
func TestCsharpBackend_Events(t *testing.T) {
	out := csharpBackend{namespace: "Mmokit.Sdk"}.genEvents(sampleEntitySchema())
	for _, want := range []string{
		"public sealed class Damage",
		"public const uint TypeID = 0xa1b2c3d4u;",
		"public float amount;",
		"public uint victim;", // entity → uint
		"public static Damage Decode(byte[] buf)",
		"m.amount = r.ReadF32();",
		"m.victim = r.ReadEntity();",
		"public sealed class Pong",
		"public sealed class TypedDispatcher",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("genEvents missing %q in:\n%s", want, out)
		}
	}
}

func TestCsharpBackend_Inputs(t *testing.T) {
	out := csharpBackend{namespace: "Mmokit.Sdk"}.genInputs(sampleEntitySchema())
	for _, want := range []string{
		"public sealed class SetMoveTarget",
		"public byte[] Encode()",
		"w.WriteF32(this.x);",
		"public List<uint> tags = new();",
		"w.WriteSliceLen(this.tags.Count); foreach (var _v in this.tags) w.WriteU32(_v);",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("genInputs missing %q in:\n%s", want, out)
		}
	}
}

func TestCsharpBackend_Operations(t *testing.T) {
	out := csharpBackend{namespace: "Mmokit.Sdk"}.genOperations(sampleEntitySchema())
	for _, want := range []string{
		"public sealed class AuthLoginRequest",
		"public byte[] Encode()",            // request encodes
		"public static AuthLoginRequest Decode(byte[] buf)",
		"public sealed class AuthLoginResponse",
		"w.WriteString(this.Username);",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("genOperations missing %q in:\n%s", want, out)
		}
	}
	// Response is decode-only (no Encode()).
	if strings.Contains(out, "class AuthLoginResponse") &&
		strings.Contains(out[strings.Index(out, "class AuthLoginResponse"):], "public byte[] Encode()") {
		t.Fatalf("AuthLoginResponse should be decode-only (no Encode)")
	}
}

func TestCsharpBackend_OutputFiles_IncludesReflectFiles(t *testing.T) {
	files := csharpBackend{namespace: "Mmokit.Sdk"}.OutputFiles(sampleEntitySchema())
	for _, name := range []string{"EntityType.cs", "Entities.cs", "Events.cs", "Inputs.cs", "Operations.cs"} {
		if _, ok := files[name]; !ok {
			t.Fatalf("OutputFiles missing %s", name)
		}
	}
}
```

- [ ] **Step 4: Verify + commit**

Run: `go vet ./cmd/sdkgen/... && go test ./cmd/sdkgen/ 2>&1 | tail -5`
Expected: vet clean; all tests pass (Plan 5 tests still green + the 4 new emitter tests).

```bash
git add cmd/sdkgen/backend_csharp_reflect.go cmd/sdkgen/backend_csharp.go cmd/sdkgen/backend_csharp_test.go
git commit -m "feat(sdkgen): csharp Events/Inputs/Operations reflect-codec emitters

genEvents (decode + TypedDispatcher), genInputs (encode), genOperations
(Request encode+decode, Response decode), over ReflectReader/Writer. Flat
codec + scalar/string slices; struct nesting panics (deferred). ReflectCodec.cs
added to CoreFiles; OutputFiles emits the three files schema-gated.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Compile-gate the reflect-codec output

The Plan 5 compile gate already emits ALL of `OutputFiles` + copies `CoreFiles`. Since Task 2 extended the shared `sampleEntitySchema()` with broadcast/input/op types and added `ReflectCodec.cs` to `CoreFiles`, the existing gate now compiles `Events.cs`/`Inputs.cs`/`Operations.cs` + `ReflectCodec.cs` too. This task verifies that and locks it in.

**Files:** none (verification) — unless the gate surfaces an emitter bug to fix in `backend_csharp_reflect.go`.

- [ ] **Step 1: Run the compile gate**

Run: `go test -tags=csharptest ./cmd/sdkgen/ -run TestCsharpSdk_Compiles -v 2>&1 | tail -25`
Expected: PASS — the generated `EntityType/Entities/Events/Inputs/Operations.cs` + the six copied `_core` files (now including `ReflectCodec.cs`) compile as one `netstandard2.1` assembly.

**If `dotnet build` fails:** the errors point at an emitter bug in `backend_csharp_reflect.go` (e.g. a bad C# type, a missing `using`, an entity-field type mismatch). Fix the generator, re-run `go test -tags=csharptest` until it compiles. Do NOT weaken the gate. Common things to check: `Mmokit.Sdk.Core` `using` is present in each generated file (the emitters write it); `List<>` needs `using System.Collections.Generic;` (the emitters write it); the `TypedDispatcher`'s `Action`/`Dictionary` need `System`/`System.Collections.Generic` (written).

- [ ] **Step 2: Confirm the full C# suite + default Go tests**

Run: `cd csharp && dotnet test 2>&1 | tail -4`
Expected: all C# tests pass (Plan 3/4/5 + the 2 new ReflectCodec golden tests).

Run: `cd . && go test ./cmd/sdkgen/ 2>&1 | tail -3`
Expected: PASS (the csharptest-tagged gate excluded).

- [ ] **Step 3: Commit (only if Step 1 required an emitter fix)**

If you fixed `backend_csharp_reflect.go`:
```bash
git add cmd/sdkgen/backend_csharp_reflect.go
git commit -m "fix(sdkgen): correct C# reflect-codec emission surfaced by compile gate

<describe the specific fix>

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```
If the gate passed with no changes, there is nothing to commit — note it in your report.

---

## Self-Review

- **Spec coverage (§D):** Events.cs (decode + TypedDispatcher), Inputs.cs (encode), Operations.cs (Req encode+decode / Res decode) over the `ReflectWriter`/`ReflectReader` runtime helper; wire layout cross-checked against Go's real `ReflectMarshal` (Task 1). The flat-codec scope cut is explicit (struct panics) and covers the flat auth ops + 4node target; struct support is a documented Plan 6b follow-up. ✅
- **Placeholder scan:** Complete code in every step. The only generated artifact (`delta_golden.json` with a `reflect` case) is produced by the fully-specified generator extension; the compile gate reuses Plan 5's harness. ✅
- **Type/name consistency:** `ReflectWriter`/`ReflectReader` method names used by the generated code (`csReflectReadCall`/`csReflectWriteCall`) exactly match the helper's public methods. `csReflectClassName` mirrors the TS `broadcastClassName`. The emitter test assertions (`ReadF32`/`ReadEntity`/`WriteSliceLen`/`WriteString`) match the emitted calls. `genEvents`/`genInputs`/`genOperations` are methods on `csharpBackend` and are wired in `OutputFiles`. `ReflectCodec.cs` is in `CoreFiles`. ✅
- **DRY:** one `writeCsReflectClass` + shared field emitters drive all three files; the generated classes share the one hand-written `ReflectCodec.cs`. ✅

## Open items (resolve during planning, not blocking)

- Struct fields / struct-slice items panic (deferred to a Plan 6b). If the real `--dump-schema` for 4node surfaces a struct-bearing reflect type (none known), that plan is pulled forward.
- `entity` fields decode to a bare `uint` NetID (matching the TS `number`); resolving it to a live entity reference is the consumer's job (stateless SDK).
- The op-call wrapper + response dispatch (`callOp`, `handleOperation`) and the typed event subscription glue live in `Client.cs` (Plan 7), not here — Operations.cs only defines the Req/Res types.
