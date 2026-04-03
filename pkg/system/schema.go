package system

import (
	"strings"
	"unicode"
)

// ---------------------------------------------------------------------------
// Schema types — machine-readable descriptions of the replication protocol.
// Used by cmd/sdkgen to auto-generate typed client SDKs.
// ---------------------------------------------------------------------------

// BindingSchemaField describes one wire field for client codegen.
type BindingSchemaField struct {
	Name     string  `json:"name"`
	Encoding string  `json:"encoding"` // "f32", "qvel", "u8", "string", etc.
	Size     int     `json:"size"`     // wire bytes (-1 for variable)
	Scale    float64 `json:"scale,omitempty"`
	Initial  bool    `json:"initial,omitempty"`
}

// BindingSchema describes one composable binding's contribution to an entity snapshot.
type BindingSchema struct {
	Type   string               `json:"type"` // "viewer_relative_pos", "q_velocity", "component", etc.
	Fields []BindingSchemaField `json:"fields"`
}

// VarTailSchema describes a variable-length tail in a snapshot (e.g. snake segments).
type VarTailSchema struct {
	Name       string               `json:"name"`
	ItemSize   int                  `json:"itemSize"`
	ItemFields []BindingSchemaField `json:"itemFields"`
}

// EntitySchema describes one entity type's replication layout.
type EntitySchema struct {
	Kind        uint8           `json:"kind"`
	Name        string          `json:"name,omitempty"` // e.g. "Player", for generated type names
	Bindings    []BindingSchema `json:"bindings"`
	Layout      []int           `json:"layout"`
	VarTail     *VarTailSchema  `json:"varTail,omitempty"`
	InitialData string          `json:"initialData,omitempty"` // e.g. "length_prefixed_string_u8"
}

// SchemaProvider is an optional interface on EntityReplicator.
// AutoReplicator implements it automatically. Hand-coded replicators
// can implement it to participate in client SDK codegen.
type SchemaProvider interface {
	Schema() EntitySchema
}

// EncodingWireSize maps encoding names to their fixed wire size.
// Exported for use by codegen validation tools.
// -1 means variable length.
var EncodingWireSize = encodingWireSize

// lcFirst lowercases the first letter of a Go identifier for use as a JS field name.
// "OwnerNode" → "ownerNode", "X" → "x".
func lcFirst(s string) string {
	if s == "" {
		return s
	}
	// Handle all-caps like "ID" → "id"
	if strings.ToUpper(s) == s {
		return strings.ToLower(s)
	}
	runes := []rune(s)
	runes[0] = unicode.ToLower(runes[0])
	return string(runes)
}
