package universe

import (
	"encoding/binary"
	"reflect"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/engine"
)

// CatClientInput is the log category for the typed client-input dispatch
// path (channel 0x02; mmokit.HandleClient). Frame-shape errors and
// untrusted typeIDs are logged here.
const CatClientInput = "input:client"

// Wire layout for one client-input frame (post channel-byte strip):
//
//	[u32 typeID][u32 bodyLen][bodyLen bytes body]
//
// typeID is fnv32(reflect.Type.String()) — matches BroadcastTypes /
// mmokit.TypeIDOf so client and server never disagree on which Go type
// a wire frame represents.
//
// The reflection codec (ReflectMarshal / ReflectUnmarshalOnStage)
// handles the body bytes — same codec used by the typed cross-cell
// message path and by the auto-broadcast queue, so client-input bytes
// round-trip through the exact same reflection layout the schema
// dump describes.
const clientInputHeaderBytes = 8

// DispatchClientInput drains the typed client-input channel (0x02) for
// every connection routed to this stage and dispatches each frame
// through the stage's MessageDispatcher.
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
		msgs := connMgr.DrainClientInput(sess.ConnID)
		if len(msgs) == 0 {
			return
		}
		for _, frame := range msgs {
			s.dispatchOneClientInput(sess, frame)
		}
	})
}

// dispatchOneClientInput decodes a single 0x02 frame and routes it
// through the typed-message dispatcher. Wraps the reflection / dispatch
// in a recover barrier so a single panicking handler drops one frame
// rather than crashing the cell loop.
func (s *Stage) dispatchOneClientInput(sess *engine.PlayerSession, frame []byte) {
	defer func() {
		if r := recover(); r != nil {
			s.eng.Log.Log(CatClientInput,
				"[%s] client-input handler panic: conn=%d panic=%v",
				s.cellID, sess.ConnID, r)
		}
	}()

	if len(frame) < clientInputHeaderBytes {
		s.eng.Log.Log(CatClientInput,
			"[%s] malformed client-input frame: conn=%d len=%d",
			s.cellID, sess.ConnID, len(frame))
		return
	}
	typeID := binary.LittleEndian.Uint32(frame[0:4])
	bodyLen := binary.LittleEndian.Uint32(frame[4:8])
	if uint64(clientInputHeaderBytes)+uint64(bodyLen) > uint64(len(frame)) {
		s.eng.Log.Log(CatClientInput,
			"[%s] truncated client-input frame: conn=%d typeID=%d bodyLen=%d frameLen=%d",
			s.cellID, sess.ConnID, typeID, bodyLen, len(frame))
		return
	}
	body := frame[clientInputHeaderBytes : clientInputHeaderBytes+bodyLen]

	msgType := ClientInputHooks.TypeOfTypeID(typeID)
	if msgType == nil {
		s.eng.Log.Log(CatClientInput,
			"[%s] untrusted client-input typeID: conn=%d typeID=%d (no HandleClient registration)",
			s.cellID, sess.ConnID, typeID)
		return
	}

	// Resolve the connection's player Entity. Sessions without a live
	// entity (e.g. mid-respawn / docked-without-ship in some games)
	// drop the frame — handlers always run against a live Entity.
	if sess.Entity == (ecs.Entity{}) || !s.eng.ECS.Alive(sess.Entity) {
		s.eng.Log.Log(CatClientInput,
			"[%s] client-input dropped, no live entity: conn=%d typeID=%d type=%s",
			s.cellID, sess.ConnID, typeID, msgType.String())
		return
	}
	netIDMap := s.NetworkIDMap()
	if !netIDMap.HasAll(sess.Entity) {
		s.eng.Log.Log(CatClientInput,
			"[%s] client-input dropped, entity has no NetworkID: conn=%d typeID=%d",
			s.cellID, sess.ConnID, typeID)
		return
	}
	playerNetID := netIDMap.Get(sess.Entity).ID

	// Allocate a fresh *T, decode body, dispatch.
	msgPtr := reflect.New(msgType)
	ReflectUnmarshalOnStage(s, body, msgPtr.Interface())
	s.Dispatcher().Invoke(playerNetID, msgPtr.Interface())
}
