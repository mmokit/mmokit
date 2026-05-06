package engine

// EventCode is any integer type usable as a wire-event code (proto enums
// are int32). Used by client/server-event registration generics.
type EventCode interface{ ~int32 | ~uint32 }

// ClientEventSchema describes one client→server event for schema export.
type ClientEventSchema struct {
	Code      uint32 `json:"code"`
	ProtoName string `json:"protoName"`
}
