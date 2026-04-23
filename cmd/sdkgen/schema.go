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

type ProtocolSchema struct {
	Game             string              `json:"game"`
	ClientEvents     []ClientEventSchema `json:"clientEvents"`
	ServerEvents     []ServerEventSchema `json:"serverEvents"`
	Entities         []EntitySchema      `json:"entities"`
	Operations       []OperationSchema   `json:"operations,omitempty"`
	ClientRenderMode string              `json:"clientRenderMode"`
}
