// Package universe — VirtualConnManager implements net.ConnSender on
// node-mode processes. It owns a bidirectional mapping between the
// wire-format SessionKey {GatewayID, ConnID} and node-local uint32
// connIDs so the node's engine game loop continues using uint32 without
// knowing about gateways. Translation happens at the VCM boundary.
//
// Outbound Send / SendReliable encode MeshFrame.ClientFrame and forward
// to the gateway peer looked up in HostNetwork (tagged peerKindGateway).
// Inbound bytes arrive via HostNetwork.routeInboundFrame's ClientInput
// dispatch and land in per-session input buffers that the cell's input
// system drains every tick via DrainInput / DrainOpInput.
package universe

import (
	"sync"
	"sync/atomic"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/logger"
	pkgnet "github.com/zenion/mmoserver/pkg/net"
)

// Compile-time assertion: VirtualConnManager must satisfy pkgnet.ConnSender.
var _ pkgnet.ConnSender = (*VirtualConnManager)(nil)

// VirtualConnManager is the node-mode implementation of net.ConnSender.
// Each session maps a {GatewayID, ConnID} wire key to a node-local uint32
// connID. The game loop and ECS systems only ever see the local IDs; all
// gateway awareness is confined to this type and the HostNetwork I/O path.
type VirtualConnManager struct {
	hn  *HostNetwork
	log *logger.Logger

	nextLocal atomic.Uint32

	mu      sync.RWMutex
	byLocal map[uint32]*virtualSession
	byKey   map[SessionKey]*virtualSession
}

// virtualSession holds per-connection state for one remote player.
type virtualSession struct {
	key      SessionKey // {GatewayID, originalConnID}
	localID  uint32     // node-local monotonic ID
	username string
	epoch    uint64
	cellID   MeshCellID // owning cell on this node (set at RegisterSession, used by DropSession)

	inputMu sync.Mutex
	input   [][]byte // channel 0x00 (event) queue
	opInput [][]byte // channel 0x01 (ops) queue
}

// NewVirtualConnManager creates a VirtualConnManager backed by hn for
// outbound frame forwarding. hn may be nil in unit tests that only
// exercise the mapping layer (Send calls on a nil hn will log-and-drop).
func NewVirtualConnManager(hn *HostNetwork, log *logger.Logger) *VirtualConnManager {
	vcm := &VirtualConnManager{
		hn:      hn,
		log:     log,
		byLocal: make(map[uint32]*virtualSession),
		byKey:   make(map[SessionKey]*virtualSession),
	}
	vcm.nextLocal.Store(1) // reserve 0 as invalid
	return vcm
}

// RegisterSession allocates a new node-local connID for the given
// {GatewayID, ConnID} key. If a session already exists for that key, the
// epoch and cellID are updated and the same localID is returned. A warning
// is logged if the new epoch is lower than the existing one (possible stale
// call), but the update is accepted regardless — callers are trusted.
// cellID is the owning cell on this node; it is stored so DropSession can
// return it for cell-inbox routing of MsgPlayerDisconnected.
func (v *VirtualConnManager) RegisterSession(key SessionKey, username string, epoch uint64, cellID MeshCellID) uint32 {
	v.mu.Lock()
	defer v.mu.Unlock()

	if existing, ok := v.byKey[key]; ok {
		if epoch < existing.epoch {
			// Stale registration — preserve the higher epoch. Can happen
			// when handoff prepare (carrying entity handoff epoch) arrives
			// after the coordinator's SessionRegister (carrying session
			// epoch); the two counters are independent and the session
			// epoch is authoritative for frame routing.
			v.log.Log(CatMeshMsg, "vcm: RegisterSession stale epoch %d < %d for key %s — keeping higher", epoch, existing.epoch, key)
		} else {
			existing.epoch = epoch
		}
		existing.cellID = cellID
		return existing.localID
	}

	localID := v.nextLocal.Add(1) - 1 // pre-increment, recover pre-value
	sess := &virtualSession{
		key:      key,
		localID:  localID,
		username: username,
		epoch:    epoch,
		cellID:   cellID,
	}
	v.byLocal[localID] = sess
	v.byKey[key] = sess
	return localID
}

// DropSession removes the session for the given key from both indices.
// Returns the localID and cellID that were recorded so the caller can route
// a MsgPlayerDisconnected to the owning cell. Returns (0, "", false) if no
// session exists for that key.
func (v *VirtualConnManager) DropSession(key SessionKey) (localID uint32, cellID MeshCellID, ok bool) {
	v.mu.Lock()
	defer v.mu.Unlock()

	sess, exists := v.byKey[key]
	if !exists {
		return 0, "", false
	}
	delete(v.byKey, key)
	delete(v.byLocal, sess.localID)
	return sess.localID, sess.cellID, true
}

// LookupByKey returns the node-local connID for a wire-format key.
func (v *VirtualConnManager) LookupByKey(key SessionKey) (localID uint32, ok bool) {
	v.mu.RLock()
	sess, exists := v.byKey[key]
	v.mu.RUnlock()
	if !exists {
		return 0, false
	}
	return sess.localID, true
}

// LookupByLocal returns the wire-format key for a node-local connID.
// Mainly useful for logging and outbound routing.
func (v *VirtualConnManager) LookupByLocal(localID uint32) (key SessionKey, ok bool) {
	v.mu.RLock()
	sess, exists := v.byLocal[localID]
	v.mu.RUnlock()
	if !exists {
		return SessionKey{}, false
	}
	return sess.key, true
}

// ─── net.ConnSender implementation ───────────────────────────────────────────

// Send enqueues an ordered ClientFrame destined for the player's
// gateway. Channel 0x00 (events/state).
//
// Used by every replication frame (delta world updates) and by game
// code that calls Engine.ConnMgr.Send from the tick loop. The
// underlying gRPC stream is TCP-reliable; combined with the replication
// system's AckReliable baselines, dropping frames here would silently
// corrupt the client's delta-encoded view of the world — so this path
// must NOT drop on queue pressure the way the original SendLossy
// wrapper did. SendOrdered gives us bounded-wait enqueue with no
// stream.Send result wait: frames stay in order, drops are visible as
// backpressure errors (logged), and the game loop doesn't stall on
// remote round-trips.
func (v *VirtualConnManager) Send(localID uint32, data []byte) {
	v.forwardToGateway(localID, data, false)
}

// SendReliable enqueues a reliable ClientFrame destined for the player's
// gateway. Channel 0x00 (events/state) with synchronous delivery
// semantics — waits for the sender goroutine to hand the frame to the
// stream and reports the result. Used for critical messages like
// login/spawn acknowledgements where the caller needs to know whether
// the wire write succeeded.
func (v *VirtualConnManager) SendReliable(localID uint32, data []byte) {
	v.forwardToGateway(localID, data, true)
}

// InjectInput appends raw bytes to the session's input queue. Called by
// the local-shortcut path which bypasses the ClientInput proto and has no
// epoch. Channel is inferred from the data prefix byte: 0x01 → op queue,
// else event queue — matching the ConnManager.InjectInput convention.
func (v *VirtualConnManager) InjectInput(localID uint32, data []byte) {
	v.injectInputInner(localID, data)
}

// InjectInputWithEpoch is like InjectInput but first validates the epoch.
// If epoch > 0 and epoch < the session's current epoch, the input is
// dropped as stale (arrived during the handoff window). Called by
// HostNetwork.routeInboundFrame when a ClientInput frame arrives over
// MeshData.
func (v *VirtualConnManager) InjectInputWithEpoch(localID uint32, data []byte, epoch uint64) {
	if epoch > 0 {
		v.mu.RLock()
		sess, ok := v.byLocal[localID]
		v.mu.RUnlock()
		if !ok {
			return
		}
		if epoch < sess.epoch {
			return
		}
	}
	v.injectInputInner(localID, data)
}

func (v *VirtualConnManager) injectInputInner(localID uint32, data []byte) {
	v.mu.RLock()
	sess, ok := v.byLocal[localID]
	v.mu.RUnlock()
	if !ok {
		return
	}

	// Determine channel from first byte, same as ConnManager.
	isOp := len(data) > 0 && data[0] == 0x01

	sess.inputMu.Lock()
	if isOp {
		sess.opInput = append(sess.opInput, data)
	} else {
		sess.input = append(sess.input, data)
	}
	sess.inputMu.Unlock()
}

// DrainInput returns and clears the accumulated event-channel (0x00) input
// for the given local connID. Returns nil if the session is unknown or no
// data is queued.
func (v *VirtualConnManager) DrainInput(localID uint32) [][]byte {
	v.mu.RLock()
	sess, ok := v.byLocal[localID]
	v.mu.RUnlock()
	if !ok {
		return nil
	}

	sess.inputMu.Lock()
	out := sess.input
	sess.input = nil
	sess.inputMu.Unlock()
	return out
}

// DrainOpInput returns and clears the accumulated op-channel (0x01) input
// for the given local connID. Returns nil if the session is unknown or no
// data is queued.
func (v *VirtualConnManager) DrainOpInput(localID uint32) [][]byte {
	v.mu.RLock()
	sess, ok := v.byLocal[localID]
	v.mu.RUnlock()
	if !ok {
		return nil
	}

	sess.inputMu.Lock()
	out := sess.opInput
	sess.opInput = nil
	sess.inputMu.Unlock()
	return out
}

// ─── internal helpers ─────────────────────────────────────────────────────────

// forwardToGateway wraps data in a MeshFrame.ClientFrame and sends it to the
// gateway peer identified by the session's key. When reliable is true it uses
// SendReliableToGateway (blocks waiting for the stream.Send result); otherwise
// it uses SendOrderedToGateway (bounded-wait enqueue, no result wait). The
// ordered path is the default for hot replication traffic — matches the
// AckReliable contract the replication system is built on.
func (v *VirtualConnManager) forwardToGateway(localID uint32, data []byte, reliable bool) {
	v.mu.RLock()
	sess, ok := v.byLocal[localID]
	v.mu.RUnlock()
	if !ok {
		v.log.Log(CatMeshMsg, "vcm: Send/SendReliable for unknown localID %d, dropping", localID)
		return
	}

	if v.hn == nil {
		// nil hn is valid in unit tests that don't exercise outbound paths.
		return
	}

	frame := &meshpb.MeshFrame{
		Msg: &meshpb.MeshFrame_ClientFrame{
			ClientFrame: &meshpb.ClientFrame{
				GatewayId: sess.key.GatewayID,
				ConnId:    sess.key.ConnID,
				Data:      data,
				Epoch:     sess.epoch,
			},
		},
	}

	if reliable {
		if err := v.hn.SendReliableToGateway(sess.key.GatewayID, frame); err != nil {
			v.log.Log(CatMeshMsg, "vcm: SendReliableToGateway %s conn %d: %v", sess.key.GatewayID, sess.key.ConnID, err)
		}
	} else {
		if err := v.hn.SendOrderedToGateway(sess.key.GatewayID, frame); err != nil {
			v.log.Log(CatMeshMsg, "vcm: SendOrderedToGateway %s conn %d: %v", sess.key.GatewayID, sess.key.ConnID, err)
		}
	}
}
