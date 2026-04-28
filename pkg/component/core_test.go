package component_test

import (
	"reflect"
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

func TestDebugInfo_Fields(t *testing.T) {
	d := component.DebugInfo{Presence: 1, OwnerHost: 7, AoIRadius: 800}
	if d.Presence != 1 {
		t.Errorf("Presence: got %d, want 1", d.Presence)
	}
	if d.OwnerHost != 7 {
		t.Errorf("OwnerHost: got %d, want 7", d.OwnerHost)
	}
	if d.AoIRadius != 800 {
		t.Errorf("AoIRadius: got %v, want 800", d.AoIRadius)
	}
}

func TestDebugInfo_NetTags(t *testing.T) {
	tp := reflect.TypeOf(component.DebugInfo{})
	cases := []struct {
		field, want string
	}{
		{"Presence", "u8"},
		{"OwnerHost", "u8"},
		{"AoIRadius", "f32"},
	}
	for _, c := range cases {
		f, ok := tp.FieldByName(c.field)
		if !ok {
			t.Fatalf("DebugInfo missing field %s", c.field)
		}
		if got := f.Tag.Get("net"); got != c.want {
			t.Errorf("DebugInfo.%s net tag: got %q, want %q", c.field, got, c.want)
		}
	}
}
