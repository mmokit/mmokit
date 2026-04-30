package engine

import (
	"fmt"
	"reflect"
	"unsafe"

	"github.com/mlange-42/ark/ecs"
)

// InputBinding records one OnInput / OnInputWith registration. It lives on
// the universe.Process and is replayed per cell at createNode time.
//
// Reflection is paid once at registration (msgType, deps). Per-tick
// dispatch chases pre-cached cell-local accessors via offset arithmetic.
type InputBinding struct {
	code      uint32
	msgType   reflect.Type
	protoName string
	stateMask StateMask
	guard     func(any) bool
	deps      *DepsLayout

	// invoke decodes the proto into a *Msg and (optionally) populates a
	// *Deps from the player entity, then calls the user handler. Wired by
	// mmokit.OnInput / OnInputWith at registration time. The dispatcher
	// passes the cell's engine + opaque stage (typed as any so engine
	// doesn't import universe) so the invoker can build a *Player without
	// a global session map.
	invoke func(eng *Engine, stage any, sess *PlayerSession, data []byte)
}

// Code returns the wire code this binding listens for.
func (b *InputBinding) Code() uint32 { return b.code }

// ProtoName returns the proto message name for schema export.
func (b *InputBinding) ProtoName() string { return b.protoName }

// MsgType returns the reflect.Type for *Msg, or nil. Used by mmokit's
// invoker to allocate a fresh message per call without re-reflecting.
func (b *InputBinding) MsgType() reflect.Type { return b.msgType }

// StateMask returns the binding's state filter mask.
func (b *InputBinding) StateMask() StateMask { return b.stateMask }

// Deps returns the deps layout (nil when there are no deps).
func (b *InputBinding) Deps() *DepsLayout { return b.deps }

// SetCode sets the wire code. Used by mmokit.OnInput / OnInputWith.
func (b *InputBinding) SetCode(c uint32) { b.code = c }

// SetMsgType sets the *Msg reflect.Type for schema + per-call decode.
func (b *InputBinding) SetMsgType(t reflect.Type) { b.msgType = t }

// SetProtoName sets the proto message name for schema export.
func (b *InputBinding) SetProtoName(n string) { b.protoName = n }

// SetStateMask sets the state-filter mask.
func (b *InputBinding) SetStateMask(m StateMask) { b.stateMask = m }

// SetDeps sets the deps layout (nil for OnInput).
func (b *InputBinding) SetDeps(d *DepsLayout) { b.deps = d }

// SetInvoke sets the type-erased dispatch thunk. The dispatcher passes
// (eng, stage, sess, data); mmokit's invoker thunk wraps these into a
// *Player.
func (b *InputBinding) SetInvoke(fn func(eng *Engine, stage any, sess *PlayerSession, data []byte)) {
	b.invoke = fn
}

// SetGuard sets an optional predicate that must return true for the
// handler to fire. The argument any is *Player from mmokit.
func (b *InputBinding) SetGuard(fn func(any) bool) { b.guard = fn }

// DepsLayout is the precomputed layout of a Deps struct. Built once at
// registration via reflection on the Deps type. Mirrors pkg/query/fieldMeta.
type DepsLayout struct {
	structType reflect.Type
	fields     []DepsField
}

// StructType exposes the deps struct type.
func (l *DepsLayout) StructType() reflect.Type { return l.structType }

// Fields exposes the per-field metadata.
func (l *DepsLayout) Fields() []DepsField { return l.fields }

// DepsField holds the precomputed info needed to populate one Deps field
// per dispatch.
type DepsField struct {
	Name       string       // for diagnostics
	ComponentT reflect.Type // resolved Ark component ID at first dispatch
	Offset     uintptr      // byte offset of the pointer field within the Deps struct
	Optional   bool         // true if tagged `ecs:"optional"`
}

// BuildDepsLayout scans a Deps struct type via reflection. Returns nil for
// empty/zero-field structs (the OnInput case). Panics if any exported
// field isn't a pointer-to-struct, matching pkg/query/buildFields semantics.
func BuildDepsLayout(t reflect.Type) *DepsLayout {
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("OnInput: deps type must be a struct, got %v", t.Kind()))
	}
	var fields []DepsField
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Type.Kind() != reflect.Ptr || f.Type.Elem().Kind() != reflect.Struct {
			panic(fmt.Sprintf(
				"OnInput: deps field %s must be a pointer to a component struct, got %v",
				f.Name, f.Type))
		}
		fields = append(fields, DepsField{
			Name:       f.Name,
			ComponentT: f.Type.Elem(),
			Offset:     f.Offset,
			Optional:   f.Tag.Get("ecs") == "optional",
		})
	}
	if len(fields) == 0 {
		return nil
	}
	return &DepsLayout{structType: t, fields: fields}
}

// CellBinding wraps an InputBinding with per-cell state — lazily-resolved
// component IDs, a cached ecs.Unsafe handle, and a pooled deps struct.
// One CellBinding per (binding × cell). Built when the binding is added
// to a cell's dispatcher.
type CellBinding struct {
	Binding *InputBinding
	layout  *DepsLayout

	// compIDs[i] is the resolved Ark component ID for layout.fields[i].
	// Lazily resolved at first dispatch because IDs are world-scoped and
	// the world isn't fully constructed at registration time.
	compIDs []ecs.ID

	// pooledDeps is a heap-allocated zero Deps struct sized for layout.
	// Reused across calls because dispatch is serial on the loop goroutine.
	pooledDeps unsafe.Pointer
}

// PooledDepsPointer returns the pointer to the pooled Deps struct.
func (cb *CellBinding) PooledDepsPointer() unsafe.Pointer { return cb.pooledDeps }

// ensureCompIDs resolves the layout's component types into Ark IDs against
// the cell's world. Idempotent.
func (cb *CellBinding) ensureCompIDs(w *ecs.World) {
	if cb.layout == nil || cb.compIDs != nil {
		return
	}
	cb.compIDs = make([]ecs.ID, len(cb.layout.fields))
	for i := range cb.layout.fields {
		cb.compIDs[i] = ecs.TypeID(w, cb.layout.fields[i].ComponentT)
	}
}

// PopulateDeps fills the pooled Deps struct from entity e. Returns false
// if any required field is absent (or e is zero/dead) — handler should be
// skipped. The zero/dead-entity guard handles legitimate cases like a
// StateDocked player whose entity has been removed: OnInput handlers
// (UNDOCK, BANK_REQUEST, etc.) still dispatch via the no-deps path, while
// OnInputWith handlers safely skip rather than panic on a missing entity.
func (cb *CellBinding) PopulateDeps(w *ecs.World, e ecs.Entity) bool {
	if e == (ecs.Entity{}) || !w.Alive(e) {
		return false
	}
	cb.ensureCompIDs(w)
	u := w.Unsafe()
	base := cb.pooledDeps
	for i := range cb.layout.fields {
		f := &cb.layout.fields[i]
		fieldPtr := (*unsafe.Pointer)(unsafe.Add(base, f.Offset))
		if u.Has(e, cb.compIDs[i]) {
			*fieldPtr = u.Get(e, cb.compIDs[i])
		} else if f.Optional {
			*fieldPtr = nil
		} else {
			return false
		}
	}
	return true
}

// inputDispatcher is the per-cell engine-internal type that owns the
// per-component accessor cache and the per-tick wire→handler dispatch.
// Replaces the public InputRouter as a system; lives in the engine and is
// driven directly from the tick loop.
type inputDispatcher struct {
	eng      *Engine
	bindings map[uint32]*CellBinding
	parse    EnvelopeParser

	// stage is the cell's *universe.Stage, passed opaquely (any) so the
	// engine doesn't import universe. Set by universe at createNode time;
	// passed through to the binding's invoke thunk so mmokit's invoker
	// can build a *Player without a global session→stage map.
	stage any
}

// SetStage records an opaque pointer to the cell's stage. Called by
// universe at createNode time; consumed by binding invokers.
func (d *inputDispatcher) SetStage(stage any) { d.stage = stage }

// Stage returns the dispatcher's opaque stage pointer (set by universe
// at createNode time). Used by mmokit's invoker to type-assert back to
// *universe.Stage when building a *Player.
func (d *inputDispatcher) Stage() any { return d.stage }

// NewInputDispatcher creates a dispatcher wired to the given engine.
func NewInputDispatcher(eng *Engine) *inputDispatcher {
	return &inputDispatcher{
		eng:      eng,
		bindings: make(map[uint32]*CellBinding),
	}
}

// SetParser injects the wire-format envelope parser. Called by the
// universe.Process when it wires the dispatcher with mmokit's
// ProtoEnvelopeParser.
func (d *inputDispatcher) SetParser(p EnvelopeParser) { d.parse = p }

// AddBinding installs a binding. Panics on duplicate code.
func (d *inputDispatcher) AddBinding(b *InputBinding) {
	if _, exists := d.bindings[b.code]; exists {
		panic(fmt.Sprintf("inputDispatcher: duplicate handler for code %d", b.code))
	}
	cb := &CellBinding{Binding: b, layout: b.deps}
	if cb.layout != nil {
		cb.pooledDeps = reflect.New(cb.layout.structType).UnsafePointer()
	}
	d.bindings[b.code] = cb
}

// Lookup returns the CellBinding for code, or nil if no handler is registered.
func (d *inputDispatcher) Lookup(code uint32) *CellBinding {
	return d.bindings[code]
}

// Tick drains and dispatches input for all connected, non-pending players
// on this cell. Called from the tick loop's dispatchInput phase.
func (d *inputDispatcher) Tick() {
	if d.parse == nil {
		return
	}
	d.eng.Players.ForEachConnected(func(sess *PlayerSession) {
		msgs := d.eng.ConnMgr.DrainInput(sess.ConnID)
		if len(msgs) == 0 {
			return
		}

		// Sessions with zero/dead entity (e.g. StateDocked players whose
		// ship has been removed) still dispatch input — OnInput handlers
		// like UNDOCK and BANK_REQUEST need to fire from this state.
		// PopulateDeps guards OnInputWith handlers from a missing entity.

		stateBit := StateMask(1) << StateMask(sess.State)
		for _, raw := range msgs {
			code, data, err := d.parse(raw)
			if err != nil {
				if d.eng.Log != nil {
					d.eng.Log.Log("input", "envelope parse error: conn=%d err=%v", sess.ConnID, err)
				}
				continue
			}
			cb := d.Lookup(code)
			if cb == nil {
				continue
			}
			if cb.Binding.stateMask&stateBit == 0 {
				continue
			}
			d.invokeWithRecover(cb, sess, data)
		}
	})
}

// invokeWithRecover calls the binding's invoke under a recover barrier.
// A panicking handler drops the message and logs, rather than killing
// the cell loop.
func (d *inputDispatcher) invokeWithRecover(cb *CellBinding, sess *PlayerSession, data []byte) {
	defer func() {
		if r := recover(); r != nil {
			if d.eng.Log != nil {
				d.eng.Log.Log("input", "handler panic: code=%d conn=%d panic=%v",
					cb.Binding.code, sess.ConnID, r)
			}
		}
	}()
	cb.Binding.invoke(d.eng, d.stage, sess, data)
}

// LookupCellBinding finds the cellBinding for a code on the engine's
// dispatcher. Used by mmokit's invoker thunk to access pooled deps.
// Returns nil if engine has no dispatcher or code is not registered.
func LookupCellBinding(eng *Engine, code uint32) *CellBinding {
	if eng == nil || eng.inputDispatcher == nil {
		return nil
	}
	return eng.inputDispatcher.Lookup(code)
}

// DefaultEnvelopeParser is the wire-format parser the dispatcher uses
// unless overridden. Set by mmokit.init() to mmokit.ProtoEnvelopeParser
// so the engine doesn't import mmokit. nil on engines built outside the
// mmokit stack (some tests) — the dispatcher falls back to a no-op Tick
// when nil.
var DefaultEnvelopeParser EnvelopeParser
