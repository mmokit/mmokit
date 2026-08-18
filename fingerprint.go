package mmokit

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/mmokit/mmokit/pkg/system"
	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

// schemaFingerprintDomain separates this hash from every other use of SHA-256
// and versions the ALGORITHM, not the schema. Bump the suffix when the
// canonicalization changes, or when something outside ProtocolSchema that the
// bytes depend on changes — the channel-byte assignments (0x00 typed events
// and client input, 0x01 typed operations) are the standing example: they are
// constants rather than schema, so version 1 means "and the channels are the
// ones this build ships".
const schemaFingerprintDomain = "mmokit-schema-fingerprint/1\n"

// SchemaFingerprint is a structural hash of the client-visible protocol.
//
// It exists because a protocol VERSION cannot detect the failure CE-009 is
// about. Wire type IDs are fnv32a(reflect.Type.String()), and the framework
// keeps one component type set across dimension profiles, so a 2D and a 3D
// component.Position hash identically. The disagreement lives in the field
// shapes inside the message body: a 3D engine binding set emits three world
// coordinates where a 2D one emits two, which changes EntitySchema.Layout from
// [4 4 …] to [4 4 4 …] and adds a field. Hashing those shapes is what turns a
// silently mis-decoded body into a refused connection.
//
// WHAT IS HASHED: the whole ProtocolSchema — entity layouts and binding
// fields, the four message registries with their type IDs and field shapes,
// the game name, and the dimension profile.
//
// WHAT IS NOT, and why exactly these two: BindingSchema.Type and
// BindingSchema.StructName. Both are dropped because no generator consumes
// them, which is a checkable criterion rather than a taste judgement.
// cmd/sdkgen decodes its own BindingSchema as {Type, Fields} — structName is
// discarded at the JSON boundary — and none of the ten sites that iterate
// ent.Bindings reads .Type; every one goes straight to .Fields. A Go struct
// rename therefore produces byte-identical SDKs and byte-identical wire
// output, and rotating on it would hard-reject every deployed client over a
// refactor that owed no regeneration.
//
// Field NAMES are hashed, deliberately. They are generator output —
// cmd/sdkgen emits them as TypeScript property names — so renaming one breaks
// application code against an unregenerated client. A rotation there is
// correct, not a false positive.
//
// The result is never 0. Zero is reserved for "this process has no schema",
// which is what a Process built through pkg/universe without the facade has,
// so a real fingerprint can never be mistaken for an absent one.
func SchemaFingerprint(ps ProtocolSchema) uint32 {
	h := sha256.New()
	h.Write([]byte(schemaFingerprintDomain))

	// Compact JSON of the projection. encoding/json emits struct fields in
	// declaration order and every list in ProtocolSchema is already sorted by
	// a stable key, so this is deterministic — the schema goldens in
	// testdata/schema/ are byte-compared on exactly that property.
	body, err := json.Marshal(projectForFingerprint(ps))
	if err != nil {
		// ProtocolSchema is plain data with no channels, funcs or cycles.
		panic(fmt.Sprintf("SchemaFingerprint: marshaling the schema failed, which should be impossible: %v", err))
	}
	h.Write(body)

	sum := binary.BigEndian.Uint32(h.Sum(nil)[:4])
	if sum == 0 {
		return 1
	}
	return sum
}

// FormatSchemaFingerprint renders a fingerprint as 8 lowercase hex digits —
// the form carried in the schema JSON and in the connection-setup query
// parameter. Declared in pkg/universe, where the connection-setup gates that
// parse it live.
var FormatSchemaFingerprint = pkguniverse.FormatSchemaFingerprint

// ParseSchemaFingerprint reads the form FormatSchemaFingerprint writes.
var ParseSchemaFingerprint = pkguniverse.ParseSchemaFingerprint

// projectForFingerprint returns a copy of ps with the fields excluded from the
// hash zeroed. A copy, not a mutation: callers hand us the schema they are
// about to serialize.
func projectForFingerprint(ps ProtocolSchema) ProtocolSchema {
	// Self-reference: the fingerprint is a field of the document it hashes, so
	// zeroing it makes SchemaFingerprint(Schema()) stable whether or not the
	// field has been populated yet.
	ps.Fingerprint = ""

	if len(ps.Entities) == 0 {
		return ps
	}
	entities := make([]system.EntitySchema, len(ps.Entities))
	copy(entities, ps.Entities)
	for i := range entities {
		if len(entities[i].Bindings) == 0 {
			continue
		}
		bindings := make([]system.BindingSchema, len(entities[i].Bindings))
		copy(bindings, entities[i].Bindings)
		for j := range bindings {
			bindings[j].Type = ""
			bindings[j].StructName = ""
		}
		entities[i].Bindings = bindings
	}
	ps.Entities = entities
	return ps
}
