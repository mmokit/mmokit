package universe

import (
	"context"
	"testing"
	"time"

	"github.com/zenion/mmokit/pkg/logger"
	"github.com/zenion/mmokit/pkg/net"
)

// The admission guard added in CE-006 criterion 9 can fail in two directions.
// Rejecting too little leaves the flap it exists to stop. Rejecting too much
// is worse, and worse *quietly*: nothing tears a locked-out host's cells down,
// so it keeps simulating while losing the coordinator notifications that keep
// players routed, and a locked-out gateway silently converts every reconnect
// into a fresh spawn.
//
// These two tests exist so that second direction fails loudly instead.

// TestAdmissionE2E_KilledHostRejoinsWithSameID is the end-to-end form of the
// killClosed term. The `host kill` console verb closes the incumbent's kill
// channel but leaves its registry entry fresh and its map entry present, and
// the host redials ~200ms later — well inside deadThreshold. A guard that
// looked only at map presence plus heartbeat freshness would lock it out for
// 3s, contradicting the verb's own documented contract.
func TestAdmissionE2E_KilledHostRejoinsWithSameID(t *testing.T) {
	fx := newDistributedFixture(t, FixtureConfig{
		CellsX:  2,
		CellsY:  2,
		HostIDs: []string{"host-a", "host-b"},
	})

	before := fx.Coord().hostRegistry.Get("host-a")
	if before == nil {
		t.Fatal("host-a never registered")
	}

	// Operator kills the stream. The registry entry survives, still fresh.
	if !fx.Coord().controlServer.cancelStream("host-a") {
		t.Fatal("cancelStream found no stream for host-a")
	}

	// The host's own reconnect loop must be readmitted. Poll rather than
	// sleeping a fixed interval: the claim is that it comes back, not that
	// it is back at some particular instant.
	deadline := time.Now().Add(5 * time.Second)
	for {
		rh := fx.Coord().hostRegistry.Get("host-a")
		if rh != nil && rh.State != RemoteHostDead && time.Since(rh.LastHeartbeat) < deadThreshold {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a killed host was not readmitted within 5s — admission is locking out `host kill`")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// And the other host must be untouched by any of it.
	if rh := fx.Coord().hostRegistry.Get("host-b"); rh == nil || rh.State == RemoteHostDead {
		t.Fatal("host-b was collateral damage of host-a's reconnect")
	}
}

// TestAdmissionE2E_RestartedHostReplacesItsAddress pins the half of criterion
// 9 that was originally worded backwards ("cannot overwrite its GrpcAddr").
//
// Every production NewHostNetwork binds ":0", so a restarted host advertises a
// NEW ephemeral port. Freezing GrpcAddr would leave the coordinator handing
// peers a dead address forever, and the failure would look like a payload-plane
// bug rather than a registry one: the control stream stays perfectly healthy.
func TestAdmissionE2E_RestartedHostReplacesItsAddress(t *testing.T) {
	fx := newDistributedFixture(t, FixtureConfig{
		CellsX:  2,
		CellsY:  2,
		HostIDs: []string{"host-a"},
	})

	first := fx.Coord().hostRegistry.Get("host-a")
	if first == nil {
		t.Fatal("host-a never registered")
	}
	firstAddr := first.GrpcAddr
	if firstAddr == "" {
		t.Fatal("host-a registered without a GrpcAddr")
	}

	// Take the original host down and stand a replacement up under the same
	// ID, exactly as a process restart would.
	if err := fx.StopHost(context.Background(), "host-a"); err != nil {
		t.Fatalf("StopHost: %v", err)
	}

	replacement := New(Config{
		CellsX:              2,
		CellsY:              2,
		Mode:                "host",
		CoordinatorAddr:     fx.Coord().controlListener.Addr().String(),
		HostID:              "host-a",
		Headless:            true,
		ShutdownGracePeriod: 50 * time.Millisecond,
		ConnManager:         net.NewConnManager(),
		Logger:              logger.New(),
	})
	replacement.Build()
	t.Cleanup(replacement.Shutdown)

	deadline := time.Now().Add(10 * time.Second)
	for {
		rh := fx.Coord().hostRegistry.Get("host-a")
		if rh != nil && rh.GrpcAddr != "" && rh.GrpcAddr != firstAddr {
			return // address refreshed: the restart was admitted and re-advertised
		}
		if time.Now().After(deadline) {
			got := ""
			if rh := fx.Coord().hostRegistry.Get("host-a"); rh != nil {
				got = rh.GrpcAddr
			}
			t.Fatalf("restarted host never refreshed its address (still %q, was %q) — "+
				"either admission locked it out or GrpcAddr was frozen", got, firstAddr)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
