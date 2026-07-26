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
	"errors"
	"sync"
	"sync/atomic"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/logger"
	pkgnet "github.com/zenion/mmoserver/pkg/net"
)

// Compile-time assertion: VirtualConnManager must satisfy pkgnet.ConnSender.
var _ pkgnet.ConnSender = (*VirtualConnManager)(nil)
var _ pkgnet.TrackedReplicationSender = (*VirtualConnManager)(nil)
var _ pkgnet.ReplicationReceiptSource = (*VirtualConnManager)(nil)

// VirtualConnManager is the node-mode implementation of net.ConnSender.
// Each session maps a {GatewayID, ConnID} wire key to a node-local uint32
// connID. The game loop and ECS systems only ever see the local IDs; all
// gateway awareness is confined to this type and the HostNetwork I/O path.
type VirtualConnManager struct {
	hn  *HostNetwork
	log *logger.Logger

	nextLocal              atomic.Uint32
	nextReplicationReceipt atomic.Uint64

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

	inputMu             sync.Mutex
	input               [][]byte // channel 0x00 (event + typed client-input post Phase 5) queue
	opInput             [][]byte // channel 0x01 (ops) queue
	replicationReceipts []pkgnet.ReplicationReceipt
	pendingReplication  map[uint64]pendingReplicationReceipt
	pendingTokens       []uint64
	pendingTokenNext    int
}

// Keep correlation state bounded even when a gateway or host disappears
// after mesh admission. Replication has its own retry/resync policy; this map
// exists only to translate opaque mesh receipt tokens back to frame sequence
// numbers without allowing an unreachable gateway to grow memory forever.
const (
	maxPendingReplicationReceipts = 256
	maxQueuedReplicationReceipts  = 256
)

type pendingReplicationReceipt struct {
	scope       uint64
	streamEpoch uint32
	seq         uint32
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
// cellID is updated and the same localID is returned. A higher epoch replaces
// the current epoch and resets receipt correlation; a lower epoch is logged
// and ignored as stale.
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
		} else if epoch > existing.epoch {
			existing.epoch = epoch
			existing.inputMu.Lock()
			existing.pendingReplication = nil
			existing.pendingTokens = nil
			existing.pendingTokenNext = 0
			existing.replicationReceipts = nil
			existing.inputMu.Unlock()
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
	if exists {
		localID = sess.localID
	}
	v.mu.RUnlock()
	if !exists {
		return 0, false
	}
	return localID, true
}

// LookupByLocal returns the wire-format key for a node-local connID.
// Mainly useful for logging and outbound routing.
func (v *VirtualConnManager) LookupByLocal(localID uint32) (key SessionKey, ok bool) {
	key, _, ok = v.LookupRouteByLocal(localID)
	return key, ok
}

// LookupRouteByLocal snapshots the stable session key and its current routing
// epoch together. Callers forwarding data across hosts must carry both so the
// destination can reject a delayed frame from a superseded route generation.
func (v *VirtualConnManager) LookupRouteByLocal(localID uint32) (key SessionKey, epoch uint64, ok bool) {
	v.mu.RLock()
	sess, exists := v.byLocal[localID]
	if exists {
		key = sess.key
		epoch = sess.epoch
	}
	v.mu.RUnlock()
	if !exists {
		return SessionKey{}, 0, false
	}
	return key, epoch, true
}

// ─── net.ConnSender implementation ───────────────────────────────────────────

// Send enqueues an ordered ClientFrame destined for the player's
// gateway. Channel 0x00 (events/state).
//
// Used by every replication frame (delta world updates) and by game
// code that calls Engine.ConnMgr.Send from the tick loop. The
// underlying gRPC stream is ordered. Delta replication must not advance its
// accepted baseline when this boundary rejects a frame, so SendOrdered uses a
// bounded-wait enqueue and surfaces that outcome without making the game loop
// wait for a remote round-trip.
func (v *VirtualConnManager) Send(localID uint32, data []byte) pkgnet.SendResult {
	return v.forwardToGateway(localID, data, false)
}

// SendReliable enqueues a reliable ClientFrame destined for the player's
// gateway. Channel 0x00 (events/state). It waits for the sender goroutine to
// hand the frame to the mesh stream and reports that link-level result. This
// is not an acknowledgement from the gateway's client transport or browser.
func (v *VirtualConnManager) SendReliable(localID uint32, data []byte) pkgnet.SendResult {
	return v.forwardToGateway(localID, data, true)
}

// SendReplication submits an ordered replication frame and asks the gateway
// to acknowledge the final client-transport enqueue asynchronously. The
// immediate result describes only mesh admission; callers advance replication
// state later from DrainReplicationReceipts.
func (v *VirtualConnManager) SendReplication(localID uint32, scope uint64, streamEpoch, seq uint32, data []byte) pkgnet.SendResult {
	if scope == 0 {
		return pkgnet.SendResult{Disposition: pkgnet.SendFailed}
	}
	v.mu.RLock()
	sess, ok := v.byLocal[localID]
	if !ok {
		v.mu.RUnlock()
		v.log.Log(CatMeshMsg, "vcm: SendReplication for unknown localID %d, dropping", localID)
		return pkgnet.SendResult{Disposition: pkgnet.SendNoRoute}
	}
	if v.hn == nil {
		v.mu.RUnlock()
		return pkgnet.SendResult{Disposition: pkgnet.SendNoRoute}
	}
	sourceHostID := v.hn.hostID
	if sourceHostID == "" {
		v.mu.RUnlock()
		return pkgnet.SendResult{Disposition: pkgnet.SendNoRoute}
	}

	// Receipt tokens are VCM-global rather than derived from frame sequence.
	// Frame sequences may restart when authority moves to a new cell, while a
	// delayed mesh frame from the prior authority can still be in flight.
	token := v.nextReplicationReceipt.Add(1)
	key := sess.key
	epoch := sess.epoch
	sess.trackReplication(token, scope, streamEpoch, seq)
	v.mu.RUnlock()

	frame := &meshpb.MeshFrame{
		DestCellId: replicationReceiptMarker(sourceHostID, token),
		Msg: &meshpb.MeshFrame_ClientFrame{
			ClientFrame: &meshpb.ClientFrame{
				GatewayId: key.GatewayID,
				ConnId:    key.ConnID,
				Data:      data,
				Epoch:     epoch,
			},
		},
	}
	if err := v.hn.SendOrderedToGateway(key.GatewayID, frame); err != nil {
		sess.discardPendingReplication(token)
		v.log.Log(CatMeshMsg, "vcm: SendReplication to gateway %s conn %d: %v", key.GatewayID, key.ConnID, err)
		return meshSendFailure(err)
	}
	return meshTrackedAcceptedResult()
}

// DrainReplicationReceipts returns and clears gateway enqueue receipts for a
// single VCM-local connection and writer scope. The scope is essential during
// handoff overlap, when source and destination replication systems can share
// the same VCM-local connection without sharing transaction state.
func (v *VirtualConnManager) DrainReplicationReceipts(localID uint32, scope uint64) []pkgnet.ReplicationReceipt {
	if scope == 0 {
		return nil
	}
	v.mu.RLock()
	sess, ok := v.byLocal[localID]
	v.mu.RUnlock()
	if !ok {
		return nil
	}

	sess.inputMu.Lock()
	matchCount := 0
	for _, receipt := range sess.replicationReceipts {
		if receipt.Scope == scope {
			matchCount++
		}
	}
	if matchCount == 0 {
		sess.inputMu.Unlock()
		return nil
	}
	out := make([]pkgnet.ReplicationReceipt, 0, matchCount)
	remaining := sess.replicationReceipts[:0]
	for _, receipt := range sess.replicationReceipts {
		if receipt.Scope == scope {
			out = append(out, receipt)
		} else {
			remaining = append(remaining, receipt)
		}
	}
	if len(remaining) == 0 {
		sess.replicationReceipts = nil
	} else {
		sess.replicationReceipts = remaining
	}
	sess.inputMu.Unlock()
	return out
}

// recordReplicationReceipt appends a receipt only when it belongs to the
// session's current routing epoch. The opaque token resolves both writer scope
// and sequence so a delayed acknowledgement from the prior authority cannot
// commit a new cell's transaction with a colliding sequence.
func (v *VirtualConnManager) recordReplicationReceipt(localID uint32, epoch uint64, token uint64, result pkgnet.SendResult) bool {
	v.mu.RLock()
	sess, ok := v.byLocal[localID]
	if !ok || sess.epoch != epoch {
		v.mu.RUnlock()
		return false
	}
	sess.inputMu.Lock()
	pending, ok := sess.pendingReplication[token]
	if !ok {
		sess.inputMu.Unlock()
		v.mu.RUnlock()
		return false
	}
	delete(sess.pendingReplication, token)
	receipt := pkgnet.ReplicationReceipt{
		ConnID:      localID,
		Scope:       pending.scope,
		StreamEpoch: pending.streamEpoch,
		Seq:         pending.seq,
		Result:      result,
	}
	if len(sess.replicationReceipts) >= maxQueuedReplicationReceipts {
		// A retired writer scope may never drain its final receipt. Evict the
		// oldest completed receipt rather than allowing repeated handoffs to
		// grow this per-session queue without bound. The affected writer will
		// take its normal receipt-timeout/full-resync path.
		copy(sess.replicationReceipts, sess.replicationReceipts[1:])
		sess.replicationReceipts[len(sess.replicationReceipts)-1] = receipt
	} else {
		sess.replicationReceipts = append(sess.replicationReceipts, receipt)
	}
	sess.inputMu.Unlock()
	v.mu.RUnlock()
	return true
}

func (s *virtualSession) trackReplication(token, scope uint64, streamEpoch, seq uint32) {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()

	if s.pendingReplication == nil {
		s.pendingReplication = make(map[uint64]pendingReplicationReceipt, maxPendingReplicationReceipts)
	}
	if len(s.pendingTokens) < maxPendingReplicationReceipts {
		s.pendingTokens = append(s.pendingTokens, token)
	} else {
		oldToken := s.pendingTokens[s.pendingTokenNext]
		delete(s.pendingReplication, oldToken)
		s.pendingTokens[s.pendingTokenNext] = token
		s.pendingTokenNext = (s.pendingTokenNext + 1) % maxPendingReplicationReceipts
	}
	s.pendingReplication[token] = pendingReplicationReceipt{
		scope:       scope,
		streamEpoch: streamEpoch,
		seq:         seq,
	}
}

func (s *virtualSession) discardPendingReplication(token uint64) {
	s.inputMu.Lock()
	delete(s.pendingReplication, token)
	s.inputMu.Unlock()
}

// InjectInput appends raw bytes to the session's event-channel (0x00) input
// queue. Used by the same-host MsgForwardInput path between cells; epoch is
// not validated.
func (v *VirtualConnManager) InjectInput(localID uint32, data []byte) {
	v.appendChannel(localID, data, pkgnet.ChannelEvent)
}

// InjectChannelInputWithEpoch is the gateway → host inbound path. The
// gateway tags every forwarded frame with its source wire channel
// (pkgnet.ChannelEvent / ChannelOperation). The host routes into the
// matching per-session queue. Frames whose epoch is older than the
// session's current epoch are dropped as stale (arrived during a handoff
// window).
func (v *VirtualConnManager) InjectChannelInputWithEpoch(localID uint32, data []byte, epoch uint64, channel byte) {
	if epoch > 0 {
		v.mu.RLock()
		sess, ok := v.byLocal[localID]
		var currentEpoch uint64
		if ok {
			currentEpoch = sess.epoch
		}
		v.mu.RUnlock()
		if !ok {
			return
		}
		if epoch < currentEpoch {
			return
		}
	}
	v.appendChannel(localID, data, channel)
}

// InjectForwardedInputWithEpoch admits the current session generation and its
// immediate predecessor. A handoff's source necessarily observed generation N
// while the coordinator advances the destination to N+1; that one-generation
// grace is the whole purpose of source-side input forwarding. Older forwards
// are stale, and a pre-registered destination session at epoch zero accepts the
// frame while its SessionRegister update is still racing across MeshControl.
func (v *VirtualConnManager) InjectForwardedInputWithEpoch(localID uint32, data []byte, sourceEpoch uint64, channel byte) bool {
	v.mu.RLock()
	sess, ok := v.byLocal[localID]
	var currentEpoch uint64
	if ok {
		currentEpoch = sess.epoch
	}
	v.mu.RUnlock()
	if !ok {
		return false
	}
	if currentEpoch != 0 && sourceEpoch != currentEpoch && sourceEpoch+1 != currentEpoch {
		return false
	}
	v.appendChannel(localID, data, channel)
	return true
}

func (v *VirtualConnManager) appendChannel(localID uint32, data []byte, channel byte) {
	v.mu.RLock()
	sess, ok := v.byLocal[localID]
	v.mu.RUnlock()
	if !ok {
		return
	}

	sess.inputMu.Lock()
	switch channel {
	case pkgnet.ChannelOperation:
		sess.opInput = append(sess.opInput, data)
	default:
		// ChannelEvent (0x00) plus typed client-input frames after
		// Plan 1 Phase 5 unified them onto the same channel. The
		// typeID registry disambiguates downstream.
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

// ActiveConnIDs returns a snapshot of every registered VCM-local connID.
// Used by the typed-op poll loop on host processes — the same drain
// pattern the gateway uses against its WS ConnManager. Order is
// unspecified.
func (v *VirtualConnManager) ActiveConnIDs() []uint32 {
	v.mu.RLock()
	out := make([]uint32, 0, len(v.byLocal))
	for id := range v.byLocal {
		out = append(out, id)
	}
	v.mu.RUnlock()
	return out
}

// RemoteAddrString satisfies the ops.DrainSource contract. The host
// can't observe the original WS RemoteAddr — that lives at the gateway —
// so this is always empty. Handlers that rely on ClientIP need the
// gateway to forward it explicitly (e.g. through the OpContext bag);
// today no handler does.
func (v *VirtualConnManager) RemoteAddrString(localID uint32) string {
	return ""
}

// ─── internal helpers ─────────────────────────────────────────────────────────

// forwardToGateway wraps data in a MeshFrame.ClientFrame and sends it to the
// gateway peer identified by the session's key. When reliable is true it uses
// SendReliableToGateway (blocks waiting for the stream.Send result); otherwise
// it uses SendOrderedToGateway (bounded-wait enqueue, no result wait). The
// disposition describes mesh acceptance; the delivery class stays
// conservative until the gateway reports its final client enqueue.
func (v *VirtualConnManager) forwardToGateway(localID uint32, data []byte, reliable bool) pkgnet.SendResult {
	v.mu.RLock()
	sess, ok := v.byLocal[localID]
	var key SessionKey
	var epoch uint64
	if ok {
		key = sess.key
		epoch = sess.epoch
	}
	v.mu.RUnlock()
	if !ok {
		v.log.Log(CatMeshMsg, "vcm: Send/SendReliable for unknown localID %d, dropping", localID)
		return pkgnet.SendResult{Disposition: pkgnet.SendNoRoute}
	}

	if v.hn == nil {
		// nil hn is valid in unit tests that don't exercise outbound paths.
		return pkgnet.SendResult{Disposition: pkgnet.SendNoRoute}
	}

	frame := &meshpb.MeshFrame{
		Msg: &meshpb.MeshFrame_ClientFrame{
			ClientFrame: &meshpb.ClientFrame{
				GatewayId: key.GatewayID,
				ConnId:    key.ConnID,
				Data:      data,
				Epoch:     epoch,
			},
		},
	}

	if reliable {
		if err := v.hn.SendReliableToGateway(key.GatewayID, frame); err != nil {
			v.log.Log(CatMeshMsg, "vcm: SendReliableToGateway %s conn %d: %v", key.GatewayID, key.ConnID, err)
			return meshSendFailure(err)
		}
		return meshAcceptedResult()
	} else {
		if err := v.hn.SendOrderedToGateway(key.GatewayID, frame); err != nil {
			v.log.Log(CatMeshMsg, "vcm: SendOrderedToGateway %s conn %d: %v", key.GatewayID, key.ConnID, err)
			return meshSendFailure(err)
		}
		return meshAcceptedResult()
	}
}

func meshAcceptedResult() pkgnet.SendResult {
	// The mesh write is ordered (and the reliable variant waits for stream
	// Send), but the gateway currently provides no receipt for its final
	// client-transport enqueue. Until that handshake exists, report only the
	// weakest end-to-end delivery class.
	return pkgnet.SendResult{
		Disposition: pkgnet.SendQueued,
		Delivery:    pkgnet.DeliveryBestEffort,
	}
}

func meshTrackedAcceptedResult() pkgnet.SendResult {
	// SendReplication installs a correlation token before enqueueing, so a
	// queued result promises ordered mesh admission while the later receipt is
	// still required to establish reliable client-transport admission.
	return pkgnet.SendResult{
		Disposition: pkgnet.SendQueued,
		Delivery:    pkgnet.DeliveryOrdered,
	}
}

func meshSendFailure(err error) pkgnet.SendResult {
	switch {
	case errors.Is(err, errPeerNoRoute):
		return pkgnet.SendResult{Disposition: pkgnet.SendNoRoute, Err: err}
	case errors.Is(err, errPeerBackpressure):
		return pkgnet.SendResult{Disposition: pkgnet.SendBackpressure, Err: err}
	case errors.Is(err, errPeerIndeterminate):
		return pkgnet.SendResult{Disposition: pkgnet.SendIndeterminate, Err: err}
	default:
		return pkgnet.SendResult{Disposition: pkgnet.SendFailed, Err: err}
	}
}
