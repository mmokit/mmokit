package universe

import (
	"bytes"
	"testing"
	"time"
)

// TestAdminTopicForwarding_HostToCoord proves the full host→coord path: a
// remote-host process ships an AdminTopicEvent over the real MeshControl
// stream and the coordinator's OnRemoteAdminTopic callback receives topic +
// payload verbatim. Uses the distributed fixture (separate host-role
// processes over gRPC), the same harness as the S6/S7 capstones.
func TestAdminTopicForwarding_HostToCoord(t *testing.T) {
	dfx := newDistributedFixture(t, FixtureConfig{
		CellsX:  2,
		CellsY:  1,
		HostIDs: []string{"h1", "h2"},
	}).(*distributedFixture)
	coord := dfx.Coord()

	type evt struct {
		topic   string
		payload []byte
	}
	got := make(chan evt, 4)
	coord.OnRemoteAdminTopic(func(topic string, payload []byte) {
		select {
		case got <- evt{topic, payload}:
		default:
		}
	})

	host := dfx.hosts["h1"]
	if !host.ForwardsAdminTopics() {
		t.Fatal("remote host-role process must report ForwardsAdminTopics() == true")
	}
	if coord.ForwardsAdminTopics() {
		t.Fatal("coordinator process must report ForwardsAdminTopics() == false")
	}

	want := []byte(`{"system":"wave","rows":[{"Field":"Amplitude","Value":"42"}]}`)

	// Re-send until delivered: the control stream is up once the fixture
	// returns, but sendIfReady legitimately drops during any reconnect blip,
	// so a single-shot send would be flaky by design.
	deadline := time.After(10 * time.Second)
	for {
		if err := host.ForwardAdminTopic("tunables", want); err != nil {
			t.Logf("ForwardAdminTopic (will retry): %v", err)
		}
		select {
		case e := <-got:
			if e.topic != "tunables" {
				t.Errorf("topic = %q, want %q", e.topic, "tunables")
			}
			if !bytes.Equal(e.payload, want) {
				t.Errorf("payload = %s, want %s", e.payload, want)
			}
			return
		case <-time.After(200 * time.Millisecond):
			// not yet — send again
		case <-deadline:
			t.Fatal("AdminTopicEvent never reached the coordinator callback")
		}
	}
}
