package mmokit

import (
	"fmt"
	"reflect"

	"google.golang.org/protobuf/proto"

	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/universe"
)

// OnInput registers a wire→handler binding without component injection.
//
// Game code:
//
//	mmokit.OnInput[gamepb.DockMsg](mmo, GCE_DOCK).
//	    Active().
//	    Do(func(p *mmokit.Player, msg *gamepb.DockMsg) {
//	        gw.Docking.Request(p, msg.StationId)
//	    })
//
// The handler runs in the cell loop's dispatchInput phase, before any
// game system. State filter must be set via .States or .Active before
// .Do is called or .Do panics.
func OnInput[
	Msg any,
	P interface {
		*Msg
		proto.Message
	},
	C engine.EventCode,
](mmo *universe.Process, code C) *InputBuilder[Msg, struct{}] {
	var zero P = new(Msg)
	return &InputBuilder[Msg, struct{}]{
		mmo:       mmo,
		code:      uint32(code),
		protoName: string(proto.MessageName(zero)),
		msgType:   reflect.TypeOf(zero),
		depsType:  nil,
	}
}

// OnInputWith registers a wire→handler binding with component injection.
// Deps is a struct whose exported pointer-to-struct fields name ECS
// components to fetch from the player entity before invoking the handler.
//
//	type MoveDeps struct {
//	    MT *mmokit.MoveTarget
//	}
//	mmokit.OnInputWith[basicpb.MoveTargetMsg, MoveDeps](mmo, BCE_MOVE_TARGET).
//	    Active().
//	    Do(func(p *mmokit.Player, msg *basicpb.MoveTargetMsg, c *MoveDeps) {
//	        c.MT.SetTarget(msg.TargetX, msg.TargetY)
//	    })
//
// Required deps fields cause the handler to be silently skipped if absent;
// fields tagged `ecs:"optional"` arrive nil if absent.
func OnInputWith[
	Msg any,
	Deps any,
	P interface {
		*Msg
		proto.Message
	},
	C engine.EventCode,
](mmo *universe.Process, code C) *InputBuilder[Msg, Deps] {
	var zero P = new(Msg)
	var depsZero Deps
	return &InputBuilder[Msg, Deps]{
		mmo:       mmo,
		code:      uint32(code),
		protoName: string(proto.MessageName(zero)),
		msgType:   reflect.TypeOf(zero),
		depsType:  reflect.TypeOf(depsZero),
	}
}

// InputBuilder is the fluent registration handle returned by OnInput /
// OnInputWith. Terminate with .Do(...) — until Do is called nothing is
// registered. State filter (.States or .Active) is required before .Do
// or .Do panics.
type InputBuilder[Msg, Deps any] struct {
	mmo       *universe.Process
	code      uint32
	protoName string
	msgType   reflect.Type // *Msg type
	depsType  reflect.Type // Deps type, or nil for OnInput

	stateMask engine.StateMask
	stateSet  bool
	guardFn   func(*Player) bool
}

// States restricts the handler to fire only when the player is in one of
// the listed states. Required (or use .Active for the StateActive shorthand).
func (b *InputBuilder[Msg, Deps]) States(states ...engine.PlayerState) *InputBuilder[Msg, Deps] {
	b.stateMask = engine.States(states...)
	b.stateSet = true
	return b
}

// Active is shorthand for States(engine.StateActive).
func (b *InputBuilder[Msg, Deps]) Active() *InputBuilder[Msg, Deps] {
	return b.States(engine.StateActive)
}

// Guard installs an optional per-input gate. Returning false from fn
// silently skips the handler for this message.
func (b *InputBuilder[Msg, Deps]) Guard(fn func(*Player) bool) *InputBuilder[Msg, Deps] {
	b.guardFn = fn
	return b
}

// Do installs the handler and registers the binding on the process.
// Panics if .States/.Active was not called.
//
// For OnInput (no deps), pass `func(p *Player, msg *Msg)`.
// For OnInputWith, pass `func(p *Player, msg *Msg, c *Deps)`.
func (b *InputBuilder[Msg, Deps]) Do(handler any) {
	if !b.stateSet {
		panic(fmt.Sprintf(
			"OnInput[%s]: state filter not set — call .States(...) or .Active() before .Do",
			b.msgType.Elem().Name()))
	}

	binding := &engine.InputBinding{}
	binding.SetCode(b.code)
	binding.SetMsgType(b.msgType)
	binding.SetProtoName(b.protoName)
	binding.SetStateMask(b.stateMask)

	var depsLayoutPtr *engine.DepsLayout
	if b.depsType != nil {
		depsLayoutPtr = engine.BuildDepsLayout(b.depsType)
	}
	binding.SetDeps(depsLayoutPtr)

	if b.guardFn != nil {
		guardFn := b.guardFn
		binding.SetGuard(func(playerAny any) bool {
			p, ok := playerAny.(*Player)
			if !ok {
				return false
			}
			return guardFn(p)
		})
	}

	binding.SetInvoke(buildInvoker[Msg, Deps](b.code, b.msgType, depsLayoutPtr, b.guardFn, handler))
	b.mmo.AddInputBinding(binding)
}

// buildInvoker constructs the type-erased invoke thunk that the
// dispatcher calls. Closes over the handler, decoding Msg and (if Deps
// is non-empty) populating a pooled Deps struct from the player entity.
func buildInvoker[Msg, Deps any](
	code uint32,
	msgType reflect.Type,
	layout *engine.DepsLayout,
	guard func(*Player) bool,
	handler any,
) func(eng *engine.Engine, stage any, sess *engine.PlayerSession, data []byte) {
	msgElem := msgType.Elem() // Msg type (struct), not *Msg

	if layout == nil {
		// OnInput path: handler is func(*Player, *Msg)
		h, ok := handler.(func(*Player, *Msg))
		if !ok {
			panic(fmt.Sprintf(
				"OnInput[%s].Do: handler must be func(*mmokit.Player, *%s); got %T",
				msgElem.Name(), msgElem.Name(), handler))
		}
		return func(eng *engine.Engine, stage any, sess *engine.PlayerSession, data []byte) {
			msgPtr := reflect.New(msgElem).Interface()
			pm, ok := msgPtr.(proto.Message)
			if !ok {
				return
			}
			if err := proto.Unmarshal(data, pm); err != nil {
				return
			}
			st, _ := stage.(*universe.Stage)
			mp := newPlayer(st, eng, sess)
			if guard != nil && !guard(mp) {
				return
			}
			h(mp, msgPtr.(*Msg))
		}
	}

	// OnInputWith path: handler is func(*Player, *Msg, *Deps)
	h, ok := handler.(func(*Player, *Msg, *Deps))
	if !ok {
		panic(fmt.Sprintf(
			"OnInputWith[%s, %s].Do: handler must be func(*mmokit.Player, *%s, *%s); got %T",
			msgElem.Name(), layout.StructType().Name(),
			msgElem.Name(), layout.StructType().Name(), handler))
	}
	return func(eng *engine.Engine, stage any, sess *engine.PlayerSession, data []byte) {
		msgPtr := reflect.New(msgElem).Interface()
		pm, ok := msgPtr.(proto.Message)
		if !ok {
			return
		}
		if err := proto.Unmarshal(data, pm); err != nil {
			return
		}
		cb := engine.LookupCellBinding(eng, code)
		if cb == nil {
			return
		}
		if !cb.PopulateDeps(eng.ECS, sess.Entity) {
			return
		}
		st, _ := stage.(*universe.Stage)
		mp := newPlayer(st, eng, sess)
		if guard != nil && !guard(mp) {
			return
		}
		depsPtr := (*Deps)(cb.PooledDepsPointer())
		h(mp, msgPtr.(*Msg), depsPtr)
	}
}
