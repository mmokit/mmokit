package game

import (
	"testing"

	"github.com/zenion/mmokit/pkg/mmokit"
)

func TestNetworkSystemDeadSessionRemainsViewer(t *testing.T) {
	gw, cm := newTestGameWorld()
	transport := &captureTransport{}
	connID := cm.AddTransport(transport, "")
	<-cm.Events()

	gw.Players.RegisterPlayer(connID, "dead-viewer")
	sess := gw.Players.ByConnID(connID)
	sess.UserID = testUserID(sess.Username)
	gw.SpawnPlayer(sess)
	sess.State = StateDead
	entity := mmokit.EntityFromECS(gw.stage, sess.Entity)
	mmokit.Set(entity, mmokit.Dormant{})

	network := &NetworkSystem{}
	mmokit.WireSystem(network, gw.stage.ECSWorld(), gw.eng, gw.stage)
	before := len(transport.AllSent())
	network.Update(0.05)
	if got := len(transport.AllSent()); got <= before {
		t.Fatalf("dead connected session sent %d frames, want more than the %d bootstrap frames", got, before)
	}
}
