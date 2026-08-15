// integration_killcredit_test.go — cross-cell integration test for
// serverOnly typed messages: when a typed message is Sent to a killer
// that is a replica on the sending cell, mmokit routes it to the
// killer's authoritative cell and the registered handler runs there.
package facadetest

import (
	"testing"
	"time"

	"github.com/mmokit/mmokit"
)

// killCreditMsg stands in for internal/game.KillCredit — same shape and
// routing semantics, declared inside this test package so we don't import
// internal/game from mmokit (a layering violation). Server-internal
// status comes from the registration verb (HandleAllInternal in
// production); this test uses Handle directly because it operates at
// stage scope, and stage-scoped Handle never touches the broadcast
// registry — the server-internal property is preserved.
type killCreditMsg struct {
	Currency uint32
	Amount   int64
}

func TestIntegration_KillCredit_CrossCell(t *testing.T) {
	cellA, cellB, drain := newTwoCellLoopback(t)

	var receivedAt string
	var receivedAmount int64
	mmokit.Handle(cellB, func(_ mmokit.Entity, msg *killCreditMsg) {
		receivedAt = "B"
		receivedAmount = msg.Amount
	})
	// Also register on A so a misroute would surface immediately.
	mmokit.Handle(cellA, func(_ mmokit.Entity, _ *killCreditMsg) {
		receivedAt = "A"
	})

	// Killer's authoritative cell is B. Spawn the killer on B; push a
	// border replica of the killer to A (modeling the AoI / spatial
	// overlap that makes the killer visible to the dying entity on A).
	killerNetID := uint32(101)
	spawnTestEntity(t, cellB, killerNetID)
	pushBorderReplicaTo(t, cellB, cellA, killerNetID)

	// From cell A's perspective, the killer is a replica. Send routes
	// back to B via the loopback bridge.
	killerOnA := mmokit.EntityByNetID(cellA, killerNetID)
	killerOnA.Send(&killCreditMsg{Currency: 1, Amount: 50})

	drain(50 * time.Millisecond)

	if receivedAt != "B" || receivedAmount != 50 {
		t.Fatalf("KillCredit didn't route to authoritative cell: at=%q amount=%d", receivedAt, receivedAmount)
	}
}
