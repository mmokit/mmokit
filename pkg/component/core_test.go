package component_test

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/component"
)

// TestReplicaComponent_HasProducedAtMsField is a sentinel: it fails at
// compile time if ProducedAtMs is renamed or removed from the Replica
// struct. Real end-to-end exercises of the field land in Phase E2 (
// producer stamps outbound frames from ClusterClock.TickTime) and Phase
// F1 (consumer copies the stamp into Replica on inbound border frames).
func TestReplicaComponent_HasProducedAtMsField(t *testing.T) {
	r := &component.Replica{ProducedAtMs: 12345}
	if r.ProducedAtMs != 12345 {
		t.Fatalf("ProducedAtMs not set: got %d", r.ProducedAtMs)
	}
}
