package main

import (
	"fmt"
	"strings"
)

// genServerEvents emits the TypeScript file containing one class per
// RegisterEvent[T]-registered Go type. Each class exposes:
//
//   - static readonly typeID (uint32, matches mmokit.TypeIDOf on the server)
//   - per-field public properties initialized to a zero value
//   - static decode(buf): ClassName — produces a populated instance from the
//     reflect-codec body bytes the server's afterSend path emits
//
// The wire layout mirrors the reflect_marshal codec defined in
// pkg/universe/reflect_marshal.go: fields encoded in source declaration
// order, no padding, little-endian. mmokit.Entity encodes as a 4-byte
// NetID via the registered reflect codec.
//
// The full server→client typed-event frame layout (one frame may carry
// many entries) is:
//
//	[byte 0x00][repeated: u32 typeID][u32 bodyLen][body bytes]
//
// The TS class only decodes a single body; the framing + per-event
// dispatch lives in client.ts handleEvent.
//
// Output schema (per type):
//
//	export class PlayerSpawned {
//	  static readonly typeID = 0x12345678;
//	  netID: number = 0;
//	  ...
//	  static decode(buf: Uint8Array): PlayerSpawned { ... }
//	}
//
// Reuses writeBroadcastClass from broadcasts.go: the schema shapes are
// identical (both ServerEventTypeSchema and BroadcastTypeSchema alias
// BroadcastTypeSchema in mmokit) and the on-wire encoding is identical.
// Emitted into broadcasts.ts so callers import everything typed-event-related
// from one file and so the file is unified with the broadcast-dispatcher
// machinery.
func writeServerEventClasses(b *strings.Builder, types []ServerEventTypeSchema) {
	for _, st := range types {
		// Shape is identical to a broadcast class; reuse writeBroadcastClass
		// to avoid duplicating the field-decode logic. Server events are
		// receive-only on the client, so withEncode=false.
		writeBroadcastClass(b, st, false)
	}
}

// serverEventClassNames returns the TS class names for every typed server
// event in deterministic order. Used by the index-emit + client-emit paths
// for re-export and import lists.
func serverEventClassNames(types []ServerEventTypeSchema) []string {
	out := make([]string, len(types))
	for i, st := range types {
		out[i] = broadcastClassName(st.Name)
	}
	return out
}

// serverEventMethodName converts a typed-event class name to a `client.onXxx`
// method name. "PlayerSpawned" → "onPlayerSpawned".
func serverEventMethodName(className string) string {
	return "on" + className
}

// writeServerEventHandler emits one `client.onXxx(handler)` method that
// subscribes against the typedEvents dispatcher under the hood.
func writeServerEventHandler(b *strings.Builder, st ServerEventTypeSchema) {
	className := broadcastClassName(st.Name)
	method := serverEventMethodName(className)
	fmt.Fprintf(b, "  /** Subscribe to typed server event %s (typeID 0x%08x). */\n", st.Name, st.TypeID)
	fmt.Fprintf(b, "  %s(handler: (msg: %s) => void): () => void {\n", method, className)
	fmt.Fprintf(b, "    return this.typedEvents.on(%s, handler);\n", className)
	b.WriteString("  }\n\n")
}
