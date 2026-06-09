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
