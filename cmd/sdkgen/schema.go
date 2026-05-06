package main

// These mirror the JSON schema output from --dump-schema.
// Kept as plain structs (not importing pkg/system) to keep sdkgen self-contained.

type BindingSchemaField struct {
	Name     string  `json:"name"`
	Encoding string  `json:"encoding"`
	Size     int     `json:"size"`
	Scale    float64 `json:"scale,omitempty"`
	Initial  bool    `json:"initial,omitempty"`
}

type BindingSchema struct {
	Type   string               `json:"type"`
	Fields []BindingSchemaField `json:"fields"`
}

type VarTailSchema struct {
	Name       string               `json:"name"`
	ItemSize   int                  `json:"itemSize"`
	ItemFields []BindingSchemaField `json:"itemFields"`
}

type EntitySchema struct {
	Kind        uint8           `json:"kind"`
	Name        string          `json:"name,omitempty"`
	Bindings    []BindingSchema `json:"bindings"`
	Layout      []int           `json:"layout"`
	VarTail     *VarTailSchema  `json:"varTail,omitempty"`
	InitialData string          `json:"initialData,omitempty"`
}

type ClientEventSchema struct {
	Code      uint32 `json:"code"`
	ProtoName string `json:"protoName"`
}

type ServerEventSchema struct {
	Code      uint32 `json:"code"`
	Name      string `json:"name"`
	ProtoName string `json:"protoName"`
}

type OperationSchema struct {
	Code          uint32 `json:"code"`
	Name          string `json:"name"`
	RequestProto  string `json:"requestProto"`
	ResponseProto string `json:"responseProto"`
}

// BroadcastFieldSchema describes one field on a broadcast-eligible type.
// JSON shape mirrors pkg/mmokit.BroadcastFieldSchema exactly.
//
// For encoding == "struct": Fields lists the inner fields in source order.
// For encoding == "slice":  Item describes the element schema.
type BroadcastFieldSchema struct {
	Name     string                 `json:"name"`
	Encoding string                 `json:"encoding"`
	Size     int                    `json:"size"`
	Fields   []BroadcastFieldSchema `json:"fields,omitempty"`
	Item     *BroadcastFieldSchema  `json:"item,omitempty"`
}

// BroadcastTypeSchema describes a broadcast-eligible Go type for sdkgen.
// JSON shape mirrors pkg/mmokit.BroadcastTypeSchema exactly.
type BroadcastTypeSchema struct {
	Name   string                 `json:"name"`
	TypeID uint32                 `json:"type_id"`
	Fields []BroadcastFieldSchema `json:"fields"`
}

// ClientInputTypeSchema describes a HandleClient-eligible Go type for sdkgen.
// JSON shape mirrors pkg/mmokit.ClientInputTypeSchema (= BroadcastTypeSchema)
// exactly — same wire codec, different direction (client → server). Sdkgen
// emits a TS class with an encode() instance method per entry.
type ClientInputTypeSchema = BroadcastTypeSchema

// ServerEventTypeSchema describes a RegisterEvent[T]-registered Go type for
// sdkgen. JSON shape mirrors pkg/mmokit.ServerEventTypeSchema (= BroadcastTypeSchema)
// exactly — same wire codec, different dispatch path. Sdkgen emits a TS class
// with a static decode(buf) method + a client.onXxx(handler) wrapper.
type ServerEventTypeSchema = BroadcastTypeSchema

// TypedOperationSchema describes one RegisterOp[Req, Res any] registration.
// JSON shape mirrors pkg/mmokit.TypedOperationSchema exactly. Both halves
// reuse BroadcastFieldSchema (same reflect-codec wire layout as broadcasts/
// server-events/client-inputs).
type TypedOperationSchema struct {
	Kind             string                 `json:"kind"`
	RequestTypeID    uint32                 `json:"request_type_id"`
	RequestTypeName  string                 `json:"request_type_name"`
	RequestFields    []BroadcastFieldSchema `json:"request_fields"`
	ResponseTypeID   uint32                 `json:"response_type_id"`
	ResponseTypeName string                 `json:"response_type_name"`
	ResponseFields   []BroadcastFieldSchema `json:"response_fields"`
}

type ProtocolSchema struct {
	Game             string                   `json:"game"`
	ClientEvents     []ClientEventSchema      `json:"clientEvents"`
	ServerEvents     []ServerEventSchema      `json:"serverEvents"`
	Entities         []EntitySchema           `json:"entities"`
	Operations       []OperationSchema        `json:"operations,omitempty"`
	BroadcastTypes   []BroadcastTypeSchema    `json:"broadcast_types,omitempty"`
	ClientInputTypes []ClientInputTypeSchema  `json:"client_input_types,omitempty"`
	ServerEventTypes []ServerEventTypeSchema  `json:"server_event_types,omitempty"`
	TypedOperations  []TypedOperationSchema   `json:"typed_operations,omitempty"`
}
