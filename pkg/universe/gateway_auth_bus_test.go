package universe

import (
	"testing"

	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/service"
)

// TestGateway_SubscribeToAuthEvents_PopulatesAuthState exercises the bus
// subscriber path that Task 5 will wire in Build. The test bypasses the
// coord/host plumbing — we only want to prove the subscribe handler
// updates authStates[connID] correctly.
//
// onAuthSuccess fans out to dispatchPostAuthAssignment, which terminates
// at the "no cell for user" early-return when the bare gateway's
// topology snapshot is empty and coord is nil — so the dispatch path is
// a no-op for the purposes of this test, but authStates is set inline
// before that fan-out fires.
func TestGateway_SubscribeToAuthEvents_PopulatesAuthState(t *testing.T) {
	g := newTestBareGateway(t)
	bus := service.NewBus("test-proc")
	g.subscribeToAuthEvents(bus)

	uid := uuid.New()
	service.Publish(bus, service.AuthLoginSucceededEvent{
		ConnID:       42,
		UserID:       uid.String(),
		Username:     "alice",
		SessionToken: "tok-xyz",
		ExpiresAtMs:  9_000_000,
		GatewayID:    g.id,
	})

	g.authMu.Lock()
	state, ok := g.authStates[42]
	g.authMu.Unlock()
	if !ok {
		t.Fatal("authStates[42] not populated by subscribe handler")
	}
	if state.username != "alice" || state.sessionToken != "tok-xyz" {
		t.Fatalf("authStates content wrong: %+v", state)
	}
	if state.expiresAtMs != 9_000_000 {
		t.Fatalf("authStates expiresAtMs=%d, want 9000000", state.expiresAtMs)
	}
	if state.userID != uid {
		t.Fatalf("authStates userID=%v, want %v", state.userID, uid)
	}
	if !state.authenticated {
		t.Fatal("authStates.authenticated should be true")
	}

	service.Publish(bus, service.AuthLogoutEvent{ConnID: 42, GatewayID: g.id})
	g.authMu.Lock()
	_, stillThere := g.authStates[42]
	g.authMu.Unlock()
	if stillThere {
		t.Fatal("authStates[42] not cleared by AuthLogoutEvent subscriber")
	}
}

// TestGateway_SubscribeToAuthEvents_NilBus pins the defensive early-return
// in subscribeToAuthEvents — Phase 1 fixture tests bypass Process.New, so
// a nil bus must be tolerated without panicking.
func TestGateway_SubscribeToAuthEvents_NilBus(t *testing.T) {
	g := newTestBareGateway(t)
	g.subscribeToAuthEvents(nil) // must not panic
}

// TestGateway_SubscribeToAuthEvents_BadUserID checks that a malformed
// UserID is logged-and-dropped rather than panicking the subscriber
// goroutine (or in this case, the publisher's goroutine — Phase 1 dispatch
// is synchronous inline).
func TestGateway_SubscribeToAuthEvents_BadUserID(t *testing.T) {
	g := newTestBareGateway(t)
	bus := service.NewBus("test-proc")
	g.subscribeToAuthEvents(bus)

	service.Publish(bus, service.AuthLoginSucceededEvent{
		ConnID:   7,
		UserID:   "not-a-uuid",
		Username: "bogus",
	})

	g.authMu.Lock()
	_, present := g.authStates[7]
	g.authMu.Unlock()
	if present {
		t.Fatal("authStates[7] should not be populated when UserID parse fails")
	}
}

// newTestBareGateway returns a minimal Gateway with just enough fields to
// run the auth subscriber. The empty topology + nil coord drive
// dispatchPostAuthAssignment into its "no cell for user" early-return,
// which is fine — we only care that authStates[connID] is set inline
// before that fan-out fires.
func newTestBareGateway(t *testing.T) *Gateway {
	t.Helper()
	return &Gateway{
		id:         "test-gw",
		log:        logger.New(),
		connMgr:    net.NewConnManager(),
		authStates: map[uint32]connAuthState{},
		sessions:   map[uint32]*localSession{},
		topology:   &cachedTopology{},
	}
}
