package universe

import (
	"encoding/binary"
	"reflect"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/engine"
)

// CatClientInput is the log category for the typed client-input dispatch
// path (channel 0x00; mmokit.HandleClient). Frame-shape errors and
// untrusted typeIDs are logged here.
const CatClientInput = "input:client"

// Wire layout for one typed-input entry on channel 0x00 (post channel-byte
// strip):
//
//	[u32 typeID][u32 bodyLen][bodyLen bytes body]
//
// A single inbound frame may carry one OR many concatenated entries — the
// dispatcher walks until the buffer is exhausted. typeID is fnv32 of the
// reflect.Type's package-qualified name; matches BroadcastTypes /
// mmokit.TypeIDOf so client and server agree on the Go type for each
// frame. The reflection codec (ReflectMarshal / ReflectUnmarshalOnStage)
// handles the body bytes.
const clientInputHeaderBytes = 8

// DispatchClientInput drains the inbound event channel (0x00) for every
// connection routed to this stage and dispatches each frame through the
// stage's MessageDispatcher.
//
// Each frame on 0x00 is a typed-event payload of the form
// [u32 typeID][u32 bodyLen][body], possibly batched.
//
// Trust contract — what the framework guarantees to the handler:
//
//   - The connection that originated the frame owns the resolved
//     player Entity (looked up via PlayerManager.ByConnID → s.Entity).
//   - The Entity is alive on this stage (zero/dead-entity sessions
//     drop the frame).
//   - The wire body decoded successfully via the reflection codec.
//
// What the framework does NOT validate:
//
//   - Field values (range, enum, etc.) — handler responsibility.
//   - Player state (StateActive vs StateDocked) — handler responsibility.
//   - Rate limits — handler responsibility (or future middleware).
//
// Untrusted typeIDs (no matching registration) and malformed frames are
// logged under CatClientInput and dropped.
//
// Called once per tick from the engine loop via Engine.clientInputTick.
func (s *Stage) DispatchClientInput() {
	if s == nil || s.eng == nil {
		return
	}
	if ClientInputHooks.TypeOfTypeID == nil {
		// pkg/mmokit not imported (tests built standalone): no handlers
		// can possibly be registered, so dropping is the correct silent
		// behavior.
		return
	}

	connMgr := s.eng.ConnMgr
	if connMgr == nil {
		return
	}

	s.eng.Players.ForEachConnected(func(sess *engine.PlayerSession) {
		msgs := connMgr.DrainInput(sess.ConnID)
		if len(msgs) == 0 {
			return
		}
		for _, frame := range msgs {
			s.dispatchInboundEventFrame(sess, frame)
		}
	})
}

// dispatchInboundEventFrame consumes one inbound 0x00 frame for sess. The
// channel byte has already been stripped by the read pump / forwardChannel
// path, so frame is the raw payload. Walks concatenated typed entries until
// the buffer is exhausted.
func (s *Stage) dispatchInboundEventFrame(sess *engine.PlayerSession, frame []byte) {
	if len(frame) == 0 {
		return
	}

	off := 0
	for off+clientInputHeaderBytes <= len(frame) {
		typeID := binary.LittleEndian.Uint32(frame[off : off+4])
		bodyLen := binary.LittleEndian.Uint32(frame[off+4 : off+8])
		off += clientInputHeaderBytes
		if uint64(bodyLen) > uint64(len(frame)-off) {
			s.eng.Log.Log(CatClientInput,
				"[%s] truncated client-input entry: conn=%d typeID=%#x bodyLen=%d remaining=%d",
				s.cellID, sess.ConnID, typeID, bodyLen, len(frame)-off)
			return
		}
		body := frame[off : off+int(bodyLen)]
		off += int(bodyLen)
		s.dispatchOneClientInput(sess, typeID, body)
	}
	if off != len(frame) {
		s.eng.Log.Log(CatClientInput,
			"[%s] malformed client-input frame: conn=%d remaining=%d (header underrun)",
			s.cellID, sess.ConnID, len(frame)-off)
	}
}

// dispatchOneClientInput resolves the player entity for sess, looks up the
// typed-message handler for typeID, decodes body, and invokes the handler.
// Wraps the reflection / dispatch in a recover barrier so a single
// panicking handler drops one frame rather than crashing the cell loop.
func (s *Stage) dispatchOneClientInput(sess *engine.PlayerSession, typeID uint32, body []byte) {
	defer func() {
		if r := recover(); r != nil {
			s.eng.Log.Log(CatClientInput,
				"[%s] client-input handler panic: conn=%d panic=%v",
				s.cellID, sess.ConnID, r)
		}
	}()

	msgType := ClientInputHooks.TypeOfTypeID(typeID)
	if msgType == nil {
		s.eng.Log.Log(CatClientInput,
			"[%s] untrusted client-input typeID: conn=%d typeID=%#x (no HandleClient registration)",
			s.cellID, sess.ConnID, typeID)
		return
	}

	// Resolve the connection's player Entity. Sessions without a live
	// entity (e.g. mid-respawn / docked-without-ship in some games)
	// drop the frame — handlers always run against a live Entity.
	if sess.Entity == (ecs.Entity{}) || !s.eng.ECS.Alive(sess.Entity) {
		s.eng.Log.Log(CatClientInput,
			"[%s] client-input dropped, no live entity: conn=%d typeID=%#x type=%s",
			s.cellID, sess.ConnID, typeID, msgType.String())
		return
	}
	netIDMap := s.NetworkIDMap()
	if !netIDMap.HasAll(sess.Entity) {
		s.eng.Log.Log(CatClientInput,
			"[%s] client-input dropped, entity has no NetworkID: conn=%d typeID=%#x",
			s.cellID, sess.ConnID, typeID)
		return
	}
	playerNetID := netIDMap.Get(sess.Entity).ID

	// Allocate a fresh *T, decode body, dispatch.
	msgPtr := reflect.New(msgType)
	ReflectUnmarshalOnStage(s, body, msgPtr.Interface())
	s.Dispatcher().Invoke(playerNetID, msgPtr.Interface())
}
