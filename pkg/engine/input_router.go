package engine

import (
	"fmt"

	"github.com/mlange-42/ark/ecs"
)

// StateMask is a bitmask of PlayerState values. Supports up to 32 states.
type StateMask uint32

// States builds a StateMask from one or more PlayerState values.
func States(states ...PlayerState) StateMask {
	var m StateMask
	for _, s := range states {
		if s >= 32 {
			panic(fmt.Sprintf("InputRouter: PlayerState %d exceeds StateMask capacity (max 31)", s))
		}
		m |= 1 << StateMask(s)
	}
	return m
}

// InputContext is passed to every input handler.
// Entity may be zero-value for players without an active ECS entity.
type InputContext struct {
	Session *PlayerSession
	ConnID  uint32
	Entity  ecs.Entity
}

// InputFilter is a predicate used for state-group filters and per-handler guards.
type InputFilter func(ctx *InputContext) bool

// EnvelopeParser extracts a message code and inner payload from raw wire bytes.
type EnvelopeParser func(raw []byte) (code uint32, data []byte, err error)

// HandlerOption configures optional per-handler behavior.
type HandlerOption func(*handlerEntry)

// WithGuard sets a per-handler guard. If it returns false, the message is skipped.
func WithGuard(fn InputFilter) HandlerOption {
	return func(e *handlerEntry) {
		e.guard = fn
	}
}

// WithProtoName sets the protobuf message name for schema export.
func WithProtoName(name string) HandlerOption {
	return func(e *handlerEntry) {
		e.protoName = name
	}
}

type handlerEntry struct {
	states    StateMask
	guard     InputFilter
	fn        func(ctx *InputContext, data []byte)
	protoName string // e.g. "basicpb.BasicMoveTargetMsg" — for schema export
}

// InputRouter dispatches client messages to registered handlers based on
// message code and player state. It implements the System interface.
type InputRouter struct {
	eng      *Engine
	parse    EnvelopeParser
	handlers map[uint32]*handlerEntry
	filters  map[PlayerState]InputFilter
}

// NewInputRouter creates an InputRouter wired to the given Engine.
func NewInputRouter(eng *Engine, parse EnvelopeParser) *InputRouter {
	return &InputRouter{
		eng:      eng,
		parse:    parse,
		handlers: make(map[uint32]*handlerEntry),
		filters:  make(map[PlayerState]InputFilter),
	}
}

// Update implements System.
func (r *InputRouter) Update(dt float32) { r.ProcessInput() }

// StateFilter sets a shared precondition for all handlers matching a player state.
func (r *InputRouter) StateFilter(state PlayerState, fn InputFilter) {
	r.filters[state] = fn
}

// Handle registers a handler for a message code. Panics on duplicate code.
func (r *InputRouter) Handle(code uint32, states StateMask, fn func(ctx *InputContext, data []byte), opts ...HandlerOption) {
	if _, exists := r.handlers[code]; exists {
		panic(fmt.Sprintf("InputRouter: duplicate handler for code %d", code))
	}
	entry := &handlerEntry{
		states: states,
		fn:     fn,
	}
	for _, opt := range opts {
		opt(entry)
	}
	r.handlers[code] = entry
}

// EventCode is any integer type usable as a message code (proto enums are int32).
type EventCode interface{ ~int32 | ~uint32 }

// Handle registers a typed handler, accepting any integer event code.
// If unmarshal returns an error, the message is silently skipped.
func Handle[C EventCode, T any](r *InputRouter, code C, states StateMask,
	unmarshal func([]byte) (T, error),
	fn func(ctx *InputContext, msg T), opts ...HandlerOption) {

	r.Handle(uint32(code), states, func(ctx *InputContext, data []byte) {
		msg, err := unmarshal(data)
		if err != nil {
			return
		}
		fn(ctx, msg)
	}, opts...)
}

// ClientEventSchema describes a registered client event handler for schema export.
type ClientEventSchema struct {
	Code      uint32 `json:"code"`
	ProtoName string `json:"protoName"`
}

// Schema returns the registered client event handlers as machine-readable schema
// for client SDK codegen.
func (r *InputRouter) Schema() []ClientEventSchema {
	schemas := make([]ClientEventSchema, 0, len(r.handlers))
	for code, entry := range r.handlers {
		schemas = append(schemas, ClientEventSchema{
			Code:      code,
			ProtoName: entry.protoName,
		})
	}
	return schemas
}

// ProcessInput drains and dispatches input for all connected, non-pending players.
func (r *InputRouter) ProcessInput() {
	r.eng.Players.ForEachConnected(func(sess *PlayerSession) {
		// Auto-skip players whose entity was removed from the ECS.
		// Zero-value entity is allowed (e.g. players in a custom state without an entity).
		if sess.Entity != (ecs.Entity{}) && !r.eng.ECS.Alive(sess.Entity) {
			r.eng.ConnMgr.DrainInput(sess.ConnID)
			return
		}

		stateBit := StateMask(1) << StateMask(sess.State)
		ctx := &InputContext{
			Session: sess,
			ConnID:  sess.ConnID,
			Entity:  sess.Entity,
		}

		// State-group filter (once per player per tick)
		if filter, ok := r.filters[sess.State]; ok && !filter(ctx) {
			r.eng.ConnMgr.DrainInput(sess.ConnID)
			return
		}

		msgs := r.eng.ConnMgr.DrainInput(sess.ConnID)
		if len(msgs) == 0 {
			return
		}

		for _, raw := range msgs {
			code, data, err := r.parse(raw)
			if err != nil {
				if r.eng.Log != nil {
					r.eng.Log.Log("input", "envelope parse error: conn=%d err=%v", sess.ConnID, err)
				}
				continue
			}

			entry := r.handlers[code]
			if entry == nil || entry.states&stateBit == 0 {
				continue
			}

			if entry.guard != nil && !entry.guard(ctx) {
				continue
			}

			entry.fn(ctx, data)
		}
	})
}
