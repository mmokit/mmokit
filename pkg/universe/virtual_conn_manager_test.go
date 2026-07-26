package universe

import (
	"sync"
	"testing"

	"github.com/zenion/mmoserver/pkg/logger"
	pkgnet "github.com/zenion/mmoserver/pkg/net"
)

func testVCMLogger() *logger.Logger {
	return logger.New(CatMeshMsg)
}

func TestVCM_RegisterLookupDrop(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	key := SessionKey{GatewayID: "gw-1", ConnID: 42}

	localID := vcm.RegisterSession(key, "alice", 1, "cell_0_0")
	if localID == 0 {
		t.Fatal("RegisterSession returned 0 (invalid)")
	}

	got, ok := vcm.LookupByKey(key)
	if !ok {
		t.Fatal("LookupByKey: expected ok=true after register")
	}
	if got != localID {
		t.Errorf("LookupByKey = %d, want %d", got, localID)
	}

	droppedID, cellID, ok := vcm.DropSession(key)
	if !ok {
		t.Fatal("DropSession: expected ok=true")
	}
	if droppedID != localID {
		t.Errorf("DropSession localID = %d, want %d", droppedID, localID)
	}
	if cellID != "cell_0_0" {
		t.Errorf("DropSession cellID = %q, want %q", cellID, "cell_0_0")
	}

	_, ok = vcm.LookupByKey(key)
	if ok {
		t.Error("LookupByKey after Drop: expected ok=false")
	}
}

func TestVCM_DoubleRegister(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	key := SessionKey{GatewayID: "gw-1", ConnID: 7}

	id1 := vcm.RegisterSession(key, "bob", 1, "cell_0_0")
	id2 := vcm.RegisterSession(key, "bob", 2, "cell_0_1")

	if id1 != id2 {
		t.Errorf("double-register returned different IDs: %d vs %d", id1, id2)
	}

	// Epoch must be updated to 2 on the stored session.
	vcm.mu.RLock()
	sess := vcm.byKey[key]
	vcm.mu.RUnlock()
	if sess == nil {
		t.Fatal("session missing after double-register")
	}
	if sess.epoch != 2 {
		t.Errorf("epoch = %d after second register, want 2", sess.epoch)
	}
	if sess.cellID != "cell_0_1" {
		t.Errorf("cellID = %q after second register, want %q", sess.cellID, "cell_0_1")
	}
}

func TestVCM_InjectDrainInput(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	key := SessionKey{GatewayID: "gw-1", ConnID: 1}
	localID := vcm.RegisterSession(key, "carol", 1, "cell_0_0")

	eventData := []byte{0xAB, 0xCD}
	opData := []byte{0x12, 0x34}

	vcm.InjectChannelInputWithEpoch(localID, eventData, 0, pkgnet.ChannelEvent)
	vcm.InjectChannelInputWithEpoch(localID, opData, 0, pkgnet.ChannelOperation)

	events := vcm.DrainInput(localID)
	if len(events) != 1 {
		t.Fatalf("DrainInput: got %d items, want 1", len(events))
	}
	if string(events[0]) != string(eventData) {
		t.Errorf("DrainInput[0] = %v, want %v", events[0], eventData)
	}

	ops := vcm.DrainOpInput(localID)
	if len(ops) != 1 {
		t.Fatalf("DrainOpInput: got %d items, want 1", len(ops))
	}
	if string(ops[0]) != string(opData) {
		t.Errorf("DrainOpInput[0] = %v, want %v", ops[0], opData)
	}

	// Second drain should return nil (queue cleared).
	if vcm.DrainInput(localID) != nil {
		t.Error("DrainInput second call: expected nil")
	}
	if vcm.DrainOpInput(localID) != nil {
		t.Error("DrainOpInput second call: expected nil")
	}
}

func TestVCM_InjectChannelInputRejectsStaleSessionEpoch(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	localID := vcm.RegisterSession(
		SessionKey{GatewayID: "gw-epoch", ConnID: 2},
		"epoch-input",
		12,
		"cell_0_0",
	)

	vcm.InjectChannelInputWithEpoch(localID, []byte{0x0b}, 11, pkgnet.ChannelEvent)
	if got := vcm.DrainInput(localID); got != nil {
		t.Fatalf("stale epoch reached input queue: %v", got)
	}

	vcm.InjectChannelInputWithEpoch(localID, []byte{0x0c}, 12, pkgnet.ChannelEvent)
	got := vcm.DrainInput(localID)
	if len(got) != 1 || len(got[0]) != 1 || got[0][0] != 0x0c {
		t.Fatalf("current epoch input = %v, want [[0x0c]]", got)
	}
}

func TestVCM_InjectForwardedInputAcceptsOnlyHandoffPredecessor(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	localID := vcm.RegisterSession(
		SessionKey{GatewayID: "gw-forward", ConnID: 3},
		"forward-input",
		12,
		"cell_1_0",
	)

	if vcm.InjectForwardedInputWithEpoch(localID, []byte{0x0a}, 10, pkgnet.ChannelEvent) {
		t.Fatal("input older than the immediate predecessor was accepted")
	}
	if !vcm.InjectForwardedInputWithEpoch(localID, []byte{0x0b}, 11, pkgnet.ChannelEvent) {
		t.Fatal("handoff predecessor input was rejected")
	}
	if !vcm.InjectForwardedInputWithEpoch(localID, []byte{0x0c}, 12, pkgnet.ChannelEvent) {
		t.Fatal("current-generation forwarded input was rejected")
	}
	got := vcm.DrainInput(localID)
	if len(got) != 2 || got[0][0] != 0x0b || got[1][0] != 0x0c {
		t.Fatalf("forwarded input queue = %v, want [[0x0b] [0x0c]]", got)
	}
}

func TestVCM_DropUnknownKey(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	key := SessionKey{GatewayID: "ghost", ConnID: 999}

	localID, cellID, ok := vcm.DropSession(key)
	if ok {
		t.Error("DropSession on missing key: expected ok=false")
	}
	if localID != 0 {
		t.Errorf("DropSession on missing key: localID = %d, want 0", localID)
	}
	if cellID != "" {
		t.Errorf("DropSession on missing key: cellID = %q, want empty", cellID)
	}
}

func TestVCM_LookupByLocal(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	key := SessionKey{GatewayID: "gw-2", ConnID: 55}
	localID := vcm.RegisterSession(key, "dave", 1, "cell_1_0")

	got, ok := vcm.LookupByLocal(localID)
	if !ok {
		t.Fatal("LookupByLocal: expected ok=true after register")
	}
	if got != key {
		t.Errorf("LookupByLocal = %v, want %v", got, key)
	}
}

func TestVCM_LookupRouteByLocalSnapshotsEpoch(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	key := SessionKey{GatewayID: "gw-route", ConnID: 27}
	localID := vcm.RegisterSession(key, "epoch-user", 12, "cell_0_0")

	gotKey, gotEpoch, ok := vcm.LookupRouteByLocal(localID)
	if !ok || gotKey != key || gotEpoch != 12 {
		t.Fatalf("route = (%+v, %d, %v), want (%+v, 12, true)", gotKey, gotEpoch, ok, key)
	}
}

func TestVCM_SendWithNilHN(t *testing.T) {
	// nil hn is the test affordance; Send/SendReliable must not panic.
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	key := SessionKey{GatewayID: "gw-1", ConnID: 1}
	localID := vcm.RegisterSession(key, "eve", 1, "")

	if result := vcm.Send(localID, []byte{0x00, 0x01}); result.Disposition != pkgnet.SendNoRoute {
		t.Fatalf("Send disposition = %v, want no-route", result.Disposition)
	}
	if result := vcm.SendReliable(localID, []byte{0x00, 0x02}); result.Disposition != pkgnet.SendNoRoute {
		t.Fatalf("SendReliable disposition = %v, want no-route", result.Disposition)
	}
}

func TestVCM_SendUnknownSessionReturnsNoRoute(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	if result := vcm.Send(999, []byte{0x01}); result.Disposition != pkgnet.SendNoRoute {
		t.Fatalf("Send disposition = %v, want no-route", result.Disposition)
	}
}

func TestMeshSendFailureClassification(t *testing.T) {
	if got := meshSendFailure(errPeerNoRoute); got.Disposition != pkgnet.SendNoRoute {
		t.Fatalf("no-route classified as %v", got.Disposition)
	}
	if got := meshSendFailure(errPeerBackpressure); got.Disposition != pkgnet.SendBackpressure {
		t.Fatalf("backpressure classified as %v", got.Disposition)
	}
	if got := meshSendFailure(errPeerIndeterminate); got.Disposition != pkgnet.SendIndeterminate {
		t.Fatalf("indeterminate send classified as %v", got.Disposition)
	}
}

func TestMeshAcceptedResultDoesNotClaimGatewayDelivery(t *testing.T) {
	got := meshAcceptedResult()
	if !got.Queued() {
		t.Fatalf("mesh accepted result = %+v, want queued", got)
	}
	if got.Supports(pkgnet.DeliveryOrdered) {
		t.Fatalf("mesh result claimed unconfirmed end-to-end ordering: %+v", got)
	}
}

func TestVCM_SendReplicationUsesOpaqueTokenAndTracksSequence(t *testing.T) {
	hn := newManualHostNetwork(t)
	peer := newIdlePeer(t, "gw-1")
	peer.kind = peerKindGateway
	hn.peers["gw-1"] = peer
	vcm := NewVirtualConnManager(hn, testVCMLogger())
	localID := vcm.RegisterSession(SessionKey{GatewayID: "gw-1", ConnID: 44}, "alice", 9, "cell_0_0")

	const scope uint64 = 51
	const streamEpoch uint32 = 17
	result := vcm.SendReplication(localID, scope, streamEpoch, 73, []byte{0xAB})
	if !result.Supports(pkgnet.DeliveryOrdered) || result.Supports(pkgnet.DeliveryReliableOrdered) {
		t.Fatalf("SendReplication immediate result = %+v, want ordered pending receipt", result)
	}

	queued := <-peer.outQ
	hostID, token, ok := parseReplicationReceiptMarker(queued.frame.GetDestCellId())
	if !ok || token == 0 || hostID != hn.hostID {
		t.Fatalf("tracked frame marker = %q", queued.frame.GetDestCellId())
	}
	cf := queued.frame.GetClientFrame()
	if cf == nil || cf.GatewayId != "gw-1" || cf.ConnId != 44 || cf.Epoch != 9 {
		t.Fatalf("tracked client frame = %+v", cf)
	}

	vcm.mu.RLock()
	sess := vcm.byLocal[localID]
	vcm.mu.RUnlock()
	sess.inputMu.Lock()
	pending, tracked := sess.pendingReplication[token]
	sess.inputMu.Unlock()
	if !tracked || pending.scope != scope || pending.streamEpoch != streamEpoch || pending.seq != 73 {
		t.Fatalf("pending token maps to (%+v, %v), want scope=%d stream=%d seq=73", pending, tracked, scope, streamEpoch)
	}
}

func TestVCM_SendReplicationRejectCleansPendingCorrelation(t *testing.T) {
	hn := newManualHostNetwork(t) // no gateway peer: immediate no-route
	vcm := NewVirtualConnManager(hn, testVCMLogger())
	localID := vcm.RegisterSession(SessionKey{GatewayID: "missing-gw", ConnID: 44}, "alice", 9, "cell_0_0")

	result := vcm.SendReplication(localID, 52, 18, 74, []byte{0xAB})
	if result.Disposition != pkgnet.SendNoRoute {
		t.Fatalf("SendReplication result = %+v, want no-route", result)
	}
	vcm.mu.RLock()
	sess := vcm.byLocal[localID]
	vcm.mu.RUnlock()
	sess.inputMu.Lock()
	pending := len(sess.pendingReplication)
	sess.inputMu.Unlock()
	if pending != 0 {
		t.Fatalf("pending correlation count = %d after rejected mesh enqueue, want 0", pending)
	}
}

func TestRouteInboundReplicationReceiptResolvesTokenAndDrainsByLocalConn(t *testing.T) {
	hn := newManualHostNetwork(t)
	vcm := NewVirtualConnManager(hn, testVCMLogger())
	hn.vcm = vcm
	key := SessionKey{GatewayID: "gw-1", ConnID: 12}
	localID := vcm.RegisterSession(key, "alice", 8, "cell_0_0")

	vcm.mu.RLock()
	sess := vcm.byLocal[localID]
	vcm.mu.RUnlock()
	const scope uint64 = 61
	sess.trackReplication(101, scope, 19, 77)

	want := pkgnet.SendResult{Disposition: pkgnet.SendQueued, Delivery: pkgnet.DeliveryReliableOrdered}
	if err := hn.routeInboundFrame(newReplicationReceiptFrame(hn.hostID, "gw-1", 12, 8, 101, want)); err != nil {
		t.Fatalf("routeInboundFrame: %v", err)
	}
	receipts := vcm.DrainReplicationReceipts(localID, scope)
	if len(receipts) != 1 {
		t.Fatalf("receipt count = %d, want 1", len(receipts))
	}
	got := receipts[0]
	if got.ConnID != localID || got.Scope != scope || got.StreamEpoch != 19 || got.Seq != 77 || !got.Result.Supports(pkgnet.DeliveryReliableOrdered) {
		t.Fatalf("receipt = %+v", got)
	}
	if second := vcm.DrainReplicationReceipts(localID, scope); second != nil {
		t.Fatalf("second drain = %+v, want nil", second)
	}
	if inputs := vcm.DrainInput(localID); inputs != nil {
		t.Fatalf("receipt leaked into client input queue: %v", inputs)
	}
}

func TestRouteInboundReplicationReceiptRejectsStaleEpoch(t *testing.T) {
	hn := newManualHostNetwork(t)
	vcm := NewVirtualConnManager(hn, testVCMLogger())
	hn.vcm = vcm
	localID := vcm.RegisterSession(SessionKey{GatewayID: "gw-1", ConnID: 12}, "alice", 8, "cell_0_0")
	vcm.mu.RLock()
	sess := vcm.byLocal[localID]
	vcm.mu.RUnlock()
	const scope uint64 = 62
	sess.trackReplication(102, scope, 20, 78)

	result := pkgnet.SendResult{Disposition: pkgnet.SendQueued, Delivery: pkgnet.DeliveryReliableOrdered}
	if err := hn.routeInboundFrame(newReplicationReceiptFrame(hn.hostID, "gw-1", 12, 7, 102, result)); err != nil {
		t.Fatalf("routeInboundFrame stale receipt: %v", err)
	}
	if got := vcm.DrainReplicationReceipts(localID, scope); got != nil {
		t.Fatalf("stale receipt was recorded: %+v", got)
	}

	if err := hn.routeInboundFrame(newReplicationReceiptFrame(hn.hostID, "gw-1", 12, 8, 102, result)); err != nil {
		t.Fatalf("routeInboundFrame current receipt: %v", err)
	}
	if got := vcm.DrainReplicationReceipts(localID, scope); len(got) != 1 || got[0].Seq != 78 {
		t.Fatalf("current receipt = %+v, want seq 78", got)
	}
}

func TestRouteInboundReplicationReceiptRejectsDifferentTargetHost(t *testing.T) {
	hn := newManualHostNetwork(t)
	vcm := NewVirtualConnManager(hn, testVCMLogger())
	hn.vcm = vcm
	localID := vcm.RegisterSession(SessionKey{GatewayID: "gw-1", ConnID: 12}, "alice", 8, "cell_0_0")
	vcm.mu.RLock()
	sess := vcm.byLocal[localID]
	vcm.mu.RUnlock()
	const scope uint64 = 63
	sess.trackReplication(103, scope, 21, 79)

	result := pkgnet.SendResult{Disposition: pkgnet.SendQueued, Delivery: pkgnet.DeliveryReliableOrdered}
	wrongHost := newReplicationReceiptFrame("host-other", "gw-1", 12, 8, 103, result)
	if err := hn.routeInboundFrame(wrongHost); err != nil {
		t.Fatalf("routeInboundFrame wrong-host receipt: %v", err)
	}
	if got := vcm.DrainReplicationReceipts(localID, scope); got != nil {
		t.Fatalf("wrong-host receipt was recorded: %+v", got)
	}

	currentHost := newReplicationReceiptFrame(hn.hostID, "gw-1", 12, 8, 103, result)
	if err := hn.routeInboundFrame(currentHost); err != nil {
		t.Fatalf("routeInboundFrame current-host receipt: %v", err)
	}
	if got := vcm.DrainReplicationReceipts(localID, scope); len(got) != 1 || got[0].Seq != 79 {
		t.Fatalf("current-host receipt = %+v, want seq 79", got)
	}
}

func TestVirtualSessionPendingReplicationIsBounded(t *testing.T) {
	sess := &virtualSession{}
	for i := uint64(1); i <= maxPendingReplicationReceipts+20; i++ {
		sess.trackReplication(i, 1, 22, uint32(i))
	}
	sess.inputMu.Lock()
	defer sess.inputMu.Unlock()
	if got := len(sess.pendingReplication); got > maxPendingReplicationReceipts {
		t.Fatalf("pending token count = %d, max %d", got, maxPendingReplicationReceipts)
	}
	if _, ok := sess.pendingReplication[1]; ok {
		t.Fatal("oldest token was not evicted")
	}
	latest := uint64(maxPendingReplicationReceipts + 20)
	if pending, ok := sess.pendingReplication[latest]; !ok || pending.seq != uint32(latest) {
		t.Fatalf("latest token maps to (%+v, %v)", pending, ok)
	}
}

func TestVCM_DrainReplicationReceiptsDoesNotStealAnotherWriterScope(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	localID := vcm.RegisterSession(SessionKey{GatewayID: "gw-1", ConnID: 19}, "alice", 4, "cell_0_0")
	vcm.mu.RLock()
	sess := vcm.byLocal[localID]
	vcm.mu.RUnlock()
	sess.trackReplication(201, 71, 23, 1)
	sess.trackReplication(202, 72, 24, 1)
	result := pkgnet.SendResult{Disposition: pkgnet.SendQueued, Delivery: pkgnet.DeliveryReliableOrdered}
	if !vcm.recordReplicationReceipt(localID, 4, 201, result) || !vcm.recordReplicationReceipt(localID, 4, 202, result) {
		t.Fatal("failed to record scoped receipts")
	}

	first := vcm.DrainReplicationReceipts(localID, 71)
	if len(first) != 1 || first[0].Scope != 71 {
		t.Fatalf("scope 71 drain = %+v", first)
	}
	second := vcm.DrainReplicationReceipts(localID, 72)
	if len(second) != 1 || second[0].Scope != 72 {
		t.Fatalf("scope 72 receipt was stolen: %+v", second)
	}
}

func TestVCM_EpochAdvanceClearsPendingAndQueuedReceipts(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	key := SessionKey{GatewayID: "gw-1", ConnID: 20}
	localID := vcm.RegisterSession(key, "alice", 4, "cell_0_0")
	vcm.mu.RLock()
	sess := vcm.byLocal[localID]
	vcm.mu.RUnlock()
	sess.trackReplication(301, 81, 25, 1)
	sess.trackReplication(302, 81, 25, 2)
	result := pkgnet.SendResult{Disposition: pkgnet.SendQueued, Delivery: pkgnet.DeliveryReliableOrdered}
	if !vcm.recordReplicationReceipt(localID, 4, 301, result) {
		t.Fatal("failed to queue pre-handoff receipt")
	}

	if gotID := vcm.RegisterSession(key, "alice", 5, "cell_0_1"); gotID != localID {
		t.Fatalf("epoch update changed local ID from %d to %d", localID, gotID)
	}
	if got := vcm.DrainReplicationReceipts(localID, 81); got != nil {
		t.Fatalf("old epoch queued receipt survived: %+v", got)
	}
	if vcm.recordReplicationReceipt(localID, 5, 302, result) {
		t.Fatal("old epoch pending token survived epoch advance")
	}
}

func TestVCM_CompletedReplicationReceiptQueueIsBounded(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	localID := vcm.RegisterSession(SessionKey{GatewayID: "gw-1", ConnID: 21}, "alice", 6, "cell_0_0")
	vcm.mu.RLock()
	sess := vcm.byLocal[localID]
	vcm.mu.RUnlock()
	result := pkgnet.SendResult{Disposition: pkgnet.SendQueued, Delivery: pkgnet.DeliveryReliableOrdered}

	last := uint64(maxQueuedReplicationReceipts + 20)
	for token := uint64(1); token <= last; token++ {
		sess.trackReplication(token, token, 26, uint32(token))
		if !vcm.recordReplicationReceipt(localID, 6, token, result) {
			t.Fatalf("failed to record token %d", token)
		}
	}
	sess.inputMu.Lock()
	queued := len(sess.replicationReceipts)
	sess.inputMu.Unlock()
	if queued != maxQueuedReplicationReceipts {
		t.Fatalf("completed receipt queue length = %d, want %d", queued, maxQueuedReplicationReceipts)
	}
	if got := vcm.DrainReplicationReceipts(localID, 1); got != nil {
		t.Fatalf("oldest evicted scope still queued: %+v", got)
	}
	if got := vcm.DrainReplicationReceipts(localID, last); len(got) != 1 || got[0].Seq != uint32(last) {
		t.Fatalf("newest scope receipt = %+v", got)
	}
}

func TestVCM_ConcurrentRegisterDrop(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	key := SessionKey{GatewayID: "gw-race", ConnID: 1}

	const iters = 100
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			vcm.RegisterSession(key, "racer", uint64(i+1), "cell_0_0")
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			vcm.DropSession(key)
		}
	}()

	wg.Wait()
}
