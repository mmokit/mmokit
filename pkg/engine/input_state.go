package engine

import "fmt"

// StateMask is a bitmask of PlayerState values. Supports up to 32 states.
type StateMask uint32

// States builds a StateMask from one or more PlayerState values.
func States(states ...PlayerState) StateMask {
	var m StateMask
	for _, s := range states {
		if s >= 32 {
			panic(fmt.Sprintf("input dispatcher: PlayerState %d exceeds StateMask capacity (max 31)", s))
		}
		m |= 1 << StateMask(s)
	}
	return m
}

// EventCode is any integer type usable as a wire-event code (proto enums
// are int32). Used by client/server-event registration generics.
type EventCode interface{ ~int32 | ~uint32 }

// EnvelopeParser extracts a message code and inner payload from raw wire
// bytes. The default parser is mmokit.ProtoEnvelopeParser, wired into
// the dispatcher via DefaultEnvelopeParser at mmokit init time.
type EnvelopeParser func(raw []byte) (code uint32, data []byte, err error)

// ClientEventSchema describes one client→server event for schema export.
type ClientEventSchema struct {
	Code      uint32 `json:"code"`
	ProtoName string `json:"protoName"`
}
