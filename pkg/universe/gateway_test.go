package universe

import (
	"testing"

	"github.com/google/uuid"

	"github.com/mmokit/mmokit/pkg/logger"

	meshpb "github.com/mmokit/mmokit/gen/go/meshpb"
)

func TestGatewayDispatchPlayerAssignmentCarriesInitialStreamGeneration(t *testing.T) {
	coord := newTestCoordinator(Config{CellsX: 1, CellsY: 1})
	cellID := CellID{X: 0, Y: 0}.MeshID()
	cell := coord.Cells[cellID]
	g := &Gateway{
		id:       InprocGatewayID,
		coord:    coord,
		log:      coord.Log,
		sessions: make(map[uint32]*localSession),
	}
	sess := &localSession{
		connID:   71,
		userID:   uuid.New(),
		username: "assignment-generation",
		hostID:   "local",
		cellID:   cellID,
		epoch:    13,
	}

	if err := g.dispatchPlayerAssignment(sess); err != nil {
		t.Fatalf("dispatchPlayerAssignment: %v", err)
	}
	select {
	case msg := <-cell.Inbox:
		if msg.Assignment == nil {
			t.Fatal("PlayerAssignment payload is nil")
		}
		if msg.Assignment.StreamGeneration != 13 {
			t.Fatalf("StreamGeneration = %d, want gateway epoch 13", msg.Assignment.StreamGeneration)
		}
	default:
		t.Fatal("target cell received no PlayerAssignment")
	}
}

func TestGatewayRemotePlayerAssignmentCarriesRouteAndStreamGenerations(t *testing.T) {
	hn := newManualHostNetwork(t)
	peer := &hostPeer{outQ: make(chan outboundFrame, 1)}
	hn.peers["host-a"] = peer

	captured := make(chan *meshpb.MeshFrame, 1)
	go func() {
		outbound := <-peer.outQ
		captured <- outbound.frame
		outbound.result <- nil
	}()

	g := &Gateway{
		id:          "gateway-a",
		hostNetwork: hn,
		log:         hn.log,
		sessions:    make(map[uint32]*localSession),
	}
	sess := &localSession{
		connID:   71,
		userID:   uuid.New(),
		username: "remote-assignment-generation",
		hostID:   "host-a",
		cellID:   "cell_0_0",
		epoch:    13,
	}

	if err := g.dispatchPlayerAssignmentRemote(sess); err != nil {
		t.Fatalf("dispatchPlayerAssignmentRemote: %v", err)
	}
	assignment := (<-captured).GetPlayerAssignment()
	if assignment == nil {
		t.Fatal("remote PlayerAssignment payload is nil")
	}
	if assignment.Epoch != 13 {
		t.Fatalf("route Epoch = %d, want 13", assignment.Epoch)
	}
	if assignment.StreamGeneration != 13 {
		t.Fatalf("StreamGeneration = %d, want initial generation 13", assignment.StreamGeneration)
	}
}

func TestGatewayLookupSessionReturnsRouteSnapshot(t *testing.T) {
	g := &Gateway{sessions: map[uint32]*localSession{
		7: {connID: 7, hostID: "host-a", cellID: "cell_0_0", epoch: 11},
	}}

	snapshot := g.lookupSession(7)
	if snapshot == nil {
		t.Fatal("lookupSession returned nil")
	}
	snapshot.hostID = "host-b"
	snapshot.cellID = "cell_1_0"
	snapshot.epoch = 12

	g.mu.RLock()
	stored := g.sessions[7]
	if stored.hostID != "host-a" || stored.cellID != "cell_0_0" || stored.epoch != 11 {
		t.Fatalf("mutating route snapshot changed stored session: %+v", stored)
	}
	g.mu.RUnlock()
}

// TestCellAtPosition_RoutesFreshLoginToOwningCell pins the fix for the
// pre-Location anyCellID bug. With CellSize=2000 and a 2×2 grid, a spawn
// point at world (0.85*cellSize, 0.85*cellSize) = (1700, 1700) must
// deterministically resolve to cell_0_0. Prior to the flip-day commit,
// cellAtPosition returned "" whenever the cell size was stale at
// resolve time (8192 default, not yet updated by SetCellSize), and the
// gateway's anyCellID fallback scattered logins across random cells.
func TestCellAtPosition_RoutesFreshLoginToOwningCell(t *testing.T) {

	// Build a cachedTopology snapshot matching the 4node-basic layout:
	// cells 0_0 / 1_0 / 0_1 / 1_1 at depth 0.
	topo := &cachedTopology{
		// Geometry is explicit now: a topology that does not know its cell
		// size cannot map a world position to a cell, and reading it from a
		// global is what CE-010 removes.
		baseCellSize: 2000,
		cells: map[MeshCellID]string{
			"cell_0_0": "host-a",
			"cell_1_0": "host-a",
			"cell_0_1": "host-b",
			"cell_1_1": "host-b",
		},
	}

	cases := []struct {
		name string
		x, y float32
		want string
	}{
		// Spawn from examples/4node-basic/main.go (registered via OnResolveSpawn):
		// {X: CellSize * 0.85, Y: CellSize * 0.85} with CellSize=2000.
		{"default_spawn_corner_of_0_0", 2000 * 0.85, 2000 * 0.85, "cell_0_0"},
		{"center_of_0_0", 1000, 1000, "cell_0_0"},
		{"center_of_1_0", 3000, 1000, "cell_1_0"},
		{"center_of_0_1", 1000, 3000, "cell_0_1"},
		{"center_of_1_1", 3000, 3000, "cell_1_1"},
		// Just inside cell boundaries:
		{"min_corner_0_0", 0, 0, "cell_0_0"},
		{"just_inside_right_edge_of_0_0", 1999.99, 1000, "cell_0_0"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := topo.cellAtPosition(tt.x, tt.y)
			if got != tt.want {
				t.Fatalf("cellAtPosition(%v, %v) = %q, want %q", tt.x, tt.y, got, tt.want)
			}
		})
	}
}

// TestCellAtPosition_OutOfBoundsReturnsEmpty pins the other half of the
// fix: the gateway's processLogin now rejects out-of-bounds spawn points
// (instead of silently falling through to anyCellID). cellAtPosition
// MUST return "" for points outside every cell, so the gateway sees
// a clear signal to reject the login.
func TestCellAtPosition_OutOfBoundsReturnsEmpty(t *testing.T) {

	topo := &cachedTopology{
		// Geometry is explicit now: a topology that does not know its cell
		// size cannot map a world position to a cell, and reading it from a
		// global is what CE-010 removes.
		baseCellSize: 2000,
		cells: map[MeshCellID]string{
			"cell_0_0": "host-a",
			"cell_1_0": "host-a",
			"cell_0_1": "host-b",
			"cell_1_1": "host-b",
		},
	}

	cases := []struct {
		name string
		x, y float32
	}{
		// The original bug: spawn from mmokit.WorldCenterOfCell(0, 0)
		// evaluated with stale CellSize=8192 → (4096, 4096). Outside the
		// 2x2 grid of 2000-cells.
		{"stale_cellSize_bug_point", 4096, 4096},
		{"negative_x", -1, 1000},
		{"negative_y", 1000, -1},
		{"x_past_grid_edge", 4001, 1000},
		{"y_past_grid_edge", 1000, 4001},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := topo.cellAtPosition(tt.x, tt.y)
			if got != "" {
				t.Fatalf("cellAtPosition(%v, %v) = %q, want \"\"", tt.x, tt.y, got)
			}
		})
	}
}

// TestApplyPeerList_DropsRemovedCells pins the bug where a standalone gateway
// kept stale child cells in its topology cache after a merge collapsed them
// back into the parent. PeerList carries the FULL ownership snapshot, so any
// cell missing from it must be evicted — otherwise cellAtPosition can return
// a non-existent cell ID (e.g. cell_d1_1_1 after merge of d1_* into 0_0) and
// processLogin assigns the player to a dead cell. Symptom in distributed mode:
// new logins routed to a missing cell after merge → client can never connect.
func TestApplyPeerList_DropsRemovedCells(t *testing.T) {

	topo := &cachedTopology{baseCellSize: 2000}

	// Initial broadcast: 0_0 split into 4 depth-1 children, all on host-a.
	// Other base cells live on host-b.
	topo.applyPeerList([]*meshpb.CellOwnership{
		{CellId: "cell_d1_0_0", HostId: "host-a"},
		{CellId: "cell_d1_1_0", HostId: "host-a"},
		{CellId: "cell_d1_0_1", HostId: "host-a"},
		{CellId: "cell_d1_1_1", HostId: "host-a"},
		{CellId: "cell_1_0", HostId: "host-b"},
		{CellId: "cell_0_1", HostId: "host-b"},
		{CellId: "cell_1_1", HostId: "host-b"},
	})

	// Spawn point (1700, 1700) lies inside d1_1_1 pre-merge.
	if got := topo.cellAtPosition(1700, 1700); got != "cell_d1_1_1" {
		t.Fatalf("pre-merge: cellAtPosition(1700,1700) = %q, want cell_d1_1_1", got)
	}

	// Coordinator broadcasts a fresh PeerList after MERGE collapses the
	// d1_* children back into parent 0_0 (now owned by host-b).
	topo.applyPeerList([]*meshpb.CellOwnership{
		{CellId: "cell_0_0", HostId: "host-b"},
		{CellId: "cell_1_0", HostId: "host-b"},
		{CellId: "cell_0_1", HostId: "host-b"},
		{CellId: "cell_1_1", HostId: "host-b"},
	})

	// Post-merge the spawn point must resolve to the parent cell — none of
	// the d1_* children may linger in the snapshot.
	if got := topo.cellAtPosition(1700, 1700); got != "cell_0_0" {
		t.Fatalf("post-merge: cellAtPosition(1700,1700) = %q, want cell_0_0", got)
	}
	for _, stale := range []string{"cell_d1_0_0", "cell_d1_1_0", "cell_d1_0_1", "cell_d1_1_1"} {
		if h := topo.HostForCell(MeshCellID(stale)); h != "local" {
			t.Fatalf("post-merge: HostForCell(%q) = %q, want \"local\" (cell evicted)", stale, h)
		}
	}
}

// A connection binds to one identity. Re-authenticating as the same user is the
// AUTH_VALIDATE_TOKEN path and must keep working; switching to a different user
// must not, or a UDP client could present a key for one account and then rebind
// the same connection to another over the op channel.
func TestGatewayOnAuthSuccessRefusesIdentityChange(t *testing.T) {
	// Bare gateway: dispatchPostAuthAssignment runs past the guard and finds no
	// cell, which is fine here — the assertion is about authStates, and a
	// coordinator would only add a spawn round trip to a test that does not
	// examine one.
	g := newTestBareGateway(t)

	first := uuid.New()
	g.onAuthSuccess(9, first, "alice", "tok-a", 0)
	if st, _ := g.authStateOf(9); st.userID != first {
		t.Fatalf("first bind did not take: %+v", st)
	}

	// A second identity on the same connection is refused outright.
	g.onAuthSuccess(9, uuid.New(), "mallory", "tok-b", 0)
	st, _ := g.authStateOf(9)
	if st.userID != first || st.username != "alice" {
		t.Fatalf("connection was rebound to a different user: %+v", st)
	}

	// The same user re-validating is not a rebind and must still be accepted —
	// an empty token means "keep the one you have".
	g.onAuthSuccess(9, first, "alice", "", 0)
	st, _ = g.authStateOf(9)
	if !st.authenticated || st.userID != first {
		t.Fatalf("same-user re-auth was rejected: %+v", st)
	}
	if st.sessionToken != "tok-a" {
		t.Fatalf("re-auth lost the session token: %q", st.sessionToken)
	}
}

// bindUDPSession is the universe-side half of the UDP identity wiring: the
// listener hands it the identity the presented key vouched for, and it must
// turn that into a bound player session exactly as a WebSocket cookie does.
func TestBindUDPSessionBindsIdentityToGateway(t *testing.T) {
	g := newTestBareGateway(t)
	// A live connection: bindUDPSession refuses to assign a player to one that
	// has already gone away, so the fixture has to register a real transport.
	connID := g.connMgr.AddTransport(&clientFrameTransport{}, "")
	p := &Process{Log: logger.New(), ConnMgr: g.connMgr, gateway: g}

	uid := uuid.New()
	p.bindUDPSession(connID, uid.String(), "tester")

	st, ok := g.authStateOf(connID)
	if !ok || !st.authenticated {
		t.Fatalf("conn %d not authenticated after bindUDPSession: %+v", connID, st)
	}
	if st.userID != uid || st.username != "tester" {
		t.Fatalf("bound identity = %s/%s, want %s/tester", st.userID, st.username, uid)
	}
	// The session token is deliberately empty: the key registry holds transport
	// secrets, and storing the session cookie beside them would make a registry
	// compromise worth an account rather than five minutes of UDP.
	if st.sessionToken != "" {
		t.Fatalf("UDP bind carried a session token %q; it must not", st.sessionToken)
	}
}

// A malformed user ID must refuse the bind rather than invent an identity —
// minting a random UUID here would spawn a brand-new character for a returning
// player instead of failing visibly.
func TestBindUDPSessionRefusesUnparseableUserID(t *testing.T) {
	g := newTestBareGateway(t)
	connID := g.connMgr.AddTransport(&clientFrameTransport{}, "")
	p := &Process{Log: logger.New(), ConnMgr: g.connMgr, gateway: g}

	p.bindUDPSession(connID, "not-a-uuid", "tester")

	if st, ok := g.authStateOf(connID); ok && st.authenticated {
		t.Fatalf("conn %d was authenticated from an unparseable user_id: %+v", connID, st)
	}
}

// A process with no gateway cannot bind anything. It must say so rather than
// panic on the nil dereference — the UDP listener calls this from its own
// goroutine, where a panic takes the process down.
func TestBindUDPSessionWithoutGatewayDoesNotPanic(t *testing.T) {
	p := &Process{Log: logger.New()}
	p.bindUDPSession(6, uuid.New().String(), "tester")
}

// The identity bind runs on its own goroutine, so the connection it names can
// already be gone — a client may send an authenticated Disconnect immediately
// after its ConnConfirm. Assigning a player then would spawn an entity nothing
// owns and stamp an activeUsers entry no disconnect will clear, locking the
// account out of its own next login until the entry ages out.
func TestBindUDPSessionSkipsAlreadyClosedConnection(t *testing.T) {
	g := newTestBareGateway(t)
	connID := g.connMgr.AddTransport(&clientFrameTransport{}, "")
	p := &Process{Log: logger.New(), ConnMgr: g.connMgr, gateway: g}

	// The listener saw a Disconnect and unregistered before the bind ran.
	g.connMgr.Unregister(connID)

	p.bindUDPSession(connID, uuid.New().String(), "tester")

	if st, ok := g.authStateOf(connID); ok && st.authenticated {
		t.Fatalf("bound a player to a connection that was already gone: %+v", st)
	}
	if sess := g.lookupSession(connID); sess != nil {
		t.Fatal("a localSession was created for a closed connection")
	}
}
