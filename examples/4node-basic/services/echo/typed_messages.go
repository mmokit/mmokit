// Package echo — typed_messages.go defines the typed Go structs for the
// echo demo service ops on the typed-op channel (channel 0x01,
// mmokit.RegisterOp[Req, Res]).
//
// These run through the engine's reflection codec — same shape as typed
// events on channel 0x00. Field names use Go's CamelCase convention; the
// generated TypeScript SDK exposes them as lowerCamelCase
// (`EchoPingRequest{Msg}` → `client.echoPing({msg})`).
//
// Wire-stable typeIDs are derived from the package-qualified type name
// (`echo.EchoPingRequest` etc.), so renames are wire-breaking.

package echo

// EchoPingRequest visualizes routing affinity by returning the handling
// instance ID. Pure compute — no DB access.
type EchoPingRequest struct {
	Msg string
}

type EchoPingResponse struct {
	Msg        string
	InstanceID string
}

// EchoPersistRequest writes a (key, value, ts) row through Postgres so a
// follow-up FETCH from any instance picks it up.
type EchoPersistRequest struct {
	Key   string
	Value string
}

type EchoPersistResponse struct {
	OK         bool
	InstanceID string
}

// EchoFetchRequest reads back a previously persisted row.
type EchoFetchRequest struct {
	Key string
}

type EchoFetchResponse struct {
	Value      string
	FoundAtMs  int64
	InstanceID string
}
